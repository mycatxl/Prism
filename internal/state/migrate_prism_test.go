package state

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// newStateMigrator builds a golang-migrate instance over the embedded state
// migrations without applying anything yet. Unlike MigrateStateDB it lets a
// test stop at an arbitrary version, which is required to reproduce an upgrade
// from a pre-Prism state.db.
func newStateMigrator(t *testing.T, db *sql.DB) *migrate.Migrate {
	t.Helper()

	sourceDriver, err := iofs.New(migrationsFS, stateMigrationsPath)
	if err != nil {
		t.Fatalf("init state migration source: %v", err)
	}
	dbDriver, err := migratesqlite.WithInstance(db, &migratesqlite.Config{
		MigrationsTable: migrateDefaultTable,
	})
	if err != nil {
		t.Fatalf("init state migration db driver: %v", err)
	}
	m, err := migrate.NewWithInstance("iofs", sourceDriver, "sqlite", dbDriver)
	if err != nil {
		t.Fatalf("init state migrator: %v", err)
	}
	// Deliberately not closed: migratesqlite.Close closes the *sql.DB that the
	// caller owns.
	return m
}

func assertStateMigrationVersion(t *testing.T, db *sql.DB, want int) {
	t.Helper()

	var version int
	var dirty bool
	if err := db.QueryRow("SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&version, &dirty); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if dirty {
		t.Fatalf("schema_migrations dirty=true at version %d", version)
	}
	if version != want {
		t.Fatalf("schema_migrations version: got %d, want %d", version, want)
	}
}

