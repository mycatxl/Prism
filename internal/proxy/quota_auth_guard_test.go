package proxy

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"prism/internal/keys"
)

// neighbourCredential is a second, valid access key. It is spelled like
// testCredential - pk_<32 hex>_<64 hex> - because the entry points only treat a
// credential-shaped token as a key.
const neighbourCredential = "pk_fedcba9876543210fedcba9876543210_" +
	"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

// authFailureGuardSpy is the failed-authentication guard a deployment installs
// with PRISM_PROXY_AUTH_FAIL_LIMIT: after limit recorded failures for one client
// address it answers every request from that address with 429 RATE_LIMITED.
//
// It is a faithful stub rather than a mock because the defect is about the
// counting: the guard is asked how many failures it saw for an address, and a
// value above zero for a request that never carried a bad credential is exactly
// the cross-tenant lockout.
type authFailureGuardSpy struct {
	limit int

	mu       sync.Mutex
	failures map[string]int
}

func newAuthFailureGuardSpy(limit int) *authFailureGuardSpy {
	return &authFailureGuardSpy{limit: limit, failures: make(map[string]int)}
}

func (g *authFailureGuardSpy) RecordFailure(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures[ip]++
}

func (g *authFailureGuardSpy) Blocked(ip string) (bool, time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failures[ip] >= g.limit {
		return true, time.Minute
	}
	return false, 0
}

func (g *authFailureGuardSpy) recorded(ip string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.failures[ip]
}

// singleKeyQuotaGate refuses exactly one key and admits every other, which is
// what a shared deployment looks like: one tenant is out of allowance or over
// its concurrency limit, its neighbours are not.
type singleKeyQuotaGate struct {
	refusedKeyID string
	reason       string

	mu      sync.Mutex
	refused int
}

func (g *singleKeyQuotaGate) Check(key *keys.KeyState, _ int) (bool, string) {
	if key != nil && key.KeyID == g.refusedKeyID {
		g.mu.Lock()
		g.refused++
		g.mu.Unlock()
		return false, g.reason
	}
	return true, ""
}

func (g *singleKeyQuotaGate) Exceeded(*keys.KeyState, int) bool { return false }

func (g *singleKeyQuotaGate) refusalCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refused
}

// credentialKeySet resolves several credentials, so one proxy can serve two
// tenants the way a real deployment does.
type credentialKeySet map[string]*keys.KeyState

func (s credentialKeySet) VerifyCredential(credential string, _ netip.Addr) *keys.KeyState {
	return s[credential]
}

// twoTenantKeys returns the refused key and an unrelated neighbour key of
// another user.
func twoTenantKeys() (*keys.KeyState, *keys.KeyState) {
	refused := workingKeyState() // k-meter / u-meter
	neighbour := workingKeyState()
	neighbour.KeyID = "k-neighbour"
	neighbour.UserID = "u-neighbour"
	return refused, neighbour
}

