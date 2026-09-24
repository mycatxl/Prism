package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"prism/internal/api"
	"prism/internal/proxy"
)

// Tests for the proxy-entry authentication failure limiter (WP04 §4.6) on the
// entry points assembled in cmd/prism: the reverse-proxy path token decided by
// the inbound mux and the SOCKS5 username/password check. They are local-only:
// loopback sockets and httptest, no outbound traffic.

// testProxyAuthToken is the credential of the stub entries. It is never printed
// in a failure message.
const testProxyAuthToken = "proxy-auth-test-token-0001"

// wrongProxyAuthSecret is deliberately not a credential of any kind: it is what
// a brute-forcer would send.
const wrongProxyAuthSecret = "not-the-token"

// countingAuthGuard records every failure so a test can assert how many
// credential checks were metered, without a credential ever reaching a message.
type countingAuthGuard struct {
	mu       sync.Mutex
	failures int
}

func (g *countingAuthGuard) Blocked(string) (bool, time.Duration) { return false, 0 }

func (g *countingAuthGuard) RecordFailure(string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures++
}

func (g *countingAuthGuard) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.failures
}

func TestInboundMuxReversePathTokenFailuresAreMetered(t *testing.T) {
	const failuresBeforeBlock = 3
	limiter := api.NewAuthFailureLimiter(failuresBeforeBlock, time.Minute, time.Minute, nil)
	reverseCalls := 0
	reverse := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reverseCalls++
		w.WriteHeader(http.StatusOK)
	})
	mux := newInboundMuxWithGuard(
		testProxyAuthToken,
		tagHandler("forward", http.StatusOK),
		reverse,
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
		limiter,
	)

	for attempt := 1; attempt <= failuresBeforeBlock; attempt++ {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wrong-token/Default/https/example.com/x", nil))
		assertCompatErrorHeader(t, rec, http.StatusForbidden, "AUTH_FAILED")
	}
	if blocked, _ := limiter.Blocked("192.0.2.1"); !blocked {
		t.Fatal("the path-token failures were not counted by the limiter")
	}

	// The next failing attempt is answered 429 by the limiter, and never
	// reaches the reverse proxy.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wrong-token/Default/https/example.com/x", nil))
	assertCompatErrorHeader(t, rec, http.StatusTooManyRequests, "RATE_LIMITED")
	if rec.Header().Get("Retry-After") == "" {
		t.Error("the 429 must carry Retry-After")
	}
	if reverseCalls != 0 {
		t.Fatalf("the reverse handler served %d requests, want 0", reverseCalls)
	}
}

func TestInboundMuxSuccessfulPathTokenIsNotCounted(t *testing.T) {
	limiter := api.NewAuthFailureLimiter(2, time.Minute, time.Minute, nil)
	reverse := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux := newInboundMuxWithGuard(
		testProxyAuthToken,
		tagHandler("forward", http.StatusOK),
		reverse,
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
		limiter,
	)

	for attempt := 0; attempt < 5; attempt++ {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(
			http.MethodGet,
			"/"+testProxyAuthToken+"/Default/https/example.com/x",
			nil,
		))
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d: status = %d, want 200", attempt, rec.Code)
		}
	}
	if blocked, _ := limiter.Blocked("192.0.2.1"); blocked {
		t.Fatal("a correct path token must never count as an authentication failure")
	}
}

func TestInboundMuxPathTokenIsNotMeteredWithoutALimiter(t *testing.T) {
	// PRISM_PROXY_AUTH_FAIL_LIMIT=0 (the documented default) must keep the
	// upstream behaviour: a wrong token is 403 every time, never 429.
	mux := newInboundMux(
		testProxyAuthToken,
		tagHandler("forward", http.StatusOK),
		tagHandler("reverse", http.StatusOK),
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
	)
	for attempt := 0; attempt < 5; attempt++ {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wrong-token/Default/https/example.com/x", nil))
		assertCompatErrorHeader(t, rec, http.StatusForbidden, "AUTH_FAILED")
	}
}

func TestSocks5AuthObserverCountsOnlyRejections(t *testing.T) {
	cases := []struct {
		name     string
		replies  [][2]byte
		failures int
	}{
		{name: "userpass rejected", replies: [][2]byte{{0x05, 0x02}, {0x01, 0x01}}, failures: 1},
		{name: "userpass accepted", replies: [][2]byte{{0x05, 0x02}, {0x01, 0x00}}, failures: 0},
		{name: "no acceptable method", replies: [][2]byte{{0x05, 0xFF}}, failures: 1},
		{name: "no auth selected", replies: [][2]byte{{0x05, 0x00}}, failures: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guard := &countingAuthGuard{}
			observer := &socks5AuthObserver{Conn: &noopConn{}, clientIP: "127.0.0.1", guard: guard}
			for _, reply := range tc.replies {
				if _, err := observer.Write(reply[:]); err != nil {
					t.Fatalf("Write: %v", err)
				}
			}
			if got := guard.count(); got != tc.failures {
				t.Fatalf("metered %d failures, want %d", got, tc.failures)
			}
		})
	}
}

