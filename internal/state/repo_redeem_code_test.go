package state

import (
	"bytes"
	"errors"
	"testing"

	"prism/internal/model"
)

// Redeem codes: the claim, the credit, and the guarantee that a spent code
// cannot be spent twice.

func seedRedeemCode(t *testing.T, repo *StateRepo, id string, amountMicro int64, maxUses int, expiresAtNs int64) []byte {
	t.Helper()
	hash := []byte("hash-" + id)
	code := model.RedeemCode{
		ID:          id,
		CodeHash:    hash,
		CodeHint:    "ABCD",
		AmountMicro: amountMicro,
		MaxUses:     maxUses,
		UsedCount:   0,
		ExpiresAtNs: expiresAtNs,
		Status:      model.RedeemCodeStatusActive,
		CreatedAtNs: 1,
		UpdatedAtNs: 1,
	}
	if err := repo.InsertRedeemCode(code); err != nil {
		t.Fatalf("insert redeem code: %v", err)
	}
	return hash
}

func creditEntry(userID string, amountMicro int64, refID string, nowNs int64) model.BalanceEntry {
	return model.BalanceEntry{
		ID:          "bl-" + refID,
		UserID:      userID,
		DeltaMicro:  amountMicro,
		Reason:      model.BalanceReasonRedeem,
		RefID:       refID,
		CreatedAtNs: nowNs,
	}
}

func TestStateRepo_RedeemCodeCreditsAndConsumes(t *testing.T) {
	repo := openBalanceRepo(t)
	hash := seedRedeemCode(t, repo, "rc-1", 12_500_000, 1, 0)

	entry, err := repo.RedeemCodeAndCredit(hash, creditEntry("u-1", 12_500_000, "rc-1", 100), 100)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if entry.BalanceAfterMicro != 12_500_000 {
		t.Fatalf("balance_after: got %d", entry.BalanceAfterMicro)
	}
	if balance, _ := repo.GetBalance("u-1"); balance != 12_500_000 {
		t.Fatalf("balance: got %d", balance)
	}
	if used, err := repo.CountRedeemCodeUses(hash); err != nil || used != 1 {
		t.Fatalf("used_count: got %d, %v", used, err)
	}
}

func TestStateRepo_RedeemCodeRefusesASpentCode(t *testing.T) {
	repo := openBalanceRepo(t)
	hash := seedRedeemCode(t, repo, "rc-1", 5_000_000, 1, 0)

	if _, err := repo.RedeemCodeAndCredit(hash, creditEntry("u-1", 5_000_000, "rc-1", 100), 100); err != nil {
		t.Fatalf("first redeem: %v", err)
	}

	_, err := repo.RedeemCodeAndCredit(hash, creditEntry("u-1", 5_000_000, "rc-2", 200), 200)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("second redeem: got %v, want ErrNotFound", err)
	}
	if balance, _ := repo.GetBalance("u-1"); balance != 5_000_000 {
		t.Fatalf("a spent code credited again: got %d", balance)
	}
}

func TestStateRepo_RedeemCodeRefusesExpiredAndDisabled(t *testing.T) {
	repo := openBalanceRepo(t)

	expired := seedRedeemCode(t, repo, "rc-exp", 1_000_000, 1, 500)
	if _, err := repo.RedeemCodeAndCredit(expired, creditEntry("u-1", 1_000_000, "rc-exp", 600), 600); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired code: got %v, want ErrNotFound", err)
	}

	disabledHash := []byte("hash-disabled")
	if err := repo.InsertRedeemCode(model.RedeemCode{
		ID: "rc-dis", CodeHash: disabledHash, AmountMicro: 1_000_000, MaxUses: 1,
		Status: model.RedeemCodeStatusDisabled, CreatedAtNs: 1, UpdatedAtNs: 1,
	}); err != nil {
		t.Fatalf("insert disabled: %v", err)
	}
	if _, err := repo.RedeemCodeAndCredit(disabledHash, creditEntry("u-1", 1_000_000, "rc-dis", 100), 100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled code: got %v, want ErrNotFound", err)
	}

	if balance, _ := repo.GetBalance("u-1"); balance != 0 {
		t.Fatalf("a refused code moved the balance: got %d", balance)
	}
}

