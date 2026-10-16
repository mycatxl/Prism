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

// A re-seed that runs after a flush has already charged the same bytes must
// restore the persisted total, not add it on top of the aggregate. The bytes a
// flush charges are still in the user aggregate (which is never drained), so a
// blind add bills the user twice for traffic that moved once.
func TestCounters_SeedAfterAFlushDoesNotDoubleCountTheChargedBytes(t *testing.T) {
	counters := NewCounters()
	store := newFakeFlushStore()
	store.subs["u-1"] = &model.UserSubscription{ID: "s-1", UserID: "u-1", PeriodStartNs: 1}
	flusher := &Flusher{Counters: counters, Store: store}

	// 500 bytes move and the flush charges them to the subscription: the
	// persisted period usage is now 500, and the aggregate holds the same 500.
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"},
		Delta{IngressBytes: 300, EgressBytes: 200})
	if _, err := flusher.FlushOnce(1); err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if got := store.subAdds["s-1"]; got != 500 {
		t.Fatalf("the subscription was charged %d bytes, want 500", got)
	}

	// The re-seed reads the persisted total back.
	subs := []model.UserSubscription{{
		ID: "s-1", UserID: "u-1", PeriodUsedBytes: 500, Status: model.SubscriptionStatusActive,
	}}
	seeded := counters.SeedFromSubscriptions(subs, time.Now().UnixNano())
	if got := counters.UserBytes("u-1"); got != 500 {
		t.Fatalf("u-1 holds %d bytes after the re-seed, want the persisted 500 "+
			"(the bytes the flush already charged must not be added again)", got)
	}
	if seeded != 0 {
		t.Fatalf("the re-seed restored %d subscription(s), want 0: the 500 bytes are already in the aggregate", seeded)
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

	// A second call restores nothing: the totals are already in the aggregate,
	// and the 100 bytes that arrived since are not on disk, so they sit above
	// the water mark on both sides instead of being added twice.
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 100})
	if seeded := counters.SeedFromSubscriptions(subs, nowNs); seeded != 0 {
		t.Fatalf("the second seed restored %d subscription(s), want 0", seeded)
	}
	if got := counters.UserBytes("u-1"); got != 600 {
		t.Fatalf("u-1 holds %d bytes after reseeding, want 600 (the persisted 500 plus the 100 it moved)", got)
	}
}

// A nil counter set must not panic on seeding.
func TestCounters_SeedFromSubscriptionsNilIsSafe(t *testing.T) {
	var counters *Counters
	if got := counters.SeedFromSubscriptions([]model.UserSubscription{{UserID: "u", PeriodUsedBytes: 1}}, 0); got != 0 {
		t.Fatalf("a nil counter set seeded %d subscriptions", got)
	}
}

// A period rollover puts the aggregate and the water mark back to zero
// together, which is what keeps the old period's bytes from being restored into
// the new one - and what keeps the re-seed loop from seeing the user as pending
// forever.
func TestCounters_ResetUserClearsTheWaterMarkWithTheTotal(t *testing.T) {
	counters := NewCounters()
	nowNs := time.Now().UnixNano()
	sub := model.UserSubscription{
		ID: "s-1", UserID: "u-1", PeriodUsedBytes: 500, Status: model.SubscriptionStatusActive,
	}

	if seeded := counters.SeedFromSubscriptions([]model.UserSubscription{sub}, nowNs); seeded != 1 {
		t.Fatalf("the first seed restored %d subscription(s), want 1", seeded)
	}
	if mark, ok := counters.PersistedBytes("u-1"); !ok || mark != 500 {
		t.Fatalf("the water mark is (%d, %v), want (500, true)", mark, ok)
	}
	if _, ok := counters.PersistedBytes("u-nobody"); ok {
		t.Fatal("a user the flusher never charged reported a water mark")
	}

	// The rollover: ResetSubscriptionPeriod zeroes the row on disk, and
	// ResetUser zeroes the aggregate and the mark that goes with it.
	counters.ResetUser("u-1")
	if mark, ok := counters.PersistedBytes("u-1"); !ok || mark != 0 {
		t.Fatalf("after the rollover the water mark is (%d, %v), want (0, true)", mark, ok)
	}
	rolled := sub
	rolled.PeriodUsedBytes = 0
	if seeded := counters.SeedFromSubscriptions([]model.UserSubscription{rolled}, nowNs); seeded != 0 {
		t.Fatalf("the new period's row restored %d subscription(s), want 0", seeded)
	}

	// The new period's own traffic: counted, charged by the flusher, and not
	// restored a second time when the re-seed reads the row back.
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 120})
	counters.MarkPersisted("u-1", 120)
	charged := sub
	charged.PeriodUsedBytes = 120
	if seeded := counters.SeedFromSubscriptions([]model.UserSubscription{charged}, nowNs); seeded != 0 {
		t.Fatalf("the re-seed restored %d subscription(s), want 0: the 120 bytes are already in the aggregate", seeded)
	}
	if got := counters.UserBytes("u-1"); got != 120 {
		t.Fatalf("u-1 holds %d bytes, want the new period's 120 and nothing of the old period's 500", got)
	}
}

