package providers

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"prism/internal/quality"
)

// DefaultDNSBLZones is the WP09 §3 zone list. dnsbl.sorbs.net is deliberately
// absent: the project stopped publishing answers.
var DefaultDNSBLZones = []string{"zen.spamhaus.org", "bl.spamcop.net", "psbl.surriel.com"}

// DNSBL codes.
const (
	// CodeDNSBLRefused marks a resolver that is refused service (127.255.255.x),
	// which is not a listing.
	CodeDNSBLRefused = "DNSBL_REFUSED"
	// CodeDNSBLDeferred marks a zone whose QPS gate stayed closed for too long.
	CodeDNSBLDeferred = "DNSBL_DEFERRED"
)

// refusedPrefix is the "query refused" answer range of the DNSBL convention.
var refusedPrefix = netip.MustParsePrefix("127.255.255.0/24")

// listedPrefix is the "listed" answer range of the DNSBL convention.
var listedPrefix = netip.MustParsePrefix("127.0.0.0/8")

// maxZoneWait bounds how long one lookup may wait for a zone's QPS gate before
// it gives up and defers the item (R4: never block a worker indefinitely).
const maxZoneWait = 5 * time.Second

// lameReferral is the error text the Go resolver uses for a successful reply
// that carries no address records; for a DNSBL that means "not listed".
const lameReferral = "lame referral"

// DNSBLOptions configures the DNSBL data source.
type DNSBLOptions struct {
	// Zones overrides the default zone list.
	Zones []string
	// Resolver is injectable; tests point it at a local DNS server.
	Resolver *net.Resolver
	TTL      time.Duration
	// ZoneQPS is the per-zone query rate (default 5/s per WP09 §3).
	ZoneQPS float64
	Now     func() time.Time
}

// DNSBL queries DNS blocklists for an address.
type DNSBL struct {
	spec     Spec
	zones    []string
	resolver *net.Resolver
	ttl      time.Duration
	zoneQPS  float64
	now      func() time.Time

	mu          sync.Mutex
	nextAllowed map[string]time.Time
}

