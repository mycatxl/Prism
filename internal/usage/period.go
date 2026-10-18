package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"prism/internal/model"
)

// maxPeriodDays bounds a "<n>d" period.
//
// time.Duration holds about 292 years in nanoseconds, so 106752 days - roughly
// 292 years - is the first day count that overflows. A period past that bound is
// refused here rather than wrapped into a negative length.
const maxPeriodDays = 36500

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
	// warned remembers, per subscription id, the period value that was already
	// reported as unrecognized. A pass runs every minute, and a plan whose
	// period is a typo would otherwise log a warning forever.
	//
	// Each pass drops the entries of the subscriptions it no longer lists, so a
	// subscription that was deleted does not stay for the life of the process.
	// It is only touched from RunOnce, which holds mu.
	warned map[string]string
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

	// The warning memory is per subscription, and subscriptions come and go, so
	// a pass that finds one gone drops what it remembered about it.
	listed := make(map[string]struct{}, len(subs))
	for _, sub := range subs {
		listed[sub.ID] = struct{}{}
	}
	defer func() {
		for id := range p.warned {
			if _, ok := listed[id]; !ok {
				delete(p.warned, id)
			}
		}
	}()

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
		if normalizePeriod(period) == PeriodOnce {
			continue // a one-time grant never comes back
		}

		next, rolled := advancePeriod(sub.PeriodStartNs, nowNs, period)
		if !rolled {
			if !periodRecognized(period) {
				// Nothing will ever advance this subscription. That used to be
				// silent: the plan looked healthy, the allowance never came
				// back, and the first sign of trouble was a user who could not
				// connect.
				p.warnUnknownPeriod(sub.ID, period)
			}
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
// The plan snapshot the subscription carries is read first: it is the plan as
// it was sold, it is part of the subscription row itself, and it keeps rolling
// the subscription over on the period it was bought under even after the plan
// has been edited or deleted. Only when the snapshot carries no usable period
// is the live plan consulted, and only when that fails too does the default
// apply - an unknown period must not silently become "30 days", because the
// rollover it drives would then reset usage on a boundary nobody agreed to.
//
// What is returned is canonical, because every decision the rollover makes is a
// comparison against the constants in this file and the period arrives from the
// database as free text.
func (p *PeriodReset) periodFor(sub model.UserSubscription) string {
	if period := periodFromSnapshot(sub.PlanSnapshotJSON); period != "" {
		return normalizePeriod(period)
	}
	if sub.PlanID == "" {
		return DefaultPeriod
	}
	plan, err := p.Store.GetPlan(sub.PlanID)
	if err != nil || plan == nil || plan.Period == "" {
		return DefaultPeriod
	}
	return normalizePeriod(plan.Period)
}

// normalizePeriod is the canonical form of a period name. Plan periods are free
// text in the database - the management API trims whitespace and stores whatever
// else it is given - so "Month" and " month " reach this package as often as
// "month" does. Comparing the raw value against the constants meant those
// variants matched neither the calendar-month branch nor the length parser:
// advancePeriod answered "no rollover" and the subscription kept the allowance
// it had spent, permanently and without a word in the log.
func normalizePeriod(period string) string {
	return strings.ToLower(strings.TrimSpace(period))
}

// periodRecognized reports whether a rollover can be computed for a period.
//
// It answers for exactly the values advancePeriod acts on, and exists so the
// values it does not act on are reported instead of being indistinguishable
// from a period that simply has not elapsed.
func periodRecognized(period string) bool {
	if normalizePeriod(period) == PeriodMonth {
		return true
	}
	length, ok := periodLength(period)
	return ok && length > 0
}

// warnUnknownPeriod reports a period the rollover cannot act on.
//
// A plan with a typo in its period is invisible otherwise: the subscription is
// listed, the quota is enforced, and the allowance never comes back. The warning
// is emitted once per value rather than once per pass - the check runs every
// minute, and filling the log is not more observable.
func (p *PeriodReset) warnUnknownPeriod(subID, period string) {
	if p.warned == nil {
		p.warned = make(map[string]string)
	}
	if last, ok := p.warned[subID]; ok && last == period {
		return
	}
	p.warned[subID] = period
	p.logf("usage: subscription %s has an unrecognized period %q, so its allowance will never roll over; use %q, %q, %q or a duration such as %q",
		subID, period, PeriodDay, PeriodWeek, PeriodMonth, DefaultPeriod)
}

// periodFromSnapshot reads the billing period out of the plan snapshot the
// subscription was opened with. An unreadable or period-less snapshot answers
// an empty string, which leaves the caller to try the live plan.
func periodFromSnapshot(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var plan model.Plan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return ""
	}
	return strings.TrimSpace(plan.Period)
}

// advancePeriod moves currentStartNs forward in whole periods until the window
// containing nowNs is found, and reports whether it moved at all.
func advancePeriod(currentStartNs, nowNs int64, period string) (int64, bool) {
	if currentStartNs > nowNs {
		// The period has not begun yet; there is nothing to roll over.
		return currentStartNs, false
	}

	if normalizePeriod(period) == PeriodMonth {
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
	normalized := normalizePeriod(period)
	switch normalized {
	case PeriodDay, "1d":
		return 24 * time.Hour, true
	case PeriodWeek, "7d":
		return 7 * 24 * time.Hour, true
	case "":
		return 0, false
	}

	// A plain "<n>d" form, which is what the plan default uses.
	trimmed := strings.TrimSuffix(normalized, "d")
	if trimmed == "" {
		return 0, false
	}
	days := 0
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return 0, false
		}
		days = days*10 + int(r-'0')
		// The digit string is free text from the plans table, so it can name a
		// window longer than a duration can hold. Checking as the number is
		// built keeps the multiplication below inside range instead of letting
		// it wrap to a negative length, which advancePeriod would then read as
		// "this period never elapses".
		if days > maxPeriodDays {
			return 0, false
		}
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
