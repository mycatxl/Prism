package state

import (
	"errors"
	"testing"
	"time"

	"prism/internal/model"
)

// subscriptionWith inserts a subscription through the repository, so every row
// in these tests has been through the same validation a real one has.
func subscriptionWith(t *testing.T, repo *StateRepo, id, userID string, status string, expiresAtNs int64) {
	t.Helper()
	if err := repo.UpsertUserSubscription(model.UserSubscription{
		ID: id, UserID: userID, PlanSnapshotJSON: `{}`,
		StartsAtNs: expiresAtNs - int64(24*time.Hour), ExpiresAtNs: expiresAtNs,
		PeriodStartNs: expiresAtNs - int64(24*time.Hour), PeriodUsedBytes: 4096,
		Status: status,
	}); err != nil {
		t.Fatalf("UpsertUserSubscription(%s): %v", id, err)
	}
}

func subscriptionStatus(t *testing.T, repo *StateRepo, id string) string {
	t.Helper()
	sub, err := repo.GetUserSubscription(id)
	if err != nil {
		t.Fatalf("GetUserSubscription(%s): %v", id, err)
	}
	return sub.Status
}

// The sweep moves exactly the rows whose window has closed, and leaves every
// other row alone: one still inside its window, one that never expires, and one
// that is already out of force for a different reason.
//
// It is idempotent, which is what lets a ticker call it every few minutes: the
// second pass over the same table has nothing left to match.
func TestExpireStaleSubscriptions_ClosesOutThePastWindowAndNothingElse(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	mustInsertUser(t, repo, "u-dead", "dead")
	mustInsertUser(t, repo, "u-edge", "edge")
	mustInsertUser(t, repo, "u-live", "live")
	mustInsertUser(t, repo, "u-never", "never")
	mustInsertUser(t, repo, "u-off", "off")

	subscriptionWith(t, repo, "s-dead", "u-dead", model.SubscriptionStatusActive, now-int64(time.Hour))
	// The readers expire a window that ends exactly at now (nowNs >=
	// expires_at_ns), and the sweep has to agree with them or a row would keep
	// read as active while every access decision already refuses it.
	subscriptionWith(t, repo, "s-edge", "u-edge", model.SubscriptionStatusActive, now)
	subscriptionWith(t, repo, "s-live", "u-live", model.SubscriptionStatusActive, now+int64(time.Hour))
	// 0 is the "never expires" sentinel 000021 writes for the accounts that
	// predate plans, not a date in 1970. Sweeping it would end access that was
	// granted as unlimited.
	subscriptionWith(t, repo, "s-never", "u-never", model.SubscriptionStatusActive, 0)
	// Already out of force: the status predicate only ever touches 'active' rows,
	// so a suspended subscription is not rewritten and its history stays as it is.
	subscriptionWith(t, repo, "s-off", "u-off", model.SubscriptionStatusSuspended, now-int64(time.Hour))

	expired, err := repo.ExpireStaleSubscriptions(now)
	if err != nil {
		t.Fatalf("ExpireStaleSubscriptions: %v", err)
	}
	if expired != 2 {
		t.Fatalf("ExpireStaleSubscriptions expired %d row(s), want 2 (s-dead and the window that ends at now)", expired)
	}

	for _, tc := range []struct {
		id   string
		want string
	}{
		{"s-dead", model.SubscriptionStatusExpired},
		{"s-edge", model.SubscriptionStatusExpired},
		{"s-live", model.SubscriptionStatusActive},
		{"s-never", model.SubscriptionStatusActive},
		{"s-off", model.SubscriptionStatusSuspended},
	} {
		if got := subscriptionStatus(t, repo, tc.id); got != tc.want {
			t.Errorf("status of %s after the sweep: got %q, want %q", tc.id, got, tc.want)
		}
	}

	// The row is history with its window and its counter untouched: closing a
	// subscription out is a status change, not a rewrite of what happened.
	stored, err := repo.GetUserSubscription("s-dead")
	if err != nil {
		t.Fatalf("GetUserSubscription(s-dead): %v", err)
	}
	if stored.ExpiresAtNs != now-int64(time.Hour) {
		t.Errorf("expires_at_ns of s-dead after the sweep: got %d, want %d (untouched)", stored.ExpiresAtNs, now-int64(time.Hour))
	}
	if stored.PeriodUsedBytes != 4096 {
		t.Errorf("period_used_bytes of s-dead after the sweep: got %d, want 4096 (untouched)", stored.PeriodUsedBytes)
	}

	// The read the sweep exists for: an expired subscription is no longer part of
	// "who is on a plan right now".
	rows, err := repo.ListActiveUserSubscriptions()
	if err != nil {
		t.Fatalf("ListActiveUserSubscriptions: %v", err)
	}
	for _, s := range rows {
		if s.ID == "s-dead" || s.ID == "s-edge" {
			t.Errorf("%s is still listed as active: the sweep did not close it out of the status-only reads", s.ID)
		}
	}

	again, err := repo.ExpireStaleSubscriptions(now)
	if err != nil {
		t.Fatalf("ExpireStaleSubscriptions (second pass): %v", err)
	}
	if again != 0 {
		t.Fatalf("the second sweep expired %d row(s), want 0: the sweep is not idempotent", again)
	}
}

// Closing a subscription out releases the user's slot in the partial unique
// index ux_user_subscriptions_active (000020), which is what lets the console
// put the same user on a new plan. Before the sweep the insert is a conflict,
// which is the state a hand-rolled expiry check would have left behind forever.
func TestExpireStaleSubscriptions_FreesTheActiveSlotForANewSubscription(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()
	mustInsertUser(t, repo, "u-1", "alice")

	subscriptionWith(t, repo, "s-old", "u-1", model.SubscriptionStatusActive, now-int64(time.Minute))
	// The window has closed but the row still holds the only active slot.
	err := repo.UpsertUserSubscription(model.UserSubscription{
		ID: "s-new", UserID: "u-1", PlanSnapshotJSON: `{}`,
		StartsAtNs: now, ExpiresAtNs: now + int64(24*time.Hour),
		PeriodStartNs: now, Status: model.SubscriptionStatusActive,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("assigning a new subscription before the sweep: got %v, want ErrConflict", err)
	}

	if n, err := repo.ExpireStaleSubscriptions(now); err != nil || n != 1 {
		t.Fatalf("ExpireStaleSubscriptions = (%d, %v), want (1, nil)", n, err)
	}

	err = repo.UpsertUserSubscription(model.UserSubscription{
		ID: "s-new", UserID: "u-1", PlanSnapshotJSON: `{}`,
		StartsAtNs: now, ExpiresAtNs: now + int64(24*time.Hour),
		PeriodStartNs: now, Status: model.SubscriptionStatusActive,
	})
	if err != nil {
		t.Fatalf("assigning a new subscription after the sweep: %v", err)
	}

	stored, err := repo.GetUserSubscription("s-old")
	if err != nil {
		t.Fatalf("the expired row must stay on the account: %v", err)
	}
	if stored.Status != model.SubscriptionStatusExpired {
		t.Errorf("status of s-old: got %q, want %q", stored.Status, model.SubscriptionStatusExpired)
	}
}
