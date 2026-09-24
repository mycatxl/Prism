package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The local dial guard (PRISM_DIRECT_DENY_PRIVATE) must cover every local dial
// path, use one address policy and be impossible to bypass by re-resolving the
// target. These tests are local-only: loopback listeners, pinned resolvers and
// no outbound network access.

// pinnedDirectLookup returns a resolver that always answers with addrs.
func pinnedDirectLookup(addrs ...string) func(context.Context, string) ([]netip.Addr, error) {
	parsed := make([]netip.Addr, 0, len(addrs))
	for _, raw := range addrs {
		parsed = append(parsed, netip.MustParseAddr(raw))
	}
	return func(context.Context, string) ([]netip.Addr, error) {
		return parsed, nil
	}
}

func failingDirectLookup(err error) func(context.Context, string) ([]netip.Addr, error) {
	return func(context.Context, string) ([]netip.Addr, error) {
		return nil, err
	}
}

// TestDirectDialGuard_AddressSet pins the address classes the guard refuses.
func TestDirectDialGuard_AddressSet(t *testing.T) {
	denied := []string{
		"127.0.0.1",        // loopback v4
		"127.255.255.254",  // loopback v4 range
		"::1",              // loopback v6
		"10.0.0.1",         // RFC1918
		"172.16.0.1",       // RFC1918
		"172.31.255.254",   // RFC1918 upper bound
		"192.168.1.1",      // RFC1918
		"169.254.1.1",      // link-local v4
		"169.254.169.254",  // cloud metadata (AWS/GCP/Azure/...)
		"100.64.0.1",       // CGNAT lower bound
		"100.100.100.200",  // Alibaba Cloud metadata (CGNAT)
		"100.127.255.255",  // CGNAT upper bound
		"198.18.0.1",       // benchmarking lower bound
		"198.19.255.255",   // benchmarking upper bound
		"240.0.0.1",        // reserved
		"255.255.255.255",  // broadcast
		"0.0.0.0",          // unspecified v4
		"::",               // unspecified v6
		"fe80::1",          // link-local v6
		"fc00::1",          // unique local
		"fd00:ec2::254",    // AWS IMDS over IPv6
		"64:ff9b::7f00:1",  // NAT64 of 127.0.0.1
		"64:ff9b:1::1",     // local-use NAT64
		"ff02::1",          // multicast
		"::ffff:127.0.0.1", // IPv4-mapped loopback
		"::ffff:10.0.0.5",  // IPv4-mapped RFC1918
		"::ffff:169.254.169.254",
	}
	for _, raw := range denied {
		addr := netip.MustParseAddr(raw)
		if !isDeniedDirectAddr(addr) {
			t.Errorf("isDeniedDirectAddr(%s) = false, want true", raw)
		}
	}

	allowed := []string{
		"8.8.8.8",
		"1.1.1.1",
		"93.184.216.34",
		"100.63.255.255",  // just below CGNAT
		"100.128.0.0",     // just above CGNAT
		"198.17.255.255",  // just below benchmarking
		"198.20.0.1",      // just above benchmarking
		"239.255.255.255", // just below reserved
		"2606:4700:4700::1111",
		"2001:4860:4860::8888",
		"::ffff:8.8.8.8", // IPv4-mapped public address
	}
	for _, raw := range allowed {
		addr := netip.MustParseAddr(raw)
		if isDeniedDirectAddr(addr) {
			t.Errorf("isDeniedDirectAddr(%s) = true, want false", raw)
		}
	}

	if !isDeniedDirectAddr(netip.Addr{}) {
		t.Error("an invalid address must be refused, not allowed")
	}
}

