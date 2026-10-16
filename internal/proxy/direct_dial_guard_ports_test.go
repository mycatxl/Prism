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
)

// The destination port policy (PRISM_BLOCKED_DESTINATION_PORTS) is independent
// of PRISM_DIRECT_DENY_PRIVATE: it must refuse the configured ports at both
// halves of the local dial policy and leave every other port exactly as it was.
// These tests are local-only: loopback listeners, pinned resolvers and no
// outbound network access.

// smtpPorts mirrors the shipped default of PRISM_BLOCKED_DESTINATION_PORTS.
var smtpPorts = []int{25, 465, 587}

// TestDirectDialGuard_BlockedPortsCheckTarget covers the pre-dial half: a
// domain target and an IP literal target are both refused for a blocked port,
// even though the resolved address is public and the address policy allows it.
func TestDirectDialGuard_BlockedPortsCheckTarget(t *testing.T) {
	guard := NewDirectDialGuard(true).
		withLookup(pinnedDirectLookup("93.184.216.34")).
		WithBlockedDestinationPorts(smtpPorts)

	for _, host := range []string{"smtp.example:25", "smtp.example:465", "smtp.example:587"} {
		err := guard.CheckTarget(context.Background(), host)
		if !errors.Is(err, errDirectDialPortBlocked) {
			t.Fatalf("CheckTarget(%q) err = %v, want errDirectDialPortBlocked", host, err)
		}
		if !strings.Contains(err.Error(), strings.TrimPrefix(host, "smtp.example:")) {
			t.Errorf("CheckTarget(%q) err = %v, want the refused port in the message", host, err)
		}
		if !strings.Contains(err.Error(), blockedDestinationPortsHint) {
			t.Errorf("CheckTarget(%q) err = %v, want %s in the message", host, err, blockedDestinationPortsHint)
		}
	}

	t.Run("IP literal target", func(t *testing.T) {
		err := guard.CheckTarget(context.Background(), "93.184.216.34:25")
		if !errors.Is(err, errDirectDialPortBlocked) {
			t.Fatalf("CheckTarget(public literal:25) err = %v, want errDirectDialPortBlocked", err)
		}
	})

	t.Run("allowed port is not refused", func(t *testing.T) {
		if err := guard.CheckTarget(context.Background(), "smtp.example:443"); err != nil {
			t.Fatalf("CheckTarget(allowed port) err = %v, want nil", err)
		}
	})
}

// TestDirectDialGuard_BlockedPortsControlContext covers the dial-time half: the
// socket address itself is refused, and an address whose port cannot be read
// fails closed while the port policy is active.
func TestDirectDialGuard_BlockedPortsControlContext(t *testing.T) {
	guard := NewDirectDialGuard(true).WithBlockedDestinationPorts(smtpPorts)
	control := guard.dialer().ControlContext
	if control == nil {
		t.Fatal("guard dialer has no ControlContext: a blocked port would not be enforced at dial time")
	}
	if err := control(context.Background(), "tcp", "1.2.3.4:25", nil); !errors.Is(err, errDirectDialPortBlocked) {
		t.Fatalf("control(1.2.3.4:25) err = %v, want errDirectDialPortBlocked", err)
	}
	if err := control(context.Background(), "tcp", "[::1]:465", nil); !errors.Is(err, errDirectDialPortBlocked) {
		t.Fatalf("control([::1]:465) err = %v, want errDirectDialPortBlocked", err)
	}
	if err := control(context.Background(), "tcp", "1.2.3.4:443", nil); err != nil {
		t.Fatalf("control(1.2.3.4:443) err = %v, want nil", err)
	}
	if err := control(context.Background(), "tcp", "1.2.3.4:not-a-port", nil); !errors.Is(err, errDirectDialPortBlocked) {
		t.Fatalf("control(1.2.3.4:not-a-port) err = %v, want a fail-closed errDirectDialPortBlocked", err)
	}
}

