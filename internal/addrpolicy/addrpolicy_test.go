package addrpolicy

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

// stubLookup returns a pinned resolver so the resolution-dependent rules can be
// tested without touching the system resolver.
func stubLookup(table map[string][]string, err error) LookupFunc {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		if err != nil {
			return nil, err
		}
		raw, ok := table[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		addrs := make([]netip.Addr, 0, len(raw))
		for _, item := range raw {
			addrs = append(addrs, netip.MustParseAddr(item))
		}
		return addrs, nil
	}
}

// TestAddrIsForbidden pins the address table itself, including the classes that
// net.IP's own helpers do not classify as private or link-local.
func TestAddrIsForbidden(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		// Allowed: ordinary public addresses.
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"203.0.113.9", false},
		{"2606:4700:4700::1111", false},
		{"2001:4860:4860::8888", false},

		// IPv4 denied classes.
		{"0.0.0.0", true},         // "this network"
		{"0.1.2.3", true},         // inside 0.0.0.0/8
		{"10.0.0.1", true},        // RFC 1918
		{"100.64.0.1", true},      // CGNAT
		{"100.127.255.255", true}, // CGNAT upper edge
		{"127.0.0.1", true},       // loopback
		{"127.1.2.3", true},       // inside 127.0.0.0/8
		{"169.254.1.1", true},     // link-local
		{"172.16.0.1", true},      // RFC 1918
		{"172.31.255.255", true},  // RFC 1918 upper edge
		{"192.0.0.1", true},       // IETF protocol assignments
		{"192.168.1.1", true},     // RFC 1918
		{"198.18.0.1", true},      // benchmarking (also the fake-IP pool)
		{"240.0.0.1", true},       // reserved
		{"255.255.255.255", true}, // broadcast
		{"169.254.169.254", true}, // AWS/Azure/GCP metadata
		{"100.100.100.200", true}, // Alibaba Cloud metadata

		// IPv6 denied classes.
		{"::", true},              // unspecified
		{"::1", true},             // loopback
		{"64:ff9b::7f00:1", true}, // NAT64 wrapping 127.0.0.1
		{"64:ff9b:1::1", true},    // local-use NAT64
		{"fc00::1", true},         // unique local
		{"fd00:ec2::254", true},   // AWS IMDS over IPv6
		{"fe80::1", true},         // link-local
		{"ff02::1", true},         // multicast

		// IPv4-mapped forms are classified as the IPv4 address they route to.
		{"::ffff:127.0.0.1", true},
		{"::ffff:8.8.8.8", false},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			addr := netip.MustParseAddr(tc.addr)
			if got := AddrIsForbidden(addr); got != tc.want {
				t.Fatalf("AddrIsForbidden(%s) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}

	// An invalid address is refused rather than allowed.
	if !AddrIsForbidden(netip.Addr{}) {
		t.Error("AddrIsForbidden(invalid) = false; an unclassifiable address must be refused")
	}
}

// TestHostIsForbiddenLexically pins the spelling rules: everything that resolves
// to loopback while looking like an ordinary hostname.
func TestHostIsForbiddenLexically(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		// Ordinary public targets.
		{"example.com", false},
		{"sub.example.com", false},
		{"my-node.example.co.jp", false},
		{"8.8.8.8", false},
		{"2606:4700:4700::1111", false},
		{"Example.COM", false},   // case folded
		{"example.com.", false},  // root label trimmed
		{" example.com ", false}, // trimmed
		{"example.com:443", false},
		{"[2606:4700::1111]:443", false},

		// Names that never identify a public target.
		{"localhost", true},
		{"LOCALHOST", true},
		{"localhost.localdomain", true},
		{"gateway", true}, // single label
		{"router", true},
		{"nas", true},
		{"printer", true},
		{"foo.local", true},
		{"foo.localhost", true},
		{"printer.lan", true},
		{"db.internal", true},
		{"host.home.arpa", true},
		{"a.corp", true},

		// inet_aton spellings of loopback that net.ParseIP rejects.
		{"2130706433", true},
		{"127.1", true},
		{"0x7f.0.0.1", true},
		{"0x7f000001", true},
		{"0177.0.0.1", true},

		// Literal addresses in the denied set.
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true},
		{"::1", true},
		{"fe80::1", true},

		// Malformed or dangerous shapes.
		{"", true},
		{"   ", true},
		{"a/b", true},
		{"user@host.example.com", true},
		{"host name.example.com", true},
		{".example.com", true},
		{"-example.com", true},
		{"example-.com", true},
		{"exa mple.com", true},

		// The wildcard-DNS services are NOT caught here: they are ordinary public
		// hostnames. Only resolution can see where they point, which is what
		// TestNodeTargetIsForbidden_CatchesWildcardDNS pins.
		{"127.0.0.1.nip.io", false},
		{"10.0.0.1.sslip.io", false},
	} {
		t.Run(tc.host, func(t *testing.T) {
			if got := HostIsForbiddenLexically(tc.host); got != tc.want {
				t.Fatalf("HostIsForbiddenLexically(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

// TestNodeTargetIsForbidden_CatchesWildcardDNS pins the case the whole resolver
// pass exists for: a public hostname whose DNS answer is loopback.
func TestNodeTargetIsForbidden_CatchesWildcardDNS(t *testing.T) {
	for _, tc := range []struct {
		host string
		addr string
		want bool
	}{
		// The wildcard-DNS services: an ordinary-looking name, a private answer.
		{"127.0.0.1.nip.io", "127.0.0.1", true},
		{"10.0.0.1.nip.io", "10.0.0.1", true},
		{"192.168.1.1.nip.io", "192.168.1.1", true},
		{"169.254.169.254.nip.io", "169.254.169.254", true},
		{"10.0.0.1.sslip.io", "10.0.0.1", true},
		{"127.0.0.1.xip.io", "127.0.0.1", true},
		{"172.16.0.1.traefik.me", "172.16.0.1", true},
		{"127.0.0.1.localtest.me", "127.0.0.1", true},

		// A name that resolves to both a public and a private address is refused:
		// the dialer may pick the private one.
		{"dual.example.com", "8.8.8.8", true},

		// Ordinary public names stay allowed.
		{"node.example.com", "203.0.113.9", false},
		{"v6.example.com", "2606:4700::1111", false},
	} {
		t.Run(tc.host, func(t *testing.T) {
			var table map[string][]string
			if tc.host == "dual.example.com" {
				table = map[string][]string{tc.host: {"8.8.8.8", "10.0.0.1"}}
			} else {
				table = map[string][]string{tc.host: {tc.addr}}
			}
			got, reason := NodeTargetIsForbidden(context.Background(), tc.host, stubLookup(table, nil))
			if got != tc.want {
				t.Fatalf("NodeTargetIsForbidden(%q -> %s) = %v (%s), want %v", tc.host, tc.addr, got, reason, tc.want)
			}
		})
	}
}

// TestNodeTargetIsForbidden_FakeIPIsInconclusive pins the one case where the
// resolver check must NOT refuse.
//
// A machine running a transparent proxy in fake-IP mode answers every proxied
// name from 198.18.0.0/15. Treating that as evidence of a private target would
// refuse every node on such a machine, including the local test nodes smoke.sh
// uses.
func TestNodeTargetIsForbidden_FakeIPIsInconclusive(t *testing.T) {
	lookup := stubLookup(map[string][]string{
		"node.example.com": {"198.18.1.178"},
	}, nil)
	if got, reason := NodeTargetIsForbidden(context.Background(), "node.example.com", lookup); got {
		t.Fatalf("a fake-IP answer must be inconclusive, got refused (%s)", reason)
	}

	// A literal in the fake-IP range is still refused lexically: a node that
	// literally names 198.18.x.x is not a fake-IP artifact.
	if got := HostIsForbiddenLexically("198.18.1.178"); !got {
		t.Error("a literal 198.18.x.x address must be refused")
	}

	// A name that answers with a fake IP *and* a real private address is refused:
	// the fake-IP carve-out must not mask a real private answer.
	mixed := stubLookup(map[string][]string{
		"mixed.example.com": {"198.18.1.178", "10.0.0.1"},
	}, nil)
	if got, _ := NodeTargetIsForbidden(context.Background(), "mixed.example.com", mixed); !got {
		t.Error("a name resolving to a fake IP and a private address must be refused")
	}
}

// TestNodeTargetIsForbidden_FailsClosed pins that an unresolvable name is
// refused. A check that fails open is not a check.
func TestNodeTargetIsForbidden_FailsClosed(t *testing.T) {
	// Resolution error.
	if got, reason := NodeTargetIsForbidden(context.Background(), "gone.example.com",
		stubLookup(nil, errors.New("servfail"))); !got {
		t.Fatalf("an unresolvable name must be refused, got allowed (%s)", reason)
	}
	// Empty answer.
	if got, _ := NodeTargetIsForbidden(context.Background(), "empty.example.com",
		stubLookup(map[string][]string{"empty.example.com": {}}, nil)); !got {
		t.Error("a name resolving to no address must be refused")
	}
	// Empty host.
	if got, _ := NodeTargetIsForbidden(context.Background(), "", nil); !got {
		t.Error("an empty host must be refused")
	}
	// A lexical violation is refused before any lookup happens.
	calls := 0
	counting := func(_ context.Context, host string) ([]netip.Addr, error) {
		calls++
		return nil, nil
	}
	if got, _ := NodeTargetIsForbidden(context.Background(), "127.0.0.1", counting); !got {
		t.Error("a loopback literal must be refused")
	}
	if calls != 0 {
		t.Errorf("the resolver ran %d times for a lexical refusal, want 0", calls)
	}
	// A public literal needs no resolution either.
	if got, _ := NodeTargetIsForbidden(context.Background(), "203.0.113.9", counting); got {
		t.Error("a public literal must be allowed")
	}
	if calls != 0 {
		t.Errorf("the resolver ran %d times for a literal, want 0", calls)
	}
}

// TestNodeTargetIsForbidden_AcceptsRealNodes pins that the policy does not get in
// the way of ordinary nodes.
func TestNodeTargetIsForbidden_AcceptsRealNodes(t *testing.T) {
	lookup := stubLookup(map[string][]string{
		"hk1.example.net":    {"203.0.113.10"},
		"jp.example.net":     {"198.51.100.7"},
		"v6.example.net":     {"2606:4700::1111"},
		"cloudflare.example": {"104.16.0.1"},
	}, nil)
	for _, host := range []string{"hk1.example.net", "jp.example.net", "v6.example.net", "cloudflare.example"} {
		if got, reason := NodeTargetIsForbidden(context.Background(), host, lookup); got {
			t.Errorf("NodeTargetIsForbidden(%q) = refused (%s), want allowed", host, reason)
		}
	}
	// Public literals.
	for _, host := range []string{"203.0.113.9", "8.8.8.8", "2606:4700:4700::1111"} {
		if got, reason := NodeTargetIsForbidden(context.Background(), host, lookup); got {
			t.Errorf("NodeTargetIsForbidden(%q) = refused (%s), want allowed", host, reason)
		}
	}
}

// TestNormalizeHost pins the normalisation every entry point shares.
func TestNormalizeHost(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Example.COM", "example.com"},
		{" example.com ", "example.com"},
		{"example.com.", "example.com"},
		{"example.com:443", "example.com"},
		{"[2606:4700::1111]:443", "2606:4700::1111"},
		{"[::1]", "::1"},
		{"::1", "::1"},
		{"", ""},
		{"   ", ""},
		{"EXAMPLE.com.", "example.com"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := NormalizeHost(tc.in); got != tc.want {
				t.Fatalf("NormalizeHost(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestStripZone pins IPv6 zone removal.
func TestStripZone(t *testing.T) {
	if got := StripZone("fe80::1%eth0"); got != "fe80::1" {
		t.Fatalf("StripZone = %q, want fe80::1", got)
	}
	if got := StripZone("example.com"); got != "example.com" {
		t.Fatalf("StripZone on a name = %q, want unchanged", got)
	}
}

// TestAddrIsFakeIP pins the fake-IP range test.
func TestAddrIsFakeIP(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"198.18.0.1", true},
		{"198.18.1.178", true},
		{"198.19.255.255", true},
		{"198.17.255.255", false},
		{"198.20.0.0", false},
		{"8.8.8.8", false},
		{"10.0.0.1", false},
	} {
		if got := AddrIsFakeIP(netip.MustParseAddr(tc.addr)); got != tc.want {
			t.Errorf("AddrIsFakeIP(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
	if AddrIsFakeIP(netip.Addr{}) {
		t.Error("AddrIsFakeIP(invalid) = true, want false")
	}
}
