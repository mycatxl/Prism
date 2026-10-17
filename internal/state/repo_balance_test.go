package state

import (
	"errors"
	"testing"

	"prism/internal/model"
)

// The balance tables: what a movement does, and what stops a second one.

func openBalanceRepo(t *testing.T) *StateRepo {
	t.Helper()
	repo := newTestStateRepo(t)
	insertBalanceTestUser(t, repo, "u-1")
	return repo
}

func insertBalanceTestUser(t *testing.T, repo *StateRepo, id string) {
	t.Helper()
	err := repo.InsertUser(model.User{
		ID:           id,
		Username:     id,
		PasswordHash: "hash",
		Role:         model.UserRoleUser,
		Status:       model.UserStatusActive,
		CreatedAtNs:  1,
		UpdatedAtNs:  1,
	})
	if err != nil {
		t.Fatalf("insert user %s: %v", id, err)
	}
}

func TestStateRepo_ApplyBalanceDeltaCreditsAndDebits(t *testing.T) {
	repo := openBalanceRepo(t)

	if balance, err := repo.GetBalance("u-1"); err != nil || balance != 0 {
		t.Fatalf("a new account should start at zero: got %d, %v", balance, err)
	}

	credit, err := repo.ApplyBalanceDelta(model.BalanceEntry{
		ID: "bl-1", UserID: "u-1", DeltaMicro: 10_000_000,
		Reason: model.BalanceReasonAdminAdjust, Note: "opening", Actor: "admin",
		CreatedAtNs: 10,
	})
	if err != nil {
		t.Fatalf("credit: %v", err)
	}
	if credit.BalanceAfterMicro != 10_000_000 {
		t.Fatalf("balance_after: got %d, want 10000000", credit.BalanceAfterMicro)
	}

	debit, err := repo.ApplyBalanceDelta(model.BalanceEntry{
		ID: "bl-2", UserID: "u-1", DeltaMicro: -4_000_000,
		Reason: model.BalanceReasonPurchase, RefID: "plan-1", CreatedAtNs: 20,
	})
	if err != nil {
		t.Fatalf("debit: %v", err)
	}
	if debit.BalanceAfterMicro != 6_000_000 {
		t.Fatalf("balance_after: got %d, want 6000000", debit.BalanceAfterMicro)
	}

	if balance, err := repo.GetBalance("u-1"); err != nil || balance != 6_000_000 {
		t.Fatalf("stored balance: got %d, %v", balance, err)
	}
}

