package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"prism/internal/quality"
)

// Vendor profiles. The profile string is the normalised contract of one
// provider response shape; a decoder change bumps it and invalidates old rows.
const (
	ProxyCheckProfile  = "proxycheck-v3-2"
	AbuseIPDBProfile   = "abuseipdb-v2-30d-2"
	IPQSProfile        = "ipqs-v1-2"
	IPAPIISProfile     = "ipapi-is-v2-2"
	DNSBLProfile       = "dnsbl-zones-v1"
	IPPureProfile      = "ippure-via-node-v2"
	IPAPIProfile       = "ip-api-via-node-v2"
	TorProfile         = "onionoo-public-relays-v2"
	DBIPProfile        = "dbip-lite-v1"
	MaxMindProfile     = "maxmind-geolite2-v1"
	IPInfoProfile      = "ipinfo-lite-v1"
	GeoCountryProfile  = "country-mmdb-v1"
	providerUserAgent  = "Prism/1.0 (intel IP inspection)"
	abuseIPDBMaxAgeDay = 30
)

// ---------------------------------------------------------------------------
// proxycheck.io
// ---------------------------------------------------------------------------

// ProxyCheckOptions configures the proxycheck.io data source.
type ProxyCheckOptions struct {
	Key     string
	TTL     time.Duration
	Timeout time.Duration
	// BaseURL overrides the endpoint; tests point it at httptest.
	BaseURL string
	Client  *http.Client
	Now     func() time.Time
}

// ProxyCheck performs proxycheck.io risk lookups.
type ProxyCheck struct {
	spec    Spec
	key     string
	ttl     time.Duration
	client  *http.Client
	baseURL string
	now     func() time.Time
}

