package e2e

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
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
	startPeerFixture(t, peerFixture{
		inbounds: []string{fmt.Sprintf(inboundTemplate, port)},
	})
	return port
}

// peerFixture is the local sing-box side of one offline case: the listeners the
// tunnel connects to, and the node document the tunnel is built from. Both are
// already rendered, because a case that needs several listeners or a helper
// server only learns its ports while the test runs.
type peerFixture struct {
	// inbounds are inbound JSON objects to serve.
	inbounds []string
	// endpoints are endpoint JSON objects to serve. A protocol whose server
	// side is an endpoint rather than an inbound needs this: WireGuard accepts
	// a peer instead of proxying connections.
	endpoints []string
	// node is the Prism node document the tunnel is built from.
	node string
	// dialAddress is the address the tunnel dials the target on, for a protocol
	// whose peer maps its own tunnel subnet onto loopback. Empty means
	// 127.0.0.1.
	dialAddress string
	// connectAsync marks a protocol whose endpoint establishes its tunnel in
	// the background after it is built: the first dials fail with "endpoint is
	// not ready yet" until the handshake finished. The test then retries the
	// dial within dialTimeout instead of failing on the first attempt. The
	// assertion is unchanged: the target body must still come back.
	connectAsync bool
}

// startPeerFixture starts a sing-box instance serving the fixture's listeners.
func startPeerFixture(t *testing.T, fixture peerFixture) {
	t.Helper()
	if len(fixture.inbounds) == 0 && len(fixture.endpoints) == 0 {
		t.Fatal("peer fixture serves no listener")
	}
	ctx := include.Context(context.Background())

	inbounds := make([]option.Inbound, 0, len(fixture.inbounds))
	for _, raw := range fixture.inbounds {
		inbounds = append(inbounds, decodeInbound(t, ctx, raw))
	}
	endpoints := make([]option.Endpoint, 0, len(fixture.endpoints))
	for _, raw := range fixture.endpoints {
		var endpoint option.Endpoint
		if err := singjson.UnmarshalContext(ctx, []byte(raw), &endpoint); err != nil {
			t.Fatalf("unmarshal endpoint %s: %v", raw, err)
		}
		if endpoint.Options == nil {
			t.Fatalf("endpoint %s produced nil options; the context registry was not consulted", raw)
		}
		endpoints = append(endpoints, endpoint)
	}

	instance, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{
			Log:       &option.LogOptions{Disabled: true},
			Inbounds:  inbounds,
			Endpoints: endpoints,
		},
	})
	if err != nil {
		t.Fatalf("box.New(inbounds=%v endpoints=%v): %v", fixture.inbounds, fixture.endpoints, err)
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		t.Fatalf("instance.Start(inbounds=%v endpoints=%v): %v", fixture.inbounds, fixture.endpoints, err)
	}
	t.Cleanup(func() { _ = instance.Close() })
}

// decodeInbound parses one rendered inbound object with sing's context-aware
// unmarshaller and refuses a parse that produced no options.
func decodeInbound(t *testing.T, ctx context.Context, raw string) option.Inbound {
	t.Helper()
	var inbound option.Inbound
	if err := singjson.UnmarshalContext(ctx, []byte(raw), &inbound); err != nil {
		t.Fatalf("unmarshal inbound %s: %v", raw, err)
	}
	if inbound.Options == nil {
		t.Fatalf("inbound %s produced nil options; the context registry was not consulted", raw)
	}
	return inbound
}

