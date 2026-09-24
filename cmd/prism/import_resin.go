package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"prism/internal/config"
	"prism/internal/state"
)

const (
	// preImportSuffix marks every database an import moves out of the way.
	preImportSuffix = ".pre-import-"
	// requestLogFilePrefix is the rolling request-log database naming shared by
	// the upstream Resin releases and Prism.
	requestLogFilePrefix = "request_logs-"
)

// importResinSources points at the upstream Resin data directories.
type importResinSources struct {
	StateDir string
	CacheDir string
	LogDir   string
}

// importResinTarget describes the Prism installation that receives the data.
type importResinTarget struct {
	StateDir      string
	CacheDir      string
	LogDir        string
	ListenAddress string
	Port          int
}

// importResinSummary reports what the import moved into place.
type importResinSummary struct {
	Platforms     int
	Subscriptions int
	Nodes         int
	Leases        int
	Endpoints     int
	RequestLogDBs int
}

// --- import-resin ---

func runImportResinCommand(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("import-resin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fromState := fs.String("from-state", "", "directory holding the upstream Resin state.db (required)")
	fromCache := fs.String("from-cache", "", "directory holding the upstream Resin cache.db (required)")
	fromLog := fs.String("from-log", "", "optional directory holding upstream Resin request-log databases")
	force := fs.Bool("force", false, "replace existing databases (they are renamed to *.pre-import-<timestamp> first)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("import-resin: unexpected argument %q", fs.Arg(0))
	}
	if strings.TrimSpace(*fromState) == "" {
		return errors.New("import-resin: --from-state DIR is required")
	}
	if strings.TrimSpace(*fromCache) == "" {
		return errors.New("import-resin: --from-cache DIR is required")
	}

	// Mirror `prism restore`: ./.env is loaded first and never overrides the
	// process environment.
	if err := loadDotenvFile(envFileName); err != nil {
		return err
	}
	envCfg, err := config.LoadEnvConfig()
	if err != nil {
		return fmt.Errorf("import-resin: load configuration: %w", err)
	}

	summary, err := importResinData(
		importResinTarget{
			StateDir:      envCfg.StateDir,
			CacheDir:      envCfg.CacheDir,
			LogDir:        envCfg.LogDir,
			ListenAddress: envCfg.ListenAddress,
			Port:          envCfg.ProxyPort,
		},
		importResinSources{
			StateDir: *fromState,
			CacheDir: *fromCache,
			LogDir:   *fromLog,
		},
		*force,
		stdout,
	)
	if err != nil {
		return err
	}
	printImportResinSummary(stdout, summary)
	fmt.Fprintln(stdout, "Import complete.")
	return nil
}

// importResinData copies the upstream Resin databases into the Prism state and
// cache directories with `VACUUM INTO`, applies the Prism migrations and
// reports the imported row counts.
func importResinData(
	target importResinTarget,
	src importResinSources,
	force bool,
	logw io.Writer,
) (*importResinSummary, error) {
	if strings.TrimSpace(target.StateDir) == "" || strings.TrimSpace(target.CacheDir) == "" {
		return nil, errors.New("import-resin: PRISM_STATE_DIR and PRISM_CACHE_DIR must be configured")
	}
	if strings.TrimSpace(src.StateDir) == "" || strings.TrimSpace(src.CacheDir) == "" {
		return nil, errors.New("import-resin: --from-state and --from-cache must not be empty")
	}

	// 1. Never touch a live installation: a connectable port or a live pid file
	// wins over everything else, including --force.
	if reason, running := detectRunningService(restoreOptions{
		StateDir:      target.StateDir,
		CacheDir:      target.CacheDir,
		ListenAddress: target.ListenAddress,
		Port:          target.Port,
	}); running {
		return nil, fmt.Errorf("import-resin: refusing to run while a Prism instance is active (%s); stop it first", reason)
	}

	copies := []struct {
		name string
		src  string
		dest string
	}{
		{"state.db", filepath.Join(src.StateDir, "state.db"), filepath.Join(target.StateDir, "state.db")},
		{"cache.db", filepath.Join(src.CacheDir, "cache.db"), filepath.Join(target.CacheDir, "cache.db")},
	}
	for _, copy := range copies {
		if _, err := os.Stat(copy.src); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("import-resin: %s not found", copy.src)
			}
			return nil, fmt.Errorf("import-resin: inspect %s: %w", copy.src, err)
		}
	}

	// 2. Replacing existing data requires an explicit --force.
	if !force {
		var existing []string
		for _, copy := range copies {
			if _, err := os.Stat(copy.dest); err == nil {
				existing = append(existing, copy.dest)
			}
		}
		if len(existing) > 0 {
			return nil, fmt.Errorf(
				"import-resin: %s already exists; pass --force to replace it (existing files are renamed to *%s<timestamp>)",
				strings.Join(existing, ", "),
				preImportSuffix,
			)
		}
	}

	// 3. Move the current databases aside and copy the Resin ones with
	// `VACUUM INTO`, which only ever produces a consistent snapshot.
	stamp := time.Now().Format("20060102-150405")
	for _, copy := range copies {
		if err := os.MkdirAll(filepath.Dir(copy.dest), 0o755); err != nil {
			return nil, fmt.Errorf("import-resin: create %s: %w", filepath.Dir(copy.dest), err)
		}
		if err := moveDatabaseAside(copy.dest, stamp, logw); err != nil {
			return nil, err
		}
		if err := vacuumInto(copy.src, copy.dest); err != nil {
			return nil, fmt.Errorf("import-resin: copy %s: %w", copy.name, err)
		}
		if err := os.Chmod(copy.dest, 0o600); err != nil {
			return nil, fmt.Errorf("import-resin: secure %s: %w", copy.dest, err)
		}
		fmt.Fprintf(logw, "Copied %s to %s\n", copy.src, copy.dest)
	}

	// 4. Apply the Prism migrations (state.db 000010..000014, cache.db 000002)
	// so the imported data is usable by this build.
	if err := migrateImportedDatabase(copies[0].dest, "state.db", state.MigrateStateDB); err != nil {
		return nil, err
	}
	if err := migrateImportedDatabase(copies[1].dest, "cache.db", state.MigrateCacheDB); err != nil {
		return nil, err
	}

	// 5. Request-log databases are optional.
	requestLogDBs, err := importResinRequestLogs(src.LogDir, target.LogDir, force, stamp, logw)
	if err != nil {
		return nil, err
	}

	// 6. Summarise what has been imported.
	summary := &importResinSummary{RequestLogDBs: requestLogDBs}
	if err := countDatabaseRows(copies[0].dest, map[string]*int{
		"platforms":     &summary.Platforms,
		"subscriptions": &summary.Subscriptions,
		"endpoints":     &summary.Endpoints,
	}); err != nil {
		return nil, err
	}
	if err := countDatabaseRows(copies[1].dest, map[string]*int{
		"nodes_static": &summary.Nodes,
		"leases":       &summary.Leases,
	}); err != nil {
		return nil, err
	}
	return summary, nil
}

