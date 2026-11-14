package api

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"prism/internal/service"
)

type paymentURLTransport func(*http.Request) (*http.Response, error)

func (f paymentURLTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPaymentURLsRejectMalformedHostsAndPortsBeforePersistence(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	before := env.cp.RuntimeCfg.Load()
	for _, value := range []string{"https://example.com:0/callback", "https://example.com:65536/callback", "https://example.com:/callback", "https://-invalid.example/callback", "https://example..com/callback", "https://example.com/callback?", "https://example.com/callback%26extra", "https://example.com/callback#fragment", "https://example.com/callback#", "https://user:secret@example.com/callback", "http://example.com/callback"} {
		for _, field := range []string{"ldc_notify_url", "ldc_return_url"} {
			rec := admin.do(http.MethodPatch, "/api/v1/system/config", map[string]any{field: value})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("malformed URL accepted: field=%s status=%d", field, rec.Code)
			}
		}
	}
	if !reflect.DeepEqual(before, env.cp.RuntimeCfg.Load()) {
		t.Fatal("invalid callback save changed runtime state")
	}
}

func TestPaymentURLSaveSurfacesReachabilityWarningsWithoutRollback(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	env.cp.PaymentURLChecker = &service.PaymentURLChecker{LookupIP: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}, Client: &http.Client{Transport: paymentURLTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("private diagnostic")), Header: http.Header{}}, nil
	})}}
	rec := admin.do(http.MethodPatch, "/api/v1/system/config", map[string]any{"ldc_notify_url": "https://merchant.example/api/v1/public/ldc/notify"})
	if rec.Code != http.StatusOK {
		t.Fatalf("save status=%d", rec.Code)
	}
	checks, ok := decodeJSONMap(t, rec)["payment_url_checks"].([]any)
	if !ok || len(checks) != 1 || checks[0].(map[string]any)["code"] != "not_found" {
		t.Fatal("save omitted callback warning")
	}
	if strings.Contains(rec.Body.String(), "private diagnostic") {
		t.Fatal("raw network diagnostics reached the browser")
	}
	if env.cp.RuntimeCfg.Load().LDCNotifyURL == "" {
		t.Fatal("advisory network failure rolled back a valid operator setting")
	}
}
