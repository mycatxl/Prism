package export

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"prism/internal/subscription"
)

// This file is the unit-test suite of the reverse mapper in clashmap.go
// (sing-box outbound -> mihomo/Clash proxy). It has four parts:
//
//  1. one exhaustive protocol table: every supported outbound type is converted
//     and the emitted Clash map is compared field by field (a missing,
//     misspelled or extra key fails the test);
//  2. the refusal paths, the typed accessors and the option parsers;
//  3. round-trip tests that drive the real user-visible path
//     (Clash proxy -> subscription parser -> Export(mihomo) -> Clash proxy) with
//     the equivalence rules stated explicitly;
//  4. tests that pin behaviour this suite found lossy. Those carry a "Known
//     gap" note with the evidence, so fixing the mapper flips them loudly.

// --- helpers ---------------------------------------------------------------

// clashObject decodes a sing-box outbound literal the way the exporter sees it:
// encoding/json yields float64 for numbers and mapUint/mapString work from
// there.
func clashObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		t.Fatalf("decode sing-box object %q: %v", raw, err)
	}
	return object
}

// mustClashProxy converts one sing-box outbound literal and fails when the
// reverse mapper refuses it.
func mustClashProxy(t *testing.T, raw string) map[string]any {
	t.Helper()
	proxy, ok := clashProxyFromSingbox(clashObject(t, raw))
	if !ok {
		t.Fatalf("clashProxyFromSingbox refused %s", raw)
	}
	return proxy
}

// assertClashProxy compares the whole proxy map.
func assertClashProxy(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clash proxy mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

// assertField compares one field of an object, for the cases where only part of
// it is interesting.
func assertField(t *testing.T, where string, object map[string]any, key string, want any) {
	t.Helper()
	if !reflect.DeepEqual(object[key], want) {
		t.Fatalf("%s: %s = %#v, want %#v", where, key, object[key], want)
	}
}

// --- 1. protocol table -----------------------------------------------------

// TestClashProxyFromSingboxProtocols converts one outbound per supported
// protocol and pins every field of the resulting Clash proxy.
func TestClashProxyFromSingboxProtocols(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]any
	}{
		{
			name: "shadowsocks",
			raw:  `{"type":"shadowsocks","tag":"ss","server":"198.51.100.10","server_port":8388,"method":"aes-256-gcm","password":"ss-pw"}`,
			want: map[string]any{
				"type": "ss", "server": "198.51.100.10", "port": 8388,
				"cipher": "aes-256-gcm", "password": "ss-pw",
			},
		},
		{
			name: "shadowsocks type is matched case insensitively",
			raw:  `{"type":"Shadowsocks","server":"198.51.100.11","server_port":8388,"method":"chacha20-ietf-poly1305","password":"ss-pw"}`,
			want: map[string]any{
				"type": "ss", "server": "198.51.100.11", "port": 8388,
				"cipher": "chacha20-ietf-poly1305", "password": "ss-pw",
			},
		},
		{
			name: "shadowsocks v2ray-plugin",
			raw:  `{"type":"shadowsocks","server":"198.51.100.11","server_port":8388,"method":"aes-256-gcm","password":"ss-pw","plugin":"v2ray-plugin","plugin_opts":"tls;host=cdn.example.com;path=/ws"}`,
			want: map[string]any{
				"type": "ss", "server": "198.51.100.11", "port": 8388,
				"cipher": "aes-256-gcm", "password": "ss-pw",
				"plugin": "v2ray-plugin",
				"plugin-opts": map[string]any{
					"mode": "websocket", "tls": true, "host": "cdn.example.com", "path": "/ws",
				},
			},
		},
		{
			// sing-box spells the obfs parameters obfs=<mode>;obfs-host=<host>
			// (that is also what subscription.setSSPluginFromClash writes);
			// mihomo reads them back from plugin-opts.{mode,host}.
			name: "shadowsocks obfs plugin",
			raw:  `{"type":"shadowsocks","server":"198.51.100.12","server_port":8388,"method":"aes-256-gcm","password":"ss-pw","plugin":"obfs-local","plugin_opts":"obfs=tls;obfs-host=bing.com"}`,
			want: map[string]any{
				"type": "ss", "server": "198.51.100.12", "port": 8388,
				"cipher": "aes-256-gcm", "password": "ss-pw",
				"plugin": "obfs", "plugin-opts": map[string]any{"mode": "tls", "host": "bing.com"},
			},
		},
		{
			name: "shadowsocks unknown plugin stays plain",
			raw:  `{"type":"shadowsocks","server":"198.51.100.13","server_port":8388,"method":"aes-256-gcm","password":"ss-pw","plugin":"kcptun","plugin_opts":"mode=fast"}`,
			want: map[string]any{
				"type": "ss", "server": "198.51.100.13", "port": 8388,
				"cipher": "aes-256-gcm", "password": "ss-pw",
			},
		},
		{
			name: "vmess ws over tls",
			raw: `{"type":"vmess","server":"vmess.example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555","alter_id":0,"security":"auto",` +
				`"tls":{"enabled":true,"server_name":"vmess.example.com"},"transport":{"type":"ws","path":"/ws","headers":{"Host":"vmess.example.com"}}}`,
			want: map[string]any{
				"type": "vmess", "server": "vmess.example.com", "port": 443,
				"uuid": "11111111-2222-3333-4444-555555555555", "alterId": 0, "cipher": "auto",
				"tls": true, "sni": "vmess.example.com", "servername": "vmess.example.com",
				"network": "ws",
				"ws-opts": map[string]any{
					"path":    "/ws",
					"headers": map[string]any{"Host": "vmess.example.com"},
				},
			},
		},
		{
			name: "vmess defaults",
			raw:  `{"type":"vmess","server":"vmess.example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}`,
			want: map[string]any{
				"type": "vmess", "server": "vmess.example.com", "port": 443,
				"uuid": "11111111-2222-3333-4444-555555555555", "alterId": 0, "cipher": "auto",
			},
		},
		{
			name: "vmess alter_id is read from a string",
			raw:  `{"type":"vmess","server":"vmess.example.com","server_port":"443","uuid":"11111111-2222-3333-4444-555555555555","alter_id":"32","security":"chacha20-poly1305"}`,
			want: map[string]any{
				"type": "vmess", "server": "vmess.example.com", "port": 443,
				"uuid": "11111111-2222-3333-4444-555555555555", "alterId": 32, "cipher": "chacha20-poly1305",
			},
		},
		{
			name: "vless reality vision",
			raw: `{"type":"vless","server":"vless.example.com","server_port":443,"uuid":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","flow":"xtls-rprx-vision",` +
				`"tls":{"enabled":true,"server_name":"www.example.com","utls":{"enabled":true,"fingerprint":"chrome"},` +
				`"reality":{"enabled":true,"public_key":"PUBLIC-KEY","short_id":"0123abcd"},"alpn":["h2","http/1.1"]}}`,
			want: map[string]any{
				"type": "vless", "server": "vless.example.com", "port": 443,
				"uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "flow": "xtls-rprx-vision",
				"tls": true, "sni": "www.example.com", "servername": "www.example.com",
				"client-fingerprint": "chrome",
				"alpn":               []string{"h2", "http/1.1"},
				"reality-opts":       map[string]any{"public-key": "PUBLIC-KEY", "short-id": "0123abcd"},
			},
		},
		{
			name: "vless without flow omits the key",
			raw:  `{"type":"vless","server":"vless.example.com","server_port":443,"uuid":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","flow":"","tls":{"enabled":true}}`,
			want: map[string]any{
				"type": "vless", "server": "vless.example.com", "port": 443,
				"uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "tls": true,
			},
		},
		{
			name: "trojan grpc",
			raw: `{"type":"trojan","server":"trojan.example.org","server_port":8443,"password":"trojan-pw","tls":{"enabled":true,"server_name":"trojan.example.org"},` +
				`"transport":{"type":"grpc","service_name":"grpc-svc"}}`,
			want: map[string]any{
				"type": "trojan", "server": "trojan.example.org", "port": 8443, "password": "trojan-pw",
				"tls": true, "sni": "trojan.example.org", "servername": "trojan.example.org",
				"network":   "grpc",
				"grpc-opts": map[string]any{"grpc-service-name": "grpc-svc"},
			},
		},
		{
			name: "hysteria2 salamander obfs with port hopping and rates",
			raw: `{"type":"hysteria2","server":"hy2.example.net","server_port":443,"password":"hy2-pw",` +
				`"tls":{"enabled":true,"server_name":"hy2.example.net","insecure":true,"alpn":["h3"]},` +
				`"obfs":{"type":"salamander","password":"obfs-pw"},"server_ports":["8443","9443"],"up_mbps":100,"down_mbps":200}`,
			want: map[string]any{
				"type": "hysteria2", "server": "hy2.example.net", "port": 443, "password": "hy2-pw",
				"tls": true, "sni": "hy2.example.net", "servername": "hy2.example.net",
				"skip-cert-verify": true, "alpn": []string{"h3"},
				"obfs": "salamander", "obfs-password": "obfs-pw",
				"ports": "8443,9443", "up": 100, "down": 200,
			},
		},
		{
			name: "hysteria keeps an explicit rate unit",
			raw: `{"type":"hysteria","server":"hy.example.net","server_port":443,"auth_str":"hy-pw","obfs":"xplus",` +
				`"up_mbps":100,"down":"200 Mbps","tls":{"enabled":true,"server_name":"hy.example.net"}}`,
			want: map[string]any{
				"type": "hysteria", "server": "hy.example.net", "port": 443,
				"auth-str": "hy-pw", "obfs": "xplus", "up": "100", "down": "200 Mbps",
				"tls": true, "sni": "hy.example.net", "servername": "hy.example.net",
			},
		},
		{
			name: "hysteria accepts a scalar server_ports",
			raw:  `{"type":"hysteria","server":"hy.example.net","server_port":443,"auth_str":"hy-pw","server_ports":443}`,
			want: map[string]any{
				"type": "hysteria", "server": "hy.example.net", "port": 443,
				"auth-str": "hy-pw", "ports": "443",
			},
		},
		{
			name: "hysteria port list",
			raw:  `{"type":"hysteria","server":"hy.example.net","server_port":443,"auth_str":"hy-pw","server_ports":["8443","9443"]}`,
			want: map[string]any{
				"type": "hysteria", "server": "hy.example.net", "port": 443,
				"auth-str": "hy-pw", "ports": "8443,9443",
			},
		},
		{
			name: "tuic",
			raw: `{"type":"tuic","server":"tuic.example.org","server_port":443,"uuid":"bbbbbbbb-cccc-dddd-eeee-ffffffffffff","password":"tuic-pw",` +
				`"congestion_control":"bbr","udp_relay_mode":"native","tls":{"enabled":true,"server_name":"tuic.example.org","alpn":["h3"]}}`,
			want: map[string]any{
				"type": "tuic", "server": "tuic.example.org", "port": 443,
				"uuid": "bbbbbbbb-cccc-dddd-eeee-ffffffffffff", "password": "tuic-pw",
				"congestion-controller": "bbr", "udp-relay-mode": "native",
				"tls": true, "sni": "tuic.example.org", "servername": "tuic.example.org",
				"alpn": []string{"h3"},
			},
		},
		{
			name: "anytls",
			raw:  `{"type":"anytls","server":"anytls.example.com","server_port":443,"password":"anytls-pw","tls":{"enabled":true,"server_name":"anytls.example.com"}}`,
			want: map[string]any{
				"type": "anytls", "server": "anytls.example.com", "port": 443, "password": "anytls-pw",
				"tls": true, "sni": "anytls.example.com", "servername": "anytls.example.com",
			},
		},
		{
			name: "ssh",
			raw:  `{"type":"ssh","server":"ssh.example.com","server_port":22,"user":"root","password":"ssh-pw","private_key":"KEY","private_key_passphrase":"PP"}`,
			want: map[string]any{
				"type": "ssh", "server": "ssh.example.com", "port": 22,
				"username": "root", "password": "ssh-pw",
				"private-key": "KEY", "private-key-passphrase": "PP",
			},
		},
		{
			name: "socks with credentials",
			raw:  `{"type":"socks","server":"socks.example.com","server_port":1080,"username":"socks-user","password":"socks-pw"}`,
			want: map[string]any{
				"type": "socks5", "server": "socks.example.com", "port": 1080,
				"username": "socks-user", "password": "socks-pw",
			},
		},
		{
			name: "http over tls",
			raw:  `{"type":"HTTP","server":"http.example.com","server_port":8080,"username":"http-user","password":"http-pw","tls":{"enabled":true,"server_name":"http.example.com"}}`,
			want: map[string]any{
				"type": "http", "server": "http.example.com", "port": 8080,
				"username": "http-user", "password": "http-pw",
				"tls": true, "sni": "http.example.com", "servername": "http.example.com",
			},
		},
		{
			name: "snell v4 with obfs",
			raw:  `{"type":"snell","server":"snell.example.com","server_port":443,"psk":"snell-psk","version":4,"obfs_mode":"http","obfs_host":"cdn.example.com"}`,
			want: map[string]any{
				"type": "snell", "server": "snell.example.com", "port": 443,
				"psk": "snell-psk", "version": 4,
				"obfs-opts": map[string]any{"mode": "http", "host": "cdn.example.com"},
			},
		},
		{
			name: "wireguard endpoint",
			raw: `{"type":"wireguard","address":["10.7.0.2/32","2001:db8::2/128"],"private_key":"WG-PRIVATE","mtu":1420,` +
				`"peers":[{"address":"203.0.113.9","port":51820,"public_key":"WG-PUBLIC","pre_shared_key":"WG-PSK","reserved":[1,2,3],"allowed_ips":["0.0.0.0/0","::/0"]}]}`,
			want: map[string]any{
				"type": "wireguard", "server": "203.0.113.9", "port": 51820,
				"private-key": "WG-PRIVATE", "public-key": "WG-PUBLIC", "pre-shared-key": "WG-PSK",
				"reserved": []int{1, 2, 3}, "allowed-ips": []string{"0.0.0.0/0", "::/0"},
				"ip": "10.7.0.2/32", "ipv6": "2001:db8::2/128", "mtu": 1420,
			},
		},
		{
			name: "openvpn-client tls mode",
			raw: `{"type":"openvpn-client","servers":[{"server":"vpn.example.com","server_port":1194,"network":"tcp"}],` +
				`"tls":{"certificate":["CA-PEM","CA-PEM-2"],"client_certificate":["CERT-PEM"],"client_key":["KEY-PEM"],"cipher":"AES-256-GCM",` +
				`"control_wrap":{"key":["TA-KEY"],"type":"tls_crypt","direction":"client"}},` +
				`"data_ciphers":["AES-256-GCM","CHACHA20-POLY1305"],"data_ciphers_fallback":"AES-256-CBC","auth":"SHA256",` +
				`"username":"ovpn-user","password":"ovpn-pw"}`,
			want: map[string]any{
				"type": "openvpn", "server": "vpn.example.com", "port": 1194, "proto": "tcp",
				"ca": "CA-PEM\nCA-PEM-2", "cert": "CERT-PEM", "key": "KEY-PEM",
				"tls-crypt": "TA-KEY", "key-direction": "client",
				"cipher":       "AES-256-CBC",
				"data-ciphers": []string{"AES-256-GCM", "CHACHA20-POLY1305"},
				"auth":         "SHA256", "username": "ovpn-user", "password": "ovpn-pw",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertClashProxy(t, mustClashProxy(t, tc.raw), tc.want)
		})
	}
}

