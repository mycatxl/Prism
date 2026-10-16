package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/model"
	"prism/internal/usage"
)

// fakeUsageSeedSource is the persisted subscription view the startup seed reads,
// with a fault switch: the first failures requests fail the way a busy state.db
// does.
type fakeUsageSeedSource struct {
	mu       sync.Mutex
	subs     []model.UserSubscription
	failures int
	reads    int
}

func (s *fakeUsageSeedSource) ListUserSubscriptions() ([]model.UserSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.failures > 0 {
		s.failures--
		return nil, errors.New("state.db is busy")
	}
	return append([]model.UserSubscription(nil), s.subs...), nil
}

func (s *fakeUsageSeedSource) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// seedLogRecorder captures the accounting lines, which are the only evidence an
// operator has that the process started with the wrong allowance.
type seedLogRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (l *seedLogRecorder) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *seedLogRecorder) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func activeSeedSubscription(userID string, bytes int64) model.UserSubscription {
	return model.UserSubscription{
		ID:              "sub-" + userID,
		UserID:          userID,
		PeriodUsedBytes: bytes,
		Status:          model.SubscriptionStatusActive,
	}
}

// A seed whose read fails once is retried, and the totals land when it does.
func TestSeedUsage_RetriesUntilTheReadSucceeds(t *testing.T) {
	counters := usage.NewCounters()
	source := &fakeUsageSeedSource{
		failures: 2,
		subs: []model.UserSubscription{
			activeSeedSubscription("u-1", 4096),
			activeSeedSubscription("u-2", 8192),
		},
	}
	logs := &seedLogRecorder{}

	seeded, err := seedUsage(counters, source, usageSeedRetry{attempts: 4, backoff: time.Millisecond}, logs.logf)
	if err != nil {
		t.Fatalf("seedUsage returned %v, want the third attempt to succeed", err)
	}
	if seeded != 2 {
		t.Fatalf("seeded %d subscriptions, want 2", seeded)
	}
	if got := source.readCount(); got != 3 {
		t.Fatalf("the subscriptions were read %d times, want 3 (two failures then a success)", got)
	}
	if got := counters.UserBytes("u-1"); got != 4096 {
		t.Fatalf("u-1 holds %d bytes, want 4096", got)
	}
	if got := counters.UserBytes("u-2"); got != 8192 {
		t.Fatalf("u-2 holds %d bytes, want 8192", got)
	}
	if line := logs.joined(); !strings.Contains(line, "attempt 1/4") {
		t.Fatalf("the retries were not reported: %q", line)
	}
}

// A seed that never succeeds gives up, reports the error the caller needs to log
// loudly, and leaves the counters at zero - the state the re-seed loop then
// closes.
func TestSeedUsage_GivesUpAfterTheConfiguredAttempts(t *testing.T) {
	counters := usage.NewCounters()
	source := &fakeUsageSeedSource{
		failures: 100,
		subs:     []model.UserSubscription{activeSeedSubscription("u-1", 4096)},
	}
	logs := &seedLogRecorder{}

	seeded, err := seedUsage(counters, source, usageSeedRetry{attempts: 3}, logs.logf)
	if err == nil {
		t.Fatal("seedUsage reported success although every read failed")
	}
	if !strings.Contains(err.Error(), "state.db is busy") {
		t.Fatalf("error = %v, want the read error that explains the divergence", err)
	}
	if seeded != 0 {
		t.Fatalf("seeded %d subscriptions from reads that all failed, want 0", seeded)
	}
	if got := source.readCount(); got != 3 {
		t.Fatalf("the subscriptions were read %d times, want exactly 3", got)
	}
	if got := counters.UserBytes("u-1"); got != 0 {
		t.Fatalf("u-1 holds %d bytes after a failed seed, want 0", got)
	}
	joined := logs.joined()
	if !strings.Contains(joined, "attempt 1/3") || !strings.Contains(joined, "attempt 2/3") {
		t.Fatalf("the retries were not reported: %q", joined)
	}
}

// The re-seed loop restores what the failed startup seed missed, does it once,
// and stops.
func TestReseedUsageUntilApplied_RestoresTheTotalsASeedWouldHaveLoaded(t *testing.T) {
	counters := usage.NewCounters()
	source := &fakeUsageSeedSource{
		subs: []model.UserSubscription{
			activeSeedSubscription("u-1", 4096),
			activeSeedSubscription("u-2", 8192),
			activeSeedSubscription("u-live", 1024),
		},
	}
	// u-live moved traffic between the process starting and the re-seed, so it
	// already has an aggregate of its own - and the flusher has already charged
	// those same bytes to the persisted total. Seeding the persisted total into
	// that aggregate would bill them twice, so this user must be left alone.
	counters.Add(
		usage.CounterKey{KeyID: "k-live", UserID: "u-live", PlatformID: "plat"},
		usage.Delta{IngressBytes: 512, EgressBytes: 512},
	)

	logs := &seedLogRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		reseedUsageUntilApplied(ctx, counters, source, 5*time.Millisecond, logs.logf)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if counters.UserBytes("u-1") == 4096 && counters.UserBytes("u-2") == 8192 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := counters.UserBytes("u-1"); got != 4096 {
		t.Fatalf("u-1 holds %d bytes after the re-seed, want 4096 (%s)", got, logs.joined())
	}
	if got := counters.UserBytes("u-2"); got != 8192 {
		t.Fatalf("u-2 holds %d bytes after the re-seed, want 8192 (%s)", got, logs.joined())
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("the re-seed loop kept running after every missing total was restored: %s", logs.joined())
	}
	if joined := logs.joined(); !strings.Contains(joined, "re-seed complete") {
		t.Fatalf("the loop ended without saying the totals are live again: %q", joined)
	}

	// Seeding again is what a second tick would have done: the totals must be
	// the persisted ones, not twice them.
	if got := counters.UserBytes("u-1"); got != 4096 {
		t.Fatalf("u-1 holds %d bytes, want the persisted 4096 - the seed ran twice (%s)", got, logs.joined())
	}
	if got := counters.UserBytes("u-live"); got != 1024 {
		t.Fatalf("a user with live traffic was seeded anyway: %d bytes, want its own 1024", got)
	}
}

