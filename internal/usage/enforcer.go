package usage

import (
	"context"
	"time"

	"prism/internal/keys"
)

// DefaultSweepInterval is how often the enforcer looks for sessions that should
// no longer be running.
//
// It is the bound on how long a disabled, expired or over-quota session can
// outlive the decision, and the plan's requirement is one second.
const DefaultSweepInterval = time.Second

// KeyLookup resolves a key id to its current state.
//
// It is an interface rather than a *keys.Store so the enforcer does not depend
// on how keys are stored, and so a test can supply states directly.
type KeyLookup func(keyID string) *keys.KeyState

// Enforcer ends sessions whose key is no longer allowed to have them.
//
// Without it, a quota is only advisory: the counters know a limit was crossed
// but nothing acts on that, and a disabled key keeps serving every tunnel it
// had already opened. Counting is not enforcing, which is why this exists.
//
// It sweeps on a timer rather than reacting to each byte for a simple reason:
// the checks it makes - is the key disabled, has the plan expired, is the
// aggregate over the allowance - are the same for every connection of a key, so
// doing them once per key per second is both cheaper and easier to reason about
// than doing them per byte on the data path.
type Enforcer struct {
	// Registry holds the live sessions, grouped by key. Required.
	Registry *ConnRegistry
	// Limiter decides whether a key's usage has passed its allowance. Nil
	// disables the traffic check, leaving only the usability one.
	Limiter *Limiter
	// Lookup resolves a key id to its state. Nil disables the sweep entirely:
	// with no way to tell whether a key is still allowed, closing its sessions
	// would be arbitrary.
	Lookup KeyLookup
	// Interval is how often Start sweeps. Zero uses DefaultSweepInterval.
	Interval time.Duration
	// GracePercent is the tolerance applied to the traffic limit before a live
	// session is cut, so a transfer that crossed the limit is not cut off with
	// the bytes already in flight. Zero means no grace.
	GracePercent int
	// Logf receives messages when sessions are closed. Nil silences them.
	Logf func(format string, args ...any)

	// now is the clock the expiry checks read. Nil uses the wall clock.
	now func() int64
}

func (e *Enforcer) interval() time.Duration {
	if e.Interval > 0 {
		return e.Interval
	}
	return DefaultSweepInterval
}

func (e *Enforcer) nowNs() int64 {
	if e.now != nil {
		return e.now()
	}
	return time.Now().UnixNano()
}

func (e *Enforcer) logf(format string, args ...any) {
	if e.Logf != nil {
		e.Logf(format, args...)
	}
}

// SweepResult reports what one sweep did.
type SweepResult struct {
	// Checked is how many keys held at least one session.
	Checked int
	// ClosedSessions is how many sessions were ended.
	ClosedSessions int
}

// SweepOnce closes the sessions of every key that may no longer hold them.
//
// A key's sessions are closed when the key cannot be resolved at all (it was
// deleted, or its owner was), when its state says it is not usable (disabled,
// expired, suspended), or when its usage has passed the allowance plus the
// grace.
//
// It returns how much it did so a caller - a test, or a console - can see the
// work without reading logs.
func (e *Enforcer) SweepOnce(nowNs int64) SweepResult {
	var result SweepResult
	if e == nil || e.Registry == nil || e.Lookup == nil {
		return result
	}
	if nowNs <= 0 {
		nowNs = e.nowNs()
	}

	for _, keyID := range e.Registry.Keys() {
		key := e.Lookup(keyID)
		if key != nil {
			result.Checked++
		}

		reason := ""
		switch {
		case key == nil:
			// The key is gone. Its sessions must not outlive it, which is what
			// makes a deletion mean something.
			reason = "the key no longer exists"
		case !key.Usable(nowNs):
			reason = "the key or its plan is no longer usable"
		case e.Limiter != nil && e.Limiter.Exceeded(key, e.GracePercent):
			reason = "the allowance is used up"
		default:
			continue
		}

		closed := e.Registry.CloseKey(keyID)
		if closed > 0 {
			result.ClosedSessions += closed
			e.logf("usage: closed %d session(s) for key %s: %s", closed, keyID, reason)
		}
	}
	return result
}

// Start sweeps every interval until ctx is cancelled.
func (e *Enforcer) Start(ctx context.Context) {
	if e == nil || e.Registry == nil || e.Lookup == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(e.interval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.SweepOnce(0)
		}
	}
}
