package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"prism/internal/keys"
	"prism/internal/usage"
)

// midStreamBodyLen is the upstream body every mid-stream test serves. It is far
// larger than the allowance the key is given, so a transfer that reaches the
// client in full proves the limit was not enforced on the stream.
const midStreamBodyLen = 1 << 20

// midStreamChunk is how much a test writes into a tunnel before it waits for
// the echo. Pacing the client is what makes a mid-stream cut observable: a
// client that dumps the whole body at once can have the tunnel closed under it
// before a single byte comes back.
const midStreamChunk = 8 * 1024

// midStreamAllowance is the key's traffic allowance in the mid-stream tests.
const midStreamAllowance = 64 * 1024

// countingGate is a QuotaGate whose verdict a test can flip while a stream is
// running, which is what the live check is supposed to notice.
type countingGate struct {
	mu       sync.Mutex
	exceeded bool
	checks   int
}

func (g *countingGate) Check(*keys.KeyState, int) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.checks++
	return true, ""
}

func (g *countingGate) Exceeded(*keys.KeyState, int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.exceeded
}

func (g *countingGate) trip() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.exceeded = true
}

// newQuotaForwardProxy wires the accounting the way the process does - one
// counter set, one registry, one limiter, and the adapters between them and the
// proxy - and hands back the pieces a test observes it with.
func newQuotaForwardProxy(
	t *testing.T,
	env *proxyE2EEnv,
	store AccessKeyVerifier,
) (*ForwardProxy, *mockEventEmitter, *usage.Counters, *usage.ConnRegistry) {
	t.Helper()

	counters := usage.NewCounters()
	registry := usage.NewConnRegistry()
	emitter := newMockEventEmitter()

	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		AccessKeys: store,
		Router:     env.router,
		Pool:       env.pool,
		Health:     &mockHealthRecorder{},
		Events:     emitter,
		Metering:   &usage.Sink{Counters: counters},
		Quota:      &usage.Gate{Limiter: usage.NewLimiter(counters)},
		Sessions:   registry,
	})
	return fp, emitter, counters, registry
}

// waitForRequestLog returns the next request log entry, or fails the test.
func waitForRequestLog(t *testing.T, emitter *mockEventEmitter) RequestLogEntry {
	t.Helper()

	select {
	case ev := <-emitter.logCh:
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no request log entry was emitted")
		return RequestLogEntry{}
	}
}

