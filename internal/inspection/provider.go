package inspection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"prism/internal/quality"
)

type Provider interface {
	Lookup(context.Context, netip.Addr) (*quality.Evidence, error)
}

type ProviderError struct {
	Code       string
	Message    string
	RetryAfter time.Duration
	Pause      bool
}

func (e *ProviderError) Error() string { return e.Message }

type ProxyCheckProvider struct {
	client  *http.Client
	apiKey  string
	baseURL string
	ttl     time.Duration
}

func NewProxyCheckProvider(apiKey string, timeout, ttl time.Duration) *ProxyCheckProvider {
	return &ProxyCheckProvider{
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				// Provider credentials are never sent through an inventory node
				// or an inherited HTTP_PROXY configuration.
				DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
				ForceAttemptHTTP2:     true,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: timeout,
				MaxIdleConns:          2, MaxIdleConnsPerHost: 2,
				IdleConnTimeout: 60 * time.Second,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		apiKey: apiKey, baseURL: "https://proxycheck.io/v3/", ttl: ttl,
	}
}

func (p *ProxyCheckProvider) Close() { p.client.CloseIdleConnections() }

func (p *ProxyCheckProvider) Lookup(ctx context.Context, ip netip.Addr) (*quality.Evidence, error) {
	ip, err := quality.PublicIP(ip)
	if err != nil {
		return nil, err
	}
	target, _ := url.Parse(p.baseURL + ip.String())
	query := target.Query()
	query.Set("ver", "24-June-2026")
	query.Set("tag", "0")
	if p.apiKey != "" {
		query.Set("key", p.apiKey)
	}
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, &ProviderError{Code: "PROVIDER_REQUEST", Message: "Unable to create provider request"}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Prism/1.0 (IP quality inspection)")
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Do not propagate url.Error: its URL may contain the provider key.
		return nil, &ProviderError{Code: "PROVIDER_UNAVAILABLE", Message: "Quality provider could not be reached"}
	}
	defer resp.Body.Close()
	retryAfter := boundedRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &ProviderError{Code: "PROVIDER_LIMIT", Message: "Quality provider request limit reached", RetryAfter: retryAfter, Pause: true}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &ProviderError{Code: "PROVIDER_AUTH", Message: "Quality provider rejected the credentials", Pause: true}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &ProviderError{Code: "PROVIDER_UNAVAILABLE", Message: "Quality provider returned an unsuccessful response"}
	}
	const maxBody = 64 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return nil, &ProviderError{Code: "PROVIDER_RESPONSE", Message: "Quality provider response is unreadable or too large"}
	}
	return decodeProxyCheck(body, ip, time.Now().UTC(), p.ttl)
}

func boundedRetryAfter(raw string, now time.Time) time.Duration {
	delay := time.Hour
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		delay = time.Duration(min(seconds, 86400)) * time.Second
	} else if date, err := http.ParseTime(raw); err == nil && date.After(now) {
		delay = date.Sub(now)
	}
	return min(max(delay, time.Minute), 24*time.Hour)
}

func decodeProxyCheck(body []byte, ip netip.Addr, now time.Time, ttl time.Duration) (*quality.Evidence, error) {
	invalid := func() (*quality.Evidence, error) {
		return nil, &ProviderError{Code: "PROVIDER_RESPONSE", Message: "Quality provider returned incomplete or mismatched evidence"}
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return invalid()
	}
	var status, message string
	_ = json.Unmarshal(envelope["status"], &status)
	_ = json.Unmarshal(envelope["message"], &message)
	if status != "ok" && status != "warning" {
		code := "PROVIDER_RESPONSE"
		text := strings.ToLower(message)
		if strings.Contains(text, "limit") || strings.Contains(text, "quota") || strings.Contains(text, "queries") {
			code = "PROVIDER_LIMIT"
		} else if strings.Contains(text, "key") || strings.Contains(text, "auth") || status == "denied" {
			code = "PROVIDER_AUTH"
		}
		return nil, &ProviderError{Code: code, Message: "Quality provider did not accept the query", Pause: code != "PROVIDER_RESPONSE", RetryAfter: 24 * time.Hour}
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
	// The published OpenAPI and live v3 response currently disagree on the
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
		IP: ip.Unmap().String(), Provider: quality.ProviderID, Profile: quality.ProfileID,
		IPType: networkType, SourceType: boundedText(result.Network.Type, 80),
		ASN: boundedText(result.Network.ASN, 40), Organization: boundedText(organization, 240),
		CountryCode:     boundedText(result.Location.CountryCode, 8),
		NetworkProvider: boundedText(result.Network.Provider, 240),
		RiskScore:       d.Risk, SourceConfidence: d.Confidence, Signals: d.Signals,
		Grade: quality.RiskGrade(d.Risk, d.Signals), ObservedAt: now, ValidUntil: now.Add(ttl),
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

func boundedText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func providerFailure(err error) *ProviderError {
	var failure *ProviderError
	if errors.As(err, &failure) {
		return failure
	}
	return &ProviderError{Code: "PROVIDER_UNAVAILABLE", Message: "Quality inspection failed"}
}
