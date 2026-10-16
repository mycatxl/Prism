package usage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/model"
)

// fakePeriodStore holds subscriptions and plans for the rollover tests.
type fakePeriodStore struct {
	mu sync.Mutex

	subs      []model.UserSubscription
	plans     map[string]*model.Plan
	listErr   error
	resetErr  error
	resetCall []periodResetCall
	// sawReqs records the (from, to) pair of every reset, so a test can assert
	// the guard is passed correctly.
	planMisses int
}

type periodResetCall struct {
	id     string
	fromNs int64
	toNs   int64
}

func newFakePeriodStore() *fakePeriodStore {
	return &fakePeriodStore{plans: make(map[string]*model.Plan)}
}

func (s *fakePeriodStore) ListUserSubscriptions() ([]model.UserSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]model.UserSubscription, len(s.subs))
	copy(out, s.subs)
	return out, nil
}

func (s *fakePeriodStore) GetPlan(id string) (*model.Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, ok := s.plans[id]
	if !ok {
		s.planMisses++
		return nil, errors.New("plan not found")
	}
	return plan, nil
}

func (s *fakePeriodStore) ResetSubscriptionPeriod(id string, fromNs, toNs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resetErr != nil {
		return s.resetErr
	}
	s.resetCall = append(s.resetCall, periodResetCall{id: id, fromNs: fromNs, toNs: toNs})
	return nil
}

func (s *fakePeriodStore) resets() []periodResetCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]periodResetCall, len(s.resetCall))
	copy(out, s.resetCall)
	return out
}

func activePeriodSub(id, userID, planID string, periodStartNs int64) model.UserSubscription {
	return model.UserSubscription{
		ID:            id,
		UserID:        userID,
		PlanID:        planID,
		PeriodStartNs: periodStartNs,
		Status:        model.SubscriptionStatusActive,
	}
}

// A subscription whose period has elapsed rolls over: the usage resets and the
// period start moves.
func TestPeriodReset_RollsOverAnElapsedPeriod(t *testing.T) {
	store := newFakePeriodStore()
	store.plans["p-1"] = &model.Plan{ID: "p-1", Period: "30d"}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 0, 31) // one whole period later
	store.subs = []model.UserSubscription{activePeriodSub("s-1", "u-1", "p-1", start.UnixNano())}

	counters := NewCounters()
	counters.LoadUser("u-1", 5000)

	reset := &PeriodReset{Store: store, Counters: counters}
	result, err := reset.RunOnce(now.UnixNano())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Rolled != 1 {
		t.Fatalf("rolled %d subscriptions, want 1", result.Rolled)
	}

	// The period advanced by whole periods, not by "now".
	got := store.resets()
	if len(got) != 1 {
		t.Fatalf("%d resets, want 1", len(got))
	}
	wantNext := start.AddDate(0, 0, 30).UnixNano()
	if got[0].toNs != wantNext {
		t.Fatalf("new period start = %d, want %d (the boundary, not the current time)", got[0].toNs, wantNext)
	}
	if got[0].fromNs != start.UnixNano() {
		t.Fatalf("the guard used period start %d, want %d", got[0].fromNs, start.UnixNano())
	}

	// The running total the quota reads is cleared as well: one without the
	// other leaves a user whose new period began still being refused.
	if bytes := counters.UserBytes("u-1"); bytes != 0 {
		t.Fatalf("the running total is %d after the rollover, want 0", bytes)
	}
}

// A period that has not elapsed must be left alone, or the allowance would come
// back on every pass.
func TestPeriodReset_LeavesAPeriodThatHasNotElapsed(t *testing.T) {
	store := newFakePeriodStore()
	store.plans["p-1"] = &model.Plan{ID: "p-1", Period: "30d"}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 0, 10) // ten days in
	store.subs = []model.UserSubscription{activePeriodSub("s-1", "u-1", "p-1", start.UnixNano())}

	counters := NewCounters()
	counters.LoadUser("u-1", 5000)

	result, err := (&PeriodReset{Store: store, Counters: counters}).RunOnce(now.UnixNano())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Rolled != 0 {
		t.Fatalf("rolled %d subscriptions, want 0", result.Rolled)
	}
	if len(store.resets()) != 0 {
		t.Fatal("a period that had not elapsed was reset")
	}
	if bytes := counters.UserBytes("u-1"); bytes != 5000 {
		t.Fatalf("the running total is %d, want it untouched at 5000", bytes)
	}
}

