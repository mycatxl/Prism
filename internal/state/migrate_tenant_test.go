package state

import (
	"database/sql"
	"testing"
)

// --- 000017_tenant: schema ---

// tenantTables lists every table 000017 is responsible for, plus the column it
// adds to an existing table.
var tenantTables = []string{
	"users",
	"plans",
	"user_subscriptions",
	"access_keys",
	"usage_hourly",
	"usage_daily",
	"orders",
	"sessions",
}

func TestMigrateStateDB_TenantTablesExist(t *testing.T) {
	repo := newTestStateRepo(t)
	db := repo.db

	for _, table := range tenantTables {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing after migration: %v", table, err)
		}
	}
}

// The billing subscription table must not reuse the name of the node feed
// table, which has existed since 000001.
func TestMigrateStateDB_SubscriptionNameIsNotReused(t *testing.T) {
	repo := newTestStateRepo(t)
	db := repo.db

	var columns []string
	rows, err := db.Query(`SELECT name FROM pragma_table_info('subscriptions')`)
	if err != nil {
		t.Fatalf("read subscriptions columns: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}

	// The node feed table keeps its own shape; no user_id may have been added
	// to it by the tenant migration.
	for _, column := range columns {
		if column == "user_id" || column == "plan_id" || column == "plan_snapshot_json" {
			t.Fatalf("subscriptions looks like the billing table: column %q present", column)
		}
	}

	// And the billing table exists separately.
	var found string
	if err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'user_subscriptions'`,
	).Scan(&found); err != nil {
		t.Fatalf("user_subscriptions missing: %v", err)
	}
}

func TestMigrateStateDB_AuditLogGainsUserID(t *testing.T) {
	repo := newTestStateRepo(t)
	db := repo.db

	var hasUserID bool
	rows, err := db.Query(`SELECT name FROM pragma_table_info('audit_log')`)
	if err != nil {
		t.Fatalf("read audit_log columns: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if column == "user_id" {
			hasUserID = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	if !hasUserID {
		t.Fatal("audit_log.user_id is missing after migration")
	}

	// The pre-existing columns must still be there: this is an extension, not a
	// replacement.
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('audit_log')
		WHERE name IN ('id', 'at_ns', 'actor', 'remote_addr', 'action', 'target', 'detail_json')
	`).Scan(&count); err != nil {
		t.Fatalf("count legacy audit columns: %v", err)
	}
	if count != 7 {
		t.Fatalf("legacy audit_log columns: got %d, want 7", count)
	}
}

// The migration must run against a database that already holds rows in the
// pre-existing tables, and it must not disturb them.
func TestMigrateStateDB_TenantUpgradeKeepsExistingRows(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Stop before the tenant migration, seed a platform and a node feed, then
	// run the production upgrade path.
	m := newStateMigrator(t, db)
	if err := m.Migrate(stateVersionDropExportProfiles); err != nil {
		t.Fatalf("migrate to %d: %v", stateVersionDropExportProfiles, err)
	}

	if _, err := db.Exec(`
		INSERT INTO platforms (
			id, name, sticky_ttl_ns, regex_filters_json, region_filters_json,
			reverse_proxy_miss_action, reverse_proxy_empty_account_behavior,
			reverse_proxy_fixed_account_header, allocation_policy,
			passive_circuit_breaker_disabled, updated_at_ns
		) VALUES (
			'plat-pre-tenant', 'PreTenant', 60000000000, '[]', '[]',
			'TREAT_AS_EMPTY', 'RANDOM', '', 'BALANCED', 0, 1700000000000000000
		)
	`); err != nil {
		t.Fatalf("seed platform: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO subscriptions (
			id, name, source_type, url, content, update_interval_ns, enabled,
			ephemeral, ephemeral_node_evict_delay_ns, created_at_ns, updated_at_ns
		) VALUES (
			'sub-pre-tenant', 'PreTenantFeed', 'remote', 'https://example.com/feed', '',
			3600000000000, 1, 0, 259200000000000, 1700000000000000000, 1700000000000000000
		)
	`); err != nil {
		t.Fatalf("seed node feed: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO audit_log (at_ns, actor, remote_addr, action, target, detail_json)
		VALUES (1700000000000000000, 'deadbeef', '127.0.0.1:1234', 'POST /api/v1/platforms', 'x', '{}')
	`); err != nil {
		t.Fatalf("seed audit row: %v", err)
	}

	if err := MigrateStateDB(db); err != nil {
		t.Fatalf("MigrateStateDB: %v", err)
	}
	assertStateMigrationVersion(t, db, stateLatestVersion)

	var platformName string
	if err := db.QueryRow(`SELECT name FROM platforms WHERE id = 'plat-pre-tenant'`).Scan(&platformName); err != nil {
		t.Fatalf("platform row lost: %v", err)
	}
	if platformName != "PreTenant" {
		t.Errorf("platform name: got %q, want %q", platformName, "PreTenant")
	}

	var feedName string
	if err := db.QueryRow(`SELECT name FROM subscriptions WHERE id = 'sub-pre-tenant'`).Scan(&feedName); err != nil {
		t.Fatalf("node feed row lost: %v", err)
	}
	if feedName != "PreTenantFeed" {
		t.Errorf("node feed name: got %q, want %q", feedName, "PreTenantFeed")
	}

	// A row written before the migration decodes with the new column default.
	var actor, userID string
	if err := db.QueryRow(`
		SELECT actor, user_id FROM audit_log WHERE actor = 'deadbeef'
	`).Scan(&actor, &userID); err != nil {
		t.Fatalf("audit row lost: %v", err)
	}
	if userID != "" {
		t.Errorf("audit_log.user_id default: got %q, want empty", userID)
	}
}

// --- 000017_tenant: constraints ---

func TestMigrateStateDB_TenantConstraints(t *testing.T) {
	repo := newTestStateRepo(t)
	db := repo.db

	if _, err := db.Exec(`
		INSERT INTO users (id, username, password_hash, role, status, created_at_ns, updated_at_ns)
		VALUES ('u1', 'alice', 'hash', 'user', 'active', 1, 1)
	`); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	// Username is unique.
	if _, err := db.Exec(`
		INSERT INTO users (id, username, password_hash, role, status, created_at_ns, updated_at_ns)
		VALUES ('u2', 'alice', 'hash', 'user', 'active', 1, 1)
	`); err == nil {
		t.Error("duplicate username must be rejected")
	}

	// Role and status are constrained.
	if _, err := db.Exec(`
		INSERT INTO users (id, username, password_hash, role, status, created_at_ns, updated_at_ns)
		VALUES ('u3', 'bob', 'hash', 'root', 'active', 1, 1)
	`); err == nil {
		t.Error("role outside (admin,user) must be rejected")
	}
	if _, err := db.Exec(`
		INSERT INTO users (id, username, password_hash, role, status, created_at_ns, updated_at_ns)
		VALUES ('u4', 'carol', 'hash', 'user', 'suspended', 1, 1)
	`); err == nil {
		t.Error("status outside (active,disabled) must be rejected")
	}

	// A subscription must point at a real user.
	if _, err := db.Exec(`
		INSERT INTO user_subscriptions (
			id, user_id, plan_id, plan_snapshot_json, starts_at_ns, expires_at_ns,
			period_start_ns, period_used_bytes, status
		) VALUES ('s1', 'nobody', NULL, '{}', 1, 2, 1, 0, 'active')
	`); err == nil {
		t.Error("subscription for an unknown user must be rejected")
	}

	// An access key must point at a real user.
	if _, err := db.Exec(`
		INSERT INTO access_keys (id, user_id, secret_hash, created_at_ns)
		VALUES ('k1', 'nobody', X'00', 1)
	`); err == nil {
		t.Error("access key for an unknown user must be rejected")
	}

	// Scope is constrained.
	if _, err := db.Exec(`
		INSERT INTO access_keys (id, user_id, secret_hash, scope, created_at_ns)
		VALUES ('k2', 'u1', X'00', 'superuser', 1)
	`); err == nil {
		t.Error("scope outside (proxy,admin) must be rejected")
	}
}

// Deleting a user clears the rows that belong to it, but usage rows survive
// because they carry no foreign key.
func TestMigrateStateDB_UserDeleteCascadesButKeepsUsage(t *testing.T) {
	repo := newTestStateRepo(t)
	db := repo.db

	if _, err := db.Exec(`
		INSERT INTO users (id, username, password_hash, role, status, created_at_ns, updated_at_ns)
		VALUES ('u-del', 'dave', 'hash', 'user', 'active', 1, 1)
	`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO access_keys (id, user_id, secret_hash, created_at_ns)
		VALUES ('k-del', 'u-del', X'00', 1)
	`); err != nil {
		t.Fatalf("insert key: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO sessions (id_hash, user_id, created_at_ns, expires_at_ns)
		VALUES ('h-del', 'u-del', 1, 2)
	`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO user_subscriptions (
			id, user_id, plan_id, plan_snapshot_json, starts_at_ns, expires_at_ns,
			period_start_ns, period_used_bytes, status
		) VALUES ('s-del', 'u-del', NULL, '{}', 1, 2, 1, 0, 'active')
	`); err != nil {
		t.Fatalf("insert subscription: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO usage_hourly (key_id, user_id, platform_id, hour_ts_ns, ingress_bytes, egress_bytes, requests, errors)
		VALUES ('k-del', 'u-del', 'p1', 1, 10, 20, 1, 0)
	`); err != nil {
		t.Fatalf("insert usage: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM users WHERE id = 'u-del'`); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	for _, tc := range []struct {
		table string
		query string
	}{
		{"access_keys", `SELECT COUNT(*) FROM access_keys WHERE user_id = 'u-del'`},
		{"sessions", `SELECT COUNT(*) FROM sessions WHERE user_id = 'u-del'`},
		{"user_subscriptions", `SELECT COUNT(*) FROM user_subscriptions WHERE user_id = 'u-del'`},
	} {
		var count int
		if err := db.QueryRow(tc.query).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", tc.table, err)
		}
		if count != 0 {
			t.Errorf("%s rows for a deleted user: got %d, want 0", tc.table, count)
		}
	}

	var usage int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_hourly WHERE user_id = 'u-del'`).Scan(&usage); err != nil {
		t.Fatalf("count usage: %v", err)
	}
	if usage != 1 {
		t.Errorf("usage rows must survive user deletion: got %d, want 1", usage)
	}
}

// Deleting a plan clears plan_id but keeps the snapshot the subscription was
// opened under, so its quota stays computable.
func TestMigrateStateDB_PlanDeleteKeepsSnapshot(t *testing.T) {
	repo := newTestStateRepo(t)
	db := repo.db

	if _, err := db.Exec(`
		INSERT INTO users (id, username, password_hash, role, status, created_at_ns, updated_at_ns)
		VALUES ('u-plan', 'erin', 'hash', 'user', 'active', 1, 1)
	`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO plans (id, name, created_at_ns, updated_at_ns)
		VALUES ('p-del', 'Starter', 1, 1)
	`); err != nil {
		t.Fatalf("insert plan: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO user_subscriptions (
			id, user_id, plan_id, plan_snapshot_json, starts_at_ns, expires_at_ns,
			period_start_ns, period_used_bytes, status
		) VALUES ('s-plan', 'u-plan', 'p-del', '{"traffic_bytes":100}', 1, 2, 1, 0, 'active')
	`); err != nil {
		t.Fatalf("insert subscription: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM plans WHERE id = 'p-del'`); err != nil {
		t.Fatalf("delete plan: %v", err)
	}

	var planID sql.NullString
	var snapshot string
	if err := db.QueryRow(`
		SELECT plan_id, plan_snapshot_json FROM user_subscriptions WHERE id = 's-plan'
	`).Scan(&planID, &snapshot); err != nil {
		t.Fatalf("read subscription after plan delete: %v", err)
	}
	if planID.Valid {
		t.Errorf("plan_id must be cleared when the plan is deleted, got %q", planID.String)
	}
	if snapshot != `{"traffic_bytes":100}` {
		t.Errorf("snapshot must survive plan deletion, got %q", snapshot)
	}
}
