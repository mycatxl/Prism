package state

import (
	"database/sql"
	"errors"
	"fmt"

	"prism/internal/model"
)

// StateRepo methods for the redeem_codes table created by 000023_redeem_codes.
//
// The lookup key is the HMAC of the code, never the code: a copy of this
// database cannot be redeemed, and a code that leaks from a log or a screenshot
// is still useless. The plaintext exists only in the response that mints it.
//
// RedeemCodeAndCredit is the one that matters. Claiming a use and crediting the
// balance happen in a single transaction, because the alternative - the shape
// the invites table uses for registration - is acceptable for opening an account
// (the account exists, the operator can grant the plan afterwards) and not
// acceptable for money (the user paid and has nothing). A failed credit rolls
// the claim back, so a code is never spent without its balance arriving.

const redeemCodeColumns = `id, code_hash, code_hint, amount_micro, max_uses,
	used_count, expires_at_ns, note, status, created_by, created_at_ns, updated_at_ns`

// InsertRedeemCode persists a new code. A duplicate hash returns ErrConflict,
// which for a 40-bit code means a collision that should be regenerated.
func (r *StateRepo) InsertRedeemCode(code model.RedeemCode) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
		INSERT INTO redeem_codes (id, code_hash, code_hint, amount_micro, max_uses,
		                          used_count, expires_at_ns, note, status, created_by,
		                          created_at_ns, updated_at_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, code.ID, code.CodeHash, code.CodeHint, code.AmountMicro, code.MaxUses,
		code.UsedCount, code.ExpiresAtNs, code.Note, code.Status, code.CreatedBy,
		code.CreatedAtNs, code.UpdatedAtNs)
	if isSQLiteUniqueConstraint(err) {
		return fmt.Errorf("%w: that code already exists", ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("insert redeem code: %w", err)
	}
	return nil
}

// ListRedeemCodes returns every code, newest first. The hashes come back with
// the rows - the service decides what a console is allowed to see, and it only
// ever renders the hint.
func (r *StateRepo) ListRedeemCodes() ([]model.RedeemCode, error) {
	rows, err := r.db.Query("SELECT " + redeemCodeColumns +
		" FROM redeem_codes ORDER BY created_at_ns DESC, id")
	if err != nil {
		return nil, fmt.Errorf("list redeem codes: %w", err)
	}
	defer rows.Close()

	result := []model.RedeemCode{}
	for rows.Next() {
		code, err := scanRedeemCode(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, code)
	}
	return result, rows.Err()
}

// GetRedeemCodeByHash reads one code by its hash. The redeem path needs the
// amount before the transaction opens, and a scan of every code to find it would
// grow with the number of codes ever minted.
func (r *StateRepo) GetRedeemCodeByHash(codeHash []byte) (model.RedeemCode, error) {
	row := r.db.QueryRow("SELECT "+redeemCodeColumns+
		" FROM redeem_codes WHERE code_hash = ?", codeHash)
	code, err := scanRedeemCode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.RedeemCode{}, ErrNotFound
	}
	if err != nil {
		return model.RedeemCode{}, err
	}
	return code, nil
}

// DeleteRedeemCode removes a code. Redemptions it already paid for are untouched:
// the money is in the ledger, and the code row is only the licence to add more.
func (r *StateRepo) DeleteRedeemCode(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM redeem_codes WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete redeem code: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RedeemCodeAndCredit claims one use of a code and credits its amount in one
// transaction, returning the ledger row that was written.
//
// The claim is a single conditional UPDATE over status, expiry and remaining
// uses, so two redemptions arriving together cannot both take the last use. No
// row matched means the code is unknown, revoked, expired or spent, and the
// caller gets ErrNotFound without learning which - the four read the same on
// purpose, so this endpoint cannot be used to find valid codes.
//
// The ledger write is the same one ApplyBalanceDelta performs; it is repeated
// here rather than called, because calling it would open a second transaction
// and the point is that both writes commit together.
func (r *StateRepo) RedeemCodeAndCredit(codeHash []byte, entry model.BalanceEntry, nowNs int64) (model.BalanceEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return model.BalanceEntry{}, fmt.Errorf("begin redemption: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.Exec(`
		UPDATE redeem_codes SET used_count = used_count + 1, updated_at_ns = ?
		WHERE code_hash = ?
		  AND status = ?
		  AND (expires_at_ns = 0 OR expires_at_ns > ?)
		  AND (max_uses = 0 OR used_count < max_uses)
	`, nowNs, codeHash, model.RedeemCodeStatusActive, nowNs)
	if err != nil {
		return model.BalanceEntry{}, fmt.Errorf("claim redeem code: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return model.BalanceEntry{}, ErrNotFound
	}

	current, err := balanceForUpdate(tx, entry.UserID)
	if err != nil {
		return model.BalanceEntry{}, err
	}
	after := current + entry.DeltaMicro
	if after < 0 {
		return model.BalanceEntry{}, ErrInsufficientBalance
	}
	if _, err := tx.Exec(`
		INSERT INTO user_balance (user_id, balance_micro, updated_at_ns)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			balance_micro = excluded.balance_micro,
			updated_at_ns = excluded.updated_at_ns
	`, entry.UserID, after, entry.CreatedAtNs); err != nil {
		return model.BalanceEntry{}, fmt.Errorf("write balance: %w", err)
	}

	entry.BalanceAfterMicro = after
	if _, err := tx.Exec(`
		INSERT INTO balance_ledger (id, user_id, delta_micro, balance_after_micro,
		                            reason, ref_id, request_id, note, actor, created_at_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, entry.ID, entry.UserID, entry.DeltaMicro, entry.BalanceAfterMicro,
		entry.Reason, entry.RefID, entry.RequestID, entry.Note, entry.Actor,
		entry.CreatedAtNs); err != nil {
		if isSQLiteUniqueConstraint(err) {
			return model.BalanceEntry{}, fmt.Errorf("%w: that balance change was already applied", ErrConflict)
		}
		return model.BalanceEntry{}, fmt.Errorf("write ledger: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return model.BalanceEntry{}, fmt.Errorf("commit redemption: %w", err)
	}
	return entry, nil
}

// CountRedeemCodeUses reports how many times a code has been redeemed, which the
// console shows next to the remaining uses.
func (r *StateRepo) CountRedeemCodeUses(codeHash []byte) (int, error) {
	var used int
	err := r.db.QueryRow("SELECT used_count FROM redeem_codes WHERE code_hash = ?", codeHash).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read redeem code uses: %w", err)
	}
	return used, nil
}

func scanRedeemCode(row interface{ Scan(...any) error }) (model.RedeemCode, error) {
	var code model.RedeemCode
	err := row.Scan(&code.ID, &code.CodeHash, &code.CodeHint, &code.AmountMicro,
		&code.MaxUses, &code.UsedCount, &code.ExpiresAtNs, &code.Note, &code.Status,
		&code.CreatedBy, &code.CreatedAtNs, &code.UpdatedAtNs)
	if err != nil {
		return model.RedeemCode{}, fmt.Errorf("scan redeem code: %w", err)
	}
	return code, nil
}
