package state

import (
	"database/sql"
	"fmt"
	"strings"

	"prism/internal/model"
)

// StateRepo methods for the multi-tenant tables created by 000017_tenant.
//
// Conventions match repo_state.go: every write takes r.mu so the single write
// connection sees one writer at a time, uniqueness violations come back as
// ErrConflict, and a missing row comes back as ErrNotFound. Batch writes run in
// one transaction.
//
// Slice fields (plan platforms, IP tiers, key platforms and IP allowlists) are
// stored as JSON arrays. A nil slice round-trips as an empty slice, which is
// how "not restricted" is represented.

// --- users ---

// InsertUser persists a new user. A duplicate username returns ErrConflict.
func (r *StateRepo) InsertUser(u model.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
		INSERT INTO users (id, username, password_hash, role, status, totp_secret_enc,
		                   created_at_ns, updated_at_ns, last_login_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, u.ID, u.Username, u.PasswordHash, u.Role, u.Status, u.TOTPSecretEnc,
		u.CreatedAtNs, u.UpdatedAtNs, u.LastLoginNs)
	if isSQLiteUniqueConstraint(err) {
		return fmt.Errorf("%w: username already exists", ErrConflict)
	}
	return err
}

// UpdateUser replaces the mutable fields of an existing user. Username is
// unique, so renaming onto a taken name returns ErrConflict.
func (r *StateRepo) UpdateUser(u model.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(`
		UPDATE users SET
			username        = ?,
			password_hash   = ?,
			role            = ?,
			status          = ?,
			totp_secret_enc = ?,
			updated_at_ns   = ?,
			last_login_ns   = ?
		WHERE id = ?
	`, u.Username, u.PasswordHash, u.Role, u.Status, u.TOTPSecretEnc,
		u.UpdatedAtNs, u.LastLoginNs, u.ID)
	if isSQLiteUniqueConstraint(err) {
		return fmt.Errorf("%w: username already exists", ErrConflict)
	}
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const userColumns = `id, username, password_hash, role, status, totp_secret_enc,
	created_at_ns, updated_at_ns, last_login_ns`

func scanUser(row interface{ Scan(...any) error }) (model.User, error) {
	var u model.User
	var totp []byte
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Status, &totp,
		&u.CreatedAtNs, &u.UpdatedAtNs, &u.LastLoginNs)
	if err != nil {
		return model.User{}, err
	}
	if len(totp) > 0 {
		u.TOTPSecretEnc = totp
	}
	return u, nil
}

