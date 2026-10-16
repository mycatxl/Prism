package usage

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSessionStore stands in for the state repository. It records every pass so
// a test can tell a loop that is running from one that has stopped, and it can
// fail on demand.
type fakeSessionStore struct {
	mu      sync.Mutex
	calls   int
	removed int64
	err     error
	lastNow int64
	failFor int
}

func (s *fakeSessionStore) PruneSessions(nowNs int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.lastNow = nowNs
	if s.failFor > 0 {
		s.failFor--
		return 0, s.err
	}
	return s.removed, nil
}

func (s *fakeSessionStore) failWith(err error, passes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
	s.failFor = passes
}

func (s *fakeSessionStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *fakeSessionStore) now() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastNow
}

// logSpy collects what a background loop logged. The loop runs in its own
// goroutine, so the slice needs its own lock for -race.
type logSpy struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSpy) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logSpy) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.lines))
	copy(out, l.lines)
	return out
}

// waitUntil polls until cond holds or the deadline passes, and reports whether
// it held. A slow machine fails the test instead of hanging it.
func waitUntil(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}

// A pass removes what the store reports and hands the store the moment it is
// pruning up to: without a real timestamp the sweep would delete rows that have
// not expired yet.
func TestSessionPruner_RunOnceReportsWhatWasRemoved(t *testing.T) {
	store := &fakeSessionStore{removed: 7}
	pruner := &SessionPruner{Store: store}

	removed, err := pruner.RunOnce(12345)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if removed != 7 {
		t.Fatalf("removed %d sessions, want 7", removed)
	}
	if got := store.now(); got != 12345 {
		t.Fatalf("the store was asked to prune up to %d, want the moment it was given", got)
	}

	// A non-positive moment means "now": a timer has no clock of its own, and
	// pruning up to zero would delete nothing and let the table grow forever.
	if _, err := pruner.RunOnce(0); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := store.now(); got <= 12345 {
		t.Fatalf("the store was asked to prune up to %d, want the current time", got)
	}
}

// A failing store is reported, wrapped so the caller can tell what failed, and
// with nothing removed. The wrapper is what a log line shows, so it has to name
// the operation as well as the cause.
func TestSessionPruner_RunOnceWrapsAFailingStore(t *testing.T) {
	boom := errors.New("database is closed")
	store := &fakeSessionStore{}
	store.failWith(boom, 1)
	pruner := &SessionPruner{Store: store}

	removed, err := pruner.RunOnce(time.Now().UnixNano())
	if err == nil {
		t.Fatal("a failed prune was reported as success")
	}
	if removed != 0 {
		t.Fatalf("a failed prune reported %d removed sessions, want 0", removed)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("the error is %v, want it to wrap the store's error", err)
	}
	if !strings.Contains(err.Error(), "prune sessions") {
		t.Fatalf("the error is %q, want it to name the operation that failed", err)
	}
}

// A pruner with nothing to prune is inert rather than a panic: the process
// holds one unconditionally, and a deployment without a state database still
// has to start.
func TestSessionPruner_MissingDependenciesAreInert(t *testing.T) {
	var nilPruner *SessionPruner
	if removed, err := nilPruner.RunOnce(time.Now().UnixNano()); removed != 0 || err != nil {
		t.Fatalf("a nil pruner reported (%d, %v)", removed, err)
	}

	noStore := &SessionPruner{}
	if removed, err := noStore.RunOnce(time.Now().UnixNano()); removed != 0 || err != nil {
		t.Fatalf("a pruner without a store reported (%d, %v)", removed, err)
	}
	if got := noStore.interval(); got != DefaultSessionPruneInterval {
		t.Fatalf("the default interval is %v, want %v", got, DefaultSessionPruneInterval)
	}

	// Their stop functions close nothing and are safe to call twice.
	var stop func()
	stop = nilPruner.Start()
	stop()
	stop()
	stop = noStore.Start()
	stop()
	stop()
}

// The loop runs a pass on its interval, and a failed pass is logged and then
// retried on the next tick rather than ending the loop: the rows are still
// there, and giving up would leave every expired session behind.
func TestSessionPruner_StartRunsAndSurvivesAFailure(t *testing.T) {
	boom := errors.New("database is busy")
	store := &fakeSessionStore{removed: 2}
	store.failWith(boom, 1)
	spy := &logSpy{}
	pruner := &SessionPruner{Store: store, Interval: 2 * time.Millisecond, Logf: spy.logf}

	stop := pruner.Start()
	defer stop()

	if !waitUntil(func() bool { return store.count() >= 2 }, 5*time.Second) {
		t.Fatalf("the pruner ran %d passes, want at least 2: it never started, or it stopped when the first pass failed", store.count())
	}

	lines := spy.all()
	if len(lines) == 0 {
		t.Fatal("a failed pass was not logged")
	}
	if !strings.Contains(lines[0], boom.Error()) {
		t.Fatalf("the log line is %q, want it to name the failure", lines[0])
	}
}

// Start returns the stop function, and once it has been called the loop must be
// gone: a pruner that keeps deleting rows after its shutdown has begun is a
// delete racing the database closing.
func TestSessionPruner_StopEndsTheLoop(t *testing.T) {
	store := &fakeSessionStore{removed: 1}
	pruner := &SessionPruner{Store: store, Interval: 2 * time.Millisecond}

	stop := pruner.Start()
	if !waitUntil(func() bool { return store.count() >= 1 }, 5*time.Second) {
		t.Fatal("Start never ran a pass")
	}
	stop()

	// The pass in flight when the stop arrived may still land, so the count is
	// taken once the loop has had time to leave.
	time.Sleep(20 * time.Millisecond)
	settled := store.count()
	time.Sleep(60 * time.Millisecond)
	if got := store.count(); got != settled {
		t.Fatalf("the pruner ran %d more passes after Stop", got-settled)
	}
}

// The stop function is a once: a caller that stops twice - a shutdown path and
// a deferred call - must not close a closed channel.
func TestSessionPruner_StopIsIdempotent(t *testing.T) {
	store := &fakeSessionStore{}
	pruner := &SessionPruner{Store: store, Interval: time.Millisecond}

	stop := pruner.Start()
	if !waitUntil(func() bool { return store.count() >= 1 }, 5*time.Second) {
		t.Fatal("Start never ran a pass, so there is nothing to stop")
	}
	// A shutdown path and a deferred call both stop the same pruner.
	stop()
	stop()

	// Two Starts hand out two independent stop functions, and both are safe to
	// call more than once.
	first := pruner.Start()
	second := pruner.Start()
	first()
	second()
	first()
}