// --- 2. refusals, accessors, option parsing --------------------------------

// TestClashProxyFromSingboxRejects pins every refusal: the mapper must return
// ok == false (the caller then reports NOT_REPRESENTABLE) instead of emitting a
// proxy that misses its credentials.
func TestClashProxyFromSingboxRejects(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"unknown type", `{"type":"mieru","server":"1.2.3.4","server_port":443}`},
		{"missing type", `{"server":"1.2.3.4","server_port":443}`},
		{"empty type", `{"type":"","server":"1.2.3.4","server_port":443}`},
		{"shadowsocks without method", `{"type":"shadowsocks","server":"1.2.3.4","server_port":443,"password":"pw"}`},
		{"shadowsocks without password", `{"type":"shadowsocks","server":"1.2.3.4","server_port":443,"method":"aes-256-gcm"}`},
		{"vmess without uuid", `{"type":"vmess","server":"1.2.3.4","server_port":443}`},
		{"vless without uuid", `{"type":"vless","server":"1.2.3.4","server_port":443}`},
		{"trojan without password", `{"type":"trojan","server":"1.2.3.4","server_port":443}`},
		{"hysteria2 without password", `{"type":"hysteria2","server":"1.2.3.4","server_port":443}`},
		{"tuic without uuid", `{"type":"tuic","server":"1.2.3.4","server_port":443,"password":"pw"}`},
		{"anytls without password", `{"type":"anytls","server":"1.2.3.4","server_port":443}`},
		{"snell without psk", `{"type":"snell","server":"1.2.3.4","server_port":443,"version":4}`},
		{"snell version 5", `{"type":"snell","server":"1.2.3.4","server_port":443,"psk":"psk","version":5}`},
		{"snell version 6", `{"type":"snell","server":"1.2.3.4","server_port":443,"psk":"psk","version":6}`},
		{"snell version 3 as a string", `{"type":"snell","server":"1.2.3.4","server_port":443,"psk":"psk","version":"3"}`},
		{"wireguard without private key", `{"type":"wireguard","peers":[{"address":"1.2.3.4","port":51820,"public_key":"PUB"}]}`},
		{"wireguard without peers", `{"type":"wireguard","private_key":"PRIV"}`},
		{"wireguard without a peer port", `{"type":"wireguard","private_key":"PRIV","peers":[{"address":"1.2.3.4","public_key":"PUB"}]}`},
		{"wireguard without a peer public key", `{"type":"wireguard","private_key":"PRIV","peers":[{"address":"1.2.3.4","port":51820}]}`},
		{"wireguard host name endpoint", `{"type":"wireguard","private_key":"PRIV","peers":[{"address":"wg.example.com","port":51820,"public_key":"PUB"}]}`},
		{"wireguard invalid ipv4 literal", `{"type":"wireguard","private_key":"PRIV","peers":[{"address":"1.2.3.256","port":51820,"public_key":"PUB"}]}`},
		{"openvpn-client static_key mode", `{"type":"openvpn-client","mode":"static_key","static_key":["KEY"],"servers":[{"server":"vpn.example.com","server_port":1194}]}`},
		{"openvpn-client without servers", `{"type":"openvpn-client","tls":{"certificate":["CA"]}}`},
		{"openvpn-client without a server address", `{"type":"openvpn-client","servers":[{"server_port":1194}],"tls":{"certificate":["CA"]}}`},
		{"openvpn-client without tls", `{"type":"openvpn-client","servers":[{"server":"vpn.example.com","server_port":1194}]}`},
		{"openconnect without server", `{"type":"openconnect","server_port":4443}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if proxy, ok := clashProxyFromSingbox(clashObject(t, tc.raw)); ok {
				t.Fatalf("mapper accepted %s and produced %#v", tc.raw, proxy)
			}
		})
	}
}

// TestClashProxyFromSingboxNilObject checks a nil object cannot panic the
// dispatcher.
func TestClashProxyFromSingboxNilObject(t *testing.T) {
	if proxy, ok := clashProxyFromSingbox(nil); ok {
		t.Fatalf("nil object produced %#v", proxy)
	}
}

// TestClashBaseOptionalFields pins the shared server/port rendering.
func TestClashBaseOptionalFields(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]any
	}{
		{
			name: "no server and no port",
			raw:  `{"type":"socks"}`,
			want: map[string]any{"type": "socks5"},
		},
		{
			name: "port as a string",
			raw:  `{"type":"socks","server":"1.2.3.4","server_port":"1080"}`,
			want: map[string]any{"type": "socks5", "server": "1.2.3.4", "port": 1080},
		},
		{
			name: "zero port is omitted",
			raw:  `{"type":"socks","server":"1.2.3.4","server_port":0}`,
			want: map[string]any{"type": "socks5", "server": "1.2.3.4"},
		},
		{
			name: "negative port is omitted",
			raw:  `{"type":"socks","server":"1.2.3.4","server_port":-1}`,
			want: map[string]any{"type": "socks5", "server": "1.2.3.4"},
		},
		{
			name: "port above the uint16 range is emitted as is",
			raw:  `{"type":"socks","server":"1.2.3.4","server_port":70000}`,
			want: map[string]any{"type": "socks5", "server": "1.2.3.4", "port": 70000},
		},
		{
			name: "server is copied verbatim",
			raw:  `{"type":"socks","server":"  1.2.3.4  ","server_port":1080}`,
			want: map[string]any{"type": "socks5", "server": "  1.2.3.4  ", "port": 1080},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertClashProxy(t, mustClashProxy(t, tc.raw), tc.want)
		})
	}
}

// TestClashTypedAccessors pins the small typed accessors the mapper is built
// from, including the values it refuses to read.
func TestClashTypedAccessors(t *testing.T) {
	t.Run("mapString", func(t *testing.T) {
		object := map[string]any{
			"string": "value", "spaced": "  value  ", "number": float64(3.5),
			"true": true, "false": false, "nil": nil, "object": map[string]any{},
			"list": []any{"a"}, "goint": int(7),
		}
		for key, want := range map[string]string{
			"string": "value", "spaced": "  value  ", "number": "3.5",
			"true": "true", "false": "false", "nil": "", "object": "",
			"list": "", "goint": "", "missing": "",
		} {
			if got := mapString(object, key); got != want {
				t.Errorf("mapString(%s) = %q, want %q", key, got, want)
			}
		}
	})

	t.Run("mapUint", func(t *testing.T) {
		object := map[string]any{
			"float": float64(443), "fraction": float64(443.9), "negative": float64(-1),
			"string": " 443 ", "padding": "0443", "bad": "abc", "signed": "-1",
			"true": true, "nil": nil, "list": []any{1}, "goint": int(9), "huge": "99999999999999999999",
		}
		for key, want := range map[string]uint64{
			"float": 443, "fraction": 443, "negative": 0,
			"string": 443, "padding": 443, "bad": 0, "signed": 0,
			"true": 0, "nil": 0, "list": 0, "goint": 0, "huge": 0, "missing": 0,
		} {
			if got := mapUint(object, key); got != want {
				t.Errorf("mapUint(%s) = %d, want %d", key, got, want)
			}
		}
	})

	t.Run("mapUintSlice", func(t *testing.T) {
		object := map[string]any{
			"ok": []any{float64(1), float64(2), float64(3)}, "bad": []any{float64(1), "2"},
			"string": "1", "nil": nil, "empty": []any{}, "fraction": []any{float64(1.9)},
		}
		if got := mapUintSlice(object, "ok"); !reflect.DeepEqual(got, []int{1, 2, 3}) {
			t.Errorf("mapUintSlice(ok) = %#v", got)
		}
		if got := mapUintSlice(object, "fraction"); !reflect.DeepEqual(got, []int{1}) {
			t.Errorf("mapUintSlice(fraction) = %#v", got)
		}
		// One unreadable entry drops the whole list.
		for _, key := range []string{"bad", "string", "nil", "missing"} {
			if got := mapUintSlice(object, key); got != nil {
				t.Errorf("mapUintSlice(%s) = %#v, want nil", key, got)
			}
		}
		if got := mapUintSlice(object, "empty"); len(got) != 0 {
			t.Errorf("mapUintSlice(empty) = %#v", got)
		}
	})

	t.Run("mapStringSlice", func(t *testing.T) {
		object := map[string]any{
			"list": []any{"a", " b ", "", float64(3)}, "string": "single",
			"blank": "   ", "empty": []any{}, "number": float64(3), "nil": nil,
		}
		if got := mapStringSlice(object, "list"); !reflect.DeepEqual(got, []string{"a", " b "}) {
			t.Errorf("mapStringSlice(list) = %#v", got)
		}
		// A scalar keeps its own spacing; only a blank scalar counts as absent.
		if got := mapStringSlice(object, "string"); !reflect.DeepEqual(got, []string{"single"}) {
			t.Errorf("mapStringSlice(string) = %#v", got)
		}
		for _, key := range []string{"blank", "number", "nil", "missing"} {
			if got := mapStringSlice(object, key); got != nil {
				t.Errorf("mapStringSlice(%s) = %#v, want nil", key, got)
			}
		}
		if got := mapStringSlice(object, "empty"); len(got) != 0 {
			t.Errorf("mapStringSlice(empty) = %#v", got)
		}
	})

	t.Run("mapObject", func(t *testing.T) {
		object := map[string]any{"ok": map[string]any{"a": 1}, "string": "x", "nil": nil, "list": []any{}}
		if got := mapObject(object, "ok"); !reflect.DeepEqual(got, map[string]any{"a": 1}) {
			t.Errorf("mapObject(ok) = %#v", got)
		}
		for _, key := range []string{"string", "nil", "list", "missing"} {
			if got := mapObject(object, key); got != nil {
				t.Errorf("mapObject(%s) = %#v, want nil", key, got)
			}
		}
	})

	t.Run("mapObjectSlice", func(t *testing.T) {
		object := map[string]any{
			"ok":     []any{map[string]any{"a": 1}, map[string]any{"b": 2}},
			"wild":   []any{map[string]any{"a": 1}, "skip", nil, []any{}},
			"string": "x",
		}
		if got := mapObjectSlice(object, "ok"); len(got) != 2 || got[1]["b"] != 2 {
			t.Errorf("mapObjectSlice(ok) = %#v", got)
		}
		if got := mapObjectSlice(object, "wild"); len(got) != 1 || got[0]["a"] != 1 {
			t.Errorf("mapObjectSlice(wild) = %#v", got)
		}
		for _, key := range []string{"string", "missing"} {
			if got := mapObjectSlice(object, key); got != nil {
				t.Errorf("mapObjectSlice(%s) = %#v, want nil", key, got)
			}
		}
	})

	t.Run("firstNonEmptyString", func(t *testing.T) {
		if got := firstNonEmptyString(); got != "" {
			t.Errorf("no argument = %q", got)
		}
		if got := firstNonEmptyString("  ", "\t", ""); got != "" {
			t.Errorf("blank arguments = %q", got)
		}
		if got := firstNonEmptyString("", " b ", "c"); got != "b" {
			t.Errorf("first non blank = %q", got)
		}
	})
}

// TestLooksLikeIPLiteral pins the IPv4/IPv6 heuristic the wireguard mapping
// depends on.
func TestLooksLikeIPLiteral(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"203.0.113.9", true},
		{"0.0.0.0", true},
		{"255.255.255.255", true},
		{"01.02.03.04", true},
		{" 203.0.113.9 ", true},
		{"256.0.0.1", false},
		{"1.2.3", false},
		{"1.2.3.4.5", false},
		{"1.2.3.a", false},
		{"-1.2.3.4", false},
		{"", false},
		{"   ", false},
		{"wg.example.com", false},
		{"203.0.113.9.example.com", false},
		{"::1", true},
		{"2001:db8::9", true},
		{"fe80::1%eth0", true},
		// The colon rule is deliberately loose: anything with a colon counts.
		{"wg.example.com:51820", true},
		{":::", true},
	}
	for _, tc := range cases {
		if got := looksLikeIPLiteral(tc.value); got != tc.want {
			t.Errorf("looksLikeIPLiteral(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

// TestParsePluginOptions pins the plugin_opts splitter.
func TestParsePluginOptions(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{"empty", "", map[string]string{}},
		{"only separators", " ;; , ; ", map[string]string{}},
		{"key value pairs", "mode=tls;host=bing.com", map[string]string{"mode": "tls", "host": "bing.com"}},
		{"commas also separate", "mode=tls,host=bing.com", map[string]string{"mode": "tls", "host": "bing.com"}},
		{"bare flag becomes true", "tls;mux", map[string]string{"tls": "true", "mux": "true"}},
		{"keys are lowercased and trimmed", "  Mode = TLS ; Host = bing.com ", map[string]string{"mode": "TLS", "host": "bing.com"}},
		{"only the first separator splits", "a=b=c", map[string]string{"a": "b=c"}},
		{"empty value is kept", "mode=", map[string]string{"mode": ""}},
		{"empty key is kept", "=tls", map[string]string{"": "tls"}},
		{"later duplicates win", "mode=http;mode=tls", map[string]string{"mode": "tls"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parsePluginOptions(tc.raw); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parsePluginOptions(%q) = %#v, want %#v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestApplyShadowsocksPlugin pins the plugin translation in isolation.
func TestApplyShadowsocksPlugin(t *testing.T) {
	cases := []struct {
		name   string
		plugin string
		opts   string
		want   map[string]any
	}{
		{
			name: "obfs with the sing-box obfs spelling", plugin: "obfs", opts: "obfs=tls;obfs-host=bing.com",
			want: map[string]any{"plugin": "obfs", "plugin-opts": map[string]any{"mode": "tls", "host": "bing.com"}},
		},
		{
			name: "obfs with the mode/host spelling", plugin: "obfs", opts: "mode=tls;host=bing.com",
			want: map[string]any{"plugin": "obfs", "plugin-opts": map[string]any{"mode": "tls", "host": "bing.com"}},
		},
		{
			name: "obfs mode only", plugin: "simple-obfs", opts: "mode=http",
			want: map[string]any{"plugin": "obfs", "plugin-opts": map[string]any{"mode": "http"}},
		},
		{
			name: "obfs without options", plugin: "obfs-local", opts: "",
			want: map[string]any{"plugin": "obfs"},
		},
		{
			name: "v2ray-plugin full", plugin: "v2ray-plugin", opts: "tls;host=cdn.example.com;path=/ws",
			want: map[string]any{
				"plugin":      "v2ray-plugin",
				"plugin-opts": map[string]any{"mode": "websocket", "tls": true, "host": "cdn.example.com", "path": "/ws"},
			},
		},
		{
			name: "v2ray-plugin is always a websocket", plugin: "v2ray-plugin", opts: "",
			want: map[string]any{
				"plugin":      "v2ray-plugin",
				"plugin-opts": map[string]any{"mode": "websocket"},
			},
		},
		{
			name: "shadow-tls has no plugin spelling here", plugin: "shadow-tls", opts: "host=front.example.com;password=pw",
			want: map[string]any{},
		},
		{
			name: "unknown plugin stays plain", plugin: "kcptun", opts: "mode=fast",
			want: map[string]any{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxy := map[string]any{}
			applyShadowsocksPlugin(proxy, tc.plugin, tc.opts)
			if !reflect.DeepEqual(proxy, tc.want) {
				t.Fatalf("applyShadowsocksPlugin(%q, %q) = %#v, want %#v", tc.plugin, tc.opts, proxy, tc.want)
			}
		})
	}
}

// TestClashShadowsocksObfsOptionsSurviveTheRoundTrip pins the obfs option round
// trip.
//
// sing-box spells the obfs options `obfs=<mode>;obfs-host=<host>` (that is also
// what this repository's own Clash importer writes, see setSSPluginFromClash),
// and mihomo reads them from plugin-opts.{mode,host}. The mapper used to write a
// top-level obfs/obfs-host pair while reading only mode/host, so every obfs
// parameter was lost on the way back out.
func TestClashShadowsocksObfsOptionsSurviveTheRoundTrip(t *testing.T) {
	proxy := mustClashProxy(t, `{"type":"shadowsocks","server":"198.51.100.12","server_port":8388,"method":"aes-256-gcm","password":"ss-pw",`+
		`"plugin":"obfs-local","plugin_opts":"obfs=tls;obfs-host=bing.com"}`)
	assertClashProxy(t, proxy, map[string]any{
		"type": "ss", "server": "198.51.100.12", "port": 8388,
		"cipher": "aes-256-gcm", "password": "ss-pw",
		"plugin": "obfs", "plugin-opts": map[string]any{"mode": "tls", "host": "bing.com"},
	})
}

// TestHysteriaRateFromMap pins the hysteria up/down rendering, including the
// values it refuses to read (only strings and JSON numbers are understood).
func TestHysteriaRateFromMap(t *testing.T) {
	cases := []struct {
		name   string
		object map[string]any
		keys   []string
		want   string
	}{
		{"json number", map[string]any{"up_mbps": float64(100)}, []string{"up_mbps", "up"}, "100"},
		{"fraction", map[string]any{"up_mbps": float64(100.5)}, []string{"up_mbps", "up"}, "100.5"},
		{"string keeps its unit", map[string]any{"up": " 200 Mbps "}, []string{"up_mbps", "up"}, "200 Mbps"},
		{"empty string falls through to the next key", map[string]any{"up_mbps": "", "up": "50 Mbps"}, []string{"up_mbps", "up"}, "50 Mbps"},
		{"nil falls through to the next key", map[string]any{"up_mbps": nil, "up": float64(50)}, []string{"up_mbps", "up"}, "50"},
		{"first key wins", map[string]any{"up_mbps": "10 Mbps", "up": "20 Mbps"}, []string{"up_mbps", "up"}, "10 Mbps"},
		{"zero is rendered as 0", map[string]any{"up_mbps": float64(0)}, []string{"up_mbps", "up"}, "0"},
		{"bool is not a rate", map[string]any{"up_mbps": true}, []string{"up_mbps", "up"}, ""},
		{"a Go int is not a rate", map[string]any{"up_mbps": 100}, []string{"up_mbps", "up"}, ""},
		{"missing", map[string]any{}, []string{"up_mbps", "up"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hysteriaRateFromMap(tc.object, tc.keys...); got != tc.want {
				t.Fatalf("hysteriaRateFromMap = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClashTLSMapping pins the TLS field selection shared by vmess, vless,
// trojan, hysteria, hysteria2, tuic, anytls and http through the trojan branch.
func TestClashTLSMapping(t *testing.T) {
	// noTLSKey marks a case where the object has no "tls" key at all; a plain
	// nil means the key is present with a null value.
	type noTLSKey struct{}

	baseProxy := map[string]any{"type": "trojan", "server": "t.example.org", "port": 443, "password": "trojan-pw"}
	proxyWith := func(extra map[string]any) map[string]any {
		proxy := make(map[string]any, len(baseProxy)+len(extra))
		for key, value := range baseProxy {
			proxy[key] = value
		}
		for key, value := range extra {
			proxy[key] = value
		}
		return proxy
	}

	cases := []struct {
		name   string
		tls    any
		wantOK bool
		want   map[string]any
	}{
		{"without a tls key", noTLSKey{}, false, proxyWith(nil)},
		{"null tls", nil, false, proxyWith(nil)},
		{"tls true", true, true, proxyWith(map[string]any{"tls": true})},
		{"tls false", false, true, proxyWith(nil)},
		{"empty tls object", map[string]any{}, true, proxyWith(nil)},
		{"enabled only", map[string]any{"enabled": true}, true, proxyWith(map[string]any{"tls": true})},
		{"enabled false", map[string]any{"enabled": false}, true, proxyWith(nil)},
		{
			"server name", map[string]any{"enabled": true, "server_name": "sni.example.com"}, true,
			proxyWith(map[string]any{"tls": true, "sni": "sni.example.com", "servername": "sni.example.com"}),
		},
		{
			"insecure", map[string]any{"enabled": true, "insecure": true}, true,
			proxyWith(map[string]any{"tls": true, "skip-cert-verify": true}),
		},
		{"insecure false", map[string]any{"enabled": true, "insecure": false}, true, proxyWith(map[string]any{"tls": true})},
		{
			"alpn", map[string]any{"enabled": true, "alpn": []any{"h2", "h3"}}, true,
			proxyWith(map[string]any{"tls": true, "alpn": []string{"h2", "h3"}}),
		},
		{
			"scalar alpn becomes a one element list", map[string]any{"enabled": true, "alpn": "h2"}, true,
			proxyWith(map[string]any{"tls": true, "alpn": []string{"h2"}}),
		},
		{
			"uTLS fingerprint", map[string]any{"enabled": true, "utls": map[string]any{"enabled": true, "fingerprint": "chrome"}}, true,
			proxyWith(map[string]any{"tls": true, "client-fingerprint": "chrome"}),
		},
		{
			"uTLS without a fingerprint", map[string]any{"enabled": true, "utls": map[string]any{"enabled": true}}, true,
			proxyWith(map[string]any{"tls": true}),
		},
		{
			"reality", map[string]any{"enabled": true, "reality": map[string]any{"enabled": true, "public_key": "PK", "short_id": "SID"}}, true,
			proxyWith(map[string]any{"tls": true, "reality-opts": map[string]any{"public-key": "PK", "short-id": "SID"}}),
		},
		{
			"reality without keys", map[string]any{"enabled": true, "reality": map[string]any{"enabled": true}}, true,
			proxyWith(map[string]any{"tls": true}),
		},
		{
			"reality while tls is disabled", map[string]any{"enabled": false, "reality": map[string]any{"public_key": "PK", "short_id": "SID"}}, true,
			proxyWith(nil),
		},
		{
			// The sni fields are gated on `enabled`, like the rest of the TLS
			// block: a Clash client must never see a server name without
			// `tls: true`.
			"server name while tls is disabled", map[string]any{"enabled": false, "server_name": "sni.example.com"}, true,
			proxyWith(nil),
		},
		{
			"enabled as a string is ignored", map[string]any{"enabled": "true", "server_name": "sni.example.com"}, true,
			proxyWith(nil),
		},
		{"tls as a string", "tls", false, proxyWith(nil)},
		{"tls as a number", float64(1), false, proxyWith(nil)},
		{"tls as a list", []any{"tls"}, false, proxyWith(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			object := map[string]any{"type": "trojan", "server": "t.example.org", "server_port": 443, "password": "trojan-pw"}
			if _, absent := tc.tls.(noTLSKey); !absent {
				object["tls"] = tc.tls
			}

			tls, ok := parseClashTLS(object)
			if ok != tc.wantOK {
				t.Fatalf("parseClashTLS ok = %v, want %v", ok, tc.wantOK)
			}
			// applyClashTLS is the helper under test here: it fills the proxy
			// with the shared TLS keys only.
			proxy := map[string]any{}
			applyClashTLS(proxy, object)
			assertClashProxy(t, proxy, withoutBaseFields(tc.want))
			// The trojan branch must agree with the two helpers.
			assertClashProxy(t, mustClashProxy(t, mustJSON(t, object)), tc.want)
			if !ok && !reflect.DeepEqual(tls, clashTLS{}) {
				t.Fatalf("refused tls produced %#v", tls)
			}
		})
	}
}

// withoutBaseFields drops the fields clashBase renders so a whole proxy map can
// be compared against the TLS-only map applyClashTLS produces.
func withoutBaseFields(proxy map[string]any) map[string]any {
	out := make(map[string]any, len(proxy))
	for key, value := range proxy {
		switch key {
		case "type", "server", "port", "password":
			continue
		}
		out[key] = value
	}
	return out
}

// mustJSON re-encodes a hand built object so the mapper sees JSON types.
func mustJSON(t *testing.T, object map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("encode object: %v", err)
	}
	return string(encoded)
}

// TestParseClashTLSStructFields pins the parsed struct, not just the emitted
// keys.
func TestParseClashTLSStructFields(t *testing.T) {
	full := map[string]any{
		"tls": map[string]any{
			"enabled": true, "server_name": "sni.example.com", "insecure": true,
			"utls":    map[string]any{"enabled": true, "fingerprint": "chrome"},
			"reality": map[string]any{"enabled": true, "public_key": "PK", "short_id": "SID"},
			"alpn":    []any{"h2", float64(3)},
		},
	}
	tls, ok := parseClashTLS(full)
	if !ok {
		t.Fatal("parseClashTLS refused a complete tls object")
	}
	if !tls.Enabled || tls.ServerName != "sni.example.com" || !tls.Insecure ||
		tls.Fingerprint != "chrome" || !tls.Reality || tls.PublicKey != "PK" || tls.ShortID != "SID" {
		t.Fatalf("parsed tls = %#v", tls)
	}
	// A non string alpn entry is dropped rather than crashing.
	if !reflect.DeepEqual(tls.ALPN, []string{"h2"}) {
		t.Fatalf("parsed alpn = %#v", tls.ALPN)
	}
}

// TestClashV2RayTransportMapping pins the transport object mapping, including
// the transports Clash cannot express.
func TestClashV2RayTransportMapping(t *testing.T) {
	newVMess := func(t *testing.T, transport string) map[string]any {
		t.Helper()
		object := map[string]any{"type": "vmess", "server": "vmess.example.com", "server_port": 443, "uuid": "UUID"}
		if transport != "" {
			var decoded any
			if err := json.Unmarshal([]byte(transport), &decoded); err != nil {
				t.Fatalf("decode transport %s: %v", transport, err)
			}
			object["transport"] = decoded
		}
		return object
	}
	base := map[string]any{
		"type": "vmess", "server": "vmess.example.com", "port": 443, "uuid": "UUID",
		"alterId": 0, "cipher": "auto",
	}
	proxyWith := func(extra map[string]any) map[string]any {
		proxy := make(map[string]any, len(base)+len(extra))
		for key, value := range base {
			proxy[key] = value
		}
		for key, value := range extra {
			proxy[key] = value
		}
		return proxy
	}

	cases := []struct {
		name      string
		transport string
		want      map[string]any
	}{
		{"without a transport", "", proxyWith(nil)},
		{"transport is not an object", `"ws"`, proxyWith(nil)},
		{"ws with a path", `{"type":"ws","path":"/ws"}`, proxyWith(map[string]any{"network": "ws", "ws-opts": map[string]any{"path": "/ws"}})},
		{"ws without options", `{"type":"ws"}`, proxyWith(map[string]any{"network": "ws"})},
		{"ws type is case insensitive", `{"type":"WS","path":"/ws"}`, proxyWith(map[string]any{"network": "ws", "ws-opts": map[string]any{"path": "/ws"}})},
		{
			"ws headers without Host", `{"type":"ws","path":"/ws","headers":{"User-Agent":"prism"}}`,
			proxyWith(map[string]any{"network": "ws", "ws-opts": map[string]any{"path": "/ws"}}),
		},
		{
			// sing-box's Listable[string] also accepts a list; only a scalar is
			// read here.
			"ws Host as a list is dropped", `{"type":"ws","path":"/ws","headers":{"Host":["cdn.example.com"]}}`,
			proxyWith(map[string]any{"network": "ws", "ws-opts": map[string]any{"path": "/ws"}}),
		},
		{
			"ws headers keep the Host", `{"type":"ws","path":"/ws","headers":{"Host":"cdn.example.com","User-Agent":"prism"}}`,
			proxyWith(map[string]any{"network": "ws", "ws-opts": map[string]any{"path": "/ws", "headers": map[string]any{"Host": "cdn.example.com"}}}),
		},
		{
			"grpc", `{"type":"grpc","service_name":"grpc-svc"}`,
			proxyWith(map[string]any{"network": "grpc", "grpc-opts": map[string]any{"grpc-service-name": "grpc-svc"}}),
		},
		{"grpc without a service name", `{"type":"grpc"}`, proxyWith(map[string]any{"network": "grpc"})},
		{
			"http transport becomes h2", `{"type":"http","path":"/h2","host":["h2.example.com","h2b.example.com"]}`,
			proxyWith(map[string]any{"network": "h2", "h2-opts": map[string]any{"path": "/h2", "host": []string{"h2.example.com", "h2b.example.com"}}}),
		},
		{"http transport without options", `{"type":"http"}`, proxyWith(map[string]any{"network": "h2"})},
		{
			"httpupgrade keeps its own network type", `{"type":"httpupgrade","path":"/up","host":"up.example.com"}`,
			proxyWith(map[string]any{"network": "httpupgrade", "http-upgrade-opts": map[string]any{"path": "/up", "host": "up.example.com"}}),
		},
		{"httpupgrade without options", `{"type":"httpupgrade"}`, proxyWith(map[string]any{"network": "httpupgrade"})},
		{"quic", `{"type":"quic"}`, proxyWith(map[string]any{"network": "quic"})},
		{"unknown transport is ignored", `{"type":"xhttp","path":"/x"}`, proxyWith(nil)},
		{"empty transport type is ignored", `{"type":""}`, proxyWith(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			object := newVMess(t, tc.transport)
			proxy := map[string]any{}
			applyClashV2RayTransport(proxy, object)
			wantTransport := map[string]any{}
			for key, value := range tc.want {
				if key != "type" && key != "server" && key != "port" && key != "uuid" && key != "alterId" && key != "cipher" {
					wantTransport[key] = value
				}
			}
			if !reflect.DeepEqual(proxy, wantTransport) {
				t.Fatalf("applyClashV2RayTransport(%s) = %#v, want %#v", tc.transport, proxy, wantTransport)
			}
			assertClashProxy(t, mustClashProxy(t, mustJSON(t, object)), tc.want)
		})
	}
}

// TestClashSnellVersionAndObfs pins the Snell v4 gate and the obfs options.
func TestClashSnellVersionAndObfs(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		wantOK bool
		want   map[string]any
	}{
		{
			name: "version 4", raw: `{"type":"snell","server":"snell.example.com","server_port":443,"psk":"psk","version":4}`, wantOK: true,
			want: map[string]any{"type": "snell", "server": "snell.example.com", "port": 443, "psk": "psk", "version": 4},
		},
		{
			name: "version 4 as a string", raw: `{"type":"snell","server":"snell.example.com","server_port":443,"psk":"psk","version":"4"}`, wantOK: true,
			want: map[string]any{"type": "snell", "server": "snell.example.com", "port": 443, "psk": "psk", "version": 4},
		},
		{
			name: "version 0 is treated as v4", raw: `{"type":"snell","server":"snell.example.com","server_port":443,"psk":"psk","version":0}`, wantOK: true,
			want: map[string]any{"type": "snell", "server": "snell.example.com", "port": 443, "psk": "psk", "version": 4},
		},
		{
			name: "obfs mode only", raw: `{"type":"snell","server":"snell.example.com","server_port":443,"psk":"psk","version":4,"obfs_mode":"tls"}`, wantOK: true,
			want: map[string]any{
				"type": "snell", "server": "snell.example.com", "port": 443, "psk": "psk", "version": 4,
				"obfs-opts": map[string]any{"mode": "tls"},
			},
		},
		{
			// obfs_host without a mode has nothing to attach to.
			name: "obfs host without a mode is dropped", raw: `{"type":"snell","server":"snell.example.com","server_port":443,"psk":"psk","version":4,"obfs_host":"cdn.example.com"}`, wantOK: true,
			want: map[string]any{"type": "snell", "server": "snell.example.com", "port": 443, "psk": "psk", "version": 4},
		},
		{
			name: "version 5 is refused", raw: `{"type":"snell","server":"snell.example.com","server_port":443,"psk":"psk","version":5}`, wantOK: false,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxy, ok := clashProxyFromSingbox(clashObject(t, tc.raw))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK {
				assertClashProxy(t, proxy, tc.want)
			}
		})
	}
}

// TestClashWireGuardPeerFields pins the wireguard field selection.
func TestClashWireGuardPeerFields(t *testing.T) {
	base := `"type":"wireguard","private_key":"PRIV","peers":[{"address":"203.0.113.9","port":51820,"public_key":"PUB"`
	cases := []struct {
		name string
		raw  string
		want map[string]any
	}{
		{
			name: "minimal",
			raw:  `{` + base + `}]}`,
			want: map[string]any{
				"type": "wireguard", "server": "203.0.113.9", "port": 51820,
				"private-key": "PRIV", "public-key": "PUB",
			},
		},
		{
			name: "ipv6 endpoint and local address",
			raw:  `{"type":"wireguard","address":["2001:db8::2/128"],"private_key":"PRIV","peers":[{"address":"2001:db8::9","port":51820,"public_key":"PUB"}]}`,
			want: map[string]any{
				"type": "wireguard", "server": "2001:db8::9", "port": 51820,
				"private-key": "PRIV", "public-key": "PUB", "ipv6": "2001:db8::2/128",
			},
		},
		{
			name: "reserved needs exactly three bytes",
			raw:  `{` + base + `,"reserved":[1,2]}]}`,
			want: map[string]any{
				"type": "wireguard", "server": "203.0.113.9", "port": 51820,
				"private-key": "PRIV", "public-key": "PUB",
			},
		},
		{
			name: "reserved with a non number entry is dropped",
			raw:  `{` + base + `,"reserved":[1,"2",3]}]}`,
			want: map[string]any{
				"type": "wireguard", "server": "203.0.113.9", "port": 51820,
				"private-key": "PRIV", "public-key": "PUB",
			},
		},
		{
			// The canonical form: node.WireGuardPeer.Reserved is a []uint8, which
			// Go's JSON codec renders as a base64 string.
			name: "reserved as the canonical base64 string",
			raw:  `{` + base + `,"reserved":"AQID"}]}`,
			want: map[string]any{
				"type": "wireguard", "server": "203.0.113.9", "port": 51820,
				"private-key": "PRIV", "public-key": "PUB",
				"reserved": []int{1, 2, 3},
			},
		},
		{
			// The number-array spelling still works, for hand-written documents.
			name: "reserved as a number array",
			raw:  `{` + base + `,"reserved":[1,2,3]}]}`,
			want: map[string]any{
				"type": "wireguard", "server": "203.0.113.9", "port": 51820,
				"private-key": "PRIV", "public-key": "PUB",
				"reserved": []int{1, 2, 3},
			},
		},
		{
			// A base64 string that does not decode to three bytes is dropped,
			// like the wrong-length array above.
			name: "reserved base64 of the wrong length",
			raw:  `{` + base + `,"reserved":"AQI="}]}`,
			want: map[string]any{
				"type": "wireguard", "server": "203.0.113.9", "port": 51820,
				"private-key": "PRIV", "public-key": "PUB",
			},
		},
		{
			// A string that is not base64 at all is dropped rather than guessed.
			name: "reserved that is not base64",
			raw:  `{` + base + `,"reserved":"not base64!!"}]}`,
			want: map[string]any{
				"type": "wireguard", "server": "203.0.113.9", "port": 51820,
				"private-key": "PRIV", "public-key": "PUB",
			},
		},
		{
			// A wrong JSON type is dropped.
			name: "reserved as a number",
			raw:  `{` + base + `,"reserved":5}]}`,
			want: map[string]any{
				"type": "wireguard", "server": "203.0.113.9", "port": 51820,
				"private-key": "PRIV", "public-key": "PUB",
			},
		},
		{
			name: "zero mtu is omitted",
			raw:  `{"type":"wireguard","address":["10.0.0.2/32"],"private_key":"PRIV","mtu":0,"peers":[{"address":"203.0.113.9","port":51820,"public_key":"PUB"}]}`,
			want: map[string]any{
				"type": "wireguard", "server": "203.0.113.9", "port": 51820,
				"private-key": "PRIV", "public-key": "PUB", "ip": "10.0.0.2/32",
			},
		},
		{
			name: "port as a string",
			raw:  `{"type":"wireguard","private_key":"PRIV","peers":[{"address":"203.0.113.9","port":"51820","public_key":"PUB"}]}`,
			want: map[string]any{
				"type": "wireguard", "server": "203.0.113.9", "port": 51820,
				"private-key": "PRIV", "public-key": "PUB",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertClashProxy(t, mustClashProxy(t, tc.raw), tc.want)
		})
	}
}

// TestOpenVPNProto pins the OpenVPN network rendering.
func TestOpenVPNProto(t *testing.T) {
	cases := map[string]string{
		"tcp": "tcp", "TCP": "tcp", "tcp4": "tcp", "tcp6": "tcp", " tcp ": "tcp",
		"udp": "udp", "udp4": "udp", "udp6": "udp", "": "udp", "sctp": "udp", "tcp-client": "udp",
	}
	for network, want := range cases {
		if got := openVPNProto(network); got != want {
			t.Errorf("openVPNProto(%q) = %q, want %q", network, got, want)
		}
	}
}

// TestClashOpenVPNClientFields pins the OpenVPN reverse mapping: the fields the
// profile builder in internal/node produces.
func TestClashOpenVPNClientFields(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]any
	}{
		{
			name: "udp proto from the server entry",
			raw:  `{"type":"openvpn-client","servers":[{"server":"vpn.example.com","server_port":1194,"network":"udp"}],"tls":{}}`,
			want: map[string]any{"type": "openvpn", "server": "vpn.example.com", "port": 1194, "proto": "udp"},
		},
		{
			name: "the top level network is the fallback",
			raw:  `{"type":"openvpn-client","network":"tcp4","servers":[{"server":"vpn.example.com","server_port":1194}],"tls":{}}`,
			want: map[string]any{"type": "openvpn", "server": "vpn.example.com", "port": 1194, "proto": "tcp"},
		},
		{
			name: "the server entry wins over the top level network",
			raw:  `{"type":"openvpn-client","network":"tcp","servers":[{"server":"vpn.example.com","server_port":1194,"network":"udp6"}],"tls":{}}`,
			want: map[string]any{"type": "openvpn", "server": "vpn.example.com", "port": 1194, "proto": "udp"},
		},
		{
			name: "without a network there is no proto key",
			raw:  `{"type":"openvpn-client","servers":[{"server":"vpn.example.com","server_port":1194}],"tls":{}}`,
			want: map[string]any{"type": "openvpn", "server": "vpn.example.com", "port": 1194},
		},
		{
			name: "without a port there is no port key",
			raw:  `{"type":"openvpn-client","servers":[{"server":"vpn.example.com"}],"tls":{}}`,
			want: map[string]any{"type": "openvpn", "server": "vpn.example.com"},
		},
		{
			name: "cipher from the tls block",
			raw:  `{"type":"openvpn-client","servers":[{"server":"vpn.example.com","server_port":1194}],"tls":{"cipher":"AES-256-GCM"}}`,
			want: map[string]any{"type": "openvpn", "server": "vpn.example.com", "port": 1194, "cipher": "AES-256-GCM"},
		},
		{
			name: "the data cipher fallback wins over the tls cipher",
			raw:  `{"type":"openvpn-client","servers":[{"server":"vpn.example.com","server_port":1194}],"data_ciphers_fallback":"AES-128-CBC","tls":{"cipher":"AES-256-GCM"}}`,
			want: map[string]any{"type": "openvpn", "server": "vpn.example.com", "port": 1194, "cipher": "AES-128-CBC"},
		},
		{
			name: "tls-auth control wrap",
			raw:  `{"type":"openvpn-client","servers":[{"server":"vpn.example.com","server_port":1194}],"tls":{"control_wrap":{"key":["TA-KEY"],"type":"tls_auth","direction":"server"}}}`,
			want: map[string]any{"type": "openvpn", "server": "vpn.example.com", "port": 1194, "tls-auth": "TA-KEY", "key-direction": "server"},
		},
		{
			name: "a control wrap without a key emits nothing",
			raw:  `{"type":"openvpn-client","servers":[{"server":"vpn.example.com","server_port":1194}],"tls":{"control_wrap":{"type":"tls_auth","direction":"client"}}}`,
			want: map[string]any{"type": "openvpn", "server": "vpn.example.com", "port": 1194},
		},
		{
			name: "a control wrap that is not an object is ignored",
			raw:  `{"type":"openvpn-client","servers":[{"server":"vpn.example.com","server_port":1194}],"tls":{"control_wrap":"tls_auth"}}`,
			want: map[string]any{"type": "openvpn", "server": "vpn.example.com", "port": 1194},
		},
		{
			name: "multi line certificates are joined",
			raw:  `{"type":"openvpn-client","servers":[{"server":"vpn.example.com","server_port":1194}],"tls":{"certificate":["CA-BEGIN","CA-END"],"client_certificate":["CERT"],"client_key":["KEY"]}}`,
			want: map[string]any{
				"type": "openvpn", "server": "vpn.example.com", "port": 1194,
				"ca": "CA-BEGIN\nCA-END", "cert": "CERT", "key": "KEY",
			},
		},
		{
			name: "credentials and ciphers",
			raw: `{"type":"openvpn-client","servers":[{"server":"vpn.example.com","server_port":1194}],` +
				`"tls":{},"data_ciphers":["AES-256-GCM"],"auth":"SHA1","username":"ovpn-user","password":"ovpn-pw"}`,
			want: map[string]any{
				"type": "openvpn", "server": "vpn.example.com", "port": 1194,
				"data-ciphers": []string{"AES-256-GCM"}, "auth": "SHA1",
				"username": "ovpn-user", "password": "ovpn-pw",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertClashProxy(t, mustClashProxy(t, tc.raw), tc.want)
		})
	}
}

// TestClashOpenVPNClientStaticKeySpelling documents which static-key spellings
// are refused.
//
// Known gap: internal/node writes mode "static_key" (with the underscore), and
// only that exact spelling is refused here. A document that says
// "static-key" is emitted as a TLS mode proxy even though it carries no TLS
// material.
func TestClashOpenVPNClientStaticKeySpelling(t *testing.T) {
	staticKey := `{"type":"openvpn-client","mode":"static_key","static_key":["KEY"],"servers":[{"server":"vpn.example.com","server_port":1194}]}`
	if proxy, ok := clashProxyFromSingbox(clashObject(t, staticKey)); ok {
		t.Fatalf("static_key mode was accepted: %#v", proxy)
	}
	upper := `{"type":"openvpn-client","mode":"STATIC_KEY","static_key":["KEY"],"servers":[{"server":"vpn.example.com","server_port":1194}]}`
	if proxy, ok := clashProxyFromSingbox(clashObject(t, upper)); ok {
		t.Fatalf("STATIC_KEY mode was accepted: %#v", proxy)
	}
	dashed := `{"type":"openvpn-client","mode":"static-key","static_key":["KEY"],"servers":[{"server":"vpn.example.com","server_port":1194}],"tls":{}}`
	proxy, ok := clashProxyFromSingbox(clashObject(t, dashed))
	if !ok {
		t.Fatal("the dashed spelling was refused, the pin is stale")
	}
	assertClashProxy(t, proxy, map[string]any{"type": "openvpn", "server": "vpn.example.com", "port": 1194})
}

// TestClashOpenConnectIsRefused pins that openconnect is not exported at all.
//
// openconnect is not a mihomo proxy type (docs/plan/PRISM_PLAN_FULL.md F12,
// verified against mihomo v1.19.31 adapter/parser.go) and mihomo refuses a
// whole config that contains an unknown type, so emitting one would break every
// other node in the same document. The mapper reports it as not representable.
func TestClashOpenConnectIsRefused(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"minimal", `{"type":"openconnect","server":"oc.example.com"}`},
		{"port as a string", `{"type":"openconnect","server":"oc.example.com","server_port":"4443"}`},
		{"zero port", `{"type":"openconnect","server":"oc.example.com","server_port":0}`},
		{"credentials", `{"type":"openconnect","server":"oc.example.com","server_port":4443,"username":"oc-user","password":"oc-pw"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if proxy, ok := clashProxyFromSingbox(clashObject(t, tc.raw)); ok {
				t.Fatalf("openconnect was exported as %#v; mihomo refuses a whole document for an unknown proxy type", proxy)
			}
		})
	}
}

