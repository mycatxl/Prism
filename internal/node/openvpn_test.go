package node

import (
	"strings"
	"testing"
)

// The profile text fixtures below are the smallest inputs that reach the branch
// under test. A .ovpn profile is customer-supplied, so every refusal in this
// file is a boundary: what the endpoint cannot express has to be named at parse
// time instead of failing inside sing-box on the data plane.

const openVPNTestCA = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

// openVPNTestFingerprint is one SHA-256 digest written the way OpenVPN prints
// it, and openVPNTestDigest is the same digest in the bare form the runtime
// pins against.
const openVPNTestFingerprint = "00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF"

const openVPNTestDigest = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

// tlsProfile is a profile that parses: one remote, an inline CA, no surprises.
func tlsProfile(extra string) string {
	return "client\n" +
		"dev tun\n" +
		"proto udp\n" +
		"remote vpn.example.com 1194\n" +
		"<ca>\n" + openVPNTestCA + "</ca>\n" +
		extra
}

func mustParseOpenVPN(t *testing.T, text string) OpenVPNProfile {
	t.Helper()
	profile, err := ParseOpenVPNProfile(text, "profile", "", "")
	if err != nil {
		t.Fatalf("ParseOpenVPNProfile: unexpected error: %v", err)
	}
	return profile
}

// parseOpenVPNError asserts the parse was refused and returns the typed error.
func parseOpenVPNError(t *testing.T, text string) *OpenVPNParseError {
	t.Helper()
	_, err := ParseOpenVPNProfile(text, "profile", "", "")
	if err == nil {
		t.Fatal("ParseOpenVPNProfile: expected a refusal, got none")
	}
	parseErr, ok := err.(*OpenVPNParseError)
	if !ok {
		t.Fatalf("ParseOpenVPNProfile: error is %T, want *OpenVPNParseError", err)
	}
	return parseErr
}

// TestParseOpenVPNProfile_MinimalTLS pins the shape of the option document the
// endpoint is built from. Everything downstream (node rendering, hashing) reads
// these keys, so a rename here is a silent data-plane break rather than a
// compile error.
func TestParseOpenVPNProfile_MinimalTLS(t *testing.T) {
	profile := mustParseOpenVPN(t, tlsProfile(""))

	if profile.Name != "profile" {
		t.Errorf("Name = %q, want %q", profile.Name, "profile")
	}
	if profile.Remote != "vpn.example.com" {
		t.Errorf("Remote = %q, want %q", profile.Remote, "vpn.example.com")
	}
	if profile.Detail() != "" {
		t.Errorf("Detail() = %q, want empty", profile.Detail())
	}
	if got := profile.Options["network"]; got != "udp" {
		t.Errorf("options[network] = %v, want udp", got)
	}

	servers, ok := profile.Options["servers"].([]map[string]any)
	if !ok || len(servers) != 1 {
		t.Fatalf("options[servers] = %#v, want one server", profile.Options["servers"])
	}
	if servers[0]["server"] != "vpn.example.com" {
		t.Errorf("servers[0][server] = %v", servers[0]["server"])
	}
	if servers[0]["server_port"] != uint16(1194) {
		t.Errorf("servers[0][server_port] = %v, want 1194", servers[0]["server_port"])
	}

	tls, ok := profile.Options["tls"].(map[string]any)
	if !ok {
		t.Fatalf("options[tls] = %#v, want a map", profile.Options["tls"])
	}
	certificate, ok := tls["certificate"].([]string)
	if !ok || len(certificate) != 1 {
		t.Fatalf("tls[certificate] = %#v, want one entry", tls["certificate"])
	}
	if !strings.HasPrefix(certificate[0], "-----BEGIN CERTIFICATE-----") {
		t.Errorf("tls[certificate][0] does not start with the PEM header: %q", certificate[0])
	}
	if _, present := tls["client_certificate"]; present {
		t.Error("tls[client_certificate] is set without a <cert> block")
	}
}

// TestParseOpenVPNProfile_NameFallsBackToTheRemote covers the naming rule: a
// bundle profile without a name is named after the host it dials.
func TestParseOpenVPNProfile_NameFallsBackToTheRemote(t *testing.T) {
	profile, err := ParseOpenVPNProfile(tlsProfile(""), "", "", "")
	if err != nil {
		t.Fatalf("ParseOpenVPNProfile: %v", err)
	}
	if want := "openvpn-vpn.example.com"; profile.Name != want {
		t.Errorf("Name = %q, want %q", profile.Name, want)
	}
}

