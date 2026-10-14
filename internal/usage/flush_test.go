package usage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"prism/internal/model"
)

// fakeFlushStore records what the flusher persisted and can be made to fail.
type fakeFlushStore struct {
	mu sync.Mutex

	rows     []model.UsageRow
	rowCalls int
	rowErr   error

	subs        map[string]*model.UserSubscription
	subCalls    int
	subErr      error
	subAdds     map[string]int64
	subAddCalls int
	addErr      error
}

func newFakeFlushStore() *fakeFlushStore {
	return &fakeFlushStore{
		subs:    make(map[string]*model.UserSubscription),
		subAdds: make(map[string]int64),
	}
}

func (s *fakeFlushStore) UpsertUsageHourly(rows []model.UsageRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rowCalls++
	if s.rowErr != nil {
		return s.rowErr
	}
	s.rows = append(s.rows, rows...)
	return nil
}

func (s *fakeFlushStore) GetActiveUserSubscription(userID string) (*model.UserSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subCalls++
	if s.subErr != nil {
		return nil, s.subErr
	}
	return s.subs[userID], nil
}

func (s *fakeFlushStore) AddSubscriptionUsedBytes(id string, delta int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subAddCalls++
	if s.addErr != nil {
		return s.addErr
	}
	s.subAdds[id] += delta
	return nil
}

func (s *fakeFlushStore) totalBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	for _, row := range s.rows {
		total += row.IngressBytes + row.EgressBytes
	}
	return total
}

// A flush writes one row per bucket, bucketed into the hour it ran in.
func TestFlusher_WritesOneRowPerBucketInTheCurrentHour(t *testing.T) {
	counters := NewCounters()
	store := newFakeFlushStore()
	flusher := &Flusher{Counters: counters, Store: store}

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"},
		Delta{IngressBytes: 100, EgressBytes: 40, Requests: 2})
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-2"},
		Delta{IngressBytes: 10, EgressBytes: 5, Requests: 1, Errors: 1})

	// A fixed instant inside a known hour.
	hourStart := int64(time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC).UnixNano())
	nowNs := hourStart + int64(7*time.Minute)

	result, err := flusher.FlushOnce(nowNs)
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if result.Rows != 2 {
		t.Fatalf("wrote %d rows, want 2", result.Rows)
	}
	if got := result.Bytes; got != 155 {
		t.Fatalf("wrote %d bytes, want 155", got)
	}

	// Both rows carry the same hour bucket, not the flush instant.
	for _, row := range store.rows {
		if row.TSNs != hourStart {
			t.Fatalf("row %s bucket = %d, want the hour start %d", row.KeyID, row.TSNs, hourStart)
		}
	}
}

// A drained bucket must be gone, so the next pass does not write it again.
func TestFlusher_SecondPassWritesNothing(t *testing.T) {
	counters := NewCounters()
	store := newFakeFlushStore()
	flusher := &Flusher{Counters: counters, Store: store}

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 100})

	if _, err := flusher.FlushOnce(1); err != nil {
		t.Fatalf("first flush: %v", err)
	}
	result, err := flusher.FlushOnce(2)
	if err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if result.Rows != 0 {
		t.Fatalf("the second pass wrote %d rows, want 0", result.Rows)
	}
	if got := store.totalBytes(); got != 100 {
		t.Fatalf("the store holds %d bytes after two passes, want 100 (no double count)", got)
	}
}

// A failed write must put the drained bytes back, because losing them would
// silently under-bill traffic that was actually moved.
func TestFlusher_RequeuesAfterAFailedWrite(t *testing.T) {
	counters := NewCounters()
	store := newFakeFlushStore()
	store.rowErr = errors.New("disk full")
	flusher := &Flusher{Counters: counters, Store: store}

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"},
		Delta{IngressBytes: 100, EgressBytes: 50})

	if _, err := flusher.FlushOnce(1); err == nil {
		t.Fatal("a failed write was reported as success")
	}

	// The bytes are back in the counters, ready for the next pass.
	if got := counters.KeyBytes(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}).Total(); got != 150 {
		t.Fatalf("counters hold %d bytes after a failed flush, want 150", got)
	}

	// And the retry succeeds with the full amount.
	store.rowErr = nil
	result, err := flusher.FlushOnce(2)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := result.Bytes; got != 150 {
		t.Fatalf("the retry wrote %d bytes, want 150", got)
	}
}

