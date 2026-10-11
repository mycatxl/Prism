package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"prism/internal/proxy"
)

// TestProxyClientIPMatchesAuthFailureLimiter pins the proxy entry points to the
// management API's client-key semantics. The two implementations
// live in different packages, so a divergence would silently split the same
// client across two buckets — or worse, let a proxy-entry attacker rotate
// X-Forwarded-For when the API side would refuse to honour it.
//
// The cases below are exactly the ones wp04_security_test.go pins for the API
// side; both resolvers must answer identically for every one of them.
func TestProxyClientIPMatchesAuthFailureLimiter(t *testing.T) {
	trusted := []string{"10.0.0.0/8", "127.0.0.1/32", "::1"}

	for _, tc := range []struct {
		name   string
		remote string
		xff    string
	}{
		{"untrusted peer, no header", "192.0.2.20:4000", ""},
		{"untrusted peer with forged header", "192.0.2.20:4000", "203.0.113.7"},
		{"trusted peer, single hop", "10.1.2.3:4000", "198.51.100.7"},
		{"trusted peer, chained hops", "10.9.9.9:1", "198.51.100.9, 10.4.4.4"},
		{"trusted loopback peer", "127.0.0.1:4000", "198.51.100.4"},
		{"all hops trusted", "10.1.2.3:4000", "10.4.4.4, 10.5.5.5"},
		{"malformed hops only", "10.1.2.3:4000", "nope, also-nope"},
		{"mixed malformed and valid", "10.1.2.3:4000", "nope, 198.51.100.9"},
		{"hop with port", "10.1.2.3:4000", "198.51.100.9:1234, 10.4.4.4:99"},
		{"ipv6 hop", "10.1.2.3:4000", "[2001:db8::1]:443"},
		{"ipv4-mapped hop", "10.1.2.3:4000", "::ffff:198.51.100.9"},
		{"bare ipv6 peer", "2001:db8::5", "198.51.100.9"},
		{"empty header", "10.1.2.3:4000", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiIP := clientIPFromRequest(t, tc.remote, tc.xff, trusted)
			proxyIP := clientIPFromProxy(t, tc.remote, tc.xff, trusted)
			if apiIP != proxyIP {
				t.Fatalf("client IP diverged: api.AuthFailureLimiter.ClientIP = %q, proxy.ProxyClientIP = %q (remote=%q xff=%q)",
					apiIP, proxyIP, tc.remote, tc.xff)
			}
		})
	}
}

// clientIPFromRequest resolves the key through the management-API limiter.
func clientIPFromRequest(t *testing.T, remote, xff string, trusted []string) string {
	t.Helper()
	limiter := NewAuthFailureLimiter(10, time.Minute, 5*time.Minute, trusted)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	req.RemoteAddr = remote
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	return limiter.ClientIP(req)
}

// clientIPFromProxy resolves the same key through the proxy entry point.
func clientIPFromProxy(t *testing.T, remote, xff string, trusted []string) string {
	t.Helper()
	proxy.SetTrustedProxies(trusted)
	t.Cleanup(func() { proxy.SetTrustedProxies(nil) })
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.RemoteAddr = remote
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	return proxy.ProxyClientIP(req)
}
