package state

import (
	"errors"
	"testing"

	"prism/internal/model"
)

func purchaseStateFixture(t *testing.T, autoRenew bool) (*StateRepo, model.UserSubscription) {
	t.Helper()
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-purchase", "purchase-user")
	if err := repo.UpsertPlan(model.Plan{ID: "p-purchase", Name: "Purchase", PriceMicro: 1_000_000, Status: model.KeyStatusActive, CreatedAtNs: 1, UpdatedAtNs: 1}); err != nil {
		t.Fatalf("UpsertPlan: %v", err)
	}
	sub := model.UserSubscription{
		ID: "s-purchase", UserID: "u-purchase", PlanID: "p-purchase",
		PlanSnapshotJSON: `{}`, StartsAtNs: 1, ExpiresAtNs: 2_000,
		PeriodStartNs: 1, AutoRenew: autoRenew, Status: model.SubscriptionStatusActive,
	}
	if err := repo.UpsertUserSubscription(sub); err != nil {
		t.Fatalf("UpsertUserSubscription: %v", err)
	}
	if _, err := repo.ApplyBalanceDelta(model.BalanceEntry{
		ID: "bl-purchase-seed", UserID: sub.UserID, DeltaMicro: 2_000_000,
		Reason: model.BalanceReasonAdminAdjust, CreatedAtNs: 1,
	}); err != nil {
		t.Fatalf("seed balance: %v", err)
	}
	return repo, sub
}

func renewalEntry() model.BalanceEntry {
	return model.BalanceEntry{
		ID: "bl-renew", UserID: "u-purchase", DeltaMicro: -1_000_000,
		Reason: model.BalanceReasonRenew, RefID: "s-purchase", RequestID: "renew-test",
		CreatedAtNs: 2,
	}
}

func TestStateRepo_AutoRenewChecksOptOutInTheChargeTransaction(t *testing.T) {
	repo, sub := purchaseStateFixture(t, true)
	if err := repo.SetSubscriptionAutoRenew(sub.ID, false); err != nil {
		t.Fatalf("SetSubscriptionAutoRenew: %v", err)
	}
	updated := sub
	updated.ExpiresAtNs = 3_000
	updated.PeriodStartNs = 2_000
	updated.PeriodIndex = 1
	_, err := repo.RenewSubscriptionWithBalanceExpectedSnapshot(updated, sub.PeriodIndex, sub.PlanSnapshotJSON, renewalEntry(), true)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("renewal after opt-out = %v, want ErrConflict", err)
	}
	if balance, err := repo.GetBalance(sub.UserID); err != nil || balance != 2_000_000 {
		t.Fatalf("opt-out race changed balance to %d, %v", balance, err)
	}
}

func TestStateRepo_RenewalCannotRestoreAStoppedSnapshot(t *testing.T) {
	repo, sub := purchaseStateFixture(t, false)
	stopped := sub
	stopped.Status = model.SubscriptionStatusSuspended
	stopped.PlanSnapshotJSON = `{"status":"disabled"}`
	if err := repo.UpdateUserSubscriptionTerms(stopped, sub); err != nil {
		t.Fatalf("stop subscription: %v", err)
	}
	updated := sub
	updated.ExpiresAtNs = 3_000
	updated.PeriodStartNs = 2_000
	updated.PeriodIndex = 1
	_, err := repo.RenewSubscriptionWithBalanceExpectedSnapshot(updated, sub.PeriodIndex, sub.PlanSnapshotJSON, renewalEntry(), false)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("renewal after stop = %v, want ErrConflict", err)
	}
	if balance, err := repo.GetBalance(sub.UserID); err != nil || balance != 2_000_000 {
		t.Fatalf("stopped renewal changed balance to %d, %v", balance, err)
	}
}

func TestStateRepo_StaleTermsCannotEraseACommittedRenewal(t *testing.T) {
	repo, sub := purchaseStateFixture(t, false)
	updated := sub
	updated.ExpiresAtNs = 3_000
	updated.PeriodStartNs = 2_000
	updated.PeriodIndex = 1
	if _, err := repo.RenewSubscriptionWithBalance(updated, sub.PeriodIndex, renewalEntry()); err != nil {
		t.Fatalf("renewal: %v", err)
	}
	stale := sub
	stale.Status = model.SubscriptionStatusSuspended
	if err := repo.UpdateUserSubscriptionTerms(stale, sub); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale terms write = %v, want ErrConflict", err)
	}
	current, err := repo.GetUserSubscription(sub.ID)
	if err != nil {
		t.Fatalf("read renewed subscription: %v", err)
	}
	if current.PeriodIndex != 1 || current.Status != model.SubscriptionStatusActive || current.ExpiresAtNs != 3_000 {
		t.Fatalf("stale terms changed committed renewal: %+v", current)
	}
}
