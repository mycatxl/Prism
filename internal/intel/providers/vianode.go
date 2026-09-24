package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"

	"prism/internal/quality"
)

// viaNodeMaxBody bounds one via-node response body.
const viaNodeMaxBody = 32 * 1024

// viaNodeResponse is one bounded response fetched through a node.
type viaNodeResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// fetchViaNode performs one request through the tested node's outbound. It never
// uses an environment proxy, the platform router, a bypass rule or a direct
// fallback (WP09 §1).
func fetchViaNode(ctx context.Context, ob adapter.Outbound, target string, headers map[string]string, timeout time.Duration) (viaNodeResponse, Result) {
	if ob == nil {
		return viaNodeResponse{}, Failure(CodeUnavailable, "the node connection is not ready")
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return ob.DialContext(ctx, network, M.ParseSocksaddr(address))
		},
		DisableKeepAlives: true, ForceAttemptHTTP2: true,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return viaNodeResponse{}, Failure(CodeRequest, "the via-node request could not be built")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", providerUserAgent)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return viaNodeResponse{}, Failure(CodeUnavailable, "the via-node request was canceled")
		}
		return viaNodeResponse{}, Failure(CodeUnavailable, "the via-node request failed")
	}
	defer resp.Body.Close()
	out := viaNodeResponse{Status: resp.StatusCode, Header: resp.Header.Clone()}
	if resp.StatusCode != http.StatusOK {
		return out, statusFailure(response{Status: resp.StatusCode, RetryAfter: BoundedRetryAfter(resp.Header.Get("Retry-After"), time.Now())}, "the data source")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, viaNodeMaxBody+1))
	if err != nil || len(body) > viaNodeMaxBody {
		return out, Failure(CodeResponse, "the via-node response is unreadable or too large")
	}
	out.Body = body
	return out, Result{}
}

// ---------------------------------------------------------------------------
// IPPure (via-node)
// ---------------------------------------------------------------------------

// IPPureURL is the IPPure "what is my IP" endpoint.
const IPPureURL = "https://my.ippure.com/v1/info"

// IPPureOptions configures the IPPure data source.
type IPPureOptions struct {
	URL     string
	TTL     time.Duration
	Timeout time.Duration
	Now     func() time.Time
}

// IPPure reports the fraud score and residential classification of a node's own
// egress address.
type IPPure struct {
	spec    Spec
	url     string
	ttl     time.Duration
	timeout time.Duration
	now     func() time.Time
}