// TestDirectDialGuard_BlockedPortsApplyWithAddressPolicyOff pins the
// independence requirement: PRISM_DIRECT_DENY_PRIVATE off must not reopen a
// blocked port, and it must not turn the port policy into an address policy.
func TestDirectDialGuard_BlockedPortsApplyWithAddressPolicyOff(t *testing.T) {
	guard := NewDirectDialGuard(false).WithBlockedDestinationPorts(smtpPorts)

	for _, host := range []string{"smtp.example:25", "127.0.0.1:465", "[::1]:587"} {
		err := guard.CheckTarget(context.Background(), host)
		if !errors.Is(err, errDirectDialPortBlocked) {
			t.Fatalf("CheckTarget(%q) with the address policy off err = %v, want errDirectDialPortBlocked", host, err)
		}
	}

	control := guard.dialer().ControlContext
	if control == nil {
		t.Fatal("the dial-time hook must be installed for the port policy alone")
	}
	if err := control(context.Background(), "tcp", "1.2.3.4:25", nil); !errors.Is(err, errDirectDialPortBlocked) {
		t.Fatalf("control(1.2.3.4:25) err = %v, want errDirectDialPortBlocked", err)
	}
	// The address policy is still off, so a loopback address on an allowed port
	// passes the hook and stays dialable.
	if err := control(context.Background(), "tcp", "127.0.0.1:8080", nil); err != nil {
		t.Fatalf("control(127.0.0.1:8080) err = %v, want nil with PRISM_DIRECT_DENY_PRIVATE off", err)
	}
}

// TestDirectDialGuard_EmptyBlockedPortsBlockNothing pins the other side of the
// switch: an empty list adds no restriction of its own and does not disturb the
// address policy that may be active next to it.
func TestDirectDialGuard_EmptyBlockedPortsBlockNothing(t *testing.T) {
	guard := NewDirectDialGuard(false).WithBlockedDestinationPorts(nil)
	if control := guard.dialer().ControlContext; control != nil {
		t.Fatal("an empty port list with the address policy off must keep the plain Resin dialer")
	}
	for _, host := range []string{"127.0.0.1:25", "smtp.example:587"} {
		if err := guard.CheckTarget(context.Background(), host); err != nil {
			t.Fatalf("CheckTarget(%q) with an empty port list err = %v, want nil", host, err)
		}
	}

	// An empty list must not weaken the original private-target refusal: the
	// address policy is the only one left in charge.
	strict := NewDirectDialGuard(true).WithBlockedDestinationPorts(nil)
	if err := strict.CheckTarget(context.Background(), "10.0.0.5:80"); !errors.Is(err, errDirectDialDenied) {
		t.Fatalf("CheckTarget(10.0.0.5:80) err = %v, want errDirectDialDenied with the port list empty", err)
	}
}

// TestDirectDialGuard_BlockedPortsAllowOtherPorts proves the positive case end
// to end: a port that is not blocked still dials, here against a loopback
// listener.
func TestDirectDialGuard_BlockedPortsAllowOtherPorts(t *testing.T) {
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetLn.Close()
	accepted := make(chan struct{})
	go func() {
		conn, acceptErr := targetLn.Accept()
		if acceptErr != nil {
			return
		}
		close(accepted)
		_ = conn.Close()
	}()

	// PRISM_DIRECT_DENY_PRIVATE is off, so only the port policy applies and the
	// listener's high loopback port is not in it.
	guard := NewDirectDialGuard(false).WithBlockedDestinationPorts(smtpPorts)
	if err := guard.CheckTarget(context.Background(), targetLn.Addr().String()); err != nil {
		t.Fatalf("CheckTarget(%s) err = %v, want nil", targetLn.Addr(), err)
	}
	conn, err := guard.dialer().DialContext(context.Background(), "tcp", targetLn.Addr().String())
	if err != nil {
		t.Fatalf("dial %s through the guarded dialer: %v", targetLn.Addr(), err)
	}
	_ = conn.Close()
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("the allowed target was never dialled")
	}
}

// TestForwardProxy_BlockedDestinationPortsRefuseBypassTarget covers the
// configuration path from PRISM_BLOCKED_DESTINATION_PORTS to the proxy
// entrypoints: with the address policy off, the bypass branch refuses the SMTP
// port instead of dialling it.
func TestForwardProxy_BlockedDestinationPortsRefuseBypassTarget(t *testing.T) {
	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken:              "tok",
		Events:                  NoOpEventEmitter{},
		ProxyBypassRules:        []string{"bypass.test"},
		BlockedDestinationPorts: []int{25},
	})

	req := httptest.NewRequest(http.MethodGet, "http://bypass.test:25/relay", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("plat", "tok"))
	w := httptest.NewRecorder()
	fp.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403 (body=%q)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Prism-Error"); got != "DIRECT_TARGET_DENIED" {
		t.Fatalf("X-Prism-Error: got %q, want DIRECT_TARGET_DENIED", got)
	}
}
