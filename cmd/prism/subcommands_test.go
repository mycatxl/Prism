package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"prism/internal/state"
)

// --- helpers ---

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func envValue(t *testing.T, content, key string) string {
	t.Helper()
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, value, found := strings.Cut(trimmed, "=")
		if found && name == key {
			return value
		}
	}
	t.Fatalf("key %s not found in generated env file", key)
	return ""
}

func assertMode0600(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("%s mode = %o, want 600", path, perm)
	}
}

// createSQLiteDatabase writes a tiny database with one row so backups have
// real content to vacuum.
func createSQLiteDatabase(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec("CREATE TABLE probe (id INTEGER PRIMARY KEY, label TEXT)"); err != nil {
		t.Fatalf("create table in %s: %v", path, err)
	}
	if _, err := db.Exec("INSERT INTO probe (label) VALUES ('backup-test')"); err != nil {
		t.Fatalf("insert into %s: %v", path, err)
	}
}

func backupManifestNames(manifest *backupManifest) []string {
	names := make([]string, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		names = append(names, file.Name)
	}
	return names
}

// --- init ---

func TestInitCommandWritesEnvWith0600(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	if err := runInitCommand([]string{"--dir", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("init: %v (stderr=%s)", err, stderr.String())
	}

	envPath := filepath.Join(dir, ".env")
	assertMode0600(t, envPath)

	content := readFileString(t, envPath)
	adminToken := envValue(t, content, "PRISM_ADMIN_TOKEN")
	proxyToken := envValue(t, content, "PRISM_PROXY_TOKEN")
	if len(adminToken) != 64 || len(proxyToken) != 64 {
		t.Fatalf("tokens must be 32 random bytes in hex (got %d/%d chars)", len(adminToken), len(proxyToken))
	}
	if adminToken == proxyToken {
		t.Fatal("admin and proxy tokens must differ")
	}
	if got := envValue(t, content, "PRISM_LISTEN_ADDRESS"); got != "127.0.0.1" {
		t.Fatalf("PRISM_LISTEN_ADDRESS = %q, want 127.0.0.1", got)
	}
	if got := envValue(t, content, "PRISM_PORT"); got != "2260" {
		t.Fatalf("PRISM_PORT = %q, want 2260", got)
	}
	for _, key := range []string{"PRISM_STATE_DIR", "PRISM_CACHE_DIR", "PRISM_LOG_DIR"} {
		if got := envValue(t, content, key); got == "" {
			t.Fatalf("%s must be set in the generated file", key)
		}
	}

	// The admin token is printed exactly once; the proxy token never is.
	out := stdout.String()
	if count := strings.Count(out, adminToken); count != 1 {
		t.Fatalf("admin token printed %d times, want exactly 1", count)
	}
	if strings.Contains(out, proxyToken) {
		t.Fatal("proxy token must not be printed by init")
	}
}

func TestInitCommandRefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := runInitCommand([]string{"--dir", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("first init: %v", err)
	}
	original := readFileString(t, filepath.Join(dir, ".env"))

	stdout.Reset()
	stderr.Reset()
	err := runInitCommand([]string{"--dir", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("init without --force must refuse to overwrite an existing .env")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error should point at --force, got %v", err)
	}
	if got := readFileString(t, filepath.Join(dir, ".env")); got != original {
		t.Fatal("refused init must not modify .env")
	}
}

func TestInitCommandWithForceCreatesTimestampedBackup(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := runInitCommand([]string{"--dir", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("first init: %v", err)
	}
	original := readFileString(t, filepath.Join(dir, ".env"))

	stdout.Reset()
	stderr.Reset()
	if err := runInitCommand([]string{"--dir", dir, "--force"}, &stdout, &stderr); err != nil {
		t.Fatalf("init --force: %v", err)
	}

	backups, err := filepath.Glob(filepath.Join(dir, ".env.bak.*"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected exactly 1 timestamped backup, got %d (%v)", len(backups), backups)
	}
	if got := readFileString(t, backups[0]); got != original {
		t.Fatal("backup must hold the previous .env content")
	}
	assertMode0600(t, backups[0])

	updated := readFileString(t, filepath.Join(dir, ".env"))
	if updated == original {
		t.Fatal("--force must write a new .env with fresh tokens")
	}
	assertMode0600(t, filepath.Join(dir, ".env"))
}

// --- backup ---

func TestBackupProducesConsistentDatabasesAndManifest(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cacheDir := filepath.Join(t.TempDir(), "cache")

	engine, closer, err := state.PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatalf("persistence bootstrap: %v", err)
	}
	_ = engine
	if err := closer.Close(); err != nil {
		t.Fatalf("close persistence: %v", err)
	}
	// intel.db only exists from WP08 on; a backup must pick it up when present.
	createSQLiteDatabase(t, filepath.Join(stateDir, "intel.db"))

	// Secrets must never be copied into a backup.
	if err := os.WriteFile(filepath.Join(stateDir, ".env"), []byte("PRISM_ADMIN_TOKEN=secret\n"), 0o600); err != nil {
		t.Fatalf("write state dir .env: %v", err)
	}

	outDir := filepath.Join(t.TempDir(), "backup")
	var log bytes.Buffer
	manifest, err := performBackup(stateDir, cacheDir, outDir, &log)
	if err != nil {
		t.Fatalf("performBackup: %v (%s)", err, log.String())
	}

	wantNames := []string{"state.db", "cache.db", "intel.db"}
	gotNames := backupManifestNames(manifest)
	if strings.Join(gotNames, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("manifest files = %v, want %v", gotNames, wantNames)
	}
	if manifest.CreatedAt == "" || manifest.Version == "" {
		t.Fatalf("manifest must record a timestamp and a version: %+v", manifest)
	}

	info, err := os.Stat(outDir)
	if err != nil {
		t.Fatalf("stat output dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("backup dir mode = %o, want 700", perm)
	}

	if _, err := os.Stat(filepath.Join(outDir, ".env")); err == nil {
		t.Fatal("backup must not contain .env")
	}

	for _, file := range manifest.Files {
		path := filepath.Join(outDir, file.Name)
		assertMode0600(t, path)

		sum, size, err := fileSHA256(path)
		if err != nil {
			t.Fatalf("hash %s: %v", file.Name, err)
		}
		if sum != file.SHA256 {
			t.Fatalf("%s sha256 = %s, manifest = %s", file.Name, sum, file.SHA256)
		}
		if size != file.Size {
			t.Fatalf("%s size = %d, manifest = %d", file.Name, size, file.Size)
		}

		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		var check string
		if err := db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil {
			_ = db.Close()
			t.Fatalf("quick_check %s: %v", file.Name, err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close %s: %v", file.Name, err)
		}
		if check != "ok" {
			t.Fatalf("quick_check %s = %q, want ok", file.Name, check)
		}
	}

	// The manifest on disk must be valid JSON and match the returned value.
	var stored backupManifest
	if err := json.Unmarshal([]byte(readFileString(t, filepath.Join(outDir, backupManifestName))), &stored); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(stored.Files) != len(manifest.Files) {
		t.Fatalf("stored manifest has %d files, want %d", len(stored.Files), len(manifest.Files))
	}
	assertMode0600(t, filepath.Join(outDir, backupManifestName))
}

// --- restore ---

func TestRestoreRefusesWhileServiceIsRunning(t *testing.T) {
	backupDir := makeTestBackup(t)

	targetState := t.TempDir()
	targetCache := t.TempDir()
	// A live pid file (this test process) marks the installation as running.
	if err := os.WriteFile(filepath.Join(targetState, pidFileName), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	opts := restoreOptions{StateDir: targetState, CacheDir: targetCache, Port: 0}
	err := restoreBackup(opts, backupDir, true, io.Discard)
	if err == nil {
		t.Fatal("restore must refuse to run while the service is active")
	}
	if !strings.Contains(err.Error(), "active") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(targetState, "state.db")); statErr == nil {
		t.Fatal("refused restore must not write any database")
	}
}

func TestRestoreSucceedsWithForceWhenNotRunning(t *testing.T) {
	backupDir := makeTestBackup(t)

	targetState := t.TempDir()
	targetCache := t.TempDir()
	// Stale databases are present, so --force is required.
	staleState := filepath.Join(targetState, "state.db")
	staleCache := filepath.Join(targetCache, "cache.db")
	for _, path := range []string{staleState, staleCache} {
		if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	opts := restoreOptions{StateDir: targetState, CacheDir: targetCache, Port: 0}
	if err := restoreBackup(opts, backupDir, false, io.Discard); err == nil {
		t.Fatal("restore without --force must refuse to replace existing databases")
	}

	if err := restoreBackup(opts, backupDir, true, io.Discard); err != nil {
		t.Fatalf("restore --force: %v", err)
	}

	restored, _, err := fileSHA256(staleState)
	if err != nil {
		t.Fatalf("hash restored state.db: %v", err)
	}
	expected, _, err := fileSHA256(filepath.Join(backupDir, "state.db"))
	if err != nil {
		t.Fatalf("hash backup state.db: %v", err)
	}
	if restored != expected {
		t.Fatal("restored state.db does not match the backup")
	}
	assertMode0600(t, staleState)

	asides, err := filepath.Glob(staleState + ".pre-restore-*")
	if err != nil {
		t.Fatalf("glob pre-restore files: %v", err)
	}
	if len(asides) != 1 {
		t.Fatalf("expected 1 *.pre-restore-* file, got %d", len(asides))
	}
	if got := readFileString(t, asides[0]); got != "stale" {
		t.Fatalf("pre-restore file content = %q, want %q", got, "stale")
	}
}

func TestRestoreRejectsTamperedBackup(t *testing.T) {
	backupDir := makeTestBackup(t)
	if err := os.WriteFile(filepath.Join(backupDir, "cache.db"), []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper backup: %v", err)
	}

	opts := restoreOptions{StateDir: t.TempDir(), CacheDir: t.TempDir(), Port: 0}
	err := restoreBackup(opts, backupDir, true, io.Discard)
	if err == nil {
		t.Fatal("restore must reject a backup whose sha256 does not match the manifest")
	}
	if !strings.Contains(err.Error(), "sha256") && !strings.Contains(err.Error(), "size") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// makeTestBackup builds a source installation and returns a fresh backup of it.
func makeTestBackup(t *testing.T) string {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	cacheDir := filepath.Join(t.TempDir(), "cache")
	if _, closer, err := state.PersistenceBootstrap(stateDir, cacheDir); err != nil {
		t.Fatalf("persistence bootstrap: %v", err)
	} else if err := closer.Close(); err != nil {
		t.Fatalf("close persistence: %v", err)
	}

	backupDir := filepath.Join(t.TempDir(), "backup")
	if _, err := performBackup(stateDir, cacheDir, backupDir, io.Discard); err != nil {
		t.Fatalf("performBackup: %v", err)
	}
	return backupDir
}

// --- check-config ---

func TestCheckConfigNeverPrintsSecrets(t *testing.T) {
	const (
		adminToken = "0123456789abcdef0123456789abcdef"
		proxyToken = "fedcba9876543210fedcba9876543210"
		qualityKey = "quality-key-should-never-be-printed"
	)
	t.Setenv("PRISM_ADMIN_TOKEN", adminToken)
	t.Setenv("PRISM_PROXY_TOKEN", proxyToken)
	t.Setenv("PRISM_QUALITY_API_KEY", qualityKey)
	t.Setenv("PRISM_ADMIN_LISTEN", "127.0.0.1:12345")

	var stdout, stderr bytes.Buffer
	if err := runCheckConfigCommand(nil, &stdout, &stderr); err != nil {
		t.Fatalf("check-config: %v", err)
	}

	out := stdout.String()
	for _, secret := range []string{adminToken, proxyToken, qualityKey} {
		if strings.Contains(out, secret) {
			t.Fatalf("check-config output leaked a secret: %s", out)
		}
		if strings.Contains(stderr.String(), secret) {
			t.Fatal("check-config stderr leaked a secret")
		}
	}
	if !strings.Contains(out, "PRISM_ADMIN_TOKEN=***") {
		t.Fatalf("check-config must mark the admin token as set, got: %s", out)
	}
	if !strings.Contains(out, "PRISM_ADMIN_LISTEN=127.0.0.1:12345") {
		t.Fatalf("check-config must print the effective admin listener, got: %s", out)
	}
}

// --- version ---

func TestVersionCommandPrintsBuildInfo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runVersionCommand(nil, &stdout, &stderr); err != nil {
		t.Fatalf("version: %v", err)
	}
	out := stdout.String()
	for _, label := range []string{"Version:", "GitCommit:", "BuildTime:", "BuildTags:"} {
		if !strings.Contains(out, label) {
			t.Fatalf("version output missing %s: %s", label, out)
		}
	}
}

// --- admin listener ---

func TestAdminOnlyHandlerRejectsProxyPaths(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	handler := newAdminOnlyHandler(next)

	allowed := []string{"/", "/healthz", "/api/v1/system/info", "/ui/", "/ui/assets/app.js"}
	for _, path := range allowed {
		reached = false
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if !reached || recorder.Code != http.StatusOK {
			t.Fatalf("%s must reach the API handler (reached=%v status=%d)", path, reached, recorder.Code)
		}
	}

	rejected := []string{"/Default:token/http/example.com/", "/foo", "/healthz/extra"}
	for _, path := range rejected {
		reached = false
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if reached {
			t.Fatalf("%s must not reach the API handler", path)
		}
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, recorder.Code)
		}
	}

	reached = false
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodConnect, "/", nil))
	if reached || recorder.Code != http.StatusNotFound {
		t.Fatal("CONNECT must never be served by the admin listener")
	}
}

func TestStartAdminListenerDisabledWhenAddressEmpty(t *testing.T) {
	listener, err := startAdminListener("", http.NotFoundHandler())
	if err != nil {
		t.Fatalf("startAdminListener(\"\"): %v", err)
	}
	if listener != nil {
		t.Fatal("an empty PRISM_ADMIN_LISTEN must disable the listener")
	}
	if listener.Errors() != nil {
		t.Fatal("Errors() on a disabled listener must be nil")
	}
	if err := listener.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown on a disabled listener: %v", err)
	}
}
