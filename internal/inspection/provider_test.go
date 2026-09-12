package inspection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProxyCheckV3NetworkTypesAndSchemaVariants(t *testing.T) {
	for _, kind := range []string{"Business", "Wireless"} {
		for _, location := range []string{`"risk":4,"detections":{"proxy":false}`, `"detections":{"risk":4,"proxy":false}`} {
			body := fmt.Sprintf(`{"status":"ok","8.8.8.8":{"network":{"type":%q,"provider":"Network ISP"},%s,"operator":{"name":"Observed operator","services":["residential_proxies"]},"attack_history":{"vulnerability_probing":7}}}`, kind, location)
			e, err := decodeProxyCheck([]byte(body), netip.MustParseAddr("8.8.8.8"), time.Now(), time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if e.IPType != strings.ToLower(kind) || e.RiskScore == nil || *e.RiskScore != 4 ||
				e.NetworkProvider != "Network ISP" || e.Operator != "Observed operator" || e.AttackHistory["vulnerability_probing"] != 7 {
				t.Fatalf("v3 classification or evidence lost: %+v", e)
			}
		}
	}
	_, err := decodeProxyCheck([]byte(`{"status":"ok","8.8.8.8":{"risk":0,"detections":{"risk":90}}}`), netip.MustParseAddr("8.8.8.8"), time.Now(), time.Hour)
	if err == nil {
		t.Fatal("contradictory risk fields accepted")
	}
}

func TestProxyCheckRejectsMissingOrMismatchedEvidence(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"status":"ok","8.8.8.8":{"detections":{}}}`,
		`{"status":"ok","8.8.8.8":{"detections":{"confidence":100}}}`,
		`{"status":"ok","8.8.8.8":{"detections":{"risk":null,"proxy":null,"hosting":null}}}`,
		`{"status":"ok","1.1.1.1":{"detections":{"risk":0}}}`,
		`{"status":"ok","8.8.8.8":{"detections":{"risk":-1}}}`,
		`{"status":"ok","8.8.8.8":{"detections":{"risk":101}}}`,
		`{"status":"ok","8.8.8.8":{"detections":{"risk":0,"confidence":101}}}`,
	} {
		evidence, err := decodeProxyCheck([]byte(body), netip.MustParseAddr("8.8.8.8"), time.Now(), time.Hour)
		var failure *ProviderError
		if evidence != nil || !errors.As(err, &failure) || failure.Code != "PROVIDER_RESPONSE" {
			t.Fatalf("incomplete response accepted: %s, evidence=%+v err=%v", body, evidence, err)
		}
	}
}

func TestProxyCheckPreservesSourceMeaningAndUnknownFields(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	body := `{"status":"ok","::ffff:8.8.8.8":{"network":{"type":"Hosting","asn":"AS15169","organisation":"Test network"},"detections":{"risk":33,"proxy":false,"hosting":true,"confidence":100},"key":"must-not-be-stored"}}`
	e, err := decodeProxyCheck([]byte(body), netip.MustParseAddr("8.8.8.8"), now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if e.IP != "8.8.8.8" || e.IPType != "datacenter" || e.RiskScore == nil || *e.RiskScore != 33 || e.Grade != "moderate" {
		t.Fatalf("wrong provider interpretation: %+v", e)
	}
	if e.Signals.Proxy == nil || *e.Signals.Proxy || e.Signals.VPN != nil {
		t.Fatal("missing field and observed false were conflated")
	}
	if e.ObservedAt != now || !e.ValidUntil.Equal(now.Add(time.Hour)) {
		t.Fatal("invalid evidence lifetime")
	}
	encoded, _ := json.Marshal(e)
	if strings.Contains(string(encoded), "must-not-be-stored") {
		t.Fatal("unapproved provider fields persisted")
	}
	e, err = decodeProxyCheck([]byte(`{"status":"ok","8.8.8.8":{"detections":{"hosting":true}}}`), netip.MustParseAddr("8.8.8.8"), now, time.Hour)
	if err != nil || e.RiskScore != nil || e.Grade != "unknown" {
		t.Fatal("classification-only evidence invented a score")
	}
	e, err = decodeProxyCheck([]byte(`{"status":"ok","8.8.8.8":{"network":{"type":"Residential"},"detections":{"hosting":true,"risk":0}}}`), netip.MustParseAddr("8.8.8.8"), now, time.Hour)
	if err != nil || e.IPType != "conflicting" || e.Grade != "unknown" {
		t.Fatal("contradictory network evidence was marked qualified")
	}
}

func TestProxyCheckHTTPBoundariesAndCredentialRedaction(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		retry  string
		code   string
	}{
		{"quota", 429, "", "1800", "PROVIDER_LIMIT"},
		{"auth", 401, "test-provider-key", "", "PROVIDER_AUTH"},
		{"oversized", 200, strings.Repeat("x", 70*1024), "", "PROVIDER_RESPONSE"},
		{"api-auth", 200, `{"status":"denied","message":"invalid key test-provider-key"}`, "", "PROVIDER_AUTH"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("key") != "test-provider-key" {
					t.Error("provider credential was not supplied")
				}
				if test.retry != "" {
					w.Header().Set("Retry-After", test.retry)
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			p := NewProxyCheckProvider("test-provider-key", time.Second, time.Hour)
			p.baseURL = server.URL + "/"
			defer p.Close()
			_, err := p.Lookup(context.Background(), netip.MustParseAddr("8.8.8.8"))
			var failure *ProviderError
			if !errors.As(err, &failure) || failure.Code != test.code {
				t.Fatalf("wrong failure classification: %v", err)
			}
			if strings.Contains(err.Error(), "test-provider-key") {
				t.Fatal("provider key leaked in error")
			}
			if test.retry != "" && failure.RetryAfter != 30*time.Minute {
				t.Fatalf("Retry-After ignored: %v", failure.RetryAfter)
			}
		})
	}
	var redirects atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirects.Add(1) }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer server.Close()
	p := NewProxyCheckProvider("test-provider-key", time.Second, time.Hour)
	p.baseURL = server.URL + "/"
	defer p.Close()
	if _, err := p.Lookup(context.Background(), netip.MustParseAddr("8.8.8.8")); err == nil {
		t.Fatal("redirect was accepted")
	}
	if redirects.Load() != 0 {
		t.Fatal("provider redirect followed")
	}
}

func TestAbuseIPDBUsesHeaderKeyAndKeepsReportScoreIndependent(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Key") != "test-abuse-key" || r.URL.Query().Get("maxAgeInDays") != "30" || r.URL.Query().Get("key") != "" {
			t.Error("invalid credentials or query window")
		}
		_, _ = io.WriteString(w, `{"data":{"ipAddress":"8.8.8.8","isPublic":true,"usageType":"Data Center/Web Hosting/Transit","isp":"Test network","abuseConfidenceScore":0,"totalReports":0,"numDistinctUsers":0,"lastReportedAt":null}}`)
	}))
	defer server.Close()
	p := NewAbuseIPDBProvider("test-abuse-key", time.Second, time.Hour)
	p.baseURL = server.URL
	defer p.Close()
	e, err := p.Lookup(context.Background(), netip.MustParseAddr("8.8.8.8"))
	if err != nil {
		t.Fatal(err)
	}
	if e.RiskScore != nil || e.Grade != "unknown" || e.AbuseConfidence == nil || *e.AbuseConfidence != 0 || e.TotalReports == nil || *e.TotalReports != 0 {
		t.Fatalf("abuse confidence misrepresented as purity: %+v", e)
	}
	p.apiKey = ""
	if _, err := p.Lookup(context.Background(), netip.MustParseAddr("8.8.8.8")); err == nil {
		t.Fatal("missing API key accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("unconfigured provider made an external request")
	}
	if _, err := decodeAbuseIPDB([]byte(`{"data":{"ipAddress":"1.1.1.1","abuseConfidenceScore":0,"totalReports":0,"numDistinctUsers":0}}`), netip.MustParseAddr("8.8.8.8"), time.Now(), time.Hour); err == nil {
		t.Fatal("mismatched abuse evidence accepted")
	}
}