// TestParseOpenVPNProfile_RefusesServerOnlyAndHostDirectives walks the rejection
// table. Each of these either has no endpoint equivalent or would make Prism
// read a file from the host it runs on.
func TestParseOpenVPNProfile_RefusesServerOnlyAndHostDirectives(t *testing.T) {
	cases := []string{
		"pkcs12", "http-proxy", "socks-proxy", "plugin", "askpass",
		"management", "ifconfig-pool-persist", "server", "client-config-dir",
		"mode", "push",
	}
	for _, directive := range cases {
		t.Run(directive, func(t *testing.T) {
			parseErr := parseOpenVPNError(t, tlsProfile(directive+" value\n"))
			if parseErr.Reason != ReasonUnsupportedFeature {
				t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonUnsupportedFeature)
			}
			if !strings.Contains(parseErr.Detail, directive) {
				t.Errorf("Detail = %q, want it to name %q", parseErr.Detail, directive)
			}
		})
	}
}

// TestParseOpenVPNProfile_RefusesFilePaths is the host-filesystem boundary: key
// material has to be inline. "[inline]" is the OpenVPN spelling for "the block
// follows", so it must be accepted rather than mistaken for a path.
func TestParseOpenVPNProfile_RefusesFilePaths(t *testing.T) {
	cases := []struct {
		name      string
		directive string
	}{
		{name: "ca", directive: "ca /etc/openvpn/ca.crt"},
		{name: "cert", directive: "cert /etc/openvpn/client.crt"},
		{name: "key", directive: "key /etc/openvpn/client.key"},
		{name: "tls-auth", directive: "tls-auth /etc/openvpn/ta.key"},
		{name: "tls-crypt", directive: "tls-crypt /etc/openvpn/tc.key"},
		{name: "tls-crypt-v2", directive: "tls-crypt-v2 /etc/openvpn/tc2.key"},
		{name: "auth-user-pass", directive: "auth-user-pass /etc/openvpn/creds.txt"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parseErr := parseOpenVPNError(t, tlsProfile(testCase.directive+"\n"))
			if parseErr.Reason != ReasonUnsupportedFeature {
				t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonUnsupportedFeature)
			}
			if !strings.Contains(parseErr.Detail, testCase.name) {
				t.Errorf("Detail = %q, want it to name %q", parseErr.Detail, testCase.name)
			}
		})
	}
}

// TestParseOpenVPNProfile_AcceptsTheInlinePlaceholder pins the one argument that
// looks like a path and is not one.
func TestParseOpenVPNProfile_AcceptsTheInlinePlaceholder(t *testing.T) {
	text := "client\ndev tun\nremote vpn.example.com 1194\nca [inline]\n" +
		"<ca>\n" + openVPNTestCA + "</ca>\n"
	profile := mustParseOpenVPN(t, text)
	tls, ok := profile.Options["tls"].(map[string]any)
	if !ok {
		t.Fatalf("options[tls] = %#v, want a map", profile.Options["tls"])
	}
	if _, present := tls["certificate"]; !present {
		t.Error("tls[certificate] is missing: [inline] must not disqualify the block")
	}
}

// TestParseOpenVPNProfile_RequiresATrustAnchor covers the check that turns a
// runtime failure into a parse report: the endpoint refuses to build a TLS
// client with neither a CA nor a pinned fingerprint.
func TestParseOpenVPNProfile_RequiresATrustAnchor(t *testing.T) {
	text := "client\ndev tun\nremote vpn.example.com 1194\n"
	parseErr := parseOpenVPNError(t, text)
	if parseErr.Reason != ReasonInvalid {
		t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonInvalid)
	}
	if !strings.Contains(parseErr.Detail, "peer-fingerprint") {
		t.Errorf("Detail = %q, want it to mention peer-fingerprint", parseErr.Detail)
	}
}

