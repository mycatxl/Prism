// Package usage maintains the traffic counters the console reads: it
// summarizes hourly rows into daily rows once they age out of the hourly
// window, and prunes rows past their retention.
//
// The summary and the prune happen in the same pass, in this order:
// aggregate the aged hourly rows, add them to the daily table, then delete the
// hourly rows that were aggregated. Doing the delete only after the daily write
// succeeds is what keeps a crash from losing traffic.
package usage

import (
	"fmt"
	"log"
	"sync"
	"time"

	"prism/internal/model"
)

// Retention windows and the scheduling defaults.
const (
	// DefaultHourlyKeep is how long an hour row stays in usage_hourly before it
	// is summarized into usage_daily.
	DefaultHourlyKeep = 30 * 24 * time.Hour
	// DefaultDailyKeep is how long a summarized day row is kept.
	DefaultDailyKeep = 400 * 24 * time.Hour
	// DefaultInterval is how often a pass runs. It is deliberately much longer
	// than the metric pruner's five minutes: state.db has a single write
	// connection shared with the flush worker, and a summary pass is heavier
	// than a bounded delete.
	DefaultInterval = time.Hour
)

// Store is the persistence surface the retention pass needs. *state.StateRepo
// satisfies it.
type Store interface {
	// SumUsageHourlyBefore aggregates hourly rows older than cutoffNs into
	// daily buckets.
	SumUsageHourlyBefore(cutoffNs int64) ([]model.UsageRow, error)
	// UpsertUsageDaily adds rows to the daily table, summing into existing
	// buckets rather than replacing them.
	UpsertUsageDaily(rows []model.UsageRow) error
	// DeleteUsageHourlyBefore removes hourly rows older than cutoffNs.
	DeleteUsageHourlyBefore(cutoffNs int64) (int64, error)
	// DeleteUsageDailyBefore removes daily rows older than cutoffNs.
	DeleteUsageDailyBefore(cutoffNs int64) (int64, error)
}

// Retention summarizes and prunes the usage tables.
type Retention struct {
	Store Store
	// HourlyKeep is the age at which an hourly row is summarized. Zero uses
	// DefaultHourlyKeep.
	HourlyKeep time.Duration
	// DailyKeep is how long daily rows are kept. Zero uses DefaultDailyKeep.
	DailyKeep time.Duration
	// DailyKeepFn, when set, supplies the daily window at each pass instead of
	// DailyKeep, so a change made in the console applies to the next pass
	// rather than to the next restart. It wins over DailyKeep; a non-positive
	// answer falls back to DefaultDailyKeep.
	DailyKeepFn func() time.Duration
	// Interval is how often Start runs a pass. Zero uses DefaultInterval.
	Interval time.Duration
	// Logf receives progress messages. Nil silences them.
	Logf func(format string, args ...any)
}

// Result reports what one pass did.
type Result struct {
	// Summarized is how many hourly rows were folded into daily rows.
	Summarized int64
	// HourlyRemoved is how many hourly rows were deleted.
	HourlyRemoved int64
	// DailyRemoved is how many daily rows were pruned.
	DailyRemoved int64
}

func (r *Retention) hourlyKeep() time.Duration {
	if r.HourlyKeep > 0 {
		return r.HourlyKeep
	}
	return DefaultHourlyKeep
}

func (r *Retention) dailyKeep() time.Duration {
	if r.DailyKeepFn != nil {
		if keep := r.DailyKeepFn(); keep > 0 {
			return keep
		}
	}
	if r.DailyKeep > 0 {
		return r.DailyKeep
	}
	return DefaultDailyKeep
}

func (r *Retention) interval() time.Duration {
	if r.Interval > 0 {
		return r.Interval
	}
	return DefaultInterval
}

func (r *Retention) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// RunOnce summarizes and prunes relative to nowNs.
//
// The order matters: aggregate, write the daily rows, then delete the hourly
// rows that were aggregated. A failure before the delete leaves the hourly rows
// in place, so the next pass retries them instead of losing the traffic.
func (r *Retention) RunOnce(nowNs int64) (Result, error) {
	var result Result
	if r.Store == nil {
		return result, nil
	}
	if nowNs <= 0 {
		nowNs = time.Now().UnixNano()
	}

	hourlyCutoff := nowNs - int64(r.hourlyKeep())

	// 1. Aggregate the aged hourly rows.
	aggregated, err := r.Store.SumUsageHourlyBefore(hourlyCutoff)
	if err != nil {
		return result, fmt.Errorf("summarize usage: %w", err)
	}

	// 2. Add them to the daily table before removing the source rows.
	if len(aggregated) > 0 {
		if err := r.Store.UpsertUsageDaily(aggregated); err != nil {
			return result, fmt.Errorf("write daily usage: %w", err)
		}
	}

	// 3. Only now is it safe to drop the hourly rows.
	hourlyRemoved, err := r.Store.DeleteUsageHourlyBefore(hourlyCutoff)
	if err != nil {
		return result, fmt.Errorf("delete hourly usage: %w", err)
	}
	result.HourlyRemoved = hourlyRemoved
	result.Summarized = int64(len(aggregated))

	// 4. Prune the daily rows that are past their own window.
	dailyCutoff := nowNs - int64(r.dailyKeep())
	dailyRemoved, err := r.Store.DeleteUsageDailyBefore(dailyCutoff)
	if err != nil {
		return result, fmt.Errorf("delete daily usage: %w", err)
	}
	result.DailyRemoved = dailyRemoved

	return result, nil
}

// Start runs a pass every Interval until the returned stop function is called.
// The first pass runs after one interval, not immediately: a restart must not
// trigger a summary while the flush worker is still settling.
func (r *Retention) Start() func() {
	if r == nil || r.Store == nil {
		return func() {}
	}

	done := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(r.interval())
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				result, err := r.RunOnce(time.Now().UnixNano())
				if err != nil {
					// A pass that fails is retried on the next tick: the hourly
					// rows are still there because the delete is last.
					r.logf("usage: retention pass failed: %v", err)
					continue
				}
				if result.Summarized > 0 || result.HourlyRemoved > 0 || result.DailyRemoved > 0 {
					r.logf("usage: summarized %d hourly rows, removed %d hourly and %d daily rows",
						result.Summarized, result.HourlyRemoved, result.DailyRemoved)
				}
			case <-done:
				return
			}
		}
	}()

	return func() { once.Do(func() { close(done) }) }
}

// DefaultLogf logs through the standard logger, matching how the other
// background jobs report.
func DefaultLogf(format string, args ...any) {
	log.Printf(format, args...)
}
