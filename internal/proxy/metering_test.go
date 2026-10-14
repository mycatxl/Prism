package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"prism/internal/keys"
	"prism/internal/model"
)

// recordingMeter captures what the proxy attributed, so a test can assert the
// accounting without a real accounting implementation.
type recordingMeter struct {
	mu       sync.Mutex
	traffic  map[string]int64
	requests map[string]int
	failures map[string]int
}

func newRecordingMeter() *recordingMeter {
	return &recordingMeter{
		traffic:  make(map[string]int64),
		requests: make(map[string]int),
		failures: make(map[string]int),
	}
}

func (m *recordingMeter) OnKeyTrafficDelta(keyID, _, _ string, ingress, egress int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.traffic[keyID] += ingress + egress
}

func (m *recordingMeter) OnKeyRequest(keyID, _, _ string, failed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[keyID]++
	if failed {
		m.failures[keyID]++
	}
}

func (m *recordingMeter) bytesFor(keyID string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.traffic[keyID]
}

func (m *recordingMeter) requestsFor(keyID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests[keyID]
}

func (m *recordingMeter) attributedKeyCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, bytes := range m.traffic {
		if bytes > 0 {
			count++
		}
	}
	return count
}

// gateStub records how often it was consulted and answers with a fixed verdict.
type gateStub struct {
	mu      sync.Mutex
	calls   int
	refused bool
}

func (g *gateStub) Check(*keys.KeyState, int) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	return !g.refused, "traffic"
}

func (g *gateStub) Exceeded(*keys.KeyState, int) bool { return false }

func (g *gateStub) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// keyVerifierStub resolves one credential to one key state, so the entry points
// can be driven through their real authentication path.
type keyVerifierStub struct {
	credential string
	state      *keys.KeyState
}

func (v keyVerifierStub) VerifyCredential(credential string) *keys.KeyState {
	if credential == v.credential {
		return v.state
	}
	return nil
}

func workingKeyState() *keys.KeyState {
	return &keys.KeyState{
		KeyID:      "k-meter",
		UserID:     "u-meter",
		Scope:      model.KeyScopeProxy,
		Status:     model.KeyStatusActive,
		UserStatus: model.UserStatusActive,
		Plan:       keys.PlanSnapshot{Status: model.SubscriptionStatusActive},
	}
}

// testCredential is a credential of the shape the entry points accept:
// pk_<32 hex>_<64 hex>. The verifier stub matches on it verbatim, so the tests
// exercise the real shape parsing without needing a real key store.
const testCredential = "pk_0123456789abcdef0123456789abcdef_" +
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// A forward HTTP request is attributed to the key that made it, and the request
// count lands once the request ends.
func TestMetering_ForwardHTTPAttributesTrafficToTheKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("metered-body"))
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})

	meter := newRecordingMeter()
	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		AccessKeys: keyVerifierStub{credential: testCredential, state: workingKeyState()},
		Router:     env.router,
		Pool:       env.pool,
		Health:     &mockHealthRecorder{},
		Events:     newMockEventEmitter(),
		Metering:   meter,
	})

	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/metered", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
	w := httptest.NewRecorder()
	fp.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q, err %q)", w.Code, w.Body.String(), w.Header().Get("X-Prism-Error"))
	}

	if got := meter.bytesFor("k-meter"); got < int64(len("metered-body")) {
		t.Fatalf("attributed %d bytes to the key, want at least %d", got, len("metered-body"))
	}
	if got := meter.requestsFor("k-meter"); got != 1 {
		t.Fatalf("attributed %d requests, want 1", got)
	}
}