// startHandshakeServer starts a loopback TLS server. A shadowtls inbound relays
// the TLS handshake of an incoming client to a real TLS server, so it needs a
// genuine one; this is the local stand-in.
func startHandshakeServer(t *testing.T) uint16 {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	// The tunnel only performs a TLS handshake against it, so the server's
	// request and error logs are noise.
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	return uint16(server.Listener.Addr().(*net.TCPAddr).Port)
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
	// setup replaces inbound and node for a case that needs several listeners, a
	// helper server, or an endpoint instead of an inbound. It exists because
	// such a case only learns its ports while the test runs.
	setup func(t *testing.T) peerFixture
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
		{
			// hysteria2 runs over QUIC: the inbound listens on UDP. The port
			// freeLoopbackPort reserved was only probed on TCP, so the case
			// proves the tunnel carries data rather than trusting the port.
			// The password is the inbound user's password, and both sides
			// negotiate the h3 ALPN that the hysteria2 service installs.
			name: "hysteria2-quic",
			inbound: fmt.Sprintf(`{
				"type": "hysteria2", "tag": "in", "listen": "127.0.0.1", "listen_port": %%d,
				"users": [{"name": "offline", "password": "offline-hy2-pw"}],
				"tls": {"enabled": true, "certificate": [%s], "key": [%s]}
			}`, cert, key),
			node: `{"type":"hysteria2","server":"127.0.0.1","server_port":%d,"password":"offline-hy2-pw","tls":{"enabled":true,"insecure":true}}`,
		},
		{
			// tuic is the second QUIC protocol: it authenticates with uuid plus
			// password instead of a single password.
			name: "tuic-quic",
			inbound: fmt.Sprintf(`{
				"type": "tuic", "tag": "in", "listen": "127.0.0.1", "listen_port": %%d,
				"users": [{"name": "offline", "uuid": "11111111-2222-3333-4444-555555555555", "password": "offline-tuic-pw"}],
				"tls": {"enabled": true, "certificate": [%s], "key": [%s]}
			}`, cert, key),
			node: `{"type":"tuic","server":"127.0.0.1","server_port":%d,"uuid":"11111111-2222-3333-4444-555555555555","password":"offline-tuic-pw","tls":{"enabled":true,"insecure":true}}`,
		},
		{
			// shadowtls wraps a shadowsocks session in a genuine TLS handshake.
			// Its inbound relays the client's handshake to a real TLS server,
			// then injects the authenticated stream into the inbound its
			// `detour` names. Both inbounds therefore live in one instance and
			// the node is a chain: main = shadowsocks, deps[0] = shadowtls. The
			// shadowsocks server address is the shadowtls listener, because that
			// is the address the shadowsocks stream is carried to.
			name: "shadowtls-v3-shadowsocks-chain",
			setup: func(t *testing.T) peerFixture {
				t.Helper()
				handshakePort := startHandshakeServer(t)
				ssPort := freeLoopbackPort(t)
				shadowTLSPort := freeLoopbackPort(t)
				inbounds := []string{
					fmt.Sprintf(`{
						"type": "shadowsocks", "tag": "ss-in", "listen": "127.0.0.1", "listen_port": %d,
						"method": "aes-256-gcm", "password": "offline-chain-pw"
					}`, ssPort),
					fmt.Sprintf(`{
						"type": "shadowtls", "tag": "stls-in", "listen": "127.0.0.1", "listen_port": %d,
						"version": 3, "users": [{"name": "offline", "password": "offline-stls-pw"}],
						"handshake": {"server": "127.0.0.1", "server_port": %d},
						"detour": "ss-in"
					}`, shadowTLSPort, handshakePort),
				}
				node := fmt.Sprintf(`{
					"prism_node": 1, "engine": "singbox", "kind": "chain", "name": "shadowtls-ss",
					"main": {
						"type": "shadowsocks", "server": "127.0.0.1", "server_port": %d,
						"method": "aes-256-gcm", "password": "offline-chain-pw", "detour": "d0"
					},
					"deps": [{
						"type": "shadowtls", "tag": "d0", "server": "127.0.0.1", "server_port": %d,
						"version": 3, "password": "offline-stls-pw",
						"tls": {"enabled": true, "insecure": true}
					}]
				}`, shadowTLSPort, shadowTLSPort)
				return peerFixture{inbounds: inbounds, node: node}
			},
		},
		{
			// The WireGuard server side is an endpoint with a listen_port, and
			// the node is a WireGuard endpoint whose single peer points at it.
			name:  "wireguard-endpoint",
			setup: wireGuardFixture,
		},
		{
			// The OpenVPN server side is an endpoint too, and it terminates a
			// full TLS session: the node authenticates with the client
			// certificate its `tls.client_certificate` carries, signed by the
			// CA the server verifies against.
			name:  "openvpn-server-endpoint",
			setup: openVPNFixture,
		},
	}
}

