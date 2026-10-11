package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	"prism/internal/node"
)

// dialThroughOutbound dials the requested address directly while honouring the
// caller context, so a transport built by the pool can be pointed at an
// httptest server.
type dialThroughOutbound struct {
	adapter.Outbound
}

func (o *dialThroughOutbound) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, dest.String())
}

func (o *dialThroughOutbound) Tag() string  { return "dial-through" }
func (o *dialThroughOutbound) Type() string { return "dial-through" }

// blockingOutbound never completes a dial: it waits for the context to end.
// It proves the dial phase carries a deadline of its own.
type blockingOutbound struct {
	adapter.Outbound
}

func (o *blockingOutbound) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (o *blockingOutbound) Tag() string  { return "blocking" }
func (o *blockingOutbound) Type() string { return "blocking" }

// TestNormalizeOutboundTransportConfig_DefaultsTimeouts pins the defaults that
// keep an unresponsive upstream from pinning a request forever.
func TestNormalizeOutboundTransportConfig_DefaultsTimeouts(t *testing.T) {
	cfg := normalizeOutboundTransportConfig(OutboundTransportConfig{})

	if cfg.ResponseHeaderTimeout <= 0 {
		t.Error("ResponseHeaderTimeout: got 0, want a positive default")
	}
	if cfg.TLSHandshakeTimeout <= 0 {
		t.Error("TLSHandshakeTimeout: got 0, want a positive default")
	}
	if cfg.DialTimeout <= 0 {
		t.Error("DialTimeout: got 0, want a positive default")
	}
}

// TestOutboundTransportPool_AppliesConfiguredTimeouts proves the configured
// values reach the reusable transport the proxies actually use.
func TestOutboundTransportPool_AppliesConfiguredTimeouts(t *testing.T) {
	pool := newOutboundTransportPoolWithConfig(OutboundTransportConfig{
		ResponseHeaderTimeout: 7 * time.Second,
		TLSHandshakeTimeout:   3 * time.Second,
		DialTimeout:           11 * time.Second,
	})
	transport := pool.Get(node.Hash{9}, &noopOutbound{}, nil)

	if transport.ResponseHeaderTimeout != 7*time.Second {
		t.Errorf("ResponseHeaderTimeout: got %s, want %s", transport.ResponseHeaderTimeout, 7*time.Second)
	}
	if transport.TLSHandshakeTimeout != 3*time.Second {
		t.Errorf("TLSHandshakeTimeout: got %s, want %s", transport.TLSHandshakeTimeout, 3*time.Second)
	}
	if transport.ExpectContinueTimeout <= 0 {
		t.Error("ExpectContinueTimeout: got 0, want a positive value")
	}
}

// TestOutboundTransportPool_DefaultsTimeoutsAreApplied proves a zero config
// still produces a transport with every timeout set.
func TestOutboundTransportPool_DefaultsTimeoutsAreApplied(t *testing.T) {
	transport := newOutboundTransportPool().Get(node.Hash{10}, &noopOutbound{}, nil)

	if transport.ResponseHeaderTimeout <= 0 {
		t.Error("ResponseHeaderTimeout: got 0, want a positive default")
	}
	if transport.TLSHandshakeTimeout <= 0 {
		t.Error("TLSHandshakeTimeout: got 0, want a positive default")
	}
}

// TestDirectHTTPTransport_AppliesConfiguredTimeouts covers the local direct
// (bypass) transport, which is built from its own constructor.
func TestDirectHTTPTransport_AppliesConfiguredTimeouts(t *testing.T) {
	transport := newDirectHTTPTransport(OutboundTransportConfig{
		ResponseHeaderTimeout: 7 * time.Second,
		TLSHandshakeTimeout:   3 * time.Second,
		DialTimeout:           11 * time.Second,
	}, nil, NewDirectDialGuard(false))

	if transport.ResponseHeaderTimeout != 7*time.Second {
		t.Errorf("ResponseHeaderTimeout: got %s, want %s", transport.ResponseHeaderTimeout, 7*time.Second)
	}
	if transport.TLSHandshakeTimeout != 3*time.Second {
		t.Errorf("TLSHandshakeTimeout: got %s, want %s", transport.TLSHandshakeTimeout, 3*time.Second)
	}
	if transport.ExpectContinueTimeout <= 0 {
		t.Error("ExpectContinueTimeout: got 0, want a positive value")
	}
}

// TestDirectDialGuard_DialerCarriesTimeout covers the dialer every local direct
// dial uses (direct HTTP transport and the CONNECT/SOCKS5 direct branch): a
// bare net.Dialer has no timeout at all, so a black-holed target would hold the
// connection open indefinitely.
func TestDirectDialGuard_DialerCarriesTimeout(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		dialer := NewDirectDialGuard(enabled).dialer()
		if dialer.Timeout <= 0 {
			t.Errorf("guard enabled=%v: dialer.Timeout = 0, want a positive value", enabled)
		}
		if dialer.KeepAlive <= 0 {
			t.Errorf("guard enabled=%v: dialer.KeepAlive = 0, want a positive value", enabled)
		}
	}

	// A configured dial timeout must win over the default.
	dialer := NewDirectDialGuard(true).dialerWithTimeout(250 * time.Millisecond)
	if dialer.Timeout != 250*time.Millisecond {
		t.Errorf("dialerWithTimeout: got %s, want %s", dialer.Timeout, 250*time.Millisecond)
	}
	if dialer.ControlContext == nil {
		t.Error("dialerWithTimeout dropped the dial-time policy hook")
	}
}

// TestOutboundTransportPool_ResponseHeaderTimeoutEndsHang reproduces the
// reported failure mode: the peer accepts the connection and never answers.
// The request must fail instead of hanging until the client gives up.
//
// The request context carries a watchdog deadline longer than the configured
// ResponseHeaderTimeout, so a regression fails the test instead of hanging it.
func TestOutboundTransportPool_ResponseHeaderTimeoutEndsHang(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer server.Close()
	// Registered second, so it runs first and lets the handler return before
	// httptest.Server.Close waits for it.
	defer close(release)

	pool := newOutboundTransportPoolWithConfig(OutboundTransportConfig{
		ResponseHeaderTimeout: 150 * time.Millisecond,
	})
	transport := pool.Get(node.Hash{11}, &dialThroughOutbound{}, nil)
	defer transport.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	start := time.Now()
	resp, err := transport.RoundTrip(req)
	elapsed := time.Since(start)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected the response-header timeout to end the request, got a response")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("RoundTrip blocked for %s: ResponseHeaderTimeout was not applied", elapsed)
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err = %v, want a timeout error", err)
	}
}

// TestOutboundTransportPool_DialTimeoutEndsHang pins the dial phase: an
// outbound whose dial never completes must fail on the configured deadline
// rather than on the request's own watchdog deadline.
func TestOutboundTransportPool_DialTimeoutEndsHang(t *testing.T) {
	pool := newOutboundTransportPoolWithConfig(OutboundTransportConfig{
		DialTimeout: 150 * time.Millisecond,
	})
	transport := pool.Get(node.Hash{12}, &blockingOutbound{}, nil)
	defer transport.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://blocked.example/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	start := time.Now()
	resp, err := transport.RoundTrip(req)
	elapsed := time.Since(start)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected the dial timeout to end the request, got a response")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("RoundTrip blocked for %s: DialTimeout was not applied", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}
