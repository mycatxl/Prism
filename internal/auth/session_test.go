package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A session id is a bearer token, so it has to be unguessable: high entropy,
// unique, and of a predictable shape.
func TestNewSessionID_IsHighEntropyAndUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 2000; i++ {
		id, err := NewSessionID()
		if err != nil {
			t.Fatalf("NewSessionID: %v", err)
		}
		if seen[id] {
			t.Fatalf("a duplicate session id was generated: %q", id)
		}
		seen[id] = true

		// URL-safe and unpadded, so it survives a cookie and a header without
		// escaping and has no padding to strip.
		if strings.ContainsAny(id, "+/=") {
			t.Fatalf("the session id %q is not URL-safe or is padded", id)
		}
		if len(id) < 40 {
			t.Fatalf("the session id %q is only %d characters", id, len(id))
		}
	}
}

// The stored value must not be the id itself: a read of the sessions table must
// not yield a usable cookie.
func TestHashSessionID_IsStableAndNotTheInput(t *testing.T) {
	id, err := NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}

	first := HashSessionID(id)
	if first == id {
		t.Fatal("the stored hash is the session id itself")
	}
	if first != HashSessionID(id) {
		t.Fatal("hashing the same id twice gave different answers")
	}
	if len(first) != 64 {
		t.Fatalf("the hash is %d characters, want 64 hex characters", len(first))
	}
	for _, r := range first {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("the hash %q is not lowercase hex", first)
		}
	}

	// A different id gives a different hash, so a lookup cannot match the wrong
	// session.
	other, err := NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	if HashSessionID(other) == first {
		t.Fatal("two different session ids hashed to the same value")
	}

	if HashSessionID("") == first {
		t.Fatal("the empty id hashed to the same value as a real one")
	}
}

func TestNewCSRFToken_IsUniqueAndURLSafe(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		token, err := NewCSRFToken()
		if err != nil {
			t.Fatalf("NewCSRFToken: %v", err)
		}
		if seen[token] {
			t.Fatalf("a duplicate CSRF token was generated: %q", token)
		}
		seen[token] = true
		if strings.ContainsAny(token, "+/=") {
			t.Fatalf("the CSRF token %q is not URL-safe or is padded", token)
		}
	}
}

// The double-submit check is exact: no token, an empty token, or a different
// token all fail. Treating an absent token as agreement would remove the
// protection entirely.
func TestVerifyCSRFToken(t *testing.T) {
	issued, err := NewCSRFToken()
	if err != nil {
		t.Fatalf("NewCSRFToken: %v", err)
	}
	other, err := NewCSRFToken()
	if err != nil {
		t.Fatalf("NewCSRFToken: %v", err)
	}

	cases := []struct {
		name      string
		issued    string
		presented string
		want      bool
	}{
		{"the same token", issued, issued, true},
		{"a different token", issued, other, false},
		{"nothing presented", issued, "", false},
		{"nothing issued", "", issued, false},
		{"neither", "", "", false},
		{"a prefix of the token", issued, issued[:len(issued)-1], false},
		{"the token with a suffix", issued, issued + "x", false},
		{"a case change", issued, strings.ToUpper(issued), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VerifyCSRFToken(tc.issued, tc.presented); got != tc.want {
				t.Errorf("VerifyCSRFToken = %v, want %v", got, tc.want)
			}
		})
	}
}

// TLS always gets Secure; there is no configuration that turns it off.
func TestNewCookiePolicy_OverTLSIsAlwaysSecure(t *testing.T) {
	for _, host := range []string{"prism.example.com", "127.0.0.1", "localhost", "10.0.0.5"} {
		policy, err := NewCookiePolicy(true, host)
		if err != nil {
			t.Fatalf("NewCookiePolicy(over TLS, %q): %v", host, err)
		}
		if !policy.Secure {
			t.Errorf("over TLS to %q the cookie is not Secure", host)
		}
		if policy.SameSite != "Strict" {
			t.Errorf("over TLS to %q SameSite = %q, want Strict", host, policy.SameSite)
		}
	}
}

// Plain HTTP is allowed only where the traffic cannot leave the machine.
func TestNewCookiePolicy_PlainHTTPOnlyOnLoopback(t *testing.T) {
	allowed := []string{
		"127.0.0.1",
		"127.0.0.1:8080",
		"localhost",
		"localhost:8080",
		"::1",
		"[::1]:8080",
		"127.5.5.5", // the whole loopback range, not just 127.0.0.1
		"LOCALHOST",
	}
	for _, host := range allowed {
		policy, err := NewCookiePolicy(false, host)
		if err != nil {
			t.Errorf("plain HTTP to %q was refused: %v", host, err)
			continue
		}
		if policy.Secure {
			t.Errorf("plain HTTP to %q produced a Secure cookie, which the browser would then not send", host)
		}
	}

	refused := []string{
		"prism.example.com",
		"example.com:80",
		"10.0.0.5",
		"192.168.1.10",
		"0.0.0.0",
		"",
		"localhost.evil.com", // a name that merely starts with localhost
		"127.0.0.1.evil.com",
	}
	for _, host := range refused {
		if _, err := NewCookiePolicy(false, host); !errors.Is(err, ErrPlainHTTPNotAllowed) {
			t.Errorf("plain HTTP to %q: got %v, want ErrPlainHTTPNotAllowed", host, err)
		}
	}
}

