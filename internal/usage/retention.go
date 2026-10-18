// Package usage maintains the traffic counters the console reads: it
// summarizes hourly rows into daily rows once they age out of the hourly
// window, and prunes rows past their retention.
//
// The summary and the prune happen in the same pass: the store folds the aged
// hourly rows into the daily table and deletes them in one transaction, in that
// single call, and the pass then prunes the daily rows past their own window.
// Folding and deleting together is what keeps a crash from double-counting:
// there is no state in which the traffic is already in the daily table while
// its hourly rows are still there to be summed a second time, and the daily
// write adds rather than replaces, so such a state would never repair itself.
package usage

import (
	"fmt"
	"log"
	"sync"
	"time"

	"prism/internal/safego"
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
	// SummarizeUsageHourlyBefore folds every hourly row older than cutoffNs
	// into the daily table and deletes the rows it folded, in one
	// transaction. The returned count is how many hourly rows were folded
	// and removed.
	//
	// It is deliberately the only summary call this pass has. The three-call
	// sequence it replaces - aggregate, write the daily rows, delete the
	// hourly rows - can fail between the write and the delete, and because
	// the daily write adds to the bucket instead of replacing it, the next
	// pass would sum those same rows again and double-count the day for good.
	SummarizeUsageHourlyBefore(cutoffNs int64) (int64, error)
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
	//
	// It shares its source with Summarized: the store folds the hourly rows
	// into the daily table and deletes them in the same transaction, so every
	// row counted as summarized is also a row that went away, and there is no
	// intermediate state in which one of the two happened without the other.
	// The two fields are kept apart because callers and log lines read them
	// separately.
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
// The summary is one call into the store, which folds the aged hourly rows into
// the daily table and deletes them in a single transaction: the hourly buckets
// are folded and removed together, so there is no intermediate state in which
// the traffic sits in the daily table while its hourly rows are still there.
// A failure inside the call leaves both tables untouched and the next pass
// retries the same rows.
func (r *Retention) RunOnce(nowNs int64) (Result, error) {
	var result Result
	if r.Store == nil {
		return result, nil
	}
	if nowNs <= 0 {
		nowNs = time.Now().UnixNano()
	}

	hourlyCutoff := nowNs - int64(r.hourlyKeep())

	// 1. Fold the aged hourly rows into the daily table and drop them, in one
	// transaction inside the store. The count is therefore both the number of
	// rows summarized and the number removed.
	summarized, err := r.Store.SummarizeUsageHourlyBefore(hourlyCutoff)
	if err != nil {
		return result, fmt.Errorf("summarize usage: %w", err)
	}
	result.Summarized = summarized
	result.HourlyRemoved = summarized

	// 2. Prune the daily rows that are past their own window.
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
				// A panic inside one pass must not end the loop: the next
				// tick retries, exactly as it does after a failed pass.
				safego.Run("a usage retention pass", func() {
					result, err := r.RunOnce(time.Now().UnixNano())
					if err != nil {
						// A pass that fails is retried on the next tick: the
						// summary is one transaction, so a failure left the
						// hourly rows where they were with nothing folded.
						r.logf("usage: retention pass failed: %v", err)
						return
					}
					if result.Summarized > 0 || result.HourlyRemoved > 0 || result.DailyRemoved > 0 {
						r.logf("usage: summarized %d hourly rows, removed %d hourly and %d daily rows",
							result.Summarized, result.HourlyRemoved, result.DailyRemoved)
					}
				})
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
