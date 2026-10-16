package proxy

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"prism/internal/usage"
)

// refusalLogRecorder captures what the proxies reported when they refused a
// request, so a test can assert an operator can see the refusal at all.
type refusalLogRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (l *refusalLogRecorder) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *refusalLogRecorder) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// assertRefusalLogged checks the one line a refusal must leave: it names the key
// and the limit that was hit, and it names nothing about the person behind it.
func assertRefusalLogged(t *testing.T, rec *refusalLogRecorder, wantReason string) {
	t.Helper()

	lines := rec.snapshot()
	if len(lines) != 1 {
		t.Fatalf("the refusal was reported %d time(s) (%v), want exactly 1", len(lines), lines)
	}
	line := lines[0]
	if !strings.Contains(line, "k-meter") {
		t.Fatalf("the refusal line %q does not name the key that hit the limit", line)
	}
	if !strings.Contains(line, wantReason) {
		t.Fatalf("the refusal line %q does not say which limit was hit (%q)", line, wantReason)
	}
	// The client-facing error text stays general on purpose; the server-side
	// line must be just as free of anything that identifies the account.
	if strings.Contains(line, testCredential) {
		t.Fatalf("the refusal line leaked the credential: %q", line)
	}
	if strings.Contains(line, "u-meter") {
		t.Fatalf("the refusal line leaked the user id: %q", line)
	}
}

// A refused request used to leave no trace at all: the four data-plane checks
// return before the request lifecycle exists, so nothing reaches the request log
// and nothing reaches the server log either, even though the gate knows exactly
// which limit was hit (Decision.Reason) and the sink carries it out.
func TestQuotaRefusal_IsReportedWithItsReasonOnEveryInbound(t *testing.T) {
	const reason = "traffic"

	t.Run("forward http request", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}))
		defer upstream.Close()

		env := newProxyE2EEnv(t)
		setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, strings.TrimPrefix(upstream.URL, "http://"))
		})

		logs := &refusalLogRecorder{}
		fp := NewForwardProxy(ForwardProxyConfig{
			ProxyToken: "tok",
			AccessKeys: &mutableKeyStore{state: workingKeyState()},
			Router:     env.router,
			Pool:       env.pool,
			Health:     &mockHealthRecorder{},
			Events:     newMockEventEmitter(),
			Quota:      &gateStub{refused: true},
			QuotaLogf:  logs.logf,
		})

		req := httptest.NewRequest(http.MethodGet, upstream.URL+"/refused", nil)
		req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
		w := httptest.NewRecorder()
		fp.ServeHTTP(w, req)

		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429 (body %q)", w.Code, w.Body.String())
		}
		assertRefusalLogged(t, logs, reason)
	})

	t.Run("forward connect tunnel", func(t *testing.T) {
		env := newProxyE2EEnv(t)
		logs := &refusalLogRecorder{}
		fp := NewForwardProxy(ForwardProxyConfig{
			ProxyToken: "tok",
			AccessKeys: &mutableKeyStore{state: workingKeyState()},
			Router:     env.router,
			Pool:       env.pool,
			Health:     &mockHealthRecorder{},
			Events:     newMockEventEmitter(),
			Quota:      &gateStub{refused: true},
			QuotaLogf:  logs.logf,
		})
		server := httptest.NewServer(fp)
		defer server.Close()

		conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			t.Fatalf("dial proxy: %v", err)
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatalf("SetDeadline: %v", err)
		}
		target := "127.0.0.1:443"
		req := fmt.Sprintf(
			"CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n",
			target, target, basicAuth("plat", testCredential),
		)
		if _, err := conn.Write([]byte(req)); err != nil {
			t.Fatalf("write CONNECT: %v", err)
		}
		statusLine, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			t.Fatalf("read CONNECT status: %v", err)
		}
		if !strings.Contains(statusLine, "429") {
			t.Fatalf("CONNECT answered %q, want 429", statusLine)
		}
		assertRefusalLogged(t, logs, reason)
	})

	t.Run("reverse request", func(t *testing.T) {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}))
		defer backend.Close()

		env := newProxyE2EEnv(t)
		setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, strings.TrimPrefix(backend.URL, "http://"))
		})

		logs := &refusalLogRecorder{}
		rp := NewReverseProxy(ReverseProxyConfig{
			ProxyToken:     "tok",
			AccessKeys:     &mutableKeyStore{state: workingKeyState()},
			Router:         env.router,
			Pool:           env.pool,
			PlatformLookup: env.pool,
			Health:         &mockHealthRecorder{},
			Events:         newMockEventEmitter(),
			Quota:          &gateStub{refused: true},
			QuotaLogf:      logs.logf,
		})

		host := strings.TrimPrefix(backend.URL, "http://")
		path := fmt.Sprintf("/%s/plat:acct/http/%s/refused", testCredential, host)
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		rp.ServeHTTP(w, req)

		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429 (body %q)", w.Code, w.Body.String())
		}
		assertRefusalLogged(t, logs, reason)
	})

	t.Run("socks5 session", func(t *testing.T) {
		env := newProxyE2EEnv(t)
		logs := &refusalLogRecorder{}
		inbound := NewSocks5Inbound(Socks5InboundConfig{
			ProxyToken: "tok",
			AccessKeys: &mutableKeyStore{state: workingKeyState()},
			Router:     env.router,
			Pool:       env.pool,
			Health:     &mockHealthRecorder{},
			Events:     newMockEventEmitter(),
			Metering:   &usage.Sink{Counters: usage.NewCounters()},
			Quota:      &gateStub{refused: true},
			QuotaLogf:  logs.logf,
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

		assertRefusalLogged(t, logs, reason)
	})
}

// An admitted request reports nothing: the line exists for refusals only.
func TestQuotaRefusal_AdmittedRequestReportsNothing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, strings.TrimPrefix(upstream.URL, "http://"))
	})

	logs := &refusalLogRecorder{}
	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		AccessKeys: &mutableKeyStore{state: workingKeyState()},
		Router:     env.router,
		Pool:       env.pool,
		Health:     &mockHealthRecorder{},
		Events:     newMockEventEmitter(),
		Quota:      &gateStub{},
		QuotaLogf:  logs.logf,
	})

	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/admitted", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
	w := httptest.NewRecorder()
	fp.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if lines := logs.snapshot(); len(lines) != 0 {
		t.Fatalf("an admitted request was reported as a refusal: %v", lines)
	}
}

// The refusal line is built without a lifecycle and without a key being
// guaranteed: a gate refusal for a request whose key could not be resolved must
// still be reportable, and a deployment without a log function must not panic.
func TestRefusalProxyError_HandlesMissingKeyAndMissingLogf(t *testing.T) {
	logs := &refusalLogRecorder{}
	err := refusalProxyError("concurrency", nil, logs.logf)
	if err != ErrQuotaExceeded {
		t.Fatalf("error = %v, want ErrQuotaExceeded", err)
	}
	lines := logs.snapshot()
	if len(lines) != 1 || !strings.Contains(lines[0], "concurrency") {
		t.Fatalf("the refusal line = %v, want one line naming the reason", lines)
	}

	if err := refusalProxyError("traffic", workingKeyState(), nil); err != ErrQuotaExceeded {
		t.Fatalf("error = %v, want ErrQuotaExceeded", err)
	}
}