// TestDirectDialGuard_CheckTarget covers name resolution, the fail-closed rule
// and the localhost shortcuts.
func TestDirectDialGuard_CheckTarget(t *testing.T) {
	enabled := NewDirectDialGuard(true)
	disabled := NewDirectDialGuard(false)

	t.Run("private resolution is refused", func(t *testing.T) {
		guard := enabled.withLookup(pinnedDirectLookup("10.0.0.5"))
		if err := guard.CheckTarget(context.Background(), "internal.example:443"); !errors.Is(err, errDirectDialDenied) {
			t.Fatalf("err = %v, want errDirectDialDenied", err)
		}
	})

	t.Run("mixed resolution is refused", func(t *testing.T) {
		guard := enabled.withLookup(pinnedDirectLookup("93.184.216.34", "192.168.0.10"))
		if err := guard.CheckTarget(context.Background(), "mixed.example:443"); !errors.Is(err, errDirectDialDenied) {
			t.Fatalf("err = %v, want errDirectDialDenied", err)
		}
	})

	t.Run("metadata resolution is refused", func(t *testing.T) {
		guard := enabled.withLookup(pinnedDirectLookup("100.100.100.200"))
		if err := guard.CheckTarget(context.Background(), "metadata.example:80"); !errors.Is(err, errDirectDialDenied) {
			t.Fatalf("err = %v, want errDirectDialDenied", err)
		}
	})

	t.Run("public resolution is allowed", func(t *testing.T) {
		guard := enabled.withLookup(pinnedDirectLookup("93.184.216.34"))
		if err := guard.CheckTarget(context.Background(), "public.example:443"); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	})

	t.Run("resolution failure is refused", func(t *testing.T) {
		guard := enabled.withLookup(failingDirectLookup(&net.DNSError{Err: "no such host", Name: "gone.example"}))
		err := guard.CheckTarget(context.Background(), "gone.example:443")
		if !errors.Is(err, errDirectDialUnresolvable) {
			t.Fatalf("err = %v, want errDirectDialUnresolvable (fail-closed)", err)
		}
	})

	t.Run("empty resolution is refused", func(t *testing.T) {
		guard := enabled.withLookup(pinnedDirectLookup())
		if err := guard.CheckTarget(context.Background(), "empty.example:443"); !errors.Is(err, errDirectDialUnresolvable) {
			t.Fatalf("err = %v, want errDirectDialUnresolvable", err)
		}
	})

	t.Run("localhost names are refused", func(t *testing.T) {
		for _, host := range []string{"localhost:80", "localhost", "api.localhost:8080"} {
			if err := enabled.CheckTarget(context.Background(), host); !errors.Is(err, errDirectDialDenied) {
				t.Fatalf("CheckTarget(%q) err = %v, want errDirectDialDenied", host, err)
			}
		}
	})

	t.Run("literals and ports", func(t *testing.T) {
		if err := enabled.CheckTarget(context.Background(), "[::ffff:127.0.0.1]:80"); !errors.Is(err, errDirectDialDenied) {
			t.Fatalf("IPv4-mapped loopback err = %v, want errDirectDialDenied", err)
		}
		if err := enabled.CheckTarget(context.Background(), "[64:ff9b::7f00:1]:80"); !errors.Is(err, errDirectDialDenied) {
			t.Fatalf("NAT64 err = %v, want errDirectDialDenied", err)
		}
		if err := enabled.CheckTarget(context.Background(), "100.100.100.200:80"); !errors.Is(err, errDirectDialDenied) {
			t.Fatalf("CGNAT metadata err = %v, want errDirectDialDenied", err)
		}
		if err := enabled.CheckTarget(context.Background(), "93.184.216.34:443"); err != nil {
			t.Fatalf("public literal err = %v, want nil", err)
		}
	})

	t.Run("disabled guard allows everything", func(t *testing.T) {
		for _, host := range []string{"127.0.0.1:80", "localhost", "::1", "100.100.100.200"} {
			if err := disabled.CheckTarget(context.Background(), host); err != nil {
				t.Fatalf("CheckTarget(%q) with the guard off err = %v, want nil", host, err)
			}
		}
	})
}

