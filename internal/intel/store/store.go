// Package store owns the persistent intel.db SQLite database: the open path,
// the embedded migrations and the repositories for egress observations,
// provider evidence, via-node checks, purity assessments, provider budgets and
// batch jobs (WP08 §2).
//
// intel.db is deliberately a separate file from state.db/cache.db. It is set up
// as a single-writer WAL database (SetMaxOpenConns(1), synchronous=NORMAL,
// busy_timeout=5000), the file is 0600 and the containing directory is 0700.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// FileName is the intel database file name inside $PRISM_STATE_DIR.
const FileName = "intel.db"

// ErrClosed is returned when an operation runs against a closed store.
var ErrClosed = errors.New("intel store is closed")

// Store is the intel.db handle. It is safe for concurrent use: every write
// method serialises through the single SQLite connection.
type Store struct {
	db   *sql.DB
	path string

	mu     sync.RWMutex
	closed bool
}

// Open creates (or opens) path and applies the WP08 pragmas and migrations.
//
// The directory is created with 0700 when missing and the database file is
// forced to 0600 so an unrelated process cannot read provider evidence.
func Open(path string) (*Store, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, fmt.Errorf("open intel store: empty path")
	}
	dir := filepath.Dir(trimmed)
	if err := ensurePrivateDir(dir); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", trimmed)
	if err != nil {
		return nil, fmt.Errorf("open intel.db %s: %w", trimmed, err)
	}

	// Single writer: SQLite allows one writer at a time and this keeps every
	// write on one connection so BEGIN IMMEDIATE style budgeting is reliable.
	db.SetMaxOpenConns(1)

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("exec %q on %s: %w", p, trimmed, err)
		}
	}

	if err := Migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate intel.db: %w", err)
	}

	store := &Store{db: db, path: trimmed}
	if err := store.hardenPermissions(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Path returns the absolute or relative file system path of the database.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// DB exposes the underlying handle for advanced callers (snapshot loading and
// cross-table maintenance). Callers must not change the connection pool size.
func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}

func (s *Store) conn() (*sql.DB, error) {
	if s == nil {
		return nil, ErrClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ErrClosed
	}
	return s.db, nil
}

// hardenPermissions forces the 0600 file mode and the 0700 directory mode.
func (s *Store) hardenPermissions() error {
	if err := ensurePrivateDir(filepath.Dir(s.path)); err != nil {
		return err
	}
	if err := os.Chmod(s.path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("chmod intel.db %s: %w", s.path, err)
	}
	// WAL side files contain the same data as the main file.
	for _, suffix := range []string{"-wal", "-shm"} {
		side := s.path + suffix
		if _, err := os.Stat(side); err == nil {
			if err := os.Chmod(side, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("chmod %s: %w", side, err)
			}
		}
	}
	return nil
}

func ensurePrivateDir(dir string) error {
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create intel dir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod intel dir %s: %w", dir, err)
	}
	return nil
}

// FileBytes reports the total on-disk size of intel.db including its WAL side
// files. It is used by GET /api/v1/intel/status.
func (s *Store) FileBytes() (int64, error) {
	if s == nil {
		return 0, nil
	}
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(s.path + suffix)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}

// Optimize runs PRAGMA optimize; the daily cleaner calls it every week.
func (s *Store) Optimize() error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	_, err = db.Exec("PRAGMA optimize")
	return err
}

// FreePageRatio returns the share of the database that is free pages (0..1).
func (s *Store) FreePageRatio() (float64, error) {
	db, err := s.conn()
	if err != nil {
		return 0, err
	}
	var pageCount, freeCount int64
	if err := db.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		return 0, err
	}
	if err := db.QueryRow("PRAGMA freelist_count").Scan(&freeCount); err != nil {
		return 0, err
	}
	if pageCount <= 0 {
		return 0, nil
	}
	return float64(freeCount) / float64(pageCount), nil
}

// Vacuum compacts the database file.
func (s *Store) Vacuum() error {
	db, err := s.conn()
	if err != nil {
		return err
	}
	_, err = db.Exec("VACUUM")
	return err
}

// VacuumIfFragmented vacuums only when more than ratio of the page count is on
// the freelist, bounding the size of intel.db without paying for a full rewrite
// on every cleanup pass.
func (s *Store) VacuumIfFragmented(ratio float64) (bool, error) {
	current, err := s.FreePageRatio()
	if err != nil {
		return false, err
	}
	if current <= ratio {
		return false, nil
	}
	if err := s.Vacuum(); err != nil {
		return false, err
	}
	return true, nil
}