// TestParseOpenVPNProfile_FingerprintReplacesTheCA is the other half of that
// rule, and it pins the colon handling: OpenVPN prints a SHA-256 digest with a
// colon between every byte, while the runtime pins against the bare digest and
// compares it whole.
func TestParseOpenVPNProfile_FingerprintReplacesTheCA(t *testing.T) {
	text := "client\ndev tun\nremote vpn.example.com 1194\n" +
		"peer-fingerprint " + openVPNTestFingerprint + "\n"
	profile := mustParseOpenVPN(t, text)
	tls, ok := profile.Options["tls"].(map[string]any)
	if !ok {
		t.Fatalf("options[tls] = %#v, want a map", profile.Options["tls"])
	}
	fingerprints, ok := tls["peer_fingerprint"].([]string)
	if !ok || len(fingerprints) != 1 {
		t.Fatalf("tls[peer_fingerprint] = %#v, want one entry", tls["peer_fingerprint"])
	}
	if fingerprints[0] != openVPNTestDigest {
		t.Errorf("tls[peer_fingerprint][0] = %q, want %q", fingerprints[0], openVPNTestDigest)
	}
}

// TestParseOpenVPNProfile_RefusesUnterminatedBlock covers a truncated profile:
// the parser must not silently treat the rest of the file as a directive list.
func TestParseOpenVPNProfile_RefusesUnterminatedBlock(t *testing.T) {
	text := "client\ndev tun\nremote vpn.example.com 1194\n<ca>\n" + openVPNTestCA
	parseErr := parseOpenVPNError(t, text)
	if parseErr.Reason != ReasonInvalid {
		t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonInvalid)
	}
	if !strings.Contains(parseErr.Detail, "unterminated") {
		t.Errorf("Detail = %q, want it to say unterminated", parseErr.Detail)
	}
}

// TestParseOpenVPNProfile_RefusesOversizedProfile covers the size bound. The
// limit exists so one subscription cannot make the parser hold an unbounded
// string in memory.
func TestParseOpenVPNProfile_RefusesOversizedProfile(t *testing.T) {
	text := strings.Repeat("x", maxOpenVPNProfileBytes+1)
	parseErr := parseOpenVPNError(t, text)
	if parseErr.Reason != ReasonInvalid {
		t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonInvalid)
	}
	if !strings.Contains(parseErr.Detail, "exceeds") {
		t.Errorf("Detail = %q, want it to say exceeds", parseErr.Detail)
	}
}

// TestParseOpenVPNProfile_RefusesUnsupportedDevice pins that only tun is
// representable: the endpoint always builds a tun device.
func TestParseOpenVPNProfile_RefusesUnsupportedDevice(t *testing.T) {
	text := "client\ndev tap\nremote vpn.example.com 1194\n<ca>\n" + openVPNTestCA + "</ca>\n"
	parseErr := parseOpenVPNError(t, text)
	if parseErr.Reason != ReasonUnsupportedFeature {
		t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonUnsupportedFeature)
	}
	if !strings.Contains(parseErr.Detail, "tap") {
		t.Errorf("Detail = %q, want it to name tap", parseErr.Detail)
	}
}

// TestParseOpenVPNProfile_RequiresARemote covers a profile that names no server:
// without one the endpoint has nothing to dial.
func TestParseOpenVPNProfile_RequiresARemote(t *testing.T) {
	text := "client\ndev tun\n<ca>\n" + openVPNTestCA + "</ca>\n"
	parseErr := parseOpenVPNError(t, text)
	if parseErr.Reason != ReasonInvalid {
		t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonInvalid)
	}
	if !strings.Contains(parseErr.Detail, "remote") {
		t.Errorf("Detail = %q, want it to mention remote", parseErr.Detail)
	}
}

// TestParseOpenVPNProfile_RemoteArguments covers the three argument shapes the
// remote directive accepts and the port precedence between `port` and a
// per-remote value.
func TestParseOpenVPNProfile_RemoteArguments(t *testing.T) {
	cases := []struct {
		name    string
		extra   string
		port    uint16
		network any
	}{
		{name: "host only", extra: "", port: 1194, network: nil},
		{name: "host and port", extra: "remote other.example.com 443\n", port: 443, network: nil},
		{name: "host port and proto", extra: "remote third.example.com 443 tcp\n", port: 443, network: "tcp"},
		{name: "host and proto", extra: "remote fourth.example.com tcp-client\n", port: 1194, network: "tcp"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// The first remote decides the server under test; extras add more.
			profile := mustParseOpenVPN(t, tlsProfile(testCase.extra))
			servers := profile.Options["servers"].([]map[string]any)
			var last map[string]any
			if testCase.extra == "" {
				last = servers[0]
			} else {
				last = servers[len(servers)-1]
			}
			if last["server_port"] != testCase.port {
				t.Errorf("server_port = %v, want %d", last["server_port"], testCase.port)
			}
			if testCase.network == nil {
				if _, present := last["network"]; present {
					t.Errorf("network = %v, want it absent", last["network"])
				}
				return
			}
			if last["network"] != testCase.network {
				t.Errorf("network = %v, want %v", last["network"], testCase.network)
			}
		})
	}
}