// waitForSessionCount waits for the registry to report exactly want sessions for
// the key.
func waitForSessionCount(t *testing.T, registry *usage.ConnRegistry, keyID string, want int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := registry.Count(keyID); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("registry holds %d sessions for %s, want %d", registry.Count(keyID), keyID, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// A stream that runs past its allowance is cut while it is still running, and
// the cut happens once: the copy loops that notice it stop, they do not each
// report a new cut.
func TestStreamQuota_CutsAStreamThatRunsPastItsAllowance(t *testing.T) {
	gate := &countingGate{}
	aborts := make(chan struct{}, 4)
	quota := newStreamQuota(gate, workingKeyState(), 0, func() { aborts <- struct{}{} })
	if quota == nil {
		t.Fatal("a gate and a key must produce an enforcer")
	}
	quota.interval = 8

	quota.egressBytes(7)
	select {
	case <-aborts:
		t.Fatal("a stream below its allowance was cut")
	default:
	}
	if quota.Exceeded() {
		t.Fatal("Exceeded reports a cut the gate never asked for")
	}

	quota.ingressBytes(1)
	select {
	case <-aborts:
		t.Fatal("the stream was cut while the gate still allowed it")
	default:
	}

	gate.trip()
	quota.ingressBytes(8)
	select {
	case <-aborts:
	default:
		t.Fatal("a stream past its allowance was not cut")
	}
	if !quota.Exceeded() {
		t.Fatal("the cut is not reported, so a copy loop cannot tell it from a network error")
	}

	// The cut is one-shot: further steps must not abort again.
	quota.egressBytes(64)
	select {
	case <-aborts:
		t.Fatal("the stream was cut twice")
	default:
	}
}

// A nil gate and a request with no key both leave the stream unenforced, which
// is what keeps a deployment without quotas behaving as it did before.
func TestStreamQuota_IsInertWithoutAGateOrKey(t *testing.T) {
	if got := newStreamQuota(nil, workingKeyState(), 0, nil); got != nil {
		t.Fatal("a nil gate produced an enforcer")
	}
	if got := newStreamQuota(&countingGate{}, nil, 0, nil); got != nil {
		t.Fatal("a nil key produced an enforcer")
	}

	var quota *streamQuota
	quota.ingressBytes(1024)
	quota.egressBytes(1024)
	quota.flushReports()
	if quota.Exceeded() {
		t.Fatal("a nil enforcer reports a cut")
	}
	if got := quota.withLiveReports(func(int64) {}, func(int64) {}); got != nil {
		t.Fatal("a nil enforcer accepted live reporters")
	}
}

// The bytes of a live stream reach the key's sink in whole steps, and the tail
// of a partial step reaches it on the flush - once, never twice.
func TestStreamQuota_ReportsItsBytesToTheSinkInWholeSteps(t *testing.T) {
	quota := newStreamQuota(&countingGate{}, workingKeyState(), 0, nil)
	quota.interval = 4

	var ingress, egress int64
	ingressSteps, egressSteps := 0, 0
	quota.withLiveReports(
		func(n int64) { ingress += n; ingressSteps++ },
		func(n int64) { egress += n; egressSteps++ },
	)

	for i := 0; i < 10; i++ {
		quota.egressBytes(1)
	}
	if egress != 8 || egressSteps != 2 {
		t.Fatalf("reported %d bytes in %d steps, want 8 in 2 (two whole steps of 4)", egress, egressSteps)
	}

	quota.flushReports()
	if egress != 10 || egressSteps != 3 {
		t.Fatalf("after the flush: %d bytes in %d steps, want 10 in 3", egress, egressSteps)
	}
	quota.flushReports()
	if egress != 10 || egressSteps != 3 {
		t.Fatalf("the flush reported the tail twice: %d bytes in %d steps", egress, egressSteps)
	}
	if ingress != 0 || ingressSteps != 0 {
		t.Fatalf("a half-emptied ingress reported %d bytes in %d steps, want nothing", ingress, ingressSteps)
	}
}

// The reader is what stops a copy loop whose stream has run out of allowance: it
// reports the bytes that did move together with the error that ends the loop.
func TestQuotaReader_StopsTheCopyWhenTheAllowanceIsGone(t *testing.T) {
	gate := &countingGate{}
	quota := newStreamQuota(gate, workingKeyState(), 0, nil)
	quota.interval = 4

	reader := &quotaReader{reader: bytes.NewReader(make([]byte, 64)), quota: quota}
	buf := make([]byte, 8)

	n, err := reader.Read(buf)
	if n != 8 || err != nil {
		t.Fatalf("first read = (%d, %v), want (8, nil)", n, err)
	}

	gate.trip()
	n, err = reader.Read(buf)
	if !errors.Is(err, errStreamQuotaExceeded) {
		t.Fatalf("read error = %v, want %v", err, errStreamQuotaExceeded)
	}
	if n != 8 {
		t.Fatalf("n = %d, want 8: the bytes that did move must still be reported", n)
	}
}

// A request body is read by the transport, so its last partial step only reaches
// the sink when the transport closes it.
func TestQuotaReadCloser_FlushesTheTailOnClose(t *testing.T) {
	quota := newStreamQuota(&countingGate{}, workingKeyState(), 0, nil)
	quota.interval = 4

	var reported int64
	quota.withLiveReports(nil, func(n int64) { reported += n })

	body := newQuotaReadCloser(io.NopCloser(bytes.NewReader([]byte("abcdefghij"))), quota)
	buf := make([]byte, 1)
	for i := 0; i < 10; i++ {
		if _, err := body.Read(buf); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	if reported != 8 {
		t.Fatalf("reported %d bytes before the close, want the 8 of two whole steps", reported)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if reported != 10 {
		t.Fatalf("reported %d bytes after the close, want 10", reported)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if reported != 10 {
		t.Fatalf("the tail was reported twice: %d", reported)
	}
}

// An ingress body (a reverse-proxied response) is charged to the ingress side,
// so the two directions of one stream are billed apart.
func TestQuotaReadCloserDir_ChargesTheIngressSide(t *testing.T) {
	quota := newStreamQuota(&countingGate{}, workingKeyState(), 0, nil)
	quota.interval = 4

	var ingress, egress int64
	quota.withLiveReports(func(n int64) { ingress += n }, func(n int64) { egress += n })

	body := newQuotaReadCloserDir(io.NopCloser(bytes.NewReader([]byte("abcd"))), quota, true)
	if _, err := io.Copy(io.Discard, body); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if ingress != 4 {
		t.Fatalf("ingress reported %d bytes, want 4", ingress)
	}
	if egress != 0 {
		t.Fatalf("egress reported %d bytes for an ingress body, want 0", egress)
	}
}

// The writer is what ends the copy that feeds the client: it reports the bytes
// that reached it and refuses to continue once the allowance is gone.
func TestQuotaResponseWriter_EndsTheCopyWithAnError(t *testing.T) {
	gate := &countingGate{}
	quota := newStreamQuota(gate, workingKeyState(), 0, nil)
	quota.interval = 4

	rec := httptest.NewRecorder()
	writer := &quotaResponseWriter{ResponseWriter: rec, quota: quota}

	if _, err := writer.Write([]byte("abcd")); err != nil {
		t.Fatalf("write below the allowance: %v", err)
	}
	gate.trip()

	n, err := writer.Write([]byte("efgh"))
	if !errors.Is(err, errStreamQuotaExceeded) {
		t.Fatalf("write error = %v, want %v", err, errStreamQuotaExceeded)
	}
	if n != 4 {
		t.Fatalf("n = %d, want 4: the caller's own count has to stay truthful", n)
	}
	if got := rec.Body.String(); got != "abcdefgh" {
		t.Fatalf("the client body is %q, want what was actually written", got)
	}
	if got := writer.Unwrap(); got != http.ResponseWriter(rec) {
		t.Fatal("Unwrap does not expose the wrapped writer, which breaks httputil's hijack and flush paths")
	}
}

// The configured tolerance: zero asks for the default, a negative value asks for
// none, and anything else is taken as written.
func TestNormalizeQuotaGracePercent(t *testing.T) {
	if got := normalizeQuotaGracePercent(0); got != DefaultStreamGracePercent {
		t.Fatalf("grace for 0 = %d, want %d", got, DefaultStreamGracePercent)
	}
	if got := normalizeQuotaGracePercent(-5); got != 0 {
		t.Fatalf("grace for a negative value = %d, want 0", got)
	}
	if got := normalizeQuotaGracePercent(7); got != 7 {
		t.Fatalf("grace for 7 = %d, want 7", got)
	}
}

// The response half of a forward request is enforced on the live stream: a key
// whose allowance runs out mid-download has the rest of it cut, and the request
// log records what actually moved.
func TestForwardProxy_HTTPResponseIsCutMidStreamWhenTheAllowanceRunsOut(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), midStreamBodyLen)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})

	key := workingKeyState()
	key.Plan.TrafficBytes = midStreamAllowance
	store := &mutableKeyStore{state: key}
	fp, emitter, _, registry := newQuotaForwardProxy(t, env, store)

	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/large", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
	rec := httptest.NewRecorder()
	fp.ServeHTTP(rec, req)

	body := rec.Body.Bytes()
	if int64(len(body)) >= midStreamBodyLen {
		t.Fatalf("the whole %d-byte response reached the client: the allowance was not enforced mid-stream", len(body))
	}
	if len(body) == 0 {
		t.Fatal("nothing reached the client, so the transfer was cut before it started")
	}

	logEv := waitForRequestLog(t, emitter)
	if logEv.PrismError != ErrQuotaExceeded.PrismError {
		t.Fatalf("PrismError = %q, want %q", logEv.PrismError, ErrQuotaExceeded.PrismError)
	}
	if !logEv.NetOK {
		t.Fatal("a quota cut was recorded as a node failure")
	}
	if logEv.IngressBytes <= 0 || logEv.IngressBytes >= midStreamBodyLen {
		t.Fatalf("IngressBytes = %d, want the partial amount that actually reached the client", logEv.IngressBytes)
	}
	if got := registry.Count("k-meter"); got != 0 {
		t.Fatalf("%d sessions are still registered after the request ended", got)
	}
}

// The request half of the same rule: an upload that would run past the allowance
// is cut while it is still flowing, and the bytes that did leave the client are
// accounted - not the whole body, and not nothing.
func TestForwardProxy_UploadIsCutMidStreamWhenTheAllowanceRunsOut(t *testing.T) {
	var received atomic.Int64
	handlerDone := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		n, _ := io.Copy(io.Discard, r.Body)
		received.Store(n)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})

	key := workingKeyState()
	key.Plan.TrafficBytes = midStreamAllowance
	store := &mutableKeyStore{state: key}
	fp, emitter, _, registry := newQuotaForwardProxy(t, env, store)

	body := bytes.Repeat([]byte("z"), midStreamBodyLen)
	req := httptest.NewRequest(http.MethodPost, upstream.URL+"/upload", bytes.NewReader(body))
	req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
	rec := httptest.NewRecorder()
	fp.ServeHTTP(rec, req)

	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream never stopped reading the body")
	}
	if got := received.Load(); got >= int64(len(body)) {
		t.Fatalf("the upstream received all %d bytes: the upload was not cut", got)
	}

	logEv := waitForRequestLog(t, emitter)
	if logEv.EgressBytes <= 0 {
		t.Fatal("the partial upload was not accounted at all")
	}
	if logEv.EgressBytes > int64(len(body)) {
		t.Fatalf("EgressBytes = %d, want no more than the %d bytes that were sent", logEv.EgressBytes, len(body))
	}
	if got := registry.Count("k-meter"); got != 0 {
		t.Fatalf("%d sessions are still registered after the request ended", got)
	}
}

