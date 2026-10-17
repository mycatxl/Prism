package state

import (
	"database/sql"
	"errors"
	"fmt"

	"prism/internal/model"
)

// StateRepo methods for the balance tables created by 000022_balance.
//
// Every mutation here is one transaction that writes a ledger row and moves the
// balance together. The two can therefore never disagree: a crash rolls both
// back, and the ledger's balance_after_micro is the value the balance had
// immediately after the row it describes.
//
// Writes take r.mu like the rest of this package, so the read-modify-write on
// user_balance is serialised even before the single connection matters. The
// transactions here do no I/O beyond SQLite: anything that talks to the network
// has to happen in the caller, before the transaction opens.

const balanceLedgerColumns = `id, user_id, delta_micro, balance_after_micro, reason,
	ref_id, request_id, note, actor, created_at_ns`

// ApplyBalanceDelta moves a user's balance by delta and records the movement.
//
// delta is signed: a credit is positive, a charge is negative. The row is
// refused when it would take the balance below zero, which is the only rule the
// money has - there is no overdraft, and no code path may create one.
//
// The two unique indexes on balance_ledger are how a repeated call is stopped:
// a second row with the same (user_id, request_id) or the same (reason, ref_id)
// fails, the transaction rolls back, and the caller gets ErrConflict having
// changed nothing. That is deliberate - the retry that follows reads the row
// that won instead of applying the movement twice.
func (r *StateRepo) ApplyBalanceDelta(entry model.BalanceEntry) (model.BalanceEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return model.BalanceEntry{}, fmt.Errorf("begin balance change: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

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
		return model.BalanceEntry{}, fmt.Errorf("commit balance change: %w", err)
	}
	return entry, nil
}

// GetBalance returns a user's balance, which is zero for an account that has
// never been credited.
//
// A missing row is not an error: the balance table is a cache of movements, and
// an account with no movements has a balance of zero by definition.
func (r *StateRepo) GetBalance(userID string) (int64, error) {
	var balance int64
	err := r.db.QueryRow("SELECT balance_micro FROM user_balance WHERE user_id = ?", userID).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read balance: %w", err)
	}
	return balance, nil
}

// ListBalanceLedger returns a user's movements, newest first, capped at limit.
// A limit of zero or less means no cap.
func (r *StateRepo) ListBalanceLedger(userID string, limit int) ([]model.BalanceEntry, error) {
	query := "SELECT " + balanceLedgerColumns + ` FROM balance_ledger
		WHERE user_id = ? ORDER BY created_at_ns DESC, id DESC`
	args := []any{userID}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list ledger: %w", err)
	}
	defer rows.Close()

	result := []model.BalanceEntry{}
	for rows.Next() {
		var entry model.BalanceEntry
		if err := rows.Scan(&entry.ID, &entry.UserID, &entry.DeltaMicro,
			&entry.BalanceAfterMicro, &entry.Reason, &entry.RefID, &entry.RequestID,
			&entry.Note, &entry.Actor, &entry.CreatedAtNs); err != nil {
			return nil, fmt.Errorf("scan ledger row: %w", err)
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

// GetBalanceEntryByRequest returns the movement a client's request_id produced,
// or ErrNotFound when that request has not been settled.
//
// This is how a retried purchase is answered with the original result: the
// second call finds the first call's row instead of charging again.
func (r *StateRepo) GetBalanceEntryByRequest(userID, requestID string) (*model.BalanceEntry, error) {
	if requestID == "" {
		return nil, ErrNotFound
	}
	row := r.db.QueryRow("SELECT "+balanceLedgerColumns+`
		FROM balance_ledger WHERE user_id = ? AND request_id = ?`, userID, requestID)
	entry, err := scanBalanceEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read ledger by request: %w", err)
	}
	return &entry, nil
}

// balanceForUpdate reads the current balance inside a transaction. The row is
// created on first write rather than here, so a read of an unknown user is
// simply zero.
func balanceForUpdate(tx *sql.Tx, userID string) (int64, error) {
	var balance int64
	err := tx.QueryRow("SELECT balance_micro FROM user_balance WHERE user_id = ?", userID).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read balance for update: %w", err)
	}
	return balance, nil
}

func scanBalanceEntry(row interface{ Scan(...any) error }) (model.BalanceEntry, error) {
	var entry model.BalanceEntry
	err := row.Scan(&entry.ID, &entry.UserID, &entry.DeltaMicro, &entry.BalanceAfterMicro,
		&entry.Reason, &entry.RefID, &entry.RequestID, &entry.Note, &entry.Actor,
		&entry.CreatedAtNs)
	return entry, err
}
