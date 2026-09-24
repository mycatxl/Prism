package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"prism/internal/node"
	"prism/internal/state"
)

// --- Resin fixtures ---

const (
	// resinFixtureSocksOutbound is a plain sing-box outbound; its RawOptions
	// must survive the import byte for byte (fact D3).
	resinFixtureSocksOutbound = `{"type":"socks","tag":"resin-socks","server":"198.51.100.10","server_port":1080,"version":"5"}`
	resinFixtureHTTPOutbound  = `{"type":"http","tag":"resin-http","server":"198.51.100.11","server_port":8080}`
)

// resinFixtureNode keeps the node identity a fixture wrote to cache.db.
type resinFixtureNode struct {
	Hash string
	Raw  string
}

// applyResinMigrations executes the upstream migration SQL up to lastVersion and
// records the resulting schema_migrations row the way golang-migrate does, so
// the fixture looks exactly like a database left behind by Resin.
func applyResinMigrations(t *testing.T, db *sql.DB, kind string, lastVersion int) {
	t.Helper()
	for version := 1; version <= lastVersion; version++ {
		pattern := filepath.Join(
			"..", "..", "internal", "state", "migrations", kind,
			fmt.Sprintf("%06d_*.up.sql", version),
		)
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		if len(matches) != 1 {
			t.Fatalf("expected exactly one %s migration for version %d, got %v", kind, version, matches)
		}
		sqlText, err := os.ReadFile(matches[0])
		if err != nil {
			t.Fatalf("read %s: %v", matches[0], err)
		}
		if _, err := db.Exec(string(sqlText)); err != nil {
			t.Fatalf("apply %s: %v", matches[0], err)
		}
	}
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS schema_migrations (version uint64, dirty bool)"); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	if _, err := db.Exec("INSERT INTO schema_migrations (version, dirty) VALUES (?, 0)", lastVersion); err != nil {
		t.Fatalf("record migration version: %v", err)
	}
}