// NewDNSBLProvider builds the DNSBL provider.
func NewDNSBLProvider(opts DNSBLOptions) *DNSBL {
	zones := opts.Zones
	if len(zones) == 0 {
		zones = DefaultDNSBLZones
	}
	cleaned := make([]string, 0, len(zones))
	for _, zone := range zones {
		if trimmed := strings.TrimSuffix(strings.TrimSpace(zone), "."); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	resolver := opts.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	qps := opts.ZoneQPS
	if qps <= 0 {
		qps = 5
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &DNSBL{
		spec: Spec{
			ID: "dnsbl", Name: "DNS blocklists", Website: "https://www.spamhaus.org/",
			Terms: "Spamhaus and SpamCop both restrict their public zones to non-commercial, " +
				"low-volume resolvers that ask from their own recursive resolver; queries from a " +
				"public resolver are refused (recorded as DNSBL_REFUSED, never as a listing). " +
				"Prism defaults to 5 queries per second per zone and does not query IPv6.",
			Kind: KindOnlineIP, Profile: DNSBLProfile,
			RequiresKey: false, DefaultEnabled: true,
			DefaultDailyLimit: 0, DefaultQPS: 5, BatchSize: 1,
			DefaultTTL: ttl, SupportsIPv6: false,
		},
		zones: cleaned, resolver: resolver, ttl: ttl, zoneQPS: qps, now: now,
		nextAllowed: make(map[string]time.Time),
	}
}

// Spec implements OnlineProvider.
func (p *DNSBL) Spec() Spec { return p.spec }

// Zones returns the configured zone list.
func (p *DNSBL) Zones() []string {
	out := make([]string, len(p.zones))
	copy(out, p.zones)
	return out
}

// Lookup implements OnlineProvider. IPv6 is reported as unsupported (§3).
func (p *DNSBL) Lookup(ctx context.Context, ips []netip.Addr) map[netip.Addr]Result {
	out := make(map[netip.Addr]Result, len(ips))
	for _, raw := range ips {
		ip, unsupported, ok := PublicIP(raw)
		if !ok {
			out[raw] = unsupported
			continue
		}
		if !ip.Is4() {
			out[raw] = Failure(CodeUnsupported, "DNSBL queries are IPv4-only")
			continue
		}
		out[raw] = p.lookupOne(ctx, ip)
	}
	return out
}

func (p *DNSBL) lookupOne(ctx context.Context, ip netip.Addr) Result {
	if len(p.zones) == 0 {
		return Failure(CodeUnavailable, "no DNSBL zone is configured")
	}
	reversed := reverseIPv4(ip)
	listed := make([]string, 0, len(p.zones))
	checked := make([]string, 0, len(p.zones))
	refused := false
	for _, zone := range p.zones {
		if err := p.waitForGate(ctx, zone); err != nil {
			var providerErr *ProviderError
			if errors.As(err, &providerErr) {
				return Result{Err: providerErr}
			}
			return Failure(CodeUnavailable, "DNSBL query was canceled")
		}
		answers, err := p.query(ctx, reversed+"."+zone)
		if err != nil {
			return FailureErr(err, "DNSBL zone "+zone+" could not be queried")
		}
		checked = append(checked, zone)
		switch {
		case answersListed(answers) == refusedMarker:
			refused = true
		case answersListed(answers) == listedMarker:
			listed = append(listed, zone)
		}
	}
	if refused {
		// A refused query is never a hit, and it must stay visible.
		return Result{Err: &ProviderError{
			Code: CodeDNSBLRefused,
			Message: "the DNSBL zone refused this resolver; configure a private resolver " +
				"in config_json.resolver",
			RetryAfter: time.Hour,
		}}
	}
	now := p.now().UTC()
	evidence := &quality.Evidence{
		IP: ip.String(), Provider: "dnsbl", Profile: DNSBLProfile,
		IPType: "unknown", Grade: "unknown",
		DNSBLListed: listed, DNSBLChecked: checked,
		ObservedAt: now, ValidUntil: now.Add(p.ttl),
	}
	if len(listed) > 0 {
		yes := true
		evidence.Signals.Anonymous = &yes
	}
	return Result{Evidence: evidence}
}

type dnsblAnswer int

const (
	notListed dnsblAnswer = iota
	listedMarker
	refusedMarker
)

func answersListed(answers []netip.Addr) dnsblAnswer {
	state := notListed
	for _, answer := range answers {
		address := answer.Unmap()
		if refusedPrefix.Contains(address) {
			return refusedMarker
		}
		if listedPrefix.Contains(address) {
			state = listedMarker
		}
	}
	return state
}

// query resolves one DNSBL name. NXDOMAIN and a NOERROR reply without address
// records both mean "this zone does not list the address", which is a normal
// answer. Everything else is an explainable failure.
func (p *DNSBL) query(ctx context.Context, name string) ([]netip.Addr, error) {
	answers, err := p.resolver.LookupNetIP(ctx, "ip4", name)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && (dnsErr.IsNotFound || dnsErr.Err == lameReferral) {
			// The Go resolver reports a successful reply without address records
			// as a "lame referral".
			return nil, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, &ProviderError{Code: CodeUnavailable, Message: "DNSBL query failed", RetryAfter: time.Minute}
	}
	return answers, nil
}

// waitForGate blocks until the zone is allowed to receive another query. The
// wait is bounded by maxZoneWait; a longer wait is deferred, not slept through.
func (p *DNSBL) waitForGate(ctx context.Context, zone string) error {
	now := p.now().UTC()
	p.mu.Lock()
	next := p.nextAllowed[zone]
	if !next.After(now) {
		p.nextAllowed[zone] = now.Add(p.gateInterval())
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	wait := next.Sub(now)
	if wait > maxZoneWait {
		return &ProviderError{
			Code: CodeDNSBLDeferred, Message: "DNSBL zone rate limit is not reached",
			RetryAfter: wait,
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	p.mu.Lock()
	p.nextAllowed[zone] = p.now().UTC().Add(p.gateInterval())
	p.mu.Unlock()
	return nil
}

func (p *DNSBL) gateInterval() time.Duration {
	if p.zoneQPS <= 0 {
		return 0
	}
	return time.Duration(float64(time.Second) / p.zoneQPS)
}

// reverseIPv4 renders the reverse-octet lookup label of an IPv4 address.
func reverseIPv4(ip netip.Addr) string {
	bytes := ip.Unmap().As4()
	return itoa(int(bytes[3])) + "." + itoa(int(bytes[2])) + "." +
		itoa(int(bytes[1])) + "." + itoa(int(bytes[0]))
}

// ResolverFromURL builds a *net.Resolver from a "udp://host:53" or
// "tcp://host:53" URL (config_json.resolver). An empty value returns nil so the
// caller keeps the system resolver.
func ResolverFromURL(raw string) (*net.Resolver, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	network := strings.ToLower(parsed.Scheme)
	if network == "" {
		network = "udp"
	}
	if network != "udp" && network != "tcp" {
		return nil, errors.New("resolver scheme must be udp or tcp")
	}
	address := parsed.Host
	if address == "" {
		return nil, errors.New("resolver URL must contain a host")
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, "53")
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			return dialer.DialContext(ctx, network, address)
		},
	}, nil
}
