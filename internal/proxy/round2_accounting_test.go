package proxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	M "github.com/sagernet/sing/common/metadata"
	"prism/internal/keys"
	"prism/internal/usage"
)

type tenantAccountingKeys map[string]*keys.KeyState

func (v tenantAccountingKeys) VerifyCredential(credential string, _ netip.Addr) *keys.KeyState {
	return v[credential]
}

func TestHTTPProxyQuotaAndMetricsCannotSelectAnotherTenant(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "forward"
		if reverse {
			name = "reverse"
		}
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("tenant-accounted-body")) }))
			defer upstream.Close()
			env := newProxyE2EEnv(t)
			setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
				return net.Dial("tcp", upstream.Listener.Addr().String())
			})
			alice, bob := workingKeyState(), workingKeyState()
			alice.KeyID, alice.UserID = "alice-key", "alice"
			bob.KeyID, bob.UserID = "bob-key", "bob"
			alice.Plan.TrafficBytes, bob.Plan.TrafficBytes = 4096, 4096
			const bobCredential = "pk_11111111111111111111111111111111_" + "1111111111111111111111111111111111111111111111111111111111111111"
			verifier := tenantAccountingKeys{testCredential: alice, bobCredential: bob}
			counters := usage.NewCounters()
			counters.Add(usage.CounterKey{KeyID: bob.KeyID, UserID: bob.UserID, PlatformID: "plat-id"}, usage.Delta{IngressBytes: 4096})
			sink, gate := &usage.Sink{Counters: counters}, &usage.Gate{Limiter: usage.NewLimiter(counters)}
			var handler http.Handler
			if reverse {
				handler = NewReverseProxy(ReverseProxyConfig{AccessKeys: verifier, Router: env.router, Pool: env.pool, PlatformLookup: env.pool, Metering: sink, Quota: gate})
			} else {
				handler = NewForwardProxy(ForwardProxyConfig{AccessKeys: verifier, Router: env.router, Pool: env.pool, Events: NoOpEventEmitter{}, Metering: sink, Quota: gate})
			}
			request := func(credential, selectedUser string) *httptest.ResponseRecorder {
				target := upstream.URL + "/resource?user_id=" + selectedUser
				if reverse {
					target = "/" + credential + "/plat:" + selectedUser + "/http/" + strings.TrimPrefix(upstream.URL, "http://") + "/resource?user_id=" + selectedUser
				}
				req := httptest.NewRequest(http.MethodGet, target, nil)
				if !reverse {
					req.Header.Set("Proxy-Authorization", basicAuth("plat:"+selectedUser, credential))
				}
				req.Header.Set("X-User-ID", selectedUser)
				req.Header.Set("X-Tenant-ID", selectedUser)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				return rec
			}
			if rec := request(testCredential, "bob"); rec.Code != http.StatusOK {
				t.Fatalf("Bob's exhausted quota affected Alice: %d %s", rec.Code, rec.Body.String())
			}
			if counters.UserBytes("alice") <= 0 || counters.UserBytes("bob") != 4096 {
				t.Fatal("traffic was charged to the selected tenant instead of the key owner")
			}
			if rec := request(bobCredential, "alice"); rec.Code != http.StatusPaymentRequired {
				t.Fatalf("Bob borrowed Alice's quota: %d %s", rec.Code, rec.Body.String())
			}
			for _, counter := range counters.Keys() {
				if counter.KeyID == alice.KeyID && counter.UserID != alice.UserID || counter.KeyID == bob.KeyID && counter.UserID != bob.UserID {
					t.Fatalf("counter owner disagrees with key: %+v", counter)
				}
			}
			if counters.UserBytes("bob") != 4096 {
				t.Fatal("refused request consumed another user's traffic allowance")
			}
		})
	}
}
