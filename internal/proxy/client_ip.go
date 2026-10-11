package proxy

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
)

// Client-key resolution for the proxy entry points.
//
// The management API derives its authentication-failure bucket from
// api.AuthFailureLimiter.ClientIP, which honours X-Forwarded-For when the peer
// is a configured trusted proxy. The proxy entry points (forward HTTP, CONNECT,
// reverse and the mux's path-token branch) must use the same rule: behind a NAT
// or a reverse proxy every client would otherwise share one bucket, and a
// single abusive client would rate limit the whole segment.
//
// PRISM-DEVIATION: the two implementations stay in their own packages because
// internal/proxy must not import internal/api (the API layer already imports
// the proxy layer in its tests, and a runtime dependency would invert the
// direction). TestProxyClientIPMatchesAuthFailureLimiter in internal/api pins
// the two to the same answers for the same inputs.

// trustedProxyPrefixes holds the parsed PRISM_TRUSTED_PROXIES list. It is read
// on every request, so it lives in an atomic pointer rather than a plain slice.
var trustedProxyPrefixes atomic.Pointer[[]netip.Prefix]

// SetTrustedProxies installs the trusted proxy prefixes (CIDRs and plain IP
// literals) used by ProxyClientIP. It is called once at startup with
// PRISM_TRUSTED_PROXIES; an empty list trusts no proxy, which keeps the
// upstream behaviour of never reading X-Forwarded-For.
//
// Invalid entries are ignored, exactly like api.parseTrustedProxies: a
// malformed value must never accidentally trust a whole range.
func SetTrustedProxies(values []string) {
	prefixes := parseTrustedProxyPrefixes(values)
	if len(prefixes) == 0 {
		trustedProxyPrefixes.Store(nil)
		return
	}
	trustedProxyPrefixes.Store(&prefixes)
}

// trustedProxies returns the installed prefixes, or nil when none are set.
func trustedProxies() []netip.Prefix {
	prefixes := trustedProxyPrefixes.Load()
	if prefixes == nil {
		return nil
	}
	return *prefixes
}

// parseTrustedProxyPrefixes parses CIDRs and plain IP literals into prefixes.
// Invalid values are ignored, which keeps the empty default "trust nothing".
func parseTrustedProxyPrefixes(values []string) []netip.Prefix {
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

// ProxyClientIP returns the client address of a proxy request.
//
// The peer address (RemoteAddr) is used unless it belongs to a trusted proxy,
// in which case the rightmost X-Forwarded-For hop that is not itself a trusted
// proxy is used. A header whose every hop is trusted, or that carries no usable
// address at all, falls back to the peer address: a client can therefore never
// choose its own bucket unless an operator listed its peer.
func ProxyClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	remote := clientHostOnly(r.RemoteAddr)
	if !isTrustedProxyAddress(remote, trustedProxies()) {
		return remote
	}
	if forwarded := lastUntrustedForwardedIP(r, trustedProxies()); forwarded != "" {
		return forwarded
	}
	return remote
}

// isTrustedProxyAddress reports whether host belongs to one of the prefixes.
func isTrustedProxyAddress(host string, prefixes []netip.Prefix) bool {
	if len(prefixes) == 0 || host == "" {
		return false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// lastUntrustedForwardedIP returns the rightmost X-Forwarded-For address that
// is not itself a trusted proxy. It returns "" when the header carries no
// usable address.
func lastUntrustedForwardedIP(r *http.Request, prefixes []netip.Prefix) string {
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
		for _, prefix := range prefixes {
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

// clientHostOnly returns the host part of a host:port listener address.
func clientHostOnly(addr string) string {
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(addr, "[]")
}
