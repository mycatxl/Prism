package usage

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/keys"
	"prism/internal/model"
)

// closerSpy is an io.Closer that records whether it was closed.
type closerSpy struct {
	closed atomic.Int32
	err    error
}

func (c *closerSpy) Close() error {
	c.closed.Add(1)
	return c.err
}

func (c *closerSpy) wasClosed() bool { return c.closed.Load() > 0 }

var _ io.Closer = (*closerSpy)(nil)

func sweepKeyState(status string) *keys.KeyState {
	return &keys.KeyState{
		KeyID:      "k-1",
		UserID:     "u-1",
		Scope:      model.KeyScopeProxy,
		Status:     status,
		UserStatus: model.UserStatusActive,
		Plan:       keys.PlanSnapshot{Status: model.SubscriptionStatusActive},
	}
}

func enforcerFor(key *keys.KeyState) (*Enforcer, *ConnRegistry) {
	registry := NewConnRegistry()
	return &Enforcer{
		Registry: registry,
		Limiter:  NewLimiter(NewCounters()),
		Lookup:   func(string) *keys.KeyState { return key },
	}, registry
}

// A usable key's sessions are left alone: the sweep must not disconnect people
// who are within their allowance.
func TestEnforcer_LeavesUsableKeysAlone(t *testing.T) {
	enforcer, registry := enforcerFor(sweepKeyState(model.KeyStatusActive))
	conn := &closerSpy{}
	registry.Track("k-1", conn)

	result := enforcer.SweepOnce(time.Now().UnixNano())

	if result.ClosedSessions != 0 {
		t.Fatalf("closed %d sessions for a usable key, want 0", result.ClosedSessions)
	}
	if conn.wasClosed() {
		t.Fatal("a usable key's session was closed")
	}
	if result.Checked != 1 {
		t.Fatalf("checked %d keys, want 1", result.Checked)
	}
}

// A disabled key's live sessions are ended, which is what makes a disable
// immediate rather than "at the next request".
func TestEnforcer_ClosesDisabledKeysSessions(t *testing.T) {
	enforcer, registry := enforcerFor(sweepKeyState(model.KeyStatusDisabled))
	conn := &closerSpy{}
	registry.Track("k-1", conn)

	result := enforcer.SweepOnce(time.Now().UnixNano())

	if result.ClosedSessions != 1 {
		t.Fatalf("closed %d sessions, want 1", result.ClosedSessions)
	}
	if !conn.wasClosed() {
		t.Fatal("a disabled key's session was left open")
	}
	if got := registry.Count("k-1"); got != 0 {
		t.Fatalf("the registry still holds %d sessions", got)
	}
}

// An expired key's sessions are ended too.
func TestEnforcer_ClosesExpiredKeysSessions(t *testing.T) {
	key := sweepKeyState(model.KeyStatusActive)
	key.ExpiresAtNs = time.Now().Add(-time.Minute).UnixNano()
	enforcer, registry := enforcerFor(key)
	conn := &closerSpy{}
	registry.Track("k-1", conn)

	if result := enforcer.SweepOnce(time.Now().UnixNano()); result.ClosedSessions != 1 {
		t.Fatalf("closed %d sessions, want 1", result.ClosedSessions)
	}
	if !conn.wasClosed() {
		t.Fatal("an expired key's session was left open")
	}
}

// A key that cannot be resolved at all is deleted, or its owner was. Its
// sessions must not outlive it.
func TestEnforcer_ClosesSessionsOfAKeyThatNoLongerExists(t *testing.T) {
	registry := NewConnRegistry()
	enforcer := &Enforcer{
		Registry: registry,
		// The key is gone: the lookup answers nothing.
		Lookup: func(string) *keys.KeyState { return nil },
	}
	conn := &closerSpy{}
	registry.Track("k-deleted", conn)

	result := enforcer.SweepOnce(time.Now().UnixNano())

	if result.ClosedSessions != 1 {
		t.Fatalf("closed %d sessions, want 1", result.ClosedSessions)
	}
	if !conn.wasClosed() {
		t.Fatal("a deleted key's session was left open")
	}
	if result.Checked != 0 {
		t.Fatalf("counted %d resolved keys, want 0", result.Checked)
	}
}

// A session that has passed the traffic allowance is cut, with the grace
// applied so a transfer that crossed the line is not cut off mid-flight.
func TestEnforcer_ClosesOverQuotaSessionsWithGrace(t *testing.T) {
	counters := NewCounters()
	key := sweepKeyState(model.KeyStatusActive)
	key.Plan.TrafficBytes = 1000

	registry := NewConnRegistry()
	enforcer := &Enforcer{
		Registry:     registry,
		Limiter:      NewLimiter(counters),
		Lookup:       func(string) *keys.KeyState { return key },
		GracePercent: 10,
	}

	conn := &closerSpy{}
	registry.Track("k-1", conn)

	// At the limit but inside the grace: still running.
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 1050})
	if result := enforcer.SweepOnce(time.Now().UnixNano()); result.ClosedSessions != 0 {
		t.Fatalf("closed %d sessions inside the grace, want 0", result.ClosedSessions)
	}
	if conn.wasClosed() {
		t.Fatal("a session inside the grace was closed")
	}

	// Past the grace: cut.
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 200})
	if result := enforcer.SweepOnce(time.Now().UnixNano()); result.ClosedSessions != 1 {
		t.Fatalf("closed %d sessions past the grace, want 1", result.ClosedSessions)
	}
	if !conn.wasClosed() {
		t.Fatal("an over-quota session was left open")
	}
}