// TestDirectDialGuard_DialTimeControlRefusesReboundAddress pins the dial-time
// half of the policy: whatever the pre-dial check saw, the address that is
// about to be connected to is validated again.
func TestDirectDialGuard_DialTimeControlRefusesReboundAddress(t *testing.T) {
	guard := NewDirectDialGuard(true)
	dialer := guard.dialer()
	if dialer.ControlContext == nil {
		t.Fatal("guard dialer has no ControlContext: a re-resolution would bypass the policy")
	}

	for _, denied := range []string{"127.0.0.1:80", "10.1.2.3:443", "100.100.100.200:80", "[::1]:80", "[64:ff9b::7f00:1]:80"} {
		if err := dialer.ControlContext(context.Background(), "tcp", denied, nil); !errors.Is(err, errDirectDialDenied) {
			t.Errorf("control(%s) err = %v, want errDirectDialDenied", denied, err)
		}
	}
	for _, allowed := range []string{"93.184.216.34:443", "[2606:4700:4700::1111]:443"} {
		if err := dialer.ControlContext(context.Background(), "tcp", allowed, nil); err != nil {
			t.Errorf("control(%s) err = %v, want nil", allowed, err)
		}
	}
	if err := dialer.ControlContext(context.Background(), "tcp", "rebind.example:80", nil); !errors.Is(err, errDirectDialDenied) {
		t.Error("an unvalidated name reaching the dial hook must be refused")
	}

	// The dispatcher used by every local direct dial must carry the hook, and a
	// disabled guard must not.
	disabled := NewDirectDialGuard(false).dialer()
	if disabled.ControlContext != nil {
		t.Error("a disabled guard must keep the plain Resin dialer")
	}
}

// TestDirectDialGuard_DirectTransportRefusesPrivateAddress proves the direct
// HTTP transport itself refuses a rebound address: no pre-dial check runs here.
func TestDirectDialGuard_DirectTransportRefusesPrivateAddress(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	transport := newDirectHTTPTransport(OutboundTransportConfig{}, nil, NewDirectDialGuard(true))
	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/secret", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected the dial-time guard to refuse a loopback target")
	}
	if !strings.Contains(err.Error(), directDenyPrivateHint) {
		t.Fatalf("err = %v, want %s in the message", err, directDenyPrivateHint)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("upstream received %d requests, want 0", got)
	}
}

// TestForwardProxy_DirectDenyPrivateBlocksLocalDial covers the forward HTTP
// entrypoint: the guard is enforced there and the default stays Resin.
func TestForwardProxy_DirectDenyPrivateBlocksLocalDial(t *testing.T) {
	var upstreamHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("direct-forward"))
	}))
	defer upstream.Close()

	for _, tc := range []struct {
		name      string
		deny      bool
		wantCode  int
		wantError string
	}{
		{name: "enabled refuses loopback", deny: true, wantCode: http.StatusForbidden, wantError: "DIRECT_TARGET_DENIED"},
		{name: "disabled keeps Resin behaviour", deny: false, wantCode: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := NewForwardProxy(ForwardProxyConfig{
				ProxyToken:        "tok",
				Events:            NoOpEventEmitter{},
				ProxyBypassRules:  []string{"127.*"},
				DirectDenyPrivate: tc.deny,
			})

			req := httptest.NewRequest(http.MethodGet, upstream.URL+"/direct", nil)
			req.Header.Set("Proxy-Authorization", basicAuth("plat", "tok"))
			w := httptest.NewRecorder()
			fp.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("status: got %d, want %d (body=%q)", w.Code, tc.wantCode, w.Body.String())
			}
			if got := w.Header().Get("X-Prism-Error"); got != tc.wantError {
				t.Fatalf("X-Prism-Error: got %q, want %q", got, tc.wantError)
			}
		})
	}

	// The guarded request never reached the local target; the unguarded one did.
	if got := upstreamHits.Load(); got != 1 {
		t.Fatalf("upstream hits = %d, want 1 (only the request made with the guard off)", got)
	}
}

