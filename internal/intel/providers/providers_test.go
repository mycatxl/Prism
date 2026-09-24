package providers

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"

	"prism/internal/quality"
)

var (
	testIP  = netip.MustParseAddr("8.8.8.8")
	testNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

// directOutbound dials the destination directly; it stands in for a node whose
// tunnel lands on the test server.
type directOutbound struct{}

func (directOutbound) Type() string           { return "direct" }
func (directOutbound) Tag() string            { return "test-direct" }
func (directOutbound) Network() []string      { return []string{"tcp"} }
func (directOutbound) Dependencies() []string { return nil }
func (directOutbound) Close() error           { return nil }
func (directOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("not supported")
}

func (directOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if network == "" {
		network = "tcp"
	}
	return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, destination.String())
}

var _ adapter.Outbound = directOutbound{}

func mustEvidence(t *testing.T, result Result) *quality.Evidence {
	t.Helper()
	if result.Err != nil {
		t.Fatalf("unexpected provider error: %s (%s)", result.Err.Code, result.Err.Message)
	}
	if result.Evidence == nil {
		t.Fatal("provider returned no evidence")
	}
	return result.Evidence
}

func expectCode(t *testing.T, result Result, code string) *ProviderError {
	t.Helper()
	if result.Err == nil {
		t.Fatalf("expected error %s, got evidence", code)
	}
	if result.Err.Code != code {
		t.Fatalf("expected error %s, got %s (%s)", code, result.Err.Code, result.Err.Message)
	}
	return result.Err
}

// --- proxycheck ------------------------------------------------------------

func TestDecodeProxyCheckSuccess(t *testing.T) {
	evidence, err := DecodeProxyCheck(fixture(t, "proxycheck/success.json"), testIP, testNow, 24*time.Hour)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if evidence.Provider != "proxycheck" || evidence.Profile != ProxyCheckProfile {
		t.Fatalf("unexpected provider/profile: %s/%s", evidence.Provider, evidence.Profile)
	}
	if evidence.RiskScore == nil || *evidence.RiskScore != 12 {
		t.Fatalf("risk score: %v", evidence.RiskScore)
	}
	if evidence.ASNNumber != 15169 || evidence.ASN != "AS15169" {
		t.Fatalf("asn: %d %q", evidence.ASNNumber, evidence.ASN)
	}
	if evidence.City != "Mountain View" || evidence.Region != "California" {
		t.Fatalf("location: %q %q", evidence.City, evidence.Region)
	}
	if evidence.IPType != "business" {
		t.Fatalf("ip type: %q", evidence.IPType)
	}
	if evidence.UsageType != "business" {
		t.Fatalf("usage type: %q", evidence.UsageType)
	}
	if evidence.AttackHistory["http"] != 3 {
		t.Fatalf("attack history: %v", evidence.AttackHistory)
	}
	if len(evidence.OperatorServices) != 2 || evidence.Operator != "Google Fiber" {
		t.Fatalf("operator: %q %v", evidence.Operator, evidence.OperatorServices)
	}
	if evidence.ValidUntil != testNow.Add(24*time.Hour) {
		t.Fatalf("valid until: %v", evidence.ValidUntil)
	}
}

func TestDecodeProxyCheckHostingSignals(t *testing.T) {
	evidence, err := DecodeProxyCheck(fixture(t, "proxycheck/proxy_hosting.json"), testIP, testNow, time.Hour)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if evidence.IPType != "datacenter" {
		t.Fatalf("ip type: %q", evidence.IPType)
	}
	if evidence.Signals.Proxy == nil || !*evidence.Signals.Proxy {
		t.Fatal("proxy signal missing")
	}
	if evidence.Signals.Compromised == nil || !*evidence.Signals.Compromised {
		t.Fatal("compromised signal missing")
	}
	if evidence.Grade != "severe" {
		t.Fatalf("grade: %q", evidence.Grade)
	}
}

func TestDecodeProxyCheckRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		code string
	}{
		{"contradictory risk", string(fixture(t, "proxycheck/contradictory_risk.json")), CodeResponse},
		{"no detections", `{"status":"ok","8.8.8.8":{"network":{"type":"residential"}}}`, CodeResponse},
		{"other address", `{"status":"ok","9.9.9.9":{"detections":{"risk":1}}}`, CodeResponse},
		{"out of range", `{"status":"ok","8.8.8.8":{"detections":{"risk":101}}}`, CodeResponse},
		{"not json", `nope`, CodeResponse},
		{"limit", string(fixture(t, "proxycheck/rate_limited.json")), CodeLimit},
		{"auth", string(fixture(t, "proxycheck/auth.json")), CodeAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeProxyCheck([]byte(tc.body), testIP, testNow, time.Hour)
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) {
				t.Fatalf("expected provider error, got %v", err)
			}
			if providerErr.Code != tc.code {
				t.Fatalf("expected %s, got %s", tc.code, providerErr.Code)
			}
			if tc.code == CodeAuth && !providerErr.Pause {
				t.Fatal("an auth failure must pause the provider")
			}
		})
	}
}

func TestProxyCheckHTTPStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		code   string
		pause  bool
	}{
		{http.StatusTooManyRequests, CodeLimit, true},
		{http.StatusUnauthorized, CodeAuth, true},
		{http.StatusForbidden, CodeAuth, true},
		{http.StatusInternalServerError, CodeUnavailable, false},
	}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		provider := NewProxyCheckProvider(ProxyCheckOptions{
			Key: "secret-key", BaseURL: server.URL + "/", Client: server.Client(), Now: func() time.Time { return testNow },
		})
		results := provider.Lookup(context.Background(), []netip.Addr{testIP})
		err := expectCode(t, results[testIP], tc.code)
		if err.Message == "" || strings.Contains(err.Message, "secret-key") {
			t.Fatalf("error message leaks or is empty: %q", err.Message)
		}
		if err.Pause != tc.pause {
			t.Fatalf("pause: got %v want %v", err.Pause, tc.pause)
		}
		server.Close()
	}
}

func TestProxyCheckSendsKeyAndDecodes(t *testing.T) {
	var gotPath, gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.URL.Query().Get("key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, "proxycheck/success.json"))
	}))
	defer server.Close()

	provider := NewProxyCheckProvider(ProxyCheckOptions{
		Key: "unit-test-key", BaseURL: server.URL + "/", Client: server.Client(),
		Now: func() time.Time { return testNow },
	})
	results := provider.Lookup(context.Background(), []netip.Addr{testIP})
	evidence := mustEvidence(t, results[testIP])
	if evidence.IP != testIP.String() {
		t.Fatalf("ip: %s", evidence.IP)
	}
	if gotPath != "/8.8.8.8" {
		t.Fatalf("request path: %s", gotPath)
	}
	if gotKey != "unit-test-key" {
		t.Fatalf("key not sent: %q", gotKey)
	}
}

func TestProxyCheckUnsupportedIP(t *testing.T) {
	provider := NewProxyCheckProvider(ProxyCheckOptions{Now: func() time.Time { return testNow }})
	results := provider.Lookup(context.Background(), []netip.Addr{netip.MustParseAddr("10.0.0.1")})
	expectCode(t, results[netip.MustParseAddr("10.0.0.1")], CodeUnsupported)
}

func TestProxyCheckSpecDefaults(t *testing.T) {
	spec := NewProxyCheckProvider(ProxyCheckOptions{}).Spec()
	if spec.DefaultDailyLimit != 80 || spec.DefaultDailyLimitWithKey != 900 {
		t.Fatalf("daily limits: %d/%d", spec.DefaultDailyLimit, spec.DefaultDailyLimitWithKey)
	}
	if spec.MaxDailyLimit != 1_000_000 {
		t.Fatalf("the old 900 cap must be lifted, got %d", spec.MaxDailyLimit)
	}
	if spec.BatchSize != 1 || spec.DefaultQPS != 1 {
		t.Fatalf("batch/qps: %d/%v", spec.BatchSize, spec.DefaultQPS)
	}
}

// --- AbuseIPDB -------------------------------------------------------------

