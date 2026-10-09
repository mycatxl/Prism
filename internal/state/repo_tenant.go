package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"prism/internal/model"
	"prism/internal/tenant"
)

// TenantRepo implements tenant.Store on state.db. Writes share StateRepo's
// mutex so state.db keeps a single writer.
type TenantRepo struct {
	db *sql.DB
	mu *sync.Mutex
}

var _ tenant.Store = (*TenantRepo)(nil)

// Tenant returns the multi-tenant store backed by state.db.
func (e *StateEngine) Tenant() *TenantRepo {
	return &TenantRepo{db: e.StateRepo.db, mu: &e.StateRepo.mu}
}

func isConflict(err error) bool {
	var sqlErr *sqlite.Error
	if !errors.As(err, &sqlErr) {
		return false
	}
	switch sqlErr.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
		return true
	}
	return false
}

func wrapWrite(err error) error {
	if err != nil && isConflict(err) {
		return fmt.Errorf("%w: %v", tenant.ErrConflict, err)
	}
	return err
}

func mustAffect(res sql.Result, err error) error {
	if err != nil {
		return wrapWrite(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return tenant.ErrNotFound
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return tenant.ErrNotFound
	}
	return err
}

// nullableJSON encodes nil as SQL NULL and a non-nil slice (even empty) as JSON.
func nullableJSON(v []string) (sql.NullString, error) {
	if v == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func decodeNullableJSON(ns sql.NullString) ([]string, error) {
	if !ns.Valid {
		return nil, nil
	}
	out := []string{}
	if err := json.Unmarshal([]byte(ns.String), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func jsonList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// --- users ---

const userColumns = `id, username, password_hash, role, status, totp_secret_enc, created_at_ns, updated_at_ns, last_login_at_ns`

func scanUser(s rowScanner) (*tenant.User, error) {
	var u tenant.User
	var role string
	if err := s.Scan(&u.ID, &u.Username, &u.PasswordHash, &role, &u.Status, &u.TOTPSecretEnc,
		&u.CreatedAtNs, &u.UpdatedAtNs, &u.LastLoginAtNs); err != nil {
		return nil, err
	}
	u.Role = tenant.Role(role)
	return &u, nil
}

type rowScanner interface{ Scan(dest ...any) error }

func (r *TenantRepo) CreateUser(u tenant.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec(`INSERT INTO users (`+userColumns+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		u.ID, u.Username, u.PasswordHash, string(u.Role), u.Status, u.TOTPSecretEnc,
		u.CreatedAtNs, u.UpdatedAtNs, u.LastLoginAtNs)
	return wrapWrite(err)
}

func (r *TenantRepo) GetUser(id string) (*tenant.User, error) {
	u, err := scanUser(r.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
	return u, notFound(err)
}

func (r *TenantRepo) GetUserByUsername(username string) (*tenant.User, error) {
	u, err := scanUser(r.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE username = ?`, username))
	return u, notFound(err)
}

func (r *TenantRepo) ListUsers() ([]tenant.User, error) {
	rows, err := r.db.Query(`SELECT ` + userColumns + ` FROM users ORDER BY created_at_ns, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenant.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (r *TenantRepo) UpdateUser(id string, p tenant.UserPatch, nowNs int64) error {
	sets := []string{"updated_at_ns = ?"}
	args := []any{nowNs}
	if p.Username != nil {
		sets, args = append(sets, "username = ?"), append(args, *p.Username)
	}
	if p.PasswordHash != nil {
		sets, args = append(sets, "password_hash = ?"), append(args, *p.PasswordHash)
	}
	if p.Role != nil {
		if id == tenant.BuiltinAdminUserID && *p.Role != tenant.RoleAdmin {
			return fmt.Errorf("tenant: built-in admin must keep the admin role")
		}
		sets, args = append(sets, "role = ?"), append(args, string(*p.Role))
	}
	if p.Status != nil {
		sets, args = append(sets, "status = ?"), append(args, *p.Status)
	}
	if p.TOTPSecretEnc != nil {
		sets, args = append(sets, "totp_secret_enc = ?"), append(args, *p.TOTPSecretEnc)
	}
	args = append(args, id)
	r.mu.Lock()
	defer r.mu.Unlock()
	return mustAffect(r.db.Exec(`UPDATE users SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...))
}

func (r *TenantRepo) TouchUserLogin(id string, nowNs int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return mustAffect(r.db.Exec(`UPDATE users SET last_login_at_ns = ? WHERE id = ?`, nowNs, id))
}

func (r *TenantRepo) DeleteUser(id string) error {
	if id == tenant.BuiltinAdminUserID {
		return fmt.Errorf("tenant: built-in admin cannot be deleted")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return mustAffect(r.db.Exec(`DELETE FROM users WHERE id = ?`, id))
}

// --- plans ---

const planColumns = `id, name, platforms_json, ip_tiers_json, traffic_bytes, period, max_concurrent, max_rps,
	max_bandwidth_bps, max_keys, duration_days, status, created_at_ns, updated_at_ns`

func scanPlan(s rowScanner) (*tenant.Plan, error) {
	var p tenant.Plan
	var platforms, tiers string
	if err := s.Scan(&p.ID, &p.Name, &platforms, &tiers, &p.TrafficBytes, &p.Period, &p.MaxConcurrent,
		&p.MaxRPS, &p.MaxBandwidthBps, &p.MaxKeys, &p.DurationDays, &p.Status, &p.CreatedAtNs, &p.UpdatedAtNs); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(platforms), &p.Platforms); err != nil {
		return nil, fmt.Errorf("plan %s platforms_json: %w", p.ID, err)
	}
	if err := json.Unmarshal([]byte(tiers), &p.IPTiers); err != nil {
		return nil, fmt.Errorf("plan %s ip_tiers_json: %w", p.ID, err)
	}
	return &p, nil
}

func (r *TenantRepo) CreatePlan(p tenant.Plan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec(`INSERT INTO plans (`+planColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, jsonList(p.Platforms), jsonList(p.IPTiers), p.TrafficBytes, p.Period, p.MaxConcurrent,
		p.MaxRPS, p.MaxBandwidthBps, p.MaxKeys, p.DurationDays, p.Status, p.CreatedAtNs, p.UpdatedAtNs)
	return wrapWrite(err)
}

func (r *TenantRepo) UpdatePlan(p tenant.Plan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return mustAffect(r.db.Exec(`UPDATE plans SET name = ?, platforms_json = ?, ip_tiers_json = ?, traffic_bytes = ?,
		period = ?, max_concurrent = ?, max_rps = ?, max_bandwidth_bps = ?, max_keys = ?, duration_days = ?,
		status = ?, updated_at_ns = ? WHERE id = ?`,
		p.Name, jsonList(p.Platforms), jsonList(p.IPTiers), p.TrafficBytes, p.Period, p.MaxConcurrent,
		p.MaxRPS, p.MaxBandwidthBps, p.MaxKeys, p.DurationDays, p.Status, p.UpdatedAtNs, p.ID))
}

func (r *TenantRepo) GetPlan(id string) (*tenant.Plan, error) {
	p, err := scanPlan(r.db.QueryRow(`SELECT `+planColumns+` FROM plans WHERE id = ?`, id))
	return p, notFound(err)
}

func (r *TenantRepo) ListPlans() ([]tenant.Plan, error) {
	rows, err := r.db.Query(`SELECT ` + planColumns + ` FROM plans ORDER BY created_at_ns, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenant.Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *TenantRepo) CountActiveSubscribers() (map[string]int, error) {
	rows, err := r.db.Query(`SELECT plan_id, COUNT(*) FROM user_subscriptions WHERE status = 'active' GROUP BY plan_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// --- subscriptions ---

const subColumns = `id, user_id, plan_id, plan_snapshot_json, starts_at_ns, expires_at_ns, period_start_ns,
	period_used_bytes, status, created_at_ns, updated_at_ns`

func scanSub(s rowScanner) (*tenant.Subscription, error) {
	var sub tenant.Subscription
	var snap string
	if err := s.Scan(&sub.ID, &sub.UserID, &sub.PlanID, &snap, &sub.StartsAtNs, &sub.ExpiresAtNs,
		&sub.PeriodStartNs, &sub.PeriodUsedBytes, &sub.Status, &sub.CreatedAtNs, &sub.UpdatedAtNs); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(snap), &sub.Snapshot); err != nil {
		return nil, fmt.Errorf("subscription %s snapshot: %w", sub.ID, err)
	}
	return &sub, nil
}

func (r *TenantRepo) ActivateSubscription(s tenant.Subscription) error {
	snap, err := json.Marshal(s.Snapshot)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if s.Status == tenant.SubActive {
		if _, err := tx.Exec(`UPDATE user_subscriptions SET status = 'expired', updated_at_ns = ?
			WHERE user_id = ? AND status = 'active'`, s.UpdatedAtNs, s.UserID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO user_subscriptions (`+subColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.UserID, s.PlanID, string(snap), s.StartsAtNs, s.ExpiresAtNs, s.PeriodStartNs,
		s.PeriodUsedBytes, s.Status, s.CreatedAtNs, s.UpdatedAtNs); err != nil {
		return wrapWrite(err)
	}
	return tx.Commit()
}

func (r *TenantRepo) GetActiveSubscription(userID string) (*tenant.Subscription, error) {
	s, err := scanSub(r.db.QueryRow(`SELECT `+subColumns+` FROM user_subscriptions
		WHERE user_id = ? AND status = 'active'`, userID))
	return s, notFound(err)
}

func (r *TenantRepo) ListSubscriptions(userID string) ([]tenant.Subscription, error) {
	rows, err := r.db.Query(`SELECT `+subColumns+` FROM user_subscriptions WHERE user_id = ?
		ORDER BY created_at_ns DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenant.Subscription
	for rows.Next() {
		s, err := scanSub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *TenantRepo) SetSubscriptionStatus(id, status string, nowNs int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return mustAffect(r.db.Exec(`UPDATE user_subscriptions SET status = ?, updated_at_ns = ? WHERE id = ?`,
		status, nowNs, id))
}

func (r *TenantRepo) ExtendSubscription(id string, expiresAtNs, nowNs int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return mustAffect(r.db.Exec(`UPDATE user_subscriptions SET expires_at_ns = ?, updated_at_ns = ? WHERE id = ?`,
		expiresAtNs, nowNs, id))
}

func (r *TenantRepo) SyncPlanSnapshot(planID string, limits tenant.PlanLimits, nowNs int64) (int64, error) {
	snap, err := json.Marshal(limits)
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	res, err := r.db.Exec(`UPDATE user_subscriptions SET plan_snapshot_json = ?, updated_at_ns = ?
		WHERE plan_id = ? AND status = 'active'`, string(snap), nowNs, planID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *TenantRepo) ResetPeriod(id string, periodStartNs, nowNs int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return mustAffect(r.db.Exec(`UPDATE user_subscriptions SET period_start_ns = ?, period_used_bytes = 0,
		updated_at_ns = ? WHERE id = ?`, periodStartNs, nowNs, id))
}

func (r *TenantRepo) ExpireDue(nowNs int64) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows, err := r.db.Query(`UPDATE user_subscriptions SET status = 'expired', updated_at_ns = ?
		WHERE status = 'active' AND expires_at_ns <= ? RETURNING user_id`, nowNs, nowNs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// --- access keys ---

const keyColumns = `id, user_id, name, secret_hash, platforms_json, ip_allowlist_json, status, expires_at_ns,
	last_used_at_ns, created_at_ns, updated_at_ns`

func scanKey(s rowScanner) (*tenant.AccessKey, error) {
	var k tenant.AccessKey
	var platforms, allow sql.NullString
	if err := s.Scan(&k.ID, &k.UserID, &k.Name, &k.SecretHash, &platforms, &allow, &k.Status,
		&k.ExpiresAtNs, &k.LastUsedNs, &k.CreatedAtNs, &k.UpdatedAtNs); err != nil {
		return nil, err
	}
	var err error
	if k.Platforms, err = decodeNullableJSON(platforms); err != nil {
		return nil, fmt.Errorf("key %s platforms_json: %w", k.ID, err)
	}
	if k.IPAllowlist, err = decodeNullableJSON(allow); err != nil {
		return nil, fmt.Errorf("key %s ip_allowlist_json: %w", k.ID, err)
	}
	return &k, nil
}

func (r *TenantRepo) CreateKey(k tenant.AccessKey) error {
	platforms, err := nullableJSON(k.Platforms)
	if err != nil {
		return err
	}
	allow, err := nullableJSON(k.IPAllowlist)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err = r.db.Exec(`INSERT INTO access_keys (`+keyColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		k.ID, k.UserID, k.Name, k.SecretHash, platforms, allow, k.Status, k.ExpiresAtNs,
		k.LastUsedNs, k.CreatedAtNs, k.UpdatedAtNs)
	return wrapWrite(err)
}

func (r *TenantRepo) GetKey(userID, keyID string) (*tenant.AccessKey, error) {
	k, err := scanKey(r.db.QueryRow(`SELECT `+keyColumns+` FROM access_keys WHERE id = ? AND user_id = ?`,
		keyID, userID))
	return k, notFound(err)
}

func (r *TenantRepo) ListKeys(userID string) ([]tenant.AccessKey, error) {
	rows, err := r.db.Query(`SELECT `+keyColumns+` FROM access_keys WHERE user_id = ? ORDER BY created_at_ns, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenant.AccessKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

func (r *TenantRepo) CountKeys(userID string) (int, error) {
	var n int
	err := r.db.QueryRow(`SELECT COUNT(*) FROM access_keys WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

func (r *TenantRepo) UpdateKey(userID, keyID string, p tenant.KeyPatch, nowNs int64) error {
	sets := []string{"updated_at_ns = ?"}
	args := []any{nowNs}
	if p.Name != nil {
		sets, args = append(sets, "name = ?"), append(args, *p.Name)
	}
	if p.Platforms != nil {
		v, err := nullableJSON(*p.Platforms)
		if err != nil {
			return err
		}
		sets, args = append(sets, "platforms_json = ?"), append(args, v)
	}
	if p.IPAllowlist != nil {
		v, err := nullableJSON(*p.IPAllowlist)
		if err != nil {
			return err
		}
		sets, args = append(sets, "ip_allowlist_json = ?"), append(args, v)
	}
	if p.Status != nil {
		sets, args = append(sets, "status = ?"), append(args, *p.Status)
	}
	if p.ExpiresAtNs != nil {
		sets, args = append(sets, "expires_at_ns = ?"), append(args, *p.ExpiresAtNs)
	}
	args = append(args, keyID, userID)
	r.mu.Lock()
	defer r.mu.Unlock()
	return mustAffect(r.db.Exec(`UPDATE access_keys SET `+strings.Join(sets, ", ")+` WHERE id = ? AND user_id = ?`, args...))
}

func (r *TenantRepo) DeleteKey(userID, keyID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return mustAffect(r.db.Exec(`DELETE FROM access_keys WHERE id = ? AND user_id = ?`, keyID, userID))
}

func (r *TenantRepo) TouchKeysUsed(lastUsed map[string]int64) error {
	if len(lastUsed) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`UPDATE access_keys SET last_used_at_ns = ? WHERE id = ? AND last_used_at_ns < ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for id, ts := range lastUsed {
		if _, err := stmt.Exec(ts, id, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const authSnapshotSQL = `SELECT ` + `k.id, k.user_id, k.name, k.secret_hash, k.platforms_json, k.ip_allowlist_json, k.status,
	k.expires_at_ns, k.last_used_at_ns, k.created_at_ns, k.updated_at_ns,
	u.status, u.role,
	s.id, s.plan_id, s.plan_snapshot_json, s.starts_at_ns, s.expires_at_ns, s.period_start_ns,
	s.period_used_bytes, s.status, s.created_at_ns, s.updated_at_ns
FROM access_keys k
JOIN users u ON u.id = k.user_id
LEFT JOIN user_subscriptions s ON s.user_id = k.user_id AND s.status = 'active'`

func scanAuthRecord(s rowScanner) (*tenant.AuthRecord, error) {
	var rec tenant.AuthRecord
	k := &rec.Key
	var platforms, allow sql.NullString
	var role string
	var sID, sPlan, sSnap, sStatus sql.NullString
	var sStarts, sExpires, sPeriod, sUsed, sCreated, sUpdated sql.NullInt64
	if err := s.Scan(&k.ID, &k.UserID, &k.Name, &k.SecretHash, &platforms, &allow, &k.Status,
		&k.ExpiresAtNs, &k.LastUsedNs, &k.CreatedAtNs, &k.UpdatedAtNs,
		&rec.UserStatus, &role,
		&sID, &sPlan, &sSnap, &sStarts, &sExpires, &sPeriod, &sUsed, &sStatus, &sCreated, &sUpdated); err != nil {
		return nil, err
	}
	rec.UserRole = tenant.Role(role)
	var err error
	if k.Platforms, err = decodeNullableJSON(platforms); err != nil {
		return nil, fmt.Errorf("key %s platforms_json: %w", k.ID, err)
	}
	if k.IPAllowlist, err = decodeNullableJSON(allow); err != nil {
		return nil, fmt.Errorf("key %s ip_allowlist_json: %w", k.ID, err)
	}
	if sID.Valid {
		sub := &tenant.Subscription{
			ID: sID.String, UserID: k.UserID, PlanID: sPlan.String,
			StartsAtNs: sStarts.Int64, ExpiresAtNs: sExpires.Int64, PeriodStartNs: sPeriod.Int64,
			PeriodUsedBytes: sUsed.Int64, Status: sStatus.String,
			CreatedAtNs: sCreated.Int64, UpdatedAtNs: sUpdated.Int64,
		}
		if err := json.Unmarshal([]byte(sSnap.String), &sub.Snapshot); err != nil {
			return nil, fmt.Errorf("subscription %s snapshot: %w", sub.ID, err)
		}
		rec.Subscription = sub
	}
	return &rec, nil
}

func (r *TenantRepo) LoadAuthSnapshot() ([]tenant.AuthRecord, error) {
	rows, err := r.db.Query(authSnapshotSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenant.AuthRecord
	for rows.Next() {
		rec, err := scanAuthRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

func (r *TenantRepo) LoadAuthRecord(keyID string) (*tenant.AuthRecord, error) {
	rec, err := scanAuthRecord(r.db.QueryRow(authSnapshotSQL+` WHERE k.id = ?`, keyID))
	return rec, notFound(err)
}

// --- usage ---

func (r *TenantRepo) FlushUsage(deltas []tenant.UsageDelta, periodUsed map[string]int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if len(deltas) > 0 {
		stmt, err := tx.Prepare(`INSERT INTO usage_hourly
			(key_id, user_id, platform_id, hour_ts, ingress_bytes, egress_bytes, requests, errors)
			VALUES (?,?,?,?,?,?,?,?)
			ON CONFLICT(key_id, platform_id, hour_ts) DO UPDATE SET
				ingress_bytes = ingress_bytes + excluded.ingress_bytes,
				egress_bytes  = egress_bytes + excluded.egress_bytes,
				requests      = requests + excluded.requests,
				errors        = errors + excluded.errors`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, d := range deltas {
			if d.IngressBytes == 0 && d.EgressBytes == 0 && d.Requests == 0 && d.Errors == 0 {
				continue
			}
			hour := d.HourTs - d.HourTs%3600
			if _, err := stmt.Exec(d.KeyID, d.UserID, d.PlatformID, hour,
				d.IngressBytes, d.EgressBytes, d.Requests, d.Errors); err != nil {
				return err
			}
		}
	}
	if len(periodUsed) > 0 {
		stmt, err := tx.Prepare(`UPDATE user_subscriptions SET period_used_bytes = period_used_bytes + ? WHERE id = ?`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for id, n := range periodUsed {
			if n == 0 {
				continue
			}
			if _, err := stmt.Exec(n, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (r *TenantRepo) RollupUsage(cutoffTs int64) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO usage_daily
			(key_id, user_id, platform_id, day_ts, ingress_bytes, egress_bytes, requests, errors)
		SELECT key_id, MAX(user_id), platform_id, hour_ts - hour_ts % 86400,
			SUM(ingress_bytes), SUM(egress_bytes), SUM(requests), SUM(errors)
		FROM usage_hourly WHERE hour_ts < ?
		GROUP BY key_id, platform_id, hour_ts - hour_ts % 86400
		ON CONFLICT(key_id, platform_id, day_ts) DO UPDATE SET
			ingress_bytes = ingress_bytes + excluded.ingress_bytes,
			egress_bytes  = egress_bytes + excluded.egress_bytes,
			requests      = requests + excluded.requests,
			errors        = errors + excluded.errors`, cutoffTs); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM usage_hourly WHERE hour_ts < ?`, cutoffTs)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

func (r *TenantRepo) PruneUsageDaily(olderThanTs int64) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, err := r.db.Exec(`DELETE FROM usage_daily WHERE day_ts < ?`, olderThanTs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// QueryUsage reads both tables (hourly for recent data, daily for rolled-up
// data) and merges them into the requested buckets. Because rollup moves rows
// atomically, a bucket never appears in both tables.
func (r *TenantRepo) QueryUsage(f tenant.UsageFilter) ([]tenant.UsageRow, error) {
	var where []string
	var args []any
	if f.UserID != "" {
		where, args = append(where, "user_id = ?"), append(args, f.UserID)
	}
	if f.KeyID != "" {
		where, args = append(where, "key_id = ?"), append(args, f.KeyID)
	}
	if f.PlatformID != "" {
		where, args = append(where, "platform_id = ?"), append(args, f.PlatformID)
	}
	cond := func(tsCol string) (string, []any) {
		w := append([]string(nil), where...)
		a := append([]any(nil), args...)
		if f.FromTs > 0 {
			w, a = append(w, tsCol+" >= ?"), append(a, f.FromTs)
		}
		if f.ToTs > 0 {
			w, a = append(w, tsCol+" < ?"), append(a, f.ToTs)
		}
		if len(w) == 0 {
			return "", a
		}
		return " WHERE " + strings.Join(w, " AND "), a
	}
	group := ""
	switch f.GroupBy {
	case tenant.GroupKey:
		group = "key_id"
	case tenant.GroupUser:
		group = "user_id"
	case tenant.GroupPlatform:
		group = "platform_id"
	}
	bucket := "ts"
	if f.Daily {
		bucket = "ts - ts % 86400"
	}
	hw, ha := cond("hour_ts")
	dw, da := cond("day_ts")
	groupSel := "''"
	if group != "" {
		groupSel = group
	}
	q := `SELECT g, ` + bucket + ` AS b, SUM(i), SUM(e), SUM(rq), SUM(er) FROM (
		SELECT ` + groupSel + ` AS g, hour_ts AS ts, ingress_bytes AS i, egress_bytes AS e, requests AS rq, errors AS er FROM usage_hourly` + hw + `
		UNION ALL
		SELECT ` + groupSel + `, day_ts, ingress_bytes, egress_bytes, requests, errors FROM usage_daily` + dw + `
	) GROUP BY g, b ORDER BY b, g`
	rows, err := r.db.Query(q, append(ha, da...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenant.UsageRow
	for rows.Next() {
		var row tenant.UsageRow
		var g string
		if err := rows.Scan(&g, &row.BucketTs, &row.IngressBytes, &row.EgressBytes, &row.Requests, &row.Errors); err != nil {
			return nil, err
		}
		switch f.GroupBy {
		case tenant.GroupKey:
			row.KeyID = g
		case tenant.GroupUser:
			row.UserID = g
		case tenant.GroupPlatform:
			row.PlatformID = g
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// --- invites ---

func (r *TenantRepo) CreateInvite(inv tenant.Invite) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec(`INSERT INTO invites (code_hash, plan_id, max_uses, used_count, expires_at_ns, created_by, created_at_ns)
		VALUES (?,?,?,?,?,?,?)`, inv.CodeHash, nullString(inv.PlanID), inv.MaxUses, inv.UsedCount,
		inv.ExpiresAtNs, inv.CreatedBy, inv.CreatedAtNs)
	return wrapWrite(err)
}

const inviteColumns = `code_hash, COALESCE(plan_id, ''), max_uses, used_count, expires_at_ns, created_by, created_at_ns`

func scanInvite(s rowScanner) (*tenant.Invite, error) {
	var inv tenant.Invite
	err := s.Scan(&inv.CodeHash, &inv.PlanID, &inv.MaxUses, &inv.UsedCount, &inv.ExpiresAtNs, &inv.CreatedBy, &inv.CreatedAtNs)
	return &inv, err
}

func (r *TenantRepo) RedeemInvite(codeHash string, nowNs int64) (*tenant.Invite, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inv, err := scanInvite(r.db.QueryRow(`UPDATE invites SET used_count = used_count + 1
		WHERE code_hash = ? AND used_count < max_uses AND (expires_at_ns = 0 OR expires_at_ns > ?)
		RETURNING `+inviteColumns, codeHash, nowNs))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tenant.ErrInviteUnavailable
	}
	return inv, err
}

func (r *TenantRepo) ListInvites() ([]tenant.Invite, error) {
	rows, err := r.db.Query(`SELECT ` + inviteColumns + ` FROM invites ORDER BY created_at_ns DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenant.Invite
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *inv)
	}
	return out, rows.Err()
}

func (r *TenantRepo) DeleteInvite(codeHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return mustAffect(r.db.Exec(`DELETE FROM invites WHERE code_hash = ?`, codeHash))
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// --- orders ---

func (r *TenantRepo) CreateOrder(o tenant.Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec(`INSERT INTO orders (id, user_id, plan_id, amount, note, created_by, created_at_ns)
		VALUES (?,?,?,?,?,?,?)`, o.ID, o.UserID, nullString(o.PlanID), o.Amount, o.Note, o.CreatedBy, o.CreatedAtNs)
	return wrapWrite(err)
}

func (r *TenantRepo) ListOrders(userID string) ([]tenant.Order, error) {
	rows, err := r.db.Query(`SELECT id, user_id, COALESCE(plan_id, ''), amount, note, created_by, created_at_ns
		FROM orders WHERE user_id = ? ORDER BY created_at_ns DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenant.Order
	for rows.Next() {
		var o tenant.Order
		if err := rows.Scan(&o.ID, &o.UserID, &o.PlanID, &o.Amount, &o.Note, &o.CreatedBy, &o.CreatedAtNs); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// --- sessions ---

const sessionColumns = `id_hash, user_id, created_at_ns, expires_at_ns, ip, ua`

func scanSession(s rowScanner) (*tenant.Session, error) {
	var x tenant.Session
	err := s.Scan(&x.IDHash, &x.UserID, &x.CreatedAtNs, &x.ExpiresAtNs, &x.IP, &x.UA)
	return &x, err
}

func (r *TenantRepo) CreateSession(s tenant.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec(`INSERT INTO user_sessions (`+sessionColumns+`) VALUES (?,?,?,?,?,?)`,
		s.IDHash, s.UserID, s.CreatedAtNs, s.ExpiresAtNs, s.IP, s.UA)
	return wrapWrite(err)
}

func (r *TenantRepo) GetSession(idHash string, nowNs int64) (*tenant.Session, error) {
	s, err := scanSession(r.db.QueryRow(`SELECT `+sessionColumns+` FROM user_sessions
		WHERE id_hash = ? AND expires_at_ns > ?`, idHash, nowNs))
	if err != nil {
		return nil, notFound(err)
	}
	return s, nil
}

func (r *TenantRepo) ExtendSession(idHash string, expiresAtNs int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return mustAffect(r.db.Exec(`UPDATE user_sessions SET expires_at_ns = ? WHERE id_hash = ?`, expiresAtNs, idHash))
}

func (r *TenantRepo) DeleteSession(idHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec(`DELETE FROM user_sessions WHERE id_hash = ?`, idHash)
	return err
}

func (r *TenantRepo) DeleteUserSessions(userID string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, err := r.db.Exec(`DELETE FROM user_sessions WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *TenantRepo) ListUserSessions(userID string, nowNs int64) ([]tenant.Session, error) {
	rows, err := r.db.Query(`SELECT `+sessionColumns+` FROM user_sessions WHERE user_id = ? AND expires_at_ns > ?
		ORDER BY created_at_ns DESC`, userID, nowNs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenant.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *TenantRepo) PruneSessions(nowNs int64) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, err := r.db.Exec(`DELETE FROM user_sessions WHERE expires_at_ns <= ?`, nowNs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- audit ---

func (r *TenantRepo) AppendAudit(e model.AuditEntry) error {
	return (&StateRepo{db: r.db}).appendAuditLocked(r.mu, e)
}

func (r *TenantRepo) ListAuditByActor(actorUserID string, beforeID int64, limit int) ([]model.AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	if beforeID <= 0 {
		beforeID = maxInt64
	}
	rows, err := r.db.Query("SELECT "+auditLogColumns+" FROM audit_log WHERE actor_user_id = ? AND id < ? ORDER BY id DESC LIMIT ?",
		actorUserID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AuditEntry
	for rows.Next() {
		var e model.AuditEntry
		if err := scanAuditEntry(rows, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