// Several missed periods advance in one step rather than one per pass, so a
// service that was down for months does not need a pass per month.
func TestPeriodReset_SkipsSeveralMissedPeriodsAtOnce(t *testing.T) {
	store := newFakePeriodStore()
	store.plans["p-1"] = &model.Plan{ID: "p-1", Period: "week"}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 0, 30) // more than four weeks later
	store.subs = []model.UserSubscription{activePeriodSub("s-1", "u-1", "p-1", start.UnixNano())}

	result, err := (&PeriodReset{Store: store}).RunOnce(now.UnixNano())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Rolled != 1 {
		t.Fatalf("rolled %d subscriptions, want 1", result.Rolled)
	}

	got := store.resets()
	// 28 days is the last whole week boundary at or before day 30.
	wantNext := start.AddDate(0, 0, 28).UnixNano()
	if got[0].toNs != wantNext {
		t.Fatalf("new period start = %d, want %d", got[0].toNs, wantNext)
	}
	if got[0].toNs > now.UnixNano() {
		t.Fatal("the new period starts in the future")
	}
}

// A calendar month is not thirty days, and a subscription that started on the
// 31st must keep its own boundary rather than drifting.
func TestPeriodReset_AdvancesByCalendarMonth(t *testing.T) {
	store := newFakePeriodStore()
	store.plans["p-1"] = &model.Plan{ID: "p-1", Period: "month"}

	start := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) // past the February boundary
	store.subs = []model.UserSubscription{activePeriodSub("s-1", "u-1", "p-1", start.UnixNano())}

	result, err := (&PeriodReset{Store: store}).RunOnce(now.UnixNano())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Rolled != 1 {
		t.Fatalf("rolled %d subscriptions, want 1", result.Rolled)
	}
	got := store.resets()
	if len(got) != 1 {
		t.Fatalf("%d resets, want 1", len(got))
	}
	newStart := time.Unix(0, got[0].toNs).UTC()
	if newStart.Month() != time.February || newStart.Day() != 28 {
		t.Fatalf("the new period starts %s, want the February boundary", newStart)
	}
	if !newStart.Before(now) && !newStart.Equal(now) {
		t.Fatalf("the new period starts %s, which is after now %s", newStart, now)
	}
}

// A one-time grant never rolls over: that is the difference between a plan with
// a period and a plan that is a single allowance.
func TestPeriodReset_DoesNotRollOverAOneTimePlan(t *testing.T) {
	store := newFakePeriodStore()
	store.plans["p-1"] = &model.Plan{ID: "p-1", Period: PeriodOnce}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 6, 0)
	store.subs = []model.UserSubscription{activePeriodSub("s-1", "u-1", "p-1", start.UnixNano())}

	counters := NewCounters()
	counters.LoadUser("u-1", 5000)

	result, err := (&PeriodReset{Store: store, Counters: counters}).RunOnce(now.UnixNano())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Rolled != 0 {
		t.Fatalf("a one-time plan rolled over %d times, want 0", result.Rolled)
	}
	if bytes := counters.UserBytes("u-1"); bytes != 5000 {
		t.Fatalf("a one-time plan's usage was reset to %d", bytes)
	}
}

// Only active subscriptions roll over: a suspended one must not quietly start a
// fresh allowance, and an expired one is the record of what it was granted.
func TestPeriodReset_SkipsSubscriptionsThatMayNotRollOver(t *testing.T) {
	store := newFakePeriodStore()
	store.plans["p-1"] = &model.Plan{ID: "p-1", Period: "day"}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 0, 5)

	suspended := activePeriodSub("s-susp", "u-susp", "p-1", start.UnixNano())
	suspended.Status = model.SubscriptionStatusSuspended
	expired := activePeriodSub("s-exp", "u-exp", "p-1", start.UnixNano())
	expired.Status = model.SubscriptionStatusExpired
	noStart := activePeriodSub("s-nostart", "u-nostart", "p-1", 0)

	store.subs = []model.UserSubscription{suspended, expired, noStart}

	result, err := (&PeriodReset{Store: store}).RunOnce(now.UnixNano())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Rolled != 0 {
		t.Fatalf("rolled %d subscriptions, want 0", result.Rolled)
	}
	if len(store.resets()) != 0 {
		t.Fatal("a subscription that may not roll over was reset")
	}
	if result.Checked != 1 {
		t.Fatalf("checked %d subscriptions, want 1 (only the active one is considered)", result.Checked)
	}
}

