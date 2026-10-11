package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// setTestTrustedProxies installs trusted proxy prefixes for one test and
// restores the previous value afterwards.
func setTestTrustedProxies(t *testing.T, trusted []string) {
	t.Helper()
	SetTrustedProxies(trusted)
	t.Cleanup(func() { SetTrustedProxies(nil) })
}

func newClientIPRequest(remoteAddr, xff string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	return req
}

// TestProxyClientIP_TrustedProxyUsesRightmostUntrustedHop pins the corrected
// behaviour: behind a listed reverse proxy every client must get its own limit
// bucket instead of sharing the proxy's address.
func TestProxyClientIP_TrustedProxyUsesRightmostUntrustedHop(t *testing.T) {
	setTestTrustedProxies(t, []string{"10.0.0.0/8", "127.0.0.1"})

	for _, tc := range []struct {
		name   string
		remote string
		xff    string
		want   string
	}{
		{"single client hop", "10.0.0.5:4000", "203.0.113.7", "203.0.113.7"},
		{"rightmost untrusted wins", "10.0.0.5:4000", "203.0.113.7, 198.51.100.9", "198.51.100.9"},
		{"trailing trusted hop is skipped", "10.0.0.5:4000", "203.0.113.7, 10.0.0.9", "203.0.113.7"},
		{"two trusted hops are skipped", "10.0.0.5:4000", "203.0.113.7, 10.0.0.9, 10.0.0.10", "203.0.113.7"},
		{"loopback literal is trusted", "127.0.0.1:4000", "198.51.100.9", "198.51.100.9"},
		{"ip:port hops are tolerated", "10.0.0.5:4000", "203.0.113.7:5555, 10.0.0.9:6666", "203.0.113.7"},
		{"ipv6 hop is tolerated", "10.0.0.5:4000", "[2001:db8::1]:443", "2001:db8::1"},
		{"ipv4-mapped ipv6 is unmapped", "10.0.0.5:4000", "::ffff:203.0.113.7", "203.0.113.7"},
		{"malformed hops are ignored", "10.0.0.5:4000", "garbage, 198.51.100.9", "198.51.100.9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProxyClientIP(newClientIPRequest(tc.remote, tc.xff)); got != tc.want {
				t.Fatalf("ProxyClientIP(%q, xff=%q) = %q, want %q", tc.remote, tc.xff, got, tc.want)
			}
		})
	}
}

// TestProxyClientIP_UntrustedPeerCannotForgeForwardedFor is the security half:
// a client that is not itself a trusted proxy must never be able to choose its
// own bucket by rotating the header.
func TestProxyClientIP_UntrustedPeerCannotForgeForwardedFor(t *testing.T) {
	setTestTrustedProxies(t, []string{"10.0.0.0/8"})

	req := newClientIPRequest("203.0.113.50:4000", "1.2.3.4")
	if got := ProxyClientIP(req); got != "203.0.113.50" {
		t.Fatalf("forged X-Forwarded-For changed the client key: got %q, want %q", got, "203.0.113.50")
	}
}

// TestProxyClientIP_FallsBackToPeerWhenNoHopIsUsable covers the remaining
// fallbacks: every hop trusted, header absent, header empty, header malformed.
func TestProxyClientIP_FallsBackToPeerWhenNoHopIsUsable(t *testing.T) {
	setTestTrustedProxies(t, []string{"10.0.0.0/8"})

	for _, tc := range []struct {
		name string
		xff  string
	}{
		{"no header", ""},
		{"empty header", "   "},
		{"all hops trusted", "10.0.0.9, 10.0.0.10"},
		{"only malformed hops", "not-an-ip, also-not-an-ip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProxyClientIP(newClientIPRequest("10.0.0.5:4000", tc.xff)); got != "10.0.0.5" {
				t.Fatalf("ProxyClientIP(xff=%q) = %q, want the peer address %q", tc.xff, got, "10.0.0.5")
			}
		})
	}
}

// TestProxyClientIP_NoTrustedProxiesConfiguredIgnoresHeader keeps the shipped
// default (PRISM_TRUSTED_PROXIES empty) unchanged.
func TestProxyClientIP_NoTrustedProxiesConfiguredIgnoresHeader(t *testing.T) {
	setTestTrustedProxies(t, nil)

	req := newClientIPRequest("203.0.113.50:4000", "1.2.3.4")
	if got := ProxyClientIP(req); got != "203.0.113.50" {
		t.Fatalf("with no trusted proxies configured: got %q, want %q", got, "203.0.113.50")
	}
}

// TestProxyClientIP_InvalidTrustedProxyEntriesAreIgnored matches the api-side
// parser: a malformed entry must not accidentally trust anyone.
func TestProxyClientIP_InvalidTrustedProxyEntriesAreIgnored(t *testing.T) {
	setTestTrustedProxies(t, []string{"", "   ", "not-a-cidr", "10.0.0.0/8"})

	req := newClientIPRequest("10.0.0.5:4000", "198.51.100.9")
	if got := ProxyClientIP(req); got != "198.51.100.9" {
		t.Fatalf("got %q, want %q", got, "198.51.100.9")
	}
	untrusted := newClientIPRequest("192.0.2.5:4000", "198.51.100.9")
	if got := ProxyClientIP(untrusted); got != "192.0.2.5" {
		t.Fatalf("an unparsable entry must not trust anyone: got %q, want %q", got, "192.0.2.5")
	}
}

// TestProxyClientIP_NilRequest covers the guard call sites that pass through a
// possibly nil request.
func TestProxyClientIP_NilRequest(t *testing.T) {
	setTestTrustedProxies(t, []string{"10.0.0.0/8"})
	if got := ProxyClientIP(nil); got != "" {
		t.Fatalf("ProxyClientIP(nil) = %q, want empty", got)
	}
}

// TestProxyClientIP_MultipleForwardedForHeadersAreMerged pins that repeated
// header lines are treated as one list, in order.
func TestProxyClientIP_MultipleForwardedForHeadersAreMerged(t *testing.T) {
	setTestTrustedProxies(t, []string{"10.0.0.0/8"})

	req := newClientIPRequest("10.0.0.5:4000", "")
	req.Header.Add("X-Forwarded-For", "203.0.113.7")
	req.Header.Add("X-Forwarded-For", "10.0.0.9")

	if got := ProxyClientIP(req); got != "203.0.113.7" {
		t.Fatalf("got %q, want %q", got, "203.0.113.7")
	}
}
