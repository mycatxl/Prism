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
	transport := &http2.Transport{TLSClientConfig: server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
	defer transport.CloseIdleConnections()
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
