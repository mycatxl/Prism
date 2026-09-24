package proxy

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// AuthFailureGuard observes failed proxy authentication on the proxy entrypoints
// and can temporarily block an abusive client address.
//
// A nil guard disables proxy-entry failure limiting, which keeps the upstream
// Resin behaviour: 407/403 responses are never rate limited unless the operator
// sets PRISM_PROXY_AUTH_FAIL_LIMIT to a positive value.
type AuthFailureGuard interface {
	// Blocked reports whether the client is currently blocked and, when it is,
	// for how much longer.
	Blocked(clientIP string) (bool, time.Duration)
	// RecordFailure records one failed proxy authentication for the client.
	RecordFailure(clientIP string)
}

// ProxyClientIP returns the client address of a proxy request.
func ProxyClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

// guardProxyAuthFailure writes 429 RATE_LIMITED when the guard already blocks
// the client. It reports whether the request has been answered.
func guardProxyAuthFailure(w http.ResponseWriter, r *http.Request, guard AuthFailureGuard) bool {
	if guard == nil {
		return false
	}
	blocked, retryAfter := guard.Blocked(ProxyClientIP(r))
	if !blocked {
		return false
	}
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
	}
	writeProxyError(w, ErrRateLimited)
	return true
}

// recordProxyAuthFailure records one failed proxy authentication for the client.
func recordProxyAuthFailure(guard AuthFailureGuard, r *http.Request) {
	if guard == nil {
		return
	}
	guard.RecordFailure(ProxyClientIP(r))
}

// retryAfterSeconds converts a wait duration to the whole seconds used by the
// Retry-After header, always rounding up.
func retryAfterSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}