// The subscription's period usage is what a traffic quota is checked against,
// so the same bytes must reach it.
func TestFlusher_AddsToTheSubscriptionPeriodUsage(t *testing.T) {
	counters := NewCounters()
	store := newFakeFlushStore()
	store.subs["u-1"] = &model.UserSubscription{ID: "s-1", UserID: "u-1"}
	flusher := &Flusher{Counters: counters, Store: store}

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"},
		Delta{IngressBytes: 300, EgressBytes: 200})

	result, err := flusher.FlushOnce(1)
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if result.Subscriptions != 1 {
		t.Fatalf("updated %d subscriptions, want 1", result.Subscriptions)
	}
	if got := store.subAdds["s-1"]; got != 500 {
		t.Fatalf("subscription got %d bytes, want 500", got)
	}
}

// A user holding several keys has one allowance: the keys' traffic is summed
// into the one subscription rather than applied once per key.
func TestFlusher_SumsEveryKeyOfAUserIntoOneSubscription(t *testing.T) {
	counters := NewCounters()
	store := newFakeFlushStore()
	store.subs["u-1"] = &model.UserSubscription{ID: "s-1", UserID: "u-1"}
	flusher := &Flusher{Counters: counters, Store: store}

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 100})
	counters.Add(CounterKey{KeyID: "k-2", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 50})
	counters.Add(CounterKey{KeyID: "k-3", UserID: "u-1", PlatformID: "p-2"}, Delta{EgressBytes: 25})

	if _, err := flusher.FlushOnce(1); err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if got := store.subAdds["s-1"]; got != 175 {
		t.Fatalf("subscription got %d bytes, want 175 (the sum of the user's keys)", got)
	}
	if got := store.subAddCalls; got != 1 {
		t.Fatalf("the subscription was updated %d times, want 1", got)
	}
}

// Traffic from a user with no subscription is still recorded - the operator can
// see it - but it counts against nothing because there is no allowance.
func TestFlusher_RecordsTrafficWithNoSubscription(t *testing.T) {
	counters := NewCounters()
	store := newFakeFlushStore()
	flusher := &Flusher{Counters: counters, Store: store}

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-no-plan", PlatformID: "p-1"}, Delta{IngressBytes: 77})

	result, err := flusher.FlushOnce(1)
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if result.Rows != 1 {
		t.Fatalf("wrote %d rows, want 1", result.Rows)
	}
	if result.UnclaimedBytes != 77 {
		t.Fatalf("reported %d unclaimed bytes, want 77", result.UnclaimedBytes)
	}
	if got := store.totalBytes(); got != 77 {
		t.Fatalf("the store holds %d bytes, want 77", got)
	}
}

// A failing subscription update must not fail the whole pass: the hourly rows
// are already written, and the next pass adds to the period usage again.
func TestFlusher_SubscriptionFailureDoesNotFailThePass(t *testing.T) {
	counters := NewCounters()
	store := newFakeFlushStore()
	store.subs["u-1"] = &model.UserSubscription{ID: "s-1", UserID: "u-1"}
	store.addErr = errors.New("locked")
	flusher := &Flusher{Counters: counters, Store: store}

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 100})

	result, err := flusher.FlushOnce(1)
	if err != nil {
		t.Fatalf("a subscription failure failed the whole pass: %v", err)
	}
	if result.Rows != 1 {
		t.Fatalf("wrote %d rows, want 1", result.Rows)
	}
	if result.UnclaimedBytes != 100 {
		t.Fatalf("reported %d unclaimed bytes, want 100", result.UnclaimedBytes)
	}
	// The bytes were not put back: the hourly row is the record, and requeueing
	// would double-count them against the allowance on the next pass.
	if got := counters.Size(); got != 0 {
		t.Fatalf("counters still hold %d buckets, want 0", got)
	}
}

// A flusher with nothing to write must not touch the store at all.
func TestFlusher_EmptyCountersWriteNothing(t *testing.T) {
	store := newFakeFlushStore()
	flusher := &Flusher{Counters: NewCounters(), Store: store}

	result, err := flusher.FlushOnce(1)
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if result.Rows != 0 {
		t.Fatalf("wrote %d rows, want 0", result.Rows)
	}
	if store.rowCalls != 0 {
		t.Fatalf("the store was called %d times with nothing to write", store.rowCalls)
	}
}

