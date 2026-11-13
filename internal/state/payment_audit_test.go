package state

import (
	"errors"
	"testing"

	"prism/internal/model"
	"prism/internal/usage"
)

func TestRequestOnlyUsageDoesNotPoisonOtherTenantsFlush(t *testing.T) {
	repo := newTestStateRepo(t)
	for _, id := range []string{"request-only", "traffic"} {
		mustInsertUser(t, repo, id, id)
		if err := repo.UpsertUserSubscription(model.UserSubscription{ID: id, UserID: id, PlanSnapshotJSON: `{}`, Status: model.SubscriptionStatusActive, PeriodStartNs: 20}); err != nil {
			t.Fatal(err)
		}
	}
	c := usage.NewCounters()
	c.BeginUserPeriodTransition("request-only").Commit("request-only", 10)
	c.Add(usage.CounterKey{KeyID: "empty", UserID: "request-only"}, usage.Delta{Requests: 1, Errors: 1})
	c.BeginUserPeriodTransition("request-only").Commit("request-only", 20)
	c.Add(usage.CounterKey{KeyID: "data", UserID: "traffic"}, usage.Delta{IngressBytes: 123, Requests: 1})
	f := usage.Flusher{Counters: c, Store: repo}
	result, err := f.FlushOnce(100)
	if err != nil || result.Rows != 2 || result.Bytes != 123 {
		t.Fatalf("flush=%+v err=%v", result, err)
	}
	sub, err := repo.GetActiveUserSubscription("traffic")
	if err != nil || sub.PeriodUsedBytes != 123 {
		t.Fatalf("subscription=%+v err=%v", sub, err)
	}
	var requests, failures int64
	if err := repo.db.QueryRow(`SELECT requests,errors FROM usage_hourly WHERE key_id='empty'`).Scan(&requests, &failures); err != nil || requests != 1 || failures != 1 {
		t.Fatalf("request-only usage lost: %d/%d %v", requests, failures, err)
	}
	if _, err := f.FlushOnce(200); err != nil {
		t.Fatal(err)
	}
}

func TestStatusAndFactorRollbackWhenSessionRevocationFails(t *testing.T) {
	for _, kind := range []string{"status", "factor", "patch"} {
		t.Run(kind, func(t *testing.T) {
			r := newTestStateRepo(t)
			u := mustInsertUser(t, r, "account", "account")
			if err := r.UpdateUserTOTPSecret(u.ID, []byte("factor"), 1); err != nil {
				t.Fatal(err)
			}
			u2, err := r.GetUser(u.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.InsertSession(model.Session{IDHash: "session", UserID: u.ID, ExpiresAtNs: 100}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.db.Exec(`CREATE TRIGGER refuse_session_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT,'injected storage failure'); END`); err != nil {
				t.Fatal(err)
			}
			status := model.UserStatusDisabled
			switch kind {
			case "status":
				err = r.UpdateUserStatus(u.ID, status, 2)
			case "factor":
				err = r.UpdateVerifiedTOTP(*u2, nil, 2)
			case "patch":
				password := "changed"
				err = r.UpdateUserSecurityPatch(u.ID, &status, nil, &password, 2)
			}
			if err == nil {
				t.Fatal("revocation failure was ignored")
			}
			after, err := r.GetUser(u.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != u2.Status || after.PasswordHash != u2.PasswordHash || string(after.TOTPSecretEnc) != "factor" {
				t.Fatal("partial credential update escaped rollback")
			}
		})
	}
}

func TestVerifiedFactorRemovalRejectsStalePasswordAndRevokesSessions(t *testing.T) {
	r := newTestStateRepo(t)
	u := mustInsertUser(t, r, "account", "account")
	if err := r.UpdateUserTOTPSecret(u.ID, []byte("factor"), 1); err != nil {
		t.Fatal(err)
	}
	verified, err := r.GetUser(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateUserPassword(u.ID, "replacement", 2); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateVerifiedTOTP(*verified, nil, 3); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale verification accepted: %v", err)
	}
	current, err := r.GetUser(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.InsertSession(model.Session{IDHash: "new-session", UserID: u.ID, ExpiresAtNs: 100}); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateVerifiedTOTP(*current, nil, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetSession("new-session"); !errors.Is(err, ErrNotFound) {
		t.Fatal("factor removal retained a session")
	}
}

func TestChainScanResumesAndRetiresReconciledOrderWindows(t *testing.T) {
	r := newTestStateRepo(t)
	mustInsertUser(t, r, "chain", "chain")
	d := newDepositIntent("old-order", "chain", 0, 1_000_000)
	d.Address = "recipient"
	d.Contract = "contract"
	if _, err := r.InsertDepositIntent(d, []int64{1_000_001}, 1_000_000); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveChainScan("stream", 1, 4_000_000, "next-page"); err != nil {
		t.Fatal(err)
	}
	since, until, next, err := r.ChainScan("stream")
	if err != nil || since != 1 || until != 4_000_000 || next != "next-page" {
		t.Fatal("pagination state was not durable")
	}
	target := model.DepositTarget{Currency: "usdt", Address: "recipient", Contract: "contract"}
	if err := r.CompleteChainScan("stream", target, since, until, 4_000_000_000_000); err != nil {
		t.Fatal(err)
	}
	targets, err := r.DepositScanTargets()
	if err != nil || len(targets) != 0 {
		t.Fatalf("abandoned order still rescanned: %+v %v", targets, err)
	}
	_, until, _, err = r.ChainScan("stream")
	if err != nil || until != 0 {
		t.Fatal("completed pagination state survived")
	}
}

func TestPaymentDowngradeRefusesPendingConvertedLDC(t *testing.T) {
	r := newTestStateRepo(t)
	if err := r.InsertLDCDeposit(model.LDCDeposit{OutTradeNo: "converted", UserID: "user", AmountMicro: 3_125_000, PaymentMicro: 12_500_000, Rate: 0.25, Status: model.LDCDepositStatusPending}); err != nil {
		t.Fatal(err)
	}
	down, err := migrationsFS.ReadFile(stateMigrationsPath + "/000030_payment_settings.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(string(down)); err == nil {
		t.Fatal("downgrade silently discarded pending payment terms")
	}
}
