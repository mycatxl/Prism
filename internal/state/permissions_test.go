package state

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// assertFileMode fails when path does not carry exactly want.
func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", filepath.Base(path), err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", filepath.Base(path), got, want)
	}
}

// mustMkdirLoose creates dir with the permissive 0755 mode an older revision
// (or a permissive umask) would have left behind.
func mustMkdirLoose(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
}

// TestHardenDBFiles_ForcesPrivateModes covers G-04: the directory becomes 0700
// and the database file plus its WAL side files become 0600, even when every
// one of them already exists with a world-readable mode.
func TestHardenDBFiles_ForcesPrivateModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	mustMkdirLoose(t, dir)
	path := filepath.Join(dir, "state.db")
	files := []string{path, path + "-wal", path + "-shm"}
	for _, file := range files {
		if err := os.WriteFile(file, []byte("legacy"), 0o644); err != nil {
			t.Fatalf("write %s: %v", filepath.Base(file), err)
		}
	}

	if err := HardenDBFiles(path); err != nil {
		t.Fatalf("HardenDBFiles: %v", err)
	}
	assertFileMode(t, dir, PrivateDirMode)
	for _, file := range files {
		assertFileMode(t, file, PrivateFileMode)
	}
}

// TestHardenDBFiles_MissingFileCreatesPrivateDir is the first-start case: the
// directory is created 0700 and a missing database file is not an error.
func TestHardenDBFiles_MissingFileCreatesPrivateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	path := filepath.Join(dir, "state.db")
	if err := HardenDBFiles(path); err != nil {
		t.Fatalf("HardenDBFiles(missing): %v", err)
	}
	assertFileMode(t, dir, PrivateDirMode)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("HardenDBFiles must not create the database file itself (stat err = %v)", err)
	}
}

// TestOpenDB_HardensExistingDatabaseFile covers an installation upgraded from a
// revision that left state.db at 0644 inside a 0755 directory: opening it
// repairs both modes, including the WAL side files created afterwards.
func TestOpenDB_HardensExistingDatabaseFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	mustMkdirLoose(t, dir)
	path := filepath.Join(dir, "state.db")

	db, err := sql.Open("sqlite", path) // deliberately not OpenDB
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE legacy (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod legacy db: %v", err)
	}

	reopened, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	assertFileMode(t, dir, PrivateDirMode)
	assertFileMode(t, path, PrivateFileMode)

	// A write creates the WAL side files. HardenDBFiles runs again in
	// PersistenceBootstrap after the migrations, so pin them here too.
	if _, err := reopened.Exec("INSERT INTO legacy (id) VALUES (1)"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := HardenDBFiles(path); err != nil {
		t.Fatalf("HardenDBFiles after write: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		side := path + suffix
		if _, err := os.Stat(side); err != nil {
			continue // this write did not create it
		}
		assertFileMode(t, side, PrivateFileMode)
	}
}

// TestPersistenceBootstrap_HardensStateAndCachePermissions covers the bootstrap
// path (PRISM_STATE_DIR / PRISM_CACHE_DIR): state.db and cache.db and their
// directories are private, and a later bootstrap repairs modes that were
// loosened after the databases were first created.
func TestPersistenceBootstrap_HardensStateAndCachePermissions(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	cacheDir := filepath.Join(root, "cache")
	mustMkdirLoose(t, stateDir)
	mustMkdirLoose(t, cacheDir)

	engine, closer, err := PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	if engine == nil {
		t.Fatal("PersistenceBootstrap returned a nil engine")
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("close persistence: %v", err)
	}

	stateDBPath := filepath.Join(stateDir, "state.db")
	cacheDBPath := filepath.Join(cacheDir, "cache.db")
	assertFileMode(t, stateDir, PrivateDirMode)
	assertFileMode(t, cacheDir, PrivateDirMode)
	assertFileMode(t, stateDBPath, PrivateFileMode)
	assertFileMode(t, cacheDBPath, PrivateFileMode)

	// Loosen everything the way an older revision would have, then bootstrap
	// again: the modes must be repaired, not only applied on creation.
	if err := os.Chmod(stateDir, 0o755); err != nil {
		t.Fatalf("chmod state dir: %v", err)
	}
	if err := os.Chmod(cacheDir, 0o755); err != nil {
		t.Fatalf("chmod cache dir: %v", err)
	}
	for _, file := range []string{stateDBPath, cacheDBPath} {
		if err := os.Chmod(file, 0o644); err != nil {
			t.Fatalf("chmod %s: %v", filepath.Base(file), err)
		}
	}

	_, closer2, err := PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatalf("second PersistenceBootstrap: %v", err)
	}
	defer func() { _ = closer2.Close() }()
	assertFileMode(t, stateDir, PrivateDirMode)
	assertFileMode(t, cacheDir, PrivateDirMode)
	assertFileMode(t, stateDBPath, PrivateFileMode)
	assertFileMode(t, cacheDBPath, PrivateFileMode)
}