// TestSocks5EntryMetersWrongCredentialsAndBlocks drives the real SOCKS5 inbound
// over loopback: every rejected username/password authentication must reach the
// limiter, and once the client is blocked the entry refuses the session before
// any credential is examined.
func TestSocks5EntryMetersWrongCredentialsAndBlocks(t *testing.T) {
	const failuresBeforeBlock = 3
	limiter := api.NewAuthFailureLimiter(failuresBeforeBlock, time.Minute, time.Minute, nil)
	inbound := proxy.NewSocks5Inbound(proxy.Socks5InboundConfig{ProxyToken: testProxyAuthToken})
	handler := newSocks5AuthGuard(limiter, inbound)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()
	var wg sync.WaitGroup
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				handler.ServeConnContext(context.Background(), conn)
			}()
		}
	}()
	defer wg.Wait()

	for attempt := 1; attempt <= failuresBeforeBlock; attempt++ {
		reader := socks5WrongCredentialAttempt(t, listener)
		reply := make([]byte, 2)
		if _, err := io.ReadFull(reader, reply); err != nil {
			t.Fatalf("attempt %d: reading the authentication reply: %v", attempt, err)
		}
		if reply[0] != 0x01 || reply[1] != 0x01 {
			t.Fatalf("attempt %d: authentication reply = % x, want the RFC 1929 failure", attempt, reply)
		}
	}

	clientIP := "127.0.0.1"
	if blocked, _ := limiter.Blocked(clientIP); !blocked {
		t.Fatal("the SOCKS5 authentication failures were not counted by the limiter")
	}

	// A blocked client is refused with "no acceptable methods" before any
	// handshake, and the server closes the session.
	blockedConn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial blocked attempt: %v", err)
	}
	defer func() { _ = blockedConn.Close() }()
	if err := blockedConn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	refusal := make([]byte, 2)
	if _, err := io.ReadFull(blockedConn, refusal); err != nil {
		t.Fatalf("blocked attempt: reading the refusal: %v", err)
	}
	if refusal[0] != 0x05 || refusal[1] != 0xFF {
		t.Fatalf("blocked attempt: refusal = % x, want 05 ff", refusal)
	}
	if _, err := blockedConn.Read(make([]byte, 1)); err == nil {
		t.Fatal("the blocked session must be closed after the refusal")
	}
}

// TestSocks5EntryMetersNothingWithoutALimiter keeps the documented default: a
// nil guard leaves the SOCKS5 inbound exactly as it was.
func TestSocks5EntryMetersNothingWithoutALimiter(t *testing.T) {
	inbound := proxy.NewSocks5Inbound(proxy.Socks5InboundConfig{ProxyToken: testProxyAuthToken})
	if guard := newSocks5AuthGuard(nil, inbound); guard != inbound {
		t.Fatal("a nil guard must leave the SOCKS5 inbound untouched")
	}
}

// socks5WrongCredentialAttempt completes the method negotiation and sends a
// username/password pair that cannot match the configured token. It returns the
// reader of the session.
func socks5WrongCredentialAttempt(t *testing.T, listener net.Listener) *bufio.Reader {
	t.Helper()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	reader := bufio.NewReader(conn)
	if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(reader, method); err != nil {
		t.Fatalf("reading the method selection: %v", err)
	}
	if method[0] != 0x05 || method[1] != 0x02 {
		t.Fatalf("method selection = % x, want username/password", method)
	}
	request := []byte{0x01, 0x04, 'u', 's', 'e', 'r', byte(len(wrongProxyAuthSecret))}
	request = append(request, wrongProxyAuthSecret...)
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("credentials: %v", err)
	}
	return reader
}

// noopConn is a net.Conn that discards writes, for the observer unit test.
type noopConn struct{}

func (noopConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (noopConn) Write(p []byte) (int, error)      { return len(p), nil }
func (noopConn) Close() error                     { return nil }
func (noopConn) LocalAddr() net.Addr              { return noopAddr("local") }
func (noopConn) RemoteAddr() net.Addr             { return noopAddr("remote") }
func (noopConn) SetDeadline(time.Time) error      { return nil }
func (noopConn) SetReadDeadline(time.Time) error  { return nil }
func (noopConn) SetWriteDeadline(time.Time) error { return nil }

type noopAddr string

func (a noopAddr) Network() string { return "noop" }
func (a noopAddr) String() string  { return string(a) }

// TestSocks5AuthObserverIgnoresNonHandshakeBytes makes sure the observer stops
// interpreting bytes as soon as the exchange is not the SOCKS5 handshake, so a
// tunnel payload can never be mistaken for a rejection.
func TestSocks5AuthObserverIgnoresNonHandshakeBytes(t *testing.T) {
	guard := &countingAuthGuard{}
	observer := &socks5AuthObserver{Conn: &noopConn{}, clientIP: "127.0.0.1", guard: guard}
	if _, err := observer.Write([]byte{0x47, 0x45, 0x54}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := observer.Write([]byte{0x01, 0x01, 0x01, 0x01}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := guard.count(); got != 0 {
		t.Fatalf("metered %d failures for non-handshake bytes, want 0", got)
	}
}
