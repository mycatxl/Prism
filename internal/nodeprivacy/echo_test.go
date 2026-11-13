package nodeprivacy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestTLSEchoReportsActualPeerAndIdentityHeaders(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(EchoHandler))
	defer server.Close()
	marker, _ := NewMarker()
	req, _ := http.NewRequest("GET", server.URL+"/check", nil)
	req.Header.Set(MarkerHeader, marker)
	req.Header.Set("Cookie", "secret-customer-canary")
	req.Header.Set("X-Forwarded-For", "192.0.2.123")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "secret-customer-canary") {
		t.Fatal("echo reflected customer data")
	}
	var out EchoResponse
	if json.Unmarshal(body, &out) != nil || out.ObservedAddress != "127.0.0.1" || out.Headers["X-Forwarded-For"][0] != "192.0.2.123" {
		t.Fatalf("bad echo: %s", body)
	}
	leak, err := EvaluateEcho(body, marker, netip.MustParseAddr("127.0.0.1"))
	if err != nil || !leak {
		t.Fatal(leak, err)
	}
}
func TestEchoDecisionUnknownIsNeverClean(t *testing.T) {
	marker, _ := NewMarker()
	exit := netip.MustParseAddr("203.0.113.7")
	base := func() EchoResponse {
		e := EchoResponse{ObservedAddress: exit.String(), Marker: marker, MarkerSurvived: true, Headers: map[string][]string{}}
		for _, h := range IdentityHeaders {
			e.Headers[h] = nil
		}
		return e
	}
	for _, tc := range []struct {
		name          string
		change        func(*EchoResponse)
		leak, unknown bool
	}{
		{"clean", func(*EchoResponse) {}, false, false},
		{"wrong_peer", func(e *EchoResponse) { e.ObservedAddress = "192.0.2.123" }, true, false},
		{"forwarded", func(e *EchoResponse) { e.Headers["Forwarded"] = []string{"for=192.0.2.123"} }, true, false},
		{"via", func(e *EchoResponse) { e.Headers["Via"] = []string{"1.1 revealing-origin"} }, true, false},
		{"marker_missing", func(e *EchoResponse) { e.MarkerSurvived = false }, false, true},
		{"header_missing", func(e *EchoResponse) { delete(e.Headers, "Client-IP") }, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := base()
			tc.change(&e)
			b, _ := json.Marshal(e)
			leak, err := EvaluateEcho(b, marker, exit)
			if leak != tc.leak || (err != nil) != tc.unknown {
				t.Fatal(leak, err)
			}
		})
	}
	if ValidateEchoURL("http://example.test/check") == nil {
		t.Fatal("allowed cleartext echo")
	}
}
