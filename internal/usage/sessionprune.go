package usage

import (
	"fmt"
	"sync"
	"time"

	"prism/internal/safego"
)

// DefaultSessionPruneInterval is how often expired console sessions are swept.
//
// It is an hour: a session's own expiry is checked on every request it makes, so
// the sweep is housekeeping - it decides when the row is gone, not when the
// session stops working.
const DefaultSessionPruneInterval = time.Hour

// SessionStore is the persistence surface the console-session pruner needs.
// *state.StateRepo satisfies it.
type SessionStore interface {
	// PruneSessions removes the sessions that expired before nowNs and reports
	// how many rows were removed.
	PruneSessions(nowNs int64) (int64, error)
}

// SessionPruner deletes console sessions once they have expired.
//
// It lives here because it is the same kind of job as the rest of this package:
// a timer that maintains state.db on behalf of the process rather than of a
// request. Without it the sessions table only grows - a login writes a row and
// nothing ever removes it once the row's own expiry has passed - so an operator
// who deletes a user still leaves every credential that user ever held behind.
type SessionPruner struct {
	// Store is where the sessions live. Nil disables the pruner.
	Store SessionStore
	// Interval is how often Start runs a pass. Zero uses
	// DefaultSessionPruneInterval.
	Interval time.Duration
	// Logf receives progress and error messages. Nil silences them.
	Logf func(format string, args ...any)
}

func (p *SessionPruner) interval() time.Duration {
	if p.Interval > 0 {
		return p.Interval
	}
	return DefaultSessionPruneInterval
}

func (p *SessionPruner) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

// RunOnce removes the sessions that had expired by nowNs and reports how many
// were removed.
func (p *SessionPruner) RunOnce(nowNs int64) (int64, error) {
	if p == nil || p.Store == nil {
		return 0, nil
	}
	if nowNs <= 0 {
		nowNs = time.Now().UnixNano()
	}
	removed, err := p.Store.PruneSessions(nowNs)
	if err != nil {
		return 0, fmt.Errorf("prune sessions: %w", err)
	}
	return removed, nil
}

// Start runs a pass every Interval until the returned stop function is called.
// The first pass runs after one interval, not immediately: a restart must not
// add a delete to the work the databases are already doing while they settle.
func (p *SessionPruner) Start() func() {
	if p == nil || p.Store == nil {
		return func() {}
	}

	done := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(p.interval())
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// A panic inside one pass must not end the loop: the next
				// tick tries again, exactly as it does after a failed pass.
				safego.Run("the usage session prune", func() {
					removed, err := p.RunOnce(time.Now().UnixNano())
					if err != nil {
						// A failed pass needs no retry of its own: the rows
						// are still there, and the next tick tries again.
						p.logf("usage: session prune failed: %v", err)
						return
					}
					if removed > 0 {
						p.logf("usage: pruned %d expired session(s)", removed)
					}
				})
			case <-done:
				return
			}
		}
	}()

	return func() { once.Do(func() { close(done) }) }
}
