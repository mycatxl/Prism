package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
	"time"

	"prism/internal/proxy"
)

// This uses actual sockets and two HTTP forwarding hops. The alternate
// loopback source is the customer; only the reverse proxies' exact peer is
// trusted. It is an offline Tunnel-shaped fixture, not live CF acceptance.
func TestClientIPThroughTunnelShapedReverseProxyAndDirectSpoofing(t *testing.T) {
	trusted := []string{"127.0.0.1/32"}
	proxy.SetTrustedProxies(trusted)
	t.Cleanup(func() { proxy.SetTrustedProxies(nil) })
	limiter := NewAuthFailureLimiter(10, time.Minute, time.Minute, trusted)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"proxy_ip": proxy.ProxyClientIP(r), "management_ip": limiter.ClientIP(r), "host": r.Host})
	}))
	defer backend.Close()
	bridge := func(destination string) *httptest.Server {
		u, err := url.Parse(destination)
		if err != nil {
			t.Fatal(err)
		}
		handler := httputil.NewSingleHostReverseProxy(u)
		handler.Transport = &http.Transport{Proxy: nil}
		return httptest.NewServer(handler)
	}
	tunnel := bridge(backend.URL)
	defer tunnel.Close()
	edge := bridge(tunnel.URL)
	defer edge.Close()
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.2")}, Timeout: time.Second}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	for _, destination := range []string{edge.URL, tunnel.URL, backend.URL} {
		for _, forged := range []string{"203.0.113.250", "203.0.113.250, 10.0.0.1", "garbage, 203.0.113.250"} {
			req, _ := http.NewRequest(http.MethodGet, destination, nil)
			req.Host = "panel.prism.test"
			req.Header.Set("X-Forwarded-For", forged)
			req.Header.Set("CF-Connecting-IP", "203.0.113.251")
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]string
			err = json.NewDecoder(response.Body).Decode(&result)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if result["proxy_ip"] != "127.0.0.2" || result["management_ip"] != "127.0.0.2" || result["host"] != "panel.prism.test" {
				t.Fatalf("customer attribution was replaced by a forwarded header: %+v", result)
			}
		}
	}
}
