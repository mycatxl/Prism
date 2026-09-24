package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"prism/internal/buildinfo"
	"prism/internal/config"
)

const (
	envFileName = ".env"
	// pidFileName is written next to state.db so that `prism restore` can detect
	// a live instance even when its listen port is unreachable.
	pidFileName = "prism.pid"

	// backupManifestName is the metadata file written next to the database
	// copies produced by `prism backup`.
	backupManifestName = "manifest.json"
)

// runCLI dispatches argv (without the program name) to a subcommand.
func runCLI(args []string) error {
	if len(args) == 0 {
		// Bare `prism` behaves exactly like `prism run`.
		return run()
	}

	command := args[0]
	rest := args[1:]
	switch command {
	case "run":
		if len(rest) > 0 {
			return fmt.Errorf("run: unexpected argument %q (flags must be configured through the environment/.env)", rest[0])
		}
		return run()
	case "init":
		return runInitCommand(rest, os.Stdout, os.Stderr)
	case "version":
		return runVersionCommand(rest, os.Stdout, os.Stderr)
	case "check-config":
		return runCheckConfigCommand(rest, os.Stdout, os.Stderr)
	case "backup":
		return runBackupCommand(rest, os.Stdout, os.Stderr)
	case "restore":
		return runRestoreCommand(rest, os.Stdout, os.Stderr)
	case "import-resin":
		return runImportResinCommand(rest, os.Stdout, os.Stderr)
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return nil
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", command)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `Prism - single-port proxy and control plane

Usage:
  prism [run]                     Start the service (loads ./.env first)
  prism init [--dir DIR] [--force]
                                  Write DIR/.env (mode 0600) with fresh tokens
  prism version                   Print version, commit, build time and build tags
  prism check-config              Validate and print the effective configuration
  prism backup --out DIR          Online backup of state.db, cache.db and intel.db
  prism restore --from DIR [--force]
                                  Verify and restore a backup created by "prism backup"
  prism import-resin --from-state DIR --from-cache DIR [--from-log DIR] [--force]
                                  Import an upstream Resin installation (state.db, cache.db, request logs)
  prism help                      Print this help

Exit codes:
  0  success
  1  fatal error (configuration, backup, restore or runtime failure)
`)
}

// --- init ---

func runInitCommand(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", ".", "directory that receives the generated .env")
	force := fs.Bool("force", false, "overwrite an existing .env (it is first moved to .env.bak.<timestamp>)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("init: unexpected argument %q", fs.Arg(0))
	}

	targetDir := *dir
	if strings.TrimSpace(targetDir) == "" {
		return errors.New("init: --dir must not be empty")
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("init: create %s: %w", targetDir, err)
	}
	target := filepath.Join(targetDir, envFileName)

	if _, err := os.Stat(target); err == nil {
		if !*force {
			return fmt.Errorf("init: %s already exists; pass --force to overwrite it (the current file is moved to %s.bak.<timestamp> first)", target, target)
		}
		backupPath, err := moveAsideForBackup(target)
		if err != nil {
			return fmt.Errorf("init: back up %s: %w", target, err)
		}
		fmt.Fprintf(stdout, "Existing config backed up to %s\n", backupPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("init: inspect %s: %w", target, err)
	}

	adminToken, err := generateToken()
	if err != nil {
		return fmt.Errorf("init: generate admin token: %w", err)
	}
	proxyToken, err := generateToken()
	if err != nil {
		return fmt.Errorf("init: generate proxy token: %w", err)
	}

	content := renderInitEnv(adminToken, proxyToken, time.Now())
	if err := writeFile0600(target, content, true); err != nil {
		return fmt.Errorf("init: write %s: %w", target, err)
	}

	fmt.Fprintf(stdout, "Wrote %s (mode 0600)\n\n", target)
	fmt.Fprintln(stdout, "Admin token (displayed once, store it somewhere safe):")
	fmt.Fprintln(stdout, adminToken)
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "The proxy token and all other settings are stored in %s.\n", target)
	fmt.Fprintln(stdout, "Start the service with `prism run` and open the UI at /ui/ on the configured listen address.")
	return nil
}

// generateToken returns a 32-byte cryptographically random token in hex form.
func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func renderInitEnv(adminToken, proxyToken string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Prism environment file generated by `prism init` on %s.\n", now.UTC().Format(time.RFC3339))
	b.WriteString("# Keep this file private (mode 0600): it holds the admin and proxy tokens.\n")
	fmt.Fprintf(&b, "PRISM_ADMIN_TOKEN=%s\n", adminToken)
	fmt.Fprintf(&b, "PRISM_PROXY_TOKEN=%s\n", proxyToken)
	b.WriteString("\n# Primary listener: UI, API, HTTP/SOCKS5 proxy, reverse proxy, token-action.\n")
	b.WriteString("PRISM_LISTEN_ADDRESS=127.0.0.1\n")
	b.WriteString("PRISM_PORT=2260\n")
	b.WriteString("\n# Optional independent management listener (host:port, empty = disabled).\n")
	b.WriteString("# It serves only /ui, /api and /healthz.\n")
	b.WriteString("# PRISM_ADMIN_LISTEN=127.0.0.1:2261\n")
	b.WriteString("\n# Data directories (relative to the working directory of the process).\n")
	b.WriteString("PRISM_STATE_DIR=./.local/state\n")
	b.WriteString("PRISM_CACHE_DIR=./.local/cache\n")
	b.WriteString("PRISM_LOG_DIR=./.local/logs\n")
	return b.String()
}

// moveAsideForBackup renames path to "<path>.bak.<timestamp>" and returns the
// backup path. A numeric suffix is appended when that name is already taken.
func moveAsideForBackup(path string) (string, error) {
	stamp := time.Now().Format("20060102-150405")
	backupPath := path + ".bak." + stamp
	for i := 1; ; i++ {
		_, err := os.Stat(backupPath)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		backupPath = fmt.Sprintf("%s.bak.%s.%d", path, stamp, i)
	}
	if err := os.Rename(path, backupPath); err != nil {
		return "", err
	}
	return backupPath, nil
}

// writeFile0600 writes content to path with mode 0600. When exclusive is true an
// already existing file is never truncated.
func writeFile0600(path, content string, exclusive bool) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if exclusive {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// --- version ---

func runVersionCommand(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("version: unexpected argument %q", fs.Arg(0))
	}

	tags := buildinfo.TagList()
	tagsOut := "(none)"
	if len(tags) > 0 {
		tagsOut = strings.Join(tags, ",")
	}
	fmt.Fprintf(stdout, "Prism\n")
	fmt.Fprintf(stdout, "Version:   %s\n", buildinfo.Version)
	fmt.Fprintf(stdout, "GitCommit: %s\n", buildinfo.GitCommit)
	fmt.Fprintf(stdout, "BuildTime: %s\n", buildinfo.BuildTime)
	fmt.Fprintf(stdout, "BuildTags: %s\n", tagsOut)
	return nil
}

// --- check-config ---

func runCheckConfigCommand(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("check-config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("check-config: unexpected argument %q", fs.Arg(0))
	}

	// Mirror `prism run`: ./.env is loaded first and never overrides the
	// process environment.
	if err := loadDotenvFile(envFileName); err != nil {
		return err
	}
	envCfg, err := config.LoadEnvConfig()
	if err != nil {
		fmt.Fprintf(stderr, "check-config: invalid configuration: %v\n", err)
		return errors.New("check-config: configuration is invalid")
	}
	printEnvConfig(stdout, envCfg)
	return nil
}

// printEnvConfig prints the effective environment configuration with every
// secret masked. Tokens and provider keys never reach stdout.
func printEnvConfig(w io.Writer, cfg *config.EnvConfig) {
	if cfg == nil {
		return
	}
	adminListen := cfg.AdminListen
	if adminListen == "" {
		adminListen = "(disabled)"
	}
	rows := [][2]string{
		{"PRISM_AUTH_VERSION", string(cfg.AuthVersion)},
		{"PRISM_LISTEN_ADDRESS", cfg.ListenAddress},
		{"PRISM_PORT", strconv.Itoa(cfg.ProxyPort)},
		{"PRISM_ADMIN_LISTEN", adminListen},
		{"PRISM_STATE_DIR", cfg.StateDir},
		{"PRISM_CACHE_DIR", cfg.CacheDir},
		{"PRISM_LOG_DIR", cfg.LogDir},
		{"PRISM_API_MAX_BODY_BYTES", strconv.Itoa(cfg.APIMaxBodyBytes)},
		{"PRISM_PROBE_CONCURRENCY", strconv.Itoa(cfg.ProbeConcurrency)},
		{"PRISM_PROBE_TIMEOUT", cfg.ProbeTimeout.String()},
		{"PRISM_MAX_LATENCY_TABLE_ENTRIES", strconv.Itoa(cfg.MaxLatencyTableEntries)},
		{"PRISM_DEFAULT_PLATFORM_STICKY_TTL", cfg.DefaultPlatformStickyTTL.String()},
		{"PRISM_GEOIP_UPDATE_SCHEDULE", cfg.GeoIPUpdateSchedule},
		{"PRISM_PROXY_BYPASS", strings.Join(cfg.ProxyBypassRules, ",")},
		{"PRISM_ADMIN_TOKEN", maskSecret(cfg.AdminToken)},
		{"PRISM_PROXY_TOKEN", maskSecret(cfg.ProxyToken)},
		{"PRISM_QUALITY_ENABLED", strconv.FormatBool(cfg.Quality.Enabled)},
		{"PRISM_QUALITY_API_KEY", maskSecret(cfg.Quality.APIKey)},
		{"PRISM_ABUSEIPDB_API_KEY", maskSecret(cfg.Quality.AbuseAPIKey)},
	}
	fmt.Fprintln(w, "# Prism effective configuration (secrets masked)")
	for _, row := range rows {
		fmt.Fprintf(w, "%s=%s\n", row[0], row[1])
	}
}

// maskSecret reports whether a secret is configured without disclosing it.
func maskSecret(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(unset)"
	}
	return "***"
}

// --- backup ---

type backupManifestFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type backupManifest struct {
	Version   string               `json:"version"`
	GitCommit string               `json:"git_commit"`
	BuildTime string               `json:"build_time"`
	BuildTags []string             `json:"build_tags,omitempty"`
	CreatedAt string               `json:"created_at"`
	Files     []backupManifestFile `json:"files"`
}

func runBackupCommand(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outDir := fs.String("out", "", "output directory for the backup (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("backup: unexpected argument %q", fs.Arg(0))
	}
	if strings.TrimSpace(*outDir) == "" {
		return errors.New("backup: --out DIR is required")
	}

	if err := loadDotenvFile(envFileName); err != nil {
		return err
	}
	envCfg, err := config.LoadEnvConfig()
	if err != nil {
		return fmt.Errorf("backup: load configuration: %w", err)
	}

	manifest, err := performBackup(envCfg.StateDir, envCfg.CacheDir, *outDir, stdout)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Backup complete: %d database(s) written to %s\n", len(manifest.Files), *outDir)
	return nil
}

// performBackup copies each existing database into outDir with `VACUUM INTO`,
// using independent read-only connections so a running service is not disturbed.
// `.env` and any other file are deliberately not part of a backup.
func performBackup(stateDir, cacheDir, outDir string, logw io.Writer) (*backupManifest, error) {
	if strings.TrimSpace(stateDir) == "" || strings.TrimSpace(cacheDir) == "" {
		return nil, errors.New("backup: state and cache directories must be configured")
	}
	if strings.TrimSpace(outDir) == "" {
		return nil, errors.New("backup: output directory must not be empty")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, fmt.Errorf("backup: create output directory: %w", err)
	}
	if err := os.Chmod(outDir, 0o700); err != nil {
		return nil, fmt.Errorf("backup: secure output directory: %w", err)
	}

	sources := []struct {
		name string
		path string
	}{
		{"state.db", filepath.Join(stateDir, "state.db")},
		{"cache.db", filepath.Join(cacheDir, "cache.db")},
		{"intel.db", filepath.Join(stateDir, "intel.db")},
	}

	manifest := &backupManifest{
		Version:   buildinfo.Version,
		GitCommit: buildinfo.GitCommit,
		BuildTime: buildinfo.BuildTime,
		BuildTags: buildinfo.TagList(),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Files:     []backupManifestFile{},
	}

	for _, src := range sources {
		if _, err := os.Stat(src.path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(logw, "Skipping %s (not present)\n", src.name)
				continue
			}
			return nil, fmt.Errorf("backup: inspect %s: %w", src.path, err)
		}

		dest := filepath.Join(outDir, src.name)
		if _, err := os.Stat(dest); err == nil {
			fmt.Fprintf(logw, "Replacing existing %s\n", src.name)
			if err := os.Remove(dest); err != nil {
				return nil, fmt.Errorf("backup: remove existing %s: %w", dest, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("backup: inspect %s: %w", dest, err)
		}

		if err := vacuumInto(src.path, dest); err != nil {
			return nil, fmt.Errorf("backup: copy %s: %w", src.name, err)
		}
		if err := os.Chmod(dest, 0o600); err != nil {
			return nil, fmt.Errorf("backup: secure %s: %w", dest, err)
		}
		sum, size, err := fileSHA256(dest)
		if err != nil {
			return nil, fmt.Errorf("backup: hash %s: %w", dest, err)
		}
		manifest.Files = append(manifest.Files, backupManifestFile{Name: src.name, Size: size, SHA256: sum})
		fmt.Fprintf(logw, "Wrote %s (%d bytes, sha256 %s)\n", dest, size, sum)
	}

	if len(manifest.Files) == 0 {
		return nil, fmt.Errorf("backup: no databases found in %s and %s", stateDir, cacheDir)
	}

	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("backup: encode manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	manifestPath := filepath.Join(outDir, backupManifestName)
	if err := writeFile0600(manifestPath, string(encoded), false); err != nil {
		return nil, fmt.Errorf("backup: write manifest: %w", err)
	}
	fmt.Fprintf(logw, "Wrote %s\n", manifestPath)
	return manifest, nil
}

// vacuumInto writes a consistent snapshot of srcPath into destPath.
func vacuumInto(srcPath, destPath string) error {
	db, err := sql.Open("sqlite", readOnlyDSN(srcPath))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec("VACUUM INTO " + sqliteStringLiteral(destPath)); err != nil {
		return err
	}
	return nil
}

func readOnlyDSN(path string) string {
	return "file:" + filepath.ToSlash(path) + "?mode=ro"
}

func sqliteStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()

	hasher := sha256.New()
	size, err := io.Copy(hasher, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

// --- restore ---

// restoreOptions describes the live installation a restore targets.
type restoreOptions struct {
	StateDir      string
	CacheDir      string
	ListenAddress string
	Port          int
}

func runRestoreCommand(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fromDir := fs.String("from", "", "backup directory produced by `prism backup` (required)")
	force := fs.Bool("force", false, "replace existing databases (they are renamed to *.pre-restore-<timestamp>)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("restore: unexpected argument %q", fs.Arg(0))
	}
	if strings.TrimSpace(*fromDir) == "" {
		return errors.New("restore: --from DIR is required")
	}

	if err := loadDotenvFile(envFileName); err != nil {
		return err
	}
	envCfg, err := config.LoadEnvConfig()
	if err != nil {
		return fmt.Errorf("restore: load configuration: %w", err)
	}

	opts := restoreOptions{
		StateDir:      envCfg.StateDir,
		CacheDir:      envCfg.CacheDir,
		ListenAddress: envCfg.ListenAddress,
		Port:          envCfg.ProxyPort,
	}
	if err := restoreBackup(opts, *fromDir, *force, stdout); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Restore complete.")
	return nil
}

// restoreBackup verifies the manifest, refuses to run against a live instance,
// moves existing databases aside and then copies the backup files into place.
func restoreBackup(opts restoreOptions, fromDir string, force bool, logw io.Writer) error {
	if strings.TrimSpace(fromDir) == "" {
		return errors.New("restore: backup directory must not be empty")
	}

	// 1. Never touch a live installation: a connectable port or a live pid file
	// wins over everything else, including --force.
	if reason, running := detectRunningService(opts); running {
		return fmt.Errorf("restore: refusing to run while a Prism instance is active (%s); stop it first", reason)
	}

	// 2. Verify the manifest before modifying anything.
	manifest, err := readBackupManifest(filepath.Join(fromDir, backupManifestName))
	if err != nil {
		return err
	}
	if len(manifest.Files) == 0 {
		return errors.New("restore: manifest lists no files")
	}
	targets, err := manifestTargets(opts, manifest)
	if err != nil {
		return err
	}
	for _, file := range manifest.Files {
		path := filepath.Join(fromDir, file.Name)
		sum, size, err := fileSHA256(path)
		if err != nil {
			return fmt.Errorf("restore: read %s: %w", path, err)
		}
		if size != file.Size {
			return fmt.Errorf("restore: %s size mismatch: manifest=%d actual=%d", file.Name, file.Size, size)
		}
		if sum != file.SHA256 {
			return fmt.Errorf("restore: %s sha256 mismatch: manifest=%s actual=%s", file.Name, file.SHA256, sum)
		}
	}

	// 3. Replacing existing data requires an explicit --force.
	if !force {
		var existing []string
		for _, file := range manifest.Files {
			if _, err := os.Stat(targets[file.Name]); err == nil {
				existing = append(existing, targets[file.Name])
			}
		}
		if len(existing) > 0 {
			return fmt.Errorf(
				"restore: %s already exists; pass --force to replace it (existing files are renamed to *.pre-restore-<timestamp>)",
				strings.Join(existing, ", "),
			)
		}
	}

	// 4. Move the current databases aside, then copy the verified backups.
	stamp := time.Now().Format("20060102-150405")
	for _, file := range manifest.Files {
		dest := targets[file.Name]
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("restore: create %s: %w", filepath.Dir(dest), err)
		}
		for _, candidate := range []string{dest, dest + "-wal", dest + "-shm"} {
			if _, err := os.Stat(candidate); err != nil {
				continue
			}
			aside := candidate + ".pre-restore-" + stamp
			if err := os.Rename(candidate, aside); err != nil {
				return fmt.Errorf("restore: move %s aside: %w", candidate, err)
			}
			fmt.Fprintf(logw, "Moved %s to %s\n", candidate, aside)
		}
		if err := copyFile0600(filepath.Join(fromDir, file.Name), dest); err != nil {
			return fmt.Errorf("restore: copy %s: %w", file.Name, err)
		}
		fmt.Fprintf(logw, "Restored %s\n", dest)
	}
	return nil
}

func readBackupManifest(path string) (*backupManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("restore: read manifest %s: %w", path, err)
	}
	var manifest backupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("restore: parse manifest %s: %w", path, err)
	}
	return &manifest, nil
}

// manifestTargets maps every manifest file name to its destination.
func manifestTargets(opts restoreOptions, manifest *backupManifest) (map[string]string, error) {
	targets := make(map[string]string, len(manifest.Files))
	for _, file := range manifest.Files {
		switch file.Name {
		case "state.db", "intel.db":
			if strings.TrimSpace(opts.StateDir) == "" {
				return nil, errors.New("restore: state directory must be configured")
			}
			targets[file.Name] = filepath.Join(opts.StateDir, file.Name)
		case "cache.db":
			if strings.TrimSpace(opts.CacheDir) == "" {
				return nil, errors.New("restore: cache directory must be configured")
			}
			targets[file.Name] = filepath.Join(opts.CacheDir, file.Name)
		default:
			return nil, fmt.Errorf("restore: unknown file %q in manifest", file.Name)
		}
	}
	return targets, nil
}

func copyFile0600(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dest, 0o600)
}

// detectRunningService reports whether a Prism instance still owns the target
// directories. Port 0 disables the TCP probe (used by tests).
func detectRunningService(opts restoreOptions) (string, bool) {
	if strings.TrimSpace(opts.StateDir) != "" {
		pidPath := filepath.Join(opts.StateDir, pidFileName)
		if data, err := os.ReadFile(pidPath); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 && processAlive(pid) {
				return fmt.Sprintf("%s holds live pid %d", pidPath, pid), true
			}
		}
	}
	if opts.Port > 0 {
		host := strings.TrimSpace(opts.ListenAddress)
		if host == "" {
			host = "127.0.0.1"
		}
		addr := net.JoinHostPort(host, strconv.Itoa(opts.Port))
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return fmt.Sprintf("%s accepts connections", addr), true
		}
	}
	return "", false
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}
