// Package addrpolicy holds the single address policy shared by every component
// that decides whether a node target is acceptable.
//
// Two very different callers need the same answer, which is why it lives here
// rather than inside either of them:
//
//   - internal/publicsource screens nodes collected from public gists. Those
//     documents are untrusted input, so a target that names loopback, the LAN or
//     a cloud metadata endpoint must be rejected before the node is ever stored.
//   - internal/outbound screens a node before it builds a sing-box outbound. This
//     is the second gate, and the one that matters for nodes that entered the
//     pool through some other route (a hand-added subscription, a restored
//     state.db).
//
// The interesting case is a name that is not an IP literal but resolves to one.
// "127.0.0.1.nip.io" and friends (sslip.io, xip.io, traefik.me, localtest.me)
// are ordinary public hostnames by every lexical rule — the wildcard DNS is what
// makes them point at loopback. Only resolution can see that, which is what
// HostIsForbidden does and what HostIsForbiddenLexically deliberately does not.
package addrpolicy

import (
	"context"
	"net"
	"net/netip"
	"strings"
)

// forbiddenHostnames are names that never identify a public node target.
//
// A single-label name is not in this list because the lexical check rejects every
// single-label name: it resolves through the resolver's search domains, which is
// how a target ends up talking to whatever the host's local network happens to
// call "gateway".
var forbiddenHostnames = map[string]bool{
	"localhost":             true,
	"localhost.localdomain": true,
	"local":                 true,
	"intranet":              true,
	"internal":              true,
	"router":                true,
	"gateway":               true,
	"home":                  true,
	"nas":                   true,
	"printer":               true,
	"broadcasthost":         true,
	"ip6-allnodes":          true,
	"ip6-allrouters":        true,
}

// forbiddenHostSuffixes are the reserved or convention-private suffixes that
// never resolve to a public address.
var forbiddenHostSuffixes = []string{
	".localhost", ".local", ".localdomain", ".home", ".home.arpa",
	".lan", ".internal", ".intranet", ".corp", ".private",
}

// forbiddenPrefixes is the address set no node target may live in.
//
// Every entry names the class it covers; the list is the single source of truth
// for the lexical check, the resolver check and the dial-time check, so the three
// can never disagree.
var forbiddenPrefixes = []netip.Prefix{
	// IPv4.
	netip.MustParsePrefix("0.0.0.0/8"),      // RFC 1122 "this network"
	netip.MustParsePrefix("10.0.0.0/8"),     // RFC 1918 private
	netip.MustParsePrefix("100.64.0.0/10"),  // RFC 6598 shared address space (CGNAT)
	netip.MustParsePrefix("127.0.0.0/8"),    // RFC 1122 loopback
	netip.MustParsePrefix("169.254.0.0/16"), // RFC 3927 link-local
	netip.MustParsePrefix("172.16.0.0/12"),  // RFC 1918 private
	netip.MustParsePrefix("192.0.0.0/24"),   // RFC 6890 IETF protocol assignments
	netip.MustParsePrefix("192.168.0.0/16"), // RFC 1918 private
	netip.MustParsePrefix("198.18.0.0/15"),  // RFC 2544 benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),    // RFC 1112 reserved (incl. 255.255.255.255)

	// IPv6.
	netip.MustParsePrefix("::/128"),         // unspecified
	netip.MustParsePrefix("::1/128"),        // loopback
	netip.MustParsePrefix("64:ff9b::/96"),   // RFC 6052 NAT64
	netip.MustParsePrefix("64:ff9b:1::/48"), // RFC 8215 local-use NAT64
	netip.MustParsePrefix("fc00::/7"),       // RFC 4193 unique local
	netip.MustParsePrefix("fe80::/10"),      // RFC 4291 link-local unicast
	netip.MustParsePrefix("ff00::/8"),       // RFC 4291 multicast
}

// forbiddenAddrs are the cloud metadata endpoints that fall outside the prefix
// table. They are kept explicit so the intent survives a change to that table.
var forbiddenAddrs = map[netip.Addr]bool{
	// AWS, Azure, GCP, DigitalOcean, Oracle and OpenStack metadata service.
	netip.MustParseAddr("169.254.169.254"): true,
	// Alibaba Cloud metadata service (inside CGNAT 100.64.0.0/10).
	netip.MustParseAddr("100.100.100.200"): true,
	// AWS IMDS over IPv6.
	netip.MustParseAddr("fd00:ec2::254"): true,
}

