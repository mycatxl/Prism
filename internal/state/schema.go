// Package state implements the persistence layer: SQLite repos, StateEngine,
// dirty-set flush, consistency repair, and bootstrap.
package state

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// PrivateDirMode and PrivateFileMode are the modes forced on every directory
// and every SQLite file Prism owns. state.db holds provider API keys in clear
// (intel_provider_settings.api_key), the audit log, export-profile token
// digests and subscription URLs, so neither the database files nor their
// containing directory may be readable by another local user.
const (
	PrivateDirMode  os.FileMode = 0o700
	PrivateFileMode os.FileMode = 0o600
)

// ensurePrivateDir creates dir when it is missing and forces 0700 on it. A
// bare file name ("" or ".") leaves the working directory alone.
func ensurePrivateDir(dir string) error {
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, PrivateDirMode); err != nil {
		return fmt.Errorf("create private dir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, PrivateDirMode); err != nil {
		return fmt.Errorf("chmod private dir %s: %w", dir, err)
	}
	return nil
}

// HardenDBFiles forces 0700 on the database directory and 0600 on the database
// file and its WAL side files. Files that already exist with a looser mode are
// repaired too, so an installation created by an older revision (or by a
// permissive umask) is fixed on the next start. A missing file is not an error:
// SQLite creates it on the first write and inherits the mode of the main
// database file.
func HardenDBFiles(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	// WAL side files contain the same data as the main file.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		file := path + suffix
		if _, err := os.Stat(file); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("stat %s: %w", file, err)
		}
		if err := os.Chmod(file, PrivateFileMode); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("chmod %s: %w", file, err)
		}
	}
	return nil
}

// sqliteConnectionPragmas is appended to the database path handed to sql.Open.
//
// modernc.org/sqlite runs every "_pragma" parameter of the DSN on each
// connection it opens, which is what the settings below need: foreign_keys,
// synchronous and busy_timeout are all connection-scoped, and database/sql does
// close and reopen connections (it discards one after a driver error, for
// instance). A one-off Exec after sql.Open reaches the first connection only,
// so a replacement would come back with foreign keys off and no busy timeout:
// the ON DELETE CASCADE and SET NULL clauses the tenant schema relies on would
// silently stop firing and a second writer would fail with SQLITE_BUSY instead
// of waiting. journal_mode is deliberately absent from the list: it is recorded
// in the database file itself, so setting it once is enough.
const sqliteConnectionPragmas = "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(1)"

// OpenDB opens (or creates) a SQLite database at path with recommended pragmas:
// WAL journal mode, synchronous=NORMAL, foreign_keys=ON, busy_timeout=5000.
// Every connection the pool opens carries them, not just the first one (see
// sqliteConnectionPragmas).
//
// The database is private by construction: the containing directory is created
// 0700 and the database file plus its WAL side files are forced to 0600 both
// before and after the pragmas run.
func OpenDB(path string) (*sql.DB, error) {
	if err := HardenDBFiles(path); err != nil {
		return nil, fmt.Errorf("harden db %s: %w", path, err)
	}
	db, err := sql.Open("sqlite", path+sqliteConnectionPragmas)
	if err != nil {
		return nil, fmt.Errorf("open db %s: %w", path, err)
	}

	// Single-writer: only one connection needed.
	db.SetMaxOpenConns(1)

	// journal_mode belongs to the file rather than to the connection. Running
	// it also opens the first connection, so a path that cannot be opened still
	// fails here instead of at the first query.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("exec journal_mode=WAL on %s: %w", path, err)
	}

	// journal_mode=WAL may have created the side files just now.
	if err := HardenDBFiles(path); err != nil {
		db.Close()
		return nil, fmt.Errorf("harden db %s: %w", path, err)
	}

	return db, nil
}

// InitDB executes raw DDL statements on the given database.
// Used by non-state SQLite stores (for example metrics and request logs).
func InitDB(db *sql.DB, ddl string) error {
	_, err := db.Exec(ddl)
	return err
}

func ensureTableColumn(db *sql.DB, table, column, columnDDL string) error {
	exists, err := hasTableColumn(db, table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, columnDDL)
	if _, err := db.Exec(stmt); err != nil {
		return fmt.Errorf("migrate %s.%s: %w", table, column, err)
	}
	return nil
}

func hasTableColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, fmt.Errorf("inspect table %s: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			name      string
			colType   string
			notNull   int
			defaultV  sql.NullString
			primaryID int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultV, &primaryID); err != nil {
			return false, fmt.Errorf("scan table_info(%s): %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate table_info(%s): %w", table, err)
	}
	return false, nil
}
