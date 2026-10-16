package api

import (
	"container/list"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Authentication failure limiting.
//
// Only failed authentications are counted: a client IP that accumulates
// authFailuresPerWindow failures within authFailureWindow is then blocked for
// authFailureBlock, and every management API request from that IP is answered
// with 429 RATE_LIMITED plus Retry-After. Successful requests never count.
const (
	authFailuresPerWindow = 10
	authFailureWindow     = time.Minute
	authFailureBlock      = 5 * time.Minute
	authFailureMaxEntries = 65536
)

// AuthFailureBlockDuration is how long a client stays blocked after exceeding
// the authentication failure threshold. It is exported so the proxy entrypoints
// reuse the same window as the management API.
const AuthFailureBlockDuration = authFailureBlock

type authFailureEntry struct {
	ip           string
	failures     int
	windowStart  time.Time
	blockedUntil time.Time
	element      *list.Element
}

// AuthFailureLimiter counts failed authentications per client IP and can
// temporarily block an abusive client. It is safe for concurrent use.
//
// The client key comes from the RemoteAddr host; X-Forwarded-For is honoured
// only when RemoteAddr itself belongs to one of the configured trusted proxy
// CIDRs, in which case the rightmost untrusted address in the header is used.
//
// Operational precondition: that rightmost untrusted hop
// is the observed client only when the trusted proxy *appends* the peer address
// it saw. A proxy that forwards a client-supplied header verbatim makes the key
// attacker-chosen, and a client can then rotate the header to get a fresh bucket
// for every guess. Malformed entries are ignored, and when every hop in the
// header is trusted the peer address itself is used.
type AuthFailureLimiter struct {
	mu          sync.Mutex
	maxFailures int
	window      time.Duration
	blockFor    time.Duration
	maxEntries  int
	now         func() time.Time
	trusted     []netip.Prefix
	entries     map[string]*authFailureEntry
	order       *list.List
}

// NewAuthFailureLimiter creates a limiter that blocks a client IP for blockFor
// once it records maxFailures failed authentications inside window.
//
// maxFailures <= 0 disables limiting (Blocked always reports false and
// RecordFailure does nothing). trustedProxies accepts CIDRs and plain IP
// literals; an empty list means X-Forwarded-For is never trusted.
func NewAuthFailureLimiter(
	maxFailures int,
	window, blockFor time.Duration,
	trustedProxies []string,
) *AuthFailureLimiter {
	if window <= 0 {
		window = authFailureWindow
	}
	if blockFor <= 0 {
		blockFor = authFailureBlock
	}
	return &AuthFailureLimiter{
		maxFailures: maxFailures,
		window:      window,
		blockFor:    blockFor,
		maxEntries:  authFailureMaxEntries,
		now:         time.Now,
		trusted:     parseTrustedProxies(trustedProxies),
		entries:     make(map[string]*authFailureEntry),
		order:       list.New(),
	}
}

// Enabled reports whether the limiter counts and blocks at all.
func (l *AuthFailureLimiter) Enabled() bool {
	return l != nil && l.maxFailures > 0
}

// ClientIP returns the rate-limit key for a request.
func (l *AuthFailureLimiter) ClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	remote := hostOnly(r.RemoteAddr)
	if l == nil || !l.isTrustedAddress(remote) {
		return remote
	}
	if forwarded := l.lastUntrustedForwardedIP(r); forwarded != "" {
		return forwarded
	}
	return remote
}

// IsSecureRequest reports whether a request reached the server over HTTPS as far
// as the end user is concerned.
//
// Prism does not terminate TLS itself - TLS is terminated in front of it - so
// r.TLS is nil on every request a production deployment serves and the only
// other evidence is a forwarded header any client can write. That header is
// therefore believed only when the request arrived directly from a configured
// trusted proxy, which keeps a deployment that sets no PRISM_TRUSTED_PROXIES on
// exactly the answer the connection itself gives.
//
// The last entry of X-Forwarded-Proto is the one read, for the same reason
// lastUntrustedForwardedIP takes the rightmost address: a proxy configured the
// way this list requires appends what it saw, so the entries to its left may be
// client-supplied.
func (l *AuthFailureLimiter) IsSecureRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	// isTrustedAddress is nil-safe: a server without a limiter trusts nothing.
	if !l.isTrustedAddress(hostOnly(r.RemoteAddr)) {
		return false
	}
	return forwardedProtoIsHTTPS(r)
}

// forwardedProtoIsHTTPS reports whether the nearest trusted hop said the request
// was made over HTTPS. A missing header, "http", or a chain ending in "http"
// all answer no.
func forwardedProtoIsHTTPS(r *http.Request) bool {
	scheme := ""
	for _, value := range r.Header.Values("X-Forwarded-Proto") {
		for _, part := range strings.Split(value, ",") {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				scheme = trimmed
			}
		}
	}
	return strings.EqualFold(scheme, "https")
}

