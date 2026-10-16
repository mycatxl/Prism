package state

import (
	"database/sql"
	"fmt"
	"runtime"
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

// UpdateUser replaces every mutable column of a user row in one statement.
// Username is unique, so renaming onto a taken name returns ErrConflict.
//
// A whole-row write is only safe for a caller that owns the row for the whole
// read-modify-write - seeding a fixture, importing a row. A caller that read
// the row, decided something, and is now writing it back must use one of the
// single-column writers below instead (UpdateUserPassword, UpdateUserStatus,
// UpdateUserRole, UpdateUserTOTPSecret). This statement also carries the columns
// that caller's read copied, so any write that landed in between - the second
// administrator disabling the account while this one resets a password - is
// silently reverted, and the answer still reads as a success. The login path and
// the subscription terms write made the same choice for the same reason
// (MarkUserLogin, UpdateUserSubscriptionTerms).
//
// totp_last_used_ns is deliberately not one of these columns: it is the replay
// ledger, it may only move forward (AdvanceTOTPLastUsed), and only a replaced
// secret may clear it (ResetTOTPLastUsed).
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
	created_at_ns, updated_at_ns, last_login_ns, totp_last_used_ns`

func scanUser(row interface{ Scan(...any) error }) (model.User, error) {
	var u model.User
	var totp []byte
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Status, &totp,
		&u.CreatedAtNs, &u.UpdatedAtNs, &u.LastLoginNs, &u.TOTPLastUsedNs)
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

// updateUserColumn writes one column of one user row and stamps updated_at_ns.
//
// The column name is a literal chosen by the callers below, never a value that
// came from a request, which is what keeps the concatenated statement safe.
//
// Zero rows means the account is gone, and that is checked rather than inferred
// from the count: a write that stores the value the row already holds is a
// success, and how a driver counts one is not something a caller should have to
// know (see userRowExists).
func (r *StateRepo) updateUserColumn(id, column string, value any, atNs int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(
		"UPDATE users SET "+column+" = ?, updated_at_ns = ? WHERE id = ?",
		value, atNs, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return r.userNotFoundIfAbsent(id)
	}
	return nil
}

// userRowExists reports whether a user row is still there. The caller holds the
// write lock.
func (r *StateRepo) userRowExists(id string) (bool, error) {
	var exists int
	if err := r.db.QueryRow("SELECT COUNT(1) FROM users WHERE id = ?", id).Scan(&exists); err != nil {
		return false, err
	}
	return exists > 0, nil
}

// userNotFoundIfAbsent turns "an UPDATE matched nothing" into the answer it
// stands for: ErrNotFound when the row is gone, and nil when it is there and the
// statement simply had nothing to change.
//
// This connection's SQLite counts a row an UPDATE matched whether or not the
// values it stores differ from the ones already in it: a write that stores the
// terms a subscription already holds is a normal, successful write
// (TestUpdateUserSubscriptionTerms_UnchangedTermsAreNotAMissingRow), and so is
// clearing a replay ledger that already reads zero
// (TestResetTOTPLastUsed_AlreadyZeroIsASuccess). The check is still made here
// rather than assumed, because "unchanged" and "absent" have opposite answers
// for a client and the difference is a driver detail - the same one
// ResetSubscriptionPeriod and RevokeUserSubscription resolve with a COUNT.
func (r *StateRepo) userNotFoundIfAbsent(id string) error {
	exists, err := r.userRowExists(id)
	if err != nil {
		return fmt.Errorf("check user %s: %w", id, err)
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// UpdateUserPassword writes the password hash of one account and nothing else.
//
// This is the write the console's password change and password reset use. The
// caller read the row, checked or policed a password, and hashed the new one -
// tens of milliseconds with argon2id - and every other column it copied belongs
// to whoever wrote it after that read: writing the whole row back would revert a
// concurrent status or role change silently, which is the one outcome a "my
// password was leaked, change it" flow must not have.
func (r *StateRepo) UpdateUserPassword(id, passwordHash string, atNs int64) error {
	return r.updateUserColumn(id, "password_hash", passwordHash, atNs)
}

// UpdateUserStatus writes the status of one account and nothing else.
func (r *StateRepo) UpdateUserStatus(id, status string, atNs int64) error {
	return r.updateUserColumn(id, "status", status, atNs)
}

// UpdateUserRole writes the role of one account and nothing else.
func (r *StateRepo) UpdateUserRole(id, role string, atNs int64) error {
	return r.updateUserColumn(id, "role", role, atNs)
}

// UpdateUserTOTPSecret writes the sealed second factor of one account and
// nothing else. A nil value clears it, which is how the factor is removed.
func (r *StateRepo) UpdateUserTOTPSecret(id string, sealed []byte, atNs int64) error {
	return r.updateUserColumn(id, "totp_secret_enc", sealed, atNs)
}

// ResetTOTPLastUsed clears the replay ledger of an account's second factor.
//
// The ledger is monotone by design, and this is the one place it moves backwards:
// it is called when the secret the ledger belongs to is replaced or removed
// (service.ConfirmTOTPEnrolment, service.DisableTOTP). A step spent by a code
// computed from the old secret says nothing about the new one, and leaving it
// behind would refuse a perfectly fresh code that happens to fall inside the same
// thirty-second step - the account would be refused with TOTP_ALREADY_USED in
// the one flow that exists to hand it a working second factor.
//
// AdvanceTOTPLastUsed is deliberately not reused for this: its guard is
// totp_last_used_ns < ?, so it can only raise the value, and asking it for a
// lower one is a no-op that reports the step as already spent.
func (r *StateRepo) ResetTOTPLastUsed(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("UPDATE users SET totp_last_used_ns = 0 WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		// A ledger that already reads zero is the ordinary case - a factor
		// enrolled on an account that never signed in with one - so this is
		// the path that must not read "nothing changed" as "no such row".
		return r.userNotFoundIfAbsent(id)
	}
	return nil
}

// AdvanceTOTPLastUsed spends the step a second factor's code belonged to, and
// reports whether this call is the one that spent it.
//
// A compare and set, not a write: the step being spent must be newer than the
// last one this account spent, so of two logins arriving with the same code
// exactly one is accepted, and a code from an earlier step (still inside the
// accepted tolerance) can never be replayed. The caller must treat false as a
// refusal, and an error as a refusal too: the state of the factor is then
// unknown, and letting an unknown state through is what this exists to prevent.
func (r *StateRepo) AdvanceTOTPLastUsed(userID string, stepNs int64) (bool, error) {
	if userID == "" {
		return false, ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(`
		UPDATE users SET totp_last_used_ns = ?
		WHERE id = ? AND totp_last_used_ns < ?
	`, stepNs, userID, stepNs)
	if err != nil {
		return false, err
	}
	if n, _ := result.RowsAffected(); n > 0 {
		return true, nil
	}

	// Nothing was updated: either the account does not exist or the step has
	// already been spent. The caller refuses either way; telling the two apart
	// is for the operator reading a log, not for the client.
	var exists int
	if err := r.db.QueryRow("SELECT COUNT(1) FROM users WHERE id = ?", userID).Scan(&exists); err != nil {
		return false, err
	}
	if exists == 0 {
		return false, ErrNotFound
	}
	return false, nil
}

// --- user_subscriptions ---

// UpsertUserSubscription inserts or updates a subscription by ID. A write that
// would leave the user with a second active row is ErrConflict: 000020's
// ux_user_subscriptions_active is the guard, and this is where its constraint
// error becomes a conflict the caller can answer with instead of a fault.
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
	if isSQLiteUniqueConstraint(err) {
		return fmt.Errorf("%w: the user already has an active subscription", ErrConflict)
	}
	return err
}

// UpdateUserSubscriptionTerms rewrites the terms of an existing subscription
// and leaves the usage counters alone: period_used_bytes and period_start_ns
// are never written here.
//
// The reason this method exists is that the counter has a concurrent writer.
// The flush worker adds to period_used_bytes with an UPDATE of its own
// (AddSubscriptionUsedBytes), while an operator's PATCH reads the row, changes
// a term and writes it back. UpsertUserSubscription writes every column, so
// that write-back carries the counter as it looked when the operator's read
// happened: any increment in between is overwritten and the bytes it counted
// are given away for free, permanently, because nothing recomputes the counter
// from the usage tables. Writing only the terms removes the race: the terms are
// not mutated behind the operator's back, only the counter is.
//
// The caller passes the complete terms (normally the row it read, with the
// fields it is changing applied). A missing row is ErrNotFound, and a set of
// terms that would leave a second active subscription behind is ErrConflict.
//
// Terms that change nothing are a write like any other: the console replaces a
// status with the status it already holds whenever it PATCHes {"status":"active"}
// on an active row, and that is a legal request. So zero rows affected is not
// read as "no such row" on its own - the row is checked, which is also what the
// other writers in this file do (ResetSubscriptionPeriod, RevokeUserSubscription,
// userNotFoundIfAbsent).
func (r *StateRepo) UpdateUserSubscriptionTerms(s model.UserSubscription) error {
	snapshot := strings.TrimSpace(s.PlanSnapshotJSON)
	if snapshot == "" {
		snapshot = "{}"
	}
	if s.Status == "" {
		s.Status = model.SubscriptionStatusActive
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(`
		UPDATE user_subscriptions SET
			user_id            = ?,
			plan_id            = ?,
			plan_snapshot_json = ?,
			starts_at_ns       = ?,
			expires_at_ns      = ?,
			status             = ?
		WHERE id = ?
	`, s.UserID, nullableString(s.PlanID), snapshot, s.StartsAtNs,
		s.ExpiresAtNs, s.Status, s.ID)
	if isSQLiteUniqueConstraint(err) {
		return fmt.Errorf("%w: the user already has an active subscription", ErrConflict)
	}
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		// Zero rows: the row is gone, or the write stored the terms it
		// already held. The console's PATCH {"status":"active"} on an
		// active row is exactly that second case and it is a legal
		// request, so the two are told apart rather than assumed, the way
		// ResetSubscriptionPeriod and RevokeUserSubscription do it.
		exists, checkErr := r.subscriptionRowExists(s.ID)
		if checkErr != nil {
			return fmt.Errorf("check subscription %s: %w", s.ID, checkErr)
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}

// subscriptionRowExists reports whether a subscription row is still there. The
// caller holds the write lock.
func (r *StateRepo) subscriptionRowExists(id string) (bool, error) {
	var exists int
	if err := r.db.QueryRow("SELECT COUNT(1) FROM user_subscriptions WHERE id = ?", id).Scan(&exists); err != nil {
		return false, err
	}
	return exists > 0, nil
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
	exists, err := r.subscriptionRowExists(id)
	if err != nil {
		return fmt.Errorf("check subscription %s: %w", id, err)
	}
	if !exists {
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

// SummarizeUsageHourlyBefore folds every hourly row older than cutoffNs into
// the daily table and deletes the rows it folded, in one transaction. The
// returned count is how many hourly rows were folded and removed.
//
// It is the whole summary in one statement pair, which is the point: the
// SumUsageHourlyBefore + UpsertUsageDaily + DeleteUsageHourlyBefore sequence it
// replaces takes the write lock three times, so a crash (or a failure of the
// third call) after the second one committed leaves the hourly rows in place
// while their traffic is already in the daily table. The next pass would sum
// those same rows again, and UpsertUsageDaily adds rather than replaces, so the
// day would read double forever: every usage page and every alert built on it
// would be wrong and nothing would repair itself. Both statements here run in
// one transaction, so the daily rows and the hourly rows always agree: either
// the traffic moved to the daily table and the hourly rows are gone, or nothing
// happened at all.
//
// Aggregation is the same as SumUsageHourlyBefore: one row per key, platform
// and UTC day, summed into whatever is already in the daily table.
func (r *StateRepo) SummarizeUsageHourlyBefore(cutoffNs int64) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin usage summary: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`
		INSERT INTO usage_daily (key_id, user_id, platform_id, day_ts_ns,
		                         ingress_bytes, egress_bytes, requests, errors)
		SELECT key_id, user_id, platform_id,
		       (hour_ts_ns / 86400000000000) * 86400000000000 AS day_ts_ns,
		       SUM(ingress_bytes), SUM(egress_bytes), SUM(requests), SUM(errors)
		FROM usage_hourly
		WHERE hour_ts_ns < ?
		GROUP BY key_id, user_id, platform_id, day_ts_ns
		ON CONFLICT(key_id, platform_id, day_ts_ns) DO UPDATE SET
			ingress_bytes = ingress_bytes + excluded.ingress_bytes,
			egress_bytes  = egress_bytes  + excluded.egress_bytes,
			requests      = requests      + excluded.requests,
			errors        = errors        + excluded.errors
	`, cutoffNs); err != nil {
		return 0, fmt.Errorf("write summarized usage: %w", err)
	}

	result, err := tx.Exec("DELETE FROM usage_hourly WHERE hour_ts_ns < ?", cutoffNs)
	if err != nil {
		return 0, fmt.Errorf("delete summarized usage: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count summarized usage: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit usage summary: %w", err)
	}
	return removed, nil
}

// stateDeleteBatch is how many rows one batched delete statement removes.
//
// A retention delete runs on the one connection every writer shares - logins,
// the flush worker and the console all queue behind it - so removing a whole
// table tail in a single statement would hold that connection for the length of
// the tail and undo all of it if it failed. The loops below delete in bounded
// batches instead, releasing the write lock between them.
//
// It is a var rather than a const so a test can shrink it and see that the loop
// really repeats instead of relying on one statement.
var stateDeleteBatch int64 = 5000

// deleteBeforeBatched removes every row of table whose matchColumn is older
// than cutoff and returns how many rows went away.
//
// The delete is split into batches of stateDeleteBatch rows: each batch is its
// own statement, so the write lock is released in between and a caller waiting
// for it only ever waits for one batch. Partial progress survives a failure
// instead of rolling back with the whole tail; a retry picks up the rows that
// are left, which is the only thing a retention delete promises. table and
// matchColumn are constants written in this package, never caller input.
func (r *StateRepo) deleteBeforeBatched(table, matchColumn string, cutoff int64) (int64, error) {
	query := fmt.Sprintf(`DELETE FROM %s WHERE rowid IN (
		SELECT rowid FROM %s WHERE %s < ? ORDER BY %s LIMIT ?)`, table, table, matchColumn, matchColumn)

	var total int64
	for {
		r.mu.Lock()
		result, err := r.db.Exec(query, cutoff, stateDeleteBatch)
		r.mu.Unlock()
		if err != nil {
			return total, fmt.Errorf("delete %s before cutoff: %w", table, err)
		}
		removed, err := result.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("count deleted %s rows: %w", table, err)
		}
		total += removed
		if removed < stateDeleteBatch {
			return total, nil
		}
		// A full batch means there is probably more: yield so a writer waiting
		// on r.mu is not starved for the whole prune.
		runtime.Gosched()
	}
}

// DeleteUsageHourlyBefore removes hourly rows older than cutoffNs and returns
// how many were removed. It deletes in batches, so the connection is available
// to the other writers between them.
func (r *StateRepo) DeleteUsageHourlyBefore(cutoffNs int64) (int64, error) {
	return r.deleteBeforeBatched("usage_hourly", "hour_ts_ns", cutoffNs)
}

// DeleteUsageDailyBefore removes daily rows older than cutoffNs and returns how
// many were removed. It deletes in batches, like DeleteUsageHourlyBefore.
func (r *StateRepo) DeleteUsageDailyBefore(cutoffNs int64) (int64, error) {
	return r.deleteBeforeBatched("usage_daily", "day_ts_ns", cutoffNs)
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

// PruneSessions removes sessions that expired before nowNs.
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
//
// The hourly side is filtered by overlap, not by bucket start: a row belongs to
// the window when any part of its hour does. Callers pass minute-precision
// windows (the alert scanner passes the last few minutes), and a row is a whole
// hour, so comparing the bucket start against the window would drop the hour
// the window is in and report nothing at all for windows shorter than an hour.
// The price is that the hour the window starts in is counted whole: an hourly
// row cannot say which minute of the hour it belongs to.
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
			WHERE hour_ts_ns + 3600000000000 > ? AND hour_ts_ns < ?
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

// DeleteUserSubscription removes one subscription row for good.
//
// It is no longer how the console ends a user's access: that revokes the row
// instead (RevokeUserSubscription), because an account left with no subscription
// row at all is the state that reads as unlimited. This stays for the callers
// that really want the row gone - a rollback, a test, a cleanup - and it is the
// only function that can take the row out of existence.
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

// RevokeUserSubscription takes a subscription out of force without deleting it.
//
// Deleting the row was how the console ended a user's access, and it left
// nothing behind: an account with no subscription row at all is the state the
// loader and the publish path read as the zero snapshot, which means no quota,
// no restriction and no expiry - unlimited. A user whose last subscription was
// deleted could therefore mint keys with no limit, which is the opposite of what
// deleting it was for.
//
// So the row stays and only its status moves to suspended, which is the status
// that already means "not active, and refused": keys.Usable refuses any snapshot
// whose status is not active. It is deliberately not a new status value and not
// a new column - the schema's CHECK constraint allows active, expired and
// suspended, and suspended is the one an operator can set and clear from the
// console. The window (expires_at_ns) is left as it was: it is history, and what
// ended the access was the decision, not the date.
//
// The decision row keeps deciding. A suspended row is still the newest of the
// user's rows, so a restart reaches it and refuses the keys, instead of finding
// no row at all and handing the zero snapshot back.
//
// A missing row is ErrNotFound. Re-revoking an already suspended row is not an
// error: the status is written unconditionally, so the call is idempotent.
func (r *StateRepo) RevokeUserSubscription(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(`
		UPDATE user_subscriptions SET status = ?
		WHERE id = ?
	`, model.SubscriptionStatusSuspended, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		// Zero rows here is a row that is not there. It is not the same-value
		// case: this connection's SQLite counts a row an UPDATE matched even
		// when the write stores what the row already holds, which
		// TestUpdateUserSubscriptionTerms_UnchangedTermsAreNotAMissingRow
		// settles, so a second revoke affects one row and never arrives here.
		// The check stays because the meaning of zero rows is worth stating
		// rather than inheriting, and it is what makes "re-revoking is not an
		// error" hold whatever a driver chooses to count.
		exists, checkErr := r.subscriptionRowExists(id)
		if checkErr != nil {
			return checkErr
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}
