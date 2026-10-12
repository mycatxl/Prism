//go:build !windows

package state

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes an exclusive, non-blocking advisory lock on file.
//
// LOCK_NB is what makes a second instance fail immediately instead of hanging
// at startup with no explanation. The lock belongs to the open file
// description, so it is dropped when the process exits or dies, and a crashed
// instance leaves nothing stale behind.
func lockFile(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
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
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
