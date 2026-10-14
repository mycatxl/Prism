package usage

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"prism/internal/model"
)

// DefaultPeriodCheckInterval is how often the rollover check runs.
//
// A period boundary is not an event that has to be caught within milliseconds:
// what matters is that the allowance comes back, and a minute of lateness on the
// first day of the month costs the user nothing noticeable. The check is cheap
// because it reads a handful of subscriptions.
const DefaultPeriodCheckInterval = time.Minute

// Period names a rollover window. They are the values plans.period accepts.
//
// Naming is plain on purpose: an operator writing a plan should not have to
// learn a grammar for a length of time.
const (
	PeriodOnce  = "once"
	PeriodDay   = "day"
	PeriodWeek  = "week"
	PeriodMonth = "month"
	// DefaultPeriod is what a plan gets when it names none. It matches the
	// default the plan repository writes, so the two cannot disagree.
	DefaultPeriod = "30d"
)

// PeriodStore is the persistence surface the rollover needs.
type PeriodStore interface {
	// ListUserSubscriptions returns every subscription, whatever its status:
	// the worker decides which ones to consider.
	ListUserSubscriptions() ([]model.UserSubscription, error)
	// GetPlan finds the plan a subscription was opened against. A deleted plan
	// is reported as an error, and the rollover falls back to the default
	// period rather than refusing to advance.
	GetPlan(id string) (*model.Plan, error)
	// ResetSubscriptionPeriod starts a new period, guarded on the period start
	// it expects. It returns state.ErrNotFound when the subscription is gone,
	// and nil when another writer got there first.
	ResetSubscriptionPeriod(id string, fromPeriodStartNs, toPeriodStartNs int64) error
}

// PeriodReset advances subscriptions whose usage period has elapsed.
//
// Without it an allowance is a one-time grant: a user who uses their monthly
// traffic on the first day is refused for the rest of the month and forever
// after, because nothing ever moves the period on. The database and the running
// aggregate are reset together for the same reason - one without the other
// leaves a user whose new period has begun still being refused.
type PeriodReset struct {
	// Store is where the subscriptions live. Nil disables the rollover.
	Store PeriodStore
	// Counters holds the running totals the quota reads. Nil means only the
	// persisted counter is reset, which is a deployment that never started the
	// accounting in the first place.
	Counters *Counters
	// Interval is how often Start checks. Zero uses DefaultPeriodCheckInterval.
	Interval time.Duration
	// Logf receives progress messages. Nil silences them.
	Logf func(format string, args ...any)

	// mu serializes passes: two overlapping passes could both read the same
	// period start and race the guarded write, which is survivable but wasteful.
	mu sync.Mutex
}

// PeriodResetResult reports what one pass did.
type PeriodResetResult struct {
	// Checked is how many subscriptions were examined.
	Checked int
	// Rolled is how many periods were advanced.
	Rolled int
}

func (p *PeriodReset) interval() time.Duration {
	if p.Interval > 0 {
		return p.Interval
	}
	return DefaultPeriodCheckInterval
}

func (p *PeriodReset) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