// TestParseOpenVPNProfile_StaticKey covers the second mode: psK-only profiles
// carry no TLS material at all and use ifconfig for their addresses.
func TestParseOpenVPNProfile_StaticKey(t *testing.T) {
	text := "dev tun\nremote vpn.example.com 1194\nifconfig 10.8.0.2 10.8.0.1\n" +
		"secret [inline]\n<secret>\n-----BEGIN OpenVPN Static key V1-----\nabcd\n-----END OpenVPN Static key V1-----\n</secret>\n" +
		"key-direction 1\ncipher AES-256-CBC\n"

	profile := mustParseOpenVPN(t, text)
	if profile.Options["mode"] != "static_key" {
		t.Errorf("options[mode] = %v, want static_key", profile.Options["mode"])
	}
	if profile.Options["key_direction"] != "client" {
		t.Errorf("options[key_direction] = %v, want client", profile.Options["key_direction"])
	}
	addresses, ok := profile.Options["address"].([]string)
	if !ok || len(addresses) != 1 || addresses[0] != "10.8.0.2/32" {
		t.Errorf("options[address] = %#v, want [10.8.0.2/32]", profile.Options["address"])
	}
	if profile.Options["peer_address"] != "10.8.0.1" {
		t.Errorf("options[peer_address] = %v, want 10.8.0.1", profile.Options["peer_address"])
	}
	if profile.Options["cipher"] != "AES-256-CBC" {
		t.Errorf("options[cipher] = %v, want AES-256-CBC", profile.Options["cipher"])
	}
	if _, present := profile.Options["tls"]; present {
		t.Error("options[tls] is set in static_key mode")
	}
}

// TestParseOpenVPNProfile_RefusesMixedModes covers the two contradictions the
// endpoint cannot resolve: static key material next to TLS options, and two
// control-wrap keys at once.
func TestParseOpenVPNProfile_RefusesMixedModes(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		fragment string
	}{
		{
			name: "static key with a ca",
			text: "remote vpn.example.com 1194\nifconfig 10.8.0.2 10.8.0.1\n" +
				"secret [inline]\n<secret>\nKEY\n</secret>\n<ca>\n" + openVPNTestCA + "</ca>\n",
			fragment: "static_key",
		},
		{
			name:     "tls-auth and tls-crypt",
			text:     tlsProfile("<tls-auth>\nKEY\n</tls-auth>\n<tls-crypt>\nKEY\n</tls-crypt>\n"),
			fragment: "tls-auth",
		},
		{
			name:     "key-direction without tls-auth",
			text:     tlsProfile("key-direction 1\n"),
			fragment: "key-direction",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parseErr := parseOpenVPNError(t, testCase.text)
			if parseErr.Reason != ReasonInvalid {
				t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonInvalid)
			}
			if !strings.Contains(parseErr.Detail, testCase.fragment) {
				t.Errorf("Detail = %q, want it to mention %q", parseErr.Detail, testCase.fragment)
			}
		})
	}
}

// TestParseOpenVPNProfile_ControlWrap covers the tls-auth direction, which only
// means something next to a tls-auth key.
func TestParseOpenVPNProfile_ControlWrap(t *testing.T) {
	profile := mustParseOpenVPN(t, tlsProfile("<tls-auth>\nKEY\n</tls-auth>\nkey-direction 0\n"))
	tls := profile.Options["tls"].(map[string]any)
	wrap, ok := tls["control_wrap"].(map[string]any)
	if !ok {
		t.Fatalf("tls[control_wrap] = %#v, want a map", tls["control_wrap"])
	}
	if wrap["type"] != "tls_auth" {
		t.Errorf("control_wrap[type] = %v, want tls_auth", wrap["type"])
	}
	if wrap["direction"] != "server" {
		t.Errorf("control_wrap[direction] = %v, want server", wrap["direction"])
	}
}

