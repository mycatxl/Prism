package alerting

import (
	"context"
	"errors"
	"sync"
	"time"

	"prism/internal/config"
	"prism/internal/safego"
)

const MonitorInterval = time.Minute

type Monitor struct {
	Config          func() *config.RuntimeConfig
	DiskPaths       []string
	DiskFreePercent func(string) (float64, error)
	PoolStats       func() (total, healthy int)
	Report          func(Kind)
	StartedAt       time.Time
	hadNodes        bool
}

func (m *Monitor) Check(now time.Time) {
	if m == nil || m.Config == nil || m.Report == nil {
		return
	}
	c := m.Config()
	if c == nil || !c.AlertTelegramEnabled {
		return
	}
	threshold := c.AlertDiskFreePercent
	if threshold <= 0 {
		threshold = config.DefaultAlertDiskFreePercent
	}
	stat := m.DiskFreePercent
	if stat == nil {
		stat = DiskFreePercent
	}
	for _, path := range m.DiskPaths {
		free, err := stat(path)
		if errors.Is(err, ErrDiskUnsupported) {
			continue
		}
		if err != nil || free < float64(threshold) {
			m.Report(DiskPressure)
			break
		}
	}
	if m.PoolStats != nil {
		total, healthy := m.PoolStats()
		if total > 0 {
			m.hadNodes = true
		}
		if now.Sub(m.StartedAt) >= 2*time.Minute && healthy == 0 && m.hadNodes {
			m.Report(NodePoolUnavailable)
		}
	}
}

func (m *Monitor) Start() func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var once sync.Once
	if m.StartedAt.IsZero() {
		m.StartedAt = time.Now()
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(MonitorInterval)
		defer ticker.Stop()
		for {
			safego.Run("the operational alert scan", func() { m.Check(time.Now()) })
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { once.Do(cancel); <-done }
}

var ErrDiskUnsupported = errors.New("disk free-space inspection is unsupported on this operating system")
