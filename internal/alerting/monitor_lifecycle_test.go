package alerting

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMonitorStartsImmediatelyAndStopsIdempotently(t *testing.T) {
	reported := make(chan Kind, 2)
	m := &Monitor{Config: testAlertConfig, DiskPaths: []string{"fixture"}, DiskFreePercent: func(string) (float64, error) { return 1, nil }, Report: func(kind Kind) { reported <- kind }}
	stop := m.Start()
	select {
	case kind := <-reported:
		if kind != DiskPressure {
			t.Fatal("startup monitor did not report storage pressure")
		}
	case <-time.After(time.Second):
		t.Fatal("monitor did not perform an initial scan")
	}
	stop()
	stop()
	select {
	case <-reported:
		t.Fatal("stopped monitor still reported an incident")
	default:
	}
}

func TestDiskFreePercentUsesTheFixtureFilesystem(t *testing.T) {
	dir := t.TempDir()
	free, err := DiskFreePercent(dir)
	if err == ErrDiskUnsupported {
		t.Skip("platform does not implement disk inspection")
	}
	if err != nil || free < 0 || free > 100 {
		t.Fatalf("invalid filesystem availability: %g, %v", free, err)
	}
	if _, err := DiskFreePercent(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing filesystem path was silently accepted")
	}
}