// printImportResinSummary writes the platform/subscription/node/lease/endpoint
// counts of a completed import.
func printImportResinSummary(w io.Writer, summary *importResinSummary) {
	if summary == nil {
		return
	}
	fmt.Fprintln(w, "Imported Resin data:")
	fmt.Fprintf(w, "  platforms:     %d\n", summary.Platforms)
	fmt.Fprintf(w, "  subscriptions: %d\n", summary.Subscriptions)
	fmt.Fprintf(w, "  nodes:         %d\n", summary.Nodes)
	fmt.Fprintf(w, "  leases:        %d\n", summary.Leases)
	fmt.Fprintf(w, "  endpoints:     %d\n", summary.Endpoints)
	if summary.RequestLogDBs > 0 {
		fmt.Fprintf(w, "  request logs:  %d database(s)\n", summary.RequestLogDBs)
	}
}

// moveDatabaseAside renames an existing database, including its WAL sidecars,
// to *.pre-import-<stamp> before the import writes new files.
func moveDatabaseAside(path, stamp string, logw io.Writer) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(candidate); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("import-resin: inspect %s: %w", candidate, err)
		}
		aside := candidate + preImportSuffix + stamp
		if err := os.Rename(candidate, aside); err != nil {
			return fmt.Errorf("import-resin: move %s aside: %w", candidate, err)
		}
		fmt.Fprintf(logw, "Moved %s to %s\n", candidate, aside)
	}
	return nil
}