// TestParseOpenVPNProfile_Credentials covers the three credential sources: an
// inline block, the Prism bundle, and a profile that asks for credentials the
// caller does not have.
func TestParseOpenVPNProfile_Credentials(t *testing.T) {
	t.Run("bundle credentials fill a bare request", func(t *testing.T) {
		profile, err := ParseOpenVPNProfile(tlsProfile("auth-user-pass\n"), "p", "alice", "s3cret")
		if err != nil {
			t.Fatalf("ParseOpenVPNProfile: %v", err)
		}
		if profile.Options["username"] != "alice" || profile.Options["password"] != "s3cret" {
			t.Errorf("credentials = %v / %v, want alice / s3cret",
				profile.Options["username"], profile.Options["password"])
		}
	})

	t.Run("inline credentials win over the bundle", func(t *testing.T) {
		profile, err := ParseOpenVPNProfile(
			tlsProfile("auth-user-pass\n<auth-user-pass>\nbob\ninline-pass\n</auth-user-pass>\n"),
			"p", "alice", "s3cret")
		if err != nil {
			t.Fatalf("ParseOpenVPNProfile: %v", err)
		}
		if profile.Options["username"] != "bob" || profile.Options["password"] != "inline-pass" {
			t.Errorf("credentials = %v / %v, want bob / inline-pass",
				profile.Options["username"], profile.Options["password"])
		}
	})

	t.Run("a request with no credentials is refused", func(t *testing.T) {
		parseErr := parseOpenVPNError(t, tlsProfile("auth-user-pass\n"))
		if parseErr.Reason != ReasonInvalid {
			t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonInvalid)
		}
	})
}

// TestParseOpenVPNProfile_RefusesOutOfRangeNumbers covers the numeric bounds.
// A value the endpoint cannot hold is a parse error, not a silent truncation.
func TestParseOpenVPNProfile_RefusesOutOfRangeNumbers(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "tun-mtu above uint32", text: tlsProfile("tun-mtu 4294967296\n")},
		{name: "mssfix above uint32", text: tlsProfile("mssfix 4294967296\n")},
		{name: "fragment above uint32", text: tlsProfile("fragment 4294967296\n")},
		{name: "ping above uint32", text: tlsProfile("ping 4294967296\n")},
		{name: "ping-restart above uint32", text: tlsProfile("ping-restart 4294967296\n")},
		{name: "reneg-sec above uint32", text: tlsProfile("reneg-sec 4294967296\n")},
		{name: "replay-window above uint32", text: tlsProfile("replay-window 4294967296\n")},
		{name: "keepalive with one value", text: tlsProfile("keepalive 10\n")},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parseErr := parseOpenVPNError(t, testCase.text)
			if parseErr.Reason != ReasonInvalid {
				t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonInvalid)
			}
		})
	}
}

// TestParseOpenVPNProfile_TimingOptions pins the unit conversion: sing-box takes
// durations, the profile takes seconds.
func TestParseOpenVPNProfile_TimingOptions(t *testing.T) {
	profile := mustParseOpenVPN(t, tlsProfile("keepalive 10 60\nreneg-sec 0\nreplay-window 64\n"))
	if profile.Options["ping_interval"] != "10s" {
		t.Errorf("options[ping_interval] = %v, want 10s", profile.Options["ping_interval"])
	}
	if profile.Options["ping_restart"] != "60s" {
		t.Errorf("options[ping_restart] = %v, want 60s", profile.Options["ping_restart"])
	}
	if profile.Options["renegotiate_disabled"] != true {
		t.Errorf("options[renegotiate_disabled] = %v, want true for reneg-sec 0",
			profile.Options["renegotiate_disabled"])
	}
	if profile.Options["replay_window"] != uint64(64) {
		t.Errorf("options[replay_window] = %v, want 64", profile.Options["replay_window"])
	}
}

// TestParseOpenVPNProfile_CipherPrecedence covers the rule that a TLS profile's
// `cipher` becomes the fallback data cipher unless data-ciphers was given, in
// which case it is only a hint and is recorded as a note.
func TestParseOpenVPNProfile_CipherPrecedence(t *testing.T) {
	t.Run("cipher alone becomes the fallback", func(t *testing.T) {
		profile := mustParseOpenVPN(t, tlsProfile("cipher AES-128-CBC\n"))
		if profile.Options["data_ciphers_fallback"] != "AES-128-CBC" {
			t.Errorf("options[data_ciphers_fallback] = %v, want AES-128-CBC",
				profile.Options["data_ciphers_fallback"])
		}
	})

	t.Run("data-ciphers wins and cipher is noted", func(t *testing.T) {
		profile := mustParseOpenVPN(t, tlsProfile("data-ciphers AES-256-GCM:AES-128-GCM\ncipher AES-128-CBC\n"))
		ciphers, ok := profile.Options["data_ciphers"].([]string)
		if !ok || len(ciphers) != 2 {
			t.Fatalf("options[data_ciphers] = %#v, want two entries", profile.Options["data_ciphers"])
		}
		if _, present := profile.Options["data_ciphers_fallback"]; present {
			t.Error("options[data_ciphers_fallback] is set next to data-ciphers")
		}
		if !strings.Contains(profile.Detail(), "AES-128-CBC") {
			t.Errorf("Detail() = %q, want it to record the dropped cipher", profile.Detail())
		}
	})
}