// writeResinFixtures creates state.db and cache.db with the upstream Resin
// schema (state migrations 000001..000009, cache migrations 000001..000002) and
// one row per table that matters for an import.
func writeResinFixtures(t *testing.T, stateDir, cacheDir string) []resinFixtureNode {
	t.Helper()
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("create %s: %v", stateDir, err)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("create %s: %v", cacheDir, err)
	}

	nodes := []resinFixtureNode{
		{Hash: node.HashFromRawOptions([]byte(resinFixtureSocksOutbound)).Hex(), Raw: resinFixtureSocksOutbound},
		{Hash: node.HashFromRawOptions([]byte(resinFixtureHTTPOutbound)).Hex(), Raw: resinFixtureHTTPOutbound},
	}

	stateDB := openImportTestDatabase(t, filepath.Join(stateDir, "state.db"))
	applyResinMigrations(t, stateDB, "state", 9)
	if _, err := stateDB.Exec(`
		INSERT INTO platforms (
			id, name, sticky_ttl_ns, regex_filters_json, region_filters_json,
			reverse_proxy_miss_action, reverse_proxy_empty_account_behavior,
			reverse_proxy_fixed_account_header, allocation_policy,
			passive_circuit_breaker_disabled, updated_at_ns
		) VALUES (
			'plat-resin', 'ResinPlatform', 604800000000000, '["^Resin/.*"]', '["us"]',
			'TREAT_AS_EMPTY', 'ACCOUNT_HEADER_RULE', 'Authorization', 'BALANCED',
			0, 1700000000000000000
		)
	`); err != nil {
		t.Fatalf("insert Resin platform: %v", err)
	}
	if _, err := stateDB.Exec(`
		INSERT INTO subscriptions (
			id, name, source_type, url, content, update_interval_ns, enabled,
			ephemeral, ephemeral_node_evict_delay_ns, incremental_alive_nodes,
			created_at_ns, updated_at_ns
		) VALUES (
			'sub-resin', 'ResinSub', 'remote', 'https://subscription.invalid/resin', '', 3600000000000,
			1, 0, 259200000000000, 0, 1700000000000000000, 1700000000000000000
		)
	`); err != nil {
		t.Fatalf("insert Resin subscription: %v", err)
	}
	if _, err := stateDB.Exec(`
		INSERT INTO endpoints (
			id, port, allow_management, allow_proxy, require_proxy_auth_info,
			allow_http_forward, allow_http_reverse, allow_socks5, enabled,
			created_at_ns, updated_at_ns
		) VALUES (
			'ep-resin', 32456, 1, 1, 0, 1, 1, 1, 1, 1700000000000000000, 1700000000000000000
		)
	`); err != nil {
		t.Fatalf("insert Resin endpoint: %v", err)
	}
	if err := stateDB.Close(); err != nil {
		t.Fatalf("close Resin state.db: %v", err)
	}

	cacheDB := openImportTestDatabase(t, filepath.Join(cacheDir, "cache.db"))
	applyResinMigrations(t, cacheDB, "cache", 2)
	for _, fixtureNode := range nodes {
		if _, err := cacheDB.Exec(
			`INSERT INTO nodes_static (hash, raw_options_json, created_at_ns) VALUES (?, ?, 1700000000000000000)`,
			fixtureNode.Hash, fixtureNode.Raw,
		); err != nil {
			t.Fatalf("insert Resin node %s: %v", fixtureNode.Hash, err)
		}
		if _, err := cacheDB.Exec(`
			INSERT INTO nodes_dynamic (
				hash, failure_count, circuit_open_since, egress_ip, egress_region,
				egress_updated_at_ns, last_latency_probe_attempt_ns,
				last_authority_latency_probe_attempt_ns, last_egress_update_attempt_ns
			) VALUES (?, 0, 0, '203.0.113.9', 'us', 1700000000000000000, 0, 0, 0)
		`, fixtureNode.Hash); err != nil {
			t.Fatalf("insert Resin node dynamic row: %v", err)
		}
		// Every Resin node belongs to a subscription; consistency repair on the
		// imported installation would drop an orphaned node otherwise.
		if _, err := cacheDB.Exec(`
			INSERT INTO subscription_nodes (subscription_id, node_hash, tags_json, evicted)
			VALUES ('sub-resin', ?, '["tag-a"]', 0)
		`, fixtureNode.Hash); err != nil {
			t.Fatalf("insert Resin subscription node: %v", err)
		}
	}
	if _, err := cacheDB.Exec(`
		INSERT INTO leases (
			platform_id, account, node_hash, egress_ip, created_at_ns, expiry_ns, last_accessed_ns
		) VALUES (
			'plat-resin', 'account-a', ?, '203.0.113.9', 1700000000000000000,
			1700604800000000000, 1700000000000000000
		)
	`, nodes[0].Hash); err != nil {
		t.Fatalf("insert Resin lease: %v", err)
	}
	if err := cacheDB.Close(); err != nil {
		t.Fatalf("close Resin cache.db: %v", err)
	}
	return nodes
}

// writeResinRequestLogFixture creates a request-log database with the upstream
// resin_error column.
func writeResinRequestLogFixture(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	path := filepath.Join(dir, "request_logs-1700000000000.db")
	db := openImportTestDatabase(t, path)
	if _, err := db.Exec(`
		CREATE TABLE request_logs (
			id          TEXT PRIMARY KEY,
			ts_ns       INTEGER NOT NULL,
			client_ip   TEXT NOT NULL DEFAULT '',
			http_status INTEGER NOT NULL DEFAULT 0,
			resin_error TEXT NOT NULL DEFAULT ''
		)
	`); err != nil {
		t.Fatalf("create request_logs table: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO request_logs (id, ts_ns, client_ip, http_status, resin_error) VALUES ('log-1', 1700000000000000001, '198.51.100.7', 502, 'UPSTREAM_TIMEOUT')`,
	); err != nil {
		t.Fatalf("insert request log row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close request log fixture: %v", err)
	}
	return path
}

func openImportTestDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return db
}

func databaseIntValue(t *testing.T, path, query string, args ...any) int {
	t.Helper()
	db := openImportTestDatabase(t, path)
	defer func() { _ = db.Close() }()

	var value int
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("query %q on %s: %v", query, path, err)
	}
	return value
}