func TestDecodeAbuseIPDB(t *testing.T) {
	evidence, err := DecodeAbuseIPDB(fixture(t, "abuseipdb/success.json"), testIP, testNow, 24*time.Hour)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if evidence.IPType != "datacenter" || evidence.SourceType != "Data Center/Web Hosting/Transit" {
		t.Fatalf("ip type: %q %q", evidence.IPType, evidence.SourceType)
	}
	if evidence.AbuseConfidence == nil || *evidence.AbuseConfidence != 0 {
		t.Fatalf("confidence: %v", evidence.AbuseConfidence)
	}
	if evidence.ReportWindowDays != 30 {
		t.Fatalf("report window: %d", evidence.ReportWindowDays)
	}

	abusive, err := DecodeAbuseIPDB(fixture(t, "abuseipdb/abusive.json"), testIP, testNow, time.Hour)
	if err != nil {
		t.Fatalf("decode abusive: %v", err)
	}
	if abusive.Signals.Tor == nil || !*abusive.Signals.Tor {
		t.Fatal("tor signal missing")
	}
	if abusive.AbuseConfidence == nil || *abusive.AbuseConfidence != 92 {
		t.Fatalf("confidence: %v", abusive.AbuseConfidence)
	}
	if abusive.LastReportedAt == nil {
		t.Fatal("last reported at missing")
	}

	if _, err := DecodeAbuseIPDB(fixture(t, "abuseipdb/mismatch.json"), testIP, testNow, time.Hour); err == nil {
		t.Fatal("an address mismatch must be rejected")
	}
	if _, err := DecodeAbuseIPDB([]byte(`{"data":{"ipAddress":"8.8.8.8","abuseConfidenceScore":101,"totalReports":0,"numDistinctUsers":0}}`), testIP, testNow, time.Hour); err == nil {
		t.Fatal("an out-of-range confidence score must be rejected")
	}
}

func TestAbuseIPDBRequiresKey(t *testing.T) {
	provider := NewAbuseIPDBProvider(AbuseIPDBOptions{Now: func() time.Time { return testNow }})
	results := provider.Lookup(context.Background(), []netip.Addr{testIP})
	err := expectCode(t, results[testIP], CodeAuth)
	if !err.Pause {
		t.Fatal("a missing key must pause the provider")
	}
}

// --- IPQualityScore --------------------------------------------------------

func TestDecodeIPQS(t *testing.T) {
	evidence, err := DecodeIPQS(fixture(t, "ipqs/success.json"), testIP, testNow, 72*time.Hour)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if evidence.FraudScore == nil || *evidence.FraudScore != 5 {
		t.Fatalf("fraud score: %v", evidence.FraudScore)
	}
	if evidence.IPType != "business" || evidence.ASNNumber != 15169 {
		t.Fatalf("type/asn: %q %d", evidence.IPType, evidence.ASNNumber)
	}
	if _, err := DecodeIPQS(fixture(t, "ipqs/quota.json"), testIP, testNow, time.Hour); err == nil {
		t.Fatal("a quota failure must be rejected")
	}
	_, err = DecodeIPQS(fixture(t, "ipqs/quota.json"), testIP, testNow, time.Hour)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Code != CodeLimit {
		t.Fatalf("expected PROVIDER_LIMIT, got %v", err)
	}
	if _, err := DecodeIPQS([]byte(`{"success":true,"ip_address":"9.9.9.9","fraud_score":1}`), testIP, testNow, time.Hour); err == nil {
		t.Fatal("an address mismatch must be rejected")
	}
}

// --- ipapi.is -------------------------------------------------------------

func TestDecodeIPAPIIS(t *testing.T) {
	evidence, err := DecodeIPAPIIS(fixture(t, "ipapi_is/success.json"), testIP, testNow, 72*time.Hour)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if evidence.ASNNumber != 15169 || evidence.RegisteredCC != "US" {
		t.Fatalf("asn/registered: %d %q", evidence.ASNNumber, evidence.RegisteredCC)
	}
	if evidence.Signals.Hosting == nil || !*evidence.Signals.Hosting {
		t.Fatal("hosting signal missing")
	}
	if evidence.IPType != "datacenter" {
		t.Fatalf("ip type: %q", evidence.IPType)
	}
	if _, err := DecodeIPAPIIS(fixture(t, "ipapi_is/mismatch.json"), testIP, testNow, time.Hour); err == nil {
		t.Fatal("an address mismatch must be rejected")
	}
}

// --- IPPure (via-node) -----------------------------------------------------

