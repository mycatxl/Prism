package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"
)

// Session parameters.
const (
	// SessionTTL is how long a session lasts, measured from its last use.
	//
	// Twelve hours is a working day: long enough that an operator is not asked
	// to log in again after lunch, short enough that a stolen cookie from a
	// laptop left open overnight is worthless by morning.
	SessionTTL = 12 * time.Hour
	// SessionRefreshAfter is how much of the TTL must be spent before the
	// expiry is pushed back.
	//
	// Sliding expiry could be implemented by writing on every request, but that
	// turns a read-only page view into a database write. Refreshing only in the
	// second half of the window keeps the session alive for someone who is
	// using it while collapsing a burst of requests into at most one write per
	// half-window.
	SessionRefreshAfter = SessionTTL / 2

	// SessionIDBytes is the entropy of a session id. 32 bytes is 256 bits: the
	// id is a bearer token, so guessing one has to be infeasible rather than
	// merely unlikely.
	SessionIDBytes = 32
	// CSRFTokenBytes is the entropy of a CSRF token.
	CSRFTokenBytes = 32

	// SessionCookieName carries the session id.
	SessionCookieName = "prism_session"
	// CSRFCookieName carries the double-submit token. It is deliberately not
	// HttpOnly: the page has to read it to echo it back in the header, which is
	// the whole mechanism.
	CSRFCookieName = "prism_csrf"
	// CSRFHeaderName is where the page echoes the token.
	CSRFHeaderName = "X-Prism-CSRF"
)

// NewSessionID returns a fresh session id.
//
// It is base64url without padding so it is safe in a cookie and in a URL
// without escaping, and so its length is exactly predictable.
func NewSessionID() (string, error) {
	buf := make([]byte, SessionIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: generate session id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashSessionID returns the value stored for a session id.
//
// The database holds only this, so a read of the sessions table does not yield
// a usable cookie. A plain SHA-256 is right here, unlike for a password: the id
// is already 256 bits of randomness, so there is nothing to brute-force and no
// reason to pay for a slow KDF on every request.
func HashSessionID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// NewCSRFToken returns a fresh double-submit token.
func NewCSRFToken() (string, error) {
	buf := make([]byte, CSRFTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: generate CSRF token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// VerifyCSRFToken reports whether the presented token matches the issued one.
//
// The comparison is constant time so the check cannot be turned into a byte-at-
// a-time oracle. An empty token on either side never matches: a session that was
// issued without one, or a request that sent none, must not be treated as
// agreeing by accident.
func VerifyCSRFToken(issued, presented string) bool {
	if issued == "" || presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(issued), []byte(presented)) == 1
}

// CookiePolicy is how a session cookie must be set for a given request.
type CookiePolicy struct {
	// Secure requires HTTPS to send the cookie back.
	Secure bool
	// SameSite is always Strict: a console has no cross-site flows, so a
	// browser is never expected to send the cookie on one.
	SameSite string
}

// ErrPlainHTTPNotAllowed is returned when a console would be served over plain
// HTTP on a host that is not loopback.
//
// The cookie would be readable by anyone on the path, so issuing one is worse
// than refusing: the operator would believe they had logged in securely. A
// loopback address is exempt because the traffic never reaches a network, which
// is what makes local development and the smoke test work at all.
var ErrPlainHTTPNotAllowed = fmt.Errorf("auth: refusing to issue a session cookie over plain HTTP")

// NewCookiePolicy decides how the session cookie may be set for a request.
//
// isTLS says whether the connection reached the server over TLS - the server
// knows this directly, so nothing here trusts a forwarded header.
func NewCookiePolicy(isTLS bool, host string) (CookiePolicy, error) {
	if isTLS {
		return CookiePolicy{Secure: true, SameSite: "Strict"}, nil
	}
	if PlainHTTPAllowed(host) {
		// Loopback: no network, so nothing to eavesdrop on. Secure has to be
		// off here, because a browser will not send a Secure cookie over HTTP
		// and the session would appear to work and then vanish.
		return CookiePolicy{Secure: false, SameSite: "Strict"}, nil
	}
	return CookiePolicy{}, ErrPlainHTTPNotAllowed
}

// PlainHTTPAllowed reports whether a host is reachable only by the machine
// itself, which is the only case where a session may be issued without TLS.
func PlainHTTPAllowed(host string) bool {
	// Strip the port, and the brackets around an IPv6 literal.
	name := strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(name); err == nil {
		name = h
	}
	name = strings.Trim(name, "[]")
	if name == "" {
		return false
	}

	lower := strings.ToLower(name)
	// "localhost" is in the list explicitly rather than being resolved: the
	// name could be pointed elsewhere by a hosts file, and resolving it here
	// would make this decision depend on the machine's DNS.
	if lower == "localhost" {
		return true
	}

	addr, err := netip.ParseAddr(name)
	if err != nil {
		return false
	}
	return addr.IsLoopback()
}

// SessionExpiry returns the expiry a session created or refreshed at nowNs gets.
func SessionExpiry(nowNs int64) int64 {
	return nowNs + int64(SessionTTL)
}

// SessionNeedsRefresh reports whether a session should have its expiry pushed
// back at nowNs.
//
// An expired session never needs refreshing - it needs deleting - and a session
// that is still in the first half of its window is left alone so that a burst of
// requests does not become a burst of writes.
func SessionNeedsRefresh(expiresAtNs, nowNs int64) bool {
	if expiresAtNs <= nowNs {
		return false
	}
	return nowNs >= expiresAtNs-int64(SessionTTL-SessionRefreshAfter)
}

// NewSession builds the row for a freshly authenticated user.
func NewSession(id, userID, ip, ua string, nowNs int64) (SessionRecord, error) {
	if id == "" || userID == "" {
		return SessionRecord{}, fmt.Errorf("auth: session needs an id and a user")
	}
	return SessionRecord{
		IDHash:      HashSessionID(id),
		UserID:      userID,
		CreatedAtNs: nowNs,
		ExpiresAtNs: SessionExpiry(nowNs),
		IP:          ip,
		UA:          truncateUserAgent(ua),
	}, nil
}

// SessionRecord is the row a session is stored as.
//
// It mirrors the sessions table without importing the model package, so the auth
// package keeps depending on nothing but the standard library.
type SessionRecord struct {
	IDHash      string
	UserID      string
	CreatedAtNs int64
	ExpiresAtNs int64
	IP          string
	UA          string
}

// maxUserAgentBytes bounds the stored user-agent string.
//
// It is recorded for an operator to recognise a session, not to be parsed, so a
// client sending a megabyte of header should not be able to grow the table.
const maxUserAgentBytes = 256

// truncateUserAgent bounds a user-agent string on a rune boundary.
//
// Cutting at a byte offset would split a multi-byte character and store an
// invalid string that no reader can render, so the cut backs off to the start
// of the rune it landed in.
func truncateUserAgent(ua string) string {
	if len(ua) <= maxUserAgentBytes {
		return ua
	}
	cut := maxUserAgentBytes
	for cut > 0 && !utf8.RuneStart(ua[cut]) {
		cut--
	}
	return ua[:cut]
}
