package export

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/sagernet/sing/common/byteformats"

	"prism/internal/node"
	"prism/internal/subscription"
)

// Regression tests for the "exported field silently dropped or rewritten"
// family of defects. Every case here is a field the
// subscription parser reads back, so a link or proxy that omits it makes the
// round trip lossy.
//
//   - wireguard `reserved` (uri.go): the canonical endpoint document carries
//     the byte triple as the base64 string Go's JSON codec writes for []uint8,
//     while the share-link writer read it with a number-array-only reader, which
//     never saw it. Every WARP-style node exported a link without its reserved
//     bytes.
//   - hysteria v1 rates (uri.go): the link carried `up=20 Mbps`, which the
//     parser's strconv.ParseUint silently turns into 0, while a numeric
//     `up_mbps` was never written at all.
//   - SOCKS4/4a (uri.go, clashmap.go): both writers hard-coded the SOCKS5
//     spelling, so a SOCKS4 node came back as SOCKS5 with different semantics.
//   - vmess allowInsecure and hysteria2 certificate pinning (uri.go): the
//     parser reads them, the exporter never wrote them, so an exported node
//     lost its "do not verify" flag or its certificate material.

// --- wireguard reserved ------------------------------------------------

// TestWireGuardReservedSurvivesTheShareLink covers both spellings of
// the peer's reserved field must reach the link, and the link must be one the
// wireguard parser turns back into the same three bytes.
func TestWireGuardReservedSurvivesTheShareLink(t *testing.T) {
	const document = `{"prism_node":1,"engine":"singbox","kind":"endpoint","name":"wg-reserved",` +
		`"main":{"type":"wireguard","address":["10.7.0.2/32"],"private_key":"WG-PRIVATE-KEY-FIXTURE",` +
		`"peers":[{"address":"203.0.113.9","port":51820,"public_key":"WG-PUBLIC-KEY-FIXTURE",` +
		`"reserved":%s,"allowed_ips":["0.0.0.0/0","::/0"]}]}}`

	cases := []struct {
		name     string
		reserved string
	}{
		// The canonical shape: node.WireGuardPeer.Reserved is a []uint8, which
		// encoding/json renders as base64, so this is what the exporter sees
		// for every node that went through the real import path.
		{name: "base64 string", reserved: `"AQID"`},
		// A hand-written or legacy document may still use the number array.
		{name: "number array", reserved: `[1,2,3]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(fmt.Sprintf(document, tc.reserved))
			if _, err := node.ParseNodeDoc(raw); err != nil {
				t.Fatalf("the fixture itself does not parse: %v", err)
			}
			body, _, report, err := Export([]Item{{Name: "wg-reserved", RawOptions: raw}}, FormatURI, Options{})
			if err != nil {
				t.Fatalf("Export: %v", err)
			}
			if report.Exported != 1 {
				t.Fatalf("exported = %d, want 1 (skipped: %+v)", report.Exported, report.Skipped)
			}
			link := strings.TrimSpace(string(body))
			if !strings.Contains(link, "reserved=") {
				t.Fatalf("the exported link carries no reserved parameter: %s", link)
			}

			// The real consumer is node.ParseWireGuardURI, so the assertion is
			// made on what it reads back rather than on the encoding.
			_, endpoint, ok := node.ParseWireGuardURI(link)
			if !ok {
				t.Fatalf("the exported link is not a wireguard link Prism can import: %s", link)
			}
			if len(endpoint.Peers) != 1 {
				t.Fatalf("parsed %d peers, want 1", len(endpoint.Peers))
			}
			if got := endpoint.Peers[0].Reserved; len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
				t.Fatalf("reserved round trip = %v, want [1 2 3] (%s)", got, link)
			}
		})
	}
}

// TestWireGuardReservedReadsTheNumberArrayDirectly covers the peer map as
// decoded from JSON rather than as normalised by node.ParseNodeDoc: the
// number-array spelling has to keep working, so the fix may not simply replace
// one spelling with the other.
func TestWireGuardReservedReadsTheNumberArrayDirectly(t *testing.T) {
	main := json.RawMessage(`{"type":"wireguard","address":["10.7.0.2/32"],"private_key":"WG-PRIVATE-KEY-FIXTURE",` +
		`"peers":[{"address":"203.0.113.9","port":51820,"public_key":"WG-PUBLIC-KEY-FIXTURE","reserved":[7,8,9]}]}`)
	item := preparedItem{
		Item: Item{Name: "wg-array"},
		Doc:  NodeDoc{Engine: node.EngineSingbox, Kind: node.DocEndpoint, Type: "wireguard", Main: main},
	}
	link, ok := wireguardLink(item)
	if !ok {
		t.Fatal("wireguardLink refused a complete endpoint")
	}
	_, endpoint, ok := node.ParseWireGuardURI(link)
	if !ok {
		t.Fatalf("wireguardLink produced a link the parser refuses: %s", link)
	}
	if got := endpoint.Peers[0].Reserved; len(got) != 3 || got[0] != 7 || got[1] != 8 || got[2] != 9 {
		t.Fatalf("reserved round trip = %v, want [7 8 9] (%s)", got, link)
	}
}

// --- hysteria v1 rates -------------------------------------------------

// TestHysteriaV1RatesSurviveTheShareLink covers the shape a URI
// import produces: sing-box's up_mbps/down_mbps are whole Mbps, which is
// exactly what the share-link up/down parameters mean.
func TestHysteriaV1RatesSurviveTheShareLink(t *testing.T) {
	raw := json.RawMessage(`{"type":"hysteria","tag":"hy1-rates","server":"hy1.example.net","server_port":443,` +
		`"auth_str":"hy1-pw","up_mbps":20,"down_mbps":200,"tls":{"enabled":true,"server_name":"hy1.example.net"}}`)
	if _, err := node.ParseNodeDoc(raw); err != nil {
		t.Fatalf("the fixture itself does not parse: %v", err)
	}
	body, _, report, err := Export([]Item{{Name: "hy1-rates", RawOptions: raw}}, FormatURI, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if report.Exported != 1 {
		t.Fatalf("exported = %d, want 1 (skipped: %+v)", report.Exported, report.Skipped)
	}
	link := strings.TrimSpace(string(body))
	for _, fragment := range []string{"up=20", "down=200"} {
		if !strings.Contains(link, fragment) {
			t.Fatalf("the exported link is missing %q: %s", fragment, link)
		}
	}

	result, err := subscription.ParseWithReport(body)
	if err != nil {
		t.Fatalf("ParseWithReport: %v", err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("parsed %d nodes, want 1 (skipped: %+v)", len(result.Nodes), result.Skipped)
	}
	outbound, err := objectMap(result.Nodes[0].RawOptions)
	if err != nil {
		t.Fatalf("decode parsed outbound: %v", err)
	}
	if got := mapUint(outbound, "up_mbps"); got != 20 {
		t.Fatalf("up_mbps round trip = %d, want 20 (%s)", got, result.Nodes[0].RawOptions)
	}
	if got := mapUint(outbound, "down_mbps"); got != 200 {
		t.Fatalf("down_mbps round trip = %d, want 200 (%s)", got, result.Nodes[0].RawOptions)
	}
}

// TestHysteriaV1ByteRateIsConvertedToMbps covers the other half of the rule:
// the shape a Clash import produces carries `up: "20 Mbps"`, which is a *byte*
// rate, and sing-box prefers it over up_mbps when both are set. Whole Mbps is
// an exact unit for a byte rate (125000 bytes per Mbps, see bytesPerMbps), so
// the link must carry the converted value instead of the node being dropped or
// the rate being rewritten by a factor of 125000.
//
// The numeric spelling is the same rate written as a bare byte count.
func TestHysteriaV1ByteRateIsConvertedToMbps(t *testing.T) {
	rates := []struct {
		name     string
		body     string
		wantUp   uint64
		wantDown uint64
	}{
		{name: "unit string", body: `"up":"20 Mbps","down":"200 Mbps"`, wantUp: 20, wantDown: 200},
		{name: "numeric byte count", body: `"up":2500000,"down":25000000`, wantUp: 20, wantDown: 200},
		// A byte rate wins over up_mbps/down_mbps in sing-box, so the exported
		// number has to follow the byte rate rather than the Mbps field.
		{name: "byte rate wins over the Mbps pair", body: `"up":"20 Mbps","down":"200 Mbps","up_mbps":7,"down_mbps":70`, wantUp: 20, wantDown: 200},
	}
	for _, rate := range rates {
		t.Run(rate.name, func(t *testing.T) {
			raw := json.RawMessage(`{"type":"hysteria","tag":"hy1-byterate","server":"hy1.example.net","server_port":443,` +
				`"auth_str":"hy1-pw",` + rate.body + `,"tls":{"enabled":true,"server_name":"hy1.example.net"}}`)
			if _, err := node.ParseNodeDoc(raw); err != nil {
				t.Fatalf("the fixture itself does not parse: %v", err)
			}
			for _, format := range []string{FormatURI, FormatV2rayN} {
				body, _, report, err := Export([]Item{{Name: "hy1-byterate", RawOptions: raw}}, format, Options{})
				if err != nil {
					t.Fatalf("%s: %v", format, err)
				}
				if report.Exported != 1 {
					t.Fatalf("%s: exported = %d, want 1 (skipped: %+v)", format, report.Exported, report.Skipped)
				}
				link := strings.TrimSpace(string(shareLinkBody(t, format, body)))
				for _, fragment := range []string{
					"up=" + strconv.FormatUint(rate.wantUp, 10),
					"down=" + strconv.FormatUint(rate.wantDown, 10),
				} {
					if !strings.Contains(link, fragment) {
						t.Fatalf("%s: the exported link is missing %q: %s", format, fragment, link)
					}
				}
			}

			// The real consumer is the subscription parser, so the rate is
			// asserted on what it reads back.
			result, err := subscription.ParseWithReport(mustExport(t, raw, FormatURI))
			if err != nil {
				t.Fatalf("ParseWithReport: %v", err)
			}
			if len(result.Nodes) != 1 {
				t.Fatalf("parsed %d nodes, want 1 (skipped: %+v)", len(result.Nodes), result.Skipped)
			}
			outbound, err := objectMap(result.Nodes[0].RawOptions)
			if err != nil {
				t.Fatalf("decode parsed outbound: %v", err)
			}
			if got := mapUint(outbound, "up_mbps"); got != rate.wantUp {
				t.Fatalf("up_mbps round trip = %d, want %d (%s)", got, rate.wantUp, result.Nodes[0].RawOptions)
			}
			if got := mapUint(outbound, "down_mbps"); got != rate.wantDown {
				t.Fatalf("down_mbps round trip = %d, want %d (%s)", got, rate.wantDown, result.Nodes[0].RawOptions)
			}
		})
	}
}

// mustExport renders one node as a share-link body or fails the test.
func mustExport(t *testing.T, raw json.RawMessage, format string) []byte {
	t.Helper()
	body, _, _, err := Export([]Item{{Name: "hy1-byterate", RawOptions: raw}}, format, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	return body
}

// shareLinkBody returns the plain share-link lines of an exported body, undoing
// the base64 wrapper of the v2rayN format.
func shareLinkBody(t *testing.T, format string, body []byte) []byte {
	t.Helper()
	if format != FormatV2rayN {
		return body
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
	if err != nil {
		t.Fatalf("decode the v2rayN body: %v", err)
	}
	return decoded
}

// TestHysteriaV1ByteRateThatIsNotWholeMbpsIsSkipped covers the limit of the
// conversion: a byte rate that is not a whole number of Mbps has no exact
// share-link value, so the node is skipped with a reason instead of being
// exported with a rate that is off by up to a megabit.
func TestHysteriaV1ByteRateThatIsNotWholeMbpsIsSkipped(t *testing.T) {
	rates := []struct {
		name string
		body string
	}{
		{name: "unit string", body: `"up":"20.5 Mbps"`},
		{name: "numeric byte count", body: `"up":2500001`},
		// sing-box resolves an unknown unit to an error, so the rate is not a
		// rate at all and cannot be converted either.
		{name: "unsupported unit", body: `"up":"20 Xbps"`},
	}
	for _, rate := range rates {
		t.Run(rate.name, func(t *testing.T) {
			raw := json.RawMessage(`{"type":"hysteria","tag":"hy1-byterate","server":"hy1.example.net","server_port":443,` +
				`"auth_str":"hy1-pw",` + rate.body + `,"tls":{"enabled":true,"server_name":"hy1.example.net"}}`)
			for _, format := range []string{FormatURI, FormatV2rayN} {
				_, _, report, err := Export([]Item{{Name: "hy1-byterate", RawOptions: raw}}, format, Options{})
				if err != nil {
					t.Fatalf("%s: %v", format, err)
				}
				if report.Exported != 0 {
					t.Fatalf("%s: exported = %d, want 0: a byte rate that is not whole Mbps cannot be written as a share-link value", format, report.Exported)
				}
				if len(report.Skipped) != 1 || report.Skipped[0].Reason != "NOT_REPRESENTABLE:uri" {
					t.Fatalf("%s: skipped = %+v", format, report.Skipped)
				}
			}
		})
	}
}

// TestHysteriaByteRateUnitsMatchSingBox pins the unit tables against
// sing-box itself: byteformats.NetworkBytesCompat is the decoder sing-box runs
// on the `up`/`down` fields, so every unit Prism converts has to resolve to the
// same byte count sing-box would compute. A table that drifts from the upstream
// one would silently export a wrong rate.
//
// The count is 1 so that no unit overflows uint64: sing-box multiplies without
// a range check, so "20 EBps" wraps around there, and Prism refuses a rate it
// cannot represent instead of copying a wrapped number.
func TestHysteriaByteRateUnitsMatchSingBox(t *testing.T) {
	units := []string{
		"Bps", "Kbps", "KBps", "Mbps", "MBps", "Gbps", "GBps",
		"Tbps", "TBps", "Pbps", "PBps", "Ebps", "EBps",
		"b", "k", "kb", "ki", "kib", "m", "mb", "mi", "mib",
		"g", "gb", "gi", "gib", "t", "tb", "ti", "tib",
		"p", "pb", "pi", "pib", "e", "eb", "ei", "eib",
	}
	for _, unit := range units {
		value := "1 " + unit
		var upstream byteformats.NetworkBytesCompat
		if err := upstream.UnmarshalJSON([]byte(strconv.Quote(value))); err != nil {
			t.Fatalf("sing-box refused %q: %v", value, err)
		}
		got, ok := parseNetworkByteRate(strings.TrimSpace(value))
		if !ok {
			t.Fatalf("Prism refused %q, which sing-box resolves to %d", value, upstream.Value())
		}
		if got != upstream.Value() {
			t.Fatalf("%q: Prism resolved %d, sing-box %d", value, got, upstream.Value())
		}
	}

	// The common spellings also have to agree at the counts a subscription
	// actually carries. "mbps"/"kbps" are deliberately absent: sing-box's
	// case-sensitive table holds "Mbps"/"Kbps" and the lower-case fallback holds
	// only "mb"/"kb", so the all-lower-case spelling is an unsupported unit
	// upstream and Prism refuses it too (pinned below).
	for _, value := range []string{"20 Mbps", "200 Mbps", "1000 Mbps", "20 Kbps", "20 KBps", "20 MBps", "2 GBps", "20 mb", "20 kb"} {
		var upstream byteformats.NetworkBytesCompat
		if err := upstream.UnmarshalJSON([]byte(strconv.Quote(value))); err != nil {
			t.Fatalf("sing-box refused %q: %v", value, err)
		}
		got, ok := parseNetworkByteRate(value)
		if !ok {
			t.Fatalf("Prism refused %q, which sing-box resolves to %d", value, upstream.Value())
		}
		if got != upstream.Value() {
			t.Fatalf("%q: Prism resolved %d, sing-box %d", value, got, upstream.Value())
		}
	}

	// A unit sing-box rejects is not a rate Prism may convert.
	for _, value := range []string{"20 mbps", "20 kbps", "20 Xbps", "20", "Mbps", ""} {
		var upstream byteformats.NetworkBytesCompat
		if err := upstream.UnmarshalJSON([]byte(strconv.Quote(value))); err == nil {
			t.Fatalf("sing-box accepted %q (%d); the refusal case is stale", value, upstream.Value())
		}
		if got, ok := parseNetworkByteRate(value); ok {
			t.Fatalf("Prism converted %q to %d, which sing-box does not accept as a rate", value, got)
		}
	}
}

// --- B3: SOCKS version -----------------------------------------------------

// TestSOCKSVersionSurvivesTheShareLink covers the share-link side:
// the scheme has to follow the node's version field instead of always saying
// socks5. An absent version means SOCKS5, which is what the parser assumes too.
func TestSOCKSVersionSurvivesTheShareLink(t *testing.T) {
	cases := []struct {
		name    string
		version string
		scheme  string
		// wantVersion is the version the parser reports after the round trip.
		// The modern schemes carry no version field at all: omitting it means 5.
		wantVersion string
	}{
		{name: "socks4", version: "4", scheme: "socks4", wantVersion: "4"},
		{name: "socks4a", version: "4a", scheme: "socks4a", wantVersion: "4a"},
		{name: "explicit socks5", version: "5", scheme: "socks5", wantVersion: ""},
		{name: "absent version defaults to socks5", version: "", scheme: "socks5", wantVersion: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			document := `{"type":"socks","tag":"socks-1","server":"socks.example.com","server_port":1080`
			if tc.version != "" {
				document += `,"version":"` + tc.version + `"`
			}
			raw := json.RawMessage(document + `}`)
			if _, err := node.ParseNodeDoc(raw); err != nil {
				t.Fatalf("the fixture itself does not parse: %v", err)
			}
			body, _, report, err := Export([]Item{{Name: "socks-1", RawOptions: raw}}, FormatURI, Options{})
			if err != nil {
				t.Fatalf("Export: %v", err)
			}
			if report.Exported != 1 {
				t.Fatalf("exported = %d, want 1 (skipped: %+v)", report.Exported, report.Skipped)
			}
			link := strings.TrimSpace(string(body))
			if !strings.HasPrefix(link, tc.scheme+"://") {
				t.Fatalf("exported link = %q, want the %s:// scheme", link, tc.scheme)
			}

			result, err := subscription.ParseWithReport(body)
			if err != nil {
				t.Fatalf("ParseWithReport: %v", err)
			}
			if len(result.Nodes) != 1 {
				t.Fatalf("parsed %d nodes, want 1 (skipped: %+v)", len(result.Nodes), result.Skipped)
			}
			outbound, err := objectMap(result.Nodes[0].RawOptions)
			if err != nil {
				t.Fatalf("decode parsed outbound: %v", err)
			}
			if got := mapString(outbound, "version"); got != tc.wantVersion {
				t.Fatalf("version round trip = %q, want %q (%s)", got, tc.wantVersion, result.Nodes[0].RawOptions)
			}
		})
	}
}

// TestSOCKS4IsRefusedByTheClashExport covers the Clash side.
//
// mihomo v1.19.31 has no SOCKS4 outbound: adapter/parser.go maps exactly one
// SOCKS spelling, "socks5", and every other type fails with "unsupport proxy
// type", which makes mihomo refuse the whole document. Emitting `type: socks4`
// would therefore trade a silent semantic change for an unloadable config, so
// a SOCKS4/4a node is refused with a reason — the same rule openconnect already
// follows in clashmap.go.
func TestSOCKS4IsRefusedByTheClashExport(t *testing.T) {
	for _, version := range []string{"4", "4a"} {
		raw := fmt.Sprintf(`{"type":"socks","server":"socks.example.com","server_port":1080,"version":%q}`, version)
		if proxy, ok := clashProxyFromSingbox(clashObject(t, raw)); ok {
			t.Fatalf("SOCKS %s was exported as %#v: mihomo has no SOCKS4 outbound, and a socks5 entry would silently change the node's semantics", version, proxy)
		}
	}

	// Through the real export path the refusal is reported, never silent.
	raw := json.RawMessage(`{"type":"socks","tag":"socks4-1","server":"socks.example.com","server_port":1080,"version":"4"}`)
	_, _, report, err := Export([]Item{{Name: "socks4-1", RawOptions: raw}}, FormatMihomo, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if report.Exported != 0 || len(report.Skipped) != 1 || report.Skipped[0].Reason != "NOT_REPRESENTABLE:mihomo" {
		t.Fatalf("report = %+v, want one NOT_REPRESENTABLE:mihomo skip", report)
	}

	// SOCKS5 keeps its spelling, with and without an explicit version.
	for _, document := range []string{
		`{"type":"socks","server":"socks.example.com","server_port":1080}`,
		`{"type":"socks","server":"socks.example.com","server_port":1080,"version":"5"}`,
	} {
		proxy := mustClashProxy(t, document)
		assertField(t, "socks5", proxy, "type", "socks5")
	}
}

// --- B4: vmess allowInsecure, hysteria2 certificate material ---------------

// TestVmessAllowInsecureSurvivesTheShareLink covers the vmess side.
// parseVmessFromMap reads allowInsecure out of the base64 share JSON, so the
// payload has to carry it; the exporter never wrote it and an exported node
// silently started verifying certificates again.
func TestVmessAllowInsecureSurvivesTheShareLink(t *testing.T) {
	raw := json.RawMessage(`{"type":"vmess","tag":"vmess-insecure","server":"vmess.example.com","server_port":443,` +
		`"uuid":"11111111-2222-3333-4444-555555555555","alter_id":0,"security":"auto",` +
		`"tls":{"enabled":true,"server_name":"vmess.example.com","insecure":true}}`)
	if _, err := node.ParseNodeDoc(raw); err != nil {
		t.Fatalf("the fixture itself does not parse: %v", err)
	}
	body, _, report, err := Export([]Item{{Name: "vmess-insecure", RawOptions: raw}}, FormatURI, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if report.Exported != 1 {
		t.Fatalf("exported = %d, want 1 (skipped: %+v)", report.Exported, report.Skipped)
	}
	link := strings.TrimSpace(string(body))
	payload, err := decodeVmessSharePayload(link)
	if err != nil {
		t.Fatalf("decode vmess payload: %v", err)
	}
	if insecure, _ := payload["allowInsecure"].(bool); !insecure {
		t.Fatalf("the vmess payload lost allowInsecure: %s", link)
	}

	result, err := subscription.ParseWithReport(body)
	if err != nil {
		t.Fatalf("ParseWithReport: %v", err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("parsed %d nodes, want 1 (skipped: %+v)", len(result.Nodes), result.Skipped)
	}
	outbound, err := objectMap(result.Nodes[0].RawOptions)
	if err != nil {
		t.Fatalf("decode parsed outbound: %v", err)
	}
	tls := mapObject(outbound, "tls")
	if insecure, _ := tls["insecure"].(bool); !insecure {
		t.Fatalf("tls.insecure round trip = %v, want true (%s)", tls, result.Nodes[0].RawOptions)
	}
}

// decodeVmessSharePayload returns the JSON object of a vmess:// link.
func decodeVmessSharePayload(link string) (map[string]any, error) {
	if !strings.HasPrefix(link, "vmess://") {
		return nil, fmt.Errorf("not a vmess link: %s", link)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(link, "vmess://"))
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// TestHysteria2CertificatePinningSurvivesTheShareLink covers the
// hysteria2 side: parseHysteria2URI reads pinSHA256, ca and ca-str, so a node
// with certificate material must export it. Dropping it is a security
// downgrade — the imported node stops checking the certificate at all.
func TestHysteria2CertificatePinningSurvivesTheShareLink(t *testing.T) {
	const pinOne = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	const pinTwo = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="
	const pem = "-----BEGIN CERTIFICATE-----\nMIIBFIXTURE\n-----END CERTIFICATE-----"
	raw := json.RawMessage(`{"type":"hysteria2","tag":"hy2-pin","server":"hy2.example.net","server_port":443,` +
		`"password":"hy2-pw","tls":{"enabled":true,"server_name":"hy2.example.net",` +
		`"certificate_public_key_sha256":["` + pinOne + `","` + pinTwo + `"],` +
		`"certificate":["` + strings.ReplaceAll(pem, "\n", `\n`) + `"]}}`)
	if _, err := node.ParseNodeDoc(raw); err != nil {
		t.Fatalf("the fixture itself does not parse: %v", err)
	}
	body, _, report, err := Export([]Item{{Name: "hy2-pin", RawOptions: raw}}, FormatURI, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if report.Exported != 1 {
		t.Fatalf("exported = %d, want 1 (skipped: %+v)", report.Exported, report.Skipped)
	}
	link := strings.TrimSpace(string(body))
	for _, key := range []string{"pinSHA256=", "ca-str="} {
		if !strings.Contains(link, key) {
			t.Fatalf("the exported link is missing %q: %s", key, link)
		}
	}

	result, err := subscription.ParseWithReport(body)
	if err != nil {
		t.Fatalf("ParseWithReport: %v", err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("parsed %d nodes, want 1 (skipped: %+v)", len(result.Nodes), result.Skipped)
	}
	outbound, err := objectMap(result.Nodes[0].RawOptions)
	if err != nil {
		t.Fatalf("decode parsed outbound: %v", err)
	}
	tls := mapObject(outbound, "tls")
	pins := mapStringSlice(tls, "certificate_public_key_sha256")
	if len(pins) != 2 || pins[0] != pinOne || pins[1] != pinTwo {
		t.Fatalf("certificate_public_key_sha256 round trip = %v, want [%s %s] (%s)", pins, pinOne, pinTwo, result.Nodes[0].RawOptions)
	}
	certs := mapStringSlice(tls, "certificate")
	if len(certs) != 1 || certs[0] != pem {
		t.Fatalf("tls.certificate round trip = %q, want %q", certs, pem)
	}
}

// TestHysteria2CAResolvesToTheCertificatePath covers the `ca` spelling,
// which maps onto tls.certificate_path. That option is refused by
// node.ParseNodeDoc (it names a local file), so the writer is exercised
// directly instead of through Export.
func TestHysteria2CAResolvesToTheCertificatePath(t *testing.T) {
	object := map[string]any{
		"type":        "hysteria2",
		"server":      "hy2.example.net",
		"server_port": float64(443),
		"password":    "hy2-pw",
		"tls": map[string]any{
			"enabled":          true,
			"server_name":      "hy2.example.net",
			"certificate_path": "/etc/prism/ca.pem",
		},
	}
	link, ok := hysteria2Link(object, "hy2-ca")
	if !ok {
		t.Fatal("hysteria2Link refused a complete node")
	}
	if !strings.Contains(link, "ca=%2Fetc%2Fprism%2Fca.pem") && !strings.Contains(link, "ca=/etc/prism/ca.pem") {
		t.Fatalf("the exported link lost tls.certificate_path: %s", link)
	}
}

// TestFractionalMbpsRateIsRefused covers the Mbps half of the rate rule.
//
// A share link carries the rate as decimal digits that the parser reads back
// with strconv.ParseUint, so a fraction would be silently truncated to its
// integer part - a rate the node never had. sing-box is no more permissive:
// up_mbps is an int and it refuses both "20" (a string) and 20.5 (a fraction)
// outright, which is why such a node is skipped with a reason instead of being
// exported with a rounded rate.
func TestFractionalMbpsRateIsRefused(t *testing.T) {
	rates := []struct {
		name     string
		protocol string
		body     string
	}{
		{name: "hysteria fraction", protocol: "hysteria", body: `"up_mbps":20.5`},
		{name: "hysteria string", protocol: "hysteria", body: `"up_mbps":"20"`},
		{name: "hysteria2 fraction", protocol: "hysteria2", body: `"up_mbps":20.5`},
		// The string spelling reaches hysteria2 through the shared
		// shareLinkRateSupported guard rather than through the v1 rate resolver,
		// so it pins that helper too.
		{name: "hysteria2 string", protocol: "hysteria2", body: `"up_mbps":"20"`},
		{name: "hysteria2 bool", protocol: "hysteria2", body: `"up_mbps":true`},
	}
	for _, rate := range rates {
		t.Run(rate.name, func(t *testing.T) {
			secret := `"auth_str":"hy-pw"`
			if rate.protocol == "hysteria2" {
				secret = `"password":"hy-pw"`
			}
			raw := json.RawMessage(`{"type":"` + rate.protocol + `","tag":"fraction","server":"hy.example.net","server_port":443,` +
				secret + `,` + rate.body + `,"tls":{"enabled":true,"server_name":"hy.example.net"}}`)
			for _, format := range []string{FormatURI, FormatV2rayN} {
				_, _, report, err := Export([]Item{{Name: "fraction", RawOptions: raw}}, format, Options{})
				if err != nil {
					t.Fatalf("%s: %v", format, err)
				}
				if report.Exported != 0 {
					t.Fatalf("%s: exported = %d, want 0: a rate that is not a whole number has no share-link spelling", format, report.Exported)
				}
				if len(report.Skipped) != 1 || report.Skipped[0].Reason != "NOT_REPRESENTABLE:uri" {
					t.Fatalf("%s: skipped = %+v", format, report.Skipped)
				}
			}

			// The same document with a whole number is exported, so the refusal
			// is about the fraction and not about the fixture.
			whole := json.RawMessage(strings.Replace(string(raw), rate.body, `"up_mbps":20`, 1))
			if _, _, report, err := Export([]Item{{Name: "fraction", RawOptions: whole}}, FormatURI, Options{}); err != nil {
				t.Fatalf("Export (whole rate): %v", err)
			} else if report.Exported != 1 {
				t.Fatalf("a whole Mbps rate was refused too: %+v", report)
			}
		})
	}
}