// TestParseOpenVPNProfile_RefusesUnknownDigest pins that a digest sing-box
// cannot name is refused rather than passed through.
func TestParseOpenVPNProfile_RefusesUnknownDigest(t *testing.T) {
	parseErr := parseOpenVPNError(t, tlsProfile("auth WHIRLPOOL\n"))
	if parseErr.Reason != ReasonUnsupportedFeature {
		t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonUnsupportedFeature)
	}
}

// TestParseOpenVPNProfile_Compression covers the two static compression
// directives and the precedence rule against allow-compression.
func TestParseOpenVPNProfile_Compression(t *testing.T) {
	t.Run("comp-lzo and compress land in their own fields", func(t *testing.T) {
		profile := mustParseOpenVPN(t, tlsProfile("comp-lzo adaptive\ncompress lz4-v2\n"))
		if profile.Options["compression_lzo"] != "adaptive" {
			t.Errorf("options[compression_lzo] = %v, want adaptive", profile.Options["compression_lzo"])
		}
		if profile.Options["compression"] != "lz4-v2" {
			t.Errorf("options[compression] = %v, want lz4-v2", profile.Options["compression"])
		}
	})

	t.Run("allow-compression yields to a static setting", func(t *testing.T) {
		profile := mustParseOpenVPN(t, tlsProfile("compress stub-v2\nallow-compression no\n"))
		if profile.Options["compression"] != "stub-v2" {
			t.Errorf("options[compression] = %v, want stub-v2", profile.Options["compression"])
		}
		if _, present := profile.Options["allow_compression"]; present {
			t.Error("options[allow_compression] is set next to compress")
		}
	})

	t.Run("an unknown compression level is refused", func(t *testing.T) {
		parseErr := parseOpenVPNError(t, tlsProfile("compress snappy\n"))
		if parseErr.Reason != ReasonUnsupportedFeature {
			t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonUnsupportedFeature)
		}
	})
}

// TestParseOpenVPNProfile_PullFilters covers the client-side filter list and the
// route-nopull switch.
func TestParseOpenVPNProfile_PullFilters(t *testing.T) {
	profile := mustParseOpenVPN(t, tlsProfile(
		"route-nopull\npull-filter ignore \"dhcp-option DNS\"\npull-filter bogus x\n"))
	if profile.Options["route_no_pull"] != true {
		t.Errorf("options[route_no_pull] = %v, want true", profile.Options["route_no_pull"])
	}
	filters, ok := profile.Options["pull_filters"].([]map[string]any)
	if !ok || len(filters) != 1 {
		t.Fatalf("options[pull_filters] = %#v, want one filter (the bogus action is dropped)",
			profile.Options["pull_filters"])
	}
	if filters[0]["action"] != "ignore" || filters[0]["text"] != "dhcp-option DNS" {
		t.Errorf("pull_filters[0] = %#v", filters[0])
	}
}

// TestParseOpenVPNProfile_TLSVersions covers the version mapping and its refusal
// of a value sing-box does not accept.
func TestParseOpenVPNProfile_TLSVersions(t *testing.T) {
	profile := mustParseOpenVPN(t, tlsProfile("tls-version-min 1.2\ntls-version-max 1.3\n"))
	tls := profile.Options["tls"].(map[string]any)
	if tls["version_min"] != "1.2" || tls["version_max"] != "1.3" {
		t.Errorf("tls versions = %v / %v, want 1.2 / 1.3", tls["version_min"], tls["version_max"])
	}

	parseErr := parseOpenVPNError(t, tlsProfile("tls-version-min 2.0\n"))
	if parseErr.Reason != ReasonUnsupportedFeature {
		t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonUnsupportedFeature)
	}
}

