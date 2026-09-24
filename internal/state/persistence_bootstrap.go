package state

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
)

// persistenceCloser holds DB handles for cleanup. Implements io.Closer.
type persistenceCloser struct {
	stateDB *sql.DB
	cacheDB *sql.DB
}

func (c *persistenceCloser) Close() error {
	return errors.Join(c.stateDB.Close(), c.cacheDB.Close())
}

// PersistenceBootstrap initializes both databases, runs consistency repair,
// and returns a ready-to-use StateEngine plus an io.Closer for the DB handles.
//
// state.db and cache.db are private: both directories are created (or repaired)
// as 0700 and both database files plus their WAL side files are forced to 0600,
// including files that already existed with a looser mode.
//
// Steps:
//  1. Open/create state.db and cache.db with recommended pragmas.
//  2. Run schema migrations on both databases.
//  3. Run consistency repair (cross-db orphan cleanup).
//  4. Construct and return StateEngine.
func PersistenceBootstrap(stateDir, cacheDir string) (engine *StateEngine, closer io.Closer, err error) {
	if err := ensurePrivateDir(stateDir); err != nil {
		return nil, nil, fmt.Errorf("create state dir %s: %w", stateDir, err)
	}
	if err := ensurePrivateDir(cacheDir); err != nil {
		return nil, nil, fmt.Errorf("create cache dir %s: %w", cacheDir, err)
	}

	stateDBPath := filepath.Join(stateDir, "state.db")
	cacheDBPath := filepath.Join(cacheDir, "cache.db")

	stateDB, err := OpenDB(stateDBPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open state.db: %w", err)
	}

	cacheDB, err := OpenDB(cacheDBPath)
	if err != nil {
		stateDB.Close()
		return nil, nil, fmt.Errorf("open cache.db: %w", err)
	}

	if err := MigrateStateDB(stateDB); err != nil {
		stateDB.Close()
		cacheDB.Close()
		return nil, nil, fmt.Errorf("migrate state.db: %w", err)
	}

	if err := MigrateCacheDB(cacheDB); err != nil {
		stateDB.Close()
		cacheDB.Close()
		return nil, nil, fmt.Errorf("migrate cache.db: %w", err)
	}

	if err := RepairConsistency(stateDBPath, cacheDB); err != nil {
		stateDB.Close()
		cacheDB.Close()
		return nil, nil, fmt.Errorf("repair consistency: %w", err)
	}

	// Migrations and the repair pass write through the WAL, so re-apply the
	// private file modes once every side file exists.
	for _, path := range []string{stateDBPath, cacheDBPath} {
		if err := HardenDBFiles(path); err != nil {
			stateDB.Close()
			cacheDB.Close()
			return nil, nil, fmt.Errorf("harden %s: %w", path, err)
		}
	}

	stateRepo := newStateRepo(stateDB)
	cacheRepo := newCacheRepo(cacheDB)
	engine = newStateEngine(stateRepo, cacheRepo)

	return engine, &persistenceCloser{stateDB: stateDB, cacheDB: cacheDB}, nil
}
