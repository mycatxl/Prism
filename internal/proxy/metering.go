package proxy

import (
	"io"

	"prism/internal/keys"
)

// SessionTracker holds the live sessions of each key so they can be ended when
// the key is no longer allowed to have them.
//
// It exists because counting is not enforcing: a counter can know that an
// allowance is used up, but only a registry of what is open can hang up on it.
// A disabled key keeping every tunnel it had already opened would make a
// disable mean "at the next request", which is not what an operator expects.
type SessionTracker interface {
	// Track records one session against a key.
	Track(keyID string, closer io.Closer)
	// Untrack removes it. It is safe to call for a session that was never
	// tracked, which keeps the caller's defer unconditional.
	Untrack(keyID string, closer io.Closer)
	// Count reports how many sessions one key currently holds. The concurrency
	// limit reads it.
	Count(keyID string) int
}

// checkQuota asks the gate whether a key may serve another request.
//
// It answers nil when the request may proceed, and the error to return when it
// may not. A nil gate, and a request that carried no key, both proceed: the
// gate is optional and the transitional token path has no allowance to check.
//
// concurrent is the number of sessions the key already holds. The callers that
// can know it pass it; the ones that cannot pass zero, which means only the
// traffic limit is enforced for them.
func checkQuota(gate QuotaGate, key *keys.KeyState, concurrent int) *ProxyError {
	if gate == nil || key == nil {
		return nil
	}
	allowed, _ := gate.Check(key, concurrent)
	if allowed {
		return nil
	}
	return ErrQuotaExceeded
}

// sessionCount reports how many sessions a key holds, for the concurrency limit.
func sessionCount(tracker SessionTracker, key *keys.KeyState) int {
	if tracker == nil || key == nil || key.KeyID == "" {
		return 0
	}
	return tracker.Count(key.KeyID)
}

// trackSession registers a session against a key and returns the function that
// unregisters it.
//
// It answers a no-op when there is nothing to track: no tracker, or a request
// that carried no key. That keeps every caller's defer unconditional.
func trackSession(tracker SessionTracker, key *keys.KeyState, closer io.Closer) func() {
	if tracker == nil || key == nil || key.KeyID == "" || closer == nil {
		return func() {}
	}
	tracker.Track(key.KeyID, closer)
	return func() { tracker.Untrack(key.KeyID, closer) }
}

// MeteringSink receives traffic attributed to the access key that caused it.
//
// It is separate from MetricsEventSink on purpose. MetricsEventSink reports
// whole-process totals and knows nothing about who moved the bytes; a quota
// needs the opposite. A sink may implement both, in which case the dashboard
// keeps its totals while a per-key allowance is enforced.
//
// The interface deals only in ids and counts, so the proxy package does not
// have to depend on the accounting implementation.
type MeteringSink interface {
	// OnKeyTrafficDelta reports bytes moved for one key on one platform.
	// An empty keyID means the request carried no key - the transitional
	// global-token path - and such a delta is counted against nobody.
	OnKeyTrafficDelta(keyID, userID, platformID string, ingressBytes, egressBytes int64)
	// OnKeyRequest reports one finished request. failed says whether the
	// request ended in a proxy or upstream error.
	OnKeyRequest(keyID, userID, platformID string, failed bool)
}

// QuotaGate decides whether a request may proceed at all.
//
// It is consulted once per request, after the credential is resolved and
// before anything is dialled. A nil gate allows everything, which is what a
// deployment without plans gets.
type QuotaGate interface {
	// Check reports whether the key may open another request given that it
	// already holds concurrent connections.
	Check(key *keys.KeyState, concurrent int) (allowed bool, reason string)
	// Exceeded reports whether a key has already used more than its
	// allowance, with gracePercent of tolerance. It is what a live
	// connection asks before hanging itself up.
	Exceeded(key *keys.KeyState, gracePercent int) bool
}

// requestMeter attributes one request's or one tunnel's traffic to a key.
//
// It is attached to the request lifecycle rather than to a connection. That
// distinction is what makes the accounting correct: the upstream HTTP
// transports are pooled per node and their connections are reused across
// requests, so a connection cannot be said to belong to one key. The lifecycle
// can - it is created per request, after the credential has been resolved.
//
// bytesAtConn says the bytes are already being reported by the connection
// itself. That is true on the tunnel paths, where the upstream connection is
// dialled per session and carries a counting wrapper that flushes every 32 KB.
// Those paths get mid-stream reporting, which is what lets a long download be
// cut off while it is still running; the lifecycle must then not report the
// same bytes again at the end.
type requestMeter struct {
	sink        MeteringSink
	keyID       string
	userID      string
	bytesAtConn bool
}

// attachMeter enables attributed accounting for this request.
//
// A nil sink, or a request that carried no key, leaves the lifecycle
// unattributed: the transitional token path has nothing to attribute to.
func (l *requestLifecycle) attachMeter(sink MeteringSink, key *keys.KeyState, bytesAtConn bool) {
	if l == nil || sink == nil || key == nil || key.KeyID == "" {
		return
	}
	l.meter = &requestMeter{
		sink:        sink,
		keyID:       key.KeyID,
		userID:      key.UserID,
		bytesAtConn: bytesAtConn,
	}
}

// platformForMetering returns the platform the request was routed to, which is
// filled in by setRouteResult before any byte moves.
func (l *requestLifecycle) platformForMetering() string {
	if l == nil {
		return ""
	}
	return l.log.PlatformID
}

// reportIngress forwards one ingress increment, unless the connection is
// already reporting the bytes.
func (l *requestLifecycle) reportIngress(n int64) {
	if l == nil || l.meter == nil || l.meter.bytesAtConn || l.meter.sink == nil {
		return
	}
	l.meter.sink.OnKeyTrafficDelta(l.meter.keyID, l.meter.userID, l.platformForMetering(), n, 0)
}

// reportEgress forwards one egress increment, unless the connection is already
// reporting the bytes.
func (l *requestLifecycle) reportEgress(n int64) {
	if l == nil || l.meter == nil || l.meter.bytesAtConn || l.meter.sink == nil {
		return
	}
	l.meter.sink.OnKeyTrafficDelta(l.meter.keyID, l.meter.userID, l.platformForMetering(), 0, n)
}

// reportFinish forwards the request count. Bytes are not sent from here: they
// went out as they were counted, which is what makes the totals add up when a
// connection is still open.
func (l *requestLifecycle) reportFinish() {
	if l == nil || l.meter == nil || l.meter.sink == nil {
		return
	}
	l.meter.sink.OnKeyRequest(
		l.meter.keyID,
		l.meter.userID,
		l.platformForMetering(),
		!l.log.NetOK,
	)
}
