package usage

import (
	"errors"
	"testing"

	"prism/internal/model"
)

// A requeue is a retry of a write, not a second sighting of the traffic. The
// buckets have to come back - the next pass is what records them - but the user
// aggregate must not: it was already incremented when the bytes arrived, and
// adding the batch to it again would charge the subscription twice for traffic
// that moved once.
func TestFlusher_RequeueDoesNotDoubleCountTheUserAggregate(t *testing.T) {
	counters := NewCounters()
	store := newFakeFlushStore()
	store.subs["u-1"] = &model.UserSubscription{ID: "s-1", UserID: "u-1", PeriodStartNs: 1000}
	store.rowErr = errors.New("disk full")
	flusher := &Flusher{Counters: counters, Store: store}

	bucket := CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}
	counters.Add(bucket, Delta{IngressBytes: 100, EgressBytes: 50})

	if _, err := flusher.FlushOnce(1); err == nil {
		t.Fatal("a failed write was reported as success")
	}
	if got := counters.UserBytes("u-1"); got != 150 {
		t.Fatalf("the user aggregate is %d after a requeue, want 150 (the batch was counted once)", got)
	}
	if got := counters.KeyBytes(bucket).Total(); got != 150 {
		t.Fatalf("the bucket holds %d bytes after a requeue, want 150", got)
	}

	// The retry writes the same 150 bytes and charges the subscription exactly
	// once: the failed attempt never reached it.
	store.rowErr = nil
	result, err := flusher.FlushOnce(2)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if result.Bytes != 150 {
		t.Fatalf("the retry wrote %d bytes, want 150", result.Bytes)
	}
	if got := store.subAdds["s-1"]; got != 150 {
		t.Fatalf("the subscription was charged %d bytes, want 150", got)
	}
	if got := counters.UserBytes("u-1"); got != 150 {
		t.Fatalf("the user aggregate is %d after the retry, want 150", got)
	}

	// A later pass has nothing left to write, so the subscription is not billed
	// a second time for the retried batch.
	if _, err := flusher.FlushOnce(3); err != nil {
		t.Fatalf("third pass: %v", err)
	}
	if got := store.subAdds["s-1"]; got != 150 {
		t.Fatalf("the subscription totals %d bytes, want 150: the requeued batch was billed again", got)
	}
	if got := store.subAddCalls; got != 1 {
		t.Fatalf("the subscription was written %d times, want 1", got)
	}
}

// rolloverFlushStore moves a subscription onto a new period between the two
// reads one flush makes, which is a rollover landing inside the flush window.
type rolloverFlushStore struct {
	*fakeFlushStore
	before *model.UserSubscription
	after  *model.UserSubscription
	reads  int
}

func (s *rolloverFlushStore) GetActiveUserSubscription(string) (*model.UserSubscription, error) {
	s.reads++
	if s.reads > 1 {
		return s.after, nil
	}
	return s.before, nil
}

// A period boundary crossed between the drain and the write must not charge the
// new period for the previous one's traffic: the bytes belong to a period that
// has already been closed and reset.
func TestFlusher_DoesNotChargeANewPeriodForTheOldOnesTraffic(t *testing.T) {
	store := &rolloverFlushStore{
		fakeFlushStore: newFakeFlushStore(),
		before:         &model.UserSubscription{ID: "s-1", UserID: "u-1", PeriodStartNs: 1_000},
		after:          &model.UserSubscription{ID: "s-1", UserID: "u-1", PeriodStartNs: 2_000},
	}
	counters := NewCounters()
	flusher := &Flusher{Counters: counters, Store: store}

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 150})

	result, err := flusher.FlushOnce(1)
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if result.Bytes != 150 {
		t.Fatalf("wrote %d bytes, want 150: the traffic is still recorded in the hourly table", result.Bytes)
	}
	if result.RolledBytes != 150 {
		t.Fatalf("reported %d rolled bytes, want 150", result.RolledBytes)
	}
	if result.Subscriptions != 0 {
		t.Fatalf("charged %d subscriptions, want 0 after the rollover", result.Subscriptions)
	}
	if got := store.subAdds["s-1"]; got != 0 {
		t.Fatalf("the new period was charged %d bytes of the old period's traffic, want 0", got)
	}
	if store.reads < 2 {
		t.Fatal("the window was not re-read, so nothing guarded the period write")
	}
}

