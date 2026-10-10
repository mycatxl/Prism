package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"prism/internal/api"
	"prism/internal/model"
)

// TestListenerHTTPServerAppliesConnectionBounds pins the F8 fix: the two
// http.Server values cmd/prism actually serves (the endpoint demux server and
// the optional admin listener) must carry the listener-level hygiene bounds
// exported by internal/api.
//
// Before the fix both were bare &http.Server{Handler: ...}, so a client could
// hold a connection open forever with a half-sent request header (slowloris).
// WriteTimeout must stay unset: the SSE endpoint is a long-lived stream and a
// write deadline would cut every progress stream short.
func TestListenerHTTPServerAppliesConnectionBounds(t *testing.T) {
	port := reserveTestPorts(t, 1)[0]
	manager := newEndpointRuntimeManager("127.0.0.1", "", nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	if err := manager.ApplyEndpoint(model.Endpoint{ID: "bounds", Port: port, Enabled: true}); err != nil {
		t.Fatalf("ApplyEndpoint: %v", err)
	}
	runtime := manager.runtimes["bounds"]
	if runtime == nil || runtime.server == nil {
		t.Fatal("endpoint runtime was not registered")
	}
	assertServerConnectionBounds(t, "endpoint listener", runtime.server.httpServer)

	admin, err := startAdminListener("127.0.0.1:0", http.NotFoundHandler())
	if err != nil {
		t.Fatalf("startAdminListener: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = admin.Shutdown(ctx)
	})
	assertServerConnectionBounds(t, "admin listener", admin.server)

	// The nil-httpServer fallback of the demux constructor is a third place a
	// served server is built; it must carry the same bounds.
	assertServerConnectionBounds(t, "demux nil fallback", newInboundDemuxServer(nil, nil).httpServer)
}

// TestAdminListenerServesAndShutsDownGracefully guards the other half of the F8
// change: building the admin listener's http.Server through api.NewListenerServer
// must not disturb its Serve/Shutdown path. A real request is served, then
// Shutdown must return promptly rather than waiting out the idle timeout.
func TestAdminListenerServesAndShutsDownGracefully(t *testing.T) {
	admin, err := startAdminListener("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	if err != nil {
		t.Fatalf("startAdminListener: %v", err)
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + admin.listener.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("StatusCode: got %d, want %d", resp.StatusCode, http.StatusTeapot)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	if err := admin.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Shutdown took %v; a graceful stop must not wait out IdleTimeout (%v)", elapsed, api.IdleTimeout)
	}
	if err := admin.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown must be idempotent: %v", err)
	}
}

func assertServerConnectionBounds(t *testing.T, name string, srv *http.Server) {
	t.Helper()
	if srv == nil {
		t.Fatalf("%s: nil http.Server", name)
	}
	if srv.ReadHeaderTimeout != api.ReadHeaderTimeout {
		t.Errorf("%s: ReadHeaderTimeout = %v, want %v", name, srv.ReadHeaderTimeout, api.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != api.IdleTimeout {
		t.Errorf("%s: IdleTimeout = %v, want %v", name, srv.IdleTimeout, api.IdleTimeout)
	}
	if srv.MaxHeaderBytes != api.MaxHeaderBytes {
		t.Errorf("%s: MaxHeaderBytes = %d, want %d", name, srv.MaxHeaderBytes, api.MaxHeaderBytes)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("%s: WriteTimeout = %v, want 0 (a deadline would cut the SSE stream)", name, srv.WriteTimeout)
	}
	if srv.ReadTimeout != 0 {
		t.Errorf("%s: ReadTimeout = %v, want 0 (a deadline would cut request bodies and SSE)", name, srv.ReadTimeout)
	}
}

// shortHeaderTimeout is ReadHeaderTimeout shrunk for tests that have to wait for
// the header phase to expire. The value is set on a server built by the real
// constructor, so the code path under test is unchanged; only the duration is.
const shortHeaderTimeout = 300 * time.Millisecond

// newListenerServerForTest builds the server through the production constructor
// and then shrinks the header bound so a test can wait for it to expire.
//
// The constructor's value must be non-zero before shrinking: the demux clears
// its sniff deadline, so a zero ReadHeaderTimeout leaves the header phase
// unbounded, which is exactly the hole these tests exist to catch.
func newListenerServerForTest(t *testing.T, handler http.Handler) *http.Server {
	t.Helper()
	srv := api.NewListenerServer(handler)
	if srv.ReadHeaderTimeout <= 0 {
		t.Fatalf("api.NewListenerServer left ReadHeaderTimeout = %v; the demux clears its sniff deadline, so the header phase would be unbounded",
			srv.ReadHeaderTimeout)
	}
	srv.ReadHeaderTimeout = shortHeaderTimeout
	return srv
}

// startDemuxForTest serves handler on a loopback listener through the real
// inboundDemuxServer + api.NewListenerServer pair.
func startDemuxForTest(t *testing.T, httpServer *http.Server, socks inboundConnHandler) (*inboundDemuxServer, net.Addr) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	demux := newInboundDemuxServer(httpServer, socks)
	go func() { _ = demux.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = demux.Shutdown(ctx)
	})
	return demux, ln.Addr()
}