// TestClashChainProxyShape pins the one chain shape mihomo can express.
func TestClashChainProxyShape(t *testing.T) {
	ssMain := `{"type":"shadowsocks","server":"198.51.100.20","server_port":443,"method":"aes-256-gcm","password":"chain-pw","detour":"d0"}`
	shadowTLSDep := `{"type":"shadowtls","tag":"d0","server":"198.51.100.20","server_port":443,"version":3,"password":"stls-pw"}`
	chainItem := func(main string, deps ...string) preparedItem {
		item := preparedItem{Doc: NodeDoc{Main: json.RawMessage(main)}}
		for _, dep := range deps {
			item.Doc.Deps = append(item.Doc.Deps, json.RawMessage(dep))
		}
		return item
	}

	cases := []struct {
		name   string
		item   preparedItem
		wantOK bool
		want   map[string]any
	}{
		{
			name: "shadowsocks with a shadow-tls hop", wantOK: true,
			item: chainItem(ssMain, shadowTLSDep),
			want: map[string]any{
				"type": "ss", "server": "198.51.100.20", "port": 443,
				"cipher": "aes-256-gcm", "password": "chain-pw",
				"plugin": "shadow-tls",
				"plugin-opts": map[string]any{
					"host": "198.51.100.20", "password": "stls-pw", "version": 3,
				},
			},
		},
		{
			name: "version as a string", wantOK: true,
			item: chainItem(ssMain, `{"type":"shadowtls","server":"198.51.100.20","port":443,"version":"3","password":"stls-pw"}`),
			want: map[string]any{
				"type": "ss", "server": "198.51.100.20", "port": 443,
				"cipher": "aes-256-gcm", "password": "chain-pw",
				"plugin": "shadow-tls",
				"plugin-opts": map[string]any{
					"host": "198.51.100.20", "password": "stls-pw", "version": 3,
				},
			},
		},
		{
			// Without a host or a password the version alone still passes the
			// plugin-opts emptiness check.
			name: "a hop without credentials is emitted anyway", wantOK: true,
			item: chainItem(ssMain, `{"type":"shadowtls","version":3}`),
			want: map[string]any{
				"type": "ss", "server": "198.51.100.20", "port": 443,
				"cipher": "aes-256-gcm", "password": "chain-pw",
				"plugin":      "shadow-tls",
				"plugin-opts": map[string]any{"version": 3},
			},
		},
		{
			name: "an empty hop is refused", wantOK: false,
			item: chainItem(ssMain, `{"type":"shadowtls"}`),
		},
		{
			name: "a chain without deps is refused", wantOK: false,
			item: chainItem(ssMain),
		},
		{
			name: "a main object that is not shadowsocks is refused", wantOK: false,
			item: chainItem(`{"type":"vmess","server":"a.example.com","server_port":443,"uuid":"UUID"}`, shadowTLSDep),
		},
		{
			name: "a hop that is not shadow-tls is refused", wantOK: false,
			item: chainItem(ssMain, `{"type":"socks","server":"198.51.100.20","server_port":443}`),
		},
		{
			name: "only the first dep is considered", wantOK: false,
			item: chainItem(ssMain, `{"type":"socks","server":"198.51.100.20","server_port":443}`, shadowTLSDep),
		},
		{
			name: "a malformed main object is refused", wantOK: false,
			item: chainItem(`not json`, shadowTLSDep),
		},
		{
			name: "a malformed hop is refused", wantOK: false,
			item: chainItem(ssMain, `not json`),
		},
		{
			name: "a shadowsocks main without credentials is refused", wantOK: false,
			item: chainItem(`{"type":"shadowsocks","server":"198.51.100.20","server_port":443,"detour":"d0"}`, shadowTLSDep),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxy, ok := clashChainProxy(tc.item)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			assertClashProxy(t, proxy, tc.want)
		})
	}
}