func TestIPPureViaNode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fixture(t, "ippure/v4_residential.json"))
	}))
	defer server.Close()

	provider := NewIPPureProvider(IPPureOptions{URL: server.URL, Now: func() time.Time { return testNow }})
	result := provider.Lookup(context.Background(), directOutbound{}, testIP)
	evidence := mustEvidence(t, result)
	if evidence.FraudScore == nil || *evidence.FraudScore != 3 {
		t.Fatalf("fraud score: %v", evidence.FraudScore)
	}
	if evidence.IPType != "residential" || evidence.Native == nil || *evidence.Native != true {
		t.Fatalf("residential/native: %q %v", evidence.IPType, evidence.Native)
	}
	if evidence.ASNNumber != 7922 {
		t.Fatalf("asn: %d", evidence.ASNNumber)
	}
	if len(result.Raw) == 0 {
		t.Fatal("the raw response must be stored")
	}
}

func TestIPPureEgressMismatchIsNotEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fixture(t, "ippure/mismatch.json"))
	}))
	defer server.Close()

	provider := NewIPPureProvider(IPPureOptions{URL: server.URL, Now: func() time.Time { return testNow }})
	result := provider.Lookup(context.Background(), directOutbound{}, testIP)
	expectCode(t, result, CodeEgressMismat)
}

func TestDecodeIPPureRules(t *testing.T) {
	evidence, err := DecodeIPPure(fixture(t, "ippure/v4.json"), testIP, testNow, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if evidence.IPType != "non_residential" || evidence.SourceType != "Non-residential" {
		t.Fatalf("ip type: %q %q", evidence.IPType, evidence.SourceType)
	}

	v6, err := DecodeIPPure(fixture(t, "ippure/v6.json"), netip.MustParseAddr("2606:4700:4700::1111"), testNow, time.Hour)
	if err != nil {
		t.Fatalf("decode v6: %v", err)
	}
	if v6.FraudScore != nil {
		t.Fatalf("IPPure does not score IPv6; got %v", v6.FraudScore)
	}
	if v6.Native == nil || *v6.Native != true {
		t.Fatalf("native: %v", v6.Native)
	}

	if _, err := DecodeIPPure(fixture(t, "ippure/out_of_range.json"), testIP, testNow, time.Hour); err == nil {
		t.Fatal("an out-of-range fraud score must be rejected")
	}
	if _, err := DecodeIPPure([]byte(`{"ip":"8.8.8.8"}`), testIP, testNow, time.Hour); err == nil {
		t.Fatal("an empty payload must be rejected")
	}
}

// TestIPPureSpecScopesLimitsPerNode pins the via-node limit split: the daily
// budget is counted per node (the vendor's anonymous quota belongs to the
// node's address) and the spec-level QPS is only the provider-wide valve that
// keeps the whole inventory from reaching the vendor at once. The previous
// "one query per minute" was a global gate that made a 287-node inventory take
// hours to walk even though every node had its own untouched quota.
func TestIPPureSpecScopesLimitsPerNode(t *testing.T) {
	spec := NewIPPureProvider(IPPureOptions{}).Spec()
	if spec.Kind != KindViaNode {
		t.Fatalf("kind: %v", spec.Kind)
	}
	if spec.DefaultDailyLimit != 500 {
		t.Fatalf("daily limit: %d", spec.DefaultDailyLimit)
	}
	if spec.DefaultQPS <= 0 || spec.DefaultQPS > 5 {
		t.Fatalf("provider-wide QPS valve out of range: %v", spec.DefaultQPS)
	}
	if spec.DefaultTTL != 7*24*time.Hour {
		t.Fatalf("ttl: %v", spec.DefaultTTL)
	}
}

// --- ip-api.com (via-node) -------------------------------------------------

func TestIPAPIViaNode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "fields=") {
			t.Errorf("missing fields selection: %s", r.URL.RawQuery)
		}
		_, _ = w.Write(fixture(t, "ip_api/success.json"))
	}))
	defer server.Close()

	provider := NewIPAPIProvider(IPAPIOptions{URL: server.URL, Now: func() time.Time { return testNow }})
	result := provider.Lookup(context.Background(), directOutbound{}, testIP)
	evidence := mustEvidence(t, result)
	if evidence.ASNNumber != 15169 || evidence.ASN != "AS15169 Google LLC" {
		t.Fatalf("asn: %d %q", evidence.ASNNumber, evidence.ASN)
	}
	if evidence.IPType != "datacenter" {
		t.Fatalf("ip type: %q", evidence.IPType)
	}
	if evidence.Signals.Hosting == nil || !*evidence.Signals.Hosting {
		t.Fatal("hosting signal missing")
	}
	if evidence.City != "Mountain View" || evidence.Region != "California" {
		t.Fatalf("location: %q %q", evidence.City, evidence.Region)
	}
}

