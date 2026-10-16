package state

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"prism/internal/keys"
	"prism/internal/model"
)

// --- 000021_subscription_backfill_and_totp_replay ---

// stopBeforeBackfill migrates a fresh database to the release before 000021, so
// the rows a legacy deployment would hold can be seeded with the old shape.
func stopBeforeBackfill(t *testing.T, db *sql.DB) {
	t.Helper()
	m := newStateMigrator(t, db)
	if err := m.Migrate(stateVersionIntegrityFixes); err != nil {
		t.Fatalf("migrate to %d: %v", stateVersionIntegrityFixes, err)
	}
}

func seedMigrationUser(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO users (id, username, password_hash, role, status, created_at_ns, updated_at_ns)
		VALUES (?, ?, 'hash', 'user', 'active', 1, 1)
	`, id, "name-"+id); err != nil {
		t.Fatalf("seed user %s: %v", id, err)
	}
}

func seedMigrationKey(t *testing.T, db *sql.DB, id, userID string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO access_keys (id, user_id, name, secret_hash, scope, status, created_at_ns)
		VALUES (?, ?, 'default', X'00', 'proxy', 'active', 1)
	`, id, userID); err != nil {
		t.Fatalf("seed access key %s: %v", id, err)
	}
}

func seedMigrationSubscription(t *testing.T, db *sql.DB, id, userID, status string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO user_subscriptions (
			id, user_id, plan_id, plan_snapshot_json, starts_at_ns, expires_at_ns,
			period_start_ns, period_used_bytes, status
		) VALUES (?, ?, NULL, '{}', 1, 2, 1, 0, ?)
	`, id, userID, status); err != nil {
		t.Fatalf("seed subscription %s: %v", id, err)
	}
}

func countSubscriptions(t *testing.T, db *sql.DB, id string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_subscriptions WHERE id = ?`, id).Scan(&count); err != nil {
		t.Fatalf("count subscription %s: %v", id, err)
	}
	return count
}