func TestStateRepo_RedeemCodeUnknownHashCreditsNothing(t *testing.T) {
	repo := openBalanceRepo(t)

	_, err := repo.RedeemCodeAndCredit([]byte("no-such-code"), creditEntry("u-1", 1_000_000, "rc-x", 100), 100)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown code: got %v, want ErrNotFound", err)
	}
	if balance, _ := repo.GetBalance("u-1"); balance != 0 {
		t.Fatalf("an unknown code moved the balance: got %d", balance)
	}
	if ledger, _ := repo.ListBalanceLedger("u-1", 0); len(ledger) != 0 {
		t.Fatalf("an unknown code wrote %d ledger rows", len(ledger))
	}
}

func TestStateRepo_RedeemCodeUnlimitedUses(t *testing.T) {
	repo := openBalanceRepo(t)
	hash := seedRedeemCode(t, repo, "rc-1", 1_000_000, 0, 0)

	for i := 0; i < 3; i++ {
		ref := string(rune('a' + i))
		if _, err := repo.RedeemCodeAndCredit(hash, creditEntry("u-1", 1_000_000, ref, int64(100+i)), int64(100+i)); err != nil {
			t.Fatalf("redeem %d: %v", i, err)
		}
	}
	if balance, _ := repo.GetBalance("u-1"); balance != 3_000_000 {
		t.Fatalf("three unlimited redemptions: got %d", balance)
	}
	if used, _ := repo.CountRedeemCodeUses(hash); used != 3 {
		t.Fatalf("used_count: got %d, want 3", used)
	}
}

func TestStateRepo_RedeemCodeRejectsDuplicateHash(t *testing.T) {
	repo := openBalanceRepo(t)
	hash := seedRedeemCode(t, repo, "rc-1", 1_000_000, 1, 0)

	err := repo.InsertRedeemCode(model.RedeemCode{
		ID: "rc-2", CodeHash: hash, AmountMicro: 1_000_000, MaxUses: 1,
		Status: model.RedeemCodeStatusActive, CreatedAtNs: 1, UpdatedAtNs: 1,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate hash: got %v, want ErrConflict", err)
	}
}

func TestStateRepo_ListAndDeleteRedeemCodes(t *testing.T) {
	repo := openBalanceRepo(t)
	seedRedeemCode(t, repo, "rc-1", 1_000_000, 1, 0)
	seedRedeemCode(t, repo, "rc-2", 2_000_000, 1, 0)

	codes, err := repo.ListRedeemCodes()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(codes) != 2 {
		t.Fatalf("codes: got %d, want 2", len(codes))
	}

	if err := repo.DeleteRedeemCode("rc-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := repo.DeleteRedeemCode("rc-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: got %v, want ErrNotFound", err)
	}

	// Deleting a code must not touch money it already credited.
	hash := seedRedeemCode(t, repo, "rc-3", 3_000_000, 1, 0)
	if _, err := repo.RedeemCodeAndCredit(hash, creditEntry("u-1", 3_000_000, "rc-3", 100), 100); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if err := repo.DeleteRedeemCode("rc-3"); err != nil {
		t.Fatalf("delete after redeem: %v", err)
	}
	if balance, _ := repo.GetBalance("u-1"); balance != 3_000_000 {
		t.Fatalf("deleting a code removed its credit: got %d", balance)
	}
}

func TestStateRepo_RedeemCodeStoresNoPlaintext(t *testing.T) {
	repo := openBalanceRepo(t)
	hash := []byte{0xde, 0xad, 0xbe, 0xef}
	if err := repo.InsertRedeemCode(model.RedeemCode{
		ID: "rc-1", CodeHash: hash, CodeHint: "WXYZ", AmountMicro: 1_000_000,
		MaxUses: 1, Status: model.RedeemCodeStatusActive, CreatedAtNs: 1, UpdatedAtNs: 1,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var stored []byte
	if err := repo.db.QueryRow("SELECT code_hash FROM redeem_codes WHERE id = 'rc-1'").Scan(&stored); err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if !bytes.Equal(stored, hash) {
		t.Fatalf("stored hash: got %x, want %x", stored, hash)
	}
}
