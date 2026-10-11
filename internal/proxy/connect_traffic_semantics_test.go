package proxy

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

// TestPumpPreparedTunnelReader_ZeroTrafficIsNotAFailure pins the semantics
// decision directly on the relay: a client that opens the tunnel and closes it
// without sending anything must not be reported as an upstream failure, because
// the node was never given a chance to misbehave.
func TestPumpPreparedTunnelReader_ZeroTrafficIsNotAFailure(t *testing.T) {
	clientConn, clientPeer := net.Pipe()
	defer clientPeer.Close()
	upstreamConn, upstreamPeer := net.Pipe()
	defer upstreamPeer.Close()

	resultCh := make(chan tunnelRelayResult, 1)
	go func() {
		resultCh <- pumpPreparedTunnelReader(
			clientConn,
			clientConn,
			&preparedTunnel{upstreamConn: upstreamConn, recordResult: func(bool) {}},
			tunnelPumpOptions{},
		)
	}()

	// The client connects, sends nothing, and leaves.
	_ = clientPeer.Close()

	select {
	case result := <-resultCh:
		if !result.netOK {
			t.Fatalf("zero-traffic tunnel netOK = false (stage %q, err %v), want true", result.upstreamStage, result.upstreamErr)
		}
		if result.proxyErr != nil {
			t.Fatalf("zero-traffic tunnel proxyErr = %v, want nil", result.proxyErr)
		}
		if result.upstreamStage != "" {
			t.Fatalf("zero-traffic tunnel stage = %q, want empty", result.upstreamStage)
		}
		if result.ingressBytes != 0 || result.egressBytes != 0 {
			t.Fatalf("zero-traffic bytes: ingress=%d egress=%d, want both 0", result.ingressBytes, result.egressBytes)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pumpPreparedTunnelReader did not return for a zero-traffic tunnel")
	}
}

// TestPumpPreparedTunnelReader_UpstreamWriteFailureStillFails is the other half
// of the rule: removing the zero-traffic verdict must not blunt the circuit
// breaker. A real write failure toward the upstream still fails the node.
func TestPumpPreparedTunnelReader_UpstreamWriteFailureStillFails(t *testing.T) {
	clientConn, clientPeer := net.Pipe()
	defer clientPeer.Close()
	upstreamBase, upstreamPeer := net.Pipe()
	defer upstreamPeer.Close()

	failingUpstream := &failOnWriteConn{Conn: upstreamBase, failAt: 1}

	resultCh := make(chan tunnelRelayResult, 1)
	go func() {
		resultCh <- pumpPreparedTunnelReader(
			clientConn,
			clientConn,
			&preparedTunnel{upstreamConn: failingUpstream, recordResult: func(bool) {}},
			tunnelPumpOptions{},
		)
	}()

	if _, err := clientPeer.Write([]byte("client-payload")); err != nil {
		t.Fatalf("client write: %v", err)
	}

	select {
	case result := <-resultCh:
		if result.netOK {
			t.Fatal("a failed client->upstream copy must still report netOK=false")
		}
		if result.proxyErr != ErrUpstreamRequestFailed {
			t.Fatalf("proxyErr = %v, want ErrUpstreamRequestFailed", result.proxyErr)
		}
		if result.upstreamStage != "connect_client_to_upstream_copy" {
			t.Fatalf("stage = %q, want connect_client_to_upstream_copy", result.upstreamStage)
		}
		if result.upstreamErr == nil {
			t.Fatal("the real write error must be preserved in the result")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pumpPreparedTunnelReader did not return after an upstream write failure")
	}
}

// TestForwardProxy_CONNECTUpstreamWriteFailureRecordsNodeFailure drives the
// same rule through the real CONNECT entry point and asserts the health
// recorder still sees the failure.
func TestForwardProxy_CONNECTUpstreamWriteFailureRecordsNodeFailure(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()
	health := &mockHealthRecorder{}

	upstreamBase, upstreamPeer := net.Pipe()
	defer upstreamPeer.Close()
	failingUpstream := &failOnWriteConn{Conn: upstreamBase, failAt: 1}

	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return failingUpstream, nil
	})

	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		Router:     env.router,
		Pool:       env.pool,
		Health:     health,
		Events:     emitter,
	})

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	w := &hijackTestResponseWriter{
		conn: serverConn,
		rw:   bufio.NewReadWriter(bufio.NewReader(serverConn), bufio.NewWriter(serverConn)),
	}

	req := httptest.NewRequest(http.MethodConnect, "http://example.com:443", nil)
	req.Host = "example.com:443"
	req.Header.Set("Proxy-Authorization", basicAuth("plat", "tok"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		fp.ServeHTTP(w, req)
	}()

	reader := bufio.NewReader(clientConn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read CONNECT status line: %v", err)
	}
	if !strings.Contains(statusLine, "200 Connection Established") {
		t.Fatalf("unexpected CONNECT status line: %q", statusLine)
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
	if _, err := clientConn.Write([]byte("client-payload")); err != nil {
		t.Fatalf("write tunnel payload: %v", err)
	}
	_ = clientConn.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("CONNECT handler did not return after the upstream write failure")
	}

	select {
	case logEv := <-emitter.logCh:
		if logEv.NetOK {
			t.Fatal("CONNECT upstream write failure must log net_ok=false")
		}
		if logEv.PrismError != ErrUpstreamRequestFailed.PrismError {
			t.Fatalf("PrismError: got %q, want %q", logEv.PrismError, ErrUpstreamRequestFailed.PrismError)
		}
		if logEv.UpstreamStage != "connect_client_to_upstream_copy" {
			t.Fatalf("UpstreamStage: got %q, want connect_client_to_upstream_copy", logEv.UpstreamStage)
		}
	case <-time.After(time.Second):
		t.Fatal("expected CONNECT log event")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if health.resultCalls.Load() > 0 {
			if health.lastSuccess.Load() != 0 {
				t.Fatalf("RecordResult lastSuccess: got %d, want 0 (a real write failure must penalise the node)", health.lastSuccess.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected RecordResult call for the failed CONNECT tunnel")
}
