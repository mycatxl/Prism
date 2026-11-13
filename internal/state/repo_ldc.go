package state

import (
	"database/sql"
	"errors"
	"fmt"

	"prism/internal/model"
)

const ldcDepositColumns = `out_trade_no, user_id, amount_micro, status,
    trade_no, created_at_ns, paid_at_ns, payment_micro, rate, merchant_pid, verification_key`

// InsertLDCDeposit records a pending EasyPay checkout before its redirect is
// returned to the caller.
func (r *StateRepo) InsertLDCDeposit(deposit model.LDCDeposit) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
		INSERT INTO ldc_deposits (`+ldcDepositColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, deposit.OutTradeNo, deposit.UserID, deposit.AmountMicro, deposit.Status,
		deposit.TradeNo, deposit.CreatedAtNs, deposit.PaidAtNs, deposit.PaymentMicro, deposit.Rate, deposit.MerchantPID, deposit.VerificationKey)
	if isSQLiteUniqueConstraint(err) || isSQLitePrimaryKeyConstraint(err) {
		return fmt.Errorf("%w: LDC deposit already exists", ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("insert LDC deposit: %w", err)
	}
	return nil
}

// ReserveLDCDepositTrade binds a provider trade number to one pending intent
// before its balance credit is attempted. The unique trade_no index prevents a
// single provider transaction from settling two different out_trade_no values.
// Repeating the same reservation is a no-op so a callback can resume after a
// process crash between reservation and credit.
func (r *StateRepo) ReserveLDCDepositTrade(outTradeNo, tradeNo string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(`
		UPDATE ldc_deposits SET trade_no = ?
		WHERE out_trade_no = ? AND status = ? AND (trade_no = '' OR trade_no = ?)
	`, tradeNo, outTradeNo, model.LDCDepositStatusPending, tradeNo)
	if isSQLiteUniqueConstraint(err) {
		return fmt.Errorf("%w: LDC trade number is already attached to another deposit", ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("reserve LDC trade number: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read LDC trade reservation result: %w", err)
	}
	if affected != 0 {
		return nil
	}

	var status, storedTradeNo string
	err = r.db.QueryRow(`SELECT status, trade_no FROM ldc_deposits WHERE out_trade_no = ?`, outTradeNo).
		Scan(&status, &storedTradeNo)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read LDC trade reservation: %w", err)
	}
	if storedTradeNo == tradeNo &&
		(status == model.LDCDepositStatusPending || status == model.LDCDepositStatusPaid) {
		return nil
	}
	return fmt.Errorf("%w: LDC deposit is attached to another trade number", ErrConflict)
}

// GetLDCDeposit returns the checkout attached to outTradeNo.
func (r *StateRepo) GetLDCDeposit(outTradeNo string) (model.LDCDeposit, error) {
	row := r.db.QueryRow(`SELECT `+ldcDepositColumns+`
		FROM ldc_deposits WHERE out_trade_no = ?`, outTradeNo)
	deposit, err := scanLDCDeposit(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.LDCDeposit{}, ErrNotFound
	}
	if err != nil {
		return model.LDCDeposit{}, fmt.Errorf("read LDC deposit: %w", err)
	}
	return deposit, nil
}

// MarkLDCDepositPaid records the provider trade number after the balance
// ledger accepted the credit. Repeating the same settlement is a no-op.
func (r *StateRepo) MarkLDCDepositPaid(outTradeNo, tradeNo string, paidAtNs int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(`
		UPDATE ldc_deposits
		SET status = ?,
		    trade_no = CASE WHEN trade_no = '' THEN ? ELSE trade_no END,
		    paid_at_ns = ?
		WHERE out_trade_no = ? AND status = ?
	`, model.LDCDepositStatusPaid, tradeNo, paidAtNs, outTradeNo, model.LDCDepositStatusPending)
	if isSQLiteUniqueConstraint(err) {
		return fmt.Errorf("%w: LDC trade number already settled", ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("settle LDC deposit: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read LDC settlement result: %w", err)
	}
	if affected != 0 {
		return nil
	}

	var status, storedTradeNo string
	err = r.db.QueryRow(`SELECT status, trade_no FROM ldc_deposits WHERE out_trade_no = ?`, outTradeNo).
		Scan(&status, &storedTradeNo)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read LDC settlement: %w", err)
	}
	if status == model.LDCDepositStatusPaid && (storedTradeNo == tradeNo || tradeNo == "") {
		return nil
	}
	return fmt.Errorf("%w: LDC deposit is already settled", ErrConflict)
}

func scanLDCDeposit(row interface{ Scan(...any) error }) (model.LDCDeposit, error) {
	var deposit model.LDCDeposit
	err := row.Scan(&deposit.OutTradeNo, &deposit.UserID, &deposit.AmountMicro,
		&deposit.Status, &deposit.TradeNo, &deposit.CreatedAtNs, &deposit.PaidAtNs, &deposit.PaymentMicro, &deposit.Rate, &deposit.MerchantPID, &deposit.VerificationKey)
	return deposit, err
}

// CreditLDCDeposit binds the provider transaction, credits the ledger and marks
// the order paid in one transaction. Replays never create another balance entry.
func (r *StateRepo) CreditLDCDeposit(outTradeNo, tradeNo string, entry model.BalanceEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := scanLDCDeposit(tx.QueryRow(`SELECT `+ldcDepositColumns+` FROM ldc_deposits WHERE out_trade_no=?`, outTradeNo))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if tradeNo == "" {
		tradeNo = d.TradeNo
		if tradeNo == "" {
			tradeNo = outTradeNo
		}
	}
	if d.TradeNo != "" && d.TradeNo != tradeNo {
		return ErrConflict
	}
	if d.Status == model.LDCDepositStatusPaid {
		return nil
	}
	if d.Status != model.LDCDepositStatusPending {
		return ErrConflict
	}
	_, err = tx.Exec(`UPDATE ldc_deposits SET status=?,trade_no=?,paid_at_ns=? WHERE out_trade_no=?`, model.LDCDepositStatusPaid, tradeNo, entry.CreatedAtNs, outTradeNo)
	if isSQLiteUniqueConstraint(err) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	entry.UserID, entry.DeltaMicro, entry.RefID, entry.Reason = d.UserID, d.AmountMicro, outTradeNo, model.BalanceReasonDeposit
	// Resume an order whose older implementation committed its ledger but
	// crashed before updating status. Validate the existing credit first.
	var user string
	var amount int64
	err = tx.QueryRow(`SELECT user_id,delta_micro FROM balance_ledger WHERE reason=? AND ref_id=?`, entry.Reason, outTradeNo).Scan(&user, &amount)
	if err == nil {
		if user != d.UserID || amount != d.AmountMicro {
			return ErrConflict
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		if _, err = applyBalanceDeltaTx(tx, entry); err != nil {
			return err
		}
	} else {
		return err
	}
	return tx.Commit()
}

// BindLegacyLDCCredentials preserves pre-upgrade checkout verification before rotation.
func (r *StateRepo) BindLegacyLDCCredentials(pid, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec(`UPDATE ldc_deposits SET merchant_pid=?,verification_key=? WHERE verification_key=''`, pid, key)
	return err
}
