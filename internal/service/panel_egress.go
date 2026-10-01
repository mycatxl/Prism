package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"prism/internal/intel/egress"
)

// ------------------------------------------------------------------
// Panel self-trace (panel egress region/IP)
// ------------------------------------------------------------------
//
// The dashboard map needs to know where the panel server itself (the operator's
// VPS) egresses from, so it can draw traffic flowing FROM the panel TO the node
// regions. That observation is best effort and cached:
//
//   - the trace request is a DIRECT HTTPS GET issued by this process. It must
//     never go through outbound.Manager, which routes through proxy nodes and
//     would report a node's egress instead of the panel's own.
//   - the API layer only reads the cached snapshot. Snapshot never blocks: a
//     caller that arrives before the first trace completes reads empty strings.
//   - a failed lookup leaves the previous observation in place and is not
//     retried more often than the TTL.

const (
	// PanelEgressTTL bounds how long one panel self-trace stays valid.
	PanelEgressTTL = 30 * time.Minute
	// PanelEgressTimeout bounds one panel self-trace request.
	PanelEgressTimeout = 10 * time.Second
	// panelEgressMaxTraceBytes bounds the trace response body; it mirrors
	// egress.maxTraceBytes.
	panelEgressMaxTraceBytes = 8 * 1024
	// panelEgressUserAgent identifies the panel's own request.
	panelEgressUserAgent = "Prism/1.0"
)

// panelEgressClient is the direct client used when no client is injected. The
// per-request deadline is carried by the request context, as in egress.Probe.
var panelEgressClient = &http.Client{Timeout: PanelEgressTimeout}

// PanelEgressSnapshot is one cached panel self-trace observation. Both fields
// are empty strings when the panel egress location is unknown.
type PanelEgressSnapshot struct {
	Region string
	IP     string
}

// PanelEgress caches the panel's own egress region and IP from the Cloudflare
// trace endpoint. The zero value is usable and all fields are read-only after
// the provider is shared.
type PanelEgress struct {
	// URL overrides the trace endpoint; defaults to egress.DefaultTraceURLv4.
	URL string
	// Client overrides the direct HTTP client. It must be a direct client: an
	// outbound-backed one would report a node's egress.
	Client *http.Client
	// Timeout bounds one fetch; defaults to PanelEgressTimeout.
	Timeout time.Duration
	// TTL bounds cache validity; defaults to PanelEgressTTL.
	TTL time.Duration
	// Now is the injected clock; defaults to time.Now.
	Now func() time.Time

	mu sync.Mutex
	// region/ip hold the last successful observation.
	region string
	ip     string
	// fetchedAt is the last attempt, successful or not, so a broken trace
	// endpoint is never retried more often than the TTL.
	fetchedAt time.Time
	// fetching is the single-flight guard: concurrent Snapshot calls trigger at
	// most one background fetch.
	fetching bool
}

// NewPanelEgressProvider returns the panel self-trace cache used by the system
// info endpoint. The first Snapshot call schedules the first background fetch.
func NewPanelEgressProvider() *PanelEgress {
	return &PanelEgress{URL: egress.DefaultTraceURLv4}
}

// Snapshot returns the cached panel egress observation without blocking. When
// the cache is empty or older than the TTL, it schedules a background fetch
// (single-flight) and immediately returns the current values, which may be
// empty strings.
func (p *PanelEgress) Snapshot() PanelEgressSnapshot {
	if p == nil {
		return PanelEgressSnapshot{}
	}
	now := p.now()

	p.mu.Lock()
	snapshot := PanelEgressSnapshot{Region: p.region, IP: p.ip}
	stale := p.fetchedAt.IsZero() || now.Sub(p.fetchedAt) >= p.ttl()
	trigger := stale && !p.fetching
	if trigger {
		p.fetching = true
	}
	p.mu.Unlock()

	if trigger {
		go p.fetch()
	}
	return snapshot
}

// fetch performs one direct self-trace and replaces the cache on success. It
// runs in a background goroutine and never surfaces an error: an unknown egress
// location simply leaves the previous snapshot in place.
func (p *PanelEgress) fetch() {
	snapshot, ok := p.trace()
	now := p.now()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.fetching = false
	// The attempt time is recorded even on failure, which is what keeps a
	// failing endpoint from being retried more often than the TTL.
	p.fetchedAt = now
	if !ok {
		return
	}
	p.region = snapshot.Region
	p.ip = snapshot.IP
}

// trace issues one direct HTTPS GET to the Cloudflare trace endpoint and parses
// it with the shared egress parser. The panel's own egress is the subject here,
// so no outbound.Manager is involved.
func (p *PanelEgress) trace() (PanelEgressSnapshot, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url(), nil)
	if err != nil {
		return PanelEgressSnapshot{}, false
	}
	req.Header.Set("User-Agent", panelEgressUserAgent)

	resp, err := p.client().Do(req)
	if err != nil {
		return PanelEgressSnapshot{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return PanelEgressSnapshot{}, false
	}

	// Bound the body exactly like egress.Probe does through MaxBodyBytes.
	body, err := io.ReadAll(io.LimitReader(resp.Body, panelEgressMaxTraceBytes+1))
	if err != nil || len(body) > panelEgressMaxTraceBytes {
		return PanelEgressSnapshot{}, false
	}

	parsed, err := egress.ParseTrace(body)
	if err != nil || strings.TrimSpace(parsed.Loc) == "" {
		return PanelEgressSnapshot{}, false
	}
	return PanelEgressSnapshot{
		Region: strings.TrimSpace(parsed.Loc),
		IP:     parsed.IP.String(),
	}, true
}

func (p *PanelEgress) url() string {
	if trimmed := strings.TrimSpace(p.URL); trimmed != "" {
		return trimmed
	}
	return egress.DefaultTraceURLv4
}

func (p *PanelEgress) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return PanelEgressTimeout
}

func (p *PanelEgress) ttl() time.Duration {
	if p.TTL > 0 {
		return p.TTL
	}
	return PanelEgressTTL
}

func (p *PanelEgress) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func (p *PanelEgress) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return panelEgressClient
}
