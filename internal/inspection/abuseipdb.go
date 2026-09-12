package inspection

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"prism/internal/quality"
)

// AbuseIPDB complements network classification with recent abuse reports.
// Its confidence score is kept separate from ProxyCheck's risk score.
type AbuseIPDBProvider struct {
	client  *http.Client
	apiKey  string
	baseURL string
	ttl     time.Duration
}

func NewAbuseIPDBProvider(apiKey string, timeout, ttl time.Duration) *AbuseIPDBProvider {
	strictClient := NewProxyCheckProvider("", timeout, ttl).client
	return &AbuseIPDBProvider{client: strictClient, apiKey: apiKey, ttl: ttl, baseURL: "https://api.abuseipdb.com/api/v2/check"}
}

func (p *AbuseIPDBProvider) Close() { p.client.CloseIdleConnections() }

func (p *AbuseIPDBProvider) Lookup(ctx context.Context, ip netip.Addr) (*quality.Evidence, error) {
	ip, err := quality.PublicIP(ip)
	if err != nil {
		return nil, err
	}
	if p.apiKey == "" {
		return nil, &ProviderError{Code: "PROVIDER_AUTH", Message: "A free AbuseIPDB API key is required", Pause: true}
	}
	target, _ := url.Parse(p.baseURL)
	query := target.Query()
	query.Set("ipAddress", ip.String())
	query.Set("maxAgeInDays", "30")
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, &ProviderError{Code: "PROVIDER_REQUEST", Message: "Unable to create provider request"}
	}
	req.Header.Set("Key", p.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Prism/1.0 (IP quality inspection)")
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &ProviderError{Code: "PROVIDER_UNAVAILABLE", Message: "AbuseIPDB could not be reached"}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &ProviderError{Code: "PROVIDER_LIMIT", Message: "AbuseIPDB request limit reached", RetryAfter: boundedRetryAfter(resp.Header.Get("Retry-After"), time.Now()), Pause: true}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &ProviderError{Code: "PROVIDER_AUTH", Message: "AbuseIPDB rejected the credentials", Pause: true}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &ProviderError{Code: "PROVIDER_UNAVAILABLE", Message: "AbuseIPDB returned an unsuccessful response"}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		return nil, &ProviderError{Code: "PROVIDER_RESPONSE", Message: "AbuseIPDB response is unreadable or too large"}
	}
	return decodeAbuseIPDB(body, ip, time.Now().UTC(), p.ttl)
}

func decodeAbuseIPDB(body []byte, ip netip.Addr, now time.Time, ttl time.Duration) (*quality.Evidence, error) {
	invalid := func() (*quality.Evidence, error) {
		return nil, &ProviderError{Code: "PROVIDER_RESPONSE", Message: "AbuseIPDB returned incomplete or mismatched evidence"}
	}
	var response struct {
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
	if err := json.Unmarshal(body, &response); err != nil || response.Data == nil {
		return invalid()
	}
	d := response.Data
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
		IP: ip.Unmap().String(), Provider: quality.AbuseProviderID, Profile: quality.AbuseProfileID,
		IPType: ipType, SourceType: boundedText(d.UsageType, 80),
		Organization: boundedText(d.ISP, 240), CountryCode: boundedText(d.CountryCode, 8),
		Grade: "unknown", Signals: quality.Signals{Tor: d.IsTor},
		AbuseConfidence: d.Confidence, TotalReports: d.Reports, DistinctReporters: d.Reporters,
		LastReportedAt: d.LastReportedAt, ReportWindowDays: 30,
		ObservedAt: now, ValidUntil: now.Add(ttl),
	}, nil
}