func databaseStringValue(t *testing.T, path, query string, args ...any) string {
	t.Helper()
	db := openImportTestDatabase(t, path)
	defer func() { _ = db.Close() }()

	var value string
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("query %q on %s: %v", query, path, err)
	}
	return value
}

func databaseHasColumnForTest(t *testing.T, path, table, column string) bool {
	t.Helper()
	db := openImportTestDatabase(t, path)
	defer func() { _ = db.Close() }()

	has, err := databaseHasColumn(db, table, column)
	if err != nil {
		t.Fatalf("inspect %s.%s: %v", table, column, err)
	}
	return has
}

// freeTestPort reserves and immediately releases a loopback port so the running
// instance probe never dials a service by accident.
func freeTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release loopback port: %v", err)
	}
	return port
}

// --- tests ---

func TestImportResinMigratesAndReportsSummary(t *testing.T) {
	fromState := filepath.Join(t.TempDir(), "resin-state")
	fromCache := filepath.Join(t.TempDir(), "resin-cache")
	fixtureNodes := writeResinFixtures(t, fromState, fromCache)

	targetState := filepath.Join(t.TempDir(), "prism-state")
	targetCache := filepath.Join(t.TempDir(), "prism-cache")

	var log bytes.Buffer
	summary, err := importResinData(
		importResinTarget{StateDir: targetState, CacheDir: targetCache, LogDir: filepath.Join(t.TempDir(), "logs")},
		importResinSources{StateDir: fromState, CacheDir: fromCache},
		false,
		&log,
	)
	if err != nil {
		t.Fatalf("importResinData: %v (log=%s)", err, log.String())
	}

	if summary.Platforms != 1 || summary.Subscriptions != 1 || summary.Endpoints != 1 {
		t.Fatalf("state summary = %+v, want 1 platform, 1 subscription, 1 endpoint", summary)
	}
	if summary.Nodes != len(fixtureNodes) || summary.Leases != 1 {
		t.Fatalf("cache summary = %+v, want %d nodes and 1 lease", summary, len(fixtureNodes))
	}
	if summary.RequestLogDBs != 0 {
		t.Fatalf("request log databases = %d, want 0 without --from-log", summary.RequestLogDBs)
	}

	var printed bytes.Buffer
	printImportResinSummary(&printed, summary)
	for _, want := range []string{
		"  platforms:     1\n",
		"  subscriptions: 1\n",
		fmt.Sprintf("  nodes:         %d\n", len(fixtureNodes)),
		"  leases:        1\n",
		"  endpoints:     1\n",
	} {
		if !strings.Contains(printed.String(), want) {
			t.Fatalf("summary output %q is missing %q", printed.String(), want)
		}
	}

	statePath := filepath.Join(targetState, "state.db")
	cachePath := filepath.Join(targetCache, "cache.db")
	assertMode0600(t, statePath)
	assertMode0600(t, cachePath)

	// Prism migrations state 000010..000014 / cache 000002 ran during the import.
	if got := databaseIntValue(t, statePath, "SELECT version FROM schema_migrations LIMIT 1"); got != 14 {
		t.Fatalf("state schema_migrations version = %d, want 14", got)
	}
	if got := databaseIntValue(t, cachePath, "SELECT version FROM schema_migrations LIMIT 1"); got != 2 {
		t.Fatalf("cache schema_migrations version = %d, want 2", got)
	}

	// Resin rows are intact and the node identity is byte-identical (D3).
	if got := databaseStringValue(t, statePath, "SELECT name FROM platforms WHERE id = 'plat-resin'"); got != "ResinPlatform" {
		t.Fatalf("platform name = %q, want ResinPlatform", got)
	}
	if got := databaseStringValue(t, statePath, "SELECT url FROM subscriptions WHERE id = 'sub-resin'"); got != "https://subscription.invalid/resin" {
		t.Fatalf("subscription url = %q", got)
	}
	if got := databaseStringValue(t, cachePath, "SELECT account FROM leases WHERE platform_id = 'plat-resin'"); got != "account-a" {
		t.Fatalf("lease account = %q, want account-a", got)
	}
	for _, fixtureNode := range fixtureNodes {
		raw := databaseStringValue(t, cachePath, "SELECT raw_options_json FROM nodes_static WHERE hash = ?", fixtureNode.Hash)
		if raw != fixtureNode.Raw {
			t.Fatalf("node %s raw options changed:\n got %s\nwant %s", fixtureNode.Hash, raw, fixtureNode.Raw)
		}
		if recomputed := node.HashFromRawOptions([]byte(raw)).Hex(); recomputed != fixtureNode.Hash {
			t.Fatalf("node hash changed: %s -> %s", fixtureNode.Hash, recomputed)
		}
	}

	// Columns and tables added by the Prism migrations exist with their defaults.
	if got := databaseStringValue(t, statePath, "SELECT quality_policy_json FROM platforms WHERE id = 'plat-resin'"); got != "{}" {
		t.Fatalf("platforms.quality_policy_json = %q, want {}", got)
	}
	if got := databaseIntValue(t, statePath, "SELECT rotation_avoid_previous_ip FROM platforms WHERE id = 'plat-resin'"); got != 1 {
		t.Fatalf("platforms.rotation_avoid_previous_ip = %d, want 1", got)
	}
	if got := databaseIntValue(t, statePath, "SELECT auto_intel FROM subscriptions WHERE id = 'sub-resin'"); got != 1 {
		t.Fatalf("subscriptions.auto_intel = %d, want 1", got)
	}
	if got := databaseIntValue(t, statePath, "SELECT COUNT(*) FROM audit_log"); got != 0 {
		t.Fatalf("audit_log rows = %d, want 0", got)
	}

	// Prism opens the imported installation without any further migration and
	// serves the Resin rows.
	engine, closer, err := state.PersistenceBootstrap(targetState, targetCache)
	if err != nil {
		t.Fatalf("PersistenceBootstrap on the imported installation: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	platforms, err := engine.ListPlatforms()
	if err != nil {
		t.Fatalf("ListPlatforms: %v", err)
	}
	if len(platforms) != 1 || platforms[0].ID != "plat-resin" {
		t.Fatalf("imported platforms = %+v, want the Resin platform", platforms)
	}
	subscriptions, err := engine.ListSubscriptions()
	if err != nil {
		t.Fatalf("ListSubscriptions: %v", err)
	}
	if len(subscriptions) != 1 || subscriptions[0].ID != "sub-resin" {
		t.Fatalf("imported subscriptions = %+v, want the Resin subscription", subscriptions)
	}
	endpoints, err := engine.ListEndpoints()
	if err != nil {
		t.Fatalf("ListEndpoints: %v", err)
	}
	if len(endpoints) != 1 || endpoints[0].ID != "ep-resin" {
		t.Fatalf("imported endpoints = %+v, want the Resin endpoint", endpoints)
	}
	loadedNodes, err := engine.LoadAllNodesStatic()
	if err != nil {
		t.Fatalf("LoadAllNodesStatic: %v", err)
	}
	if len(loadedNodes) != len(fixtureNodes) {
		t.Fatalf("imported nodes = %d, want %d", len(loadedNodes), len(fixtureNodes))
	}
	leases, err := engine.LoadAllLeases()
	if err != nil {
		t.Fatalf("LoadAllLeases: %v", err)
	}
	if len(leases) != 1 || leases[0].Account != "account-a" {
		t.Fatalf("imported leases = %+v, want account-a", leases)
	}
}

func TestImportResinRequiresForceAndMovesExistingDatabasesAside(t *testing.T) {
	fromState := filepath.Join(t.TempDir(), "resin-state")
	fromCache := filepath.Join(t.TempDir(), "resin-cache")
	writeResinFixtures(t, fromState, fromCache)

	targetState := t.TempDir()
	targetCache := t.TempDir()
	staleState := filepath.Join(targetState, "state.db")
	staleCache := filepath.Join(targetCache, "cache.db")
	for _, path := range []string{staleState, staleCache} {
		if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	target := importResinTarget{StateDir: targetState, CacheDir: targetCache}
	src := importResinSources{StateDir: fromState, CacheDir: fromCache}

	err := func() error {
		_, err := importResinData(target, src, false, new(bytes.Buffer))
		return err
	}()
	if err == nil {
		t.Fatal("import-resin without --force must refuse to replace existing databases")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error should point at --force, got %v", err)
	}
	if got := readFileString(t, staleState); got != "stale" {
		t.Fatalf("refused import modified %s: %q", staleState, got)
	}

	if _, err := importResinData(target, src, true, new(bytes.Buffer)); err != nil {
		t.Fatalf("importResinData --force: %v", err)
	}

	asides, err := filepath.Glob(staleState + preImportSuffix + "*")
	if err != nil {
		t.Fatalf("glob pre-import files: %v", err)
	}
	if len(asides) != 1 {
		t.Fatalf("expected 1 %s* file for state.db, got %v", preImportSuffix, asides)
	}
	if got := readFileString(t, asides[0]); got != "stale" {
		t.Fatalf("pre-import backup content = %q, want %q", got, "stale")
	}
	if got := databaseIntValue(t, staleState, "SELECT COUNT(*) FROM platforms"); got != 1 {
		t.Fatalf("imported platforms = %d, want 1", got)
	}
	assertMode0600(t, staleState)
	assertMode0600(t, staleCache)
}

func TestImportResinRefusesWhileServiceIsRunning(t *testing.T) {
	fromState := filepath.Join(t.TempDir(), "resin-state")
	fromCache := filepath.Join(t.TempDir(), "resin-cache")
	writeResinFixtures(t, fromState, fromCache)

	targetState := t.TempDir()
	targetCache := t.TempDir()
	// A live pid file (this test process) marks the installation as running.
	if err := os.WriteFile(filepath.Join(targetState, pidFileName), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	_, err := importResinData(
		importResinTarget{StateDir: targetState, CacheDir: targetCache},
		importResinSources{StateDir: fromState, CacheDir: fromCache},
		true,
		new(bytes.Buffer),
	)
	if err == nil {
		t.Fatal("import-resin must refuse to run while the service is active")
	}
	if !strings.Contains(err.Error(), "active") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(targetState, "state.db")); statErr == nil {
		t.Fatal("a refused import must not write any database")
	}
}

func TestImportResinImportsOptionalRequestLogs(t *testing.T) {
	fromState := filepath.Join(t.TempDir(), "resin-state")
	fromCache := filepath.Join(t.TempDir(), "resin-cache")
	writeResinFixtures(t, fromState, fromCache)
	fromLog := filepath.Join(t.TempDir(), "resin-logs")
	writeResinRequestLogFixture(t, fromLog)

	targetState := t.TempDir()
	targetCache := t.TempDir()
	targetLog := t.TempDir()

	summary, err := importResinData(
		importResinTarget{StateDir: targetState, CacheDir: targetCache, LogDir: targetLog},
		importResinSources{StateDir: fromState, CacheDir: fromCache, LogDir: fromLog},
		false,
		new(bytes.Buffer),
	)
	if err != nil {
		t.Fatalf("importResinData with --from-log: %v", err)
	}
	if summary.RequestLogDBs != 1 {
		t.Fatalf("request log databases = %d, want 1", summary.RequestLogDBs)
	}

	imported := filepath.Join(targetLog, "request_logs-1700000000000.db")
	assertMode0600(t, imported)
	if !databaseHasColumnForTest(t, imported, "request_logs", "prism_error") {
		t.Fatal("imported request log must use the prism_error column")
	}
	if databaseHasColumnForTest(t, imported, "request_logs", "resin_error") {
		t.Fatal("imported request log must not keep the resin_error column")
	}
	if got := databaseStringValue(t, imported, "SELECT prism_error FROM request_logs WHERE id = 'log-1'"); got != "UPSTREAM_TIMEOUT" {
		t.Fatalf("imported request log error = %q, want UPSTREAM_TIMEOUT", got)
	}
	if got := databaseIntValue(t, imported, "SELECT COUNT(*) FROM request_logs"); got != 1 {
		t.Fatalf("imported request log rows = %d, want 1", got)
	}
}

func TestImportResinRequestLogsRequireForceWhenTargetExists(t *testing.T) {
	fromLog := filepath.Join(t.TempDir(), "resin-logs")
	writeResinRequestLogFixture(t, fromLog)

	targetLog := t.TempDir()
	existing := filepath.Join(targetLog, "request_logs-1700000000000.db")
	if err := os.WriteFile(existing, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write %s: %v", existing, err)
	}

	if _, err := importResinRequestLogs(fromLog, targetLog, false, "20260101-000000", new(bytes.Buffer)); err == nil {
		t.Fatal("an existing request log database must require --force")
	} else if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error should point at --force, got %v", err)
	}

	imported, err := importResinRequestLogs(fromLog, targetLog, true, "20260101-000000", new(bytes.Buffer))
	if err != nil {
		t.Fatalf("importResinRequestLogs --force: %v", err)
	}
	if imported != 1 {
		t.Fatalf("imported = %d, want 1", imported)
	}
	asides, err := filepath.Glob(existing + preImportSuffix + "*")
	if err != nil {
		t.Fatalf("glob pre-import files: %v", err)
	}
	if len(asides) != 1 {
		t.Fatalf("expected 1 %s* file, got %v", preImportSuffix, asides)
	}
	if got := readFileString(t, asides[0]); got != "stale" {
		t.Fatalf("pre-import request log = %q, want %q", got, "stale")
	}
}

func TestImportResinCommandImportsIntoConfiguredDirectories(t *testing.T) {
	fromState := filepath.Join(t.TempDir(), "resin-state")
	fromCache := filepath.Join(t.TempDir(), "resin-cache")
	writeResinFixtures(t, fromState, fromCache)

	targetState := filepath.Join(t.TempDir(), "state")
	targetCache := filepath.Join(t.TempDir(), "cache")
	targetLog := filepath.Join(t.TempDir(), "logs")

	t.Setenv("PRISM_ADMIN_TOKEN", strings.Repeat("a", 32))
	t.Setenv("PRISM_PROXY_TOKEN", strings.Repeat("b", 32))
	t.Setenv("PRISM_LISTEN_ADDRESS", "127.0.0.1")
	t.Setenv("PRISM_PORT", strconv.Itoa(freeTestPort(t)))
	t.Setenv("PRISM_STATE_DIR", targetState)
	t.Setenv("PRISM_CACHE_DIR", targetCache)
	t.Setenv("PRISM_LOG_DIR", targetLog)

	var stdout, stderr bytes.Buffer
	if err := runImportResinCommand(
		[]string{"--from-state", fromState, "--from-cache", fromCache},
		&stdout,
		&stderr,
	); err != nil {
		t.Fatalf("runImportResinCommand: %v (stderr=%s)", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"Imported Resin data:", "  platforms:     1", "Import complete."} {
		if !strings.Contains(out, want) {
			t.Fatalf("import-resin output %q is missing %q", out, want)
		}
	}
	if _, err := os.Stat(filepath.Join(targetState, "state.db")); err != nil {
		t.Fatalf("imported state.db missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetCache, "cache.db")); err != nil {
		t.Fatalf("imported cache.db missing: %v", err)
	}

	// Both source directories are mandatory.
	if err := runImportResinCommand(nil, &stdout, &stderr); err == nil {
		t.Fatal("--from-state must be required")
	} else if !strings.Contains(err.Error(), "--from-state") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := runImportResinCommand([]string{"--from-state", targetState}, &stdout, &stderr); err == nil {
		t.Fatal("--from-cache must be required")
	} else if !strings.Contains(err.Error(), "--from-cache") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImportResinRejectsMissingSourceDatabases(t *testing.T) {
	fromState := t.TempDir()
	fromCache := t.TempDir()

	_, err := importResinData(
		importResinTarget{StateDir: t.TempDir(), CacheDir: t.TempDir()},
		importResinSources{StateDir: fromState, CacheDir: fromCache},
		false,
		new(bytes.Buffer),
	)
	if err == nil {
		t.Fatal("an import without a Resin state.db must fail")
	}
	if !strings.Contains(err.Error(), "state.db not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}