// A plan that has been deleted falls back to the default period rather than
// leaving the subscription frozen forever.
func TestPeriodReset_FallsBackToTheDefaultPeriodWhenThePlanIsGone(t *testing.T) {
	store := newFakePeriodStore()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 0, 40) // past the 30-day default
	store.subs = []model.UserSubscription{activePeriodSub("s-1", "u-1", "p-gone", start.UnixNano())}

	result, err := (&PeriodReset{Store: store}).RunOnce(now.UnixNano())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Rolled != 1 {
		t.Fatalf("rolled %d subscriptions, want 1", result.Rolled)
	}
	if store.planMisses == 0 {
		t.Fatal("the plan was never looked up, so the fallback was not exercised")
	}
	got := store.resets()
	wantNext := start.AddDate(0, 0, 30).UnixNano()
	if got[0].toNs != wantNext {
		t.Fatalf("new period start = %d, want %d", got[0].toNs, wantNext)
	}
}

// A subscription with no plan id at all is the same case as a deleted plan.
func TestPeriodReset_SubscriptionWithoutAPlanUsesTheDefault(t *testing.T) {
	store := newFakePeriodStore()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 0, 31)
	store.subs = []model.UserSubscription{activePeriodSub("s-1", "u-1", "", start.UnixNano())}

	result, err := (&PeriodReset{Store: store}).RunOnce(now.UnixNano())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Rolled != 1 {
		t.Fatalf("rolled %d subscriptions, want 1", result.Rolled)
	}
}

// A failed database write must not clear the running total: that would hand out
// an allowance that is still spent on disk.
func TestPeriodReset_DoesNotClearTheTotalWhenTheWriteFails(t *testing.T) {
	store := newFakePeriodStore()
	store.plans["p-1"] = &model.Plan{ID: "p-1", Period: "day"}
	store.resetErr = errors.New("database is busy")

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 0, 5)
	store.subs = []model.UserSubscription{activePeriodSub("s-1", "u-1", "p-1", start.UnixNano())}

	counters := NewCounters()
	counters.LoadUser("u-1", 5000)

	result, err := (&PeriodReset{Store: store, Counters: counters}).RunOnce(now.UnixNano())
	if err != nil {
		t.Fatalf("a failed write failed the whole pass: %v", err)
	}
	if result.Rolled != 0 {
		t.Fatalf("reported %d rollovers for a failed write, want 0", result.Rolled)
	}
	if bytes := counters.UserBytes("u-1"); bytes != 5000 {
		t.Fatalf("the running total is %d after a failed write, want it untouched at 5000", bytes)
	}
}

// A list failure is reported so the caller can log it, and leaves everything
// untouched.
func TestPeriodReset_ListFailureIsReported(t *testing.T) {
	store := newFakePeriodStore()
	store.listErr = errors.New("database is closed")

	if _, err := (&PeriodReset{Store: store}).RunOnce(time.Now().UnixNano()); err == nil {
		t.Fatal("a failed listing was reported as success")
	}
}

// A future period start is not rolled over backwards.
func TestPeriodReset_LeavesAFuturePeriodAlone(t *testing.T) {
	store := newFakePeriodStore()
	store.plans["p-1"] = &model.Plan{ID: "p-1", Period: "day"}

	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, -1, 0) // now is before the period started
	store.subs = []model.UserSubscription{activePeriodSub("s-1", "u-1", "p-1", start.UnixNano())}

	result, err := (&PeriodReset{Store: store}).RunOnce(now.UnixNano())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Rolled != 0 {
		t.Fatalf("rolled %d subscriptions, want 0", result.Rolled)
	}
}

// An unparseable period is treated as "no rollover" rather than as some default
// length: refusing to act is recoverable, inventing a window is not.
func TestPeriodLength_UnknownNamesAreRejected(t *testing.T) {
	cases := []struct {
		period string
		want   time.Duration
		ok     bool
	}{
		{"day", 24 * time.Hour, true},
		{"1d", 24 * time.Hour, true},
		{"week", 7 * 24 * time.Hour, true},
		{"7d", 7 * 24 * time.Hour, true},
		{"30d", 30 * 24 * time.Hour, true},
		{"90d", 90 * 24 * time.Hour, true},
		{" 30d ", 30 * 24 * time.Hour, true},
		{"MONTHLY", 0, false}, // not a name we define, and not the Nd form
		{"month", 0, false},   // calendar, handled before this function
		{"", 0, false},
		{"0d", 0, false},
		{"-3d", 0, false},
		{"d", 0, false},
	}

	for _, tc := range cases {
		got, ok := periodLength(tc.period)
		if ok != tc.ok || got != tc.want {
			t.Errorf("periodLength(%q) = (%v, %v), want (%v, %v)", tc.period, got, ok, tc.want, tc.ok)
		}
	}
}

