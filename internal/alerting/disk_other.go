//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package alerting

func DiskFreePercent(string) (float64, error) { return 0, ErrDiskUnsupported }
