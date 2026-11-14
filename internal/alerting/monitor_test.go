package alerting

import (
	"errors"
	"testing"
	"time"

	"prism/internal/config"
)

func TestMonitorCoversDiskPoolCollapseAndStartupGrace(t *testing.T) {
	cfg := testAlertConfig()
	now := time.Now()
	total, healthy := 4, 0
	free, diskErr := 50.0, error(nil)
	var reports []Kind
	m := &Monitor{
		Config:          func() *config.RuntimeConfig { return cfg },
		DiskPaths:       []string{"fixture-state", "fixture-cache", "fixture-log"},
		DiskFreePercent: func(string) (float64, error) { return free, diskErr },
		PoolStats:       func() (int, int) { return total, healthy },
		Report:          func(kind Kind) { reports = append(reports, kind) },
		StartedAt:       now,
	}
	m.Check(now)
	if len(reports) != 0 {
		t.Fatal("startup grace was ignored")
	}
	m.Check(now.Add(3 * time.Minute))
	if len(reports) != 1 || reports[0] != NodePoolUnavailable {
		t.Fatal("all-down pool was not detected")
	}
	healthy, free = 3, 5
	reports = nil
	m.Check(now.Add(4 * time.Minute))
	if len(reports) != 1 || reports[0] != DiskPressure {
		t.Fatal("disk pressure was not detected once per pass")
	}
	healthy, total, free = 0, 0, 50
	reports = nil
	m.Check(now.Add(5 * time.Minute))
	if len(reports) != 1 || reports[0] != NodePoolUnavailable {
		t.Fatal("a previously populated pool disappearing was ignored")
	}
	diskErr = errors.New("fixture stat failure")
	reports = nil
	m.Check(now.Add(6 * time.Minute))
	if reports[0] != DiskPressure {
		t.Fatal("storage inspection failure was ignored")
	}
	cfg.AlertTelegramEnabled = false
	reports = nil
	m.Check(now.Add(7 * time.Minute))
	if len(reports) != 0 {
		t.Fatal("disabled monitoring emitted alerts")
	}
}

func TestMonitorEmptyInstallationDoesNotAlert(t *testing.T) {
	var count int
	m := &Monitor{Config: testAlertConfig, PoolStats: func() (int, int) { return 0, 0 }, Report: func(Kind) { count++ }, StartedAt: time.Now().Add(-time.Hour)}
	m.Check(time.Now())
	if count != 0 {
		t.Fatal("a new empty installation is not a pool outage")
	}
}