// --- 3. round trips --------------------------------------------------------

// clashRoundTrip drives the user visible path for one Clash proxy: the
// subscription parser turns it into a sing-box node document, and the mihomo
// exporter turns that document back into a Clash proxy.
func clashRoundTrip(t *testing.T, name string, proxy map[string]any) (singbox map[string]any, clash map[string]any, report Report) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"proxies": []any{proxy}})
	if err != nil {
		t.Fatalf("marshal clash document: %v", err)
	}
	result, err := subscription.ParseWithReport(body)
	if err != nil {
		t.Fatalf("parse clash document: %v", err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("clash parser imported %d nodes (skipped: %+v), want 1", len(result.Nodes), result.Skipped)
	}
	singbox, err = objectMap(result.Nodes[0].RawOptions)
	if err != nil {
		t.Fatalf("decode parsed outbound: %v", err)
	}
	out, _, exportReport, err := Export([]Item{{Name: name, RawOptions: result.Nodes[0].RawOptions}}, FormatMihomo, Options{})
	if err != nil {
		t.Fatalf("Export(%s): %v", name, err)
	}
	report = exportReport
	if report.Exported != 1 || len(report.Skipped) != 0 {
		return singbox, nil, report
	}
	document := decodeMihomo(t, out)
	if len(document.Proxies) != 1 {
		t.Fatalf("mihomo export produced %d proxies, want 1", len(document.Proxies))
	}
	return singbox, document.Proxies[0], report
}