func TestIPAPIRejectsMismatchAndLimits(t *testing.T) {
	evidence, err := DecodeIPAPI(fixture(t, "ip_api/mobile.json"), testIP, testNow, time.Hour)
	if err != nil {
		t.Fatalf("decode mobile: %v", err)
	}
	if evidence.IPType != "mobile" || evidence.IsMobile == nil || !*evidence.IsMobile {
		t.Fatalf("mobile: %q %v", evidence.IPType, evidence.IsMobile)
	}
	if evidence.Signals.Proxy == nil || !*evidence.Signals.Proxy {
		t.Fatal("proxy signal missing")
	}

	if _, err := DecodeIPAPI(fixture(t, "ip_api/mismatch.json"), testIP, testNow, time.Hour); err == nil {
		t.Fatal("an address mismatch must be rejected")
	}
	_, err = DecodeIPAPI(fixture(t, "ip_api/limit.json"), testIP, testNow, time.Hour)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Code != CodeLimit {
		t.Fatalf("expected PROVIDER_LIMIT, got %v", err)
	}
}

func TestViaNodeRequiresOutbound(t *testing.T) {
	provider := NewIPAPIProvider(IPAPIOptions{URL: "http://127.0.0.1:1/", Now: func() time.Time { return testNow }})
	result := provider.Lookup(context.Background(), nil, testIP)
	expectCode(t, result, CodeUnavailable)
}

// --- proxycheck.io (via-node) ---------------------------------------------

func TestProxyCheckViaNode(t *testing.T) {
	var gotPath, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write(fixture(t, "proxycheck/success.json"))
	}))
	defer server.Close()

	provider := NewProxyCheckViaNodeProvider(ProxyCheckViaNodeOptions{
		URL: server.URL, Now: func() time.Time { return testNow },
	})
	result := provider.Lookup(context.Background(), directOutbound{}, testIP)
	evidence := mustEvidence(t, result)

	// The anonymous API rejects a request without an address ("No valid IP
	// Addresses supplied."), so the node's own address must be in the path: that
	// is what makes the vendor attribute the query to the node's quota.
	if !strings.Contains(gotPath, testIP.String()) {
		t.Fatalf("the node's own address must be the query target, path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "ver=") {
		t.Fatalf("the pinned API version is missing: %q", gotQuery)
	}
	if strings.Contains(gotQuery, "key=") {
		t.Fatal("a key must never be sent through a node's outbound")
	}
	if got := evidence.Provider; got != "proxycheck_node" {
		t.Fatalf("provider: %q", got)
	}
	if got := evidence.Profile; got != ProxyCheckNodeProfile {
		t.Fatalf("profile: %q", got)
	}
}

func TestProxyCheckViaNodeNeedsAnAddress(t *testing.T) {
	provider := NewProxyCheckViaNodeProvider(ProxyCheckViaNodeOptions{
		URL: "https://example.invalid", Now: func() time.Time { return testNow },
	})
	result := provider.Lookup(context.Background(), directOutbound{}, netip.Addr{})
	if !result.Failed() {
		t.Fatal("a via-node lookup without the node's address must fail instead of querying another owner's quota")
	}
	if result.Err.Code != CodeRequest {
		t.Fatalf("code: %q", result.Err.Code)
	}
}

func TestProxyCheckNodeSpecSpendsQuotaPerNode(t *testing.T) {
	spec := NewProxyCheckViaNodeProvider(ProxyCheckViaNodeOptions{}).Spec()
	if spec.Kind != KindViaNode {
		t.Fatalf("kind: %v", spec.Kind)
	}
	if spec.RequiresKey {
		t.Fatal("the via-node variant must never need a credential")
	}
	if !spec.DefaultEnabled {
		t.Fatal("the via-node variant is the default proxycheck source")
	}
	if spec.DefaultDailyLimit != 100 {
		t.Fatalf("daily limit: %d", spec.DefaultDailyLimit)
	}
	if spec.DefaultQPS <= 0 || spec.DefaultQPS > 5 {
		t.Fatalf("provider-wide QPS valve out of range: %v", spec.DefaultQPS)
	}
}
