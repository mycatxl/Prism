// Package egress probes a node's own egress addresses (IPv4, IPv6, Cloudflare
// colo/loc) through outbound.Manager and persists the observation (WP08 §3.2
// step 1 and §4).
package egress

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"prism/internal/intel/store"
	"prism/internal/netutil"
	"prism/internal/node"
)

// Cloudflare trace endpoints. 1.1.1.1 is the primary choice of §3.2; the
// per-family public hostnames are the documented fallback for environments where
// the IP-literal URLs are blocked or the certificate does not cover the IP SANs.
const (
	DefaultTraceURLv4 = "https://1.1.1.1/cdn-cgi/trace"
	DefaultTraceURLv6 = "https://[2606:4700:4700::1111]/cdn-cgi/trace"

	FallbackTraceURLv4 = "https://cloudflare.com/cdn-cgi/trace"
	FallbackTraceURLv6 = "https://cloudflare.com/cdn-cgi/trace"
)

// DefaultTimeout bounds one egress request (§3.2: 15 seconds).
const DefaultTimeout = 15 * time.Second

// maxTraceBytes bounds the trace response body.
const maxTraceBytes = 8 * 1024

// ErrNoIPv4 is returned when the IPv4 trace request failed: the node has no
// usable egress and the item must be retried.
var ErrNoIPv4 = errors.New("egress probe: no IPv4 address")

// Fetcher mirrors outbound.Manager.FetchWithOptions.
type Fetcher interface {
	FetchWithOptions(ctx context.Context, hash node.Hash, url string, opts netutil.OutboundHTTPOptions) ([]byte, time.Duration, error)
}

// Trace is one parsed /cdn-cgi/trace response.
type Trace struct {
	IP   netip.Addr
	Loc  string
	Colo string
}

// ParseTrace extracts ip, loc and colo from a Cloudflare trace body.
func ParseTrace(body []byte) (Trace, error) {
	var trace Trace
	for _, line := range strings.Split(string(body), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "ip":
			addr, err := netip.ParseAddr(strings.TrimSpace(value))
			if err != nil {
				return Trace{}, fmt.Errorf("parse trace ip %q: %w", value, err)
			}
			trace.IP = addr.Unmap()
		case "loc":
			trace.Loc = strings.TrimSpace(value)
		case "colo":
			trace.Colo = strings.TrimSpace(value)
		}
	}
	if !trace.IP.IsValid() {
		return Trace{}, errors.New("parse trace: missing ip field")
	}
	return trace, nil
}

// Probe performs egress observations for one node.
type Probe struct {
	Fetcher Fetcher
	Store   *store.Store
	// ProbeEgressSync keeps Resin's cache.db egress_ip semantics (§3.2 step 1).
	// It is optional: when nil the probe only writes intel.db.
	ProbeEgressSync func(ctx context.Context, hash node.Hash) error
	// URLs override the default trace endpoints.
	URLv4 string
	URLv6 string
	// Timeout bounds one request.
	Timeout time.Duration
	// Now is the injected clock.
	Now func() time.Time
}

func (p *Probe) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return DefaultTimeout
}

func (p *Probe) urlv4() string {
	if strings.TrimSpace(p.URLv4) != "" {
		return p.URLv4
	}
	return DefaultTraceURLv4
}

func (p *Probe) urlv6() string {
	if strings.TrimSpace(p.URLv6) != "" {
		return p.URLv6
	}
	return DefaultTraceURLv6
}

func (p *Probe) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

// Result is the outcome of one egress observation.
type Result struct {
	Row       store.NodeEgress
	Changed   bool
	V6Checked bool
	V6Failed  bool
}

// Run probes the node and persists the observation. An IPv4 failure is fatal
// (the item is retried); a missing IPv6 is a normal outcome.
func (p *Probe) Run(ctx context.Context, hash node.Hash) (Result, error) {
	if p.Store == nil {
		return Result{}, errors.New("egress probe: store is required")
	}
	if p.Fetcher == nil {
		return Result{}, errors.New("egress probe: fetcher is required")
	}

	obs := store.EgressObservation{NodeHash: hash.String()}

	timeoutCtx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()
	body, _, err := p.Fetcher.FetchWithOptions(timeoutCtx, hash, p.urlv4(), netutil.OutboundHTTPOptions{
		MaxBodyBytes: maxTraceBytes,
	})
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrNoIPv4, err)
	}
	trace, err := ParseTrace(body)
	if err != nil {
		return Result{}, fmt.Errorf("egress probe: %w", err)
	}
	if trace.IP.Is6() {
		// The v4 endpoint answered with a v6 address; keep it as the v6 sample
		// rather than mislabelling it.
		obs.IPv6 = trace.IP
		obs.V6Checked = true
	} else {
		obs.IPv4 = trace.IP
	}
	obs.Colo = trace.Colo
	obs.Loc = trace.Loc

	// The IPv6 attempt is recorded even when it fails (§3.2).
	v6ctx, v6cancel := context.WithTimeout(ctx, p.timeout())
	obs.V6Checked = true
	if v6Body, _, err := p.Fetcher.FetchWithOptions(v6ctx, hash, p.urlv6(), netutil.OutboundHTTPOptions{
		MaxBodyBytes: maxTraceBytes,
	}); err != nil {
		obs.IPv6 = netip.Addr{}
	} else if v6Trace, err := ParseTrace(v6Body); err != nil || !v6Trace.IP.Is6() {
		obs.IPv6 = netip.Addr{}
	} else {
		obs.IPv6 = v6Trace.IP
		if obs.Colo == "" {
			obs.Colo = v6Trace.Colo
		}
		if obs.Loc == "" {
			obs.Loc = v6Trace.Loc
		}
	}
	v6cancel()

	if !obs.IPv4.IsValid() {
		return Result{}, ErrNoIPv4
	}

	obs.NowNs = p.now().UnixNano()
	row, changed, err := p.Store.RecordEgress(ctx, obs)
	if err != nil {
		return Result{}, err
	}

	// Keep Resin's egress_ip (cache.db) semantics unchanged.
	if p.ProbeEgressSync != nil {
		if err := p.ProbeEgressSync(ctx, hash); err != nil {
			// A cache.db failure must not fail the intel.db write.
			return Result{Row: row, Changed: changed, V6Checked: obs.V6Checked, V6Failed: !obs.IPv6.IsValid()}, nil
		}
	}

	return Result{
		Row:       row,
		Changed:   changed,
		V6Checked: obs.V6Checked,
		V6Failed:  !obs.IPv6.IsValid(),
	}, nil
}