// AddrIsForbidden reports whether addr is in the denied set. An invalid address
// is refused rather than allowed: an unclassifiable address is not a safe one.
func AddrIsForbidden(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.Unmap().WithZone("")
	if forbiddenAddrs[addr] {
		return true
	}
	for _, prefix := range forbiddenPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// fakeIPPrefix is the RFC 2544 benchmarking range that transparent-proxy
// resolvers hand out as their fake-IP pool (Clash, sing-box and friends all
// default to 198.18.0.0/15). It matters here because a host that resolves into
// this range is not necessarily pointing at a private network: it is what the
// local resolver answers for *every* proxied name, so treating it as a denied
// address would reject every node on a machine that runs a transparent proxy.
var fakeIPPrefix = netip.MustParsePrefix("198.18.0.0/15")

// AddrIsFakeIP reports whether addr is inside the fake-IP pool range.
func AddrIsFakeIP(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	return fakeIPPrefix.Contains(addr.Unmap().WithZone(""))
}

// HostIsForbiddenLexically reports whether host is refused by inspection alone.
//
// It is deliberately conservative in both directions. It rejects every spelling
// of an address that inet_aton accepts but net.ParseIP does not ("2130706433",
// "127.1", "0x7f.0.0.1"), because those resolve to loopback while looking like
// ordinary hostnames. It accepts a name that resolves to a private address, which
// is exactly the nip.io case that HostIsForbidden exists to catch.
func HostIsForbiddenLexically(host string) bool {
	host = NormalizeHost(host)
	if host == "" {
		return true
	}
	if forbiddenHostnames[host] {
		return true
	}
	for _, suffix := range forbiddenHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	// A public IP literal is a valid target and is classified here.
	if addr, err := netip.ParseAddr(host); err == nil {
		return AddrIsForbidden(addr)
	}
	if strings.ContainsAny(host, " \t\r\n/@\\") {
		return true
	}
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return true
	}
	labels := strings.Split(host, ".")
	// A single-label name resolves through the resolver's search domains.
	if len(labels) < 2 {
		return true
	}
	numericLabels := 0
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return true
		}
		// "0x7f.0.0.1" is another inet_aton spelling of 127.0.0.1.
		if strings.HasPrefix(label, "0x") {
			return true
		}
		for _, r := range label {
			if !(r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
				return true
			}
		}
		if !strings.ContainsFunc(label, func(r rune) bool { return r < '0' || r > '9' }) {
			numericLabels++
		}
	}
	// Every label numeric: an IPv4 address written in a form net.ParseIP rejects.
	return numericLabels == len(labels)
}

// LookupFunc resolves a hostname to every address it currently maps to.
type LookupFunc func(ctx context.Context, host string) ([]netip.Addr, error)

// HostIsForbidden reports whether host is refused after resolution.
//
// A name that cannot be resolved is refused: a check that fails open is not a
// check, and an unresolved name may resolve to a denied address a moment later.
// The resolved addresses are classified with AddrIsForbidden, which is what
// catches the wildcard-DNS services ("127.0.0.1.nip.io") that no lexical rule
// can see.
//
// The caller supplies the resolver so tests can pin resolution without touching
// the system resolver, and so callers can share one resolver implementation.
func HostIsForbidden(ctx context.Context, host string, lookup LookupFunc) bool {
	if HostIsForbiddenLexically(host) {
		return true
	}
	normalized := NormalizeHost(host)
	// A literal was already classified lexically.
	if _, err := netip.ParseAddr(normalized); err == nil {
		return false
	}
	if lookup == nil {
		lookup = DefaultLookup
	}
	if ctx == nil {
		ctx = context.Background()
	}
	addrs, err := lookup(ctx, normalized)
	if err != nil || len(addrs) == 0 {
		return true
	}
	for _, addr := range addrs {
		if AddrIsForbidden(addr) {
			return true
		}
	}
	return false
}

// DefaultLookup resolves through the system resolver.
func DefaultLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// NormalizeHost lowercases a host, trims surrounding space and a single trailing
// dot, and strips an optional port or IPv6 brackets.
func NormalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	host = strings.ToLower(host)
	// A fully-qualified name may carry the root label's trailing dot.
	return strings.TrimSuffix(host, ".")
}

// StripZone removes an IPv6 zone identifier ("fe80::1%eth0" -> "fe80::1").
func StripZone(host string) string {
	if idx := strings.LastIndexByte(host, '%'); idx >= 0 {
		return host[:idx]
	}
	return host
}

// NodeTargetIsForbidden reports whether a node's server host is an unacceptable
// dial target, and returns a short reason when it is.
//
// This is the node-admission form of the policy: it is applied to the `server`
// of a node before Prism builds an outbound for it. It differs from
// HostIsForbidden in one deliberate way, which is the reason it exists:
//
// A machine that runs a transparent proxy (Clash, sing-box, mihomo, Surge) in
// fake-IP mode answers *every* proxied name with an address from 198.18.0.0/15.
// Those answers say nothing about where the name really points, so treating them
// as evidence of a private target would refuse every node on such a machine.
// When resolution yields only fake-IP addresses the check is therefore
// inconclusive and the target is allowed: on that machine a dial to such a name
// goes through the local proxy's routing space, not to the address it names.
//
// Every other case is fail-closed: a name that does not resolve is refused,
// because a node that cannot be resolved cannot be dialled either, and an
// unresolved name may resolve to a denied address a moment later.
func NodeTargetIsForbidden(ctx context.Context, host string, lookup LookupFunc) (bool, string) {
	if host == "" {
		return true, "empty server host"
	}
	if HostIsForbiddenLexically(host) {
		return true, "server is a loopback, private, link-local or metadata address"
	}
	normalized := NormalizeHost(host)
	// A public literal was already classified lexically.
	if _, err := netip.ParseAddr(normalized); err == nil {
		return false, ""
	}
	if lookup == nil {
		lookup = DefaultLookup
	}
	if ctx == nil {
		ctx = context.Background()
	}
	addrs, err := lookup(ctx, normalized)
	if err != nil {
		return true, "server name does not resolve"
	}
	if len(addrs) == 0 {
		return true, "server name resolves to no address"
	}
	real := 0
	for _, addr := range addrs {
		if AddrIsFakeIP(addr) {
			continue
		}
		real++
		if AddrIsForbidden(addr) {
			return true, "server name resolves to a loopback, private, link-local or metadata address"
		}
	}
	if real == 0 {
		// Only fake-IP answers: the local resolver is intercepting the name, so
		// this check cannot classify it. See the doc comment.
		return false, ""
	}
	return false, ""
}
