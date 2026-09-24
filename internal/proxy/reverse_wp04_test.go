package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"prism/internal/node"
	"prism/internal/outbound"
	"prism/internal/platform"
	"prism/internal/routing"
	"prism/internal/subscription"
	"prism/internal/testutil"
	"prism/internal/topology"
)

// reverseE2EEnv is a fully in-process node pool with one routable stub node.
type reverseE2EEnv struct {
	pool   *topology.GlobalNodePool
	router *routing.Router
}

func newReverseE2EEnv(t *testing.T) *reverseE2EEnv {
	t.Helper()

	subMgr := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})

	plat := platform.NewPlatform("plat-id", "plat", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	plat.ReverseProxyMissAction = "TREAT_AS_EMPTY"
	pool.RegisterPlatform(plat)

	sub := subscription.NewSubscription("sub-1", "sub-1", "https://example.com", true, false)
	subMgr.Register(sub)

	raw := json.RawMessage(`{"type":"stub","server":"127.0.0.1","server_port":1}`)
	hash := node.HashFromRawOptions(raw)
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"tag"}})
	pool.AddNodeFromSub(hash, raw, sub.ID)

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("node not found in pool")
	}

	obMgr := outbound.NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	obMgr.EnsureNodeOutbound(hash)
	if !entry.HasOutbound() {
		t.Fatal("outbound should be initialized")
	}

	entry.SetEgressIP(netip.MustParseAddr("203.0.113.10"))
	if entry.LatencyTable == nil {
		t.Fatal("latency table should be initialized")
	}
	entry.LatencyTable.Update("example.com", 20*time.Millisecond, 10*time.Minute)
	pool.RecordResult(hash, true)

	pool.NotifyNodeDirty(hash)
	if !plat.View().Contains(hash) {
		t.Fatal("node should be in platform routable view")
	}

	router := routing.NewRouter(routing.RouterConfig{
		Pool:        pool,
		Authorities: func() []string { return []string{"example.com"} },
		P2CWindow:   func() time.Duration { return 10 * time.Minute },
	})

	return &reverseE2EEnv{pool: pool, router: router}
}

// TestReverseProxy_DirectDenyPrivate covers PRISM_DIRECT_DENY_PRIVATE: the switch
// only applies to the local direct (bypass) path.
func TestReverseProxy_DirectDenyPrivate(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("direct"))
	}))
	defer upstream.Close()

	host := strings.TrimPrefix(upstream.URL, "http://")
	path := fmt.Sprintf("/tok/plat:acct/http/%s/api/direct", host)

	for _, tc := range []struct {
		name      string
		deny      bool
		wantCode  int
		wantBody  string
		wantError string
	}{
		{name: "enabled blocks loopback", deny: true, wantCode: http.StatusForbidden, wantError: "DIRECT_TARGET_DENIED"},
		{name: "disabled allows loopback", deny: false, wantCode: http.StatusOK, wantBody: "direct"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rp := NewReverseProxy(ReverseProxyConfig{
				ProxyToken:        "tok",
				Events:            NoOpEventEmitter{},
				ProxyBypassRules:  []string{"127.*"},
				DirectDenyPrivate: tc.deny,
			})

			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			rp.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("status: got %d, want %d (body=%q)", w.Code, tc.wantCode, w.Body.String())
			}
			if got := w.Header().Get("X-Prism-Error"); got != tc.wantError {
				t.Fatalf("X-Prism-Error: got %q, want %q", got, tc.wantError)
			}
			if tc.wantBody != "" && w.Body.String() != tc.wantBody {
				t.Fatalf("body: got %q, want %q", w.Body.String(), tc.wantBody)
			}
		})
	}
}

// TestReverseProxy_BypassCreatesNoLease verifies the restored upstream behaviour:
// a bypassed request is dialled directly and records no routed node.
func TestReverseProxy_BypassCreatesNoLease(t *testing.T) {
	env := newReverseE2EEnv(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	host := strings.TrimPrefix(upstream.URL, "http://")
	path := fmt.Sprintf("/tok/plat:acct/http/%s/api/direct", host)

	rp := NewReverseProxy(ReverseProxyConfig{
		ProxyToken:       "tok",
		Router:           env.router,
		Pool:             env.pool,
		PlatformLookup:   env.pool,
		Events:           NoOpEventEmitter{},
		ProxyBypassRules: []string{"127.*"},
	})

	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want %d (body=%q)", w.Code, http.StatusNoContent, w.Body.String())
	}
	leaseCount := 0
	env.router.RangeLeases("plat-id", func(_ string, _ routing.Lease) bool {
		leaseCount++
		return true
	})
	if leaseCount != 0 {
		t.Fatalf("bypass request created %d leases, want 0", leaseCount)
	}
}

// TestReverseProxy_DirectDenyPrivateScope pins the scope rule: the switch only
// applies to the local direct (bypass) path, never to node-routed requests.
func TestReverseProxy_DirectDenyPrivateScope(t *testing.T) {
	env := newReverseE2EEnv(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("node-routed"))
	}))
	defer upstream.Close()

	host := strings.TrimPrefix(upstream.URL, "http://")
	path := fmt.Sprintf("/tok/plat:acct/http/%s/api/nodes", host)

	rp := NewReverseProxy(ReverseProxyConfig{
		ProxyToken:        "tok",
		Router:            env.router,
		Pool:              env.pool,
		PlatformLookup:    env.pool,
		Events:            NoOpEventEmitter{},
		DirectDenyPrivate: true,
	})

	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("node-routed request: got %d, want 200 (body=%q)", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "node-routed" {
		t.Fatalf("body: got %q, want %q", got, "node-routed")
	}
}
