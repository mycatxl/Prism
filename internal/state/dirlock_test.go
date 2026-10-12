package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireDirLock_TakesAndReleases(t *testing.T) {
	dir := t.TempDir()

	lock, err := AcquireDirLock(dir)
	if err != nil {
		t.Fatalf("AcquireDirLock: %v", err)
	}
	if lock.Path() != filepath.Join(dir, lockFileName) {
		t.Errorf("lock path: got %q, want %q", lock.Path(), filepath.Join(dir, lockFileName))
	}

	// Releasing frees the directory for the next instance.
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	second, err := AcquireDirLock(dir)
	if err != nil {
		t.Fatalf("AcquireDirLock after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// A second instance must be refused rather than left to corrupt the store.
func TestAcquireDirLock_RefusesSecondHolder(t *testing.T) {
	dir := t.TempDir()

	first, err := AcquireDirLock(dir)
	if err != nil {
		t.Fatalf("first AcquireDirLock: %v", err)
	}
	defer func() { _ = first.Release() }()

	second, err := AcquireDirLock(dir)
	if err == nil {
		_ = second.Release()
		t.Fatal("a second lock on the same directory must fail")
	}
	// The message has to name the state directory: it is the only clue the
	// operator gets about which instance is in the way.
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the error should name the state directory, got %v", err)
	}
}

// Release is idempotent so a deferred call next to an explicit one is harmless.
func TestDirLock_ReleaseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireDirLock(dir)
	if err != nil {
		t.Fatalf("AcquireDirLock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}

	// A nil lock is safe too.
	var nilLock *DirLock
	if err := nilLock.Release(); err != nil {
		t.Errorf("Release on a nil lock: %v", err)
	}
	if nilLock.Path() != "" {
		t.Errorf("Path on a nil lock: got %q, want empty", nilLock.Path())
	}
}

func TestAcquireDirLock_CreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")

	lock, err := AcquireDirLock(dir)
	if err != nil {
		t.Fatalf("AcquireDirLock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("state dir mode: got %o, want 700", perm)
	}
}

func TestAcquireDirLock_RejectsEmptyDir(t *testing.T) {
	if _, err := AcquireDirLock(""); err == nil {
		t.Fatal("an empty state directory must be rejected")
	}
}

// The lock file itself must be private: it sits next to state.db.
func TestAcquireDirLock_LockFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireDirLock(dir)
	if err != nil {
		t.Fatalf("AcquireDirLock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	info, err := os.Stat(lock.Path())
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("lock file mode: got %o, want 600", perm)
	}
}

// Two different state directories must not contend with each other.
func TestAcquireDirLock_DifferentDirectoriesAreIndependent(t *testing.T) {
	first, err := AcquireDirLock(t.TempDir())
	if err != nil {
		t.Fatalf("first AcquireDirLock: %v", err)
	}
	defer func() { _ = first.Release() }()

	second, err := AcquireDirLock(t.TempDir())
	if err != nil {
		t.Fatalf("a lock on another directory must succeed: %v", err)
	}
	defer func() { _ = second.Release() }()
}