// Blocked reports whether ip is currently blocked and, if so, for how long.
func (l *AuthFailureLimiter) Blocked(ip string) (bool, time.Duration) {
	if !l.Enabled() || ip == "" {
		return false, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.entries[ip]
	if !ok {
		return false, 0
	}
	now := l.now()
	if now.Before(entry.blockedUntil) {
		return true, entry.blockedUntil.Sub(now)
	}
	if now.Sub(entry.windowStart) >= l.window {
		// Both the window and the block expired: drop the entry so idle IPs do
		// not keep capacity away from active ones.
		l.remove(entry)
	}
	return false, 0
}

// RecordFailure records one failed authentication for ip.
func (l *AuthFailureLimiter) RecordFailure(ip string) {
	if !l.Enabled() || ip == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	entry, ok := l.entries[ip]
	if !ok {
		entry = l.newEntry(ip, now)
	} else {
		l.order.MoveToBack(entry.element)
		if now.Sub(entry.windowStart) >= l.window && now.After(entry.blockedUntil) {
			entry.failures = 0
			entry.windowStart = now
		}
	}
	entry.failures++
	if entry.failures >= l.maxFailures {
		entry.blockedUntil = now.Add(l.blockFor)
	}
}

// Reset clears the failure count and any block recorded for key.
//
// It exists for the account-dimension bucket the session service keeps beside
// the address one: a login that succeeds proves the caller is the account's
// owner, so failures counted against that account must not keep counting towards
// a lockout the owner would then have to sit out. It is a no-op for an unknown
// key, so clearing is idempotent.
func (l *AuthFailureLimiter) Reset(key string) {
	if l == nil || key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if entry, ok := l.entries[key]; ok {
		l.remove(entry)
	}
}

// TrustedProxies returns the configured trusted proxy prefixes, for diagnostics.
func (l *AuthFailureLimiter) TrustedProxies() []netip.Prefix {
	if l == nil {
		return nil
	}
	return append([]netip.Prefix(nil), l.trusted...)
}

func (l *AuthFailureLimiter) newEntry(ip string, now time.Time) *authFailureEntry {
	if l.maxEntries > 0 && len(l.entries) >= l.maxEntries {
		// The table is full: evict the oldest entry to bound memory usage.
		if oldest := l.order.Front(); oldest != nil {
			l.remove(oldest.Value.(*authFailureEntry))
		}
	}
	entry := &authFailureEntry{ip: ip, windowStart: now}
	entry.element = l.order.PushBack(entry)
	l.entries[ip] = entry
	return entry
}

func (l *AuthFailureLimiter) remove(entry *authFailureEntry) {
	delete(l.entries, entry.ip)
	if entry.element != nil {
		l.order.Remove(entry.element)
		entry.element = nil
	}
}

func (l *AuthFailureLimiter) isTrustedAddress(host string) bool {
	if l == nil || len(l.trusted) == 0 || host == "" {
		return false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range l.trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// lastUntrustedForwardedIP returns the rightmost X-Forwarded-For address that
// is not itself a trusted proxy. It returns "" when the header carries no
// usable address.
func (l *AuthFailureLimiter) lastUntrustedForwardedIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	var addrs []netip.Addr
	for _, value := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			// Tolerate "ip:port" and "[v6]:port" hops.
			host := part
			if h, _, err := net.SplitHostPort(part); err == nil {
				host = h
			}
			addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
			if err != nil {
				continue
			}
			addrs = append(addrs, addr)
		}
	}
	for i := len(addrs) - 1; i >= 0; i-- {
		addr := addrs[i]
		trusted := false
		for _, prefix := range l.trusted {
			if prefix.Contains(addr.Unmap()) {
				trusted = true
				break
			}
		}
		if !trusted {
			return addr.Unmap().String()
		}
	}
	return ""
}

// parseTrustedProxies parses CIDRs and plain IP literals into prefixes.
// Invalid values are ignored, which keeps the empty default "trust nothing".
func parseTrustedProxies(values []string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.Contains(value, "/") {
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				continue
			}
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(strings.Trim(value, "[]"))
		if err != nil {
			continue
		}
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return prefixes
}

// hostOnly returns the host part of a host:port listener address.
func hostOnly(addr string) string {
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(addr, "[]")
}

// retryAfterSeconds renders a wait duration as whole Retry-After seconds.
func retryAfterSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}

// NewLoginBlocklist builds the failure limiter that the login handler and the
// management middleware share.
//
// It is exported so a caller that constructs the session service - which counts
// its own failures - can hand the same limiter to both. Two limiters would mean
// an address could fail the session limit and still be admitted by the
// middleware, or the reverse.
func NewLoginBlocklist(trustedProxies []string) *AuthFailureLimiter {
	return NewAuthFailureLimiter(
		authFailuresPerWindow,
		authFailureWindow,
		authFailureBlock,
		trustedProxies,
	)
}

// limiterMiddleware refuses an address that has failed authentication too often.
//
// It is the blocking half of AuthMiddleware without the credential comparison,
// which moved into the identity middleware. A blocked address is refused before
// the password work and before the session lookup, so neither can be used to
// make the server do expensive things from an address that is already failing.
func limiterMiddleware(limiter *AuthFailureLimiter, next http.Handler) http.Handler {
	if limiter == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := limiter.ClientIP(r)
		if blocked, retryAfter := limiter.Blocked(clientIP); blocked {
			if retryAfter > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
			}
			WriteError(w, http.StatusTooManyRequests, "RATE_LIMITED",
				"too many failed authentication attempts; retry later")
			return
		}
		next.ServeHTTP(w, r)
	})
}
