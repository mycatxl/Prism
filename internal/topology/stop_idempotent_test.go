package topology

import (
	"testing"
	"time"

	"prism/internal/subscription"
)

// stopReturnsPromptly runs stop in its own goroutine so a Stop that never
// returns (a deadlock) fails this test instead of hanging the whole suite.
func stopReturnsPromptly(t *testing.T, stop func()) bool {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		stop()
	}()
	select {
	case <-done:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

// secondStopMustNotPanic converts the panic of a repeated Stop (a second close
// of stopCh, #26) into a test failure instead of crashing the test binary, so
// the regression is reported as a FAIL of this test.
func secondStopMustNotPanic(t *testing.T, stop func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("repeated Stop panicked: %v", r)
		}
	}()
	start := time.Now()
	stop()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("repeated Stop took %s, want an immediate no-op", elapsed)
	}
}

// TestEphemeralCleanerStopIsIdempotent pins #26 on the cleaner: Stop may be
// called more than once (shutdown paths and tests both do it), and the second
// call must be a no-op rather than a close of an already closed channel.
func TestEphemeralCleanerStopIsIdempotent(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := NewGlobalNodePool(PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 2 },
	})
	cleaner := NewEphemeralCleaner(subMgr, pool)
	cleaner.Start()

	if !stopReturnsPromptly(t, cleaner.Stop) {
		t.Fatal("first Stop did not return; the scan goroutine did not exit")
	}
	// The first Stop must keep doing what it always did: closing stopCh is what
	// makes scanloop.Run return.
	select {
	case <-cleaner.stopCh:
	default:
		t.Fatal("first Stop must close stopCh")
	}

	secondStopMustNotPanic(t, cleaner.Stop)
}

// TestSubscriptionSchedulerStopIsIdempotent pins #26 on the scheduler. It also
// checks that the first Stop still cancels the download context, because that
// is the part of Stop that a naive "guard everything" rewrite would drop.
func TestSubscriptionSchedulerStopIsIdempotent(t *testing.T) {
	subMgr := NewSubscriptionManager()
	subMgr.Register(subscription.NewSubscription("s1", "One", "http://example.com/one", true, false))
	pool := newTestPool(subMgr)
	sched := newTestScheduler(subMgr, pool, makeMockFetcher(nil, nil))
	sched.Start()

	if !stopReturnsPromptly(t, sched.Stop) {
		t.Fatal("first Stop did not return; the scan goroutine did not exit")
	}
	select {
	case <-sched.stopCh:
	default:
		t.Fatal("first Stop must close stopCh")
	}
	if sched.downloadCtx.Err() == nil {
		t.Fatal("first Stop must cancel the in-flight download context")
	}

	secondStopMustNotPanic(t, sched.Stop)
}
