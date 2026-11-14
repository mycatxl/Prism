package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"prism/internal/config"
)

type paymentCheckTransport func(*http.Request) (*http.Response, error)

func (f paymentCheckTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func publicPaymentLookup(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
}

func TestPaymentURLChecksClassifyReachabilityWithoutSendingCredentials(t *testing.T) {
	cfg := config.NewDefaultRuntimeConfig()
	cfg.LDCNotifyURL = "https://merchant.example/api/v1/public/ldc/notify"
	cfg.LDCReturnURL = "https://merchant.example/ui/me/deposit"
	status, calls := http.StatusOK, 0
	checker := &PaymentURLChecker{LookupIP: publicPaymentLookup, Client: &http.Client{Transport: paymentCheckTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodHead || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.URL.RawQuery != "" {
			t.Error("URL check must be a credential-free HEAD request")
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"http://127.0.0.1/private"}}, Body: io.NopCloser(strings.NewReader("private provider response"))}, nil
	})}}
	cp := &ControlPlaneService{PaymentURLChecker: checker}
	for _, tc := range []struct {
		status int
		code   string
	}{{200, "reachable"}, {404, "not_found"}, {403, "access_denied"}, {302, "redirect"}, {503, "http_error"}, {405, "method_or_signature"}} {
		status = tc.status
		checks := cp.CheckPaymentURLs(context.Background(), []byte(`{"ldc_notify_url":"changed"}`), cfg)
		if len(checks) != 1 || checks[0].Code != tc.code || checks[0].HTTPStatus != status {
			t.Fatalf("unexpected URL check result: %+v", checks)
		}
	}
	if calls != 6 {
		t.Fatal("checker followed a redirect")
	}
	if checks := cp.CheckPaymentURLs(context.Background(), []byte(`{"ldc_rate":2}`), cfg); len(checks) != 0 {
		t.Fatal("unrelated saves performed callback checks")
	}
	cfg.LDCNotifyURL = "https://merchant.example/wrong-page"
	status = http.StatusOK
	if checks := cp.CheckPaymentURLs(context.Background(), []byte(`{"ldc_notify_url":"changed"}`), cfg); checks[0].Code != "unexpected_path" {
		t.Fatal("a reachable page was mistaken for the callback route")
	}
}

func TestPaymentURLChecksBlockPrivateResolutionAndWarnOnMissingDerivedURLs(t *testing.T) {
	called := false
	checker := &PaymentURLChecker{LookupIP: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	}, Client: &http.Client{Transport: paymentCheckTransport(func(*http.Request) (*http.Response, error) { called = true; return nil, errors.New("must not connect") })}}
	cp := &ControlPlaneService{PaymentURLChecker: checker}
	cfg := config.NewDefaultRuntimeConfig()
	cfg.LDCNotifyURL = "https://merchant.example/api/v1/public/ldc/notify"
	checks := cp.CheckPaymentURLs(context.Background(), []byte(`{"ldc_enabled":true}`), cfg)
	// Enabling checks both URLs, including a return URL derived from the checkout origin.
	cfg.LDCEnabled = true
	checks = cp.CheckPaymentURLs(context.Background(), []byte(`{"ldc_enabled":true}`), cfg)
	if called || len(checks) != 2 || checks[0].Code != "non_public" || checks[1].Code != "derived" {
		t.Fatalf("unexpected private/derived checks: %+v", checks)
	}
}

func TestPaymentURLChecksRespectDeadlineAndKeepRawErrorsPrivate(t *testing.T) {
	checker := &PaymentURLChecker{LookupIP: publicPaymentLookup, Client: &http.Client{Transport: paymentCheckTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, errors.New("private diagnostic with payment secret")
	})}}
	cp := &ControlPlaneService{PaymentURLChecker: checker}
	cfg := config.NewDefaultRuntimeConfig()
	cfg.LDCNotifyURL = "https://merchant.example/api/v1/public/ldc/notify"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	checks := cp.CheckPaymentURLs(ctx, []byte(`{"ldc_notify_url":"changed"}`), cfg)
	if time.Since(started) > time.Second || len(checks) != 1 || checks[0].Code != "connection_failed" {
		t.Fatalf("deadline was not respected: %+v", checks)
	}
}
