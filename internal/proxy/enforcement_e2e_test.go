package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/keys"
	"prism/internal/model"
	"prism/internal/usage"
)

// mutableKeyStore hands out a key state that a test can change while traffic is
// flowing, which is how a disable is modelled: the operator flips the row, and
// the running sessions must notice.
type mutableKeyStore struct {
	mu    sync.Mutex
	state *keys.KeyState
}

func (s *mutableKeyStore) VerifyCredential(credential string) *keys.KeyState {
	if credential != testCredential {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *mutableKeyStore) lookup(string) *keys.KeyState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *mutableKeyStore) disable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Status = model.KeyStatusDisabled
}

// newMeteredForwardProxy wires the accounting the way the process does: one
// counter set, one registry, one limiter, and the adapters between them and the
// proxy.
func newMeteredForwardProxy(
	t *testing.T,
	env *proxyE2EEnv,
	store *mutableKeyStore,
) (*ForwardProxy, *usage.Counters, *usage.ConnRegistry) {
	t.Helper()

	counters := usage.NewCounters()
	registry := usage.NewConnRegistry()
	limiter := usage.NewLimiter(counters)

	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		AccessKeys: store,
		Router:     env.router,
		Pool:       env.pool,
		Health:     &mockHealthRecorder{},
		Events:     newMockEventEmitter(),
		Metering:   &usage.Sink{Counters: counters},
		Quota:      &usage.Gate{Limiter: limiter},
		Sessions:   registry,
	})
	return fp, counters, registry
}

// openTunnel dials the proxy and runs a CONNECT to targetAddr, returning the
// client side of the tunnel once the proxy has accepted it.
func openTunnel(t *testing.T, fp *ForwardProxy, targetAddr string) (net.Conn, *bufio.Reader) {
	t.Helper()

	proxySrv := httptest.NewServer(fp)
	t.Cleanup(proxySrv.Close)

	clientConn, err := net.Dial("tcp", strings.TrimPrefix(proxySrv.URL, "http://"))
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	req := fmt.Sprintf(
		"CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n",
		targetAddr, targetAddr, basicAuth("plat", testCredential),
	)
	if _, err := clientConn.Write([]byte(req)); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}

	reader := bufio.NewReader(clientConn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read CONNECT status: %v", err)
	}
	if !strings.Contains(statusLine, "200 Connection Established") {
		t.Fatalf("CONNECT was refused: %q", statusLine)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read CONNECT headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	return clientConn, reader
}