// guardedFlushStore is the store that can express "charge this period only if it
// is still the one the bytes belong to", which is the atomic form of the guard.
type guardedFlushStore struct {
	*fakeFlushStore
	periodStartNs int64
	periodCalls   []guardedCharge
}

type guardedCharge struct {
	id            string
	periodStartNs int64
	delta         int64
}

func (s *guardedFlushStore) AddSubscriptionUsedBytesInPeriod(id string, periodStartNs, delta int64) (bool, error) {
	s.periodCalls = append(s.periodCalls, guardedCharge{id: id, periodStartNs: periodStartNs, delta: delta})
	if periodStartNs != s.periodStartNs {
		return false, nil
	}
	s.subAdds[id] += delta
	return true, nil
}

// The store that can guard the write itself is preferred over a re-read, and it
// is given the period the bytes were drained under as the idempotency key.
func TestFlusher_GuardedStoreChargesTheSnapshotPeriod(t *testing.T) {
	store := &guardedFlushStore{fakeFlushStore: newFakeFlushStore(), periodStartNs: 1_000}
	store.subs["u-1"] = &model.UserSubscription{ID: "s-1", UserID: "u-1", PeriodStartNs: 1_000}
	counters := NewCounters()
	flusher := &Flusher{Counters: counters, Store: store}

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 150})

	result, err := flusher.FlushOnce(1)
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if result.Subscriptions != 1 {
		t.Fatalf("charged %d subscriptions, want 1", result.Subscriptions)
	}
	if result.RolledBytes != 0 {
		t.Fatalf("reported %d rolled bytes on a current period, want 0", result.RolledBytes)
	}
	if got := store.subAdds["s-1"]; got != 150 {
		t.Fatalf("the subscription got %d bytes, want 150", got)
	}
	if store.subAddCalls != 0 {
		t.Fatalf("the unguarded write was used %d times, want 0: the guard must stay atomic with the write", store.subAddCalls)
	}
	if len(store.periodCalls) != 1 {
		t.Fatalf("the guarded write ran %d times, want 1", len(store.periodCalls))
	}
	if got := store.periodCalls[0]; got.id != "s-1" || got.periodStartNs != 1_000 || got.delta != 150 {
		t.Fatalf("guarded write got %+v, want the snapshot's window (s-1, 1000, 150)", got)
	}
}

// The same store refuses the write once the period has moved on, and the flush
// reports those bytes as rolled rather than as charged.
func TestFlusher_GuardedStoreRefusesAWriteAfterTheRollover(t *testing.T) {
	store := &guardedFlushStore{fakeFlushStore: newFakeFlushStore(), periodStartNs: 2_000}
	store.subs["u-1"] = &model.UserSubscription{ID: "s-1", UserID: "u-1", PeriodStartNs: 1_000}
	counters := NewCounters()
	flusher := &Flusher{Counters: counters, Store: store}

	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 150})

	result, err := flusher.FlushOnce(1)
	if err != nil {
		t.Fatalf("FlushOnce: %v", err)
	}
	if result.RolledBytes != 150 {
		t.Fatalf("reported %d rolled bytes, want 150", result.RolledBytes)
	}
	if result.Subscriptions != 0 {
		t.Fatalf("charged %d subscriptions after the rollover, want 0", result.Subscriptions)
	}
	if got := store.subAdds["s-1"]; got != 0 {
		t.Fatalf("the subscription was charged %d bytes, want 0", got)
	}
}