// TestParseOpenVPNProfile_NotesUnmapped covers the directives that are accepted
// and recorded: the parse report has to say what the profile asked for and did
// not get.
func TestParseOpenVPNProfile_NotesUnmapped(t *testing.T) {
	profile := mustParseOpenVPN(t, tlsProfile("route 10.0.0.0 255.0.0.0\n"))
	if !strings.Contains(profile.Detail(), "route") {
		t.Errorf("Detail() = %q, want it to record the ignored route directive", profile.Detail())
	}
}

// TestTokenizeOpenVPNLine covers the quote handling: a value may contain spaces
// when it is quoted, and a quoted empty string is still a field.
func TestTokenizeOpenVPNLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []string
	}{
		{name: "plain split", line: "remote host 1194", want: []string{"remote", "host", "1194"}},
		{name: "double quotes keep spaces", line: `pull-filter ignore "dhcp-option DNS"`, want: []string{"pull-filter", "ignore", "dhcp-option DNS"}},
		{name: "single quotes keep spaces", line: `name 'a b'`, want: []string{"name", "a b"}},
		{name: "tabs separate", line: "dev\ttun", want: []string{"dev", "tun"}},
		{name: "empty line", line: "", want: nil},
		{name: "quoted empty field is kept", line: `key ""`, want: []string{"key", ""}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := tokenizeOpenVPNLine(testCase.line)
			if len(got) != len(testCase.want) {
				t.Fatalf("tokenizeOpenVPNLine(%q) = %#v, want %#v", testCase.line, got, testCase.want)
			}
			for i := range got {
				if got[i] != testCase.want[i] {
					t.Errorf("field %d = %q, want %q", i, got[i], testCase.want[i])
				}
			}
		})
	}
}

// TestLooksLikeOpenVPN covers the detection that decides which parser sees a
// subscription body. A false positive would send a JSON or YAML document here.
func TestLooksLikeOpenVPN(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{name: "empty", text: "", want: false},
		{name: "json object", text: `{"remote":"x"}`, want: false},
		{name: "json array", text: `[1,2]`, want: false},
		{name: "remote and ca", text: "remote vpn.example.com\n<ca>\nPEM\n</ca>\n", want: true},
		{name: "remote and secret", text: "remote vpn.example.com\n<secret>\nKEY\n</secret>\n", want: true},
		{name: "client alone", text: "client\ndev tun\n", want: true},
		{name: "dev tun alone", text: "dev tun\n", want: true},
		{name: "ca alone", text: "<ca>\nPEM\n</ca>\n", want: true},
		{name: "yaml document", text: "proxies:\n  - name: a\n", want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := LooksLikeOpenVPN(testCase.text); got != testCase.want {
				t.Errorf("LooksLikeOpenVPN(%q) = %v, want %v", testCase.text, got, testCase.want)
			}
		})
	}
}

// TestParseOpenVPNProfile_RefusesMalformedFingerprint covers a digest the
// runtime would reject later: the parser has to name the problem at parse time
// instead of handing sing-box a pin that can never match.
func TestParseOpenVPNProfile_RefusesMalformedFingerprint(t *testing.T) {
	cases := []struct {
		name   string
		value  string
		detail string
	}{
		{"truncated digest", "AB:CD:EF:01", "peer-fingerprint"},
		{"not hexadecimal", strings.Repeat("z", 64), "peer-fingerprint"},
		{"no value", `""`, "tls mode requires"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			text := "client\ndev tun\nremote vpn.example.com 1194\n" +
				"peer-fingerprint " + testCase.value + "\n"
			parseErr := parseOpenVPNError(t, text)
			if parseErr.Reason != ReasonInvalid {
				t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonInvalid)
			}
			if !strings.Contains(parseErr.Detail, testCase.detail) {
				t.Errorf("Detail = %q, want it to contain %q", parseErr.Detail, testCase.detail)
			}
		})
	}
}