// A flusher without a store, or without counters, is inert rather than a panic:
// a deployment that does not persist usage has to keep working.
func TestFlusher_MissingDependenciesAreInert(t *testing.T) {
	var nilFlusher *Flusher
	if _, err := nilFlusher.FlushOnce(1); err != nil {
		t.Fatalf("a nil flusher returned an error: %v", err)
	}

	noStore := &Flusher{Counters: NewCounters()}
	noStore.Counters.Add(CounterKey{KeyID: "k"}, Delta{IngressBytes: 5})
	if _, err := noStore.FlushOnce(1); err != nil {
		t.Fatalf("a flusher without a store returned an error: %v", err)
	}
	if got := noStore.Counters.Size(); got != 1 {
		t.Fatalf("a flusher without a store drained %d buckets, want to leave them", 1-got)
	}

	noCounters := &Flusher{Store: newFakeFlushStore()}
	if _, err := noCounters.FlushOnce(1); err != nil {
		t.Fatalf("a flusher without counters returned an error: %v", err)
	}
}

// Start must write a final time when its context is cancelled, so a clean
// shutdown does not lose the traffic since the last tick.
func TestFlusher_StartFlushesOnShutdown(t *testing.T) {
	counters := NewCounters()
	store := newFakeFlushStore()
	flusher := &Flusher{
		Counters: counters,
		Store:    store,
		Interval: time.Hour, // never ticks during the test
	}
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 42})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		flusher.Start(ctx)
	}()

	// Let Start reach its select, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after its context was cancelled")
	}

	if got := store.totalBytes(); got != 42 {
		t.Fatalf("the shutdown flush wrote %d bytes, want 42", got)
	}
}

// Restoring the aggregates after a restart is what keeps an allowance from
// resetting every time the service bounces.
func TestCounters_SeedFromSubscriptions(t *testing.T) {
	counters := NewCounters()
	nowNs := time.Now().UnixNano()

	subs := []model.UserSubscription{
		{UserID: "u-1", PeriodUsedBytes: 500, Status: model.SubscriptionStatusActive},
		{UserID: "u-2", PeriodUsedBytes: 250, Status: model.SubscriptionStatusActive},
		// Not active: its usage does not belong in the running total.
		{UserID: "u-3", PeriodUsedBytes: 999, Status: model.SubscriptionStatusExpired},
		// Already expired by time, even though the row still says active.
		{UserID: "u-4", PeriodUsedBytes: 999, Status: model.SubscriptionStatusActive, ExpiresAtNs: nowNs - 1},
		// Nothing used: nothing to seed.
		{UserID: "u-5", Status: model.SubscriptionStatusActive},
	}

	seeded := counters.SeedFromSubscriptions(subs, nowNs)
	if seeded != 2 {
		t.Fatalf("seeded %d subscriptions, want 2", seeded)
	}
	if got := counters.UserBytes("u-1"); got != 500 {
		t.Fatalf("u-1 holds %d bytes, want 500", got)
	}
	if got := counters.UserBytes("u-2"); got != 250 {
		t.Fatalf("u-2 holds %d bytes, want 250", got)
	}
	if got := counters.UserBytes("u-3"); got != 0 {
		t.Fatalf("an expired subscription seeded %d bytes, want 0", got)
	}
	if got := counters.UserBytes("u-4"); got != 0 {
		t.Fatalf("a subscription past its expiry seeded %d bytes, want 0", got)
	}
	if got := counters.UserBytes("u-5"); got != 0 {
		t.Fatalf("a subscription with no usage seeded %d bytes, want 0", got)
	}

	// Seeding is additive, so a second call does not wipe what the data plane
	// has already counted.
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 100})
	counters.SeedFromSubscriptions(subs, nowNs)
	if got := counters.UserBytes("u-1"); got != 1100 {
		t.Fatalf("u-1 holds %d bytes after reseeding, want 1100", got)
	}
}

// A nil counter set must not panic on seeding.
func TestCounters_SeedFromSubscriptionsNilIsSafe(t *testing.T) {
	var counters *Counters
	if got := counters.SeedFromSubscriptions([]model.UserSubscription{{UserID: "u", PeriodUsedBytes: 1}}, 0); got != 0 {
		t.Fatalf("a nil counter set seeded %d subscriptions", got)
	}
}
