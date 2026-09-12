package inspection

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	"prism/internal/quality"
)

const IPPureURL = "https://my.ippure.com/v1/info"
const IPPureInterval = time.Minute
const IPPureEvidenceTTL = 24 * time.Hour
const maxIPPureResults = 512

// ManualSourceStatus describes local safeguards, not a claimed vendor quota.
type ManualSourceStatus struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Website         string     `json:"website"`
	Busy            bool       `json:"busy"`
	IntervalSeconds int        `json:"interval_seconds"`
	NextAllowedAt   *time.Time `json:"next_allowed_at,omitempty"`
	CurrentIPs      int        `json:"current_ips"`
}

type IPPureReview struct {
	Evidence       *quality.Evidence `json:"evidence"`
	NodeHash       string            `json:"node_hash"`
	ExpectedIP     string            `json:"expected_ip"`
	MatchesNodeIP  bool              `json:"matches_node_ip"`
	IsResidential  *bool             `json:"is_residential"`
	ScoreSupported bool              `json:"score_supported"`
	NextAllowedAt  time.Time         `json:"next_allowed_at"`
}

type IPPureResponse struct {
	StatusCode int
	RetryAfter string
	Body       []byte
}

type IPPureFetcher func(context.Context, adapter.Outbound) (IPPureResponse, error)

// IPPureChecker is deliberately separate from the durable inspection queue.
// Verified single checks can be reused temporarily in memory by the node list.
// There is no automatic retry, bulk endpoint or persistent copy of IPPure data.
type IPPureChecker struct {
	mu          sync.Mutex
	running     bool
	nextAllowed time.Time
	fetch       IPPureFetcher
	results     map[string]*quality.Evidence
}

func NewIPPureChecker() *IPPureChecker { return NewIPPureCheckerWithFetcher(fetchIPPure) }

func NewIPPureCheckerWithFetcher(fetch IPPureFetcher) *IPPureChecker {
	return &IPPureChecker{fetch: fetch, results: make(map[string]*quality.Evidence)}
}

func (c *IPPureChecker) Status() ManualSourceStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := ManualSourceStatus{ID: "ippure", Name: "IPPure", Website: "https://ippure.com/MyIP-Info-API", Busy: c.running, IntervalSeconds: int(IPPureInterval.Seconds())}
	for _, e := range c.results {
		if time.Now().Before(e.ValidUntil) && e.RiskScore != nil {
			status.CurrentIPs++
		}
	}
	if time.Now().Before(c.nextAllowed) {
		next := c.nextAllowed
		status.NextAllowedAt = &next
	}
	return status
}

// Remember is called only after the service verifies the node's exit binding.
// Cached observations retain their original time and a local 24-hour freshness
// window. Restarting removes them; expired observations survive one more day.
func (c *IPPureChecker) Remember(e *quality.Evidence) {
	if e == nil || e.Provider != "ippure" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.results == nil {
		c.results = make(map[string]*quality.Evidence)
	}
	now := time.Now()
	for ip, previous := range c.results {
		if now.Sub(previous.ObservedAt) >= 2*IPPureEvidenceTTL {
			delete(c.results, ip)
		}
	}
	if _, exists := c.results[e.IP]; !exists && len(c.results) >= maxIPPureResults {
		oldest := ""
		for ip, previous := range c.results {
			if oldest == "" || previous.ObservedAt.Before(c.results[oldest].ObservedAt) {
				oldest = ip
			}
		}
		delete(c.results, oldest)
	}
	snapshot := *e
	c.results[e.IP] = &snapshot
}

func (c *IPPureChecker) Source(ip netip.Addr) quality.SourceSummary {
	source := quality.SourceSummary{Provider: "ippure", Configured: true, State: "unobserved"}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.results[ip.Unmap().String()]
	if e == nil {
		return source
	}
	if time.Since(e.ObservedAt) >= 2*IPPureEvidenceTTL {
		delete(c.results, e.IP)
		return source
	}
	snapshot := *e
	source.Evidence = &snapshot
	source.State = "valid"
	if !time.Now().Before(e.ValidUntil) {
		source.State = "stale"
	}
	return source
}

func (c *IPPureChecker) CachedIPs() []netip.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ips []netip.Addr
	for ip, e := range c.results {
		if time.Since(e.ObservedAt) >= 2*IPPureEvidenceTTL {
			delete(c.results, ip)
			continue
		}
		if address, err := netip.ParseAddr(ip); err == nil {
			ips = append(ips, address)
		}
	}
	return ips
}