// A reverse-proxied response is enforced too, and the accounting survives the
// unwind: httputil panics with http.ErrAbortHandler when a real server's copy
// fails, which is exactly how a quota cut ends a download.
func TestReverseProxy_HTTPResponseIsCutMidStreamWhenTheAllowanceRunsOut(t *testing.T) {
	payload := bytes.Repeat([]byte("r"), midStreamBodyLen)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})

	key := workingKeyState()
	key.Plan.TrafficBytes = midStreamAllowance
	counters := usage.NewCounters()
	registry := usage.NewConnRegistry()
	emitter := newMockEventEmitter()
	rp := NewReverseProxy(ReverseProxyConfig{
		ProxyToken: "tok",
		AccessKeys: keyVerifierStub{credential: testCredential, state: key},
		Router:     env.router,
		Pool:       env.pool,
		// PlatformLookup resolves the platform the path names, which is what
		// decides the account rules; the pool serves both.
		PlatformLookup: env.pool,
		Health:         &mockHealthRecorder{},
		Events:         emitter,
		Metering:       &usage.Sink{Counters: counters},
		Quota:          &usage.Gate{Limiter: usage.NewLimiter(counters)},
		Sessions:       registry,
	})

	// The server context is what tells httputil to panic rather than to swallow a
	// failed copy, so the request carries one.
	req := httptest.NewRequest(http.MethodGet, "http://example.com/"+testCredential+"/plat:acct/http/example.com/large", nil)
	req = req.WithContext(context.WithValue(req.Context(), http.ServerContextKey, &http.Server{}))
	rec := httptest.NewRecorder()

	func() {
		defer func() {
			if r := recover(); r != http.ErrAbortHandler {
				t.Fatalf("ServeHTTP panicked with %v, want %v", r, http.ErrAbortHandler)
			}
		}()
		rp.ServeHTTP(rec, req)
	}()

	body := rec.Body.Bytes()
	if int64(len(body)) >= midStreamBodyLen {
		t.Fatalf("the whole %d-byte response reached the client: the allowance was not enforced mid-stream", len(body))
	}
	if len(body) == 0 {
		t.Fatal("nothing reached the client, so the transfer was cut before it started")
	}

	logEv := waitForRequestLog(t, emitter)
	if logEv.PrismError != ErrQuotaExceeded.PrismError {
		t.Fatalf("PrismError = %q, want %q", logEv.PrismError, ErrQuotaExceeded.PrismError)
	}
	if !logEv.NetOK {
		t.Fatal("a quota cut was recorded as a node failure")
	}
	if logEv.IngressBytes <= 0 || logEv.IngressBytes >= midStreamBodyLen {
		t.Fatalf("IngressBytes = %d, want the partial amount that actually reached the client", logEv.IngressBytes)
	}
	if got := registry.Count("k-meter"); got != 0 {
		t.Fatalf("%d sessions are still registered after the aborted request", got)
	}
}

