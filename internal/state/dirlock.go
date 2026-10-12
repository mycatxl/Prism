package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DirLock guards a data directory against a second Prism instance.
//
// Prism keeps routing state, sticky leases and quota counters in memory, so two
// processes opening the same state directory do not share them: each would
// flush its own view, hand out different exit IPs for the same account, run its
// own retention passes, and account for traffic separately. None of that is
// detectable from the outside, which is what makes a second instance worth
// refusing outright.
//
// The lock is an advisory file lock on a file inside the state directory. It is
// released by Release, by the process exiting, or by the kernel when the
// process dies, so a crashed instance never leaves a stale lock behind.
type DirLock struct {
	path string
	file *os.File
}

// lockFileName lives in the state directory next to state.db.
const lockFileName = "prism.lock"

// AcquireDirLock takes the lock for stateDir, creating the directory if needed.
//
// A second call for the same directory in the same process fails the same way a
// second process would, because the underlying lock is held by the file
// description rather than by the process.
func AcquireDirLock(stateDir string) (*DirLock, error) {
	if stateDir == "" {
		return nil, errors.New("state: state directory must not be empty")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("state: create %s: %w", stateDir, err)
	}

	path := filepath.Join(stateDir, lockFileName)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("state: open lock file: %w", err)
	}

	if err := lockFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("state: another Prism instance is using %s: %w", stateDir, err)
	}

	return &DirLock{path: path, file: file}, nil
}

// Release drops the lock. It is safe to call more than once.
func (l *DirLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(unlockErr, closeErr)
}

// Path returns the lock file path.
func (l *DirLock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
