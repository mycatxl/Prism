//go:build windows

package state

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive, non-blocking lock on file.
//
// The lock covers the whole file and is not shared, so a second instance fails
// immediately rather than waiting. Windows releases it when the handle closes,
// which includes the process dying, so no stale lock survives a crash.
func lockFile(file *os.File) error {
	overlapped := &windows.Overlapped{}
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, overlapped,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return errors.New("the state directory is locked by another process")
		}
		return err
	}
	return nil
}

// unlockFile releases the lock taken by lockFile.
func unlockFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &windows.Overlapped{})
}