// TestClashRoundTripThroughTheSubscriptionParser checks the two directions
// agree for every protocol whose Clash spelling is faithful. wantSingbox
// asserts the sing-box document the importer built and wantClash the Clash
// proxy the exporter emitted; the explicit equivalences are noted per case
// (Clash "cipher"/"port"/"servername" is sing-box "method"/"server_port"/
// "tls.server_name").
func TestClashRoundTripThroughTheSubscriptionParser(t *testing.T) {
	cases := []struct {
		name        string
		clash       map[string]any
		wantSingbox map[string]any
		wantClash   map[string]any
	}{
		{
			name:  "shadowsocks",
			clash: map[string]any{"name": "ss", "type": "ss", "server": "198.51.100.10", "port": 8388, "cipher": "aes-256-gcm", "password": "ss-pw"},
			wantSingbox: map[string]any{
				"type": "shadowsocks", "server": "198.51.100.10", "server_port": float64(8388),
				"method": "aes-256-gcm", "password": "ss-pw",
			},
			wantClash: map[string]any{"type": "ss", "server": "198.51.100.10", "port": 8388, "cipher": "aes-256-gcm", "password": "ss-pw"},
		},
		{
			name: "shadowsocks with a v2ray-plugin",
			clash: map[string]any{
				"name": "ss-plugin", "type": "ss", "server": "198.51.100.11", "port": 8388, "cipher": "aes-256-gcm", "password": "ss-pw",
				"plugin": "v2ray-plugin",
				"plugin-opts": map[string]any{
					"mode": "websocket", "tls": true, "host": "cdn.example.com", "path": "/ws",
				},
			},
			wantSingbox: map[string]any{
				"plugin":      "v2ray-plugin",
				"plugin_opts": "host=cdn.example.com;mode=websocket;path=/ws;tls",
			},
			wantClash: map[string]any{
				"plugin": "v2ray-plugin",
				"plugin-opts": map[string]any{
					"mode": "websocket", "tls": true, "host": "cdn.example.com", "path": "/ws",
				},
			},
		},
		{
			name: "vmess over ws with tls",
			clash: map[string]any{
				"name": "vmess", "type": "vmess", "server": "vmess.example.com", "port": 443,
				"uuid": "11111111-2222-3333-4444-555555555555", "alterId": 0, "cipher": "auto",
				"tls": true, "servername": "vmess.example.com", "skip-cert-verify": true,
				"network": "ws", "ws-opts": map[string]any{"path": "/ws", "headers": map[string]any{"Host": "cdn.example.com"}},
			},
			wantSingbox: map[string]any{
				"type": "vmess", "uuid": "11111111-2222-3333-4444-555555555555",
				"alter_id": float64(0), "security": "auto",
				"tls":       map[string]any{"enabled": true, "insecure": true, "server_name": "vmess.example.com"},
				"transport": map[string]any{"type": "ws", "path": "/ws", "headers": map[string]any{"Host": "cdn.example.com"}},
			},
			wantClash: map[string]any{
				"type": "vmess", "uuid": "11111111-2222-3333-4444-555555555555", "alterId": 0, "cipher": "auto",
				"tls": true, "sni": "vmess.example.com", "servername": "vmess.example.com", "skip-cert-verify": true,
				"network": "ws", "ws-opts": map[string]any{"path": "/ws", "headers": map[string]any{"Host": "cdn.example.com"}},
			},
		},
		{
			name: "vless with reality",
			clash: map[string]any{
				"name": "vless", "type": "vless", "server": "vless.example.com", "port": 443,
				"uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "flow": "xtls-rprx-vision",
				"tls": true, "servername": "www.example.com", "client-fingerprint": "chrome",
				"reality-opts": map[string]any{"public-key": "PK", "short-id": "0123abcd"},
			},
			wantSingbox: map[string]any{
				"flow": "xtls-rprx-vision",
				"tls": map[string]any{
					"enabled": true, "server_name": "www.example.com",
					"reality": map[string]any{"enabled": true, "public_key": "PK", "short_id": "0123abcd"},
					"utls":    map[string]any{"enabled": true, "fingerprint": "chrome"},
				},
			},
			wantClash: map[string]any{
				"flow": "xtls-rprx-vision", "tls": true, "sni": "www.example.com", "servername": "www.example.com",
				"client-fingerprint": "chrome",
				"reality-opts":       map[string]any{"public-key": "PK", "short-id": "0123abcd"},
			},
		},
		{
			name: "trojan over grpc",
			clash: map[string]any{
				"name": "trojan", "type": "trojan", "server": "trojan.example.org", "port": 8443, "password": "trojan-pw",
				"sni": "trojan.example.org", "skip-cert-verify": true,
				"network": "grpc", "grpc-opts": map[string]any{"grpc-service-name": "grpc-svc"},
			},
			wantSingbox: map[string]any{
				"password":  "trojan-pw",
				"tls":       map[string]any{"enabled": true, "insecure": true, "server_name": "trojan.example.org"},
				"transport": map[string]any{"type": "grpc", "service_name": "grpc-svc"},
			},
			wantClash: map[string]any{
				"type": "trojan", "password": "trojan-pw", "tls": true,
				"sni": "trojan.example.org", "servername": "trojan.example.org", "skip-cert-verify": true,
				"network": "grpc", "grpc-opts": map[string]any{"grpc-service-name": "grpc-svc"},
			},
		},
		{
			name: "hysteria keeps its string rates",
			clash: map[string]any{
				"name": "hy1", "type": "hysteria", "server": "hy.example.net", "port": 443,
				"auth-str": "hy-pw", "obfs": "xplus", "up": "30 Mbps", "down": "200 Mbps",
				"sni": "hy.example.net", "skip-cert-verify": true,
			},
			wantSingbox: map[string]any{
				"auth_str": "hy-pw", "obfs": "xplus", "up": "30 Mbps", "down": "200 Mbps",
				"tls": map[string]any{"enabled": true, "insecure": true, "server_name": "hy.example.net"},
			},
			wantClash: map[string]any{
				"type": "hysteria", "auth-str": "hy-pw", "obfs": "xplus",
				"up": "30 Mbps", "down": "200 Mbps", "tls": true, "sni": "hy.example.net",
			},
		},
		{
			name: "tuic",
			clash: map[string]any{
				"name": "tuic", "type": "tuic", "server": "tuic.example.org", "port": 443,
				"uuid": "bbbbbbbb-cccc-dddd-eeee-ffffffffffff", "password": "tuic-pw",
				"sni": "tuic.example.org", "congestion-controller": "bbr", "udp-relay-mode": "native",
				"alpn": []any{"h3"},
			},
			wantSingbox: map[string]any{
				"congestion_control": "bbr", "udp_relay_mode": "native",
				"tls": map[string]any{"enabled": true, "server_name": "tuic.example.org", "alpn": []any{"h3"}},
			},
			wantClash: map[string]any{
				"congestion-controller": "bbr", "udp-relay-mode": "native",
				"tls": true, "sni": "tuic.example.org", "alpn": []any{"h3"},
			},
		},
		{
			name: "anytls",
			clash: map[string]any{
				"name": "anytls", "type": "anytls", "server": "anytls.example.com", "port": 443, "password": "anytls-pw",
				"sni": "anytls.example.com", "client-fingerprint": "firefox", "skip-cert-verify": true,
			},
			wantSingbox: map[string]any{
				"tls": map[string]any{
					"enabled": true, "insecure": true, "server_name": "anytls.example.com",
					"utls": map[string]any{"enabled": true, "fingerprint": "firefox"},
				},
			},
			wantClash: map[string]any{
				"password": "anytls-pw", "tls": true, "sni": "anytls.example.com",
				"client-fingerprint": "firefox", "skip-cert-verify": true,
			},
		},
		{
			name: "snell v4 with obfs",
			clash: map[string]any{
				"name": "snell", "type": "snell", "server": "snell.example.com", "port": 443,
				"psk": "snell-psk", "version": 4,
				"obfs-opts": map[string]any{"mode": "http", "host": "cdn.example.com"},
			},
			wantSingbox: map[string]any{
				"psk": "snell-psk", "version": float64(4),
				"obfs_mode": "http", "obfs_host": "cdn.example.com",
			},
			wantClash: map[string]any{
				"type": "snell", "psk": "snell-psk", "version": 4,
				"obfs-opts": map[string]any{"mode": "http", "host": "cdn.example.com"},
			},
		},
		{
			name:  "socks5",
			clash: map[string]any{"name": "socks", "type": "socks5", "server": "socks.example.com", "port": 1080, "username": "socks-user", "password": "socks-pw"},
			// Clash "socks5" carries the sing-box version field.
			wantSingbox: map[string]any{"type": "socks", "version": "5", "username": "socks-user", "password": "socks-pw"},
			wantClash:   map[string]any{"type": "socks5", "server": "socks.example.com", "port": 1080, "username": "socks-user", "password": "socks-pw"},
		},
		{
			name:        "http",
			clash:       map[string]any{"name": "http", "type": "http", "server": "http.example.com", "port": 8080, "username": "http-user", "password": "http-pw"},
			wantSingbox: map[string]any{"type": "http", "username": "http-user", "password": "http-pw"},
			wantClash:   map[string]any{"type": "http", "server": "http.example.com", "port": 8080, "username": "http-user", "password": "http-pw"},
		},
		{
			name:  "ssh",
			clash: map[string]any{"name": "ssh", "type": "ssh", "server": "ssh.example.com", "port": 22, "username": "root", "password": "ssh-pw", "private-key": "KEY"},
			// Clash "username"/"private-key" is sing-box "user"/"private_key".
			wantSingbox: map[string]any{"type": "ssh", "user": "root", "password": "ssh-pw", "private_key": "KEY"},
			wantClash:   map[string]any{"type": "ssh", "server": "ssh.example.com", "port": 22, "username": "root", "password": "ssh-pw", "private-key": "KEY"},
		},
		{
			name: "wireguard",
			clash: map[string]any{
				"name": "wg", "type": "wireguard", "server": "203.0.113.9", "port": 51820,
				"private-key": "WG-PRIVATE", "public-key": "WG-PUBLIC", "pre-shared-key": "WG-PSK",
				"ip": "10.7.0.2/32", "mtu": 1420, "allowed-ips": []any{"0.0.0.0/0"},
			},
			wantClash: map[string]any{
				"type": "wireguard", "server": "203.0.113.9", "port": 51820,
				"private-key": "WG-PRIVATE", "public-key": "WG-PUBLIC", "pre-shared-key": "WG-PSK",
				"ip": "10.7.0.2/32", "mtu": 1420, "allowed-ips": []any{"0.0.0.0/0"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			singbox, proxy, report := clashRoundTrip(t, tc.name, tc.clash)
			if proxy == nil {
				t.Fatalf("%s is not representable in mihomo: %+v", tc.name, report.Skipped)
			}
			for key, want := range tc.wantSingbox {
				assertField(t, "sing-box "+tc.name, singbox, key, want)
			}
			for key, want := range tc.wantClash {
				assertField(t, "clash "+tc.name, proxy, key, want)
			}
		})
	}
}

// TestClashRoundTripObfsPluginDropsItsOptions pins a known gap.
//
// Known gap: a Clash shadowsocks node with `plugin: obfs` and
// `plugin-opts: {mode, host}` becomes a sing-box `obfs-local` node whose
// plugin_opts use the `obfs=`/`obfs-host=` spelling. applyShadowsocksPlugin
// only reads `mode`/`host`, so the re-exported Clash node loses the obfs mode
// and the obfs host and keeps a bare `plugin: obfs` (which mihomo reads as an
// obfs plugin without its options).
func TestClashRoundTripObfsPluginDropsItsOptions(t *testing.T) {
	clash := map[string]any{
		"name": "ss-obfs", "type": "ss", "server": "198.51.100.12", "port": 8388,
		"cipher": "aes-256-gcm", "password": "ss-pw",
		"plugin": "obfs", "plugin-opts": map[string]any{"mode": "tls", "host": "bing.com"},
	}
	singbox, proxy, report := clashRoundTrip(t, "ss-obfs", clash)
	if proxy == nil {
		t.Fatalf("ss+obfs is not representable in mihomo: %+v", report.Skipped)
	}
	assertField(t, "sing-box ss-obfs", singbox, "plugin", "obfs-local")
	assertField(t, "sing-box ss-obfs", singbox, "plugin_opts", "obfs=tls;obfs-host=bing.com")
	assertField(t, "clash ss-obfs", proxy, "plugin", "obfs")
	if mode, ok := proxy["obfs"]; ok {
		t.Fatalf("obfs mode survived (%v); the known gap is fixed, flip this test", mode)
	}
	if host, ok := proxy["obfs-host"]; ok {
		t.Fatalf("obfs host survived (%v); the known gap is fixed, flip this test", host)
	}
}

// TestClashRoundTripShadowTLSKeepsTheFrontSNI pins the camouflage host across a
// Clash -> sing-box -> Clash round trip.
//
// The Clash importer stores `plugin-opts.host` (the front/SNI host) in the
// shadowtls dependency's `tls.server_name`. clashChainProxy used to emit the
// dependency's `server` (the address the node connects to) as `plugin-opts.host`
// instead, so the camouflage host was replaced by the server address and the
// server saw the wrong SNI.
func TestClashRoundTripShadowTLSKeepsTheFrontSNI(t *testing.T) {
	clash := map[string]any{
		"name": "ss-shadowtls", "type": "ss", "server": "198.51.100.20", "port": 443,
		"cipher": "aes-256-gcm", "password": "chain-pw",
		"plugin": "shadow-tls",
		"plugin-opts": map[string]any{
			"host": "front.example.com", "password": "stls-pw", "version": 3,
		},
	}
	body, err := json.Marshal(map[string]any{"proxies": []any{clash}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result, err := subscription.ParseWithReport(body)
	if err != nil || len(result.Nodes) != 1 {
		t.Fatalf("parse: %v %+v", err, result.Skipped)
	}
	doc, err := ParseNodeDoc(result.Nodes[0].RawOptions)
	if err != nil {
		t.Fatalf("parse chain document: %v", err)
	}
	if !doc.Chain || len(doc.Deps) != 1 {
		t.Fatalf("expected a one hop chain, got chain=%v deps=%d", doc.Chain, len(doc.Deps))
	}
	dep, err := objectMap(doc.Deps[0])
	if err != nil {
		t.Fatalf("decode hop: %v", err)
	}
	assertField(t, "sing-box hop", dep, "type", "shadowtls")
	assertField(t, "sing-box hop", mapObject(dep, "tls"), "server_name", "front.example.com")

	proxy, ok := clashChainProxy(preparedItem{Doc: doc})
	if !ok {
		t.Fatal("the ss+shadow-tls chain is not representable in mihomo")
	}
	assertField(t, "clash chain", proxy, "plugin", "shadow-tls")
	opts := mapObject(proxy, "plugin-opts")
	assertField(t, "clash chain options", opts, "password", "stls-pw")
	// The front SNI survives: the camouflage host, not the server address.
	assertField(t, "clash chain options", opts, "host", "front.example.com")
}

// TestClashRoundTripHysteria2PortsAndRatesDrift pins two known drifts.
//
// Known gap: the importer normalizes a Clash port list ("443,8443") into
// sing-box's "start:end" form and its Mbps strings into up_mbps/down_mbps
// numbers, and clashHysteria2 writes those back verbatim. The Clash `ports`
// value therefore changes spelling, and `up`/`down` come back as numbers where
// hysteria (v1) comes back as "30 Mbps" strings.
func TestClashRoundTripHysteria2PortsAndRatesDrift(t *testing.T) {
	clash := map[string]any{
		"name": "hy2", "type": "hysteria2", "server": "hy2.example.net", "port": 443,
		"password": "hy2-pw", "sni": "hy2.example.net",
		"ports": "443,8443", "up": "30 Mbps", "down": "200 Mbps",
	}
	singbox, proxy, report := clashRoundTrip(t, "hy2", clash)
	if proxy == nil {
		t.Fatalf("hysteria2 is not representable in mihomo: %+v", report.Skipped)
	}
	assertField(t, "sing-box hy2", singbox, "server_ports", []any{"443:443", "8443:8443"})
	assertField(t, "sing-box hy2", singbox, "up_mbps", float64(30))
	assertField(t, "sing-box hy2", singbox, "down_mbps", float64(200))

	assertField(t, "clash hy2", proxy, "ports", "443:443,8443:8443")
	assertField(t, "clash hy2", proxy, "up", 30)
	assertField(t, "clash hy2", proxy, "down", 200)
}

// TestClashRoundTripWireGuardReservedSurvives pins the reserved byte triple.
//
// The canonical sing-box endpoint form carries `reserved` as the base64 string
// internal/node serializes []uint8 into, while clashWireGuard used to read it as a
// JSON number array only. The byte triple was therefore dropped from a Clash
// export, which breaks the WARP style peers that need it.
func TestClashRoundTripWireGuardReservedSurvives(t *testing.T) {
	clash := map[string]any{
		"name": "wg", "type": "wireguard", "server": "203.0.113.9", "port": 51820,
		"private-key": "WG-PRIVATE", "public-key": "WG-PUBLIC",
		"ip": "10.7.0.2/32", "reserved": []any{1, 2, 3},
	}
	body, err := json.Marshal(map[string]any{"proxies": []any{clash}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result, err := subscription.ParseWithReport(body)
	if err != nil || len(result.Nodes) != 1 {
		t.Fatalf("parse: %v %+v", err, result.Skipped)
	}
	doc, err := ParseNodeDoc(result.Nodes[0].RawOptions)
	if err != nil {
		t.Fatalf("parse wireguard document: %v", err)
	}
	peers := mapObjectSlice(mustClashObject(t, doc.Main), "peers")
	if len(peers) != 1 {
		t.Fatalf("expected one peer, got %d", len(peers))
	}
	// The exporter consumes this normalized endpoint form, where reserved is a
	// base64 string, not the number array mapUintSlice expects.
	assertField(t, "sing-box wireguard peer", peers[0], "reserved", "AQID")
	assertField(t, "sing-box wireguard peer", peers[0], "address", "203.0.113.9")
	assertField(t, "sing-box wireguard peer", peers[0], "port", float64(51820))

	_, proxy, report := clashRoundTrip(t, "wg", clash)
	if proxy == nil {
		t.Fatalf("wireguard is not representable in mihomo: %+v", report.Skipped)
	}
	assertField(t, "clash wireguard", proxy, "public-key", "WG-PUBLIC")
	assertField(t, "clash wireguard", proxy, "reserved", []any{1, 2, 3})
}

// mustClashObject decodes a raw JSON object and fails on error.
func mustClashObject(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	object, err := objectMap(raw)
	if err != nil {
		t.Fatalf("decode object: %v", err)
	}
	return object
}

// --- 4. contract notes -----------------------------------------------------

// TestClashProxyTypeNamesAreMihomoTypes guards the emitted `type` values
// against the mihomo proxy list recorded in docs/plan/PRISM_PLAN_FULL.md (F12,
// verified against mihomo v1.19.31 adapter/parser.go). A mihomo config with an
// openconnect used to be emitted as a proxy whose type mihomo does not know,
// which makes mihomo refuse the whole document. clashProxyFromSingbox now
// refuses it outright, so it is asserted in TestClashOpenConnectIsRefused.
// Known gap: openconnect is not in the mihomo list. It is flagged as false on
// purpose; drop the entry from clashProxyFromSingbox and flip the flag once
// that is fixed.
func TestClashProxyTypeNamesAreMihomoTypes(t *testing.T) {
	mihomoTypes := map[string]bool{
		"anytls": true, "direct": true, "dns": true, "easytier": true, "gost-relay": true,
		"http": true, "hysteria": true, "hysteria2": true, "masque": true, "mieru": true,
		"openvpn": true, "reject": true, "rematch": true, "shadowquic": true, "snell": true,
		"socks5": true, "ss": true, "ssr": true, "ssh": true, "sudoku": true, "tailscale": true,
		"trojan": true, "trusttunnel": true, "tuic": true, "vless": true, "vmess": true,
		"wireguard": true, "zerotier": true,
	}
	cases := []struct {
		outbound   string
		raw        string
		mihomoType bool
	}{
		{"shadowsocks", `{"type":"shadowsocks","server":"1.2.3.4","server_port":1,"method":"aes-256-gcm","password":"pw"}`, true},
		{"vmess", `{"type":"vmess","server":"1.2.3.4","server_port":1,"uuid":"UUID"}`, true},
		{"vless", `{"type":"vless","server":"1.2.3.4","server_port":1,"uuid":"UUID"}`, true},
		{"trojan", `{"type":"trojan","server":"1.2.3.4","server_port":1,"password":"pw"}`, true},
		{"hysteria2", `{"type":"hysteria2","server":"1.2.3.4","server_port":1,"password":"pw"}`, true},
		{"hysteria", `{"type":"hysteria","server":"1.2.3.4","server_port":1}`, true},
		{"tuic", `{"type":"tuic","server":"1.2.3.4","server_port":1,"uuid":"UUID"}`, true},
		{"anytls", `{"type":"anytls","server":"1.2.3.4","server_port":1,"password":"pw"}`, true},
		{"ssh", `{"type":"ssh","server":"1.2.3.4","server_port":1}`, true},
		{"socks", `{"type":"socks","server":"1.2.3.4","server_port":1}`, true},
		{"http", `{"type":"http","server":"1.2.3.4","server_port":1}`, true},
		{"wireguard", `{"type":"wireguard","private_key":"PRIV","peers":[{"address":"1.2.3.4","port":1,"public_key":"PUB"}]}`, true},
		{"snell", `{"type":"snell","server":"1.2.3.4","server_port":1,"psk":"psk","version":4}`, true},
		{"openvpn-client", `{"type":"openvpn-client","servers":[{"server":"1.2.3.4","server_port":1}],"tls":{}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.outbound, func(t *testing.T) {
			proxy := mustClashProxy(t, tc.raw)
			clashType, _ := proxy["type"].(string)
			if clashType == "" {
				t.Fatalf("%s produced no type: %#v", tc.outbound, proxy)
			}
			if strings.ContainsAny(clashType, " ") {
				t.Fatalf("%s maps to a malformed type %q", tc.outbound, clashType)
			}
			known := mihomoTypes[clashType]
			if known != tc.mihomoType {
				t.Fatalf("%s maps to type %q: mihomo type = %v, want %v (docs/plan/PRISM_PLAN_FULL.md F12)",
					tc.outbound, clashType, known, tc.mihomoType)
			}
		})
	}
}