// An account that has keys and no subscription row is the shape the loader reads
// as the zero snapshot: unlimited. The migration gives it the row that says so
// out loud, so that "no row at all" can mean "no plan in force" from now on.
func TestMigrateStateDB_BackfillsLegacyUnlimitedSubscription(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stopBeforeBackfill(t, db)

	// Three shapes, and only the first one may be given a row.
	seedMigrationUser(t, db, "u-key")
	seedMigrationKey(t, db, "k-u-key", "u-key")

	// No key, no subscription: an account the operator has not put on a plan.
	seedMigrationUser(t, db, "u-nokey")

	// A key and a retired subscription row: nothing to backfill.
	seedMigrationUser(t, db, "u-ended")
	seedMigrationKey(t, db, "k-u-ended", "u-ended")
	seedMigrationSubscription(t, db, "us-ended", "u-ended", model.SubscriptionStatusExpired)

	if err := MigrateStateDB(db); err != nil {
		t.Fatalf("MigrateStateDB: %v", err)
	}
	assertStateMigrationVersion(t, db, stateLatestVersion)

	// The legacy account has exactly one row, and it is active with no expiry.
	var planID sql.NullString
	var snapshot, status string
	var startsAtNs, expiresAtNs, periodUsedBytes int64
	if err := db.QueryRow(`
		SELECT plan_id, plan_snapshot_json, starts_at_ns, expires_at_ns, period_used_bytes, status
		FROM user_subscriptions WHERE user_id = 'u-key'
	`).Scan(&planID, &snapshot, &startsAtNs, &expiresAtNs, &periodUsedBytes, &status); err != nil {
		t.Fatalf("the legacy account has no subscription after the migration: %v", err)
	}
	if status != model.SubscriptionStatusActive {
		t.Errorf("legacy subscription status: got %q, want %q", status, model.SubscriptionStatusActive)
	}
	if expiresAtNs != 0 {
		t.Errorf("legacy subscription expires_at_ns: got %d, want 0 (no expiry)", expiresAtNs)
	}
	if startsAtNs <= 0 {
		t.Errorf("legacy subscription starts_at_ns: got %d, want the migration time", startsAtNs)
	}
	if periodUsedBytes != 0 {
		t.Errorf("legacy subscription period_used_bytes: got %d, want 0", periodUsedBytes)
	}
	// plan_id is NULL: the column is a foreign key to plans(id), so naming a plan
	// that does not exist would fail the migration, and inventing one would put a
	// plan on sale that the operator never made.
	if planID.Valid {
		t.Errorf("legacy subscription plan_id: got %q, want NULL", planID.String)
	}

	const legacyID = "us_legacy_unlimited_u-key"
	if count := countSubscriptions(t, db, legacyID); count != 1 {
		t.Errorf("the backfilled row is identified by %q: got %d rows, want 1", legacyID, count)
	}

	// The snapshot is the shape the loader decodes, and it reads as a plan that
	// is in force and limited by nothing.
	var plan model.Plan
	if err := json.Unmarshal([]byte(snapshot), &plan); err != nil {
		t.Fatalf("the legacy snapshot does not decode as a plan: %v", err)
	}
	if plan.Name != "legacy-unlimited" {
		t.Errorf("legacy snapshot plan name: got %q, want %q", plan.Name, "legacy-unlimited")
	}
	if plan.Status != model.SubscriptionStatusActive {
		t.Errorf("legacy snapshot status: got %q, want %q", plan.Status, model.SubscriptionStatusActive)
	}
	if plan.TrafficBytes != 0 {
		t.Errorf("legacy snapshot traffic_bytes: got %d, want 0 (unlimited)", plan.TrafficBytes)
	}

	sub := model.UserSubscription{
		ID: legacyID, UserID: "u-key", PlanSnapshotJSON: snapshot,
		StartsAtNs: startsAtNs, ExpiresAtNs: expiresAtNs, Status: status,
	}
	live, err := keys.SnapshotFromSubscription(&sub)
	if err != nil {
		t.Fatalf("SnapshotFromSubscription on the legacy row: %v", err)
	}
	if live.Status != model.SubscriptionStatusActive {
		t.Errorf("live snapshot status: got %q, want %q", live.Status, model.SubscriptionStatusActive)
	}
	if live.ExpiresAtNs != 0 {
		t.Errorf("live snapshot expiry: got %d, want 0", live.ExpiresAtNs)
	}

	// End to end: the startup loader publishes the account's key as usable.
	repo := newStateRepo(db)
	store, err := keys.LoadFromState(repo, t.Logf)
	if err != nil {
		t.Fatalf("LoadFromState: %v", err)
	}
	keyState, ok := store.Get("k-u-key")
	if !ok {
		t.Fatal("the legacy account's key is not in the loaded store")
	}
	if !keyState.Usable(time.Now().UnixNano()) {
		t.Errorf("the legacy account's key is not usable after the migration: %+v", keyState.Plan)
	}

	// The two other shapes were left alone.
	for _, tc := range []struct {
		userID string
		want   int
	}{
		{"u-nokey", 0},
		{"u-ended", 1},
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM user_subscriptions WHERE user_id = ?`, tc.userID).Scan(&count); err != nil {
			t.Fatalf("count subscriptions of %s: %v", tc.userID, err)
		}
		if count != tc.want {
			t.Errorf("subscriptions of %s after the migration: got %d, want %d", tc.userID, count, tc.want)
		}
	}
}

// The second factor's reuse ledger has to exist on every upgraded database, and
// the down migration has to leave a database the old build can still open -
// removing what it wrote itself and nothing else.
func TestMigrateStateDB_SubscriptionBackfillAndTOTPDown(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stopBeforeBackfill(t, db)
	seedMigrationUser(t, db, "u-down")
	seedMigrationKey(t, db, "k-u-down", "u-down")
	// A second account that is already on a plan: the backfill must never touch
	// it, in either direction.
	seedMigrationUser(t, db, "u-op")
	seedMigrationKey(t, db, "k-u-op", "u-op")
	seedMigrationSubscription(t, db, "us-operator", "u-op", model.SubscriptionStatusSuspended)

	if err := MigrateStateDB(db); err != nil {
		t.Fatalf("MigrateStateDB: %v", err)
	}
	assertStateMigrationVersion(t, db, stateLatestVersion)
	if count := countSubscriptions(t, db, legacyIDFor("u-down")); count != 1 {
		t.Fatalf("the backfill did not run for u-down: got %d rows, want 1", count)
	}

	m := newStateMigrator(t, db)
	if err := m.Steps(-1); err != nil {
		t.Fatalf("down of %d: %v", stateLatestVersion, err)
	}
	assertStateMigrationVersion(t, db, stateVersionIntegrityFixes)

	if count := countSubscriptions(t, db, "us_legacy_unlimited_u-down"); count != 0 {
		t.Error("the backfilled row survived the down migration")
	}
	if count := countSubscriptions(t, db, "us-operator"); count != 1 {
		t.Error("the down migration removed a subscription an operator created")
	}
	if has, err := hasTableColumn(db, "users", "totp_last_used_ns"); err != nil {
		t.Fatalf("look for the TOTP column: %v", err)
	} else if has {
		t.Error("users.totp_last_used_ns survived the down migration")
	}

	// Up again: the schema is restored and the backfill is reproducible, which is
	// what makes the pair symmetric rather than one-way.
	if err := m.Up(); err != nil {
		t.Fatalf("up after down: %v", err)
	}
	assertStateMigrationVersion(t, db, stateLatestVersion)
	if has, err := hasTableColumn(db, "users", "totp_last_used_ns"); err != nil {
		t.Fatalf("look for the TOTP column after the second up: %v", err)
	} else if !has {
		t.Error("users.totp_last_used_ns is missing after the second up migration")
	}
	if count := countSubscriptions(t, db, legacyIDFor("u-down")); count != 1 {
		t.Errorf("the backfill after up/down/up: got %d rows, want 1", count)
	}
	if count := countSubscriptions(t, db, "us-operator"); count != 1 {
		t.Errorf("a second up must not add a row to an account already on a plan: got %d rows, want 1", count)
	}
}

// legacyIDFor names the row the 000021 backfill writes for one account.
func legacyIDFor(userID string) string {
	return "us_legacy_unlimited_" + userID
}

// The ledger starts empty, and an account that has never used a second factor
// reads it as zero rather than as a missing column.
func TestMigrateStateDB_TOTPLedgerStartsEmpty(t *testing.T) {
	repo := newTestStateRepo(t)
	user := mustInsertUser(t, repo, "u-totp", "totp")

	stored, err := repo.GetUser(user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if stored.TOTPLastUsedNs != 0 {
		t.Errorf("totp_last_used_ns of a new account: got %d, want 0", stored.TOTPLastUsedNs)
	}
}