// echoTarget starts a TCP echo server and returns its address.
func echoTarget(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 4096)
				for {
					n, err := conn.Read(buf)
					if n > 0 {
						if _, werr := conn.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// waitForTunnelClosure reads from the tunnel until it fails, and reports how
// long that took. A tunnel that is still alive when the deadline passes makes
// the test fail rather than hang.
func waitForTunnelClosure(t *testing.T, conn net.Conn, deadline time.Duration) time.Duration {
	t.Helper()

	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 256)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()

	select {
	case <-done:
		return time.Since(start)
	case <-time.After(deadline):
		t.Fatalf("the tunnel was still open after %s", deadline)
		return 0
	}
}

// The acceptance requirement: a disabled key's live tunnel is closed within a
// second, not merely refused on its next request.
func TestEnforcement_DisabledKeyClosesItsLiveTunnelWithinASecond(t *testing.T) {
	env := newProxyE2EEnv(t)
	store := &mutableKeyStore{state: workingKeyState()}
	fp, _, registry := newMeteredForwardProxy(t, env, store)

	enforcer := &usage.Enforcer{
		Registry: registry,
		Limiter:  usage.NewLimiter(usage.NewCounters()),
		Lookup:   store.lookup,
		Interval: 50 * time.Millisecond,
	}

	conn, reader := openTunnel(t, fp, echoTarget(t))

	// Prove the tunnel works and is registered before disabling anything.
	if _, err := conn.Write([]byte("before-disable")); err != nil {
		t.Fatalf("write through the tunnel: %v", err)
	}
	echo := make([]byte, len("before-disable"))
	if _, err := io.ReadFull(reader, echo); err != nil {
		t.Fatalf("read the echo: %v", err)
	}
	if got := string(echo); got != "before-disable" {
		t.Fatalf("echo = %q", got)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && registry.Count("k-meter") == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := registry.Count("k-meter"); got != 1 {
		t.Fatalf("the tunnel was not registered (%d sessions), so it could never be revoked", got)
	}

	// The operator disables the key. Nothing else changes.
	store.disable()

	// The enforcer's own loop must notice, so this exercises the real interval
	// rather than a hand-called sweep.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		enforcer.Start(ctx)
	}()

	elapsed := waitForTunnelClosure(t, conn, 2*time.Second)
	cancel()
	<-sweepDone

	if elapsed > time.Second {
		t.Fatalf("the tunnel took %s to close after the key was disabled, want under a second", elapsed)
	}
	t.Logf("the tunnel closed %s after the key was disabled", elapsed)
}

// The other half of the same requirement: a key that has used up its allowance
// has its live tunnel cut, with the grace applied so a transfer that crossed the
// limit is not cut off with bytes still in flight.
func TestEnforcement_OverQuotaClosesItsLiveTunnel(t *testing.T) {
	env := newProxyE2EEnv(t)
	key := workingKeyState()
	key.Plan.TrafficBytes = 64
	store := &mutableKeyStore{state: key}
	fp, counters, registry := newMeteredForwardProxy(t, env, store)

	enforcer := &usage.Enforcer{
		Registry:     registry,
		Limiter:      usage.NewLimiter(counters),
		Lookup:       store.lookup,
		Interval:     50 * time.Millisecond,
		GracePercent: 10,
	}

	conn, _ := openTunnel(t, fp, echoTarget(t))

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && registry.Count("k-meter") == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := registry.Count("k-meter"); got != 1 {
		t.Fatalf("the tunnel was not registered (%d sessions)", got)
	}

	// Push the key past its allowance plus the grace.
	counters.Add(usage.CounterKey{KeyID: "k-meter", UserID: "u-meter", PlatformID: "p-1"},
		usage.Delta{IngressBytes: 64 + 64})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		enforcer.Start(ctx)
	}()

	elapsed := waitForTunnelClosure(t, conn, 2*time.Second)
	cancel()
	<-sweepDone

	if elapsed > time.Second {
		t.Fatalf("the tunnel took %s to close after the allowance ran out, want under a second", elapsed)
	}
	t.Logf("the tunnel closed %s after the allowance ran out", elapsed)
}

// A usable key's tunnel must survive the sweeps: a revocation mechanism that
// disconnects healthy users is worse than none.
func TestEnforcement_UsableKeyKeepsItsTunnel(t *testing.T) {
	env := newProxyE2EEnv(t)
	store := &mutableKeyStore{state: workingKeyState()}
	fp, counters, registry := newMeteredForwardProxy(t, env, store)

	enforcer := &usage.Enforcer{
		Registry: registry,
		Limiter:  usage.NewLimiter(counters),
		Lookup:   store.lookup,
		Interval: 20 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go enforcer.Start(ctx)
	defer cancel()

	conn, reader := openTunnel(t, fp, echoTarget(t))

	// Let several sweeps run.
	time.Sleep(200 * time.Millisecond)

	if _, err := conn.Write([]byte("still-alive")); err != nil {
		t.Fatalf("write after the sweeps: %v", err)
	}
	echo := make([]byte, len("still-alive"))
	if _, err := io.ReadFull(reader, echo); err != nil {
		t.Fatalf("a healthy key's tunnel was closed: %v", err)
	}
	if got := string(echo); got != "still-alive" {
		t.Fatalf("echo = %q", got)
	}
}
