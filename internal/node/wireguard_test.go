package node

import (
	"encoding/json"
	"reflect"
	"testing"
)

// wireGuardOptions decodes a JSON option document the way the sing-box/Clash
// parsers do (numbers as float64), so the tests exercise the real codec path.
// The local address is always present: WireGuardEndpointFromOptions refuses an
// endpoint without one, and it is not what these tests are about.
func wireGuardOptions(t *testing.T, extra string) map[string]any {
	t.Helper()
	raw := `{"server":"203.0.113.9","server_port":51820,"address":"10.7.0.2/32",` +
		`"private_key":"KEY","public_key":"PUB"` + extra + `}`
	var options map[string]any
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		t.Fatalf("decode options: %v", err)
	}
	return options
}

// TestWireGuardPeerOptions_AllowedIPsCommaJoined is the F2 regression test at
// the parser boundary. The wireguard share link writes every allowed-ips entry
// as ONE comma-joined query value (export/uri.go), so the parser must split it
// before normalizing; before the fix "0.0.0.0/0,::/0" reached
// netip.ParsePrefix and the whole node was dropped as INVALID.
func TestWireGuardPeerOptions_AllowedIPsCommaJoined(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "share link comma-joined default",
			raw:  `,"allowed_ips":"0.0.0.0/0,::/0"`,
			want: []string{"0.0.0.0/0", "::/0"},
		},
		{
			name: "comma-joined with spaces and a host prefix",
			raw:  `,"allowed_ips":"0.0.0.0/0, ::/0 , 10.7.0.9"`,
			want: []string{"0.0.0.0/0", "::/0", "10.7.0.9/32"},
		},
		{
			name: "single value unchanged",
			raw:  `,"allowed_ips":"0.0.0.0/0"`,
			want: []string{"0.0.0.0/0"},
		},
		{
			name: "array of single values unchanged",
			raw:  `,"allowed_ips":["0.0.0.0/0","::/0"]`,
			want: []string{"0.0.0.0/0", "::/0"},
		},
		{
			name: "array with a comma-joined entry",
			raw:  `,"allowed_ips":["0.0.0.0/0,::/0"]`,
			want: []string{"0.0.0.0/0", "::/0"},
		},
		{
			name: "missing allowed_ips falls back to the default",
			raw:  ``,
			want: DefaultWireGuardAllowedIPs(),
		},
		{
			name: "trailing comma does not produce an empty entry",
			raw:  `,"allowed_ips":"0.0.0.0/0,"`,
			want: []string{"0.0.0.0/0"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, err := WireGuardEndpointFromOptions(wireGuardOptions(t, tc.raw))
			if err != nil {
				t.Fatalf("WireGuardEndpointFromOptions: %v", err)
			}
			if len(endpoint.Peers) != 1 {
				t.Fatalf("peers = %+v", endpoint.Peers)
			}
			if got := endpoint.Peers[0].AllowedIPs; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("allowed_ips = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWireGuardPeerOptions_AllowedIPsInvalidStillRejected keeps the fail-closed
// behaviour: a comma-joined list with a bad member must not be silently
// truncated into a partial list.
func TestWireGuardPeerOptions_AllowedIPsInvalidStillRejected(t *testing.T) {
	for _, raw := range []string{
		`,"allowed_ips":"0.0.0.0/0,not-a-prefix"`,
		`,"allowed_ips":"not-a-prefix"`,
		`,"allowed_ips":"0.0.0.0/33"`,
		`,"allowed_ips":","`,
	} {
		if _, err := WireGuardEndpointFromOptions(wireGuardOptions(t, raw)); err == nil {
			t.Fatalf("expected an INVALID error for %s", raw)
		}
	}
}

// TestWireGuardPeerOptions_AllowedIPsKeySpellings checks every spelling the
// reader accepts carries the comma-joined form, so the F2 fix covers the Clash
// (`allowed-ips`), sing-box (`allowed_ips`) and camelCase keys alike.
func TestWireGuardPeerOptions_AllowedIPsKeySpellings(t *testing.T) {
	for _, key := range []string{"allowed_ips", "allowed-ips", "allowedIPs"} {
		t.Run(key, func(t *testing.T) {
			endpoint, err := WireGuardEndpointFromOptions(
				wireGuardOptions(t, `,"`+key+`":"0.0.0.0/0,::/0"`))
			if err != nil {
				t.Fatalf("WireGuardEndpointFromOptions: %v", err)
			}
			if got := endpoint.Peers[0].AllowedIPs; !reflect.DeepEqual(got, []string{"0.0.0.0/0", "::/0"}) {
				t.Fatalf("%s = %v, want [0.0.0.0/0 ::/0]", key, got)
			}
		})
	}
}

// TestWireGuardEndpointFromLegacyOutbound_AllowedIPsCommaJoined covers the
// form A path: node/envelope.go converts a legacy sing-box wireguard outbound
// through the same option reader, so the fix must apply there too.
func TestWireGuardEndpointFromLegacyOutbound_AllowedIPsCommaJoined(t *testing.T) {
	legacy := []byte(`{"type":"wireguard","server":"203.0.113.9","server_port":51820,` +
		`"private_key":"KEY","peer_public_key":"PUB","local_address":["10.7.0.2/32"],` +
		`"allowed_ips":"0.0.0.0/0,::/0"}`)
	endpoint, err := WireGuardEndpointFromLegacyOutbound(legacy)
	if err != nil {
		t.Fatalf("WireGuardEndpointFromLegacyOutbound: %v", err)
	}
	if !reflect.DeepEqual(endpoint.Peers[0].AllowedIPs, []string{"0.0.0.0/0", "::/0"}) {
		t.Fatalf("allowed_ips = %v, want [0.0.0.0/0 ::/0]", endpoint.Peers[0].AllowedIPs)
	}
}

// TestParseWireGuardURI_ExportedLinkShape parses the exact link the exporter
// produces (export/uri.go) and pins every field of the wireguard row, so a
// future change to either side of the contract shows up here.
func TestParseWireGuardURI_ExportedLinkShape(t *testing.T) {
	const link = "wireguard://WG-PRIVATE-KEY-FIXTURE@203.0.113.9:51820/?address=10.7.0.2%2F32&" +
		"allowedips=0.0.0.0%2F0%2C%3A%3A%2F0&publickey=WG-PUBLIC-KEY-FIXTURE#wg-1"

	name, endpoint, ok := ParseWireGuardURI(link)
	if !ok {
		t.Fatal("the exported wireguard link did not parse")
	}
	if name != "wg-1" {
		t.Fatalf("name = %q", name)
	}
	if endpoint.PrivateKey != "WG-PRIVATE-KEY-FIXTURE" {
		t.Fatalf("private key = %q", endpoint.PrivateKey)
	}
	if !reflect.DeepEqual(endpoint.Address, []string{"10.7.0.2/32"}) {
		t.Fatalf("address = %v", endpoint.Address)
	}
	if len(endpoint.Peers) != 1 {
		t.Fatalf("peers = %+v", endpoint.Peers)
	}
	peer := endpoint.Peers[0]
	if peer.Address != "203.0.113.9" || peer.Port != 51820 || peer.PublicKey != "WG-PUBLIC-KEY-FIXTURE" {
		t.Fatalf("peer = %+v", peer)
	}
	if !reflect.DeepEqual(peer.AllowedIPs, []string{"0.0.0.0/0", "::/0"}) {
		t.Fatalf("allowed_ips = %v, want [0.0.0.0/0 ::/0]", peer.AllowedIPs)
	}
}

// TestParseWireGuardURI_OptionalFields covers the other comma-carrying fields of
// the share link, so the F2 fix cannot regress them.
func TestParseWireGuardURI_OptionalFields(t *testing.T) {
	const link = "wireguard://PRIV@203.0.113.9:51820?publickey=PUB&presharedkey=PSK&" +
		"address=10.7.0.2%2F32%2Cfd01%3A%3A1%2F128&allowedips=0.0.0.0%2F0&reserved=209%2C98%2C59&" +
		"keepalive=25&mtu=1280#wg-1"

	_, endpoint, ok := ParseWireGuardURI(link)
	if !ok {
		t.Fatal("link with optional fields did not parse")
	}
	if !reflect.DeepEqual(endpoint.Address, []string{"10.7.0.2/32", "fd01::1/128"}) {
		t.Fatalf("address = %v", endpoint.Address)
	}
	if endpoint.MTU != 1280 {
		t.Fatalf("mtu = %d", endpoint.MTU)
	}
	peer := endpoint.Peers[0]
	if peer.PreSharedKey != "PSK" {
		t.Fatalf("pre_shared_key = %q", peer.PreSharedKey)
	}
	if !reflect.DeepEqual(peer.AllowedIPs, []string{"0.0.0.0/0"}) {
		t.Fatalf("allowed_ips = %v", peer.AllowedIPs)
	}
	if !reflect.DeepEqual(peer.Reserved, []uint8{209, 98, 59}) {
		t.Fatalf("reserved = %v", peer.Reserved)
	}
	if peer.PersistentKeepaliveInterval != 25 {
		t.Fatalf("keepalive = %d", peer.PersistentKeepaliveInterval)
	}
}

// TestParseWireGuardURI_DefaultAllowedIPs pins the default: a link without
// allowedips must produce exactly the default list.
func TestParseWireGuardURI_DefaultAllowedIPs(t *testing.T) {
	_, endpoint, ok := ParseWireGuardURI(
		"wireguard://PRIV@203.0.113.9:51820?publickey=PUB&address=10.7.0.2%2F32#wg-1")
	if !ok {
		t.Fatal("link without allowedips did not parse")
	}
	if !reflect.DeepEqual(endpoint.Peers[0].AllowedIPs, DefaultWireGuardAllowedIPs()) {
		t.Fatalf("allowed_ips = %v, want the default", endpoint.Peers[0].AllowedIPs)
	}
}

// TestSplitWireGuardPrefixEntries pins the helper on its own, including the
// pass-through path that keeps single-value documents byte-identical.
func TestSplitWireGuardPrefixEntries(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, []string{}},
		{"single entry untouched", []string{"0.0.0.0/0"}, []string{"0.0.0.0/0"}},
		{"joined pair", []string{"0.0.0.0/0,::/0"}, []string{"0.0.0.0/0", "::/0"}},
		{"padding preserved for the normalizer", []string{" 0.0.0.0/0 , ::/0 "}, []string{" 0.0.0.0/0 ", " ::/0 "}},
		{"empty parts dropped", []string{"0.0.0.0/0,,::/0,"}, []string{"0.0.0.0/0", "::/0"}},
		{"mixed list", []string{"10.0.0.0/8", "0.0.0.0/0,::/0"}, []string{"10.0.0.0/8", "0.0.0.0/0", "::/0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitWireGuardPrefixEntries(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("splitWireGuardPrefixEntries(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
