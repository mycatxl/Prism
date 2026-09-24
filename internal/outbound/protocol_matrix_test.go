package outbound

import (
	"context"
	"encoding/json"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"

	"prism/internal/node"
	"prism/internal/subscription"
)

// matrixCase describes one protocol-matrix row (WP06 §11).
type matrixCase struct {
	name string
	// input is a subscription payload; fixture names a
	// internal/subscription/testdata file; direct is a node document. Exactly
	// one of them must be set.
	input   string
	fixture string
	direct  json.RawMessage

	wantEngine string
	wantKind   string // outbound|endpoint|chain
	wantProto  string
	wantNodes  int
	// notParsed marks inputs the parser must not import (delegated to WP07 or
	// reported as invalid).
	notParsed bool
	// wantBuild is an error substring; empty means Build must succeed.
	wantBuild string
	// wantDial dials 127.0.0.1:1 and asserts the failure is not a detour
	// resolution failure (§6.5).
	wantDial bool
}

// loadOpenVPNFixture reads one .ovpn/.json fixture from the subscription testdata.
func loadOpenVPNFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "subscription", "testdata", "openvpn", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

func matrixDocuments(t *testing.T, tc matrixCase) []json.RawMessage {
	t.Helper()
	if len(tc.direct) > 0 {
		return []json.RawMessage{tc.direct}
	}
	payload := []byte(tc.input)
	if tc.fixture != "" {
		payload = loadOpenVPNFixture(t, tc.fixture)
	}
	nodes, err := subscription.ParseGeneralSubscription(payload)
	if err != nil {
		if tc.notParsed {
			return nil
		}
		t.Fatalf("ParseGeneralSubscription: %v", err)
	}
	if len(nodes) == 0 {
		if tc.notParsed {
			return nil
		}
		t.Fatalf("no nodes parsed from %s", tc.name)
	}
	want := tc.wantNodes
	if want == 0 {
		want = 1
	}
	if len(nodes) != want {
		t.Fatalf("parsed nodes: got %d, want %d", len(nodes), want)
	}
	documents := make([]json.RawMessage, 0, len(nodes))
	for _, parsed := range nodes {
		documents = append(documents, parsed.RawOptions)
	}
	return documents
}

func TestProtocolMatrix(t *testing.T) {
	b := newTestSingboxBuilder(t)
	defer b.Close()

	for _, tc := range protocolMatrixCases() {
		t.Run(tc.name, func(t *testing.T) {
			documents := matrixDocuments(t, tc)
			// For a multi-node input only the last document is the node under
			// test; the others are support nodes that must still build.
			target := len(documents) - 1
			for index, raw := range documents {
				doc, err := node.ParseNodeDoc(raw)
				if err != nil {
					t.Fatalf("ParseNodeDoc: %v", err)
				}
				ob, buildErr := b.Build(raw)
				if index != target {
					if buildErr != nil {
						t.Fatalf("Build(support node %d): %v", index, buildErr)
					}
					closeMatrixOutbound(t, ob, doc.Type)
					continue
				}
				if tc.wantEngine != "" && doc.Engine != tc.wantEngine {
					t.Fatalf("engine: got %q, want %q", doc.Engine, tc.wantEngine)
				}
				if tc.wantKind != "" && doc.Kind.String() != tc.wantKind {
					t.Fatalf("kind: got %q, want %q", doc.Kind, tc.wantKind)
				}
				if tc.wantProto != "" && doc.Type != tc.wantProto {
					t.Fatalf("protocol: got %q, want %q", doc.Type, tc.wantProto)
				}

				if tc.wantBuild != "" {
					if buildErr == nil {
						t.Fatalf("Build(%s): expected error containing %q", tc.wantProto, tc.wantBuild)
					}
					if !strings.Contains(buildErr.Error(), tc.wantBuild) {
						t.Fatalf("Build(%s) error %q does not contain %q", tc.wantProto, buildErr, tc.wantBuild)
					}
					continue
				}
				if buildErr != nil {
					t.Fatalf("Build(%s): %v", tc.wantProto, buildErr)
				}
				if ob == nil {
					t.Fatalf("Build(%s) returned nil outbound", tc.wantProto)
				}
				if tc.wantDial {
					assertDetourResolves(t, ob)
				}
				closeMatrixOutbound(t, ob, tc.wantProto)
			}
		})
	}
}

// closeMatrixOutbound closes a built node handle and fails on error.
func closeMatrixOutbound(t *testing.T, ob adapter.Outbound, label string) {
	t.Helper()
	if ob == nil {
		return
	}
	if closer, ok := ob.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			t.Fatalf("Close(%s): %v", label, err)
		}
	}
}