// Sliding expiry: an expired session is never refreshed, and a session still in
// the first half of its window is left alone so a burst of requests does not
// become a burst of writes.
func TestSessionNeedsRefresh(t *testing.T) {
	now := time.Now().UnixNano()
	expiry := SessionExpiry(now)

	// Just issued: more than half the window is left.
	if SessionNeedsRefresh(expiry, now) {
		t.Error("a session in the first half of its window was refreshed")
	}

	// The first half has elapsed.
	late := now + int64(SessionTTL-SessionRefreshAfter) + 1
	if !SessionNeedsRefresh(expiry, late) {
		t.Error("a session in the second half of its window was not refreshed")
	}

	// Already expired: it needs deleting, not refreshing.
	if SessionNeedsRefresh(expiry, expiry) {
		t.Error("an expired session was refreshed")
	}
	if SessionNeedsRefresh(expiry, expiry+1) {
		t.Error("an expired session was refreshed")
	}
}

func TestSessionExpiry_UsesTheConfiguredTTL(t *testing.T) {
	now := int64(1_700_000_000_000_000_000)
	want := now + int64(SessionTTL)
	if got := SessionExpiry(now); got != want {
		t.Fatalf("SessionExpiry = %d, want %d", got, want)
	}
	if SessionTTL != 12*time.Hour {
		t.Fatalf("SessionTTL = %s, want 12h", SessionTTL)
	}
}

func TestNewSession_FillsTheRow(t *testing.T) {
	id, err := NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	now := time.Now().UnixNano()

	record, err := NewSession(id, "u-1", "203.0.113.7", "Mozilla/5.0", now)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if record.IDHash != HashSessionID(id) {
		t.Error("the stored hash is not the hash of the id")
	}
	if record.IDHash == id {
		t.Error("the row stores the id itself")
	}
	if record.UserID != "u-1" {
		t.Errorf("user = %q", record.UserID)
	}
	if record.CreatedAtNs != now {
		t.Errorf("created = %d, want %d", record.CreatedAtNs, now)
	}
	if record.ExpiresAtNs != SessionExpiry(now) {
		t.Errorf("expires = %d, want %d", record.ExpiresAtNs, SessionExpiry(now))
	}
	if record.IP != "203.0.113.7" {
		t.Errorf("ip = %q", record.IP)
	}
	if record.UA != "Mozilla/5.0" {
		t.Errorf("ua = %q", record.UA)
	}

	// A session without an id or without a user is a bug in the caller, not a
	// row to write.
	if _, err := NewSession("", "u-1", "", "", now); err == nil {
		t.Error("a session with no id was built")
	}
	if _, err := NewSession(id, "", "", "", now); err == nil {
		t.Error("a session with no user was built")
	}
}

// A client that sends a huge user-agent must not be able to grow the table.
func TestNewSession_BoundsTheUserAgent(t *testing.T) {
	id, err := NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}

	huge := strings.Repeat("a", 100_000)
	record, err := NewSession(id, "u-1", "", huge, time.Now().UnixNano())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if len(record.UA) != maxUserAgentBytes {
		t.Fatalf("the user-agent was stored at %d bytes, want it cut to %d", len(record.UA), maxUserAgentBytes)
	}
}

// The names the rest of the system depends on are part of the contract.
func TestSessionContractNames(t *testing.T) {
	if SessionCookieName != "prism_session" {
		t.Errorf("SessionCookieName = %q", SessionCookieName)
	}
	if CSRFCookieName != "prism_csrf" {
		t.Errorf("CSRFCookieName = %q", CSRFCookieName)
	}
	if CSRFHeaderName != "X-Prism-CSRF" {
		t.Errorf("CSRFHeaderName = %q", CSRFHeaderName)
	}
}

// A user-agent longer than the bound is cut on a rune boundary: the stored
// value has to stay valid UTF-8, because a session list renders it.
func TestTruncateUserAgent_KeepsValidUTF8(t *testing.T) {
	// Three bytes per character, so the byte bound lands inside a rune.
	ua := strings.Repeat("汉", maxUserAgentBytes)
	got := truncateUserAgent(ua)
	if len(got) > maxUserAgentBytes {
		t.Fatalf("truncated to %d bytes, want at most %d", len(got), maxUserAgentBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncation produced invalid UTF-8: %q", got)
	}
	if want := maxUserAgentBytes / 3 * 3; len(got) != want {
		t.Fatalf("kept %d bytes, want %d (the last whole rune)", len(got), want)
	}

	// A short string and one that fits exactly are untouched.
	if short := "curl/8.0"; truncateUserAgent(short) != short {
		t.Errorf("a short user-agent was modified")
	}
	exact := strings.Repeat("a", maxUserAgentBytes)
	if truncateUserAgent(exact) != exact {
		t.Errorf("a user-agent of exactly the bound was modified")
	}
}
