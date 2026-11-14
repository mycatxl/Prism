package main

import (
	"log"
	"strings"

	"prism/internal/alerting"
	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/safego"
	"prism/internal/usage"
)

func (a *prismApp) reportUsageError(err error) {
	if a != nil && a.alertNotifier != nil {
		a.alertNotifier.ReportError(alerting.UsageRefreshFailed, err)
	}
}

func (a *prismApp) usageLogf(format string, args ...any) {
	usage.DefaultLogf(format, args...)
	for _, arg := range args {
		if err, ok := arg.(error); ok {
			a.reportUsageError(err)
		}
	}
	text := strings.ToLower(format)
	for _, marker := range []string{"failed", "cannot read", "unresolved", "unknown write outcome", "timed out"} {
		if strings.Contains(text, marker) && a.alertNotifier != nil {
			a.alertNotifier.Report(alerting.UsageRefreshFailed)
			break
		}
	}
}

func (a *prismApp) startOperationalAlerts() {
	if a.alertNotifier == nil {
		return
	}
	a.alertNotifier.Start()
	a.stopPanicObserver = safego.SetPanicObserver(func(string) { a.alertNotifier.Report(alerting.BackgroundPanic) })
	m := &alerting.Monitor{
		Config:    a.runtimeCfg.Load,
		DiskPaths: []string{a.envCfg.StateDir, a.envCfg.CacheDir, a.envCfg.LogDir},
		PoolStats: func() (int, int) {
			pool := a.topoRuntime.pool
			healthy := 0
			pool.RangeNodes(func(hash node.Hash, entry *node.NodeEntry) bool {
				if !pool.IsNodeDisabled(hash) && entry.HasOutbound() && !entry.IsCircuitOpen() {
					healthy++
				}
				return true
			})
			return pool.Size(), healthy
		},
		Report: func(kind alerting.Kind) { a.alertNotifier.Report(kind) },
	}
	a.stopOperationalMonitor = m.Start()
	log.Println("Operational alert monitor started")
}

// The wrapper retains the journal's reservation/settlement semantics while
// observing every storage failure, including retries. No alert needs a DB row.
type alertJournalStore struct {
	usage.JournalStore
	report func(error)
}

func (s alertJournalStore) ReserveUsage(row model.UsageReservation) error {
	err := s.JournalStore.ReserveUsage(row)
	if err != nil && s.report != nil {
		s.report(err)
	}
	return err
}

func (s alertJournalStore) SettleUsage(id string, actual int64) (bool, error) {
	settled, err := s.JournalStore.SettleUsage(id, actual)
	if err != nil && s.report != nil {
		s.report(err)
	}
	return settled, err
}

func (s alertJournalStore) PendingUsage() ([]model.UsageReservation, error) {
	rows, err := s.JournalStore.PendingUsage()
	if err != nil && s.report != nil {
		s.report(err)
	}
	return rows, err
}
