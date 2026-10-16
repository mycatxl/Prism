package state

import (
	"testing"
)

// The pragmas every connection needs are part of the DSN, not a one-off Exec:
// database/sql closes connections (a driver error is enough) and the replacement
// has to come back with foreign keys on and a busy timeout, or the ON DELETE
// CASCADE / SET NULL clauses of the tenant schema stop firing and a second writer
// fails with SQLITE_BUSY instead of waiting.
//
// The test forces the pool to throw its connection away and then reads the
// pragmas again. A per-connection setting that is not part of the DSN - the page
// cache - is read first to prove that a new connection really is in use, so this
// cannot pass by accident on the original one.
func TestOpenDB_ConnectionPragmasSurviveARecycledConnection(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir + "/state.db")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	assertPragmas := func(stage string) {
		t.Helper()

		var (
			foreignKeys int
			busyTimeout int
			synchronous int
		)
		if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatalf("%s: read foreign_keys: %v", stage, err)
		}
		if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatalf("%s: read busy_timeout: %v", stage, err)
		}
		if err := db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
			t.Fatalf("%s: read synchronous: %v", stage, err)
		}

		if foreignKeys != 1 {
			t.Errorf("%s: foreign_keys = %d, want 1", stage, foreignKeys)
		}
		if busyTimeout != 5000 {
			t.Errorf("%s: busy_timeout = %d, want 5000", stage, busyTimeout)
		}
		if synchronous != 1 {
			t.Errorf("%s: synchronous = %d, want 1 (NORMAL)", stage, synchronous)
		}
	}

	assertPragmas("first connection")

	// A connection-scoped setting that is not in the DSN: the next read says
	// whether the statement ran on the same connection as the one before.
	if _, err := db.Exec("PRAGMA cache_size = -1234"); err != nil {
		t.Fatalf("set cache_size: %v", err)
	}

	// No idle connections allowed: the connection is closed when the statement
	// that uses it ends, exactly as it is after a driver error.
	db.SetMaxIdleConns(0)
	if _, err := db.Exec("SELECT 1"); err != nil {
		t.Fatalf("open a fresh connection: %v", err)
	}

	var cacheSize int
	if err := db.QueryRow("PRAGMA cache_size").Scan(&cacheSize); err != nil {
		t.Fatalf("read cache_size: %v", err)
	}
	if cacheSize == -1234 {
		t.Fatalf("the connection was reused, so this test would prove nothing")
	}

	assertPragmas("after the connection was recycled")
}
