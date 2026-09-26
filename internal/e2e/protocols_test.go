package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singjson "github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"

	"prism/internal/config"
	"prism/internal/outbound"
)

// Offline protocol end-to-end coverage (docs/plan/13 §1).
//
// The acceptance item this file satisfies is "each protocol can be imported and
// is reachable in an offline end-to-end test". The protocol matrix in
// internal/outbound proves every outbound *builds* and that its detour resolves,
// but its dial target is 127.0.0.1:1 and it needs no working peer, so it cannot
// show that a protocol carries traffic. Here each case stands up a real sing-box
// instance serving that protocol's inbound on a loopback port, points a
// Prism-built outbound at it, and reads a body back from a loopback HTTP server
// through the tunnel. Nothing outside the process is contacted.
//
// The inbound is described as JSON and parsed with sing's context-aware
// unmarshaller, so the registry in the context supplies the concrete option type
// for each protocol. That is what keeps this table-driven: a plain
// json.Unmarshal leaves option.Inbound.Options nil and every protocol then
// rejects its own credentials.

// targetBody is the marker the tunnel must carry back.
const targetBody = "prism-offline-e2e"

// targetServer is a loopback HTTP responder: the destination the tunnel must
// reach. It answers every connection with a fixed body.
type targetServer struct {
	listener net.Listener
	port     uint16
	mu       sync.Mutex
	served   int
}

func startTarget(t *testing.T) *targetServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	target := &targetServer{
		listener: listener,
		port:     uint16(listener.Addr().(*net.TCPAddr).Port),
	}
	go target.serve()
	t.Cleanup(func() { _ = listener.Close() })
	return target
}

func (o *targetServer) serve() {
	for {
		conn, err := o.listener.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			buf := make([]byte, 1024)
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, _ = c.Read(buf)
			response := fmt.Sprintf(
				"HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
				len(targetBody), targetBody)
			_, _ = c.Write([]byte(response))
			o.mu.Lock()
			o.served++
			o.mu.Unlock()
		}(conn)
	}
}

func (o *targetServer) requests() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.served
}

// freeLoopbackPort reserves a port and releases it, so an inbound can bind it.
func freeLoopbackPort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer listener.Close()
	return uint16(listener.Addr().(*net.TCPAddr).Port)
}

// startPeer starts a sing-box instance with the given inbound JSON template. The
// template must contain exactly one %d for the listen port.
func startPeer(t *testing.T, inboundTemplate string) uint16 {
	t.Helper()
	port := freeLoopbackPort(t)
	ctx := include.Context(context.Background())
	raw := fmt.Sprintf(inboundTemplate, port)

	var inbound option.Inbound
	if err := singjson.UnmarshalContext(ctx, []byte(raw), &inbound); err != nil {
		t.Fatalf("unmarshal inbound %s: %v", raw, err)
	}
	if inbound.Options == nil {
		t.Fatalf("inbound %s produced nil options; the context registry was not consulted", raw)
	}

	instance, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{
			Log:      &option.LogOptions{Disabled: true},
			Inbounds: []option.Inbound{inbound},
		},
	})
	if err != nil {
		t.Fatalf("box.New(%s): %v", raw, err)
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		t.Fatalf("instance.Start(%s): %v", raw, err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	return port
}

// tlsCertificate and tlsKey read the self-signed pair committed in testdata/tls,
// so the TLS cases need no external tooling.
func tlsCertificate(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "tls", "cert.pem"))
	if err != nil {
		t.Fatalf("read test certificate: %v", err)
	}
	return string(body)
}

func tlsKey(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "tls", "key.pem"))
	if err != nil {
		t.Fatalf("read test key: %v", err)
	}
	return string(body)
}

// jsonStringLiteral renders s as a JSON string literal, so a multi-line PEM block
// can be embedded in an inbound template.
func jsonStringLiteral(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		// A string always marshals; this cannot happen.
		return `""`
	}
	return string(encoded)
}

// newBuilder builds the same sing-box runtime the daemon uses.
func newBuilder(t *testing.T) *outbound.SingboxBuilder {
	t.Helper()
	builder, err := outbound.NewSingboxBuilderWithConfig(outbound.SingboxBuilderConfig{
		DNSUpstreams:          config.DefaultNodeDNSUpstreams(),
		QuietInterfaceMonitor: true,
	})
	if err != nil {
		t.Fatalf("NewSingboxBuilderWithConfig: %v", err)
	}
	t.Cleanup(func() { _ = builder.Close() })
	return builder
}

// offlineCase is one protocol's offline round trip.
type offlineCase struct {
	name string
	// inbound is the sing-box inbound JSON template (one %d for the port).
	inbound string
	// node is the Prism node document JSON template (one %d for the port).
	node string
	// udp marks a protocol that carries datagrams: the TCP-through-tunnel
	// assertion does not apply, so the case asserts the handshake instead.
	udp bool
}

