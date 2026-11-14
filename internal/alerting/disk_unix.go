//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package alerting

import (
	"errors"
	"golang.org/x/sys/unix"
)

func DiskFreePercent(path string) (float64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Blocks == 0 {
		return 0, errors.New("filesystem size is unavailable")
	}
	// Bavail excludes space reserved for root: Prism normally runs unprivileged.
	return 100 * float64(stat.Bavail) / float64(stat.Blocks), nil
}
