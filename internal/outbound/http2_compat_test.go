package outbound

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/transport/v2rayhttp"
	"golang.org/x/net/http2"
)

func TestHTTP2TransportResetRetainsWorkingConnections(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Error("request did not use HTTP/2")
		}
		calls.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	// The classic x/net/http2 transport is the type sing-box's v2rayhttp close
	// path asserts on; x/net deprecates the whole type in favour of
	// http.Transport, which is exactly the implementation this test keeps out of
	// that path (docs/UPSTREAM_BASELINE.md). Close through the v2rayhttp helper
	// so the reset path itself is what runs here.
	clientTLS := server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport := &http2.Transport{TLSClientConfig: clientTLS} //nolint:staticcheck // see above
	defer v2rayhttp.CloseIdleConnections(transport)
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	request := func() {
		t.Helper()
		response, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != "ok" {
			t.Fatalf("unexpected response: %v", err)
		}
	}
	request()
	reset := make(chan struct{})
	go func() { v2rayhttp.ResetTransport(transport); close(reset) }()
	select {
	case <-reset:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP/2 reset blocked while closing pooled connections")
	}
	request()
	if calls.Load() != 2 {
		t.Fatalf("expected requests before and after reset, got %d", calls.Load())
	}
}