// TestForwardProxy_DirectDenyPrivateFailClosed covers the forward entrypoint
// against a name that resolves to a metadata address and against a name that
// does not resolve at all.
func TestForwardProxy_DirectDenyPrivateFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lookup func(context.Context, string) ([]netip.Addr, error)
	}{
		{name: "name resolves to alibaba metadata", lookup: pinnedDirectLookup("100.100.100.200")},
		{name: "name resolves to private range", lookup: pinnedDirectLookup("10.11.12.13")},
		{name: "name does not resolve", lookup: failingDirectLookup(&net.DNSError{Err: "no such host"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := NewForwardProxy(ForwardProxyConfig{
				ProxyToken:        "tok",
				Events:            NoOpEventEmitter{},
				ProxyBypassRules:  []string{"bypass.test"},
				DirectDenyPrivate: true,
			})
			fp.directGuard = fp.directGuard.withLookup(tc.lookup)

			req := httptest.NewRequest(http.MethodGet, "http://bypass.test/v1/meta", nil)
			req.Header.Set("Proxy-Authorization", basicAuth("plat", "tok"))
			w := httptest.NewRecorder()
			fp.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Fatalf("status: got %d, want 403 (body=%q)", w.Code, w.Body.String())
			}
			if got := w.Header().Get("X-Prism-Error"); got != "DIRECT_TARGET_DENIED" {
				t.Fatalf("X-Prism-Error: got %q, want DIRECT_TARGET_DENIED", got)
			}
		})
	}
}

// TestForwardProxyConnect_DirectDenyPrivateBlocksLocalDial covers the CONNECT
// tunnel entrypoint.
func TestForwardProxyConnect_DirectDenyPrivateBlocksLocalDial(t *testing.T) {
	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken:        "tok",
		Events:            NoOpEventEmitter{},
		ProxyBypassRules:  []string{"127.*"},
		DirectDenyPrivate: true,
	})

	req := httptest.NewRequest(http.MethodConnect, "127.0.0.1:8080", nil)
	// httptest does not parse an absolute-form CONNECT target into Host, and the
	// forward proxy dials r.Host.
	req.Host = "127.0.0.1:8080"
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

// TestForwardProxyConnect_DirectDenyPrivateControlsDisabled keeps the Resin
// behaviour: with the switch off the tunnel is dialled, not refused.
func TestForwardProxyConnect_DirectDenyPrivateControlsDisabled(t *testing.T) {
	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken:       "tok",
		Events:           NoOpEventEmitter{},
		ProxyBypassRules: []string{"127.*"},
	})

	req := httptest.NewRequest(http.MethodConnect, "127.0.0.1:8080", nil)
	// httptest does not parse an absolute-form CONNECT target into Host, and the
	// forward proxy dials r.Host.
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Proxy-Authorization", basicAuth("plat", "tok"))
	w := httptest.NewRecorder()
	fp.ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Fatalf("status: got 403 with the guard off (body=%q)", w.Body.String())
	}
	if got := w.Header().Get("X-Prism-Error"); got == "DIRECT_TARGET_DENIED" {
		t.Fatal("X-Prism-Error: DIRECT_TARGET_DENIED with the guard off")
	}
}

// TestSocks5Inbound_DirectDenyPrivateBlocksLocalDial covers the SOCKS5
// entrypoint: the refusal is the RFC 1928 "not allowed by ruleset" reply.
func TestSocks5Inbound_DirectDenyPrivateBlocksLocalDial(t *testing.T) {
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken:        "tok",
		Events:            NoOpEventEmitter{},
		ProxyBypassRules:  []string{"127.*"},
		DirectDenyPrivate: true,
	})

	clientConn, reader, done := startSocks5Session(t, inbound)
	defer clientConn.Close()

	writeAll(t, clientConn, []byte{socks5Version, 1, socks5MethodUserPass})
	if got := readExactly(t, reader, 2); got[1] != socks5MethodUserPass {
		t.Fatalf("selected method: got %d, want %d", got[1], socks5MethodUserPass)
	}
	writeAll(t, clientConn, socks5UserPassPacket("plat.acct", "tok"))
	if got := readExactly(t, reader, 2); got[1] != socks5UserPassStatusSuccess {
		t.Fatalf("auth status: got %d, want %d", got[1], socks5UserPassStatusSuccess)
	}

	writeAll(t, clientConn, socks5ConnectIPv4Packet("127.0.0.1:8080"))
	reply := readExactly(t, reader, 10)
	if reply[1] != socks5ReplyConnectionNotAllowed {
		t.Fatalf("connect reply: got %d, want %d (not allowed by ruleset)", reply[1], socks5ReplyConnectionNotAllowed)
	}

	_ = clientConn.Close()
	<-done
}