// NewProxyCheckProvider builds the proxycheck.io provider.
func NewProxyCheckProvider(opts ProxyCheckOptions) *ProxyCheck {
	client := opts.Client
	if client == nil {
		client = NewStrictClient(opts.Timeout)
	}
	base := strings.TrimSpace(opts.BaseURL)
	if base == "" {
		base = "https://proxycheck.io/v3/"
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &ProxyCheck{
		spec: Spec{
			ID: "proxycheck", Name: "proxycheck.io", Website: "https://proxycheck.io/",
			Terms: "Anonymous queries share the quota of the Prism host's public IP " +
				"(80/day) and must not be used commercially. A key raises the quota to the " +
				"plan limit (Prism defaults to 900/day, configurable up to 1,000,000). " +
				"See https://proxycheck.io/pricing/.",
			// Off by default: the anonymous quota of this source is the Prism host's
			// own 80/day, while proxycheck_node spends the same vendor's quota per
			// node through the node itself. Enable this one when an API key is
			// available - a key must never travel through an inventory node.
			Kind: KindOnlineIP, Profile: ProxyCheckProfile,
			RequiresKey: false, DefaultEnabled: false,
			DefaultDailyLimit: 80, DefaultDailyLimitWithKey: 900, MaxDailyLimit: 1_000_000,
			DefaultQPS: 1, BatchSize: 1, DefaultTTL: ttl, SupportsIPv6: true,
		},
		key: strings.TrimSpace(opts.Key), ttl: ttl, client: client, baseURL: base, now: now,
	}
}

// Spec implements OnlineProvider.
func (p *ProxyCheck) Spec() Spec { return p.spec }

// Lookup implements OnlineProvider. v3 is queried one address at a time until
// the vendor confirms batching, so BatchSize is 1 (WP09 §3).
func (p *ProxyCheck) Lookup(ctx context.Context, ips []netip.Addr) map[netip.Addr]Result {
	out := make(map[netip.Addr]Result, len(ips))
	for _, raw := range ips {
		ip, unsupported, ok := PublicIP(raw)
		if !ok {
			out[raw] = unsupported
			continue
		}
		out[raw] = p.lookupOne(ctx, ip)
	}
	return out
}

func (p *ProxyCheck) lookupOne(ctx context.Context, ip netip.Addr) Result {
	target, err := url.Parse(p.baseURL + url.PathEscape(ip.String()))
	if err != nil {
		return Failure(CodeRequest, "proxycheck.io request could not be built")
	}
	query := target.Query()
	query.Set("ver", "24-June-2026")
	query.Set("tag", "0")
	if p.key != "" {
		query.Set("key", p.key)
	}
	target.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Failure(CodeRequest, "proxycheck.io request could not be built")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", providerUserAgent)

	resp, failure := performRequest(ctx, p.client, req, "proxycheck.io")
	if failure.Failed() {
		return failure
	}
	evidence, err := DecodeProxyCheck(resp.Body, ip, p.now().UTC(), p.ttl)
	if err != nil {
		return FailureErr(err, "proxycheck.io returned incomplete or mismatched evidence")
	}
	return Result{Evidence: evidence, Raw: boundedRaw(resp.Body)}
}

// DecodeProxyCheck validates one proxycheck.io v3 payload against the queried
// address.
func DecodeProxyCheck(body []byte, ip netip.Addr, now time.Time, ttl time.Duration) (*quality.Evidence, error) {
	return DecodeProxyCheckAs(body, ip, now, ttl, "proxycheck", ProxyCheckProfile)
}

// DecodeProxyCheckAs is DecodeProxyCheck with an explicit source identity, so
// the anonymous via-node variant can record its own provider id and profile
// while sharing one strict decoder. It is strict: contradictory risk fields,
// out-of-range scores, missing detections and a mismatched address are all
// rejected.
func DecodeProxyCheckAs(body []byte, ip netip.Addr, now time.Time, ttl time.Duration, provider, profile string) (*quality.Evidence, error) {
	invalid := func() (*quality.Evidence, error) {
		return nil, &ProviderError{Code: CodeResponse, Message: "proxycheck.io returned incomplete or mismatched evidence"}
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return invalid()
	}
	var status, message string
	_ = json.Unmarshal(envelope["status"], &status)
	_ = json.Unmarshal(envelope["message"], &message)
	if status != "ok" && status != "warning" {
		code := CodeResponse
		text := strings.ToLower(message)
		switch {
		case strings.Contains(text, "limit") || strings.Contains(text, "quota") || strings.Contains(text, "queries"):
			code = CodeLimit
		case strings.Contains(text, "key") || strings.Contains(text, "auth") || status == "denied":
			code = CodeAuth
		}
		return nil, &ProviderError{
			Code: code, Message: "proxycheck.io did not accept the query",
			Pause: code != CodeResponse, RetryAfter: 24 * time.Hour,
		}
	}
	var raw json.RawMessage
	for key, value := range envelope {
		address, err := netip.ParseAddr(key)
		if err == nil && address.Unmap() == ip.Unmap() {
			raw = value
			break
		}
	}
	if raw == nil {
		return invalid()
	}
	var result struct {
		Risk    *int `json:"risk"`
		Network struct {
			ASN          string `json:"asn"`
			Organization string `json:"organisation"`
			Provider     string `json:"provider"`
			Type         string `json:"type"`
		} `json:"network"`
		Location struct {
			CountryCode string `json:"country_code"`
			City        string `json:"city"`
			Region      string `json:"region"`
		} `json:"location"`
		Detections *struct {
			quality.Signals
			Risk       *int `json:"risk"`
			Confidence *int `json:"confidence"`
		} `json:"detections"`
		LastUpdated string `json:"last_updated"`
		Operator    *struct {
			Name     string   `json:"name"`
			Services []string `json:"services"`
		} `json:"operator"`
		AttackHistory map[string]int `json:"attack_history"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Detections == nil {
		return invalid()
	}
	d := result.Detections
	// The published OpenAPI and the live v3 response currently disagree on the
	// risk field location. Accept either, reject contradictory values.
	if result.Risk != nil {
		if d.Risk != nil && *d.Risk != *result.Risk {
			return invalid()
		}
		d.Risk = result.Risk
	}
	hasDetection := d.Risk != nil
	for _, signal := range []*bool{d.Proxy, d.VPN, d.Tor, d.Hosting, d.Compromised, d.Scraper, d.Anonymous} {
		hasDetection = hasDetection || signal != nil
	}
	if !hasDetection {
		return invalid()
	}
	if (d.Risk != nil && (*d.Risk < 0 || *d.Risk > 100)) ||
		(d.Confidence != nil && (*d.Confidence < 0 || *d.Confidence > 100)) {
		return invalid()
	}
	networkType := quality.NetworkType(result.Network.Type, d.Signals)
	organization := result.Network.Organization
	if organization == "" {
		organization = result.Network.Provider
	}
	evidence := &quality.Evidence{
		IP: ip.Unmap().String(), Provider: provider, Profile: profile,
		IPType: networkType, SourceType: boundedText(result.Network.Type, 80),
		ASN: boundedText(result.Network.ASN, 40), Organization: boundedText(organization, 240),
		CountryCode:     boundedText(result.Location.CountryCode, 8),
		City:            boundedText(result.Location.City, 120),
		Region:          boundedText(result.Location.Region, 120),
		UsageType:       boundedText(result.Network.Type, 80),
		NetworkProvider: boundedText(result.Network.Provider, 240),
		RiskScore:       d.Risk, SourceConfidence: d.Confidence, Signals: d.Signals,
		Grade: quality.RiskGrade(d.Risk, d.Signals), ObservedAt: now, ValidUntil: now.Add(ttl),
	}
	if number := asnNumber(result.Network.ASN); number > 0 {
		evidence.ASNNumber = number
	}
	if result.Operator != nil {
		evidence.Operator = boundedText(result.Operator.Name, 120)
		for _, service := range result.Operator.Services {
			if len(evidence.OperatorServices) == 6 {
				break
			}
			if service = boundedText(service, 48); service != "" {
				evidence.OperatorServices = append(evidence.OperatorServices, service)
			}
		}
	}
	attackKeys := make([]string, 0, len(result.AttackHistory))
	for name := range result.AttackHistory {
		attackKeys = append(attackKeys, name)
	}
	sort.Strings(attackKeys)
	for _, name := range attackKeys {
		count := result.AttackHistory[name]
		if count < 0 {
			return invalid()
		}
		if count == 0 || len(evidence.AttackHistory) >= 8 {
			continue
		}
		if evidence.AttackHistory == nil {
			evidence.AttackHistory = make(map[string]int)
		}
		evidence.AttackHistory[boundedText(name, 48)] = min(count, 1_000_000_000)
	}
	if updated, err := time.Parse(time.RFC3339, result.LastUpdated); err == nil && !updated.After(now.Add(time.Minute)) {
		evidence.SourceUpdatedAt = &updated
	}
	if networkType == "conflicting" {
		evidence.Grade = "unknown"
	}
	return evidence, nil
}

// ---------------------------------------------------------------------------
// AbuseIPDB
// ---------------------------------------------------------------------------

// AbuseIPDBOptions configures the AbuseIPDB data source.
type AbuseIPDBOptions struct {
	Key     string
	TTL     time.Duration
	Timeout time.Duration
	BaseURL string
	Client  *http.Client
	Now     func() time.Time
}

// AbuseIPDB performs AbuseIPDB abuse-confidence lookups.
type AbuseIPDB struct {
	spec    Spec
	key     string
	ttl     time.Duration
	client  *http.Client
	baseURL string
	now     func() time.Time
}

// NewAbuseIPDBProvider builds the AbuseIPDB provider.
func NewAbuseIPDBProvider(opts AbuseIPDBOptions) *AbuseIPDB {
	client := opts.Client
	if client == nil {
		client = NewStrictClient(opts.Timeout)
	}
	base := strings.TrimSpace(opts.BaseURL)
	if base == "" {
		base = "https://api.abuseipdb.com/api/v2/check"
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &AbuseIPDB{
		spec: Spec{
			ID: "abuseipdb", Name: "AbuseIPDB", Website: "https://www.abuseipdb.com/",
			Terms: "A free account key allows 1,000 checks per day; Prism defaults to 900 to " +
				"leave headroom. Reports are licensed CC BY 4.0 and the API may not be used " +
				"to build a competing blocklist. See https://www.abuseipdb.com/pricing.",
			Kind: KindOnlineIP, Profile: AbuseIPDBProfile,
			RequiresKey: true, DefaultEnabled: false,
			DefaultDailyLimit: 900, MaxDailyLimit: 100_000,
			DefaultQPS: 1, BatchSize: 1, DefaultTTL: ttl, SupportsIPv6: true,
		},
		key: strings.TrimSpace(opts.Key), ttl: ttl, client: client, baseURL: base, now: now,
	}
}

// Spec implements OnlineProvider.
func (p *AbuseIPDB) Spec() Spec { return p.spec }

// Lookup implements OnlineProvider.
func (p *AbuseIPDB) Lookup(ctx context.Context, ips []netip.Addr) map[netip.Addr]Result {
	out := make(map[netip.Addr]Result, len(ips))
	for _, raw := range ips {
		ip, unsupported, ok := PublicIP(raw)
		if !ok {
			out[raw] = unsupported
			continue
		}
		out[raw] = p.lookupOne(ctx, ip)
	}
	return out
}

func (p *AbuseIPDB) lookupOne(ctx context.Context, ip netip.Addr) Result {
	if p.key == "" {
		return Result{Err: &ProviderError{
			Code: CodeAuth, Message: "AbuseIPDB requires an API key", Pause: true,
		}}
	}
	target, err := url.Parse(p.baseURL)
	if err != nil {
		return Failure(CodeRequest, "AbuseIPDB request could not be built")
	}
	query := target.Query()
	query.Set("ipAddress", ip.String())
	query.Set("maxAgeInDays", strconv.Itoa(abuseIPDBMaxAgeDay))
	target.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Failure(CodeRequest, "AbuseIPDB request could not be built")
	}
	req.Header.Set("Key", p.key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", providerUserAgent)

	resp, failure := performRequest(ctx, p.client, req, "AbuseIPDB")
	if failure.Failed() {
		return failure
	}
	evidence, err := DecodeAbuseIPDB(resp.Body, ip, p.now().UTC(), p.ttl)
	if err != nil {
		return FailureErr(err, "AbuseIPDB returned incomplete or mismatched evidence")
	}
	return Result{Evidence: evidence, Raw: boundedRaw(resp.Body)}
}

// DecodeAbuseIPDB validates one AbuseIPDB v2 payload.
func DecodeAbuseIPDB(body []byte, ip netip.Addr, now time.Time, ttl time.Duration) (*quality.Evidence, error) {
	invalid := func() (*quality.Evidence, error) {
		return nil, &ProviderError{Code: CodeResponse, Message: "AbuseIPDB returned incomplete or mismatched evidence"}
	}
	var payload struct {
		Data *struct {
			IPAddress      string     `json:"ipAddress"`
			IsPublic       *bool      `json:"isPublic"`
			CountryCode    string     `json:"countryCode"`
			UsageType      string     `json:"usageType"`
			ISP            string     `json:"isp"`
			IsTor          *bool      `json:"isTor"`
			Confidence     *int       `json:"abuseConfidenceScore"`
			Reports        *int       `json:"totalReports"`
			Reporters      *int       `json:"numDistinctUsers"`
			LastReportedAt *time.Time `json:"lastReportedAt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Data == nil {
		return invalid()
	}
	d := payload.Data
	returnedIP, err := netip.ParseAddr(d.IPAddress)
	if err != nil || returnedIP.Unmap() != ip.Unmap() || (d.IsPublic != nil && !*d.IsPublic) ||
		d.Confidence == nil || *d.Confidence < 0 || *d.Confidence > 100 ||
		d.Reports == nil || *d.Reports < 0 || d.Reporters == nil || *d.Reporters < 0 {
		return invalid()
	}
	ipType := "unknown"
	switch d.UsageType {
	case "Data Center/Web Hosting/Transit":
		ipType = "datacenter"
	case "Mobile ISP":
		ipType = "mobile"
	}
	return &quality.Evidence{
		IP: ip.Unmap().String(), Provider: "abuseipdb", Profile: AbuseIPDBProfile,
		IPType: ipType, SourceType: boundedText(d.UsageType, 80), UsageType: boundedText(d.UsageType, 80),
		Organization: boundedText(d.ISP, 240), CountryCode: boundedText(d.CountryCode, 8),
		Grade: "unknown", Signals: quality.Signals{Tor: d.IsTor},
		AbuseConfidence: d.Confidence, TotalReports: d.Reports, DistinctReporters: d.Reporters,
		LastReportedAt: d.LastReportedAt, ReportWindowDays: abuseIPDBMaxAgeDay,
		ObservedAt: now, ValidUntil: now.Add(ttl),
	}, nil
}

// ---------------------------------------------------------------------------
// IPQualityScore
// ---------------------------------------------------------------------------

// IPQSOptions configures the IPQualityScore data source.
type IPQSOptions struct {
	Key     string
	TTL     time.Duration
	Timeout time.Duration
	// BaseURL must not contain the key path segment; tests point it at httptest.
	BaseURL string
	Client  *http.Client
	Now     func() time.Time
}

// IPQS performs IPQualityScore fraud-score lookups.
type IPQS struct {
	spec    Spec
	key     string
	ttl     time.Duration
	client  *http.Client
	baseURL string
	now     func() time.Time
}

// NewIPQSProvider builds the IPQualityScore provider.
func NewIPQSProvider(opts IPQSOptions) *IPQS {
	client := opts.Client
	if client == nil {
		client = NewStrictClient(opts.Timeout)
	}
	base := strings.TrimSpace(opts.BaseURL)
	if base == "" {
		base = "https://ipqualityscore.com/api/json/ip"
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = 72 * time.Hour
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &IPQS{
		spec: Spec{
			ID: "ipqs", Name: "IPQualityScore", Website: "https://www.ipqualityscore.com/",
			Terms: "Requires a paid or trial key; the free tier allows 5,000 lookups per month " +
				"and rejects commercial use without a plan. Prism defaults to 150/day and 1 QPS. " +
				"See https://www.ipqualityscore.com/documentation/proxy-detection-api/overview.",
			Kind: KindOnlineIP, Profile: IPQSProfile,
			RequiresKey: true, DefaultEnabled: false,
			DefaultDailyLimit: 150, MaxDailyLimit: 1_000_000,
			DefaultQPS: 1, BatchSize: 1, DefaultTTL: ttl, SupportsIPv6: true,
		},
		key: strings.TrimSpace(opts.Key), ttl: ttl, client: client, baseURL: base, now: now,
	}
}

// Spec implements OnlineProvider.
func (p *IPQS) Spec() Spec { return p.spec }

// Lookup implements OnlineProvider.
func (p *IPQS) Lookup(ctx context.Context, ips []netip.Addr) map[netip.Addr]Result {
	out := make(map[netip.Addr]Result, len(ips))
	for _, raw := range ips {
		ip, unsupported, ok := PublicIP(raw)
		if !ok {
			out[raw] = unsupported
			continue
		}
		out[raw] = p.lookupOne(ctx, ip)
	}
	return out
}

func (p *IPQS) lookupOne(ctx context.Context, ip netip.Addr) Result {
	if p.key == "" {
		return Result{Err: &ProviderError{Code: CodeAuth, Message: "IPQualityScore requires an API key", Pause: true}}
	}
	target, err := url.Parse(strings.TrimRight(p.baseURL, "/") + "/" + url.PathEscape(p.key) + "/" + url.PathEscape(ip.String()))
	if err != nil {
		return Failure(CodeRequest, "IPQualityScore request could not be built")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Failure(CodeRequest, "IPQualityScore request could not be built")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", providerUserAgent)

	resp, failure := performRequest(ctx, p.client, req, "IPQualityScore")
	if failure.Failed() {
		return failure
	}
	evidence, err := DecodeIPQS(resp.Body, ip, p.now().UTC(), p.ttl)
	if err != nil {
		return FailureErr(err, "IPQualityScore returned incomplete or mismatched evidence")
	}
	// The response body embeds the key in no field, but the request URL does;
	// only the decoded JSON is stored, never the request line (R6).
	return Result{Evidence: evidence, Raw: boundedRaw(resp.Body)}
}

// DecodeIPQS validates one IPQualityScore payload.
func DecodeIPQS(body []byte, ip netip.Addr, now time.Time, ttl time.Duration) (*quality.Evidence, error) {
	invalid := func() (*quality.Evidence, error) {
		return nil, &ProviderError{Code: CodeResponse, Message: "IPQualityScore returned incomplete or mismatched evidence"}
	}
	var payload struct {
		Success        *bool   `json:"success"`
		Message        string  `json:"message"`
		IP             string  `json:"ip_address"`
		FraudScore     *int    `json:"fraud_score"`
		Proxy          *bool   `json:"proxy"`
		VPN            *bool   `json:"vpn"`
		Tor            *bool   `json:"tor"`
		ActiveTor      *bool   `json:"active_tor"`
		RecentAbuse    *bool   `json:"recent_abuse"`
		BotStatus      *bool   `json:"bot_status"`
		ConnectionType string  `json:"connection_type"`
		ISP            string  `json:"ISP"`
		Organization   string  `json:"organization"`
		ASN            *int    `json:"ASN"`
		CountryCode    string  `json:"country_code"`
		City           string  `json:"city"`
		Region         string  `json:"region"`
		Mobile         *bool   `json:"mobile"`
		IsCrawler      *bool   `json:"is_crawler"`
		AbuseVelocity  string  `json:"abuse_velocity"`
		ProxyS         *string `json:"proxy_type"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return invalid()
	}
	if payload.Success == nil || !*payload.Success {
		code := CodeResponse
		text := strings.ToLower(payload.Message)
		switch {
		case strings.Contains(text, "quota") || strings.Contains(text, "limit") || strings.Contains(text, "exceed"):
			code = CodeLimit
		case strings.Contains(text, "key") || strings.Contains(text, "authoriz") || strings.Contains(text, "invalid"):
			code = CodeAuth
		}
		return nil, &ProviderError{Code: code, Message: "IPQualityScore did not accept the query", Pause: code == CodeAuth}
	}
	if payload.IP != "" {
		returnedIP, err := netip.ParseAddr(payload.IP)
		if err != nil || returnedIP.Unmap() != ip.Unmap() {
			return invalid()
		}
	}
	if payload.FraudScore == nil || *payload.FraudScore < 0 || *payload.FraudScore > 100 {
		return invalid()
	}
	if payload.ASN != nil && *payload.ASN < 0 {
		return invalid()
	}
	ipType := ipqsIPType(payload.ConnectionType)
	signals := quality.Signals{Proxy: payload.Proxy, VPN: payload.VPN, Tor: payload.Tor, Scraper: payload.IsCrawler}
	if signals.Tor == nil {
		signals.Tor = payload.ActiveTor
	}
	organization := payload.Organization
	if organization == "" {
		organization = payload.ISP
	}
	evidence := &quality.Evidence{
		IP: ip.Unmap().String(), Provider: "ipqs", Profile: IPQSProfile,
		IPType: ipType, SourceType: boundedText(payload.ConnectionType, 80),
		UsageType: boundedText(payload.ConnectionType, 80), Organization: boundedText(organization, 240),
		CountryCode: boundedText(payload.CountryCode, 8), City: boundedText(payload.City, 120),
		Region: boundedText(payload.Region, 120), FraudScore: payload.FraudScore,
		IsMobile: payload.Mobile, Signals: signals, Grade: quality.RiskGrade(payload.FraudScore, signals),
		SourceConfidence: payload.FraudScore, ObservedAt: now, ValidUntil: now.Add(ttl),
	}
	if payload.ASN != nil {
		evidence.ASNNumber = *payload.ASN
		evidence.ASN = "AS" + strconv.Itoa(*payload.ASN)
	}
	// recent_abuse is the vendor's short-window reputation flag; it is surfaced
	// as a signal the WP10 penalty table understands.
	if payload.RecentAbuse != nil && *payload.RecentAbuse {
		yes := true
		evidence.Signals.Compromised = nil
		evidence.Signals.Anonymous = &yes
	}
	if ipType == "conflicting" {
		evidence.Grade = "unknown"
	}
	return evidence, nil
}

func ipqsIPType(connectionType string) string {
	switch strings.ToLower(strings.TrimSpace(connectionType)) {
	case "residential":
		return "residential"
	case "mobile":
		return "mobile"
	case "corporate":
		return "business"
	case "data center", "datacenter":
		return "datacenter"
	case "education":
		return "business"
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// ipapi.is
// ---------------------------------------------------------------------------

// IPAPIISOptions configures the ipapi.is data source.
type IPAPIISOptions struct {
	Key     string
	TTL     time.Duration
	Timeout time.Duration
	BaseURL string
	Client  *http.Client
	Now     func() time.Time
}

// IPAPIIS performs ipapi.is IP-intelligence lookups.
type IPAPIIS struct {
	spec    Spec
	key     string
	ttl     time.Duration
	client  *http.Client
	baseURL string
	now     func() time.Time
}

// NewIPAPIISProvider builds the ipapi.is provider.
func NewIPAPIISProvider(opts IPAPIISOptions) *IPAPIIS {
	client := opts.Client
	if client == nil {
		client = NewStrictClient(opts.Timeout)
	}
	base := strings.TrimSpace(opts.BaseURL)
	if base == "" {
		base = "https://api.ipapi.is/"
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = 72 * time.Hour
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &IPAPIIS{
		spec: Spec{
			ID: "ipapi_is", Name: "ipapi.is", Website: "https://ipapi.is/",
			Terms: "Anonymous use is limited to roughly 1,000 requests per day and is not " +
				"licensed for commercial use without a plan or self-hosted database. " +
				"Prism defaults to 500/day and 1 QPS. See https://ipapi.is/.",
			Kind: KindOnlineIP, Profile: IPAPIISProfile,
			RequiresKey: false, DefaultEnabled: false,
			DefaultDailyLimit: 500, DefaultDailyLimitWithKey: 500, MaxDailyLimit: 1_000_000,
			DefaultQPS: 1, BatchSize: 1, DefaultTTL: ttl, SupportsIPv6: true,
		},
		key: strings.TrimSpace(opts.Key), ttl: ttl, client: client, baseURL: base, now: now,
	}
}

// Spec implements OnlineProvider.
func (p *IPAPIIS) Spec() Spec { return p.spec }

// Lookup implements OnlineProvider.
func (p *IPAPIIS) Lookup(ctx context.Context, ips []netip.Addr) map[netip.Addr]Result {
	out := make(map[netip.Addr]Result, len(ips))
	for _, raw := range ips {
		ip, unsupported, ok := PublicIP(raw)
		if !ok {
			out[raw] = unsupported
			continue
		}
		out[raw] = p.lookupOne(ctx, ip)
	}
	return out
}

func (p *IPAPIIS) lookupOne(ctx context.Context, ip netip.Addr) Result {
	target, err := url.Parse(p.baseURL)
	if err != nil {
		return Failure(CodeRequest, "ipapi.is request could not be built")
	}
	query := target.Query()
	query.Set("q", ip.String())
	target.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Failure(CodeRequest, "ipapi.is request could not be built")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", providerUserAgent)
	if p.key != "" {
		req.Header.Set("X-Api-Key", p.key)
	}

	resp, failure := performRequest(ctx, p.client, req, "ipapi.is")
	if failure.Failed() {
		return failure
	}
	evidence, err := DecodeIPAPIIS(resp.Body, ip, p.now().UTC(), p.ttl)
	if err != nil {
		return FailureErr(err, "ipapi.is returned incomplete or mismatched evidence")
	}
	return Result{Evidence: evidence, Raw: boundedRaw(resp.Body)}
}

// DecodeIPAPIIS validates one ipapi.is payload.
func DecodeIPAPIIS(body []byte, ip netip.Addr, now time.Time, ttl time.Duration) (*quality.Evidence, error) {
	invalid := func() (*quality.Evidence, error) {
		return nil, &ProviderError{Code: CodeResponse, Message: "ipapi.is returned incomplete or mismatched evidence"}
	}
	var payload struct {
		IP           string          `json:"ip"`
		IsDatacenter *bool           `json:"is_datacenter"`
		IsVPN        *bool           `json:"is_vpn"`
		IsProxy      *bool           `json:"is_proxy"`
		IsTor        *bool           `json:"is_tor"`
		IsAbuser     *bool           `json:"is_abuser"`
		IsCrawler    *bool           `json:"is_crawler"`
		IsMobile     *bool           `json:"is_mobile"`
		ASN          json.RawMessage `json:"asn"`
		Location     struct {
			Country     string `json:"country"`
			CountryCode string `json:"country_code"`
			City        string `json:"city"`
			State       string `json:"state"`
		} `json:"location"`
		Company struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"company"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return invalid()
	}
	returnedIP, err := netip.ParseAddr(payload.IP)
	if err != nil {
		return invalid()
	}
	yes := true
	if _, err := quality.PublicIP(returnedIP); err != nil || returnedIP.Unmap() != ip.Unmap() {
		return invalid()
	}
	asnBlock, hasASN := decodeIPAPIISASN(payload.ASN)
	if !hasASN && payload.Company.Name == "" && payload.Location.CountryCode == "" &&
		payload.IsDatacenter == nil && payload.IsProxy == nil {
		return invalid()
	}
	signals := quality.Signals{
		Proxy: payload.IsProxy, VPN: payload.IsVPN, Tor: payload.IsTor, Hosting: payload.IsDatacenter,
		Scraper: payload.IsCrawler,
	}
	if payload.IsAbuser != nil && *payload.IsAbuser {
		signals.Compromised = &yes
	}
	ipType := "unknown"
	switch {
	case payload.IsDatacenter != nil && *payload.IsDatacenter:
		ipType = "datacenter"
	case strings.EqualFold(payload.Company.Type, "hosting"):
		ipType = "datacenter"
	case strings.EqualFold(payload.Company.Type, "isp"):
		ipType = "residential"
	case strings.EqualFold(payload.Company.Type, "business"):
		ipType = "business"
	}
	evidence := &quality.Evidence{
		IP: ip.Unmap().String(), Provider: "ipapi_is", Profile: IPAPIISProfile,
		IPType: ipType, SourceType: boundedText(payload.Company.Type, 80),
		UsageType: boundedText(payload.Company.Type, 80), Organization: boundedText(payload.Company.Name, 240),
		CountryCode: boundedText(payload.Location.CountryCode, 8), City: boundedText(payload.Location.City, 120),
		Region: boundedText(payload.Location.State, 120), IsMobile: payload.IsMobile,
		Signals: signals, Grade: quality.RiskGrade(nil, signals), ObservedAt: now, ValidUntil: now.Add(ttl),
	}
	if asnBlock.ASN > 0 {
		evidence.ASNNumber = asnBlock.ASN
		evidence.ASN = "AS" + strconv.Itoa(asnBlock.ASN)
	}
	if asnBlock.Org != "" {
		evidence.Organization = boundedText(asnBlock.Org, 240)
	}
	if asnBlock.Country != "" {
		evidence.RegisteredCC = boundedText(asnBlock.Country, 8)
	}
	return evidence, nil
}

type ipapiISASN struct {
	ASN     int    `json:"asn"`
	Org     string `json:"org"`
	Country string `json:"country"`
	Type    string `json:"type"`
}

func decodeIPAPIISASN(raw json.RawMessage) (ipapiISASN, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return ipapiISNEmpty, false
	}
	var decoded ipapiISASN
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ipapiISNEmpty, false
	}
	if decoded.ASN < 0 {
		return ipapiISNEmpty, false
	}
	return decoded, true
}

var ipapiISNEmpty = ipapiISASN{}

// asnNumber parses the numeric part of an "AS13335" style field.
func asnNumber(raw string) int {
	trimmed := strings.ToUpper(strings.TrimSpace(raw))
	trimmed = strings.TrimPrefix(trimmed, "AS")
	value, err := strconv.Atoi(trimmed)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}
