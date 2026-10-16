package proxy

import (
	"context"
	"io"
	"sync"

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

// sessionReservationMu serialises three things that must not be separable: the
// read of a key's session count, the decision to admit the request, and the
// registration of the session that decision is about.
//
// They used to be three steps at every call site - read the count, ask the
// gate, register afterwards - so a key with max_concurrent=1 admitted every
// request of a burst that arrived together: each of them read a count of zero
// before any of them had registered, and nothing ever corrected it, because the
// enforcer's sweep only looks at revoked keys, disabled users and exhausted
// allowances, not at concurrency. Holding the lock across the read, the check
// and the registration makes the reservation the count the check is made
// against. The critical section is a map lookup plus usage.Limiter.Check, which
// is an atomic load, so it does not sit on the copy path.
//
// One lock for every protocol on purpose: the registry is shared, so requests
// of one key arriving on the forward proxy, the reverse proxy and SOCKS5 must
// see each other's reservations.
var sessionReservationMu sync.Mutex

// sessionReservation is the session a request holds while it is served.
//
// The registry keeps the reservation itself rather than the connection: the
// session is registered before anything is dialled, and a caller that later has
// a real connection attaches it to the reservation. Untracking the reservation
// and tracking the connection instead would drop the count to zero in between,
// which is exactly the window the reservation exists to close. Closing the
// reservation closes whatever was attached to it, so an operator's CloseKey
// still ends the session.
type sessionReservation struct {
	tracker SessionTracker
	keyID   string
	session io.Closer
	tracked bool
	release sync.Once
}

// Release unregisters the session. It is idempotent and safe on a nil
// reservation, so every caller can defer it unconditionally.
func (r *sessionReservation) Release() {
	if r == nil || !r.tracked {
		return
	}
	r.release.Do(func() { r.tracker.Untrack(r.keyID, r.session) })
}

// reserveSession records the session of the request that is about to be served
// and asks the gate whether the key may hold it.
//
// It answers the error to return when the request may not proceed, together
// with the reservation the caller must release when the request is over. The
// reservation is registered before the gate is asked, so the decision is made
// against a count that already includes this session; the count handed to the
// gate is the one held before it, which is what Limiter.Check means by
// "concurrent". A refusal releases the reservation again and leaves nothing
// behind.
//
// A nil gate, and a request that carried no key, both proceed: the gate is
// optional and the transitional token path has no allowance to check.
func reserveSession(
	gate QuotaGate,
	tracker SessionTracker,
	key *keys.KeyState,
	session io.Closer,
	logf func(format string, args ...any),
) (*ProxyError, *sessionReservation) {
	reservation := &sessionReservation{}
	if key == nil {
		return nil, reservation
	}

	sessionReservationMu.Lock()
	held := sessionCount(tracker, key)
	if tracker != nil && session != nil && key.KeyID != "" {
		tracker.Track(key.KeyID, session)
		reservation.tracker = tracker
		reservation.keyID = key.KeyID
		reservation.session = session
		reservation.tracked = true
	}
	allowed, reason := true, ""
	if gate != nil {
		allowed, reason = gate.Check(key, held)
	}
	sessionReservationMu.Unlock()

	if !allowed {
		// The reservation was only a placeholder for a request that is being
		// refused: it must not count against the key's next request.
		reservation.Release()
		return refusalProxyError(reason, key, logf), reservation
	}
	return nil, reservation
}

// refusalProxyError builds the error of a request the gate refused and reports
// the refusal on the server side.
//
// The client-facing text stays deliberately general (see ErrQuotaExceeded): a
// caller is not told which part of somebody's account is at fault. The operator
// needs the opposite - which limit was hit and which key hit it - and a refusal
// happens before the request lifecycle exists, so it would otherwise leave no
// trace at all, neither in the request log nor in the server log. Only the key
// id and the reason are logged: no credential, no user id, no account.
func refusalProxyError(reason string, key *keys.KeyState, logf func(format string, args ...any)) *ProxyError {
	if logf != nil {
		keyID := "(no key)"
		if key != nil && key.KeyID != "" {
			keyID = key.KeyID
		}
		if reason == "" {
			reason = "unspecified"
		}
		logf("proxy: refused a request for key %s: %s", keyID, reason)
	}
	return ErrQuotaExceeded
}

// pendingSession is the registry handle of a session that is being
// established: the concurrency limit has to be decided before the dial, but the
// connection the decision is about does not exist yet.
//
// It starts empty and the caller attaches the real connection to it once there
// is one (a hijacked client connection, an accepted SOCKS5 connection). Close
// ends whatever the session has become - cancelling a dial in progress, or
// closing an established connection - which is what an operator's CloseKey
// reaches. Attaching after the session was closed closes the connection
// immediately rather than leaving it untracked and uncloseable.
type pendingSession struct {
	cancel context.CancelFunc

	mu     sync.Mutex
	closer io.Closer
	closed bool
}

func newPendingSession(cancel context.CancelFunc) *pendingSession {
	if cancel == nil {
		return nil
	}
	return &pendingSession{cancel: cancel}
}

// attach hands the established connection to the reservation.
func (s *pendingSession) attach(closer io.Closer) {
	if s == nil || closer == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = closer.Close()
		return
	}
	s.closer = closer
	s.mu.Unlock()
}

// Close ends the session: the cancel stops anything still being dialled or
// copied, and the attached connection - the one the client holds - is closed so
// the peer notices.
func (s *pendingSession) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closed = true
	closer := s.closer
	s.closer = nil
	s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
	}
	if closer != nil {
		return closer.Close()
	}
	return nil
}

// sessionCount reports how many sessions a key holds, for the concurrency
// limit. It is read inside sessionReservationMu by reserveSession, so the
// count it answers is the one the reservation is made against.
func sessionCount(tracker SessionTracker, key *keys.KeyState) int {
	if tracker == nil || key == nil || key.KeyID == "" {
		return 0
	}
	return tracker.Count(key.KeyID)
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

// requestSession is the registry handle of one HTTP request.
//
// The registry deals in io.Closer, and a request served over an HTTP keep-alive
// connection has no socket of its own to close: net/http owns the connection and
// reuses it for the next request. What belongs to this request alone is its
// context, so cancelling it is what "closing the session" means here - the
// upstream round trip is abandoned and the handler returns. Registering the
// request is what makes a revoked key, a disabled user or an exhausted
// allowance end a request that is already in flight, exactly as it ends a
// tunnel.
type requestSession struct {
	cancel context.CancelFunc
	once   sync.Once
}

func newRequestSession(cancel context.CancelFunc) *requestSession {
	if cancel == nil {
		return nil
	}
	return &requestSession{cancel: cancel}
}

// Close ends the request. It is idempotent: the registry may close a session
// that the handler is also unwinding, and the handler's own defer closes it on
// every exit path.
func (s *requestSession) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(s.cancel)
	return nil
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