// TestSocks5Inbound_DirectDenyPrivateDisabledDialsTarget keeps the Resin
// behaviour on the SOCKS5 local dial path.
func TestSocks5Inbound_DirectDenyPrivateDisabledDialsTarget(t *testing.T) {
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetLn.Close()
	targetDone := make(chan struct{})
	go func() {
		defer close(targetDone)
		conn, acceptErr := targetLn.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken:       "tok",
		Events:           NoOpEventEmitter{},
		ProxyBypassRules: []string{"127.*"},
	})

	clientConn, reader, done := startSocks5Session(t, inbound)
	defer clientConn.Close()

	writeAll(t, clientConn, []byte{socks5Version, 1, socks5MethodUserPass})
	_ = readExactly(t, reader, 2)
	writeAll(t, clientConn, socks5UserPassPacket("plat.acct", "tok"))
	_ = readExactly(t, reader, 2)

	writeAll(t, clientConn, socks5ConnectIPv4Packet(targetLn.Addr().String()))
	reply := readExactly(t, reader, 10)
	if reply[1] != socks5ReplySucceeded {
		t.Fatalf("connect reply: got %d, want success with the guard off", reply[1])
	}
	const payload = "socks5-off"
	writeAll(t, clientConn, []byte(payload))
	if got := string(readExactly(t, reader, len(payload))); got != payload {
		t.Fatalf("echo payload: got %q, want %q", got, payload)
	}

	_ = clientConn.Close()
	<-done
	<-targetDone
}

// TestReverseProxy_DirectDenyPrivateAddressClasses covers the reverse-proxy
// bypass branch for the address classes the pre-fix guard missed. The request is
// refused before any dial, so no target needs to exist.
func TestReverseProxy_DirectDenyPrivateAddressClasses(t *testing.T) {
	for _, host := range []string{
		"100.100.100.200:80",   // Alibaba Cloud metadata (CGNAT)
		"100.64.0.1:80",        // CGNAT lower bound
		"198.18.0.1:80",        // benchmarking
		"240.0.0.1:80",         // reserved
		"[64:ff9b::7f00:1]:80", // NAT64 of 127.0.0.1
		"[::ffff:127.0.0.1]:80",
		"[fd00:ec2::254]:80",
	} {
		t.Run(host, func(t *testing.T) {
			rp := NewReverseProxy(ReverseProxyConfig{
				ProxyToken:        "tok",
				Events:            NoOpEventEmitter{},
				ProxyBypassRules:  []string{"<local>", "*"},
				DirectDenyPrivate: true,
			})

			path := fmt.Sprintf("/tok/plat:acct/http/%s/api/meta", host)
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			rp.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Fatalf("status: got %d, want 403 (body=%q)", w.Code, w.Body.String())
			}
			if got := w.Header().Get("X-Prism-Error"); got != "DIRECT_TARGET_DENIED" {
				t.Fatalf("X-Prism-Error: got %q, want DIRECT_TARGET_DENIED", got)
			}
		})
	}
}