// wireGuardFixture stands up both ends of a WireGuard tunnel.
//
// The server side is an endpoint with a listen_port, because WireGuard accepts a
// peer rather than proxying connections, and the node is an endpoint too. The
// tunnel carries the connection for its own subnet: the tunnel dials the
// server's tunnel address, and the server rewrites a destination inside its own
// address list to loopback (protocol/wireguard/endpoint.go NewConnectionEx)
// before routing it out of its default direct outbound to the target.
func wireGuardFixture(t *testing.T) peerFixture {
	t.Helper()
	serverKeys := newWireGuardKeyPair(t)
	clientKeys := newWireGuardKeyPair(t)
	serverPort := freeLoopbackPort(t)

	endpoint := fmt.Sprintf(`{
		"type": "wireguard", "tag": "wg-server",
		"address": ["%s/32"], "private_key": %s, "listen_port": %d,
		"peers": [{
			"public_key": %s, "allowed_ips": ["%s/32"]
		}]
	}`,
		wireGuardServerAddress, jsonStringLiteral(serverKeys.privateKey), serverPort,
		jsonStringLiteral(clientKeys.publicKey), wireGuardClientAddress)

	node := fmt.Sprintf(`{
		"prism_node": 1, "engine": "singbox", "kind": "endpoint", "name": "wg-offline",
		"main": {
			"type": "wireguard",
			"address": ["%s/32"], "private_key": %s,
			"peers": [{
				"address": "127.0.0.1", "port": %d, "public_key": %s,
				"allowed_ips": ["0.0.0.0/0", "::/0"]
			}]
		}
	}`,
		wireGuardClientAddress, jsonStringLiteral(clientKeys.privateKey), serverPort,
		jsonStringLiteral(serverKeys.publicKey))

	return peerFixture{
		endpoints:   []string{endpoint},
		node:        node,
		dialAddress: wireGuardServerAddress,
	}
}

// The two tunnel addresses of the WireGuard fixture. They are documentation
// addresses, so nothing on the host can collide with them.
const (
	wireGuardServerAddress = "10.66.0.1"
	wireGuardClientAddress = "10.66.0.2"
)

// The OpenVPN fixture's tunnel pool and server address, also documentation
// addresses. The server needs a usable host address inside its own pool, which
// is why the pool starts at 10.77.0.1 rather than at the network address.
const (
	openVPNServerAddress = "10.77.0.1"
	openVPNPool          = "10.77.0.1/24"
)

