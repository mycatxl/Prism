package export

import (
	"encoding/json"
	"testing"
	"time"

	"prism/internal/node"
	"prism/internal/quality"
)

// Test fixtures (WP11 §5). They never touch the network: every node document is
// a literal and every expected output is compared against a golden file under
// testdata/.

// fixtureNodes returns the node documents the format tests use. Every document
// carries a deliberate credential so the "no credential leaks" tests have
// something to search for.
func fixtureNodes(t *testing.T) []Item {
	t.Helper()
	return []Item{
		{
			Name:       "ss-tokyo",
			Hash:       "11111111111111111111111111111111",
			Region:     "NRT",
			LatencyMs:  42.5,
			RawOptions: json.RawMessage(`{"type":"shadowsocks","tag":"ss-tokyo","server":"198.51.100.10","server_port":8388,"method":"aes-256-gcm","password":"ss-secret-credential"}`),
			Intel:      intelFixture(),
		},
		{
			Name:       "vmess-ws",
			Hash:       "22222222222222222222222222222222",
			RawOptions: json.RawMessage(`{"type":"vmess","tag":"vmess-ws","server":"vmess.example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555","alter_id":0,"security":"auto","tls":{"enabled":true,"server_name":"vmess.example.com"},"transport":{"type":"ws","path":"/ws","headers":{"Host":"vmess.example.com"}}}`),
		},
		{
			Name:       "vless-reality",
			Hash:       "33333333333333333333333333333333",
			RawOptions: json.RawMessage(`{"type":"vless","tag":"vless-reality","server":"vless.example.com","server_port":443,"uuid":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","flow":"xtls-rprx-vision","tls":{"enabled":true,"server_name":"www.example.com","utls":{"enabled":true,"fingerprint":"chrome"},"reality":{"enabled":true,"public_key":"PUBLIC-KEY-FIXTURE","short_id":"0123abcd"}}}`),
		},
		{
			Name:       "trojan-1",
			Hash:       "44444444444444444444444444444444",
			RawOptions: json.RawMessage(`{"type":"trojan","tag":"trojan-1","server":"trojan.example.org","server_port":8443,"password":"trojan-secret-credential","tls":{"enabled":true,"server_name":"trojan.example.org"}}`),
		},
		{
			Name:       "hy2-1",
			Hash:       "55555555555555555555555555555555",
			RawOptions: json.RawMessage(`{"type":"hysteria2","tag":"hy2-1","server":"hy2.example.net","server_port":443,"password":"hy2-secret-credential","tls":{"enabled":true,"server_name":"hy2.example.net"},"obfs":{"type":"salamander","password":"obfs-secret"}}`),
		},
		{
			Name:       "tuic-1",
			Hash:       "66666666666666666666666666666666",
			RawOptions: json.RawMessage(`{"type":"tuic","tag":"tuic-1","server":"tuic.example.org","server_port":443,"uuid":"bbbbbbbb-cccc-dddd-eeee-ffffffffffff","password":"tuic-secret-credential","tls":{"enabled":true,"server_name":"tuic.example.org"}}`),
		},
		{
			Name:       "anytls-1",
			Hash:       "77777777777777777777777777777777",
			RawOptions: json.RawMessage(`{"type":"anytls","tag":"anytls-1","server":"anytls.example.com","server_port":443,"password":"anytls-secret-credential","tls":{"enabled":true,"server_name":"anytls.example.com"}}`),
		},
		{
			Name:       "socks-1",
			Hash:       "88888888888888888888888888888888",
			RawOptions: json.RawMessage(`{"type":"socks","tag":"socks-1","server":"socks.example.com","server_port":1080,"username":"socks-user","password":"socks-secret-credential"}`),
		},
		{
			Name:       "http-1",
			Hash:       "99999999999999999999999999999999",
			RawOptions: json.RawMessage(`{"type":"http","tag":"http-1","server":"http.example.com","server_port":8080,"username":"http-user","password":"http-secret-credential"}`),
		},
	}
}

