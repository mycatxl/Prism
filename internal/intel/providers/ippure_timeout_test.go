package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIPPureTimeoutHasAtomicCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	provider := NewIPPureProvider(IPPureOptions{URL: server.URL, Timeout: 20 * time.Millisecond})
	result := provider.Lookup(context.Background(), directOutbound{}, testIP)
	if result.Err == nil || result.Err.Code != CodeTimeout || result.Evidence != nil {
		t.Fatal("timeout did not fail closed with its own code")
	}
}

func TestNetworkProviderFieldsDoNotInventResidentialOrHideConflict(t *testing.T) {
	isp, err := DecodeIPAPIIS([]byte(`{"ip":"8.8.8.8","company":{"name":"ISP","type":"isp"}}`), testIP, testNow, time.Hour)
	if err != nil || isp.IPType != "unknown" || isp.IsResidential != nil {
		t.Fatal("an ISP company label is not residential access evidence")
	}
	both, err := DecodeIPAPI([]byte(`{"status":"success","query":"8.8.8.8","countryCode":"US","hosting":true,"mobile":true}`), testIP, testNow, time.Hour)
	if err != nil || both.IPType != "conflicting" {
		t.Fatal("hosting and mobile conflict was hidden")
	}
}