// A key with no traffic limit is never cut for traffic, whatever it moved.
func TestEnforcer_DoesNotCutKeysWithoutATrafficLimit(t *testing.T) {
	counters := NewCounters()
	key := sweepKeyState(model.KeyStatusActive) // TrafficBytes is 0 = unlimited

	registry := NewConnRegistry()
	enforcer := &Enforcer{
		Registry: registry,
		Limiter:  NewLimiter(counters),
		Lookup:   func(string) *keys.KeyState { return key },
	}
	counters.Add(CounterKey{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1"}, Delta{IngressBytes: 1 << 40})

	conn := &closerSpy{}
	registry.Track("k-1", conn)

	if result := enforcer.SweepOnce(time.Now().UnixNano()); result.ClosedSessions != 0 {
		t.Fatalf("closed %d sessions for a key with no limit, want 0", result.ClosedSessions)
	}
	if conn.wasClosed() {
		t.Fatal("a key with no traffic limit had its session closed")
	}
}

// Only the offending key's sessions are closed: one tenant's problem must not
// disconnect another's users.
func TestEnforcer_ClosesOnlyTheOffendingKey(t *testing.T) {
	states := map[string]*keys.KeyState{
		"k-bad":  sweepKeyState(model.KeyStatusDisabled),
		"k-good": sweepKeyState(model.KeyStatusActive),
	}
	registry := NewConnRegistry()
	enforcer := &Enforcer{
		Registry: registry,
		Lookup:   func(keyID string) *keys.KeyState { return states[keyID] },
	}

	bad, good := &closerSpy{}, &closerSpy{}
	registry.Track("k-bad", bad)
	registry.Track("k-good", good)

	if result := enforcer.SweepOnce(time.Now().UnixNano()); result.ClosedSessions != 1 {
		t.Fatalf("closed %d sessions, want 1", result.ClosedSessions)
	}
	if !bad.wasClosed() {
		t.Fatal("the disabled key's session was left open")
	}
	if good.wasClosed() {
		t.Fatal("an unrelated key's session was closed")
	}
}

// Every session of the offending key goes, not just the first.
func TestEnforcer_ClosesEverySessionOfTheKey(t *testing.T) {
	enforcer, registry := enforcerFor(sweepKeyState(model.KeyStatusDisabled))
	conns := []*closerSpy{{}, {}, {}}
	for _, conn := range conns {
		registry.Track("k-1", conn)
	}

	if result := enforcer.SweepOnce(time.Now().UnixNano()); result.ClosedSessions != 3 {
		t.Fatalf("closed %d sessions, want 3", result.ClosedSessions)
	}
	for i, conn := range conns {
		if !conn.wasClosed() {
			t.Fatalf("session %d was left open", i)
		}
	}
}

// An enforcer with no lookup, no registry, or a nil limiter must be inert
// rather than a panic. A deployment without plans has no limiter, and its keys
// must keep working.
func TestEnforcer_MissingDependenciesAreInert(t *testing.T) {
	var nilEnforcer *Enforcer
	if got := nilEnforcer.SweepOnce(0); got.ClosedSessions != 0 {
		t.Fatalf("a nil enforcer closed %d sessions", got.ClosedSessions)
	}

	registry := NewConnRegistry()
	conn := &closerSpy{}
	registry.Track("k-1", conn)

	noLookup := &Enforcer{Registry: registry}
	if got := noLookup.SweepOnce(0); got.ClosedSessions != 0 {
		t.Fatalf("an enforcer with no lookup closed %d sessions", got.ClosedSessions)
	}

	// A limited enforcer with no limiter still enforces usability.
	noLimiter := &Enforcer{
		Registry: registry,
		Lookup:   func(string) *keys.KeyState { return sweepKeyState(model.KeyStatusActive) },
	}
	if got := noLimiter.SweepOnce(0); got.ClosedSessions != 0 {
		t.Fatalf("an enforcer with no limiter closed %d sessions of a usable key", got.ClosedSessions)
	}
	if conn.wasClosed() {
		t.Fatal("an enforcer with no limiter closed a usable key's session")
	}
}

// A sweep that closes nothing must not report work it did not do.
func TestEnforcer_EmptyRegistrySweepsNothing(t *testing.T) {
	enforcer, _ := enforcerFor(sweepKeyState(model.KeyStatusDisabled))
	result := enforcer.SweepOnce(time.Now().UnixNano())
	if result.Checked != 0 || result.ClosedSessions != 0 {
		t.Fatalf("an empty registry reported %+v", result)
	}
}

// Start must sweep on its interval and stop promptly when cancelled.
func TestEnforcer_StartSweepsUntilCancelled(t *testing.T) {
	enforcer, registry := enforcerFor(sweepKeyState(model.KeyStatusDisabled))
	enforcer.Interval = 10 * time.Millisecond

	conn := &closerSpy{}
	registry.Track("k-1", conn)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		enforcer.Start(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !conn.wasClosed() {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after its context was cancelled")
	}
	if !conn.wasClosed() {
		t.Fatal("Start never closed a disabled key's session")
	}
}

// A close error is the connection's own business: the session is untracked
// either way, and one failure must not stop the rest of the sweep.
func TestEnforcer_CloseErrorsDoNotStopTheSweep(t *testing.T) {
	enforcer, registry := enforcerFor(sweepKeyState(model.KeyStatusDisabled))

	failing := &closerSpy{err: errors.New("already gone")}
	other := &closerSpy{}
	registry.Track("k-1", failing)
	registry.Track("k-1", other)

	result := enforcer.SweepOnce(time.Now().UnixNano())
	if result.ClosedSessions != 2 {
		t.Fatalf("closed %d sessions, want 2", result.ClosedSessions)
	}
	if !other.wasClosed() {
		t.Fatal("a close error stopped the second session from being closed")
	}
}
