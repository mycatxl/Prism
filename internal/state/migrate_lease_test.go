package state

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// newCacheTestDB opens an empty cache.db and migrates it.
func newCacheTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cache.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open cache.db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := MigrateCacheDB(db); err != nil {
		t.Fatalf("MigrateCacheDB: %v", err)
	}
	return db
}

// cachePrimaryKeyColumns returns the ordered column names of the leases primary
// key, so the test asserts the key itself rather than a version number.
func cachePrimaryKeyColumns(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(leases)")
	if err != nil {
		t.Fatalf("table_info(leases): %v", err)
	}
	defer rows.Close()

	var pk []string
	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue sql.NullString
			pkPosition   int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pkPosition); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if pkPosition > 0 {
			pk = append(pk, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	return pk
}

// The lease key is (platform_id, user_id, account): the account alone is only
// unique within one user, so two tenants that both call their account "default"
// must be able to hold their own lease.
func TestCacheMigration_LeasePrimaryKeyIncludesTheOwner(t *testing.T) {
	db := newCacheTestDB(t)

	pk := cachePrimaryKeyColumns(t, db)
	want := []string{"platform_id", "user_id", "account"}
	if len(pk) != len(want) {
		t.Fatalf("leases primary key = %v, want %v", pk, want)
	}
	for i, column := range want {
		if pk[i] != column {
			t.Fatalf("leases primary key = %v, want %v", pk, want)
		}
	}
}

// Two owners of one account must be storable at the same time: that is the
// isolation this migration exists for.
func TestCacheMigration_AllowsTwoOwnersOfOneAccount(t *testing.T) {
	db := newCacheTestDB(t)

	insert := `INSERT INTO leases (platform_id, user_id, account, node_hash, egress_ip, created_at_ns, expiry_ns, last_accessed_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	for _, userID := range []string{"user-a", "user-b"} {
		if _, err := db.Exec(insert, "plat-1", userID, "default", "hash", "203.0.113.1", 1, 2, 1); err != nil {
			t.Fatalf("insert lease for %s: %v", userID, err)
		}
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM leases WHERE platform_id = ? AND account = ?", "plat-1", "default").Scan(&count); err != nil {
		t.Fatalf("count leases: %v", err)
	}
	if count != 2 {
		t.Fatalf("leases for one account = %d, want 2 (one per owner)", count)
	}

	// The same owner and account twice is still a conflict: the key is the
	// owner triple, not the account alone.
	if _, err := db.Exec(insert, "plat-1", "user-a", "default", "hash", "203.0.113.2", 1, 2, 1); err == nil {
		t.Fatal("a duplicate owner triple was accepted")
	}
}

// The migration carries existing rows over as the single-tenant owner, which is
// their exact owner rather than a guess: before it there was no user dimension.
func TestCacheMigration_CarriesExistingLeasesAsSingleTenant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open cache.db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Build the pre-migration schema by hand, exactly as 000001 created it.
	if _, err := db.Exec(`CREATE TABLE leases (
		platform_id      TEXT NOT NULL,
		account          TEXT NOT NULL,
		node_hash        TEXT NOT NULL,
		egress_ip        TEXT NOT NULL DEFAULT '',
		created_at_ns    INTEGER NOT NULL DEFAULT 0,
		expiry_ns        INTEGER NOT NULL,
		last_accessed_ns INTEGER NOT NULL,
		PRIMARY KEY (platform_id, account)
	)`); err != nil {
		t.Fatalf("create old leases table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO leases (platform_id, account, node_hash, egress_ip, created_at_ns, expiry_ns, last_accessed_ns)
		VALUES ('plat-1', 'legacy-account', 'hash', '203.0.113.9', 11, 22, 33)`); err != nil {
		t.Fatalf("seed old lease: %v", err)
	}

	if err := MigrateCacheDB(db); err != nil {
		t.Fatalf("MigrateCacheDB: %v", err)
	}

	var (
		userID, account, egressIP string
		createdAtNs, expiryNs     int64
	)
	if err := db.QueryRow(
		"SELECT user_id, account, egress_ip, created_at_ns, expiry_ns FROM leases WHERE platform_id = ?", "plat-1",
	).Scan(&userID, &account, &egressIP, &createdAtNs, &expiryNs); err != nil {
		t.Fatalf("read migrated lease: %v", err)
	}
	if userID != "" {
		t.Fatalf("migrated lease owner = %q, want the empty single-tenant owner", userID)
	}
	if account != "legacy-account" || egressIP != "203.0.113.9" || createdAtNs != 11 || expiryNs != 22 {
		t.Fatalf("migrated lease lost data: %q %q %d %d", account, egressIP, createdAtNs, expiryNs)
	}
}

func TestCacheMigration_VersionIsReported(t *testing.T) {
	db := newCacheTestDB(t)

	var version int
	if err := db.QueryRow("SELECT version FROM schema_migrations LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if version != CacheLatestVersion() {
		t.Fatalf("cache schema version = %d, want %d", version, CacheLatestVersion())
	}
	if CacheLatestVersion() < 3 {
		t.Fatalf("CacheLatestVersion() = %d, want at least 3 (the lease owner migration)", CacheLatestVersion())
	}
}
