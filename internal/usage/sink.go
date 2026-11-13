package usage

import (
	"prism/internal/keys"
	"time"
)

// Sink records proxy traffic into a counter set.
//
// It matches the proxy package's metering interface structurally, so neither
// package has to import the other: the proxy defines what it needs, and this
// provides it.
type Sink struct {
	// Counters is where the deltas land. A nil counter set makes every call a
	// no-op, so a deployment without accounting can still install a sink.
	Counters *Counters
}

// OnKeyTrafficDelta records bytes moved for one key on one platform.
func (s *Sink) OnKeyTrafficDelta(keyID, userID, platformID string, ingressBytes, egressBytes int64) {
	if s == nil || s.Counters == nil || keyID == "" || ingressBytes < 0 || egressBytes < 0 {
		return
	}
	s.Counters.Add(CounterKey{KeyID: keyID, UserID: userID, PlatformID: platformID},
		Delta{IngressBytes: ingressBytes, EgressBytes: egressBytes})
}

// OnKeyRequest records one finished request for a key.
func (s *Sink) OnKeyRequest(keyID, userID, platformID string, failed bool) {
	if s == nil || s.Counters == nil || keyID == "" {
		return
	}
	delta := Delta{Requests: 1}
	if failed {
		delta.Errors = 1
	}
	s.Counters.Add(CounterKey{KeyID: keyID, UserID: userID, PlatformID: platformID}, delta)
}

// Gate exposes the limiter through the proxy's quota interface.
//
// The limiter answers a rich Decision because an operator wants to know which
// limit was hit; the proxy only needs a yes or no plus a reason string. This is
// the adapter between the two, and it is a type of its own so the difference in
// signatures does not have to leak into either interface.
type Gate struct {
	// Journal makes byte liability durable before service. Wired in production.
	Journal *Journal
	// Limiter decides. A nil limiter allows keyed requests when quota
	// enforcement is not wired; keyless requests are always refused.
	Limiter *Limiter
	// Keys refreshes long-lived streams after plan changes or revocation.
	Keys *keys.Store
	// GracePercent is the tolerance a live session gets before it is cut. It is
	// read by the enforcer, not by Check.
	GracePercent int
}

// Check reports whether a key may open another request.
func (g *Gate) Check(key *keys.KeyState, concurrent int) (bool, string) {
	if key == nil {
		return false, ReasonDisabled
	}
	if g == nil || g.Limiter == nil {
		return true, ""
	}
	key = g.currentKey(key)
	if key != nil && g.Journal.Blocked(key.UserID) {
		return false, "accounting"
	}
	decision := g.Limiter.Check(key, concurrent)
	return decision.Allowed, decision.Reason
}

// Exceeded reports whether a key has already passed its allowance.
func (g *Gate) Exceeded(key *keys.KeyState, gracePercent int) bool {
	if g == nil || g.Limiter == nil {
		return false
	}
	key = g.currentKey(key)
	if key == nil || g.Journal.Blocked(key.UserID) {
		return true
	}
	return g.Limiter.Exceeded(key, gracePercent)
}

func (g *Gate) currentKey(key *keys.KeyState) *keys.KeyState {
	if g.Keys == nil || key == nil {
		return key
	}
	current, ok := g.Keys.Get(key.KeyID)
	if !ok || current.UserID != key.UserID || current.SessionEpoch != key.SessionEpoch || !current.Usable(time.Now().UnixNano()) {
		return nil
	}
	return current
}
