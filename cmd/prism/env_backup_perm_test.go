package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// worldReadableEnv writes a .env the way a legacy installation may have it:
// mode 0644, i.e. readable by every local user. It holds both tokens.
func worldReadableEnv(t *testing.T, dir string) (path, content string) {
	t.Helper()
	path = filepath.Join(dir, envFileName)
	content = "# legacy .env\nPRISM_ADMIN_TOKEN=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n" +
		"PRISM_PROXY_TOKEN=fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	// WriteFile honours the umask, so set the mode explicitly.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	assertMode(t, path, 0o644)
	return path, content
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != want {
		t.Fatalf("%s mode = %o, want %o", path, perm, want)
	}
}

// TestMoveAsideForBackupSecuresTheBackup is the recurrence guard for.
//
// os.Rename keeps the source mode, so moving a world-readable .env aside used
// to leave a world-readable copy of the admin and proxy tokens behind - the
// very file `prism init` promises to keep private. The backup must be 0600,
// and it must still hold the previous content: the permission fix is not
// allowed to become a truncation.
func TestMoveAsideForBackupSecuresTheBackup(t *testing.T) {
	dir := t.TempDir()
	path, content := worldReadableEnv(t, dir)

	backupPath, err := moveAsideForBackup(path)
	if err != nil {
		t.Fatalf("moveAsideForBackup: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the original %s must be gone (stat err = %v)", path, err)
	}
	assertMode0600(t, backupPath)
	if got := readFileString(t, backupPath); got != content {
		t.Fatalf("backup content changed:\ngot  %q\nwant %q", got, content)
	}
}

// TestInitForceSecuresTheBackupOfAWorldReadableEnv runs the same case through
// the operator-visible entry point: `prism init --force` on an installation
// whose .env is still 0644.
func TestInitForceSecuresTheBackupOfAWorldReadableEnv(t *testing.T) {
	dir := t.TempDir()
	_, original := worldReadableEnv(t, dir)

	var stdout, stderr bytes.Buffer
	if err := runInitCommand([]string{"--dir", dir, "--force"}, &stdout, &stderr); err != nil {
		t.Fatalf("init --force: %v (stderr=%s)", err, stderr.String())
	}

	backups, err := filepath.Glob(filepath.Join(dir, ".env.bak.*"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected exactly 1 timestamped backup, got %d (%v)", len(backups), backups)
	}
	assertMode0600(t, backups[0])
	if got := readFileString(t, backups[0]); got != original {
		t.Fatalf("backup must hold the previous .env content:\ngot  %q\nwant %q", got, original)
	}
	assertMode0600(t, filepath.Join(dir, envFileName))
}

// TestRestoreSecuresTheAsidesItCreates covers the same family inside
// `prism restore`: the databases it renames to *.pre-restore-<timestamp> keep
// their old mode, and state.db holds the intel provider api keys. The aside
// copy must be 0600 like the file that replaces it.
func TestRestoreSecuresTheAsidesItCreates(t *testing.T) {
	backupDir := makeTestBackup(t)

	targetState := t.TempDir()
	targetCache := t.TempDir()
	staleState := filepath.Join(targetState, "state.db")
	staleCache := filepath.Join(targetCache, "cache.db")
	for _, path := range []string{staleState, staleCache} {
		if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("chmod %s: %v", path, err)
		}
	}

	opts := restoreOptions{StateDir: targetState, CacheDir: targetCache, Port: 0}
	if err := restoreBackup(opts, backupDir, true, io.Discard); err != nil {
		t.Fatalf("restoreBackup: %v", err)
	}

	for _, stale := range []string{staleState, staleCache} {
		asides, err := filepath.Glob(stale + ".pre-restore-*")
		if err != nil {
			t.Fatalf("glob pre-restore files: %v", err)
		}
		if len(asides) != 1 {
			t.Fatalf("expected 1 *.pre-restore-* file for %s, got %d (%v)", stale, len(asides), asides)
		}
		assertMode0600(t, asides[0])
	}
}
