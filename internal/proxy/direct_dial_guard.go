package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall"
)

// DirectDialGuard is the single address policy for every path where Prism
// dials a target from its own network namespace:
//
//   - the reverse proxy's local direct (bypass) branch,
//   - the forward HTTP proxy's local direct branch,
//   - the CONNECT tunnel's local direct branch,
//   - the SOCKS5 inbound's local direct branch.
//
// It implements PRISM_DIRECT_DENY_PRIVATE, which is opt-in: the zero value is
// disabled and every method is a no-op, so a build or a configuration that does
// not enable the switch keeps the upstream Resin behaviour (local direct
// targets are dialled with no address policy).
//
// When it is enabled the policy is applied twice, because a single check is not
// enough:
//
//  1. CheckTarget resolves the target name and refuses the request when any
//     resolved address is in the denied set. A name that cannot be resolved is
//     refused too: a guard that fails open is not a guard.
//  2. dialer() returns a net.Dialer whose ControlContext hook re-checks the
//     address the socket is about to be connected to. A resolver that answers
//     differently between the two calls (DNS rebinding) therefore cannot reach
//     a denied address either.
type DirectDialGuard struct {
	enabled bool
	// lookup resolves a hostname to every address it currently maps to. It is a
	// field so tests can pin resolution without touching the system resolver.
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

// NewDirectDialGuard returns the local dial policy for one switch value.
func NewDirectDialGuard(enabled bool) DirectDialGuard {
	return DirectDialGuard{enabled: enabled, lookup: defaultDirectLookup}
}

// Enabled reports whether the policy refuses addresses.
func (g DirectDialGuard) Enabled() bool { return g.enabled }

// withLookup returns a copy of the policy using a pinned resolver. Tests only.
func (g DirectDialGuard) withLookup(lookup func(context.Context, string) ([]netip.Addr, error)) DirectDialGuard {
	g.lookup = lookup
	return g
}

func defaultDirectLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

const directDenyPrivateHint = "PRISM_DIRECT_DENY_PRIVATE"

// errDirectDialDenied is returned when a local direct target is inside the
// denied address set.
var errDirectDialDenied = errors.New(
	"direct target denied: " + directDenyPrivateHint +
		" refuses loopback, private, link-local, CGNAT, reserved and cloud metadata addresses")

// errDirectDialUnresolvable is returned when the guard cannot classify a target
// name because it does not resolve. Failing closed is the whole point of the
// switch: an unresolved name could resolve to a denied address later.
var errDirectDialUnresolvable = errors.New(
	"direct target denied: " + directDenyPrivateHint + " is fail-closed and the target name did not resolve")

// CheckTarget applies the policy to a target host[:port] before it is dialled.
func (g DirectDialGuard) CheckTarget(ctx context.Context, host string) error {
	if !g.enabled {
		return nil
	}
	hostname := stripIPv6Zone(strings.TrimSpace(directTargetHostname(host)))
	if hostname == "" {
		return fmt.Errorf("%w: empty target host", errDirectDialDenied)
	}
	lower := strings.ToLower(hostname)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return fmt.Errorf("%w: %s", errDirectDialDenied, hostname)
	}
	if addr, err := netip.ParseAddr(hostname); err == nil {
		if isDeniedDirectAddr(addr) {
			return fmt.Errorf("%w: %s", errDirectDialDenied, hostname)
		}
		return nil
	}

	lookup := g.lookup
	if lookup == nil {
		lookup = defaultDirectLookup
	}
	if ctx == nil {
		ctx = context.Background()
	}
	addrs, err := lookup(ctx, hostname)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", errDirectDialUnresolvable, hostname, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%w: %s resolved to no address", errDirectDialUnresolvable, hostname)
	}
	for _, addr := range addrs {
		if isDeniedDirectAddr(addr) {
			return fmt.Errorf("%w: %s resolves to %s", errDirectDialDenied, hostname, addr)
		}
	}
	return nil
}

// dialer returns the dialer every local direct dial must use. With the policy
// disabled it is a plain net.Dialer, which keeps Resin behaviour.
func (g DirectDialGuard) dialer() *net.Dialer {
	dialer := &net.Dialer{}
	if !g.enabled {
		return dialer
	}
	// ControlContext runs after name resolution and immediately before the
	// connect, so the address that is actually connected to is the one that is
	// validated here, not a re-resolution done somewhere else.
	dialer.ControlContext = g.controlContext
	return dialer
}

// controlContext is the dial-time half of the policy.
func (g DirectDialGuard) controlContext(_ context.Context, _ string, address string, _ syscall.RawConn) error {
	if !g.enabled {
		return nil
	}
	host := stripIPv6Zone(strings.TrimSpace(directTargetHostname(address)))
	addr, err := netip.ParseAddr(host)
	if err != nil {
		// net.Dialer hands this hook resolved addresses only. A name reaching
		// it means the dial path skipped validation, so refuse.
		return fmt.Errorf("%w: unvalidated dial address %q", errDirectDialDenied, address)
	}
	if isDeniedDirectAddr(addr) {
		return fmt.Errorf("%w: %s", errDirectDialDenied, addr)
	}
	return nil
}

// directMetadataAddrs are the cloud metadata endpoints that must never be
// reached through a local direct dial while PRISM_DIRECT_DENY_PRIVATE is
// enabled. They overlap the prefix table below, and are kept explicit so the
// intent survives a change to that table.
var directMetadataAddrs = map[netip.Addr]bool{
	// AWS, Azure, GCP, DigitalOcean, Oracle and OpenStack metadata service.
	netip.MustParseAddr("169.254.169.254"): true,
	// Alibaba Cloud metadata service (inside CGNAT 100.64.0.0/10).
	netip.MustParseAddr("100.100.100.200"): true,
	// AWS IMDS over IPv6.
	netip.MustParseAddr("fd00:ec2::254"): true,
}

// deniedDirectPrefixes is the address set PRISM_DIRECT_DENY_PRIVATE refuses.
// Every entry names the class it covers; the list is the single source of truth
// for both the pre-dial check and the dial-time hook.
var deniedDirectPrefixes = []netip.Prefix{
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

// isDeniedDirectAddr reports whether addr is refused by the local direct
// policy. IPv4-mapped IPv6 addresses (::ffff:127.0.0.1) are classified as the
// IPv4 address they route to.
func isDeniedDirectAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		// An unclassifiable address is refused rather than allowed.
		return true
	}
	addr = addr.Unmap().WithZone("")
	if directMetadataAddrs[addr] {
		return true
	}
	for _, prefix := range deniedDirectPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// directTargetHostname strips an optional port from a host[:port] segment.
func directTargetHostname(host string) string {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(trimmed); err == nil {
		return strings.Trim(h, "[]")
	}
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		return trimmed[1 : len(trimmed)-1]
	}
	return trimmed
}
