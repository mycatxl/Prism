package state

import (
	"database/sql"
	"errors"
	"fmt"

	"prism/internal/model"
)

// Deposit intents: allocation, expiry and crediting.
//
// The allocator is the partial unique index on amount_micro. A candidate is
// inserted directly and a conflict means "taken", so the database decides
// instead of a counter table and no extra lock is needed. Allocation also
// releases expired holds first: a hold whose hold_until_ns has passed but which
// no sweep has visited yet would otherwise keep occupying its amount and shrink
// the pool a little with every abandoned intent.

const depositIntentColumns = `id, user_id, amount_micro, status, tx_id,
	created_at_ns, expires_at_ns, hold_until_ns, paid_at_ns`

// InsertDepositIntent allocates an amount and opens an intent in one
// transaction.
//
// The candidate amounts are supplied by the caller in ascending order, starting
// one micro-unit above what the user asked for. Whole amounts - a zero fractional
// part - are never offered by the caller, because a round number is the most
// likely to collide with an unrelated transfer. Expired holds are released
// before the first insert, so the pool is as wide as it should be.
func (r *StateRepo) InsertDepositIntent(intent model.DepositIntent, candidates []int64, nowNs int64) (model.DepositIntent, error) {
	if len(candidates) == 0 {
		return model.DepositIntent{}, ErrConflict
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return model.DepositIntent{}, fmt.Errorf("begin deposit intent: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := releaseExpiredDepositsTx(tx, nowNs); err != nil {
		return model.DepositIntent{}, err
	}

	for _, amount := range candidates {
		row := model.DepositIntent{
			ID:          intent.ID,
			UserID:      intent.UserID,
			AmountMicro: amount,
			Status:      model.DepositStatusPending,
			CreatedAtNs: intent.CreatedAtNs,
			ExpiresAtNs: intent.ExpiresAtNs,
			HoldUntilNs: intent.HoldUntilNs,
		}
		result, err := tx.Exec(`
			INSERT INTO deposit_intents (`+depositIntentColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING
		`, row.ID, row.UserID, row.AmountMicro, row.Status, row.TxID,
			row.CreatedAtNs, row.ExpiresAtNs, row.HoldUntilNs, row.PaidAtNs)
		if err != nil {
			return model.DepositIntent{}, fmt.Errorf("allocate deposit amount: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return model.DepositIntent{}, fmt.Errorf("read allocation result: %w", err)
		}
		if affected == 0 {
			continue
		}
		if err := tx.Commit(); err != nil {
			return model.DepositIntent{}, fmt.Errorf("commit deposit intent: %w", err)
		}
		return row, nil
	}
	return model.DepositIntent{}, ErrConflict
}

// GetDepositIntent returns one intent by id.
func (r *StateRepo) GetDepositIntent(id string) (model.DepositIntent, error) {
	row := r.db.QueryRow(`SELECT `+depositIntentColumns+` FROM deposit_intents WHERE id = ?`, id)
	intent, err := scanDepositIntent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DepositIntent{}, ErrNotFound
	}
	if err != nil {
		return model.DepositIntent{}, fmt.Errorf("read deposit intent: %w", err)
	}
	return intent, nil
}

// ListDepositIntents returns a user's intents, newest first.
func (r *StateRepo) ListDepositIntents(userID string, limit int) ([]model.DepositIntent, error) {
	query := `SELECT ` + depositIntentColumns + ` FROM deposit_intents
		WHERE user_id = ? ORDER BY created_at_ns DESC, id DESC`
	args := []any{userID}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list deposit intents: %w", err)
	}
	defer rows.Close()

	result := []model.DepositIntent{}
	for rows.Next() {
		intent, err := scanDepositIntent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan deposit intent: %w", err)
		}
		result = append(result, intent)
	}
	return result, rows.Err()
}

// SweepDeposits ages intents through their two windows and returns how many
// amounts came back to the pool.
//
// An intent leaves the payment window at expires_at_ns and keeps holding its
// amount for a further 15 minutes, so a transfer sent at the last second is
// still credited once solidification catches up. Only after hold_until_ns does
// the amount return to the pool.
func (r *StateRepo) SweepDeposits(nowNs int64) (released int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin deposit sweep: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`
		UPDATE deposit_intents SET status = ?
		WHERE status = ? AND expires_at_ns <= ? AND hold_until_ns > ?
	`, model.DepositStatusUnpaid, model.DepositStatusPending, nowNs, nowNs); err != nil {
		return 0, fmt.Errorf("mark deposits unpaid: %w", err)
	}
	result, err := tx.Exec(`
		UPDATE deposit_intents SET status = ?
		WHERE status IN (?, ?) AND hold_until_ns <= ?
	`, model.DepositStatusReleased, model.DepositStatusPending, model.DepositStatusUnpaid, nowNs)
	if err != nil {
		return 0, fmt.Errorf("release expired deposits: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read release result: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit deposit sweep: %w", err)
	}
	return int(affected), nil
}

// CreditDeposit applies one on-chain transfer to the intent it belongs to.
//
// The match is by amount and by the transfer's own block time, not by status:
// an intent already marked released still owns transfers whose block time falls
// inside its occupancy, because solidification lags and a payment made at the
// last second is discovered minutes later. Paid intents are excluded - the
// transfer that settled them is already in the ledger.
//
// The ledger write, the status change and the cursor advance share this
// transaction. Advancing the cursor first would lose a transfer for good if the
// process died in between; sharing the transaction means a crash replays both,
// and the (reason, ref_id) unique index makes the replay harmless.
func (r *StateRepo) CreditDeposit(txID string, amountMicro, blockTSNs, nowNs int64, cursorName string, watermarkMs int64, entry model.BalanceEntry) (model.DepositIntent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return model.DepositIntent{}, fmt.Errorf("begin deposit credit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// A replayed transfer is answered as a conflict before the match is even
	// attempted: once an intent is paid it no longer matches, so without this
	// check the replay would look like "no intent owns this transfer".
	var settled string
	err = tx.QueryRow(`SELECT id FROM deposit_intents WHERE tx_id = ? AND status = ? LIMIT 1`,
		txID, model.DepositStatusPaid).Scan(&settled)
	if err == nil {
		return model.DepositIntent{}, fmt.Errorf("%w: that deposit was already credited", ErrConflict)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.DepositIntent{}, fmt.Errorf("check deposit replay: %w", err)
	}

	intent, err := depositIntentForTransferTx(tx, amountMicro, blockTSNs)
	if err != nil {
		return model.DepositIntent{}, err
	}

	current, err := balanceForUpdate(tx, intent.UserID)
	if err != nil {
		return model.DepositIntent{}, err
	}
	entry.UserID = intent.UserID
	entry.DeltaMicro = intent.AmountMicro
	entry.Reason = model.BalanceReasonDeposit
	entry.RefID = txID
	entry.BalanceAfterMicro = current + intent.AmountMicro
	entry.CreatedAtNs = nowNs
	if _, err := tx.Exec(`
		INSERT INTO user_balance (user_id, balance_micro, updated_at_ns)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			balance_micro = excluded.balance_micro,
			updated_at_ns = excluded.updated_at_ns
	`, entry.UserID, entry.BalanceAfterMicro, nowNs); err != nil {
		return model.DepositIntent{}, fmt.Errorf("write balance: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO balance_ledger (id, user_id, delta_micro, balance_after_micro,
		                            reason, ref_id, request_id, note, actor, created_at_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, entry.ID, entry.UserID, entry.DeltaMicro, entry.BalanceAfterMicro,
		entry.Reason, entry.RefID, entry.RequestID, entry.Note, entry.Actor,
		entry.CreatedAtNs); err != nil {
		if isSQLiteUniqueConstraint(err) {
			return model.DepositIntent{}, fmt.Errorf("%w: that deposit was already credited", ErrConflict)
		}
		return model.DepositIntent{}, fmt.Errorf("write deposit ledger: %w", err)
	}

	if _, err := tx.Exec(`
		UPDATE deposit_intents SET status = ?, tx_id = ?, paid_at_ns = ?
		WHERE id = ?
	`, model.DepositStatusPaid, txID, nowNs, intent.ID); err != nil {
		return model.DepositIntent{}, fmt.Errorf("settle deposit intent: %w", err)
	}

	if err := advanceChainCursorTx(tx, cursorName, watermarkMs, nowNs); err != nil {
		return model.DepositIntent{}, err
	}

	intent.Status = model.DepositStatusPaid
	intent.TxID = txID
	intent.PaidAtNs = nowNs
	if err := tx.Commit(); err != nil {
		return model.DepositIntent{}, fmt.Errorf("commit deposit credit: %w", err)
	}
	return intent, nil
}

// ChainCursor returns the listener watermark in milliseconds, zero when it has
// never advanced.
func (r *StateRepo) ChainCursor(name string) (int64, error) {
	var watermark int64
	err := r.db.QueryRow(`SELECT watermark_ms FROM chain_cursor WHERE name = ?`, name).Scan(&watermark)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read chain cursor: %w", err)
	}
	return watermark, nil
}

// AdvanceChainCursor records the newest block time already handled.
//
// The listener calls this for transfers that matched no intent. Nothing was
// written for them, so a crash between reading and advancing only means they are
// read again - which costs nothing, because the alternative order could skip a
// transfer that had already been credited.
func (r *StateRepo) AdvanceChainCursor(name string, watermarkMs, nowNs int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, err := r.db.Exec(chainCursorUpsert, name, watermarkMs, nowNs); err != nil {
		return fmt.Errorf("advance chain cursor: %w", err)
	}
	return nil
}

const chainCursorUpsert = `
	INSERT INTO chain_cursor (name, watermark_ms, updated_at_ns)
	VALUES (?, ?, ?)
	ON CONFLICT(name) DO UPDATE SET
		watermark_ms = excluded.watermark_ms,
		updated_at_ns = excluded.updated_at_ns
`

// releaseExpiredDepositsTx is the allocation-time half of the release sweep.
func releaseExpiredDepositsTx(tx *sql.Tx, nowNs int64) error {
	if _, err := tx.Exec(`
		UPDATE deposit_intents SET status = ?
		WHERE status IN (?, ?) AND hold_until_ns <= ?
	`, model.DepositStatusReleased, model.DepositStatusPending, model.DepositStatusUnpaid, nowNs); err != nil {
		return fmt.Errorf("release expired deposits: %w", err)
	}
	return nil
}

// depositIntentForTransferTx finds the intent a transfer belongs to.
//
// The window is [created_at_ns, hold_until_ns) measured on the chain's own
// clock. Transfers older than the intent's creation are refused outright: that
// is what lets an amount be reused, because a straggler for an expired intent
// necessarily predates the intent that holds the amount now.
func depositIntentForTransferTx(tx *sql.Tx, amountMicro, blockTSNs int64) (model.DepositIntent, error) {
	row := tx.QueryRow(`
		SELECT `+depositIntentColumns+` FROM deposit_intents
		WHERE amount_micro = ? AND status <> ?
		  AND created_at_ns <= ? AND hold_until_ns > ?
		ORDER BY created_at_ns DESC LIMIT 1
	`, amountMicro, model.DepositStatusPaid, blockTSNs, blockTSNs)
	intent, err := scanDepositIntent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DepositIntent{}, ErrNotFound
	}
	if err != nil {
		return model.DepositIntent{}, fmt.Errorf("match deposit intent: %w", err)
	}
	return intent, nil
}

func advanceChainCursorTx(tx *sql.Tx, name string, watermarkMs, nowNs int64) error {
	if _, err := tx.Exec(chainCursorUpsert, name, watermarkMs, nowNs); err != nil {
		return fmt.Errorf("advance chain cursor: %w", err)
	}
	return nil
}

func scanDepositIntent(row interface{ Scan(...any) error }) (model.DepositIntent, error) {
	var intent model.DepositIntent
	err := row.Scan(&intent.ID, &intent.UserID, &intent.AmountMicro, &intent.Status,
		&intent.TxID, &intent.CreatedAtNs, &intent.ExpiresAtNs, &intent.HoldUntilNs,
		&intent.PaidAtNs)
	return intent, err
}