// RunOnce rolls over every subscription whose period has elapsed relative to
// nowNs.
//
// A subscription is skipped when it is not active: an expired one keeps its
// numbers as the record of what it was granted, and a suspended one should not
// quietly start a fresh allowance while it is suspended. A plan that no longer
// exists falls back to the default period, which is a guess - but the
// alternative is a subscription that never rolls over, and its usage is still
// recorded either way.
func (p *PeriodReset) RunOnce(nowNs int64) (PeriodResetResult, error) {
	var result PeriodResetResult
	if p == nil || p.Store == nil {
		return result, nil
	}
	if nowNs <= 0 {
		nowNs = time.Now().UnixNano()
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	subs, err := p.Store.ListUserSubscriptions()
	if err != nil {
		return result, fmt.Errorf("list subscriptions: %w", err)
	}

	for _, sub := range subs {
		if sub.Status != model.SubscriptionStatusActive {
			continue
		}
		// Checked counts the active subscriptions, which is what a caller
		// watching this number wants: how many allowances are being kept up to
		// date. One with no period start is still counted, and then skipped.
		result.Checked++
		if sub.PeriodStartNs <= 0 {
			// Nothing to advance, and inventing a start would reset usage based
			// on a guess.
			continue
		}

		period := p.periodFor(sub)
		if period == PeriodOnce {
			continue // a one-time grant never comes back
		}

		next, rolled := advancePeriod(sub.PeriodStartNs, nowNs, period)
		if !rolled {
			continue
		}

		err := p.Store.ResetSubscriptionPeriod(sub.ID, sub.PeriodStartNs, next)
		if err != nil {
			// The repository answers nil when another writer got there first, so
			// anything here is a real failure - a missing row included - worth
			// logging and worth retrying on the next pass.
			p.logf("usage: reset period for subscription %s: %v", sub.ID, err)
			continue
		}
		// The running total is cleared only after the database has the new
		// period, so a failed write never hands out an allowance that is still
		// spent on disk.
		p.Counters.ResetUser(sub.UserID)
		result.Rolled++
		p.logf("usage: started a new %s period for subscription %s (user %s)", period, sub.ID, sub.UserID)
	}
	return result, nil
}

// periodFor resolves the rollover window of a subscription.
//
// The plan is looked up rather than read from the snapshot because the snapshot
// carries the limits, not the period. A plan that has been deleted leaves the
// subscription with the default: the alternative is refusing to roll over, which
// would leave the user with no allowance at all.
func (p *PeriodReset) periodFor(sub model.UserSubscription) string {
	if sub.PlanID == "" {
		return DefaultPeriod
	}
	plan, err := p.Store.GetPlan(sub.PlanID)
	if err != nil || plan == nil || plan.Period == "" {
		return DefaultPeriod
	}
	return plan.Period
}

// advancePeriod moves currentStartNs forward in whole periods until the window
// containing nowNs is found, and reports whether it moved at all.
func advancePeriod(currentStartNs, nowNs int64, period string) (int64, bool) {
	if currentStartNs > nowNs {
		// The period has not begun yet; there is nothing to roll over.
		return currentStartNs, false
	}

	if period == PeriodMonth {
		return advanceByCalendarMonth(currentStartNs, nowNs)
	}

	length, ok := periodLength(period)
	if !ok || length <= 0 {
		return currentStartNs, false
	}

	elapsed := nowNs - currentStartNs
	periods := elapsed / int64(length)
	if periods == 0 {
		return currentStartNs, false
	}
	return currentStartNs + periods*int64(length), true
}

// advanceByCalendarMonth walks month boundaries rather than adding a fixed 30
// days, because a month is not a fixed length and a subscription that started
// on the 31st must not drift its own boundary every cycle.
//
// The arithmetic is done in whole months and then checked, so a long-lived
// subscription is advanced in one step instead of a loop that would run once per
// month since it started.
func advanceByCalendarMonth(currentStartNs, nowNs int64) (int64, bool) {
	start := time.Unix(0, currentStartNs)
	now := time.Unix(0, nowNs)

	months := (now.Year()-start.Year())*12 + int(now.Month()) - int(start.Month())
	if months <= 0 {
		return currentStartNs, false
	}
	// The candidate can land after now, because a boundary day may not exist in
	// the target month; step back one month in that case.
	candidate := addMonthsClamped(start, months)
	if candidate.After(now) {
		months--
		if months <= 0 {
			return currentStartNs, false
		}
		candidate = addMonthsClamped(start, months)
	}
	return candidate.UnixNano(), true
}

// addMonthsClamped adds whole months to a time, clamping the day to the last day
// of the target month.
//
// The clamping is the point. time.AddDate normalizes instead: the 31st of
// January plus one month becomes the 3rd of March, because Go turns "31
// February" into the day it overflows to. For an allowance boundary that is a
// slow drift - the period would walk later every cycle, and a year of monthly
// plans would each be a few days wrong - so the day is clamped to the month's
// real length here instead.
func addMonthsClamped(t time.Time, months int) time.Time {
	monthIndex := int(t.Month()) - 1 + months
	year := t.Year() + monthIndex/12
	month := time.Month(monthIndex%12 + 1)

	// Day 0 of the following month is the last day of this one, which is how a
	// leap year is accounted for without a table.
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, t.Location()).Day()
	day := t.Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month, day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}

// periodLength turns a period name into a fixed duration.
//
// It reports false for a name it does not know, which is how a typo in a plan
// becomes "no rollover" rather than "unknown length": refusing to roll over is
// recoverable by fixing the plan, while inventing a length is not.
func periodLength(period string) (time.Duration, bool) {
	switch strings.ToLower(strings.TrimSpace(period)) {
	case PeriodDay, "1d":
		return 24 * time.Hour, true
	case PeriodWeek, "7d":
		return 7 * 24 * time.Hour, true
	case "":
		return 0, false
	}

	// A plain "<n>d" form, which is what the plan default uses.
	trimmed := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(period)), "d")
	days := 0
	if trimmed == "" {
		return 0, false
	}
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return 0, false
		}
		days = days*10 + int(r-'0')
	}
	if days <= 0 {
		return 0, false
	}
	return time.Duration(days) * 24 * time.Hour, true
}

// Start checks for rollovers every interval until ctx is cancelled.
func (p *PeriodReset) Start(ctx context.Context) {
	if p == nil || p.Store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(p.interval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := p.RunOnce(0); err != nil {
				p.logf("usage: period check failed: %v", err)
			}
		}
	}
}