// A refused key never reaches the upstream, and is not attributed anything.
func TestMetering_QuotaRefusalHappensBeforeTheUpstream(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})

	gate := &gateStub{refused: true}
	meter := newRecordingMeter()
	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		AccessKeys: keyVerifierStub{credential: testCredential, state: workingKeyState()},
		Router:     env.router,
		Pool:       env.pool,
		Health:     &mockHealthRecorder{},
		Events:     newMockEventEmitter(),
		Metering:   meter,
		Quota:      gate,
	})

	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/refused", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
	w := httptest.NewRecorder()
	fp.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body %q, err %q)", w.Code, w.Body.String(), w.Header().Get("X-Prism-Error"))
	}
	if got := w.Header().Get("X-Prism-Error"); got != "QUOTA_EXCEEDED" {
		t.Fatalf("X-Prism-Error = %q, want QUOTA_EXCEEDED", got)
	}
	if got := gate.callCount(); got != 1 {
		t.Fatalf("the gate was consulted %d times, want 1", got)
	}
	if got := upstreamCalls.Load(); got != 0 {
		t.Fatalf("the upstream was reached %d times for a refused request, want 0", got)
	}
	if got := meter.bytesFor("k-meter"); got != 0 {
		t.Fatalf("%d bytes were attributed to a refused request, want 0", got)
	}
}

// A request carrying no key is attributed to nobody: the transitional token
// path has no allowance and must not be billed to an arbitrary key.
func TestMetering_TokenPathIsNotAttributed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})

	meter := newRecordingMeter()
	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		Router:     env.router,
		Pool:       env.pool,
		Health:     &mockHealthRecorder{},
		Events:     newMockEventEmitter(),
		Metering:   meter,
	})

	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/token", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("plat", "tok"))
	w := httptest.NewRecorder()
	fp.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if got := meter.attributedKeyCount(); got != 0 {
		t.Fatalf("the token path attributed traffic to %d keys, want 0", got)
	}
}

// The reverse proxy meters through the lifecycle too, because its upstream
// transport is pooled exactly like the forward proxy's.
func TestMetering_ReverseAttributesTrafficToTheKey(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("reverse-metered"))
	}))
	defer backend.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", backend.Listener.Addr().String())
	})

	meter := newRecordingMeter()
	rp := NewReverseProxy(ReverseProxyConfig{
		ProxyToken:     "tok",
		AccessKeys:     keyVerifierStub{credential: testCredential, state: workingKeyState()},
		Router:         env.router,
		Pool:           env.pool,
		PlatformLookup: env.pool,
		Health:         &mockHealthRecorder{},
		Events:         newMockEventEmitter(),
		Metering:       meter,
	})

	host := strings.TrimPrefix(backend.URL, "http://")
	path := fmt.Sprintf("/%s/plat:acct/http/%s/metered", url.PathEscape(testCredential), host)

	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q, err %q)", w.Code, w.Body.String(), w.Header().Get("X-Prism-Error"))
	}
	if got := meter.bytesFor("k-meter"); got < int64(len("reverse-metered")) {
		t.Fatalf("attributed %d bytes, want at least %d", got, len("reverse-metered"))
	}
	if got := meter.requestsFor("k-meter"); got != 1 {
		t.Fatalf("attributed %d requests, want 1", got)
	}
}