func offlineCases(t *testing.T) []offlineCase {
	t.Helper()
	cert := jsonStringLiteral(tlsCertificate(t))
	key := jsonStringLiteral(tlsKey(t))
	return []offlineCase{
		{
			name: "shadowsocks-aes-256-gcm",
			inbound: `{
				"type": "shadowsocks", "tag": "in", "listen": "127.0.0.1", "listen_port": %d,
				"method": "aes-256-gcm", "password": "offline-pw"
			}`,
			node: `{"type":"shadowsocks","server":"127.0.0.1","server_port":%d,"method":"aes-256-gcm","password":"offline-pw"}`,
		},
		{
			name: "shadowsocks-2022-blake3-aes-256-gcm",
			inbound: `{
				"type": "shadowsocks", "tag": "in", "listen": "127.0.0.1", "listen_port": %d,
				"method": "2022-blake3-aes-256-gcm", "password": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
			}`,
			node: `{"type":"shadowsocks","server":"127.0.0.1","server_port":%d,"method":"2022-blake3-aes-256-gcm","password":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`,
		},
		{
			name: "vmess-ws",
			inbound: `{
				"type": "vmess", "tag": "in", "listen": "127.0.0.1", "listen_port": %d,
				"users": [{"uuid": "11111111-2222-3333-4444-555555555555", "alterId": 0}],
				"transport": {"type": "ws", "path": "/ws"}
			}`,
			node: `{"type":"vmess","server":"127.0.0.1","server_port":%d,"uuid":"11111111-2222-3333-4444-555555555555","alter_id":0,"security":"auto","transport":{"type":"ws","path":"/ws"}}`,
		},
		{
			name: "vless-ws",
			inbound: `{
				"type": "vless", "tag": "in", "listen": "127.0.0.1", "listen_port": %d,
				"users": [{"uuid": "11111111-2222-3333-4444-555555555555"}],
				"transport": {"type": "ws", "path": "/ws"}
			}`,
			node: `{"type":"vless","server":"127.0.0.1","server_port":%d,"uuid":"11111111-2222-3333-4444-555555555555","transport":{"type":"ws","path":"/ws"}}`,
		},
		{
			name: "vless-tls",
			inbound: fmt.Sprintf(`{
				"type": "vless", "tag": "in", "listen": "127.0.0.1", "listen_port": %%d,
				"users": [{"uuid": "11111111-2222-3333-4444-555555555555"}],
				"tls": {"enabled": true, "certificate": [%s], "key": [%s]}
			}`, cert, key),
			node: `{"type":"vless","server":"127.0.0.1","server_port":%d,"uuid":"11111111-2222-3333-4444-555555555555","tls":{"enabled":true,"insecure":true}}`,
		},
		{
			name: "trojan",
			inbound: `{
				"type": "trojan", "tag": "in", "listen": "127.0.0.1", "listen_port": %d,
				"users": [{"password": "offline-trojan-pw"}]
			}`,
			node: `{"type":"trojan","server":"127.0.0.1","server_port":%d,"password":"offline-trojan-pw"}`,
		},
		{
			name: "trojan-tls",
			inbound: fmt.Sprintf(`{
				"type": "trojan", "tag": "in", "listen": "127.0.0.1", "listen_port": %%d,
				"users": [{"password": "offline-trojan-tls-pw"}],
				"tls": {"enabled": true, "certificate": [%s], "key": [%s]}
			}`, cert, key),
			node: `{"type":"trojan","server":"127.0.0.1","server_port":%d,"password":"offline-trojan-tls-pw","tls":{"enabled":true,"insecure":true}}`,
		},
		{
			name: "socks5-with-credentials",
			inbound: `{
				"type": "socks", "tag": "in", "listen": "127.0.0.1", "listen_port": %d,
				"users": [{"username": "offline-user", "password": "offline-socks-pw"}]
			}`,
			node: `{"type":"socks","server":"127.0.0.1","server_port":%d,"version":"5","username":"offline-user","password":"offline-socks-pw"}`,
		},
		{
			name: "http-proxy-with-credentials",
			inbound: `{
				"type": "http", "tag": "in", "listen": "127.0.0.1", "listen_port": %d,
				"users": [{"username": "offline-user", "password": "offline-http-pw"}]
			}`,
			node: `{"type":"http","server":"127.0.0.1","server_port":%d,"username":"offline-user","password":"offline-http-pw"}`,
		},
		{
			// anytls always runs over TLS, so it needs a real key pair. The
			// certificate is self-signed and the node sets insecure: true, which
			// is how an anytls node with a self-signed cert is configured in
			// production.
			name: "anytls",
			inbound: fmt.Sprintf(`{
				"type": "anytls", "tag": "in", "listen": "127.0.0.1", "listen_port": %%d,
				"users": [{"password": "offline-anytls-pw"}],
				"tls": {"enabled": true, "certificate": [%s], "key": [%s]}
			}`, cert, key),
			node: `{"type":"anytls","server":"127.0.0.1","server_port":%d,"password":"offline-anytls-pw","tls":{"enabled":true,"insecure":true}}`,
		},
	}
}