// TestInboundDemuxStalledHeaderConnectionIsClosed is the end-to-end proof for
// the F8 slowloris fix.
//
// inbound_demux.go clears the sniff read deadline (SetReadDeadline(time.Time{}))
// before handing the connection to http.Server.Serve, so the 15s sniff timeout
// cannot cover the header phase. With ReadHeaderTimeout set, net/http re-arms
// the deadline itself in conn.serve, so a client that sends one byte and then
// stalls is dropped instead of holding the connection forever.
func TestInboundDemuxStalledHeaderConnectionIsClosed(t *testing.T) {
	httpServer := newListenerServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler ran for a request whose header was never completed")
	}))
	_, addr := startDemuxForTest(t, httpServer, &stubSocksHandler{})

	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// One byte of a request line, then silence: the exact slowloris shape.
	if _, err := conn.Write([]byte("G")); err != nil {
		t.Fatalf("write first byte: %v", err)
	}

	// The server must terminate the connection once the header phase expires:
	// net/http answers 400 Bad Request and closes. Before the fix nothing bounded
	// this phase (the sniff deadline is cleared and no ReadHeaderTimeout was set),
	// so the read below would sit until the client-side deadline.
	_ = conn.SetReadDeadline(time.Now().Add(10 * shortHeaderTimeout))
	start := time.Now()
	reply, err := io.ReadAll(conn)
	elapsed := time.Since(start)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("connection was still open %v after one byte and no more (ReadHeaderTimeout was not applied)",
			10*shortHeaderTimeout)
	}
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if !strings.Contains(string(reply), "400 Bad Request") {
		t.Fatalf("reply = %q, want a 400 Bad Request from the expired header phase", reply)
	}
	if elapsed > 5*shortHeaderTimeout {
		t.Fatalf("the header phase took %v to expire, want about %v", elapsed, shortHeaderTimeout)
	}
}

// TestInboundDemuxSocks5PathIgnoresHTTPHeaderBounds pins that applying the HTTP
// bounds does not touch the SOCKS5 branch: the demux dispatches on the first
// byte before any connection reaches http.Server, so a SOCKS5 greeting must
// still be answered by the SOCKS handler even while ReadHeaderTimeout is tiny.
func TestInboundDemuxSocks5PathIgnoresHTTPHeaderBounds(t *testing.T) {
	httpServer := newListenerServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("HTTP handler ran for a SOCKS5 connection")
	}))

	socks := &stubSocksHandler{firstByteCh: make(chan byte, 1)}
	_, addr := startDemuxForTest(t, httpServer, socks)

	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write socks greeting: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read socks reply: %v", err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("reply: got %v, want [5 0]", reply)
	}

	select {
	case b := <-socks.firstByteCh:
		if b != 0x05 {
			t.Fatalf("first byte: got %d, want 5", b)
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS5 handler was not called; the HTTP header bound leaked into the SOCKS path")
	}
}

// TestInboundDemuxStreamingResponseOutlivesHeaderTimeout pins the reason
// WriteTimeout stays unset: the SSE endpoint streams for the whole life of a
// job, so a response must be able to keep writing long after the header phase
// deadline has passed. A WriteTimeout (or ReadTimeout) would abort the second
// write and the client would see a truncated stream.
func TestInboundDemuxStreamingResponseOutlivesHeaderTimeout(t *testing.T) {
	httpServer := newListenerServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter is not a Flusher; the SSE endpoint would be unusable")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: first\n\n")
		flusher.Flush()

		// Outlive the header-phase deadline by a wide margin: this is the
		// keep-alive gap of a long-lived SSE stream.
		time.Sleep(4 * shortHeaderTimeout)

		_, _ = io.WriteString(w, "data: second\n\n")
		flusher.Flush()
	}))
	_, addr := startDemuxForTest(t, httpServer, &stubSocksHandler{})

	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := io.WriteString(conn, "GET /api/v1/intel/jobs/x/events HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode: got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	_ = conn.SetReadDeadline(time.Now().Add(10 * shortHeaderTimeout))
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream body: %v (the stream was cut before it finished)", err)
	}
	if !strings.Contains(string(body), "data: first") || !strings.Contains(string(body), "data: second") {
		t.Fatalf("stream body = %q, want both frames", body)
	}
}

// TestNoBareHTTPServerInProductionSources is the recurrence guard for F8.
//
// The bug was not the missing value, it was that the value lived on a server
// nobody served while the two served ones were built as bare
// &http.Server{Handler: ...}. Reading the tree is the only way to catch the next
// listener added that way, because a zero ReadHeaderTimeout looks exactly like a
// deliberately unbounded one at runtime.
func TestNoBareHTTPServerInProductionSources(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(source), "http.Server{") {
			continue
		}
		t.Errorf("%s constructs an http.Server directly; build it with api.NewListenerServer so the connection bounds cannot be omitted", name)
	}
}
