package proxy

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"

	"prism/internal/node"
	"prism/internal/platform"
	"prism/internal/routing"
	"prism/internal/subscription"
	"prism/internal/topology"
)

// Throughput baselines for the data plane.
//
// These exist because the multi-tenant work adds work to the request path -
// credential verification, quota accounting, connection registration - and a
// claim like "throughput drops by less than 5%" is unmeasurable without a
// baseline to compare against. Run them before and after such a change:
//
//	go test ./internal/proxy -run '^$' -bench 'BenchmarkProxy' -benchmem
//
// No threshold is asserted here on purpose: a number baked into a benchmark is
// machine-dependent and fails on a loaded box for reasons unrelated to the
// change under test. The comparison is the deliverable, so the benchmark has to
// stay runnable and stable rather than judge itself.

// benchEnv is the e2e fixture, built without a *testing.T so a benchmark can
// drive it. The construction mirrors newProxyE2EEnv; that helper asserts
// through *testing.T, which a benchmark cannot pass.
type benchEnv struct {
	pool   *topology.GlobalNodePool
	router *routing.Router
}

func newBenchEnv(b *testing.B, upstreamAddr string) (*benchEnv, *ForwardProxy) {
	b.Helper()

	subMgr := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(_ netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})

	// The platform name is "plat" because that is what the request credential
	// below names; a mismatch would make every benchmark measure a 404.
	plat := platform.NewPlatform("plat-id", "plat", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	plat.ReverseProxyMissAction = "TREAT_AS_EMPTY"
	pool.RegisterPlatform(plat)

	sub := subscription.NewSubscription("bench-sub", "bench-sub", "https://example.com", true, false)
	subMgr.Register(sub)

	raw := json.RawMessage(`{"type":"stub","server":"127.0.0.1","server_port":1}`)
	hash := node.HashFromRawOptions(raw)
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"bench"}})
	pool.AddNodeFromSub(hash, raw, sub.ID)

	entry, ok := pool.GetEntry(hash)
	if !ok {
		b.Fatal("node not found in pool")
	}
	ob := &mockOutbound{dialFunc: func(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstreamAddr)
	}}
	var wrapped adapter.Outbound = ob
	entry.Outbound.Store(&wrapped)

	entry.SetEgressIP(netip.MustParseAddr("203.0.113.10"))
	if entry.LatencyTable == nil {
		b.Fatal("latency table should be initialized")
	}
	entry.LatencyTable.Update("example.com", 20*time.Millisecond, 10*time.Minute)
	pool.RecordResult(hash, true)
	pool.NotifyNodeDirty(hash)

	router := routing.NewRouter(routing.RouterConfig{
		Pool:        pool,
		Authorities: func() []string { return []string{"example.com"} },
		P2CWindow:   func() time.Duration { return 10 * time.Minute },
	})

	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "bench-token",
		Router:     router,
		Pool:       pool,
		// The shared test recorder keeps the benchmark off the real health
		// bookkeeping, which records asynchronously and would add noise the
		// change under test did not cause.
		Health: &mockHealthRecorder{},
		Events: NoOpEventEmitter{},
	})
	return &benchEnv{pool: pool, router: router}, fp
}

func BenchmarkProxyForwardHTTP(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("bench-body"))
	}))
	defer upstream.Close()

	_, fp := newBenchEnv(b, upstream.Listener.Addr().String())

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, upstream.URL+"/bench", nil)
		req.Header.Set("Proxy-Authorization", basicAuth("plat", "bench-token"))
		rec := httptest.NewRecorder()
		fp.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
	}
}

func BenchmarkProxyForwardHTTPParallel(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("bench-body"))
	}))
	defer upstream.Close()

	_, fp := newBenchEnv(b, upstream.Listener.Addr().String())

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest(http.MethodGet, upstream.URL+"/bench", nil)
			req.Header.Set("Proxy-Authorization", basicAuth("plat", "bench-token"))
			rec := httptest.NewRecorder()
			fp.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("status = %d, want 200", rec.Code)
			}
		}
	})
}

// BenchmarkProxyForwardAuthenticate isolates the credential check, so a
// regression in verification can be told apart from one in the transfer path.
func BenchmarkProxyForwardAuthenticate(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	_, fp := newBenchEnv(b, upstream.Listener.Addr().String())

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, upstream.URL+"/bench", nil)
		req.Header.Set("Proxy-Authorization", basicAuth("plat", "bench-token"))
		if _, err := fp.authenticate(req); err != nil {
			b.Fatalf("authenticate: %v", err)
		}
	}
}

// BenchmarkProxyForwardLargeBody exercises the copy path, which is where byte
// counting runs; a metering change shows up here first.
func BenchmarkProxyForwardLargeBody(b *testing.B) {
	const bodySize = 1 << 20 // 1 MiB
	payload := make([]byte, bodySize)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	_, fp := newBenchEnv(b, upstream.Listener.Addr().String())

	b.SetBytes(bodySize)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, upstream.URL+"/bench-large", nil)
		req.Header.Set("Proxy-Authorization", basicAuth("plat", "bench-token"))
		rec := httptest.NewRecorder()
		fp.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.Len() != bodySize {
			b.Fatalf("body = %d bytes, want %d", rec.Body.Len(), bodySize)
		}
	}
}
