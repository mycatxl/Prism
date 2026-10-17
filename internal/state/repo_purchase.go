package state

import (
	"database/sql"
	"errors"
	"fmt"

	"prism/internal/model"
)

// Buying and renewing a subscription with balance.
//
// Both are one transaction that moves the money and changes the subscription
// together. The alternative - charge here, subscribe there - has a state in
// which the customer paid and got nothing, which is the one failure this whole
// surface exists to make impossible. The redeem path pays the same price for
// the same reason (RedeemCodeAndCredit), and repeats the ledger write rather
// than calling ApplyBalanceDelta, because calling it would open a second
// transaction.
//
// Neither writes period_used_bytes. That column has a concurrent writer - the
// usage flush worker adds to it - so a subscription write that carried the
// counter would give away every byte counted between the read and the write,
// permanently, because nothing recomputes it from the usage tables.

// PurchaseResult is what a purchase settled to.
type PurchaseResult struct {
	Entry model.BalanceEntry
	// Subscription is the row that is in force afterwards. On a replay it is
	// the row the first call created, read back rather than rebuilt.
	Subscription model.UserSubscription
	// Replayed reports that request_id had already been settled, so nothing
	// was charged this time.
	Replayed bool
}

// PurchasePlanWithBalance charges entry.DeltaMicro (a negative amount) and puts
// sub in force, in one transaction.
//
// The order inside the transaction is the order the constraints demand: the
// user's other active subscriptions are expired before the new one is inserted,
// because ux_user_subscriptions_active allows one active row per user and would
// refuse the insert otherwise.
//
// entry.RequestID is the idempotency key. A second call carrying the same one
// finds the first call's ledger row and returns Replayed with the subscription
// that is in force now, instead of charging again. An empty request_id is
// allowed and settles every time - that is the operator's path, not a client's.
func (r *StateRepo) PurchasePlanWithBalance(sub model.UserSubscription, entry model.BalanceEntry, nowNs int64) (PurchaseResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return PurchaseResult{}, fmt.Errorf("begin purchase: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if entry.RequestID != "" {
		settled, err := ledgerEntryByRequest(tx, entry.UserID, entry.RequestID)
		if err != nil {
			return PurchaseResult{}, err
		}
		if settled != nil {
			current, err := activeSubscriptionTx(tx, entry.UserID)
			if err != nil {
				return PurchaseResult{}, err
			}
			return PurchaseResult{Entry: *settled, Subscription: current, Replayed: true}, nil
		}
	}

	entry, err = chargeInTx(tx, entry)
	if err != nil {
		return PurchaseResult{}, err
	}
	if _, err := tx.Exec(`
		UPDATE user_subscriptions SET status = ?
		WHERE user_id = ? AND status = ?
	`, model.SubscriptionStatusExpired, entry.UserID, model.SubscriptionStatusActive); err != nil {
		return PurchaseResult{}, fmt.Errorf("expire previous subscription: %w", err)
	}

	snapshot := sub.PlanSnapshotJSON
	if snapshot == "" {
		snapshot = "{}"
	}
	if sub.Status == "" {
		sub.Status = model.SubscriptionStatusActive
	}
	if _, err := tx.Exec(`
		INSERT INTO user_subscriptions (id, user_id, plan_id, plan_snapshot_json, starts_at_ns,
		                                expires_at_ns, period_start_ns, period_used_bytes,
		                                auto_renew, period_index, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, sub.ID, sub.UserID, nullableString(sub.PlanID), snapshot, sub.StartsAtNs,
		sub.ExpiresAtNs, sub.PeriodStartNs, sub.PeriodUsedBytes, sub.AutoRenew,
		sub.PeriodIndex, sub.Status); err != nil {
		if isSQLiteUniqueConstraint(err) {
			return PurchaseResult{}, fmt.Errorf("%w: the user already has an active subscription", ErrConflict)
		}
		return PurchaseResult{}, fmt.Errorf("write subscription: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return PurchaseResult{}, fmt.Errorf("commit purchase: %w", err)
	}
	return PurchaseResult{Entry: entry, Subscription: sub}, nil
}

// RenewSubscriptionWithBalance charges entry.DeltaMicro and extends a
// subscription by one period, in one transaction.
//
// expectedPeriodIndex is the optimistic lock. The row is only extended when it
// still carries the period the caller read, so two sweeps that both decided to
// renew the same subscription cannot both charge for it: the second one matches
// no row, rolls its charge back and reports ErrConflict. That is the
// (subscription_id, period_index) idempotency key, enforced where the money is
// rather than in a table of its own.
//
// entry.RequestID is the second lock, and the one a customer's own button needs:
// a sweep derives it from the period, so a retry reads a higher period and gets
// a different key, while a person pressing renew twice sends the same id and is
// answered with the first call's result instead of a second charge. It is
// checked first, exactly like PurchasePlanWithBalance, because a replay has to
// answer with what happened rather than with a conflict.
//
// sub carries the terms to write: the new expiry, the new period index and, on
// the fallback path, the price written back into the snapshot. The counters are
// not in that set and are not touched here.
func (r *StateRepo) RenewSubscriptionWithBalance(sub model.UserSubscription, expectedPeriodIndex int, entry model.BalanceEntry) (PurchaseResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return PurchaseResult{}, fmt.Errorf("begin renewal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if entry.RequestID != "" {
		settled, err := ledgerEntryByRequest(tx, entry.UserID, entry.RequestID)
		if err != nil {
			return PurchaseResult{}, err
		}
		if settled != nil {
			current, err := activeSubscriptionTx(tx, entry.UserID)
			if err != nil {
				return PurchaseResult{}, err
			}
			return PurchaseResult{Entry: *settled, Subscription: current, Replayed: true}, nil
		}
	}

	entry, err = chargeInTx(tx, entry)
	if err != nil {
		return PurchaseResult{}, err
	}

	snapshot := sub.PlanSnapshotJSON
	if snapshot == "" {
		snapshot = "{}"
	}
	result, err := tx.Exec(`
		UPDATE user_subscriptions SET
			plan_snapshot_json = ?,
			expires_at_ns      = ?,
			period_index       = ?,
			status             = ?
		WHERE id = ? AND period_index = ?
	`, snapshot, sub.ExpiresAtNs, sub.PeriodIndex, model.SubscriptionStatusActive,
		sub.ID, expectedPeriodIndex)
	if err != nil {
		return PurchaseResult{}, fmt.Errorf("extend subscription: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		// Another writer renewed it first, or the row is gone. Both mean this
		// charge must not stand, and the deferred rollback is what stops it.
		return PurchaseResult{}, fmt.Errorf("%w: the subscription was renewed by another writer", ErrConflict)
	}

	if err := tx.Commit(); err != nil {
		return PurchaseResult{}, fmt.Errorf("commit renewal: %w", err)
	}
	return PurchaseResult{Entry: entry, Subscription: sub}, nil
}

// SetSubscriptionAutoRenew flips the customer's own renewal switch.
//
// It is a single-column write on purpose: the switch has nothing to do with the
// money or the window, and writing the whole row back would carry whatever
// period_used_bytes the caller happened to read.
func (r *StateRepo) SetSubscriptionAutoRenew(id string, autoRenew bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(
		"UPDATE user_subscriptions SET auto_renew = ? WHERE id = ?", autoRenew, id)
	if err != nil {
		return fmt.Errorf("set auto renew: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		exists, checkErr := r.subscriptionRowExists(id)
		if checkErr != nil {
			return fmt.Errorf("check subscription %s: %w", id, checkErr)
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}

// ListRenewableSubscriptions returns the subscriptions a sweep should look at:
// active, opted in, and inside the lead time before their expiry.
//
// A subscription with no expiry (expires_at_ns = 0) is never renewable: it does
// not run out, so there is nothing to renew.
func (r *StateRepo) ListRenewableSubscriptions(nowNs, withinNs int64) ([]model.UserSubscription, error) {
	rows, err := r.db.Query("SELECT "+userSubscriptionColumns+` FROM user_subscriptions
		WHERE status = ? AND auto_renew = 1 AND expires_at_ns > 0 AND expires_at_ns <= ?
		ORDER BY expires_at_ns`, model.SubscriptionStatusActive, nowNs+withinNs)
	if err != nil {
		return nil, fmt.Errorf("list renewable subscriptions: %w", err)
	}
	defer rows.Close()

	result := []model.UserSubscription{}
	for rows.Next() {
		sub, err := scanUserSubscription(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, sub)
	}
	return result, rows.Err()
}

// chargeInTx applies one negative balance movement inside an open transaction.
//
// It is the body of ApplyBalanceDelta with the transaction handed in, and it
// repeats the write rather than calling it for the reason stated at the top of
// this file: the point is that the charge and whatever it bought commit
// together.
func chargeInTx(tx *sql.Tx, entry model.BalanceEntry) (model.BalanceEntry, error) {
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
	return entry, nil
}

// ledgerEntryByRequest reads the movement a request_id settled, inside a
// transaction. A nil result with a nil error means it has not been settled.
func ledgerEntryByRequest(tx *sql.Tx, userID, requestID string) (*model.BalanceEntry, error) {
	row := tx.QueryRow("SELECT "+balanceLedgerColumns+`
		FROM balance_ledger WHERE user_id = ? AND request_id = ?`, userID, requestID)
	entry, err := scanBalanceEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read ledger by request: %w", err)
	}
	return &entry, nil
}

// activeSubscriptionTx reads the user's active subscription inside a
// transaction. A user without one gets the zero row rather than an error: a
// replay of a purchase whose subscription has since been ended still has to
// answer, and "nothing is in force" is a true answer.
func activeSubscriptionTx(tx *sql.Tx, userID string) (model.UserSubscription, error) {
	row := tx.QueryRow("SELECT "+userSubscriptionColumns+
		" FROM user_subscriptions WHERE user_id = ? AND status = ?",
		userID, model.SubscriptionStatusActive)
	sub, err := scanUserSubscription(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.UserSubscription{}, nil
	}
	if err != nil {
		return model.UserSubscription{}, fmt.Errorf("read active subscription: %w", err)
	}
	return sub, nil
}