// TestReverseProxy_DirectDenyPrivateFailClosedOnResolution pins the fail-closed
// rule on the guarded path: a name the guard cannot resolve is refused instead
// of being handed to the transport, which would resolve it again.
func TestReverseProxy_DirectDenyPrivateFailClosedOnResolution(t *testing.T) {
	rp := NewReverseProxy(ReverseProxyConfig{
		ProxyToken:        "tok",
		Events:            NoOpEventEmitter{},
		ProxyBypassRules:  []string{"rebind.test"},
		DirectDenyPrivate: true,
	})
	rp.directGuard = rp.directGuard.withLookup(failingDirectLookup(&net.DNSError{Err: "no such host"}))

	req := httptest.NewRequest(http.MethodGet, "/tok/plat:acct/http/rebind.test/api/meta", nil)
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403 (body=%q)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Prism-Error"); got != "DIRECT_TARGET_DENIED" {
		t.Fatalf("X-Prism-Error: got %q, want DIRECT_TARGET_DENIED", got)
	}
}

// TestReverseProxy_DirectDenyPrivateRebindingRefusedAtDialTime proves that a
// rebinding resolver cannot win: the name is validated against a public answer
// and then dialled, and the dial-time hook refuses the private address it
// actually resolves to.
func TestReverseProxy_DirectDenyPrivateRebindingRefusedAtDialTime(t *testing.T) {
	guard := NewDirectDialGuard(true)
	control := guard.dialer().ControlContext
	if control == nil {
		t.Fatal("guard dialer has no ControlContext")
	}
	// What the pre-dial check saw.
	if err := guard.withLookup(pinnedDirectLookup("93.184.216.34")).CheckTarget(context.Background(), "rebind.test:80"); err != nil {
		t.Fatalf("pre-dial check err = %v, want nil", err)
	}
	// What the socket is actually connected to.
	if err := control(context.Background(), "tcp", "127.0.0.1:80", nil); !errors.Is(err, errDirectDialDenied) {
		t.Fatalf("dial-time check err = %v, want errDirectDialDenied", err)
	}
}

// TestDirectDialGuard_TunnelDepsSharePolicy pins that CONNECT and SOCKS5 use
// the same policy object as the reverse proxy: one policy, one code path.
func TestDirectDialGuard_TunnelDepsSharePolicy(t *testing.T) {
	guard := NewDirectDialGuard(true).withLookup(pinnedDirectLookup("192.168.5.5"))
	deps := tunnelDeps{directGuard: guard, bypass: NewTargetBypassMatcher([]string{"192.168.*"})}
	result := prepareConnectTunnel(context.Background(), deps, "plat", "acct", "192.168.5.5:80")
	if result.proxyErr != ErrDirectTargetDenied {
		t.Fatalf("proxyErr = %v, want ErrDirectTargetDenied", result.proxyErr)
	}
	if result.session != nil {
		t.Fatal("a refused target must not produce a dialled session")
	}
}

// TestDirectDialGuard_DisabledTunnelStillDials keeps the default path working
// end to end through the tunnel preparation helper.
func TestDirectDialGuard_DisabledTunnelStillDials(t *testing.T) {
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

	deps := tunnelDeps{bypass: NewTargetBypassMatcher([]string{"127.*"})}
	result := prepareConnectTunnel(context.Background(), deps, "plat", "acct", targetLn.Addr().String())
	if result.session == nil {
		t.Fatalf("expected a dialled session, got proxyErr=%v", result.proxyErr)
	}
	_ = result.session.upstreamConn.Close()
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("the target was never dialled with the guard off")
	}
}

// TestSocks5ReplyForProxyError pins the reply-code mapping.
func TestSocks5ReplyForProxyError(t *testing.T) {
	if got := socks5ReplyForProxyError(ErrDirectTargetDenied); got != socks5ReplyConnectionNotAllowed {
		t.Fatalf("reply = %d, want %d", got, socks5ReplyConnectionNotAllowed)
	}
	if got := socks5ReplyForProxyError(ErrUpstreamRequestFailed); got != socks5ReplyGeneralFailure {
		t.Fatalf("reply = %d, want %d", got, socks5ReplyGeneralFailure)
	}
}
