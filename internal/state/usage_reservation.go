package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"prism/internal/model"
)

// ReserveUsage commits before the proxy may write to either peer. The runtime
// serializes a user's allowance and period with this operation.
func (r *StateRepo) ReserveUsage(v model.UsageReservation) error {
	if v.ID == "" || v.UserID == "" || v.KeyID == "" || v.Bytes <= 0 {
		return fmt.Errorf("invalid usage reservation")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if v.SubscriptionID != "" {
		var count int
		err = tx.QueryRow(`SELECT count(*) FROM user_subscriptions WHERE id=? AND user_id=? AND period_start_ns=? AND status='active'`, v.SubscriptionID, v.UserID, v.PeriodStartNs).Scan(&count)
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("usage period changed")
		}
	}
	_, err = tx.Exec(`INSERT INTO usage_reservations VALUES(?,?,?,?,?,?,?,?,?)`, v.ID, v.KeyID, v.UserID, v.PlatformID, v.SubscriptionID, v.PeriodStartNs, v.HourNs, v.Bytes, v.Ingress)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// SettleUsage is idempotent: deleting the reservation and recording its actual
// bytes share one transaction. A retry after an uncertain commit cannot debit
// twice. A late completion records history but never charges a newer period.
func (r *StateRepo) SettleUsage(id string, actual int64) (bool, error) {
	return r.settleUsage(id, actual, "", 0)
}

func (r *StateRepo) ReconcileUsage(id string, actual int64, reason string, atNs int64) error {
	if reason == "" || len(reason) > 512 {
		return fmt.Errorf("an incident/evidence reference is required")
	}
	_, err := r.settleUsage(id, actual, reason, atNs)
	return err
}

func (r *StateRepo) settleUsage(id string, actual int64, reason string, atNs int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var v model.UsageReservation
	err = tx.QueryRow(`SELECT id,key_id,user_id,platform_id,subscription_id,period_start_ns,hour_ns,bytes,ingress FROM usage_reservations WHERE id=?`, id).Scan(&v.ID, &v.KeyID, &v.UserID, &v.PlatformID, &v.SubscriptionID, &v.PeriodStartNs, &v.HourNs, &v.Bytes, &v.Ingress)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if actual < 0 || actual > v.Bytes {
		return false, fmt.Errorf("actual bytes outside reservation")
	}
	var ingress, egress int64
	if v.Ingress {
		ingress = actual
	} else {
		egress = actual
	}
	if actual > 0 {
		_, err = tx.Exec(`INSERT INTO usage_hourly(key_id,user_id,platform_id,hour_ts_ns,ingress_bytes,egress_bytes,requests,errors) VALUES(?,?,?,?,?,?,0,0)
   ON CONFLICT(key_id,platform_id,hour_ts_ns) DO UPDATE SET
   ingress_bytes=ingress_bytes+MIN(excluded.ingress_bytes,9223372036854775807-ingress_bytes),
   egress_bytes=egress_bytes+MIN(excluded.egress_bytes,9223372036854775807-egress_bytes)`, v.KeyID, v.UserID, v.PlatformID, v.HourNs, ingress, egress)
		if err != nil {
			return false, err
		}
	}
	res, err := tx.Exec(`UPDATE user_subscriptions SET period_used_bytes=period_used_bytes+MIN(?,9223372036854775807-period_used_bytes) WHERE id=? AND user_id=? AND period_start_ns=?`, actual, v.SubscriptionID, v.UserID, v.PeriodStartNs)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(`DELETE FROM usage_reservations WHERE id=?`, id); err != nil {
		return false, err
	}
	if reason != "" {
		detail, _ := json.Marshal(map[string]any{"reason": reason, "reserved_bytes": v.Bytes, "actual_bytes": actual})
		if _, err = tx.Exec(`INSERT INTO audit_log(at_ns,actor,user_id,remote_addr,action,target,detail_json) VALUES(?,'offline-owner',?,'local','offline.reconcile_usage',?,?)`, atNs, v.UserID, id, string(detail)); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return n > 0, nil
}

// PendingUsage exposes unresolved liability without credentials. Startup must
// refuse these users: a dead process cannot know which in-flight bytes arrived.
func (r *StateRepo) PendingUsage() ([]model.UsageReservation, error) {
	rows, err := r.db.Query(`SELECT id,key_id,user_id,platform_id,subscription_id,period_start_ns,hour_ns,bytes,ingress FROM usage_reservations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.UsageReservation
	for rows.Next() {
		var v model.UsageReservation
		if err = rows.Scan(&v.ID, &v.KeyID, &v.UserID, &v.PlatformID, &v.SubscriptionID, &v.PeriodStartNs, &v.HourNs, &v.Bytes, &v.Ingress); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