// Only the bytes really charged to a subscription may move the water mark, and
// the flusher is the only caller that can: a pass that finds no subscription, or
// a store that refuses the write, leaves the mark where it was.
func TestFlusher_OnlyAChargedSubscriptionMovesTheWaterMark(t *testing.T) {
	nowNs := time.Now().UnixNano()

	// 1. No subscription: the traffic is recorded, and nothing is marked.
	counters := NewCounters()
	store := newFakeFlushStore()
	flusher := &Flusher{Counters: counters, Store: store}
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 90})
	if _, err := flusher.FlushOnce(1); err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if mark, ok := counters.PersistedBytes("u-1"); ok {
		t.Fatalf("an unclaimed pass moved the water mark to %d", mark)
	}

	// 2. A store that refuses the write: the same bytes are retried, so they
	// are not on disk and the mark must not claim they are.
	counters = NewCounters()
	store = newFakeFlushStore()
	store.subs["u-1"] = &model.UserSubscription{ID: "s-1", UserID: "u-1", PeriodStartNs: 1}
	store.addErr = errors.New("locked")
	flusher = &Flusher{Counters: counters, Store: store}
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 90})
	if _, err := flusher.FlushOnce(1); err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if mark, ok := counters.PersistedBytes("u-1"); ok {
		t.Fatalf("a failed write moved the water mark to %d", mark)
	}
	// The row on disk is whatever it was before, so the re-seed restores it.
	if seeded := counters.SeedFromSubscriptions([]model.UserSubscription{{
		ID: "s-1", UserID: "u-1", PeriodUsedBytes: 400, Status: model.SubscriptionStatusActive,
	}}, nowNs); seeded != 1 {
		t.Fatalf("the re-seed restored %d subscription(s), want 1: the failed write left the total on disk", seeded)
	}
	if got := counters.UserBytes("u-1"); got != 490 {
		t.Fatalf("u-1 holds %d bytes, want 490 (the persisted 400 plus the 90 it moved)", got)
	}

	// 3. A period that rolled over between the drain and the write: the bytes
	// are recorded and count against nothing, so the mark stays put too.
	counters = NewCounters()
	store = newFakeFlushStore()
	store.subs["u-1"] = &model.UserSubscription{ID: "s-1", UserID: "u-1", PeriodStartNs: 7}
	guarded := &periodGuardStore{fakeFlushStore: store}
	flusher = &Flusher{Counters: counters, Store: guarded}
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 90})
	result, err := flusher.FlushOnce(1)
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if result.RolledBytes != 90 {
		t.Fatalf("the pass reported %d rolled-over bytes, want 90", result.RolledBytes)
	}
	if mark, ok := counters.PersistedBytes("u-1"); ok {
		t.Fatalf("a rolled-over period moved the water mark to %d", mark)
	}
}

// periodGuardStore refuses every period write the way a store that has noticed
// the period moved on does: charged is false, and nothing is written.
type periodGuardStore struct {
	*fakeFlushStore
}

func (s *periodGuardStore) AddSubscriptionUsedBytesInPeriod(id string, periodStartNs, delta int64) (bool, error) {
	return false, nil
}