// A CONNECT tunnel reports its bytes while it is still open, not only when it
// closes. That is what lets an over-quota transfer be cut off mid-stream.
func TestMetering_ConnectTunnelReportsBytesMidStream(t *testing.T) {
	env := newProxyE2EEnv(t)

	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetLn.Close()

	// The target reads and holds, so the tunnel stays open while the assertion
	// runs.
	received := make(chan int, 1)
	go func() {
		conn, err := targetLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, trafficFlushThreshold*2)
		n, _ := io.ReadFull(conn, buf)
		received <- n
		// Hold the connection open until the test finishes.
		time.Sleep(2 * time.Second)
	}()

	meter := newRecordingMeter()
	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		AccessKeys: keyVerifierStub{credential: testCredential, state: workingKeyState()},
		Router:     env.router,
		Pool:       env.pool,
		Health:     &mockHealthRecorder{},
		Events:     newMockEventEmitter(),
		Metering:   meter,
	})
	proxySrv := httptest.NewServer(fp)
	defer proxySrv.Close()

	proxyAddr := strings.TrimPrefix(proxySrv.URL, "http://")
	clientConn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer clientConn.Close()

	targetAddr := targetLn.Addr().String()
	req := fmt.Sprintf(
		"CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n",
		targetAddr,
		targetAddr,
		basicAuth("plat", testCredential),
	)
	if _, err := clientConn.Write([]byte(req)); err != nil {
		t.Fatalf("write connect request: %v", err)
	}

	reader := bufio.NewReader(clientConn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(statusLine, "200 Connection Established") {
		t.Fatalf("unexpected CONNECT status line: %q", statusLine)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read response headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}

	// Push more than one flush threshold through the tunnel. The counting
	// connection emits a delta at that point without waiting for the tunnel to
	// end.
	payload := make([]byte, trafficFlushThreshold*2)
	if _, err := clientConn.Write(payload); err != nil {
		t.Fatalf("write into the tunnel: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if meter.bytesFor("k-meter") > 0 {
			return // reported while the tunnel is still open
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no bytes were attributed while the tunnel was open (attributed: %d)", meter.bytesFor("k-meter"))
}

// The platform id is recorded from the route, so usage rows can be split by
// platform rather than only by key.
func TestMetering_RecordsThePlatformID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})

	meter := &platformRecordingMeter{}
	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		AccessKeys: keyVerifierStub{credential: testCredential, state: workingKeyState()},
		Router:     env.router,
		Pool:       env.pool,
		Health:     &mockHealthRecorder{},
		Events:     newMockEventEmitter(),
		Metering:   meter,
	})

	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/plat", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
	w := httptest.NewRecorder()
	fp.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := meter.platformID(); got == "" {
		t.Fatal("the platform id was not recorded, so usage cannot be split by platform")
	}
}

type platformRecordingMeter struct {
	mu  sync.Mutex
	pid string
}

func (m *platformRecordingMeter) OnKeyTrafficDelta(_, _, platformID string, ingress, egress int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ingress+egress > 0 {
		m.pid = platformID
	}
}

func (m *platformRecordingMeter) OnKeyRequest(string, string, string, bool) {}

func (m *platformRecordingMeter) platformID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pid
}

// A tunnel's bytes must be counted exactly once.
//
// This is the assertion the whole design turns on. The bytes reach the meter
// from the connection - it flushes every 32 KB so an over-quota transfer can be
// cut mid-stream - while the same totals also reach the request log through the
// lifecycle. If the lifecycle also reported them, every tunnel would bill its key
// twice, and a quota would run out at half the traffic the user actually bought.
//
// An echo of N bytes moves N out and N back, so the exact answer is 2N.
func TestMetering_ConnectTunnelCountsEachByteExactlyOnce(t *testing.T) {
	env := newProxyE2EEnv(t)

	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetLn.Close()

	go func() {
		conn, err := targetLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn) // echo
	}()

	meter := newRecordingMeter()
	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		AccessKeys: keyVerifierStub{credential: testCredential, state: workingKeyState()},
		Router:     env.router,
		Pool:       env.pool,
		Health:     &mockHealthRecorder{},
		Events:     newMockEventEmitter(),
		Metering:   meter,
	})

	conn, reader := openTunnel(t, fp, targetLn.Addr().String())

	// Two flush thresholds, so the counting connection flushes mid-stream and
	// the close flush carries only the remainder. Both paths are exercised.
	const payloadLen = int(trafficFlushThreshold) + 4096
	payload := make([]byte, payloadLen)
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write into the tunnel: %v", err)
	}
	echo := make([]byte, payloadLen)
	if _, err := io.ReadFull(reader, echo); err != nil {
		t.Fatalf("read the echo: %v", err)
	}

	// Closing flushes the last of the bytes that are still pending.
	_ = conn.Close()

	want := int64(2 * payloadLen)
	deadline := time.Now().Add(2 * time.Second)
	var got int64
	for time.Now().Before(deadline) {
		got = meter.bytesFor("k-meter")
		if got >= want {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got != want {
		t.Fatalf("attributed %d bytes for an echo of %d, want exactly %d (twice that would mean the lifecycle "+
			"reported the connection's bytes a second time)", got, payloadLen, want)
	}
}