// A CONNECT tunnel is cut mid-stream once the allowance is gone, rather than
// only being refused on its next request.
func TestForwardProxy_CONNECTTunnelIsCutMidStreamWhenTheAllowanceRunsOut(t *testing.T) {
	env := newProxyE2EEnv(t)
	key := workingKeyState()
	key.Plan.TrafficBytes = midStreamAllowance
	store := &mutableKeyStore{state: key}
	fp, _, counters, registry := newQuotaForwardProxy(t, env, store)

	conn, reader := openTunnel(t, fp, echoTarget(t))
	waitForSessionCount(t, registry, "k-meter", 1)

	// The client is paced by its own echoes: each round trip proves the bytes
	// of the step before it really flowed, so the cut is observed mid-stream
	// instead of racing a client that dumped its whole body in one call.
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	chunk := bytes.Repeat([]byte("y"), midStreamChunk)
	echo := make([]byte, len(chunk))
	var written, echoed int64
	for written < midStreamBodyLen {
		// The cut closes the tunnel under the writer, so its error ends the
		// loop just as the closed reader's does.
		if _, err := conn.Write(chunk); err != nil {
			break
		}
		written += int64(len(chunk))
		n, err := io.ReadFull(reader, echo)
		echoed += int64(n)
		if err != nil {
			break
		}
	}

	if echoed == 0 {
		t.Fatal("nothing flowed through the tunnel before it was cut")
	}
	if echoed >= midStreamBodyLen {
		t.Fatalf("the tunnel carried the whole %d-byte stream: the allowance was not enforced mid-stream", midStreamBodyLen)
	}
	if got := counters.UserBytes("u-meter"); got < midStreamAllowance {
		t.Fatalf("the key is charged %d bytes after the cut, want at least its %d-byte allowance", got, midStreamAllowance)
	}

	waitForSessionCount(t, registry, "k-meter", 0)
}