// Missing dependencies are inert rather than a panic: a deployment that never
// started the accounting must not crash a background loop.
func TestPeriodReset_MissingDependenciesAreInert(t *testing.T) {
	var nilReset *PeriodReset
	if got, err := nilReset.RunOnce(0); err != nil || got.Rolled != 0 {
		t.Fatalf("a nil resetter reported %+v, %v", got, err)
	}

	noStore := &PeriodReset{Counters: NewCounters()}
	if got, err := noStore.RunOnce(0); err != nil || got.Rolled != 0 {
		t.Fatalf("a resetter without a store reported %+v, %v", got, err)
	}

	// A counter set is optional: the persisted counter is still reset.
	store := newFakePeriodStore()
	store.plans["p-1"] = &model.Plan{ID: "p-1", Period: "day"}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.subs = []model.UserSubscription{activePeriodSub("s-1", "u-1", "p-1", start.UnixNano())}
	noCounters := &PeriodReset{Store: store}
	if got, err := noCounters.RunOnce(start.AddDate(0, 0, 2).UnixNano()); err != nil || got.Rolled != 1 {
		t.Fatalf("a resetter without counters reported %+v, %v", got, err)
	}
}

// Start must check on its interval and stop promptly when cancelled.
func TestPeriodReset_StartChecksUntilCancelled(t *testing.T) {
	store := newFakePeriodStore()
	store.plans["p-1"] = &model.Plan{ID: "p-1", Period: "day"}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.subs = []model.UserSubscription{
		activePeriodSub("s-1", "u-1", "p-1", start.AddDate(0, 0, -5).UnixNano()),
	}

	reset := &PeriodReset{Store: store, Interval: 10 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		reset.Start(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(store.resets()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after its context was cancelled")
	}
	if len(store.resets()) == 0 {
		t.Fatal("Start never rolled a period over")
	}
}

// Two passes must not both act on the same period.
func TestPeriodReset_ConcurrentPassesDoNotDoubleReset(t *testing.T) {
	store := newFakePeriodStore()
	store.plans["p-1"] = &model.Plan{ID: "p-1", Period: "day"}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 0, 3)
	store.subs = []model.UserSubscription{activePeriodSub("s-1", "u-1", "p-1", start.UnixNano())}

	reset := &PeriodReset{Store: store}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := reset.RunOnce(now.UnixNano()); err != nil {
				t.Errorf("RunOnce: %v", err)
			}
		}()
	}
	wg.Wait()

	// The pass is serialized, and the store's own guard is what a real
	// repository uses to collapse a race. What matters here is that the calls
	// are internally consistent: every one names the same period it observed.
	for _, call := range store.resets() {
		if call.fromNs != start.UnixNano() {
			t.Fatalf("a reset used period start %d, want %d", call.fromNs, start.UnixNano())
		}
	}
}