// assertDetourResolves checks §6.5: dialing an unreachable target must fail for
// a transport reason, never with "outbound detour not found" (fact F7).
func assertDetourResolves(t *testing.T, ob adapter.Outbound) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), dialProbeTimeout)
	defer cancel()
	conn, err := ob.DialContext(ctx, "tcp", M.SocksaddrFrom(netip.MustParseAddr("127.0.0.1"), 1))
	if err == nil {
		_ = conn.Close()
		return
	}
	if strings.Contains(err.Error(), "detour not found") {
		t.Fatalf("dial: %v", err)
	}
}

func protocolMatrixCases() []matrixCase {
	cases := []matrixCase{
		// --- shadowsocks -------------------------------------------------------
		{
			name:       "ss-aead",
			input:      "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ=@1.2.3.4:8388#ss-aead",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "shadowsocks",
		},
		{
			name:       "ss-2022-percent-encoded",
			input:      "ss://2022-blake3-aes-128-gcm:AAAAAAAAAAAAAAAAAAAAAA%3D%3D@1.2.3.4:8388#ss-2022",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "shadowsocks",
		},
		{
			name:       "ss-2022-base64-userinfo",
			input:      "ss://MjAyMi1ibGFrZTMtYWVzLTEyOC1nY206QUFBQUFBQUFBQUFBQUFBQUFBQUFBQT09@1.2.3.4:8388#ss-2022b",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "shadowsocks",
		},
		{
			name:       "ss-sip002-obfs-plugin",
			input:      "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ=@1.2.3.4:8388/?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dexample.com#ss-obfs",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "shadowsocks",
		},
		{
			name:       "ss-sip002-v2ray-plugin",
			input:      "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ=@1.2.3.4:8388/?plugin=v2ray-plugin%3Bmode%3Dwebsocket%3Bhost%3Dexample.com#ss-v2ray",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "shadowsocks",
		},

		// --- vmess / vless / trojan / hysteria2 --------------------------------
		{
			name:       "vmess-ws-tls",
			input:      "vmess://eyJ2IjoiMiIsInBzIjoidm1lc3Mtd3MiLCJhZGQiOiIxLjIuMy40IiwicG9ydCI6IjQ0MyIsImlkIjoiMTExMTExMTEtMjIyMi0zMzMzLTQ0NDQtNTU1NTU1NTU1NTU1IiwiYWlkIjoiMCIsIm5ldCI6IndzIiwidHlwZSI6Im5vbmUiLCJob3N0IjoiZXhhbXBsZS5jb20iLCJwYXRoIjoiL3dzIiwidGxzIjoidGxzIn0=",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "vmess",
		},
		{
			name:       "vmess-grpc",
			input:      "vmess://eyJ2IjoiMiIsInBzIjoidm1lc3MtZ3JwYyIsImFkZCI6IjEuMi4zLjQiLCJwb3J0IjoiNDQzIiwiaWQiOiIxMTExMTExMS0yMjIyLTMzMzMtNDQ0NC01NTU1NTU1NTU1NTUiLCJhaWQiOiIwIiwibmV0IjoiZ3JwYyIsInR5cGUiOiJub25lIiwiaG9zdCI6ImV4YW1wbGUuY29tIiwicGF0aCI6ImdycGNzdmMiLCJ0bHMiOiJ0bHMifQ==",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "vmess",
		},
		{
			name:       "vless-reality-vision",
			input:      "vless://11111111-2222-3333-4444-555555555555@1.2.3.4:443?encryption=none&security=reality&sni=example.com&fp=chrome&pbk=ISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0-P0A&sid=ab12&flow=xtls-rprx-vision&type=tcp#vless-reality",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "vless",
		},
		{
			name:       "vless-ws",
			input:      "vless://11111111-2222-3333-4444-555555555555@1.2.3.4:443?encryption=none&security=tls&type=ws&path=%2Fws&host=example.com&sni=example.com#vless-ws",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "vless",
		},
		{
			name:       "vless-grpc",
			input:      "vless://11111111-2222-3333-4444-555555555555@1.2.3.4:443?encryption=none&security=tls&type=grpc&serviceName=grpcsvc&sni=example.com#vless-grpc",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "vless",
		},
		{
			name:       "vless-httpupgrade",
			input:      "vless://11111111-2222-3333-4444-555555555555@1.2.3.4:443?encryption=none&security=tls&type=httpupgrade&path=%2Fup&host=example.com&sni=example.com#vless-upgrade",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "vless",
		},
		{
			name:      "vless-xhttp-deferred",
			input:     "vless://11111111-2222-3333-4444-555555555555@1.2.3.4:443?encryption=none&security=tls&type=xhttp&path=%2Fx&sni=example.com#vless-xhttp",
			notParsed: true,
		},
		{
			name:      "vless-encryption-deferred",
			input:     "vless://11111111-2222-3333-4444-555555555555@1.2.3.4:443?encryption=mlkem768x25519plus.native.600s&security=tls&sni=example.com#vless-enc",
			notParsed: true,
		},
		{
			name:       "trojan-ws",
			input:      "trojan://password@1.2.3.4:443?type=ws&path=%2Fws&host=example.com&sni=example.com#trojan-ws",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "trojan",
		},
		{
			name:       "hysteria2-salamander",
			input:      "hysteria2://password@1.2.3.4:443?obfs=salamander&obfs-password=xyz&sni=example.com#hy2-salamander",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "hysteria2",
		},
		{
			name:       "hysteria2-port-hopping",
			input:      "hysteria2://password@1.2.3.4:443?mport=20000-30000&sni=example.com#hy2-hop",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "hysteria2",
		},

		// --- WP06 §8 share links ----------------------------------------------
		{
			name:       "tuic-uri",
			input:      "tuic://11111111-2222-3333-4444-555555555555:password@1.2.3.4:443?congestion_control=bbr&udp_relay_mode=native&sni=example.com&alpn=h3#tuic",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "tuic",
		},
		{
			name:       "hysteria-v1-uri",
			input:      "hysteria://1.2.3.4:443?protocol=udp&auth=pw&peer=example.com&insecure=1&upmbps=30&downmbps=200&alpn=h3&obfs=xplus&obfsParam=example.com#hy1",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "hysteria",
		},
		{
			name:       "anytls-uri",
			input:      "anytls://password@1.2.3.4:443?sni=example.com&insecure=1&fp=chrome#anytls",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "anytls",
		},
		{
			name:       "ssh-uri",
			input:      "ssh://root:password@1.2.3.4:22#ssh",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "ssh",
		},
		{
			name:       "tuic-clash",
			input:      `{"proxies":[{"name":"tuic-clash","type":"tuic","server":"1.2.3.4","port":443,"uuid":"11111111-2222-3333-4444-555555555555","password":"pw","sni":"example.com","skip-cert-verify":true}]}`,
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "tuic",
		},
		{
			name:       "hysteria-v1-clash",
			input:      `{"proxies":[{"name":"hy1-clash","type":"hysteria","server":"1.2.3.4","port":443,"auth-str":"pw","up":"30","down":"200","sni":"example.com","skip-cert-verify":true,"obfs":"xplus"}]}`,
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "hysteria",
		},
		{
			name:       "anytls-clash",
			input:      `{"proxies":[{"name":"anytls-clash","type":"anytls","server":"1.2.3.4","port":443,"password":"pw","sni":"example.com","skip-cert-verify":true}]}`,
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "anytls",
		},
		{
			name:       "ssh-clash",
			input:      `{"proxies":[{"name":"ssh-clash","type":"ssh","server":"1.2.3.4","port":22,"username":"root","password":"pw"}]}`,
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "ssh",
		},

		// --- plain proxies -----------------------------------------------------
		{
			name:       "socks5-uri",
			input:      "socks5://user:pass@1.2.3.4:1080#socks5",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "socks",
		},
		{
			name:       "http-uri",
			input:      "http://user:pass@1.2.3.4:8080#http-proxy",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "http",
		},
		{
			name:       "https-uri",
			input:      "https://user:pass@1.2.3.4:8443#https-proxy",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "http",
		},
		{
			name:       "plain-ip-port-user-pass",
			input:      "1.2.3.4:8080:user:pass",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "http",
		},

		// --- WireGuard: the five sources (§3) ---------------------------------
		{
			name:       "wireguard-legacy-outbound",
			input:      `{"outbounds":[` + wireGuardLegacyOptions + `]}`,
			wantEngine: node.EngineSingbox, wantKind: "endpoint", wantProto: "wireguard",
		},
		{
			name:       "wireguard-endpoint-array",
			input:      `{"endpoints":[` + wireGuardEndpointObjectJSON + `]}`,
			wantEngine: node.EngineSingbox, wantKind: "endpoint", wantProto: "wireguard",
		},
		{
			name:       "wireguard-clash",
			input:      `{"proxies":[` + wireGuardClashProxyJSON + `]}`,
			wantEngine: node.EngineSingbox, wantKind: "endpoint", wantProto: "wireguard",
		},
		{
			name:       "wireguard-surge",
			input:      "[Proxy]\n" + wireGuardSurgeLine + "\n",
			wantEngine: node.EngineSingbox, wantKind: "endpoint", wantProto: "wireguard",
		},
		{
			name:       "wireguard-uri",
			input:      wireGuardShareLink,
			wantEngine: node.EngineSingbox, wantKind: "endpoint", wantProto: "wireguard",
		},

		// --- detour chains (§6) ------------------------------------------------
		{
			name: "chain-singbox-json",
			input: `{"outbounds":[
				{"type":"shadowsocks","tag":"ss-chain","server":"127.0.0.1","server_port":1,"method":"aes-256-gcm","password":"pw","detour":"stls"},
				{"type":"shadowtls","tag":"stls","server":"127.0.0.1","server_port":1,"version":3,"password":"stls-pw","tls":{"enabled":true,"server_name":"example.com"}}
			]}`,
			wantEngine: node.EngineSingbox, wantKind: "chain", wantProto: "shadowsocks",
			wantDial: true,
		},
		{
			name: "chain-clash-shadow-tls-plugin",
			input: `{"proxies":[{"name":"ss-stls","type":"ss","server":"127.0.0.1","port":1,"cipher":"aes-256-gcm","password":"pw",` +
				`"plugin":"shadow-tls","plugin-opts":{"host":"example.com","password":"stls-pw","version":"3"}}]}`,
			wantEngine: node.EngineSingbox, wantKind: "chain", wantProto: "shadowsocks",
			wantDial: true,
		},
		{
			name: "chain-clash-dialer-proxy",
			input: `{"proxies":[
				{"name":"front","type":"socks5","server":"127.0.0.1","port":1},
				{"name":"back","type":"vmess","server":"127.0.0.1","port":1,"uuid":"11111111-2222-3333-4444-555555555555","dialer-proxy":"front"}
			]}`,
			wantEngine: node.EngineSingbox, wantKind: "chain", wantProto: "vmess",
			wantNodes: 2, wantDial: true,
		},

		// --- Snell (§7) --------------------------------------------------------
		{
			name:       "snell-clash-v4",
			input:      `{"proxies":[{"name":"snell4","type":"snell","server":"1.2.3.4","port":443,"psk":"psk-value","version":4,"obfs-opts":{"mode":"http","host":"example.com"}}]}`,
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "snell",
		},
		{
			name:       "snell-surge-v4",
			input:      "[Proxy]\nsnell-jp = snell, 1.2.3.4, 443, psk=psk-value, version=4, obfs=http, obfs-host=example.com\n",
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "snell",
		},
		{
			name:      "snell-clash-v3-deferred",
			input:     `{"proxies":[{"name":"snell3","type":"snell","server":"1.2.3.4","port":443,"psk":"psk-value","version":3}]}`,
			notParsed: true,
		},

		// --- OpenVPN .ovpn profiles (§4) -------------------------------------
		{
			name:       "ovpn-tls-crypt",
			fixture:    "tls-crypt.ovpn",
			wantEngine: node.EngineSingbox, wantKind: "endpoint", wantProto: "openvpn-client",
		},
		{
			name:       "ovpn-tls-auth-key-direction",
			fixture:    "tls-auth-direction.ovpn",
			wantEngine: node.EngineSingbox, wantKind: "endpoint", wantProto: "openvpn-client",
		},
		{
			name:       "ovpn-multi-remote-random",
			fixture:    "multi-remote-random.ovpn",
			wantEngine: node.EngineSingbox, wantKind: "endpoint", wantProto: "openvpn-client",
		},
		{
			name:       "ovpn-tcp-client",
			fixture:    "tcp-client.ovpn",
			wantEngine: node.EngineSingbox, wantKind: "endpoint", wantProto: "openvpn-client",
		},
		{
			name:       "ovpn-static-key",
			fixture:    "static-key.ovpn",
			wantEngine: node.EngineSingbox, wantKind: "endpoint", wantProto: "openvpn-client",
		},
		{
			name:       "ovpn-verify-x509-name-peer-fingerprint",
			fixture:    "verify-x509-name.ovpn",
			wantEngine: node.EngineSingbox, wantKind: "endpoint", wantProto: "openvpn-client",
		},
		{
			name:       "ovpn-inline-credentials",
			fixture:    "inline-credentials.ovpn",
			wantEngine: node.EngineSingbox, wantKind: "endpoint", wantProto: "openvpn-client",
		},
		{
			name:       "ovpn-bundle",
			fixture:    "bundle.json",
			wantEngine: node.EngineSingbox, wantKind: "endpoint", wantProto: "openvpn-client",
			wantNodes: 2,
		},
		{
			name:      "ovpn-reject-dev-tap",
			fixture:   "reject-dev-tap.ovpn",
			notParsed: true,
		},
		{
			name:      "ovpn-reject-file-path",
			fixture:   "reject-file-path.ovpn",
			notParsed: true,
		},
		{
			name:      "ovpn-reject-missing-credentials",
			fixture:   "reject-missing-credentials.ovpn",
			notParsed: true,
		},

		// --- delegated to WP07 / not built into this binary --------------------
		{
			name:      "ssr-clash-deferred",
			input:     `{"proxies":[{"name":"ssr","type":"ssr","server":"1.2.3.4","port":443,"cipher":"aes-256-cfb","password":"pw","obfs":"plain","protocol":"origin"}]}`,
			notParsed: true,
		},
		{
			name:      "mieru-clash-deferred",
			input:     `{"proxies":[{"name":"mieru","type":"mieru","server":"1.2.3.4","port":443,"username":"u","password":"p","transport":"tcp"}]}`,
			notParsed: true,
		},
		{
			name:      "wireguard-amnezia-deferred",
			input:     `{"proxies":[{"name":"wg-az","type":"wireguard","server":"1.2.3.4","port":51820,"private-key":"AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=","public-key":"ISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+P0A=","ip":"10.0.0.2/32","amnezia-wg-option":{"jc":3}}]}`,
			notParsed: true,
		},
		{
			name:      "tailscale-endpoint-not-built",
			input:     `{"endpoints":[{"type":"tailscale","tag":"ts","auth_key":"tskey"}]}`,
			notParsed: true,
		},
		{
			name:       "naive-not-built",
			input:      `{"outbounds":[{"type":"naive","tag":"naive","server":"1.2.3.4","server_port":443}]}`,
			wantEngine: node.EngineSingbox, wantKind: "outbound", wantProto: "naive",
			wantBuild: "not included in this build",
		},
		{
			name:      "invalid-uri",
			input:     "ss://\nnot a proxy line at all\n",
			notParsed: true,
		},
	}

	// A mihomo envelope is rejected by the sing-box runtime. WP07 owns the built
	// path; until then both builds report ENGINE_NOT_BUILT.
	cases = append(cases, matrixCase{
		name:       "mihomo-proxy-envelope",
		direct:     json.RawMessage(`{"prism_node":1,"engine":"mihomo","kind":"proxy","name":"ssr-hk","proxy":{"type":"ssr","server":"1.2.3.4","port":443,"cipher":"aes-256-cfb","password":"pw","obfs":"plain","protocol":"origin"}}`),
		wantEngine: node.EngineMihomo, wantKind: "proxy", wantProto: "ssr",
		wantBuild: "ENGINE_NOT_BUILT",
	})

	// A tailscale endpoint envelope is refused by the runtime.
	cases = append(cases, matrixCase{
		name:       "tailscale-endpoint-envelope-not-built",
		direct:     json.RawMessage(`{"prism_node":1,"engine":"singbox","kind":"endpoint","name":"ts","main":{"type":"tailscale","auth_key":"tskey"}}`),
		wantEngine: node.EngineSingbox, wantKind: "endpoint", wantProto: "tailscale",
		wantBuild: "tailscale",
	})

	return cases
}

// TestProtocolMatrixCapabilities checks that every declared capability exists in
// the embedded sing-box registries (WP06 §10).
func TestProtocolMatrixCapabilities(t *testing.T) {
	b := newTestSingboxBuilder(t)
	defer b.Close()

	ctx := b.Runtime().ctx
	outboundRegistry := service.FromContext[adapter.OutboundRegistry](ctx)
	if outboundRegistry == nil {
		t.Fatal("missing outbound registry")
	}
	for _, typeName := range node.SingboxOutboundTypes() {
		if _, ok := outboundRegistry.CreateOptions(typeName); !ok {
			t.Errorf("sing-box has no outbound type %q", typeName)
		}
	}
	endpointRegistry := service.FromContext[adapter.EndpointRegistry](ctx)
	if endpointRegistry == nil {
		t.Fatal("missing endpoint registry")
	}
	for _, typeName := range node.EndpointTypes() {
		if _, ok := endpointRegistry.CreateOptions(typeName); !ok {
			t.Errorf("sing-box has no endpoint type %q", typeName)
		}
	}
}