// An HTTP request is a session of its key exactly as a tunnel is: it is
// registered while it runs, so a revocation ends a request that is already in
// flight, and the count is released on that error path too.
func TestForwardProxy_HTTPRequestIsRegisteredAndCancelledWithItsKey(t *testing.T) {
	upstreamReached := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(upstreamReached)
		// Hold the response until the client goes away, so the request stays in
		// flight for as long as the test needs it to.
		<-r.Context().Done()
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})

	key := workingKeyState()
	store := &mutableKeyStore{state: key}
	fp, _, _, registry := newQuotaForwardProxy(t, env, store)

	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/slow", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
	rec := httptest.NewRecorder()

	served := make(chan struct{})
	go func() {
		defer close(served)
		fp.ServeHTTP(rec, req)
	}()

	select {
	case <-upstreamReached:
	case <-time.After(3 * time.Second):
		t.Fatal("the request never reached the upstream")
	}
	waitForSessionCount(t, registry, "k-meter", 1)

	if closed := registry.CloseKey("k-meter"); closed != 1 {
		t.Fatalf("CloseKey closed %d sessions, want 1", closed)
	}
	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("the in-flight HTTP request survived the revocation of its key")
	}
	waitForSessionCount(t, registry, "k-meter", 0)
}

// A SOCKS5 session that fails before it is registered leaves nothing behind: a
// refused dial must not leak a session count that only ever grows.
func TestSocks5Inbound_DialFailureLeavesNoSessionRegistered(t *testing.T) {
	env := newProxyE2EEnv(t)
	key := workingKeyState()
	store := &mutableKeyStore{state: key}
	counters := usage.NewCounters()
	registry := usage.NewConnRegistry()

	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return nil, genericErr{}
	})

	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken: "tok",
		AccessKeys: store,
		Router:     env.router,
		Pool:       env.pool,
		Health:     &mockHealthRecorder{},
		Events:     newMockEventEmitter(),
		Metering:   &usage.Sink{Counters: counters},
		Quota:      &usage.Gate{Limiter: usage.NewLimiter(counters)},
		Sessions:   registry,
	})

	clientConn, reader, done := startSocks5Session(t, inbound)
	defer clientConn.Close()

	writeAll(t, clientConn, []byte{socks5Version, 1, socks5MethodUserPass})
	if got := readExactly(t, reader, 2); got[1] != socks5MethodUserPass {
		t.Fatalf("selected method: got %d, want %d", got[1], socks5MethodUserPass)
	}
	writeAll(t, clientConn, socks5UserPassPacket("plat.acct", testCredential))
	if got := readExactly(t, reader, 2); got[1] != socks5UserPassStatusSuccess {
		t.Fatalf("auth status: got %d, want success", got[1])
	}

	writeAll(t, clientConn, socks5ConnectIPv4Packet("127.0.0.1:443"))
	reply := readExactly(t, reader, 10)
	if reply[0] != socks5Version || reply[1] != socks5ReplyGeneralFailure {
		t.Fatalf("connect reply: got %v, want general failure", reply)
	}

	_ = clientConn.Close()
	<-done

	if got := registry.Count("k-meter"); got != 0 {
		t.Fatalf("a refused session left %d registrations behind", got)
	}
}