// A failed re-seed read is not fatal: the loop reports it and tries again on the
// next tick.
func TestReseedUsageUntilApplied_SurvivesAFailedRead(t *testing.T) {
	counters := usage.NewCounters()
	source := &fakeUsageSeedSource{
		failures: 2,
		subs:     []model.UserSubscription{activeSeedSubscription("u-1", 777)},
	}
	logs := &seedLogRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		reseedUsageUntilApplied(ctx, counters, source, 5*time.Millisecond, logs.logf)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if counters.UserBytes("u-1") == 777 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := counters.UserBytes("u-1"); got != 777 {
		t.Fatalf("u-1 holds %d bytes, want 777 once the read recovers (%s)", got, logs.joined())
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("the re-seed loop did not finish: %s", logs.joined())
	}
	if joined := logs.joined(); !strings.Contains(joined, "cannot read subscriptions") {
		t.Fatalf("a skipped re-seed was not reported: %q", joined)
	}
}

// The pending filter is what makes the re-seed idempotent, and it must agree
// with the filter SeedFromSubscriptions applies - a subscription the seed would
// skip could otherwise keep the loop running forever.
func TestPendingUsageSeed_SelectsOnlySubscriptionsMissingFromMemory(t *testing.T) {
	nowNs := time.Now().UnixNano()
	counters := usage.NewCounters()
	// An aggregate that exists at zero: a period that has just rolled over. It
	// is not a missing total.
	counters.LoadUser("u-rolled", 0)

	subs := []model.UserSubscription{
		activeSeedSubscription("u-missing", 5000),
		activeSeedSubscription("u-rolled", 5000),
		{ID: "s-off", UserID: "u-off", PeriodUsedBytes: 5000, Status: model.SubscriptionStatusSuspended},
		{ID: "s-exp", UserID: "u-exp", PeriodUsedBytes: 5000, Status: model.SubscriptionStatusActive, ExpiresAtNs: nowNs - int64(time.Hour)},
		{ID: "s-zero", UserID: "u-zero", PeriodUsedBytes: 0, Status: model.SubscriptionStatusActive},
		{ID: "s-ownerless", UserID: "", PeriodUsedBytes: 5000, Status: model.SubscriptionStatusActive},
	}

	pending := pendingUsageSeed(subs, counters, nowNs)
	if len(pending) != 1 || pending[0].UserID != "u-missing" {
		t.Fatalf("pending = %v, want only u-missing", pending)
	}
}

// An expired subscription whose period total is still on disk cannot be seeded:
// the filter has to drop it for the same reason SeedFromSubscriptions does.
func TestPendingUsageSeed_DropsWhatTheSeedItselfWouldSkip(t *testing.T) {
	nowNs := time.Now().UnixNano()
	counters := usage.NewCounters()
	expired := activeSeedSubscription("u-exp", 5000)
	expired.ExpiresAtNs = nowNs - int64(time.Minute)

	// The seed skips it, so the loop must not treat it as pending - otherwise
	// the two would disagree and the loop would never see an empty list.
	if seeded := counters.SeedFromSubscriptions([]model.UserSubscription{expired}, nowNs); seeded != 0 {
		t.Fatalf("SeedFromSubscriptions seeded an expired subscription (%d), so the filter needs revisiting", seeded)
	}
	if pending := pendingUsageSeed([]model.UserSubscription{expired}, counters, nowNs); len(pending) != 0 {
		t.Fatalf("pending = %v, want nothing: an expired subscription can never be seeded", pending)
	}
}

// A nil source or counters, and a retry configured with no attempts, must not
// panic or spin: the startup path calls this before anything else is up.
func TestSeedUsage_HandlesMissingInputs(t *testing.T) {
	if seeded, err := seedUsage(nil, &fakeUsageSeedSource{}, usageSeedRetry{attempts: 2}, nil); seeded != 0 || err != nil {
		t.Fatalf("seedUsage(nil counters) = (%d, %v), want (0, nil)", seeded, err)
	}
	if seeded, err := seedUsage(usage.NewCounters(), nil, usageSeedRetry{attempts: 2}, nil); seeded != 0 || err != nil {
		t.Fatalf("seedUsage(nil source) = (%d, %v), want (0, nil)", seeded, err)
	}

	counters := usage.NewCounters()
	source := &fakeUsageSeedSource{subs: []model.UserSubscription{activeSeedSubscription("u-1", 42)}}
	seeded, err := seedUsage(counters, source, usageSeedRetry{attempts: 0}, nil)
	if err != nil || seeded != 1 {
		t.Fatalf("seedUsage with no configured attempts = (%d, %v), want (1, nil)", seeded, err)
	}
	// A loop with no source returns instead of spinning.
	reseedUsageUntilApplied(context.Background(), counters, nil, time.Millisecond, nil)
}