func TestMigrateStateDB_PrismUpgradePreservesData(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Step 1: stop right before the Prism extension migrations (000010..000014).
	m := newStateMigrator(t, db)
	if err := m.Migrate(stateVersionPlatformRegexFilterRules); err != nil {
		t.Fatalf("migrate state.db to version %d: %v", stateVersionPlatformRegexFilterRules, err)
	}
	assertStateMigrationVersion(t, db, stateVersionPlatformRegexFilterRules)

	// Step 2: seed rows that only use the pre-Prism column set.
	if _, err := db.Exec(`
		INSERT INTO platforms (
			id, name, sticky_ttl_ns, regex_filters_json, region_filters_json,
			reverse_proxy_miss_action, reverse_proxy_empty_account_behavior,
			reverse_proxy_fixed_account_header, allocation_policy,
			passive_circuit_breaker_disabled, updated_at_ns
		) VALUES (
			'plat-legacy', 'LegacyPlatform', 60000000000, '["^Legacy/.*"]', '["us","jp"]',
			'TREAT_AS_EMPTY', 'RANDOM', '', 'BALANCED', 0, 1700000000000000000
		)
	`); err != nil {
		t.Fatalf("seed legacy platform row: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO subscriptions (
			id, name, source_type, url, content, update_interval_ns, enabled,
			ephemeral, ephemeral_node_evict_delay_ns, incremental_alive_nodes,
			created_at_ns, updated_at_ns
		) VALUES (
			'sub-legacy', 'LegacySub', 'remote', 'https://example.com/subscription', '',
			3600000000000, 1, 0, 259200000000000, 0, 1700000000000000000, 1700000000000000000
		)
	`); err != nil {
		t.Fatalf("seed legacy subscription row: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO endpoints (
			id, port, allow_management, allow_proxy, require_proxy_auth_info,
			allow_http_forward, allow_http_reverse, allow_socks5, created_at_ns, updated_at_ns
		) VALUES (
			'ep-legacy', 32456, 1, 1, 0, 1, 1, 1, 1700000000000000000, 1700000000000000000
		)
	`); err != nil {
		t.Fatalf("seed legacy endpoint row: %v", err)
	}

	// Step 3: run the production upgrade path.
	if err := MigrateStateDB(db); err != nil {
		t.Fatalf("MigrateStateDB: %v", err)
	}
	assertStateMigrationVersion(t, db, stateLatestVersion)

	// Step 4a: the seeded rows survive the upgrade with their values intact.
	var name, regexFiltersJSON, regionFiltersJSON string
	var stickyTTLNs int64
	if err := db.QueryRow(`
		SELECT name, sticky_ttl_ns, regex_filters_json, region_filters_json
		FROM platforms WHERE id = 'plat-legacy'
	`).Scan(&name, &stickyTTLNs, &regexFiltersJSON, &regionFiltersJSON); err != nil {
		t.Fatalf("legacy platform row is gone after upgrade: %v", err)
	}
	if name != "LegacyPlatform" {
		t.Errorf("platform name: got %q, want %q", name, "LegacyPlatform")
	}
	if stickyTTLNs != 60000000000 {
		t.Errorf("platform sticky_ttl_ns: got %d, want 60000000000", stickyTTLNs)
	}
	if regexFiltersJSON != `["^Legacy/.*"]` || regionFiltersJSON != `["us","jp"]` {
		t.Errorf("platform filters changed: regex=%s region=%s", regexFiltersJSON, regionFiltersJSON)
	}

	var subName, subURL string
	var subIntervalNs int64
	if err := db.QueryRow(`
		SELECT name, url, update_interval_ns FROM subscriptions WHERE id = 'sub-legacy'
	`).Scan(&subName, &subURL, &subIntervalNs); err != nil {
		t.Fatalf("legacy subscription row is gone after upgrade: %v", err)
	}
	if subName != "LegacySub" || subURL != "https://example.com/subscription" || subIntervalNs != 3600000000000 {
		t.Errorf("legacy subscription changed: name=%q url=%q interval=%d", subName, subURL, subIntervalNs)
	}

	var endpointID string
	var endpointPort int
	if err := db.QueryRow(`SELECT id, port FROM endpoints WHERE id = 'ep-legacy'`).Scan(&endpointID, &endpointPort); err != nil {
		t.Fatalf("legacy endpoint row is gone after upgrade: %v", err)
	}
	if endpointPort != 32456 {
		t.Errorf("legacy endpoint port changed: got %d, want 32456", endpointPort)
	}

	// Step 4b: the new columns carry their migration defaults.
	defaults := []struct {
		column string
		query  string
		want   string
	}{
		{"platforms.quality_policy_json", `SELECT quality_policy_json FROM platforms WHERE id = 'plat-legacy'`, "{}"},
		{"platforms.scheduled_rotation_enabled", `SELECT CAST(scheduled_rotation_enabled AS TEXT) FROM platforms WHERE id = 'plat-legacy'`, "0"},
		{"platforms.scheduled_rotation_interval_ns", `SELECT CAST(scheduled_rotation_interval_ns AS TEXT) FROM platforms WHERE id = 'plat-legacy'`, "0"},
		{"platforms.rotation_avoid_previous_ip", `SELECT CAST(rotation_avoid_previous_ip AS TEXT) FROM platforms WHERE id = 'plat-legacy'`, "1"},
		{"subscriptions.auto_intel", `SELECT CAST(auto_intel AS TEXT) FROM subscriptions WHERE id = 'sub-legacy'`, "1"},
		{"subscriptions.user_agent", `SELECT user_agent FROM subscriptions WHERE id = 'sub-legacy'`, ""},
		{"subscriptions.last_parse_report_json", `SELECT last_parse_report_json FROM subscriptions WHERE id = 'sub-legacy'`, "{}"},
		{"endpoints.enabled", `SELECT CAST(enabled AS TEXT) FROM endpoints WHERE id = 'ep-legacy'`, "1"},
	}
	for _, tc := range defaults {
		var got string
		if err := db.QueryRow(tc.query).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", tc.column, err)
		}
		if got != tc.want {
			t.Errorf("%s default after upgrade: got %q, want %q", tc.column, got, tc.want)
		}
	}

	// Step 4c: the repo decodes the upgraded rows the same way it decodes rows
	// written by the current code.
	repo := newStateRepo(db)

	platform, err := repo.GetPlatform("plat-legacy")
	if err != nil {
		t.Fatalf("GetPlatform after upgrade: %v", err)
	}
	if platform.Name != "LegacyPlatform" {
		t.Errorf("GetPlatform name: got %q, want %q", platform.Name, "LegacyPlatform")
	}
	if !platform.RotationAvoidPreviousIP {
		t.Error("GetPlatform: rotation_avoid_previous_ip must be true after upgrade")
	}
	if platform.ScheduledRotationEnabled || platform.ScheduledRotationIntervalNs != 0 {
		t.Errorf("GetPlatform: scheduled rotation must stay off, got %+v", platform)
	}
	if !platform.QualityPolicy.IsEmpty() {
		t.Errorf("GetPlatform: quality policy must be empty after upgrade, got %+v", platform.QualityPolicy)
	}

	subscriptions, err := repo.ListSubscriptions()
	if err != nil {
		t.Fatalf("ListSubscriptions after upgrade: %v", err)
	}
	if len(subscriptions) != 1 {
		t.Fatalf("expected 1 subscription after upgrade, got %d", len(subscriptions))
	}
	if !subscriptions[0].AutoIntel {
		t.Error("ListSubscriptions: auto_intel must default to true after upgrade")
	}
	if subscriptions[0].UserAgent != "" {
		t.Errorf("ListSubscriptions: user_agent must default to empty, got %q", subscriptions[0].UserAgent)
	}

	// Step 4d: the new tables are usable without any further DDL.
	if _, err := repo.GetExportProfile("missing"); err == nil {
		t.Error("export_profiles table must exist after upgrade")
	} else if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetExportProfile on a migrated db: got %v, want ErrNotFound", err)
	}
	if entries, err := repo.ListAudit(0, 10); err != nil {
		t.Errorf("audit_log table must exist after upgrade: %v", err)
	} else if len(entries) != 0 {
		t.Errorf("audit_log must be empty after upgrade, got %+v", entries)
	}
	if settings, err := repo.ListIntelProviderSettings(); err != nil {
		t.Errorf("intel_provider_settings table must exist after upgrade: %v", err)
	} else if len(settings) != 0 {
		t.Errorf("intel_provider_settings must be empty after upgrade, got %+v", settings)
	}
}