// A SOCKS5 tunnel that is established is registered - so a revocation can end it
// - and unregistered when it ends.
func TestSocks5Inbound_SuccessfulConnectRegistersAndReleasesTheSession(t *testing.T) {
	env := newProxyE2EEnv(t)
	key := workingKeyState()
	store := &mutableKeyStore{state: key}
	counters := usage.NewCounters()
	registry := usage.NewConnRegistry()

	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetLn.Close()
	go func() {
		for {
			conn, err := targetLn.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, targetLn.Addr().String())
	})

	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken: "tok",
		AccessKeys: store,
		Router:     env.router,
		Pool:       env.pool,
		Health:     &mockHealthRecorder{},
		Events:     newMockEventEmitter(),
		Metering:   &usage.Sink{Counters: counters},
		Quota:      &usage.Gate{Limiter: usage.NewLimiter(counters)},
		Sessions:   registry,
	})

	clientConn, reader, done := startSocks5Session(t, inbound)
	defer clientConn.Close()

	writeAll(t, clientConn, []byte{socks5Version, 1, socks5MethodUserPass})
	if got := readExactly(t, reader, 2); got[1] != socks5MethodUserPass {
		t.Fatalf("selected method: got %d, want %d", got[1], socks5MethodUserPass)
	}
	writeAll(t, clientConn, socks5UserPassPacket("plat.acct", testCredential))
	if got := readExactly(t, reader, 2); got[1] != socks5UserPassStatusSuccess {
		t.Fatalf("auth status: got %d, want success", got[1])
	}

	writeAll(t, clientConn, socks5ConnectIPv4Packet(targetLn.Addr().String()))
	reply := readExactly(t, reader, 10)
	if reply[0] != socks5Version || reply[1] != socks5ReplySucceeded {
		t.Fatalf("connect reply: got %v, want success", reply)
	}

	waitForSessionCount(t, registry, "k-meter", 1)

	_ = clientConn.Close()
	<-done
	waitForSessionCount(t, registry, "k-meter", 0)
}

// failingBody hands out its bytes and then reports an error, which is what an
// aborted or timed-out upload looks like at the reader.
type failingBody struct {
	data []byte
	err  error
	done bool
}

func (b *failingBody) Read(p []byte) (int, error) {
	if b.done {
		return 0, b.err
	}
	b.done = true
	return copy(p, b.data), b.err
}

func (b *failingBody) Close() error { return nil }

// An interrupted upload is reported as the bytes that actually moved: losing
// them would make the traffic free, and counting the declared length would bill
// bytes the upstream never saw.
func TestCountingReadCloser_CountsWhatMovedBeforeTheFailure(t *testing.T) {
	body := newCountingReadCloser(&failingBody{data: []byte("hello"), err: io.ErrUnexpectedEOF})

	buf := make([]byte, 64)
	n, err := body.Read(buf)
	if n != 5 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read = (%d, %v), want (5, %v)", n, err, io.ErrUnexpectedEOF)
	}
	if got := body.Total(); got != 5 {
		t.Fatalf("Total = %d, want the 5 bytes that moved", got)
	}

	if _, err := body.Read(buf); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("second read error = %v, want %v", err, io.ErrUnexpectedEOF)
	}
	if got := body.Total(); got != 5 {
		t.Fatalf("Total = %d after a failed read, want it unchanged at 5", got)
	}
}

// Total is read from the handler's goroutine while the transport reads the body
// in its own, so it has to be race-free and monotonic. Run with -race.
func TestCountingReadCloser_TotalIsSafeWhileTheBodyIsStillRead(t *testing.T) {
	const bodyLen = 64 * 1024
	body := newCountingReadCloser(io.NopCloser(bytes.NewReader(make([]byte, bodyLen))))

	copied := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, body)
		copied <- err
	}()

	last := int64(0)
loop:
	for {
		n := body.Total()
		if n < last {
			t.Fatalf("Total went backwards: %d then %d", last, n)
		}
		last = n
		select {
		case err := <-copied:
			if err != nil {
				t.Fatalf("copy: %v", err)
			}
			break loop
		default:
		}
	}

	if got := body.Total(); got != bodyLen {
		t.Fatalf("Total = %d after the whole body, want %d", got, bodyLen)
	}
}
