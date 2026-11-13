package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"prism/internal/node"
	"prism/internal/nodeprivacy"
	"testing"
)

func TestPrivacyBlocksDirectBypassOnAllInbounds(t *testing.T) {
	restricted := platformRestrictedKey()
	t.Helper()
	env := newProxyE2EEnv(t)
	policy, _ := node.NewPrivacyPolicy(nodeprivacy.Config{ProbeStatus: "ready"})
	env.pool.SetPrivacyPolicy(policy)

	t.Run("forward HTTP", func(t *testing.T) {
		fp := NewForwardProxy(ForwardProxyConfig{
			AccessKeys:       keyVerifierStub{credential: testCredential, state: restricted},
			Router:           env.router,
			Events:           NoOpEventEmitter{},
			ProxyBypassRules: []string{"bypass.test"},
		})
		req := httptest.NewRequest(http.MethodGet, "http://bypass.test/resource", nil)
		req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
		rec := httptest.NewRecorder()
		fp.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("X-Prism-Error") != ErrNoAvailableNodes.PrismError {
			t.Fatalf("status/header = %d/%q, want 503/%s", rec.Code, rec.Header().Get("X-Prism-Error"), ErrNoAvailableNodes.PrismError)
		}
	})

	t.Run("reverse HTTP", func(t *testing.T) {
		rp := NewReverseProxy(ReverseProxyConfig{
			AccessKeys:       keyVerifierStub{credential: testCredential, state: restricted},
			Router:           env.router,
			Events:           NoOpEventEmitter{},
			ProxyBypassRules: []string{"bypass.test"},
		})
		req := httptest.NewRequest(http.MethodGet, "/"+testCredential+"/plat:acct/https/bypass.test/resource", nil)
		rec := httptest.NewRecorder()
		rp.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("X-Prism-Error") != ErrNoAvailableNodes.PrismError {
			t.Fatalf("status/header = %d/%q, want 503/%s", rec.Code, rec.Header().Get("X-Prism-Error"), ErrNoAvailableNodes.PrismError)
		}
	})

	t.Run("CONNECT and SOCKS shared path", func(t *testing.T) {
		result := prepareConnectTunnel(
			context.Background(),
			tunnelDeps{router: env.router, bypass: NewTargetBypassMatcher([]string{"bypass.test"})},
			"plat",
			"acct",
			"bypass.test:443",
			restricted,
		)
		if result.proxyErr != ErrNoAvailableNodes || result.session != nil {
			t.Fatalf("prepare result = %+v, want ErrNoAvailableNodes without a session", result)
		}
	})
}
