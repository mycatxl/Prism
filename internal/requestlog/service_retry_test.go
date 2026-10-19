package requestlog

import (
	"errors"
	"sync"
	"testing"
	"time"

	"prism/internal/proxy"
)

// flakyInserter stands in for a repository that is temporarily unavailable -
// "database is locked", a rotation in progress, a disk that filled for a moment
// - and then accepts its writes. It keeps every entry it was handed so the test
// can say not only "the service retried" but "no entry was lost or duplicated".
type flakyInserter struct {
	mu           sync.Mutex
	failuresLeft int
	attempts     int
	accepted     []proxy.RequestLogEntry
}

func (f *flakyInserter) InsertBatch(entries []proxy.RequestLogEntry) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.failuresLeft > 0 {
		f.failuresLeft--
		return 0, errors.New("database is locked")
	}
	f.accepted = append(f.accepted, entries...)
	return len(entries), nil
}

func (f *flakyInserter) state() (attempts int, accepted []proxy.RequestLogEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts, append([]proxy.RequestLogEntry(nil), f.accepted...)
}

func testEntry(path string) proxy.RequestLogEntry {
	return proxy.RequestLogEntry{
		StartedAtNs: time.Now().UnixNano(),
		TargetHost:  "example.com",
		TargetURL:   "http://example.com" + path,
		HTTPMethod:  "GET",
	}
}

// A failed InsertBatch used to lose its batch outright. The entries of that
// window are audit evidence and the basis of traffic reconciliation, so a store
// that is busy for a moment must delay them, not destroy them.
func TestService_RequeuesFailedBatchUntilItLands(t *testing.T) {
	store := &flakyInserter{failuresLeft: 2}
	svc := NewService(ServiceConfig{QueueSize: 16, FlushBatch: 4, FlushInterval: time.Hour})
	svc.insert = store
	// The waits are shortened so the retries happen in milliseconds; the policy
	// itself is what is under test, not the wall clock.
	svc.retryBackoffMin = time.Millisecond
	svc.retryBackoffMax = 2 * time.Millisecond
	svc.Start()
	defer svc.Stop()

	want := []string{"/a", "/b", "/c", "/d"}
	for _, path := range want {
		svc.EmitRequestLog(testEntry(path))
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		attempts, accepted := store.state()
		if len(accepted) == len(want) {
			if attempts < 3 {
				t.Fatalf("the batch landed after %d attempts; two failures were configured", attempts)
			}
			for i, path := range want {
				if got := accepted[i].TargetURL; got != "http://example.com"+path {
					t.Fatalf("accepted[%d].TargetURL = %q, want %q: a retry must not reorder the log", i, got, "http://example.com"+path)
				}
			}
			if dropped := svc.DroppedEntries(); dropped != 0 {
				t.Fatalf("dropped %d entries although the third attempt succeeded", dropped)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after %d attempts %d of %d entries landed", attempts, len(accepted), len(want))
		}
		time.Sleep(time.Millisecond)
	}
}

// A store that never recovers must lose the batch in a way an operator can
// account for: a definite count, logged and readable, instead of a silent gap in
// the log.
func TestService_GivesUpAfterTheAttemptCeilingWithADefiniteCount(t *testing.T) {
	store := &flakyInserter{failuresLeft: 1 << 20}
	svc := NewService(ServiceConfig{QueueSize: 16, FlushBatch: 3, FlushInterval: time.Hour})
	svc.insert = store
	svc.retryBackoffMin = time.Millisecond
	svc.retryBackoffMax = 2 * time.Millisecond
	svc.maxFlushAttempts = 3
	svc.Start()
	defer svc.Stop()

	for _, path := range []string{"/x", "/y", "/z"} {
		svc.EmitRequestLog(testEntry(path))
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if dropped := svc.DroppedEntries(); dropped > 0 {
			if dropped != 3 {
				t.Fatalf("dropped = %d, want the whole batch of 3", dropped)
			}
			if attempts, _ := store.state(); attempts != 3 {
				t.Fatalf("attempts = %d, want the ceiling of 3", attempts)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a permanently failing store was never counted as a loss: attempts=%d", countAttempts(store))
		}
		time.Sleep(time.Millisecond)
	}
}

func countAttempts(store *flakyInserter) int {
	attempts, _ := store.state()
	return attempts
}

// The retry buffer is bounded. An outage must not let the request log grow
// without limit on the data plane's heap, and the entries it does give up have
// to be counted and the oldest ones - the newest are the ones an operator is
// about to look for.
func TestService_RetryBufferIsBoundedAndDropsTheOldest(t *testing.T) {
	svc := NewService(ServiceConfig{QueueSize: 8, FlushBatch: 4, FlushInterval: time.Hour})
	// No Start: this is the buffer policy on its own, without a store.

	entries := make([]proxy.RequestLogEntry, 20)
	for i := range entries {
		entries[i] = testEntry("/entry-" + string(rune('a'+i)))
	}

	svc.requeue(entries, errors.New("database is locked"))

	limit := svc.retryBufferLimit()
	if limit != 12 {
		t.Fatalf("retryBufferLimit = %d, want queue(8)+batch(4)", limit)
	}
	if len(svc.retry) != limit {
		t.Fatalf("kept %d entries, want the bound of %d", len(svc.retry), limit)
	}
	if dropped := svc.DroppedEntries(); dropped != uint64(len(entries)-limit) {
		t.Fatalf("dropped = %d, want %d", dropped, len(entries)-limit)
	}
	if got, want := svc.retry[0].TargetURL, entries[len(entries)-limit].TargetURL; got != want {
		t.Fatalf("oldest kept entry = %q, want %q: the newest are the ones worth keeping", got, want)
	}
	if svc.retryAt.IsZero() {
		t.Fatal("a requeued batch has no retry deadline; it would never be attempted again")
	}
}

// Shutdown during an outage cannot write what is left, and saying so is the
// difference between a documented loss and a clean-looking exit that silently
// dropped records.
func TestService_StopCountsWhatItCouldNotWrite(t *testing.T) {
	store := &flakyInserter{failuresLeft: 1 << 20}
	svc := NewService(ServiceConfig{QueueSize: 16, FlushBatch: 2, FlushInterval: time.Hour})
	svc.insert = store
	svc.retryBackoffMin = time.Hour // no retry can land before Stop
	svc.retryBackoffMax = time.Hour
	svc.Start()

	svc.EmitRequestLog(testEntry("/lost-1"))
	svc.EmitRequestLog(testEntry("/lost-2"))

	// Wait for the batch to be attempted and requeued before stopping.
	deadline := time.Now().Add(5 * time.Second)
	for countAttempts(store) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the batch was never offered to the store")
		}
		time.Sleep(time.Millisecond)
	}
	svc.Stop()

	if dropped := svc.DroppedEntries(); dropped != 2 {
		t.Fatalf("dropped = %d after shutdown, want 2", dropped)
	}
	if len(svc.retry) != 0 {
		t.Fatalf("retry buffer still holds %d entries after Stop", len(svc.retry))
	}
}