// GetUser loads one user by ID.
func (r *StateRepo) GetUser(id string) (*model.User, error) {
	row := r.db.QueryRow("SELECT "+userColumns+" FROM users WHERE id = ?", id)
	u, err := scanUser(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return &u, nil
}

// GetUserByName loads one user by username. Login uses this to find the account
// before verifying the password.
func (r *StateRepo) GetUserByName(username string) (*model.User, error) {
	row := r.db.QueryRow("SELECT "+userColumns+" FROM users WHERE username = ?", username)
	u, err := scanUser(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user by name: %w", err)
	}
	return &u, nil
}

// ListUsers returns every user ordered by username.
func (r *StateRepo) ListUsers() ([]model.User, error) {
	rows, err := r.db.Query("SELECT " + userColumns + " FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []model.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

// CountUsers returns how many users exist. Initialization uses it to decide
// whether the built-in admin still has to be created.
func (r *StateRepo) CountUsers() (int, error) {
	var count int
	if err := r.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return count, nil
}

// DeleteUser removes a user. Subscriptions, access keys, sessions and orders
// cascade; usage rows survive because they carry no foreign key.
func (r *StateRepo) DeleteUser(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM users WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- plans ---

// UpsertPlan inserts or updates a plan by ID. A duplicate name returns
// ErrConflict. CreatedAtNs is preserved on update.
func (r *StateRepo) UpsertPlan(p model.Plan) error {
	platforms, err := encodeStringSliceJSON(p.Platforms)
	if err != nil {
		return fmt.Errorf("encode plan platforms: %w", err)
	}
	ipTiers, err := encodeStringSliceJSON(p.IPTiers)
	if err != nil {
		return fmt.Errorf("encode plan ip tiers: %w", err)
	}
	if p.Period == "" {
		p.Period = "30d"
	}
	if p.Status == "" {
		p.Status = "active"
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	_, err = r.db.Exec(`
		INSERT INTO plans (id, name, platforms_json, ip_tiers_json, traffic_bytes, period,
		                   max_concurrent, max_rps, max_bandwidth_bps, max_keys, duration_days,
		                   status, created_at_ns, updated_at_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name              = excluded.name,
			platforms_json    = excluded.platforms_json,
			ip_tiers_json     = excluded.ip_tiers_json,
			traffic_bytes     = excluded.traffic_bytes,
			period            = excluded.period,
			max_concurrent    = excluded.max_concurrent,
			max_rps           = excluded.max_rps,
			max_bandwidth_bps = excluded.max_bandwidth_bps,
			max_keys          = excluded.max_keys,
			duration_days     = excluded.duration_days,
			status            = excluded.status,
			updated_at_ns     = excluded.updated_at_ns
	`, p.ID, p.Name, platforms, ipTiers, p.TrafficBytes, p.Period,
		p.MaxConcurrent, p.MaxRPS, p.MaxBandwidthBps, p.MaxKeys, p.DurationDays,
		p.Status, p.CreatedAtNs, p.UpdatedAtNs)
	if isSQLiteUniqueConstraint(err) {
		return fmt.Errorf("%w: plan name already exists", ErrConflict)
	}
	return err
}

const planColumns = `id, name, platforms_json, ip_tiers_json, traffic_bytes, period,
	max_concurrent, max_rps, max_bandwidth_bps, max_keys, duration_days,
	status, created_at_ns, updated_at_ns`

func scanPlan(row interface{ Scan(...any) error }) (model.Plan, error) {
	var p model.Plan
	var platformsJSON, ipTiersJSON string
	var maxBandwidth sql.NullInt64
	err := row.Scan(&p.ID, &p.Name, &platformsJSON, &ipTiersJSON, &p.TrafficBytes, &p.Period,
		&p.MaxConcurrent, &p.MaxRPS, &maxBandwidth, &p.MaxKeys, &p.DurationDays,
		&p.Status, &p.CreatedAtNs, &p.UpdatedAtNs)
	if err != nil {
		return model.Plan{}, err
	}
	if p.Platforms, err = decodeStringSliceJSON(platformsJSON); err != nil {
		return model.Plan{}, fmt.Errorf("decode plan platforms: %w", err)
	}
	if p.IPTiers, err = decodeStringSliceJSON(ipTiersJSON); err != nil {
		return model.Plan{}, fmt.Errorf("decode plan ip tiers: %w", err)
	}
	if maxBandwidth.Valid {
		p.MaxBandwidthBps = maxBandwidth.Int64
	}
	return p, nil
}

// GetPlan loads one plan by ID.
func (r *StateRepo) GetPlan(id string) (*model.Plan, error) {
	row := r.db.QueryRow("SELECT "+planColumns+" FROM plans WHERE id = ?", id)
	p, err := scanPlan(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get plan: %w", err)
	}
	return &p, nil
}

// ListPlans returns every plan ordered by name.
func (r *StateRepo) ListPlans() ([]model.Plan, error) {
	rows, err := r.db.Query("SELECT " + planColumns + " FROM plans ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []model.Plan{}
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// DeletePlan removes a plan. Subscriptions opened under it keep their snapshot
// and have plan_id cleared.
func (r *StateRepo) DeletePlan(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM plans WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkUserLogin records when an account last authenticated.
//
// It is a targeted UPDATE rather than a read-modify-write of the whole row: the
// login path runs on every successful authentication, and rewriting the row
// would also rewrite the password hash and the second factor with values read a
// moment earlier.
func (r *StateRepo) MarkUserLogin(id string, atNs int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(`
		UPDATE users SET last_login_ns = ? WHERE id = ?
	`, atNs, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- user_subscriptions ---

// UpsertUserSubscription inserts or updates a subscription by ID.
func (r *StateRepo) UpsertUserSubscription(s model.UserSubscription) error {
	snapshot := strings.TrimSpace(s.PlanSnapshotJSON)
	if snapshot == "" {
		snapshot = "{}"
	}
	if s.Status == "" {
		s.Status = model.SubscriptionStatusActive
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
		INSERT INTO user_subscriptions (id, user_id, plan_id, plan_snapshot_json, starts_at_ns,
		                                expires_at_ns, period_start_ns, period_used_bytes, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			user_id            = excluded.user_id,
			plan_id            = excluded.plan_id,
			plan_snapshot_json = excluded.plan_snapshot_json,
			starts_at_ns       = excluded.starts_at_ns,
			expires_at_ns      = excluded.expires_at_ns,
			period_start_ns    = excluded.period_start_ns,
			period_used_bytes  = excluded.period_used_bytes,
			status             = excluded.status
	`, s.ID, s.UserID, nullableString(s.PlanID), snapshot, s.StartsAtNs,
		s.ExpiresAtNs, s.PeriodStartNs, s.PeriodUsedBytes, s.Status)
	return err
}

const userSubscriptionColumns = `id, user_id, plan_id, plan_snapshot_json, starts_at_ns,
	expires_at_ns, period_start_ns, period_used_bytes, status`

func scanUserSubscription(row interface{ Scan(...any) error }) (model.UserSubscription, error) {
	var s model.UserSubscription
	var planID sql.NullString
	err := row.Scan(&s.ID, &s.UserID, &planID, &s.PlanSnapshotJSON, &s.StartsAtNs,
		&s.ExpiresAtNs, &s.PeriodStartNs, &s.PeriodUsedBytes, &s.Status)
	if err != nil {
		return model.UserSubscription{}, err
	}
	if planID.Valid {
		s.PlanID = planID.String
	}
	return s, nil
}

// GetUserSubscription loads one subscription by ID.
func (r *StateRepo) GetUserSubscription(id string) (*model.UserSubscription, error) {
	row := r.db.QueryRow("SELECT "+userSubscriptionColumns+" FROM user_subscriptions WHERE id = ?", id)
	s, err := scanUserSubscription(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user subscription: %w", err)
	}
	return &s, nil
}

// GetActiveUserSubscription returns the user's active subscription, if any.
// A user has at most one at a time.
func (r *StateRepo) GetActiveUserSubscription(userID string) (*model.UserSubscription, error) {
	row := r.db.QueryRow(
		"SELECT "+userSubscriptionColumns+" FROM user_subscriptions WHERE user_id = ? AND status = ?",
		userID, model.SubscriptionStatusActive,
	)
	s, err := scanUserSubscription(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get active user subscription: %w", err)
	}
	return &s, nil
}

// ListUserSubscriptions returns every subscription ordered by user.
func (r *StateRepo) ListUserSubscriptions() ([]model.UserSubscription, error) {
	rows, err := r.db.Query("SELECT " + userSubscriptionColumns + " FROM user_subscriptions ORDER BY user_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []model.UserSubscription{}
	for rows.Next() {
		s, err := scanUserSubscription(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// AddSubscriptionUsedBytes adds delta to the subscription's period usage.
// A negative delta is clamped so the counter cannot go below zero.
func (r *StateRepo) AddSubscriptionUsedBytes(id string, delta int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(`
		UPDATE user_subscriptions
		SET period_used_bytes = MAX(0, period_used_bytes + ?)
		WHERE id = ?
	`, delta, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResetSubscriptionPeriod starts a new usage period for a subscription.
//
// It zeroes the period's usage and moves the period start in one statement, so
// a reader never sees a reset counter still labelled with the old period. The
// guard on period_start_ns is what keeps two workers - or a worker racing an
// operator - from resetting the same period twice and wiping usage that belongs
// to the period that just began.
//
// It reports ErrNotFound when the subscription does not exist, and nil when the
// period had already moved on: a lost race is not a failure.
func (r *StateRepo) ResetSubscriptionPeriod(id string, fromPeriodStartNs, toPeriodStartNs int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(`
		UPDATE user_subscriptions
		SET period_start_ns = ?, period_used_bytes = 0
		WHERE id = ? AND period_start_ns = ?
	`, toPeriodStartNs, id, fromPeriodStartNs)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n > 0 {
		return nil
	}

	// Nothing was updated. Either the subscription is gone, or its period moved
	// between the read and this write. The two are told apart so a caller can
	// log a genuine missing row without logging every lost race.
	var exists int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM user_subscriptions WHERE id = ?`, id).Scan(&exists); err != nil {
		return fmt.Errorf("check subscription %s: %w", id, err)
	}
	if exists == 0 {
		return ErrNotFound
	}
	return nil
}

// --- access_keys ---

// normalizeAccessKey fills the defaults Insert and Update share, so the two
// cannot drift apart and leave an update writing an empty scope or status into
// the CHECK constraints.
func normalizeAccessKey(k model.AccessKey) model.AccessKey {
	if k.Scope == "" {
		k.Scope = model.KeyScopeProxy
	}
	if k.Status == "" {
		k.Status = model.KeyStatusActive
	}
	return k
}

// InsertAccessKey persists a new key. SecretHash is required: the plaintext
// secret is never stored.
func (r *StateRepo) InsertAccessKey(k model.AccessKey) error {
	if len(k.SecretHash) == 0 {
		return fmt.Errorf("access key %s: secret hash is empty", k.ID)
	}
	k = normalizeAccessKey(k)
	platforms, err := encodeOptionalStringSliceJSON(k.Platforms)
	if err != nil {
		return fmt.Errorf("encode key platforms: %w", err)
	}
	allowlist, err := encodeOptionalStringSliceJSON(k.IPAllowlist)
	if err != nil {
		return fmt.Errorf("encode key ip allowlist: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	_, err = r.db.Exec(`
		INSERT INTO access_keys (id, user_id, name, secret_hash, platforms_json, ip_allowlist_json,
		                         scope, status, expires_at_ns, last_used_ns, created_at_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, k.ID, k.UserID, k.Name, k.SecretHash, platforms, allowlist,
		k.Scope, k.Status, k.ExpiresAtNs, k.LastUsedNs, k.CreatedAtNs)
	return err
}

// UpdateAccessKey replaces the mutable fields of an existing key. SecretHash is
// written as well, which is how key rotation lands.
func (r *StateRepo) UpdateAccessKey(k model.AccessKey) error {
	k = normalizeAccessKey(k)
	platforms, err := encodeOptionalStringSliceJSON(k.Platforms)
	if err != nil {
		return fmt.Errorf("encode key platforms: %w", err)
	}
	allowlist, err := encodeOptionalStringSliceJSON(k.IPAllowlist)
	if err != nil {
		return fmt.Errorf("encode key ip allowlist: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(`
		UPDATE access_keys SET
			name              = ?,
			secret_hash       = ?,
			platforms_json    = ?,
			ip_allowlist_json = ?,
			scope             = ?,
			status            = ?,
			expires_at_ns     = ?,
			last_used_ns      = ?
		WHERE id = ?
	`, k.Name, k.SecretHash, platforms, allowlist, k.Scope, k.Status,
		k.ExpiresAtNs, k.LastUsedNs, k.ID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const accessKeyColumns = `id, user_id, name, secret_hash, platforms_json, ip_allowlist_json,
	scope, status, expires_at_ns, last_used_ns, created_at_ns`

func scanAccessKey(row interface{ Scan(...any) error }) (model.AccessKey, error) {
	var k model.AccessKey
	var platformsJSON, allowlistJSON sql.NullString
	err := row.Scan(&k.ID, &k.UserID, &k.Name, &k.SecretHash, &platformsJSON, &allowlistJSON,
		&k.Scope, &k.Status, &k.ExpiresAtNs, &k.LastUsedNs, &k.CreatedAtNs)
	if err != nil {
		return model.AccessKey{}, err
	}
	if k.Platforms, err = decodeOptionalStringSliceJSON(platformsJSON); err != nil {
		return model.AccessKey{}, fmt.Errorf("decode key platforms: %w", err)
	}
	if k.IPAllowlist, err = decodeOptionalStringSliceJSON(allowlistJSON); err != nil {
		return model.AccessKey{}, fmt.Errorf("decode key ip allowlist: %w", err)
	}
	return k, nil
}

// GetAccessKey loads one key by ID.
func (r *StateRepo) GetAccessKey(id string) (*model.AccessKey, error) {
	row := r.db.QueryRow("SELECT "+accessKeyColumns+" FROM access_keys WHERE id = ?", id)
	k, err := scanAccessKey(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get access key: %w", err)
	}
	return &k, nil
}

// ListAccessKeysByUser returns the user's keys ordered by creation time.
func (r *StateRepo) ListAccessKeysByUser(userID string) ([]model.AccessKey, error) {
	rows, err := r.db.Query(
		"SELECT "+accessKeyColumns+" FROM access_keys WHERE user_id = ? ORDER BY created_at_ns",
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectAccessKeys(rows)
}

// ListActiveAccessKeys returns every enabled key. The data plane loads these
// into memory at startup.
func (r *StateRepo) ListActiveAccessKeys() ([]model.AccessKey, error) {
	rows, err := r.db.Query(
		"SELECT "+accessKeyColumns+" FROM access_keys WHERE status = ? ORDER BY created_at_ns",
		model.KeyStatusActive,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectAccessKeys(rows)
}

func collectAccessKeys(rows *sql.Rows) ([]model.AccessKey, error) {
	result := []model.AccessKey{}
	for rows.Next() {
		k, err := scanAccessKey(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, k)
	}
	return result, rows.Err()
}

// DeleteAccessKey removes a key. Usage rows referencing it are kept.
func (r *StateRepo) DeleteAccessKey(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM access_keys WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- usage ---

// UpsertUsageHourly adds the given hourly rows to the table. Counters are
// summed into any existing row, never replaced, so a flush that overlaps a
// previous one cannot lose traffic.
func (r *StateRepo) UpsertUsageHourly(rows []model.UsageRow) error {
	return r.upsertUsage(`
		INSERT INTO usage_hourly (key_id, user_id, platform_id, hour_ts_ns,
		                          ingress_bytes, egress_bytes, requests, errors)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key_id, platform_id, hour_ts_ns) DO UPDATE SET
			ingress_bytes = ingress_bytes + excluded.ingress_bytes,
			egress_bytes  = egress_bytes  + excluded.egress_bytes,
			requests      = requests      + excluded.requests,
			errors        = errors        + excluded.errors
	`, rows)
}

// UpsertUsageDaily adds the given daily rows to the table, with the same
// summing semantics as UpsertUsageHourly.
func (r *StateRepo) UpsertUsageDaily(rows []model.UsageRow) error {
	return r.upsertUsage(`
		INSERT INTO usage_daily (key_id, user_id, platform_id, day_ts_ns,
		                         ingress_bytes, egress_bytes, requests, errors)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key_id, platform_id, day_ts_ns) DO UPDATE SET
			ingress_bytes = ingress_bytes + excluded.ingress_bytes,
			egress_bytes  = egress_bytes  + excluded.egress_bytes,
			requests      = requests      + excluded.requests,
			errors        = errors        + excluded.errors
	`, rows)
}

// upsertUsage runs one batch insert in a single transaction. An empty batch is
// a no-op rather than an error, because the flush path calls it every tick.
func (r *StateRepo) upsertUsage(query string, rows []model.UsageRow) error {
	if len(rows) == 0 {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin usage upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(query)
	if err != nil {
		return fmt.Errorf("prepare usage upsert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, row := range rows {
		if _, err := stmt.Exec(row.KeyID, row.UserID, row.PlatformID, row.TSNs,
			row.IngressBytes, row.EgressBytes, row.Requests, row.Errors); err != nil {
			return fmt.Errorf("upsert usage %s/%s: %w", row.KeyID, row.PlatformID, err)
		}
	}
	return tx.Commit()
}

// SumUsageHourlyBefore aggregates hourly rows older than cutoffNs into daily
// rows, bucketing by UTC day. It is the read half of the hourly-to-daily
// summary and returns rows ready for UpsertUsageDaily.
func (r *StateRepo) SumUsageHourlyBefore(cutoffNs int64) ([]model.UsageRow, error) {
	rows, err := r.db.Query(`
		SELECT key_id, user_id, platform_id,
		       (hour_ts_ns / 86400000000000) * 86400000000000 AS day_ts_ns,
		       SUM(ingress_bytes), SUM(egress_bytes), SUM(requests), SUM(errors)
		FROM usage_hourly
		WHERE hour_ts_ns < ?
		GROUP BY key_id, user_id, platform_id, day_ts_ns
	`, cutoffNs)
	if err != nil {
		return nil, fmt.Errorf("sum usage hourly: %w", err)
	}
	defer rows.Close()

	result := []model.UsageRow{}
	for rows.Next() {
		var row model.UsageRow
		if err := rows.Scan(&row.KeyID, &row.UserID, &row.PlatformID, &row.TSNs,
			&row.IngressBytes, &row.EgressBytes, &row.Requests, &row.Errors); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// DeleteUsageHourlyBefore removes hourly rows older than cutoffNs and returns
// how many were removed.
func (r *StateRepo) DeleteUsageHourlyBefore(cutoffNs int64) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM usage_hourly WHERE hour_ts_ns < ?", cutoffNs)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return n, nil
}

// DeleteUsageDailyBefore removes daily rows older than cutoffNs and returns how
// many were removed.
func (r *StateRepo) DeleteUsageDailyBefore(cutoffNs int64) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM usage_daily WHERE day_ts_ns < ?", cutoffNs)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return n, nil
}

// ListUsageByUser returns the user's usage summed per key, platform and UTC
// day in the given window, newest first. The console reads this for the usage
// chart.
//
// Both tables are read and summed together: usage_hourly holds the recent days
// that have not aged out of the hourly window yet, usage_daily holds the
// summarized older ones. Each hour row lives in exactly one of them -- the
// summary deletes the hourly rows it folded in -- so summing both sources
// cannot double-count. It also means the day the cutoff falls in is correct
// even though it is split across the two tables.
func (r *StateRepo) ListUsageByUser(userID string, fromNs, toNs int64) ([]model.UsageRow, error) {
	rows, err := r.db.Query(`
		SELECT key_id, user_id, platform_id, day_ts_ns,
		       SUM(ingress_bytes), SUM(egress_bytes), SUM(requests), SUM(errors)
		FROM (
			SELECT key_id, user_id, platform_id, day_ts_ns,
			       ingress_bytes, egress_bytes, requests, errors
			FROM usage_daily
			WHERE user_id = ? AND day_ts_ns >= ? AND day_ts_ns < ?
			UNION ALL
			SELECT key_id, user_id, platform_id,
			       (hour_ts_ns / 86400000000000) * 86400000000000 AS day_ts_ns,
			       ingress_bytes, egress_bytes, requests, errors
			FROM usage_hourly
			WHERE user_id = ? AND hour_ts_ns >= ? AND hour_ts_ns < ?
		)
		GROUP BY key_id, user_id, platform_id, day_ts_ns
		ORDER BY day_ts_ns DESC
	`, userID, fromNs, toNs, userID, fromNs, toNs)
	if err != nil {
		return nil, fmt.Errorf("list usage by user: %w", err)
	}
	defer rows.Close()

	result := []model.UsageRow{}
	for rows.Next() {
		var row model.UsageRow
		if err := rows.Scan(&row.KeyID, &row.UserID, &row.PlatformID, &row.TSNs,
			&row.IngressBytes, &row.EgressBytes, &row.Requests, &row.Errors); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// --- orders ---

// InsertOrder records a manual top-up or renewal.
func (r *StateRepo) InsertOrder(o model.Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
		INSERT INTO orders (id, user_id, plan_id, amount_minor, currency, note, created_by, created_at_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, o.ID, o.UserID, nullableString(o.PlanID), o.AmountMinor, o.Currency, o.Note,
		o.CreatedBy, o.CreatedAtNs)
	return err
}

// ListOrdersByUser returns the user's orders, newest first.
func (r *StateRepo) ListOrdersByUser(userID string) ([]model.Order, error) {
	rows, err := r.db.Query(`
		SELECT id, user_id, plan_id, amount_minor, currency, note, created_by, created_at_ns
		FROM orders WHERE user_id = ? ORDER BY created_at_ns DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []model.Order{}
	for rows.Next() {
		var o model.Order
		var planID sql.NullString
		if err := rows.Scan(&o.ID, &o.UserID, &planID, &o.AmountMinor, &o.Currency, &o.Note,
			&o.CreatedBy, &o.CreatedAtNs); err != nil {
			return nil, err
		}
		if planID.Valid {
			o.PlanID = planID.String
		}
		result = append(result, o)
	}
	return result, rows.Err()
}

// --- sessions ---

// InsertSession records a console session. Only id_hash is stored.
func (r *StateRepo) InsertSession(s model.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
		INSERT INTO sessions (id_hash, user_id, created_at_ns, expires_at_ns, ip, ua)
		VALUES (?, ?, ?, ?, ?, ?)
	`, s.IDHash, s.UserID, s.CreatedAtNs, s.ExpiresAtNs, s.IP, s.UA)
	return err
}

// GetSession loads one session by the hash of its ID.
func (r *StateRepo) GetSession(idHash string) (*model.Session, error) {
	row := r.db.QueryRow(`
		SELECT id_hash, user_id, created_at_ns, expires_at_ns, ip, ua
		FROM sessions WHERE id_hash = ?
	`, idHash)
	var s model.Session
	err := row.Scan(&s.IDHash, &s.UserID, &s.CreatedAtNs, &s.ExpiresAtNs, &s.IP, &s.UA)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	return &s, nil
}

// DeleteSession removes one session by the hash of its ID.
func (r *StateRepo) DeleteSession(idHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM sessions WHERE id_hash = ?", idHash)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSessionsByUser clears every session of one user. Changing a password or
// disabling an account calls this.
func (r *StateRepo) DeleteSessionsByUser(userID string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM sessions WHERE user_id = ?", userID)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return n, nil
}

// PruneSessions removes sessions that expired before nowNs.
// TouchSession pushes a session's expiry back.
//
// It is a targeted UPDATE rather than a read-modify-write: sliding expiry runs
// in the second half of every session's window, and rewriting the row would also
// rewrite the user id and the recorded address with values read a moment
// earlier.
func (r *StateRepo) TouchSession(idHash string, expiresAtNs int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(`
		UPDATE sessions SET expires_at_ns = ? WHERE id_hash = ?
	`, expiresAtNs, idHash)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *StateRepo) PruneSessions(nowNs int64) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM sessions WHERE expires_at_ns < ?", nowNs)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return n, nil
}

// --- helpers ---

// nullableString renders an empty string as SQL NULL. plan_id is nullable so a
// deleted plan can clear it while the snapshot keeps the subscription valid.
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// encodeOptionalStringSliceJSON encodes a slice that distinguishes "unset" from
// "empty": a nil slice becomes SQL NULL, an empty one becomes "[]". Key
// platforms use this so an unset field means "the plan decides".
func encodeOptionalStringSliceJSON(values []string) (any, error) {
	if values == nil {
		return nil, nil
	}
	encoded, err := encodeStringSliceJSON(values)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// decodeOptionalStringSliceJSON decodes a nullable JSON array. NULL and an
// empty string both decode to a nil slice, meaning "unset".
func decodeOptionalStringSliceJSON(value sql.NullString) ([]string, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	return decodeStringSliceJSON(value.String)
}

// --- console reads added for the multi-tenant console ---

// ListSessionsByUser returns one user's live console sessions, newest first.
//
// Only the session id hash is stored, so that hash is what identifies a session
// to its owner; the value that was handed out at login is never in the database
// and cannot be reconstructed here.
func (r *StateRepo) ListSessionsByUser(userID string) ([]model.Session, error) {
	rows, err := r.db.Query(`
		SELECT id_hash, user_id, created_at_ns, expires_at_ns, ip, ua
		FROM sessions
		WHERE user_id = ?
		ORDER BY created_at_ns DESC, id_hash
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list sessions by user: %w", err)
	}
	defer rows.Close()

	result := []model.Session{}
	for rows.Next() {
		var s model.Session
		if err := rows.Scan(&s.IDHash, &s.UserID, &s.CreatedAtNs, &s.ExpiresAtNs, &s.IP, &s.UA); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// DeleteUserSession ends one session, but only if it belongs to the user named.
//
// The ownership test is in the WHERE clause rather than in a preceding read, so
// a caller that guesses a session id cannot end someone else's session: the
// delete simply matches nothing.
func (r *StateRepo) DeleteUserSession(userID, idHash string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM sessions WHERE user_id = ? AND id_hash = ?", userID, idHash)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return n, nil
}

// ListActiveUserSubscriptions returns every active subscription, oldest first.
//
// It is the read behind "which users are on a plan right now": a user has at
// most one active subscription, and the ones that are not active are history.
func (r *StateRepo) ListActiveUserSubscriptions() ([]model.UserSubscription, error) {
	rows, err := r.db.Query(
		"SELECT "+userSubscriptionColumns+" FROM user_subscriptions WHERE status = ? ORDER BY starts_at_ns DESC, id",
		model.SubscriptionStatusActive,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []model.UserSubscription{}
	for rows.Next() {
		s, err := scanUserSubscription(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// ListUsageDailyRange returns every user's usage in the window, one row per
// key, platform and UTC day.
//
// Both usage tables are read, for the same reason ListUsageByUser reads both:
// the recent days are still hourly rows, the older ones have been summarized
// into daily rows, and a given hour lives in exactly one of the two. The
// console folds these rows into per-user, per-platform and per-day totals.
func (r *StateRepo) ListUsageDailyRange(fromNs, toNs int64) ([]model.UsageRow, error) {
	rows, err := r.db.Query(`
		SELECT key_id, user_id, platform_id, day_ts_ns,
		       SUM(ingress_bytes), SUM(egress_bytes), SUM(requests), SUM(errors)
		FROM (
			SELECT key_id, user_id, platform_id, day_ts_ns,
			       ingress_bytes, egress_bytes, requests, errors
			FROM usage_daily
			WHERE day_ts_ns >= ? AND day_ts_ns < ?
			UNION ALL
			SELECT key_id, user_id, platform_id,
			       (hour_ts_ns / 86400000000000) * 86400000000000 AS day_ts_ns,
			       ingress_bytes, egress_bytes, requests, errors
			FROM usage_hourly
			WHERE hour_ts_ns >= ? AND hour_ts_ns < ?
		)
		GROUP BY key_id, user_id, platform_id, day_ts_ns
		ORDER BY day_ts_ns DESC, user_id
	`, fromNs, toNs, fromNs, toNs)
	if err != nil {
		return nil, fmt.Errorf("list usage range: %w", err)
	}
	defer rows.Close()

	result := []model.UsageRow{}
	for rows.Next() {
		var row model.UsageRow
		if err := rows.Scan(&row.KeyID, &row.UserID, &row.PlatformID, &row.TSNs,
			&row.IngressBytes, &row.EgressBytes, &row.Requests, &row.Errors); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// DeleteUserSubscription removes one subscription row.
//
// Usage is deliberately left alone: usage rows carry no foreign key to a
// subscription, so the traffic a user already generated stays accountable after
// the subscription that allowed it is gone.
func (r *StateRepo) DeleteUserSubscription(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM user_subscriptions WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