// fixtureEndpointNode returns a form B wireguard endpoint node.
func fixtureEndpointNode(t *testing.T) Item {
	t.Helper()
	raw := json.RawMessage(`{"prism_node":1,"engine":"singbox","kind":"endpoint","name":"wg-1","main":{"type":"wireguard","address":["10.7.0.2/32"],"private_key":"WG-PRIVATE-KEY-FIXTURE","peers":[{"address":"203.0.113.9","port":51820,"public_key":"WG-PUBLIC-KEY-FIXTURE","allowed_ips":["0.0.0.0/0"]}]}}`)
	if !node.IsEnvelope(raw) {
		t.Fatal("wireguard fixture is not an envelope")
	}
	if _, err := node.ParseNodeDoc(raw); err != nil {
		t.Fatalf("wireguard fixture does not parse: %v", err)
	}
	return Item{Name: "wg-1", Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RawOptions: raw}
}

// fixtureChainNode returns a form B shadowsocks + shadowtls chain node.
func fixtureChainNode(t *testing.T) Item {
	t.Helper()
	raw := json.RawMessage(`{"prism_node":1,"engine":"singbox","kind":"chain","name":"ss-shadowtls","main":{"type":"shadowsocks","server":"198.51.100.20","server_port":443,"method":"aes-256-gcm","password":"chain-secret-credential","detour":"d0"},"deps":[{"type":"shadowtls","tag":"d0","server":"198.51.100.20","server_port":443,"version":3,"password":"shadowtls-secret-credential"}]}`)
	if _, err := node.ParseNodeDoc(raw); err != nil {
		t.Fatalf("chain fixture does not parse: %v", err)
	}
	return Item{Name: "ss-shadowtls", Hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", RawOptions: raw}
}

// fixtureMihomoNode returns a form B mihomo proxy node (the only shape that can
// carry an SSR node, since Prism has no SSR runtime kernel).
func fixtureMihomoNode(t *testing.T) Item {
	t.Helper()
	raw := json.RawMessage(`{"prism_node":1,"engine":"mihomo","kind":"proxy","proxy":{"type":"ssr","name":"ssr-1","server":"198.51.100.30","port":8080,"cipher":"aes-256-cfb","password":"ssr-secret-credential","protocol":"origin","obfs":"plain"}}`)
	if _, err := node.ParseNodeDoc(raw); err != nil {
		t.Fatalf("mihomo fixture does not parse: %v", err)
	}
	return Item{Name: "ssr-1", Hash: "cccccccccccccccccccccccccccccccc", RawOptions: raw}
}

// intelFixture is a settled WP10 summary with evidence from two sources.
func intelFixture() *quality.Summary {
	score := 96
	confidence := 80
	native := true
	risk := 4
	return &quality.Summary{
		IP:    "198.51.100.10",
		State: "valid",
		Evidence: &quality.Evidence{
			IP:               "198.51.100.10",
			Provider:         quality.ProviderID,
			IPType:           "residential",
			ASN:              "AS64500",
			Organization:     "Example Residential ISP",
			CountryCode:      "JP",
			City:             "Tokyo",
			Region:           "NRT",
			Native:           &native,
			RiskScore:        &risk,
			Grade:            "low",
			SourceConfidence: &confidence,
			ObservedAt:       time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC),
			ValidUntil:       time.Date(2026, 12, 24, 12, 0, 0, 0, time.UTC),
		},
		Assessment: &quality.Assessment{
			State:       "valid",
			PurityScore: &score,
			PurityBand:  "excellent",
			NetworkType: "residential",
			Native:      &native,
			Verdict:     "favorable",
			Reasons:     []string{},
		},
		Sources: []quality.SourceSummary{
			{
				Provider: quality.ProviderID,
				State:    "valid",
				Evidence: &quality.Evidence{
					Provider:         quality.ProviderID,
					IP:               "198.51.100.10",
					IPType:           "residential",
					Grade:            "low",
					RiskScore:        &risk,
					SourceConfidence: &confidence,
					ObservedAt:       time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC),
				},
			},
		},
	}
}
