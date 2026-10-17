package service

import (
	"errors"
	"testing"

	"prism/internal/model"
	"prism/internal/state"
)

// The ledger's uniqueness rules, exercised through the repository the service
// uses. It lives in this package because the test service helper already opens a
// migrated database, and the behaviour under test is the index 000025 changed.

func newLedgerTestRepo(t *testing.T) (*state.StateRepo, string) {
	t.Helper()
	cp, _, _, nowNs := newTenantTestService(t)
	return cp.Engine.StateRepo, newPurchaseTestUser(t, cp, nowNs)
}

// A ledger row's (reason, ref_id) is only unique for deposits: the same plan
// bought twice, the same subscription renewed twice and the same code redeemed
// twice are all ordinary.
func TestStateRepo_LedgerRefUniquenessIsDepositsOnly(t *testing.T) {
	repo, userID := newLedgerTestRepo(t)
	if _, err := repo.ApplyBalanceDelta(model.BalanceEntry{
		ID: "bl-seed", UserID: userID, DeltaMicro: 100,
		Reason: model.BalanceReasonAdminAdjust, Note: "seed", CreatedAtNs: 0,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for i, entry := range []model.BalanceEntry{
		{ID: "bl-1", UserID: userID, DeltaMicro: -1, Reason: model.BalanceReasonPurchase, RefID: "pl-1", CreatedAtNs: 1},
		{ID: "bl-2", UserID: userID, DeltaMicro: -1, Reason: model.BalanceReasonPurchase, RefID: "pl-1", CreatedAtNs: 2},
		{ID: "bl-3", UserID: userID, DeltaMicro: -1, Reason: model.BalanceReasonRenew, RefID: "us-1", CreatedAtNs: 3},
		{ID: "bl-4", UserID: userID, DeltaMicro: -1, Reason: model.BalanceReasonRenew, RefID: "us-1", CreatedAtNs: 4},
	} {
		if _, err := repo.ApplyBalanceDelta(entry); err != nil {
			t.Fatalf("entry %d (%s/%s) should be allowed: %v", i, entry.Reason, entry.RefID, err)
		}
	}

	// A deposit's ref_id is a transaction id: the second one is a duplicate.
	if _, err := repo.ApplyBalanceDelta(model.BalanceEntry{
		ID: "bl-5", UserID: userID, DeltaMicro: 1_000_000,
		Reason: model.BalanceReasonDeposit, RefID: "tx-1", CreatedAtNs: 5,
	}); err != nil {
		t.Fatalf("first deposit: %v", err)
	}
	_, err := repo.ApplyBalanceDelta(model.BalanceEntry{
		ID: "bl-6", UserID: userID, DeltaMicro: 1_000_000,
		Reason: model.BalanceReasonDeposit, RefID: "tx-1", CreatedAtNs: 6,
	})
	if !errors.Is(err, state.ErrConflict) {
		t.Fatalf("a repeated deposit must conflict: got %v", err)
	}
}

// The request_id rule still applies to every reason: a retried purchase is
// settled once whatever the ledger's ref index says.
func TestStateRepo_RequestIDIsUniqueAcrossReasons(t *testing.T) {
	repo, userID := newLedgerTestRepo(t)
	if _, err := repo.ApplyBalanceDelta(model.BalanceEntry{
		ID: "bl-seed", UserID: userID, DeltaMicro: 100,
		Reason: model.BalanceReasonAdminAdjust, Note: "seed", CreatedAtNs: 0,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := repo.ApplyBalanceDelta(model.BalanceEntry{
		ID: "bl-1", UserID: userID, DeltaMicro: -1,
		Reason: model.BalanceReasonPurchase, RequestID: "req-1", CreatedAtNs: 1,
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, err := repo.ApplyBalanceDelta(model.BalanceEntry{
		ID: "bl-2", UserID: userID, DeltaMicro: -1,
		Reason: model.BalanceReasonRenew, RequestID: "req-1", CreatedAtNs: 2,
	})
	if !errors.Is(err, state.ErrConflict) {
		t.Fatalf("a repeated request_id must conflict: got %v", err)
	}
}