func migrateImportedDatabase(path, name string, migrate func(*sql.DB) error) error {
	db, err := state.OpenDB(path)
	if err != nil {
		return fmt.Errorf("import-resin: open %s: %w", name, err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return fmt.Errorf("import-resin: migrate %s: %w", name, err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("import-resin: close %s: %w", name, err)
	}
	return nil
}

// countDatabaseRows counts the rows of the given tables over a read-only
// connection and writes each result through the matching counter.
func countDatabaseRows(path string, counters map[string]*int) error {
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return fmt.Errorf("import-resin: open %s: %w", path, err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	tables := make([]string, 0, len(counters))
	for table := range counters {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			return fmt.Errorf("import-resin: count %s in %s: %w", table, path, err)
		}
		*counters[table] = count
	}
	return nil
}

// importResinRequestLogs copies the optional upstream request-log databases into
// logDir and returns how many were imported.
func importResinRequestLogs(fromDir, logDir string, force bool, stamp string, logw io.Writer) (int, error) {
	if strings.TrimSpace(fromDir) == "" {
		return 0, nil
	}
	if strings.TrimSpace(logDir) == "" {
		return 0, errors.New("import-resin: PRISM_LOG_DIR must be configured when --from-log is used")
	}
	sources, err := requestLogSources(fromDir)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return 0, fmt.Errorf("import-resin: create %s: %w", logDir, err)
	}

	imported := 0
	for _, src := range sources {
		dest, err := requestLogDestination(logDir, src)
		if err != nil {
			return 0, err
		}
		if !force {
			if _, err := os.Stat(dest); err == nil {
				return 0, fmt.Errorf(
					"import-resin: %s already exists; pass --force to replace it (existing files are renamed to *%s<timestamp>)",
					dest,
					preImportSuffix,
				)
			}
		}
		if err := moveDatabaseAside(dest, stamp, logw); err != nil {
			return 0, err
		}
		if err := vacuumInto(src, dest); err != nil {
			return 0, fmt.Errorf("import-resin: copy %s: %w", src, err)
		}
		if err := migrateResinRequestLogSchema(dest); err != nil {
			return 0, fmt.Errorf("import-resin: migrate %s: %w", dest, err)
		}
		if err := os.Chmod(dest, 0o600); err != nil {
			return 0, fmt.Errorf("import-resin: secure %s: %w", dest, err)
		}
		fmt.Fprintf(logw, "Copied request log %s to %s\n", src, dest)
		imported++
	}
	return imported, nil
}

// requestLogSources lists the upstream request-log databases found in dir.
// Files that already follow the request_logs-<unix_ms>.db naming win over any
// other *.db file, so an import of a directory holding a single differently
// named database still works.
func requestLogSources(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("import-resin: read --from-log %s: %w", dir, err)
	}
	var named, other []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if strings.HasPrefix(entry.Name(), requestLogFilePrefix) {
			named = append(named, path)
			continue
		}
		other = append(other, path)
	}
	sort.Strings(named)
	sort.Strings(other)
	if len(named) > 0 {
		return named, nil
	}
	if len(other) == 0 {
		return nil, fmt.Errorf("import-resin: no request-log database found in %s", dir)
	}
	return other, nil
}

// requestLogDestination keeps the upstream file name when it already matches the
// rolling naming and derives one from the modification time otherwise, because
// the request-log repository only ever reads request_logs-<unix_ms>.db files.
func requestLogDestination(logDir, src string) (string, error) {
	name := filepath.Base(src)
	if strings.HasPrefix(name, requestLogFilePrefix) {
		return filepath.Join(logDir, name), nil
	}
	info, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("import-resin: inspect %s: %w", src, err)
	}
	return filepath.Join(logDir, fmt.Sprintf("%s%d.db", requestLogFilePrefix, info.ModTime().UnixMilli())), nil
}

// migrateResinRequestLogSchema renames the upstream resin_error column to the
// Prism spelling so request-log queries keep working on imported databases.
func migrateResinRequestLogSchema(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	hasLegacy, err := databaseHasColumn(db, "request_logs", "resin_error")
	if err != nil || !hasLegacy {
		return err
	}
	hasCurrent, err := databaseHasColumn(db, "request_logs", "prism_error")
	if err != nil || hasCurrent {
		return err
	}
	if _, err := db.Exec("ALTER TABLE request_logs RENAME COLUMN resin_error TO prism_error"); err != nil {
		return fmt.Errorf("rename resin_error column: %w", err)
	}
	return nil
}

// databaseHasColumn reports whether table has a column named column.
func databaseHasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, fmt.Errorf("inspect table %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue sql.NullString
			primaryKey   int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
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