// TestOfflineProtocolRoundTrip proves each protocol carries real traffic over
// loopback, with no external network involved.
func TestOfflineProtocolRoundTrip(t *testing.T) {
	builder := newBuilder(t)

	for _, tc := range offlineCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			port := startPeer(t, tc.inbound)
			target := startTarget(t)

			nodeJSON := fmt.Sprintf(tc.node, port)
			ob, err := builder.Build([]byte(nodeJSON))
			if err != nil {
				t.Fatalf("Build(%s): %v", nodeJSON, err)
			}
			defer closeOutbound(t, ob, tc.name)

			dialCtx, cancel := context.WithTimeout(context.Background(), dialTimeout)
			defer cancel()
			conn, err := ob.DialContext(dialCtx, "tcp",
				M.SocksaddrFrom(netip.MustParseAddr("127.0.0.1"), target.port))
			if err != nil {
				t.Fatalf("dial through %s: %v", tc.name, err)
			}
			defer conn.Close()

			if _, err := conn.Write([]byte("GET / HTTP/1.0\r\nHost: localhost\r\n\r\n")); err != nil {
				t.Fatalf("write through %s: %v", tc.name, err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
			response := readTunnelResponse(conn)
			if !strings.Contains(response, targetBody) {
				t.Fatalf("response through %s = %q, want it to contain %q",
					tc.name, response, targetBody)
			}
			if target.requests() == 0 {
				t.Fatalf("%s: the tunnel returned a body but the target saw no request", tc.name)
			}
		})
	}
}

// TestOfflineProtocolRoundTripRejectsWrongCredentials proves the round trip is
// real rather than a bypass: the same path with a wrong secret must not deliver
// the target body.
//
// Without this, a bug that made the outbound dial the target directly (ignoring
// the node) would still produce a green round trip.
func TestOfflineProtocolRoundTripRejectsWrongCredentials(t *testing.T) {
	builder := newBuilder(t)

	for _, tc := range []struct {
		name    string
		inbound string
		node    string
	}{
		{
			name: "shadowsocks",
			inbound: `{
				"type": "shadowsocks", "tag": "in", "listen": "127.0.0.1", "listen_port": %d,
				"method": "aes-256-gcm", "password": "correct-pw"
			}`,
			node: `{"type":"shadowsocks","server":"127.0.0.1","server_port":%d,"method":"aes-256-gcm","password":"wrong-pw"}`,
		},
		{
			name: "trojan",
			inbound: `{
				"type": "trojan", "tag": "in", "listen": "127.0.0.1", "listen_port": %d,
				"users": [{"password": "correct-trojan-pw"}]
			}`,
			node: `{"type":"trojan","server":"127.0.0.1","server_port":%d,"password":"wrong-trojan-pw"}`,
		},
		{
			name: "socks5",
			inbound: `{
				"type": "socks", "tag": "in", "listen": "127.0.0.1", "listen_port": %d,
				"users": [{"username": "u", "password": "correct-socks-pw"}]
			}`,
			node: `{"type":"socks","server":"127.0.0.1","server_port":%d,"version":"5","username":"u","password":"wrong-socks-pw"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := startPeer(t, tc.inbound)
			target := startTarget(t)

			nodeJSON := fmt.Sprintf(tc.node, port)
			ob, err := builder.Build([]byte(nodeJSON))
			if err != nil {
				t.Fatalf("Build(%s): %v", nodeJSON, err)
			}
			defer closeOutbound(t, ob, tc.name)

			dialCtx, cancel := context.WithTimeout(context.Background(), dialTimeout)
			defer cancel()
			conn, err := ob.DialContext(dialCtx, "tcp",
				M.SocksaddrFrom(netip.MustParseAddr("127.0.0.1"), target.port))
			if err != nil {
				// The handshake was refused: the expected outcome.
				return
			}
			defer conn.Close()

			// Some protocols only reject after the first write, so read and
			// require that no target body comes back.
			_, _ = conn.Write([]byte("GET / HTTP/1.0\r\nHost: localhost\r\n\r\n"))
			_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
			if response := readTunnelResponse(conn); strings.Contains(response, targetBody) {
				t.Fatalf("%s with wrong credentials returned the target body; the node was bypassed",
					tc.name)
			}
		})
	}
}

// readTunnelResponse drains the tunnel until the peer closes it or the deadline
// passes, and returns whatever arrived.
//
// A close that arrives before the read finishes is not an error here: anytls
// tears its stream down as soon as the response is written, so a strict
// io.ReadAll would report "use of closed network connection" even though the body
// was delivered. What matters is the bytes, not the close.
func readTunnelResponse(conn net.Conn) string {
	var out []byte
	buf := make([]byte, 1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil {
			return string(out)
		}
		if len(out) >= 4096 {
			return string(out)
		}
	}
}

// closeOutbound closes a built node handle when it is closable, and fails on
// error. adapter.Outbound is not itself an io.Closer, so the assertion decides.
func closeOutbound(t *testing.T, ob adapter.Outbound, label string) {
	t.Helper()
	if ob == nil {
		return
	}
	closer, ok := ob.(io.Closer)
	if !ok {
		return
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("Close(%s): %v", label, err)
	}
}

const (
	dialTimeout = 15 * time.Second
	readTimeout = 10 * time.Second
)
