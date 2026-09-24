package main

import (
	"context"
	"net"
	"strings"

	"prism/internal/proxy"
)

// Proxy-entry authentication failure limiting in cmd/prism (WP04 §4.6).
//
// internal/proxy meters the credential checks it owns itself (the forward and
// CONNECT Proxy-Authorization check and the reverse-proxy path token: see
// forward.go, reverse.go and auth_guard.go). Two entry points are assembled
// here instead, so they are metered here:
//
//   - the reverse-proxy path token as decided by the inbound mux before the
//     reverse handler runs (inbound_mux.go), and
//   - the SOCKS5 username/password check, which happens on a raw TCP session
//     inside proxy.Socks5Inbound and therefore has no http.ResponseWriter to
//     answer a 429 on.
//
// Everything else is deliberate: an empty proxy token disables proxy
// authentication by configuration and has nothing to meter, and the /sub/{token}
// subscription endpoint keeps its own per-token and per-IP limiter (it is not a
// PRISM_PROXY_AUTH_FAIL_LIMIT surface).

// socks5AuthObserver byte values of the two SOCKS5 rejection replies it watches
// for. They mirror the constants of internal/proxy, which are unexported there.
const (
	socks5WatchVersion          = 0x05
	socks5WatchMethodUserPass   = 0x02
	socks5WatchMethodNoAuth     = 0x00
	socks5WatchUserPassVersion  = 0x01
	socks5WatchUserPassRejected = 0x01
	socks5NoAcceptableMethods   = 0xFF
)

// socks5AuthWatchPhase tracks how far the observed SOCKS5 handshake got.
type socks5AuthWatchPhase uint8

const (
	// socks5WatchMethodReply is the initial phase: the next two bytes the
	// server writes are the RFC 1928 method-selection reply.
	socks5WatchMethodReply socks5AuthWatchPhase = iota
	// socks5WatchUserPassReply is the phase after the server selected
	// username/password authentication: the next two bytes are the RFC 1929
	// reply.
	socks5WatchUserPassReply
	// socks5WatchDone is terminal: either the client authenticated, chose
	// no-auth, or was already counted.
	socks5WatchDone
)

// newSocks5AuthGuard wraps a SOCKS5 inbound so that failed authentications are
// counted by the same limiter the HTTP entries use, and a blocked client is
// refused before any credential is looked at.
//
// A nil guard returns next unchanged, which keeps the documented default
// untouched: PRISM_PROXY_AUTH_FAIL_LIMIT=0 never changes what the SOCKS5 entry
// answers.
func newSocks5AuthGuard(guard proxy.AuthFailureGuard, next inboundConnHandler) inboundConnHandler {
	if guard == nil || next == nil {
		return next
	}
	return &socks5AuthGuard{guard: guard, next: next}
}

type socks5AuthGuard struct {
	guard proxy.AuthFailureGuard
	next  inboundConnHandler
}

// ServeConnContext refuses a blocked client with the RFC 1928 "no acceptable
// methods" reply. A raw SOCKS5 session has no status line and no headers, so
// the 429 the HTTP entries answer is a `05 FF` method-selection reply followed
// by the close, the same shape the endpoint capability gate uses.
func (g *socks5AuthGuard) ServeConnContext(ctx context.Context, conn net.Conn) {
	if conn == nil {
		return
	}
	clientIP := inboundConnClientIP(conn)
	if blocked, _ := g.guard.Blocked(clientIP); blocked {
		_, _ = conn.Write([]byte{socks5WatchVersion, socks5NoAcceptableMethods})
		_ = conn.Close()
		return
	}
	g.next.ServeConnContext(ctx, &socks5AuthObserver{
		Conn:     conn,
		clientIP: clientIP,
		guard:    g.guard,
	})
}

// socks5AuthObserver watches the two handshake replies the SOCKS5 inbound
// writes to the client and records one failed authentication per rejection.
//
// It is deliberately narrow: only the two reply pairs the RFCs define are
// interpreted, and every other byte pattern stops the observation instead of
// being guessed at. The SOCKS5 inbound writes exactly two bytes per reply, but
// partial writes are tolerated by buffering up to one reply pair.
type socks5AuthObserver struct {
	net.Conn
	clientIP string
	guard    proxy.AuthFailureGuard
	phase    socks5AuthWatchPhase
	pending  [2]byte
	held     int
}

// Write forwards the server's bytes to the client and observes the handshake
// replies on the way out.
func (o *socks5AuthObserver) Write(p []byte) (int, error) {
	n, err := o.Conn.Write(p)
	if n > 0 {
		o.observe(p[:n])
	}
	return n, err
}

func (o *socks5AuthObserver) observe(p []byte) {
	if o.phase == socks5WatchDone {
		return
	}
	for _, b := range p {
		o.pending[o.held] = b
		o.held++
		if o.held < 2 {
			continue
		}
		first := o.pending[0]
		second := o.pending[1]
		o.held = 0
		o.consumeReply(first, second)
		if o.phase == socks5WatchDone {
			return
		}
	}
}

func (o *socks5AuthObserver) consumeReply(first, second byte) {
	switch o.phase {
	case socks5WatchMethodReply:
		if first != socks5WatchVersion {
			// Not a SOCKS5 method reply: stop watching rather than guess.
			o.phase = socks5WatchDone
			return
		}
		switch second {
		case socks5WatchMethodUserPass:
			o.phase = socks5WatchUserPassReply
		case socks5WatchMethodNoAuth:
			// The client selected the no-auth method: no credential check
			// happened, so there is nothing to count.
			o.phase = socks5WatchDone
		default:
			// 0xFF (no acceptable methods) or an unknown method: the server
			// refused the client's authentication attempt.
			o.phase = socks5WatchDone
			o.recordFailure()
		}
	case socks5WatchUserPassReply:
		o.phase = socks5WatchDone
		if first != socks5WatchUserPassVersion {
			// Not the RFC 1929 reply: nothing to interpret.
			return
		}
		if second != socks5WatchUserPassRejected {
			return
		}
		o.recordFailure()
	}
}

func (o *socks5AuthObserver) recordFailure() {
	if o.guard == nil {
		return
	}
	o.guard.RecordFailure(o.clientIP)
}

// inboundConnClientIP is the rate-limit key of a raw proxy connection.
func inboundConnClientIP(conn net.Conn) string {
	if conn == nil {
		return ""
	}
	addr := conn.RemoteAddr()
	if addr == nil {
		return ""
	}
	host := addr.String()
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}