func TestStateRepo_ApplyBalanceDeltaRefusesOverdraft(t *testing.T) {
	repo := openBalanceRepo(t)

	if _, err := repo.ApplyBalanceDelta(model.BalanceEntry{
		ID: "bl-1", UserID: "u-1", DeltaMicro: 1_000_000,
		Reason: model.BalanceReasonAdminAdjust, Note: "seed", CreatedAtNs: 10,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := repo.ApplyBalanceDelta(model.BalanceEntry{
		ID: "bl-2", UserID: "u-1", DeltaMicro: -2_000_000,
		Reason: model.BalanceReasonPurchase, RefID: "plan-1", CreatedAtNs: 20,
	})
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("overdraft: got %v, want ErrInsufficientBalance", err)
	}

	// The refusal must leave nothing behind: no row, and no change to the balance.
	if balance, _ := repo.GetBalance("u-1"); balance != 1_000_000 {
		t.Fatalf("the balance moved on a refused debit: got %d", balance)
	}
	ledger, err := repo.ListBalanceLedger("u-1", 0)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if len(ledger) != 1 {
		t.Fatalf("a refused debit wrote a ledger row: got %d rows", len(ledger))
	}
}

func TestStateRepo_ApplyBalanceDeltaIsIdempotentPerRequest(t *testing.T) {
	repo := openBalanceRepo(t)

	entry := model.BalanceEntry{
		ID: "bl-1", UserID: "u-1", DeltaMicro: 5_000_000,
		Reason: model.BalanceReasonPurchase, RequestID: "req-1", CreatedAtNs: 10,
	}
	if _, err := repo.ApplyBalanceDelta(entry); err != nil {
		t.Fatalf("first: %v", err)
	}

	replay := entry
	replay.ID = "bl-2"
	if _, err := repo.ApplyBalanceDelta(replay); !errors.Is(err, ErrConflict) {
		t.Fatalf("replay: got %v, want ErrConflict", err)
	}
	if balance, _ := repo.GetBalance("u-1"); balance != 5_000_000 {
		t.Fatalf("the replay charged twice: got %d", balance)
	}

	// The row that won is what a retry is answered with.
	found, err := repo.GetBalanceEntryByRequest("u-1", "req-1")
	if err != nil {
		t.Fatalf("read by request: %v", err)
	}
	if found.ID != "bl-1" || found.DeltaMicro != 5_000_000 {
		t.Fatalf("read by request: got %+v", found)
	}
}

func TestStateRepo_ApplyBalanceDeltaIsIdempotentPerReference(t *testing.T) {
	repo := openBalanceRepo(t)

	entry := model.BalanceEntry{
		ID: "bl-1", UserID: "u-1", DeltaMicro: 7_000_000,
		Reason: model.BalanceReasonDeposit, RefID: "tx-abc", CreatedAtNs: 10,
	}
	if _, err := repo.ApplyBalanceDelta(entry); err != nil {
		t.Fatalf("first: %v", err)
	}

	replay := entry
	replay.ID = "bl-2"
	if _, err := repo.ApplyBalanceDelta(replay); !errors.Is(err, ErrConflict) {
		t.Fatalf("same txID: got %v, want ErrConflict", err)
	}
	if balance, _ := repo.GetBalance("u-1"); balance != 7_000_000 {
		t.Fatalf("the same transaction was credited twice: got %d", balance)
	}
}

func TestStateRepo_ApplyBalanceDeltaAllowsManyEmptyReferences(t *testing.T) {
	repo := openBalanceRepo(t)

	// The unique indexes skip empty ref_id and request_id, so two operator
	// adjustments with neither are two separate movements, not a conflict.
	for i, id := range []string{"bl-1", "bl-2"} {
		if _, err := repo.ApplyBalanceDelta(model.BalanceEntry{
			ID: id, UserID: "u-1", DeltaMicro: 1_000_000,
			Reason: model.BalanceReasonAdminAdjust, Note: "adjust",
			CreatedAtNs: int64(10 + i),
		}); err != nil {
			t.Fatalf("adjustment %s: %v", id, err)
		}
	}
	if balance, _ := repo.GetBalance("u-1"); balance != 2_000_000 {
		t.Fatalf("two adjustments: got %d, want 2000000", balance)
	}
}

func TestStateRepo_ListBalanceLedgerNewestFirst(t *testing.T) {
	repo := openBalanceRepo(t)

	for i, id := range []string{"bl-1", "bl-2", "bl-3"} {
		if _, err := repo.ApplyBalanceDelta(model.BalanceEntry{
			ID: id, UserID: "u-1", DeltaMicro: 1_000_000,
			Reason: model.BalanceReasonAdminAdjust, Note: "adjust",
			CreatedAtNs: int64(10 * (i + 1)),
		}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	ledger, err := repo.ListBalanceLedger("u-1", 0)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if len(ledger) != 3 {
		t.Fatalf("rows: got %d, want 3", len(ledger))
	}
	if ledger[0].ID != "bl-3" || ledger[2].ID != "bl-1" {
		t.Fatalf("order: got %s first and %s last", ledger[0].ID, ledger[2].ID)
	}

	limited, err := repo.ListBalanceLedger("u-1", 2)
	if err != nil {
		t.Fatalf("limited ledger: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limit: got %d rows, want 2", len(limited))
	}
}

func TestStateRepo_UserBalanceCascadesButLedgerDoesNot(t *testing.T) {
	repo := openBalanceRepo(t)

	if _, err := repo.ApplyBalanceDelta(model.BalanceEntry{
		ID: "bl-1", UserID: "u-1", DeltaMicro: 3_000_000,
		Reason: model.BalanceReasonAdminAdjust, Note: "seed", CreatedAtNs: 10,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := repo.DeleteUser("u-1"); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	// The balance goes with the account; the ledger stays as the record of money
	// that moved, which is what an operator has to be able to answer for later.
	var balances int
	if err := repo.db.QueryRow("SELECT COUNT(*) FROM user_balance WHERE user_id = 'u-1'").Scan(&balances); err != nil {
		t.Fatalf("count balances: %v", err)
	}
	if balances != 0 {
		t.Fatalf("the balance survived the account: got %d rows", balances)
	}
	var entries int
	if err := repo.db.QueryRow("SELECT COUNT(*) FROM balance_ledger WHERE user_id = 'u-1'").Scan(&entries); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if entries != 1 {
		t.Fatalf("the ledger was cascaded away: got %d rows, want 1", entries)
	}
}