// A period name that arrives in another case or with padding around it is the
// same period. The management API trims whitespace, fills in a default for an
// empty value and validates nothing else, so "Month", "MONTH" and " month " are
// what a plan row actually holds. The rollover used to compare the raw value
// against the constants while the length parser normalized it, so those variants
// matched neither branch: advancePeriod answered "no rollover" and the
// subscription kept the allowance it had spent, permanently and unlogged.
//
// The table asserts the effect rather than the comparison: every variant of a
// period has to advance exactly like the canonical spelling, and a value that is
// genuinely not a period has to be reported.
func TestPeriodReset_PeriodNamesAreNormalized(t *testing.T) {
	monthStart := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)
	monthNow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	// The boundary clamps to February's real length: the calendar branch, not
	// thirty days.
	monthNext := time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC)

	dayStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	dayNow := dayStart.AddDate(0, 0, 31)

	cases := []struct {
		name       string
		period     string
		start      time.Time
		now        time.Time
		wantRolled bool
		wantNext   time.Time
		wantWarned bool
	}{
		{name: "month", period: "month", start: monthStart, now: monthNow, wantRolled: true, wantNext: monthNext},
		{name: "Month", period: "Month", start: monthStart, now: monthNow, wantRolled: true, wantNext: monthNext},
		{name: "MONTH", period: "MONTH", start: monthStart, now: monthNow, wantRolled: true, wantNext: monthNext},
		{name: "padded month", period: " month ", start: monthStart, now: monthNow, wantRolled: true, wantNext: monthNext},
		{name: "30d", period: "30d", start: dayStart, now: dayNow, wantRolled: true, wantNext: dayStart.AddDate(0, 0, 30)},
		{name: "30D padded", period: " 30D ", start: dayStart, now: dayNow, wantRolled: true, wantNext: dayStart.AddDate(0, 0, 30)},
		{name: "7d", period: "7d", start: dayStart, now: dayNow, wantRolled: true, wantNext: dayStart.AddDate(0, 0, 28)},
		{name: "week", period: "week", start: dayStart, now: dayNow, wantRolled: true, wantNext: dayStart.AddDate(0, 0, 28)},
		{name: "Week padded", period: " Week ", start: dayStart, now: dayNow, wantRolled: true, wantNext: dayStart.AddDate(0, 0, 28)},
		{name: "day", period: "day", start: dayStart, now: dayNow, wantRolled: true, wantNext: dayNow},
		{name: "DAY", period: "DAY", start: dayStart, now: dayNow, wantRolled: true, wantNext: dayNow},
		{name: "empty falls back to the default", period: "", start: dayStart, now: dayNow, wantRolled: true, wantNext: dayStart.AddDate(0, 0, 30)},
		{name: "once", period: "once", start: dayStart, now: dayNow},
		{name: "Once", period: "Once", start: dayStart, now: dayNow},
		{name: "ONCE padded", period: " ONCE ", start: dayStart, now: dayNow},
		{name: "unknown period is reported", period: "monthly", start: dayStart, now: dayNow, wantWarned: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakePeriodStore()
			store.plans["p-1"] = &model.Plan{ID: "p-1", Period: tc.period}
			store.subs = []model.UserSubscription{
				activePeriodSub("s-1", "u-1", "p-1", tc.start.UnixNano()),
			}

			var logs []string
			reset := &PeriodReset{
				Store: store,
				Logf: func(format string, args ...any) {
					logs = append(logs, fmt.Sprintf(format, args...))
				},
			}

			result, err := reset.RunOnce(tc.now.UnixNano())
			if err != nil {
				t.Fatalf("RunOnce: %v", err)
			}
			if result.Checked != 1 {
				t.Fatalf("checked %d subscriptions, want 1", result.Checked)
			}

			resets := store.resets()
			if !tc.wantRolled {
				if result.Rolled != 0 || len(resets) != 0 {
					t.Fatalf("period %q rolled over: %+v, resets %+v", tc.period, result, resets)
				}
			} else {
				if result.Rolled != 1 || len(resets) != 1 {
					t.Fatalf("period %q rolled %d times (%d resets), want 1: the variant did not behave like its canonical spelling", tc.period, result.Rolled, len(resets))
				}
				if got := time.Unix(0, resets[0].toNs).UTC(); !got.Equal(tc.wantNext) {
					t.Fatalf("the new period starts %s, want %s", got, tc.wantNext)
				}
			}

			// The rollover logs its success through the same hook, so a warning
			// is counted by what it says, not by the hook being used.
			warnings := 0
			for _, line := range logs {
				if strings.Contains(line, "unrecognized period") {
					warnings++
				}
			}
			if tc.wantWarned {
				if warnings != 1 {
					t.Fatalf("logged %v for period %q, want one warning", logs, tc.period)
				}
				if !strings.Contains(logs[0], tc.period) {
					t.Fatalf("the warning does not name the period: %q", logs[0])
				}
			} else if warnings != 0 {
				t.Fatalf("period %q was reported as unrecognized: %v", tc.period, logs)
			}
			if !tc.wantWarned {
				return
			}
			// A permanently unparseable plan must not fill the log: the pass
			// runs every minute.
			if _, err := reset.RunOnce(tc.now.UnixNano()); err != nil {
				t.Fatalf("RunOnce: %v", err)
			}
			if len(logs) != warnings {
				t.Fatalf("the warning was emitted %d times, want once: %v", len(logs), logs)
			}
		})
	}
}