// NewIPPureProvider builds the IPPure provider.
func NewIPPureProvider(opts IPPureOptions) *IPPure {
	target := strings.TrimSpace(opts.URL)
	if target == "" {
		target = IPPureURL
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &IPPure{
		spec: Spec{
			ID: "ippure", Name: "IPPure", Website: "https://ippure.com/MyIP-Info-API",
			Terms: "IPPure's terms may restrict bulk or systematic use. The query runs through " +
				"the node, so the vendor sees that node's address and the quota belongs to it: " +
				"Prism counts a per-node daily budget (default 500) and keeps a provider-wide " +
				"QPS valve (default 2/s) so the whole inventory never arrives at once. Check " +
				"the vendor conditions yourself before raising these limits.",
			Kind: KindViaNode, Profile: IPPureProfile,
			RequiresKey: false, DefaultEnabled: true,
			DefaultDailyLimit: 500, DefaultQPS: 2, BatchSize: 1,
			DefaultTTL: ttl, SupportsIPv6: true,
		},
		url: target, ttl: ttl, timeout: timeout, now: now,
	}
}

// Spec implements ViaNodeProvider.
func (p *IPPure) Spec() Spec { return p.spec }

// Lookup implements ViaNodeProvider. The returned address must match the
// expected egress address or the result is an EGRESS_MISMATCH error.
func (p *IPPure) Lookup(ctx context.Context, ob adapter.Outbound, expect netip.Addr) Result {
	resp, failure := fetchViaNode(ctx, ob, p.url, nil, p.timeout)
	if failure.Failed() {
		return failure
	}
	evidence, err := DecodeIPPure(resp.Body, expect, p.now().UTC(), p.ttl)
	if err != nil {
		return FailureErr(err, "IPPure returned incomplete or invalid evidence")
	}
	return Result{Evidence: evidence, Raw: boundedRaw(resp.Body)}
}

// DecodeIPPure validates one IPPure payload against the expected egress address.
func DecodeIPPure(body []byte, expect netip.Addr, now time.Time, ttl time.Duration) (*quality.Evidence, error) {
	invalid := func() (*quality.Evidence, error) {
		return nil, &ProviderError{Code: CodeResponse, Message: "IPPure returned incomplete or invalid evidence"}
	}
	if len(body) > viaNodeMaxBody {
		return invalid()
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
		return invalid()
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(raw.IP))
	if err != nil {
		return invalid()
	}
	public, err := quality.PublicIP(ip)
	if err != nil {
		return invalid()
	}
	if expect.IsValid() && public.Unmap() != expect.Unmap() {
		return nil, &ProviderError{
			Code:    CodeEgressMismat,
			Message: "IPPure reported an address that is not this node's egress address",
		}
	}
	if (raw.Risk != nil && (*raw.Risk < 0 || *raw.Risk > 100)) ||
		(raw.ASN != nil && *raw.ASN < 0) ||
		(raw.Risk == nil && raw.Residential == nil && raw.Broadcast == nil) {
		return invalid()
	}
	// IPPure explicitly does not calculate an IPv6 fraud score. Ignore even a
	// zero placeholder for IPv6 rather than inventing perfect purity.
	if public.Is6() {
		raw.Risk = nil
	}
	evidence := &quality.Evidence{
		IP: public.String(), Provider: "ippure", Profile: IPPureProfile,
		IPType: "unknown", Grade: "unknown",
		Organization: boundedText(raw.Organization, 240),
		CountryCode:  boundedText(strings.ToUpper(strings.TrimSpace(raw.CountryCode)), 8),
		FraudScore:   raw.Risk, IsResidential: raw.Residential,
		ObservedAt: now, ValidUntil: now.Add(ttl),
	}
	if raw.ASN != nil {
		evidence.ASNNumber = *raw.ASN
		evidence.ASN = "AS" + strconv.Itoa(*raw.ASN)
	}
	if raw.Residential != nil {
		evidence.IsResidential = raw.Residential
		if *raw.Residential {
			evidence.IPType = "residential"
			evidence.SourceType = "Residential"
		} else {
			evidence.IPType = "non_residential"
			evidence.SourceType = "Non-residential"
		}
	}
	if raw.Broadcast != nil {
		native := !*raw.Broadcast
		evidence.Native = &native
	}
	if raw.Risk != nil {
		evidence.RiskScore = raw.Risk
		evidence.Grade = quality.RiskGrade(raw.Risk, evidence.Signals)
	}
	return evidence, nil
}

// ---------------------------------------------------------------------------
// ip-api.com (via-node)
// ---------------------------------------------------------------------------

// ipAPIFields is the field selection of WP09 §3.
const ipAPIFields = "status,message,country,countryCode,regionName,city,isp,org,as,asname,mobile,proxy,hosting,query"

// IPAPIURL is the free ip-api.com endpoint (HTTP only).
const IPAPIURL = "http://ip-api.com/json/"

// IPAPIOptions configures the ip-api.com data source.
type IPAPIOptions struct {
	URL     string
	Timeout time.Duration
	TTL     time.Duration
	Now     func() time.Time
}

// IPAPI reports the country, city, ISP and hosting/mobile classification of a
// node's own egress address.
type IPAPI struct {
	spec    Spec
	url     string
	ttl     time.Duration
	timeout time.Duration
	now     func() time.Time
}

// NewIPAPIProvider builds the ip-api.com provider.
func NewIPAPIProvider(opts IPAPIOptions) *IPAPI {
	target := strings.TrimSpace(opts.URL)
	if target == "" {
		target = IPAPIURL
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &IPAPI{
		spec: Spec{
			ID: "ip_api", Name: "ip-api.com", Website: "https://ip-api.com/",
			Terms: "The free endpoint is HTTP-only, limited to 45 requests per minute per " +
				"source address and is not licensed for commercial use. Because the query runs " +
				"through the node, that 45/minute budget belongs to the node's own egress " +
				"address, so the daily budget Prism counts is per node. The provider-wide 5 " +
				"queries per second valve only keeps the whole inventory from arriving at once.",
			Kind: KindViaNode, Profile: IPAPIProfile,
			RequiresKey: false, DefaultEnabled: true,
			DefaultDailyLimit: 0, DefaultQPS: 5, BatchSize: 1,
			DefaultTTL: ttl, SupportsIPv6: true,
		},
		url: target, ttl: ttl, timeout: timeout, now: now,
	}
}

// Spec implements ViaNodeProvider.
func (p *IPAPI) Spec() Spec { return p.spec }

// Lookup implements ViaNodeProvider.
func (p *IPAPI) Lookup(ctx context.Context, ob adapter.Outbound, expect netip.Addr) Result {
	target := strings.TrimRight(p.url, "/") + "/?fields=" + ipAPIFields
	resp, failure := fetchViaNode(ctx, ob, target, nil, p.timeout)
	if failure.Failed() {
		return failure
	}
	evidence, err := DecodeIPAPI(resp.Body, expect, p.now().UTC(), p.ttl)
	if err != nil {
		return FailureErr(err, "ip-api.com returned incomplete or mismatched evidence")
	}
	return Result{Evidence: evidence, Raw: boundedRaw(resp.Body)}
}

// DecodeIPAPI validates one ip-api.com payload against the expected egress
// address.
func DecodeIPAPI(body []byte, expect netip.Addr, now time.Time, ttl time.Duration) (*quality.Evidence, error) {
	invalid := func() (*quality.Evidence, error) {
		return nil, &ProviderError{Code: CodeResponse, Message: "ip-api.com returned incomplete or mismatched evidence"}
	}
	var payload struct {
		Status      string `json:"status"`
		Message     string `json:"message"`
		Country     string `json:"country"`
		CountryCode string `json:"countryCode"`
		RegionName  string `json:"regionName"`
		City        string `json:"city"`
		ISP         string `json:"isp"`
		Org         string `json:"org"`
		AS          string `json:"as"`
		ASName      string `json:"asname"`
		Mobile      *bool  `json:"mobile"`
		Proxy       *bool  `json:"proxy"`
		Hosting     *bool  `json:"hosting"`
		Query       string `json:"query"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return invalid()
	}
	switch payload.Status {
	case "success":
	case "fail":
		text := strings.ToLower(payload.Message)
		switch {
		case strings.Contains(text, "reserved") || strings.Contains(text, "invalid"):
			return nil, &ProviderError{Code: CodeUnsupported, Message: "ip-api.com rejected the address"}
		case strings.Contains(text, "limit"):
			return nil, &ProviderError{Code: CodeLimit, Message: "ip-api.com request limit reached", RetryAfter: time.Minute}
		default:
			return nil, &ProviderError{Code: CodeResponse, Message: "ip-api.com did not accept the query"}
		}
	default:
		return invalid()
	}
	queried, err := netip.ParseAddr(strings.TrimSpace(payload.Query))
	if err != nil {
		return invalid()
	}
	if expect.IsValid() && queried.Unmap() != expect.Unmap() {
		return nil, &ProviderError{
			Code:    CodeEgressMismat,
			Message: "ip-api.com answered for a different address than this node's egress",
		}
	}
	if payload.Country == "" && payload.CountryCode == "" && payload.ISP == "" && payload.AS == "" {
		return invalid()
	}
	asn, organization := parseASField(payload.AS, payload.ASName)
	if organization == "" {
		organization = payload.Org
	}
	if organization == "" {
		organization = payload.ISP
	}
	signals := quality.Signals{Proxy: payload.Proxy, Hosting: payload.Hosting}
	ipType := "unknown"
	switch {
	case payload.Hosting != nil && *payload.Hosting:
		ipType = "datacenter"
	case payload.Mobile != nil && *payload.Mobile:
		ipType = "mobile"
	}
	evidence := &quality.Evidence{
		IP: queried.Unmap().String(), Provider: "ip_api", Profile: IPAPIProfile,
		IPType: ipType, SourceType: boundedText(payload.ISP, 80),
		Organization: boundedText(organization, 240),
		ASN:          boundedText(payload.AS, 60),
		CountryCode:  boundedText(strings.ToUpper(strings.TrimSpace(payload.CountryCode)), 8),
		City:         boundedText(payload.City, 120),
		Region:       boundedText(payload.RegionName, 120),
		IsMobile:     payload.Mobile, Signals: signals,
		Grade: quality.RiskGrade(nil, signals), ObservedAt: now, ValidUntil: now.Add(ttl),
	}
	if asn > 0 {
		evidence.ASNNumber = asn
	}
	return evidence, nil
}

// parseASField splits an "AS13335 Cloudflare, Inc." field.
func parseASField(raw, asName string) (int, string) {
	text := strings.TrimSpace(raw)
	organization := strings.TrimSpace(asName)
	if text == "" {
		return 0, organization
	}
	head, rest, found := strings.Cut(text, " ")
	if !found {
		head, rest = text, ""
	}
	number := asnNumber(head)
	if organization == "" {
		organization = rest
	}
	return number, organization
}

// ViaNodeOutboundDialer resolves a node hash to a dialable outbound. It is
// implemented by the node pool / outbound manager wiring.
type ViaNodeOutboundDialer interface {
	DialContext(ctx context.Context, nodeHash string) (adapter.Outbound, error)
}

// ErrNodeNotReady marks a node that has no usable outbound yet.
var ErrNodeNotReady = errors.New("node outbound is not ready")

// ---------------------------------------------------------------------------
// proxycheck.io (via-node, anonymous)
// ---------------------------------------------------------------------------

// ProxyCheckViaNodeURL is the proxycheck.io v3 endpoint prefix.
//
// proxycheck.io has no anonymous "what is my IP" mode: a request without an
// address is rejected with HTTP 400 "No valid IP Addresses supplied.", and the
// documented origin-IP behaviour belongs to a public API key in a browser
// context, which a server cannot use. What does work is naming the address
// explicitly while the request itself leaves through that same node: the vendor
// then attributes the query to the node's egress address, so the anonymous
// quota is spent per node instead of per Prism host. That is the only way to
// cover an inventory of thousands of nodes without a key, and it needs no
// credential at all, which is exactly what makes it safe to run through an
// untrusted outbound.
const ProxyCheckViaNodeURL = "https://proxycheck.io/v3/"

// ProxyCheckNodeProfile is the normalised contract of the via-node variant. It
// is separate from ProxyCheckProfile so a decoder change can invalidate one
// without discarding the other.
const ProxyCheckNodeProfile = "proxycheck-via-node-v3-2"

// ProxyCheckViaNodeOptions configures the anonymous via-node data source.
type ProxyCheckViaNodeOptions struct {
	URL     string
	TTL     time.Duration
	Timeout time.Duration
	Now     func() time.Time
}

// ProxyCheckViaNode reports proxycheck.io's verdict for a node's own egress
// address, queried through that same node so the vendor's anonymous quota
// belongs to the node. It never carries a key: a credential must not travel
// through an untrusted outbound (WP09 §1).
type ProxyCheckViaNode struct {
	spec    Spec
	url     string
	ttl     time.Duration
	timeout time.Duration
	now     func() time.Time
}

// NewProxyCheckViaNodeProvider builds the anonymous via-node provider.
func NewProxyCheckViaNodeProvider(opts ProxyCheckViaNodeOptions) *ProxyCheckViaNode {
	target := strings.TrimSpace(opts.URL)
	if target == "" {
		target = ProxyCheckViaNodeURL
	}
	if !strings.HasSuffix(target, "/") {
		target += "/"
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &ProxyCheckViaNode{
		spec: Spec{
			ID: "proxycheck_node", Name: "proxycheck.io (via node)",
			Website: "https://proxycheck.io/",
			Terms: "Anonymous queries must not be used commercially and share the quota of the " +
				"requesting address (100/day). Because the query leaves through the node and " +
				"names that same node, the quota is spent per node - which is what makes a " +
				"large inventory affordable without a key. Prism counts a per-node daily " +
				"budget (default 100) and keeps a provider-wide QPS valve (default 2/s). A key " +
				"can raise the quota, but a key is never sent through a node: enter it on the " +
				"host-side proxycheck.io source instead. See https://proxycheck.io/pricing/.",
			Kind: KindViaNode, Profile: ProxyCheckNodeProfile,
			RequiresKey: false, DefaultEnabled: true,
			DefaultDailyLimit: 100, DefaultQPS: 2, BatchSize: 1,
			DefaultTTL: ttl, SupportsIPv6: true,
		},
		url: target, ttl: ttl, timeout: timeout, now: now,
	}
}

// Spec implements ViaNodeProvider.
func (p *ProxyCheckViaNode) Spec() Spec { return p.spec }

// Lookup implements ViaNodeProvider. The address is both the query target and -
// because the request leaves through that same node - the owner of the quota.
func (p *ProxyCheckViaNode) Lookup(ctx context.Context, ob adapter.Outbound, expect netip.Addr) Result {
	if !expect.IsValid() {
		return Failure(CodeRequest, "proxycheck.io via-node needs the node's own egress address")
	}
	address := expect.Unmap().String()
	if expect.Is6() {
		// An IPv6 literal needs brackets in a URL path.
		address = "[" + address + "]"
	}
	target := p.url + address + "?ver=24-June-2026&tag=0"
	resp, failure := fetchViaNode(ctx, ob, target, nil, p.timeout)
	if failure.Failed() {
		return failure
	}
	evidence, err := DecodeProxyCheckAs(resp.Body, expect, p.now().UTC(), p.ttl,
		"proxycheck_node", ProxyCheckNodeProfile)
	if err != nil {
		return FailureErr(err, "proxycheck.io returned incomplete or mismatched evidence")
	}
	return Result{Evidence: evidence, Raw: boundedRaw(resp.Body)}
}
