package state

import (
	"testing"

	"prism/internal/keys"
	"prism/internal/model"
	"prism/internal/usage"
)

func TestDurableUsageBeforeServiceAndCrashRecovery(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "user", "journal-user")
	sub := model.UserSubscription{ID: "sub", UserID: "user", PlanSnapshotJSON: `{}`, PeriodStartNs: 10, Status: model.SubscriptionStatusActive}
	if err := repo.UpsertUserSubscription(sub); err != nil {
		t.Fatal(err)
	}
	counters := usage.NewCounters()
	counters.SeedFromSubscriptions([]model.UserSubscription{sub}, 0)
	journal, err := usage.NewJournal(repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	gate := &usage.Gate{Limiter: usage.NewLimiter(counters), Journal: journal}
	key := &keys.KeyState{KeyID: "key", UserID: "user", Status: "active", UserStatus: "active", Plan: keys.PlanSnapshot{TrafficBytes: 100}}
	n, finish := gate.ReserveTraffic(key, "platform", true, 100)
	pending, err := repo.PendingUsage()
	if err != nil || n != 100 || len(pending) != 1 || pending[0].Bytes != 100 {
		t.Fatal("service allowed without durable liability")
	}
	// A new runtime is refused even though period_used_bytes is still zero.
	recovered, err := usage.NewJournal(repo, nil)
	if err != nil || !recovered.Blocked("user") {
		t.Fatal("crash liability reopened access")
	}
	id := pending[0].ID
	finish(73)
	finish(73)
	pending, err = repo.PendingUsage()
	if err != nil || len(pending) != 0 {
		t.Fatal("settlement did not remove reservation")
	}
	var actual, used int64
	if err := repo.db.QueryRow(`SELECT sum(ingress_bytes+egress_bytes) FROM usage_hourly`).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if err := repo.db.QueryRow(`SELECT period_used_bytes FROM user_subscriptions WHERE id='sub'`).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if actual != 73 || used != 73 || counters.UserBytes("user") != 73 {
		t.Fatalf("actual=%d used=%d memory=%d", actual, used, counters.UserBytes("user"))
	}
	if _, err := repo.SettleUsage(id, 0); err != nil {
		t.Fatal(err)
	}
	flusher := usage.Flusher{Counters: counters, Store: repo}
	if _, err := flusher.FlushOnce(0); err != nil {
		t.Fatal(err)
	}
	if err := repo.db.QueryRow(`SELECT sum(ingress_bytes+egress_bytes) FROM usage_hourly`).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != 73 {
		t.Fatal("asynchronous flush double-debited settled bytes")
	}
}

func TestDurableUsageLateSettlementRetainsHistoryOnly(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "user", "late-user")
	sub := model.UserSubscription{ID: "sub", UserID: "user", PlanSnapshotJSON: `{}`, PeriodStartNs: 10, Status: model.SubscriptionStatusActive}
	if err := repo.UpsertUserSubscription(sub); err != nil {
		t.Fatal(err)
	}
	v := model.UsageReservation{ID: "reservation", KeyID: "key", UserID: "user", SubscriptionID: "sub", PeriodStartNs: 10, Bytes: 100, Ingress: true}
	if err := repo.ReserveUsage(v); err != nil {
		t.Fatal(err)
	}
	if err := repo.ResetSubscriptionPeriod("sub", 10, 20); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := repo.SettleUsage(v.ID, 81); err != nil {
			t.Fatal(err)
		}
	}
	var actual, used int64
	if err := repo.db.QueryRow(`SELECT sum(ingress_bytes+egress_bytes) FROM usage_hourly`).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if err := repo.db.QueryRow(`SELECT period_used_bytes FROM user_subscriptions WHERE id='sub'`).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if actual != 81 || used != 0 {
		t.Fatalf("history=%d new period=%d", actual, used)
	}
}

func TestDurableUsageRefusesFailedStorageAndRetriesSettlement(t *testing.T) {
	repo := newTestStateRepo(t)
	counters := usage.NewCounters()
	journal, err := usage.NewJournal(repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	gate := &usage.Gate{Limiter: usage.NewLimiter(counters), Journal: journal}
	key := &keys.KeyState{KeyID: "key", UserID: "user", Plan: keys.PlanSnapshot{TrafficBytes: 100}}
	if _, err := repo.db.Exec(`CREATE TRIGGER test_full BEFORE INSERT ON usage_reservations BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	n, _ := gate.ReserveTraffic(key, "", true, 100)
	if n != 0 || !journal.Blocked("user") {
		t.Fatal("failed durable write granted service")
	}
}

func TestDurableSettlementChargesOriginalPeriodAfterSuspension(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "user", "suspended-user")
	sub := model.UserSubscription{ID: "sub", UserID: "user", PlanSnapshotJSON: `{}`, PeriodStartNs: 10, Status: model.SubscriptionStatusActive}
	if err := repo.UpsertUserSubscription(sub); err != nil {
		t.Fatal(err)
	}
	reservation := model.UsageReservation{ID: "in-flight", KeyID: "key", UserID: "user", SubscriptionID: sub.ID, PeriodStartNs: 10, Bytes: 100, Ingress: true}
	if err := repo.ReserveUsage(reservation); err != nil {
		t.Fatal(err)
	}
	// The bytes were admitted before suspension; changing status cannot erase
	// their liability if the exact subscription period still exists.
	sub.Status = model.SubscriptionStatusSuspended
	if err := repo.UpsertUserSubscription(sub); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SettleUsage(reservation.ID, 73); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetUserSubscription(sub.ID)
	if err != nil || got.PeriodUsedBytes != 73 {
		t.Fatal("suspending an in-flight write erased its original-period liability")
	}
}