// A refusal the quota gate makes is not an authentication failure, so it must
// not be charged to the failed-auth guard.
//
// The guard counts by client address, not by key. Charging quota refusals to it
// means one tenant that overran its allowance - or opened one tunnel too many -
// eventually gets its whole NAT or office egress blocked with 429 RATE_LIMITED,
// which cuts off every other tenant behind the same address even though their
// credentials are valid. This test drives the over-quota key past the guard's
// threshold and then makes sure the neighbour still gets served.
func TestForwardProxy_QuotaRefusalIsNotChargedToTheAuthFailureGuard(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("served"))
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})

	refusedKey, neighbourKey := twoTenantKeys()
	gate := &singleKeyQuotaGate{refusedKeyID: refusedKey.KeyID, reason: "traffic"}
	guard := newAuthFailureGuardSpy(2)
	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		AccessKeys: credentialKeySet{
			testCredential:      refusedKey,
			neighbourCredential: neighbourKey,
		},
		Router:    env.router,
		Pool:      env.pool,
		Health:    &mockHealthRecorder{},
		Events:    newMockEventEmitter(),
		Quota:     gate,
		AuthGuard: guard,
	})

	// The over-quota tenant asks far more often than the guard's limit.
	const attempts = 8
	for i := 0; i < attempts; i++ {
		target := fmt.Sprintf("http://%s/refused-%d", strings.TrimPrefix(upstream.URL, "http://"), i)
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
		rec := httptest.NewRecorder()
		fp.ServeHTTP(rec, req)

		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("attempt %d: status = %d, want 429 (body %q)", i, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-Prism-Error"); got != ErrQuotaExceeded.PrismError {
			t.Fatalf("attempt %d: X-Prism-Error = %q, want %q", i, got, ErrQuotaExceeded.PrismError)
		}
		if got := rec.Header().Get("X-Prism-Error"); got == ErrRateLimited.PrismError {
			t.Fatalf("attempt %d: a quota refusal was answered as an auth-guard block", i)
		}
	}
	if got := gate.refusalCount(); got != attempts {
		t.Fatalf("the gate refused %d of %d requests", got, attempts)
	}
	if got := guard.recorded("192.0.2.1"); got != 0 {
		t.Fatalf("the failed-auth guard recorded %d failures for a client whose credential was valid, want 0", got)
	}

	// The neighbour sits behind the same address. Its credential is valid and
	// its allowance is untouched, so nothing about it may be refused.
	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/neighbour", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("plat", neighbourCredential))
	rec := httptest.NewRecorder()
	fp.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("the neighbour was answered %d (%q), want 200: one key's quota refusal blocked another tenant",
			rec.Code, rec.Header().Get("X-Prism-Error"))
	}
	if body := rec.Body.String(); body != "served" {
		t.Fatalf("the neighbour body = %q, want %q", body, "served")
	}

	// A genuine authentication failure is still recorded: the guard has to keep
	// working, otherwise this test would pass with the whole mechanism removed.
	badReq := httptest.NewRequest(http.MethodGet, upstream.URL+"/bad", nil)
	badReq.Header.Set("Proxy-Authorization", basicAuth("plat", "not-a-credential-and-not-a-token"))
	badRec := httptest.NewRecorder()
	fp.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusForbidden {
		t.Fatalf("a bad credential was answered %d, want 403", badRec.Code)
	}
	if got := guard.recorded("192.0.2.1"); got != 1 {
		t.Fatalf("the guard recorded %d failures after one bad credential, want 1", got)
	}
}

// connectStatusLine runs one CONNECT through the proxy and returns the status
// line it answered with, so a refusal is visible as the client sees it.
func connectStatusLine(t *testing.T, proxyAddr, target, credential string) string {
	t.Helper()

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	request := fmt.Sprintf(
		"CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n",
		target, target, basicAuth("plat", credential),
	)
	if _, writeErr := conn.Write([]byte(request)); writeErr != nil {
		t.Fatalf("write CONNECT: %v", writeErr)
	}
	statusLine, readErr := bufio.NewReader(conn).ReadString('\n')
	if readErr != nil {
		t.Fatalf("read CONNECT status: %v", readErr)
	}
	return statusLine
}

// The CONNECT half of the same rule: a refused tunnel reservation is a quota
// verdict, not a bad credential, and the tenant next door keeps its tunnels.
func TestForwardProxy_ConnectQuotaRefusalIsNotChargedToTheAuthFailureGuard(t *testing.T) {
	env := newProxyE2EEnv(t)
	echoAddr := echoTarget(t)
	setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, echoAddr)
	})

	refusedKey, neighbourKey := twoTenantKeys()
	gate := &singleKeyQuotaGate{refusedKeyID: refusedKey.KeyID, reason: "concurrency"}
	guard := newAuthFailureGuardSpy(2)
	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		AccessKeys: credentialKeySet{
			testCredential:      refusedKey,
			neighbourCredential: neighbourKey,
		},
		Router:    env.router,
		Pool:      env.pool,
		Health:    &mockHealthRecorder{},
		Events:    newMockEventEmitter(),
		Quota:     gate,
		AuthGuard: guard,
	})
	server := httptest.NewServer(fp)
	defer server.Close()
	proxyAddr := strings.TrimPrefix(server.URL, "http://")

	for i := 0; i < 6; i++ {
		statusLine := connectStatusLine(t, proxyAddr, echoAddr, testCredential)
		if !strings.Contains(statusLine, "429") {
			t.Fatalf("attempt %d: CONNECT answered %q, want 429", i, statusLine)
		}
	}
	if got := guard.recorded("127.0.0.1"); got != 0 {
		t.Fatalf("the failed-auth guard recorded %d tunnel-quota refusals as authentication failures", got)
	}

	statusLine := connectStatusLine(t, proxyAddr, echoAddr, neighbourCredential)
	if !strings.Contains(statusLine, "200 Connection Established") {
		t.Fatalf("the neighbour's CONNECT answered %q, want 200: one key's limit blocked another tenant",
			statusLine)
	}
	if got := guard.recorded("127.0.0.1"); got != 0 {
		t.Fatalf("the guard recorded %d failures for a served tunnel", got)
	}
}