func (c *IPPureChecker) Check(ctx context.Context, outbound adapter.Outbound) (*IPPureReview, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if outbound == nil || c.fetch == nil {
		return nil, &ProviderError{Code: "IPPURE_UNAVAILABLE", Message: "IPPure review requires a ready node connection"}
	}
	now := time.Now().UTC()
	c.mu.Lock()
	if c.running || now.Before(c.nextAllowed) {
		delay := max(time.Second, c.nextAllowed.Sub(now))
		c.mu.Unlock()
		return nil, &ProviderError{Code: "IPPURE_LIMIT", Message: "IPPure review is cooling down; retry later", RetryAfter: delay}
	}
	c.running = true
	c.nextAllowed = now.Add(IPPureInterval)
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.running = false; c.mu.Unlock() }()

	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	response, err := c.fetch(ctx, outbound)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, &ProviderError{Code: "IPPURE_UNAVAILABLE", Message: "IPPure could not be reached through this node"}
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
		delay := boundedRetryAfter(response.RetryAfter, time.Now())
		if response.StatusCode != http.StatusTooManyRequests {
			delay = 24 * time.Hour
		}
		c.mu.Lock()
		c.nextAllowed = maxTime(c.nextAllowed, time.Now().UTC().Add(delay))
		c.mu.Unlock()
		return nil, &ProviderError{Code: "IPPURE_LIMIT", Message: "IPPure has limited reviews; retry later", RetryAfter: delay}
	}
	if response.StatusCode != http.StatusOK {
		return nil, &ProviderError{Code: "IPPURE_UNAVAILABLE", Message: "IPPure returned an unsuccessful response"}
	}
	result, err := decodeIPPure(response.Body, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	result.NextAllowedAt = c.nextAllowed
	c.mu.Unlock()
	return result, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func fetchIPPure(ctx context.Context, outbound adapter.Outbound) (IPPureResponse, error) {
	transport := &http.Transport{
		// Always dial with this exact node. Do not use environment proxies, the
		// platform router, bypass rules or direct fallback for this check.
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return outbound.DialContext(ctx, network, M.ParseSocksaddr(address))
		},
		DisableKeepAlives: true, ForceAttemptHTTP2: true,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, IPPureURL, nil)
	if err != nil {
		return IPPureResponse{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Prism/1.0 (manual node IP review)")
	response, err := client.Do(request)
	if err != nil {
		return IPPureResponse{}, err
	}
	defer response.Body.Close()
	result := IPPureResponse{StatusCode: response.StatusCode, RetryAfter: response.Header.Get("Retry-After")}
	if response.StatusCode != http.StatusOK {
		return result, nil
	}
	result.Body, err = io.ReadAll(io.LimitReader(response.Body, 32*1024+1))
	return result, err
}

func decodeIPPure(body []byte, now time.Time) (*IPPureReview, error) {
	invalid := &ProviderError{Code: "IPPURE_RESPONSE", Message: "IPPure returned incomplete or invalid evidence"}
	if len(body) > 32*1024 {
		return nil, invalid
	}
	var raw struct {
		IP           string `json:"ip"`
		ASN          *int   `json:"asn"`
		Organization string `json:"asOrganization"`
		CountryCode  string `json:"countryCode"`
		Risk         *int   `json:"fraudScore"`
		Residential  *bool  `json:"isResidential"`
		Broadcast    *bool  `json:"isBroadcast"`
	}
	if json.Unmarshal(body, &raw) != nil {
		return nil, invalid
	}
	ip, err := netip.ParseAddr(raw.IP)
	if err != nil {
		return nil, invalid
	}
	ip, err = quality.PublicIP(ip)
	if err != nil || (raw.Risk != nil && (*raw.Risk < 0 || *raw.Risk > 100)) ||
		(raw.ASN != nil && *raw.ASN < 0) || (raw.Risk == nil && raw.Residential == nil && raw.Broadcast == nil) {
		return nil, invalid
	}
	// IPPure explicitly does not calculate an IPv6 fraud score. Ignore even
	// a zero placeholder returned for IPv6 rather than inventing perfect purity.
	if ip.Is6() {
		raw.Risk = nil
	}
	evidence := &quality.Evidence{IP: ip.String(), Provider: "ippure", Profile: "ippure-manual-v1",
		IPType: "unknown", RiskScore: raw.Risk, Grade: "unknown",
		Organization: boundedText(raw.Organization, 240), CountryCode: boundedText(raw.CountryCode, 8),
		ObservedAt: now, ValidUntil: now.Add(IPPureEvidenceTTL)}
	if raw.ASN != nil {
		evidence.ASN = "AS" + strconv.Itoa(*raw.ASN)
	}
	if raw.Residential != nil {
		if *raw.Residential {
			evidence.IPType = "residential"
			evidence.SourceType = "Residential"
		} else {
			evidence.SourceType = "Non-residential"
		}
	}
	if raw.Broadcast != nil {
		native := !*raw.Broadcast
		evidence.Native = &native
	}
	return &IPPureReview{Evidence: evidence, IsResidential: raw.Residential, ScoreSupported: ip.Is4() && raw.Risk != nil}, nil
}
