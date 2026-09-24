package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

const (
	migrationsPath = "migrations"

	// Keep these markers in sync with the SQL files under migrations/.
	intelVersionBaseSchema        = 1
	intelVersionViaNodeNodeBudget = 2
	intelLatestVersion            = intelVersionViaNodeNodeBudget

	migrationTable = "schema_migrations"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// LatestVersion is the highest known intel.db migration version.
const LatestVersion = intelLatestVersion

// Migrate applies every pending intel.db migration.
func Migrate(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("migrate intel.db: nil db")
	}

	sourceDriver, err := iofs.New(migrationsFS, migrationsPath)
	if err != nil {
		return fmt.Errorf("init source: %w", err)
	}

	dbDriver, err := migratesqlite.WithInstance(db, &migratesqlite.Config{MigrationsTable: migrationTable})
	if err != nil {
		return fmt.Errorf("init db driver: %w", err)
	}

	migrator, err := migrate.NewWithInstance("iofs", sourceDriver, "sqlite", dbDriver)
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}

	if err := migrator.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("up: %w", err)
	}
	return nil
}

// SchemaVersion reports the currently applied migration version. It returns 0
// for a database without migration metadata.
func (s *Store) SchemaVersion() (int, bool, error) {
	db, err := s.conn()
	if err != nil {
		return 0, false, err
	}
	var version int
	var dirty bool
	if err := db.QueryRow(fmt.Sprintf("SELECT version, dirty FROM %s LIMIT 1", migrationTable)).
		Scan(&version, &dirty); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return version, dirty, nil
}