// TestParseOpenVPNProfile_AcceptsSeveralFingerprints pins that one
// peer-fingerprint line may carry more than one digest, and that the case of
// the hex digits does not decide whether a digest is kept.
func TestParseOpenVPNProfile_AcceptsSeveralFingerprints(t *testing.T) {
	second := "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
	text := "client\ndev tun\nremote vpn.example.com 1194\n" +
		"peer-fingerprint " + openVPNTestFingerprint + " " + strings.ToUpper(second) + "\n"
	profile := mustParseOpenVPN(t, text)
	tls, ok := profile.Options["tls"].(map[string]any)
	if !ok {
		t.Fatalf("options[tls] = %#v, want a map", profile.Options["tls"])
	}
	fingerprints, ok := tls["peer_fingerprint"].([]string)
	if !ok || len(fingerprints) != 2 {
		t.Fatalf("tls[peer_fingerprint] = %#v, want two entries", tls["peer_fingerprint"])
	}
	if fingerprints[0] != openVPNTestDigest || fingerprints[1] != second {
		t.Errorf("tls[peer_fingerprint] = %#v, want [%q %q]", fingerprints, openVPNTestDigest, second)
	}
}

// TestParseOpenVPNProfile_VerifyX509Name pins the certificate-name check, which
// is the only thing that ties the TLS session to the intended server.
func TestParseOpenVPNProfile_VerifyX509Name(t *testing.T) {
	profile := mustParseOpenVPN(t, tlsProfile("verify-x509-name vpn.example.com\n"))
	tls, ok := profile.Options["tls"].(map[string]any)
	if !ok {
		t.Fatalf("options[tls] = %#v, want a map", profile.Options["tls"])
	}
	if tls["server_name"] != "vpn.example.com" {
		t.Errorf("tls[server_name] = %#v, want %q", tls["server_name"], "vpn.example.com")
	}
	if tls["server_name_type"] != "subject" {
		t.Errorf("tls[server_name_type] = %#v, want %q: subject is the OpenVPN default", tls["server_name_type"], "subject")
	}
}

func TestParseOpenVPNProfile_VerifyX509NameType(t *testing.T) {
	cases := map[string]string{
		"verify-x509-name vpn.example.com name\n":        "name",
		"verify-x509-name vpn.example.com name-prefix\n": "name-prefix",
		"verify-x509-name vpn.example.com SUBJECT\n":     "subject",
	}
	for directive, want := range cases {
		t.Run(want, func(t *testing.T) {
			profile := mustParseOpenVPN(t, tlsProfile(directive))
			tls, _ := profile.Options["tls"].(map[string]any)
			if tls["server_name_type"] != want {
				t.Errorf("tls[server_name_type] = %#v, want %q", tls["server_name_type"], want)
			}
		})
	}
}

// TestParseOpenVPNProfile_RefusesEmptyVerifyX509Name is the security half of the
// rule: an empty name verifies nothing, so accepting it would answer a profile
// that asked for verification with a session that skips it.
func TestParseOpenVPNProfile_RefusesEmptyVerifyX509Name(t *testing.T) {
	parseErr := parseOpenVPNError(t, tlsProfile("verify-x509-name \"\"\n"))
	if parseErr.Reason != ReasonInvalid {
		t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonInvalid)
	}
	if !strings.Contains(parseErr.Detail, "verify-x509-name") {
		t.Errorf("Detail = %q, want it to name the directive", parseErr.Detail)
	}
}

func TestParseOpenVPNProfile_RefusesUnknownVerifyX509NameType(t *testing.T) {
	parseErr := parseOpenVPNError(t, tlsProfile("verify-x509-name vpn.example.com common-name\n"))
	if parseErr.Reason != ReasonInvalid {
		t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonInvalid)
	}
	if !strings.Contains(parseErr.Detail, "verify-x509-name") {
		t.Errorf("Detail = %q, want it to name the directive", parseErr.Detail)
	}
}

// TestParseOpenVPNProfile_RemoteCertTLS pins that a client profile always asks
// the peer to be a server, and that a value naming a different check is refused
// rather than quietly replaced by the one Prism can perform.
func TestParseOpenVPNProfile_RemoteCertTLS(t *testing.T) {
	profile := mustParseOpenVPN(t, tlsProfile("remote-cert-tls server\n"))
	tls, _ := profile.Options["tls"].(map[string]any)
	if tls["remote_certificate_tls"] != "server" {
		t.Errorf("tls[remote_certificate_tls] = %#v, want %q", tls["remote_certificate_tls"], "server")
	}

	parseErr := parseOpenVPNError(t, tlsProfile("remote-cert-tls client\n"))
	if parseErr.Reason != ReasonUnsupportedFeature {
		t.Errorf("Reason = %q, want %q", parseErr.Reason, ReasonUnsupportedFeature)
	}
	if !strings.Contains(parseErr.Detail, "remote-cert-tls") {
		t.Errorf("Detail = %q, want it to name the directive", parseErr.Detail)
	}
}
