package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	"prism/internal/usage"
)

type auditBlockedWriter struct {
	conn    net.Conn
	header  http.Header
	writing chan struct{}
	once    sync.Once
}

func (w *auditBlockedWriter) Header() http.Header { return w.header }
func (w *auditBlockedWriter) WriteHeader(int)     {}
func (w *auditBlockedWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.writing) })
	return w.conn.Write(p)
}
func (w *auditBlockedWriter) SetWriteDeadline(d time.Time) error { return w.conn.SetWriteDeadline(d) }
func (w *auditBlockedWriter) SetReadDeadline(d time.Time) error  { return w.conn.SetReadDeadline(d) }
func (w *auditBlockedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn := &auditWriteSignalConn{Conn: w.conn, writing: w.writing, once: &w.once}
	return conn, bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn)), nil
}

type auditWriteSignalConn struct {
	net.Conn
	writing chan struct{}
	once    *sync.Once
}

func (c *auditWriteSignalConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.writing) })
	return c.Conn.Write(p)
}

func TestSessionCancellationInterruptsBlockedDownstream(t *testing.T) {
	for _, kind := range []string{"forward", "reverse", "reverse-upgrade"} {
		t.Run(kind, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "reverse-upgrade" {
					conn, rw, err := http.NewResponseController(w).Hijack()
					if err != nil {
						return
					}
					defer conn.Close()
					_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
					_ = rw.Flush()
					_, _ = io.Copy(io.Discard, conn)
					return
				}
				_, _ = io.WriteString(w, "payload")
			}))
			defer upstream.Close()
			env := newProxyE2EEnv(t)
			setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", upstream.Listener.Addr().String())
			})
			key := workingKeyState()
			key.KeyID = "blocked-client"
			key.Plan.TrafficBytes = 1 << 20
			counters := usage.NewCounters()
			registry := usage.NewConnRegistry()
			var handler http.Handler
			var req *http.Request
			if kind == "forward" {
				fp, _, _, sessions := newQuotaForwardProxy(t, env, &mutableKeyStore{state: key})
				handler = fp
				registry = sessions
				req = httptest.NewRequest(http.MethodGet, upstream.URL, nil)
				req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
			} else {
				handler = newUpgradeQuotaReverseProxy(t, env, key, counters, registry, newMockEventEmitter())
				req = httptest.NewRequest(http.MethodGet, "http://proxy/"+testCredential+"/plat:account/http/example.com/", nil)
				if kind == "reverse-upgrade" {
					req.Header.Set("Connection", "Upgrade")
					req.Header.Set("Upgrade", "websocket")
				}
			}
			conn, peer := net.Pipe()
			defer conn.Close()
			defer peer.Close()
			writer := &auditBlockedWriter{conn: conn, header: make(http.Header), writing: make(chan struct{})}
			done := make(chan any, 1)
			go func() { defer func() { done <- recover() }(); handler.ServeHTTP(writer, req) }()
			select {
			case <-writer.writing:
			case failure := <-done:
				t.Fatalf("handler exited before blocked write: %v", failure)
			case <-time.After(3 * time.Second):
				t.Fatal("no downstream write")
			}
			if registry.CloseKey(key.KeyID) != 1 {
				t.Fatal("missing active session")
			}
			select {
			case failure := <-done:
				if failure != nil && failure != http.ErrAbortHandler {
					t.Fatalf("handler panic: %v", failure)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled session retained blocked downstream write")
			}
		})
	}
}

func TestUpgradeCloseInterruptsBandwidthDebt(t *testing.T) {
	var stopped atomic.Bool
	base := &payloadConn{payload: strings.NewReader("long payload")}
	gate := &bandwidthGate{table: &bandwidthTable{}, name: "slow", rateBps: 1, stopped: stopped.Load}
	conn := newUpgradeStreamConn(base, nil, gate)
	done := make(chan struct{})
	go func() { _, _ = conn.Read(make([]byte, 32)); close(done) }()
	// Wait until the bytes have been admitted to the shared bandwidth budget.
	deadline := time.Now().Add(time.Second)
	for {
		gate.table.mu.Lock()
		started := len(gate.table.due) > 0
		gate.table.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("read never entered throttle")
		}
		time.Sleep(time.Millisecond)
	}
	_ = conn.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		stopped.Store(true)
		<-done
		t.Fatal("closed upgrade kept waiting for bandwidth debt")
	}
}

func TestRoutedTunnelDialHonorsConfiguredTimeout(t *testing.T) {
	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, (&blockingOutbound{}).DialContext)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	result := prepareConnectTunnel(ctx, tunnelDeps{router: env.router, pool: env.pool, dialTimeout: 30 * time.Millisecond}, "plat", "account", "example.com:443", workingKeyState())
	if !errors.Is(result.upstreamErr, context.DeadlineExceeded) {
		t.Fatalf("dial result=%+v", result)
	}
	if time.Since(start) > time.Second {
		t.Fatal("routed dial used only parent timeout")
	}
}

func TestReverseUpgradeAppliesBandwidthLimit(t *testing.T) {
	clock := installVirtualBandwidthClock(t)
	upstream := startUpgradeEchoUpstream(t)
	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", upstream.Addr().String())
	})
	key := workingKeyState()
	key.Plan.MaxBandwidthBps = 1
	rp := newUpgradeQuotaReverseProxy(t, env, key, usage.NewCounters(), usage.NewConnRegistry(), newMockEventEmitter())
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(done); rp.ServeHTTP(w, r) }))
	defer server.Close()
	client, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	_, err = fmt.Fprintf(client, "GET /%s/plat:acct/http/example.com/ws HTTP/1.1\r\nHost: proxy\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n", testCredential)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 101 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	_, err = client.Write([]byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 3)
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("upgrade handler retained closed client")
	}
	if slept, _ := clock.stats(); slept < 4*time.Second {
		t.Fatalf("upgrade bypassed bandwidth limit: waited %v", slept)
	}
}