// openVPNFixture stands up both ends of an OpenVPN TLS tunnel.
//
// OpenVPN is the one protocol here whose server verifies a client certificate,
// so it needs a real chain: testdata/openvpn holds a self-signed CA plus a
// server and a client certificate, all committed, so the case needs no external
// tooling. The tunnel is TCP, which keeps the fixture off the UDP packet path,
// and the destination is the server's tunnel address: the server rewrites a
// destination inside its own address list to loopback
// (protocol/openvpn/endpoint.go newConnection) before routing it out of its
// default direct outbound to the target.
func openVPNFixture(t *testing.T) peerFixture {
	t.Helper()
	certificateAuthority := openVPNPEM(t, "ca.crt")
	serverPort := freeLoopbackPort(t)

	endpoint := fmt.Sprintf(`{
		"type": "openvpn-server", "tag": "ovpn-server",
		"listen": "127.0.0.1", "listen_port": %d,
		"network": "tcp", "mode": "tls",
		"address": ["%s"],
		"users": [{"username": "offline", "password": "offline-ovpn-pw"}],
		"tls": {
			"certificate": [%s], "key": [%s],
			"client_certificate": [%s],
			"verify_client_certificate": "require"
		},
		"data_ciphers": ["AES-256-GCM"],
		"auth": "SHA256"
	}`,
		serverPort,
		openVPNPool,
		openVPNPEM(t, "server.crt"), openVPNPEM(t, "server.key"),
		certificateAuthority)

	node := fmt.Sprintf(`{
		"prism_node": 1, "engine": "singbox", "kind": "endpoint", "name": "ovpn-offline",
		"main": {
			"type": "openvpn-client",
			"server": "127.0.0.1", "server_port": %d,
			"network": "tcp", "mode": "tls",
			"username": "offline", "password": "offline-ovpn-pw",
			"tls": {
				"certificate": [%s],
				"client_certificate": [%s],
				"client_key": [%s],
				"server_name": "offline-ovpn-server"
			}
		}
	}`,
		serverPort,
		certificateAuthority, openVPNPEM(t, "client.crt"), openVPNPEM(t, "client.key"))

	return peerFixture{
		endpoints:    []string{endpoint},
		node:         node,
		dialAddress:  openVPNServerAddress,
		connectAsync: true,
	}
}

// openVPNPEM reads one file of the committed OpenVPN certificate chain as a JSON
// string literal.
func openVPNPEM(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "openvpn", name))
	if err != nil {
		t.Fatalf("read openvpn fixture %s: %v", name, err)
	}
	return jsonStringLiteral(string(body))
}

// wireGuardKeyPair is one X25519 key pair in the base64 form WireGuard options
// carry. WireGuard clamps the private scalar, and clamping is idempotent, so the
// public key derived here is the one wireguard-go derives from the same bytes.
type wireGuardKeyPair struct {
	privateKey string
	publicKey  string
}

func newWireGuardKeyPair(t *testing.T) wireGuardKeyPair {
	t.Helper()
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate wireguard key: %v", err)
	}
	return wireGuardKeyPair{
		privateKey: base64.StdEncoding.EncodeToString(privateKey.Bytes()),
		publicKey:  base64.StdEncoding.EncodeToString(privateKey.PublicKey().Bytes()),
	}
}

// TestOfflineProtocolRoundTrip proves each protocol carries real traffic over
// loopback, with no external network involved.
func TestOfflineProtocolRoundTrip(t *testing.T) {
	builder := newBuilder(t)

	for _, tc := range offlineCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			fixture := peerFixture{node: tc.node}
			if tc.setup != nil {
				fixture = tc.setup(t)
				startPeerFixture(t, fixture)
			} else {
				port := startPeer(t, tc.inbound)
				fixture.node = fmt.Sprintf(tc.node, port)
			}
			target := startTarget(t)

			nodeJSON := fixture.node
			ob, err := builder.Build([]byte(nodeJSON))
			if err != nil {
				t.Fatalf("Build(%s): %v", nodeJSON, err)
			}
			defer closeOutbound(t, ob, tc.name)

			dialAddress := fixture.dialAddress
			if dialAddress == "" {
				dialAddress = "127.0.0.1"
			}
			dialCtx, cancel := context.WithTimeout(context.Background(), dialTimeout)
			defer cancel()
			conn, err := dialTunnel(dialCtx, ob, dialAddress, target.port, fixture.connectAsync)
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

// dialTunnel dials the target through the node. A protocol whose endpoint
// connects asynchronously is retried until the endpoint reports ready or the
// context expires; every other protocol is dialled once.
func dialTunnel(ctx context.Context, ob adapter.Outbound, address string, port uint16, retry bool) (net.Conn, error) {
	destination := M.SocksaddrFrom(netip.MustParseAddr(address), port)
	if !retry {
		return ob.DialContext(ctx, "tcp", destination)
	}
	var lastErr error
	for {
		conn, err := ob.DialContext(ctx, "tcp", destination)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, lastErr
		case <-time.After(100 * time.Millisecond):
		}
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
