package export

import (
	"encoding/base64"
	"strings"
	"testing"

	"prism/internal/node"
	"prism/internal/subscription"
)

// TestExportURIRoundTripsThroughTheParser is the §2.3 acceptance test: for
// every type the exporter supports, "export -> parse" must reproduce the node
// hash exactly. The hash ignores the tag, so the share-link text is the only
// thing under test.
func TestExportURIRoundTripsThroughTheParser(t *testing.T) {
	items := fixtureNodes(t)
	body, _, report, err := Export(items, FormatURI, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if report.Exported != len(items) {
		t.Fatalf("exported = %d, want %d (skipped: %+v)", report.Exported, len(items), report.Skipped)
	}

	result, err := subscription.ParseWithReport(body)
	if err != nil {
		t.Fatalf("parse exported URI list: %v", err)
	}
	if len(result.Nodes) != len(items) {
		t.Fatalf("parsed %d nodes, want %d (skipped: %+v)", len(result.Nodes), len(items), result.Skipped)
	}

	want := map[string]string{}
	for _, item := range items {
		want[node.HashFromRawOptions(item.RawOptions).Hex()] = item.Name
	}
	matched := map[string]bool{}
	for _, parsed := range result.Nodes {
		hash := node.HashFromRawOptions(parsed.RawOptions).Hex()
		name, ok := want[hash]
		if !ok {
			t.Fatalf("round trip produced an unexpected node hash %s (%s)", hash, parsed.RawOptions)
		}
		if matched[hash] {
			t.Fatalf("round trip produced %s twice", name)
		}
		matched[hash] = true
	}
	for hash, name := range want {
		if !matched[hash] {
			t.Fatalf("node %s (%s) did not survive the URI round trip", name, hash)
		}
	}
}

// TestExportV2rayNIsBase64OfTheURILines pins the §2.3 v2rayn encoding.
func TestExportV2rayNIsBase64OfTheURILines(t *testing.T) {
	items := fixtureNodes(t)
	uriBody, _, _, err := Export(items, FormatURI, Options{})
	if err != nil {
		t.Fatalf("Export uri: %v", err)
	}
	v2rayBody, contentType, report, err := Export(items, FormatV2rayN, Options{})
	if err != nil {
		t.Fatalf("Export v2rayn: %v", err)
	}
	if contentType != "text/plain; charset=utf-8" {
		t.Fatalf("content type = %q", contentType)
	}
	if report.Exported != len(items) {
		t.Fatalf("report = %+v", report)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(v2rayBody)))
	if err != nil {
		t.Fatalf("v2rayn body is not base64: %v", err)
	}
	want := strings.TrimSpace(string(uriBody))
	if strings.TrimSpace(string(decoded)) != want {
		t.Fatalf("v2rayn body is not the base64 of the URI lines\n--- decoded ---\n%s\n--- want ---\n%s", decoded, want)
	}
}

// TestExportURIShape pins the exact link shapes of §2.3 for each protocol.
func TestExportURIShape(t *testing.T) {
	items := fixtureNodes(t)
	body, _, _, err := Export(items, FormatURI, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != len(items) {
		t.Fatalf("lines = %d, want %d", len(lines), len(items))
	}
	byScheme := map[string]string{}
	for _, line := range lines {
		scheme := line[:strings.Index(line, "://")]
		byScheme[scheme] = line
	}

	ss := byScheme["ss"]
	if !strings.HasPrefix(ss, "ss://") || !strings.Contains(ss, "@198.51.100.10:8388") || !strings.HasSuffix(ss, "#ss-tokyo") {
		t.Fatalf("ss link = %q", ss)
	}
	// SIP002 userinfo is base64url(method:password).
	userinfo := strings.TrimPrefix(ss, "ss://")
	userinfo = userinfo[:strings.Index(userinfo, "@")]
	decoded, err := base64.RawURLEncoding.DecodeString(userinfo)
	if err != nil {
		t.Fatalf("ss userinfo is not base64url: %v", err)
	}
	if string(decoded) != "aes-256-gcm:ss-secret-credential" {
		t.Fatalf("ss userinfo = %q", decoded)
	}

	vmess := byScheme["vmess"]
	if !strings.HasPrefix(vmess, "vmess://") || strings.Contains(vmess, "#") {
		t.Fatalf("vmess link = %q", vmess)
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(vmess, "vmess://"))
	if err != nil {
		t.Fatalf("vmess payload is not base64: %v", err)
	}
	for _, fragment := range []string{`"v":"2"`, `"ps":"vmess-ws"`, `"add":"vmess.example.com"`, `"port":"443"`, `"aid":"0"`, `"net":"ws"`, `"path":"/ws"`, `"tls":"tls"`} {
		if !strings.Contains(string(payload), fragment) {
			t.Fatalf("vmess payload %s missing %s", payload, fragment)
		}
	}

	vless := byScheme["vless"]
	if !strings.Contains(vless, "security=reality") || !strings.Contains(vless, "pbk=PUBLIC-KEY-FIXTURE") ||
		!strings.Contains(vless, "sid=0123abcd") || !strings.Contains(vless, "flow=xtls-rprx-vision") ||
		!strings.Contains(vless, "fp=chrome") {
		t.Fatalf("vless link = %q", vless)
	}

	trojan := byScheme["trojan"]
	if !strings.HasPrefix(trojan, "trojan://trojan-secret-credential@trojan.example.org:8443") ||
		!strings.Contains(trojan, "sni=trojan.example.org") {
		t.Fatalf("trojan link = %q", trojan)
	}

	hy2 := byScheme["hysteria2"]
	if !strings.HasPrefix(hy2, "hysteria2://hy2-secret-credential@hy2.example.net:443") ||
		!strings.Contains(hy2, "obfs=salamander") || !strings.Contains(hy2, "obfs-password=obfs-secret") {
		t.Fatalf("hysteria2 link = %q", hy2)
	}

	tuic := byScheme["tuic"]
	if !strings.Contains(tuic, "congestion_control") && !strings.HasPrefix(tuic, "tuic://bbbbbbbb-cccc-dddd-eeee-ffffffffffff:tuic-secret-credential@") {
		t.Fatalf("tuic link = %q", tuic)
	}

	anytls := byScheme["anytls"]
	if !strings.HasPrefix(anytls, "anytls://anytls-secret-credential@anytls.example.com:443") {
		t.Fatalf("anytls link = %q", anytls)
	}

	socks := byScheme["socks5"]
	if !strings.HasPrefix(socks, "socks5://socks-user:socks-secret-credential@socks.example.com:1080") {
		t.Fatalf("socks link = %q", socks)
	}

	http := byScheme["http"]
	if !strings.HasPrefix(http, "http://http-user:http-secret-credential@http.example.com:8080") {
		t.Fatalf("http link = %q", http)
	}
}

// TestExportURISkipsShapesWithoutAShareLink covers §2.3's skip list: node
// shapes that have no share-link spelling are reported, never dropped silently.
func TestExportURISkipsShapesWithoutAShareLink(t *testing.T) {
	items := []Item{
		fixtureChainNode(t),
		fixtureMihomoNode(t),
	}
	for _, format := range []string{FormatURI, FormatV2rayN} {
		_, _, report, err := Export(items, format, Options{})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if report.Exported != 0 {
			t.Fatalf("%s: exported = %d, want 0", format, report.Exported)
		}
		if len(report.Skipped) != len(items) {
			t.Fatalf("%s: skipped = %+v", format, report.Skipped)
		}
		for _, skipped := range report.Skipped {
			if skipped.Reason != "NOT_REPRESENTABLE:uri" {
				t.Fatalf("%s: reason = %q", format, skipped.Reason)
			}
		}
	}
}

// TestExportURIWireGuardRoundTrip checks the wireguard:// row of §2.3.
func TestExportURIWireGuardRoundTrip(t *testing.T) {
	item := fixtureEndpointNode(t)
	body, _, report, err := Export([]Item{item}, FormatURI, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if report.Exported != 1 {
		t.Fatalf("report = %+v", report)
	}
	if !strings.HasPrefix(string(body), "wireguard://WG-PRIVATE-KEY-FIXTURE@203.0.113.9:51820") {
		t.Fatalf("wireguard link = %q", body)
	}
	result, err := subscription.ParseWithReport(body)
	if err != nil {
		t.Fatalf("parse wireguard link: %v", err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("parsed %d nodes, want 1 (skipped %+v)", len(result.Nodes), result.Skipped)
	}
	want := node.HashFromRawOptions(item.RawOptions).Hex()
	got := node.HashFromRawOptions(result.Nodes[0].RawOptions).Hex()
	if got != want {
		t.Fatalf("wireguard round trip hash = %s, want %s", got, want)
	}
}

// TestExportURIV2rayNRoundTripStillHolds checks the base64 wrapper is decoded
// by the parser and the hashes survive that path too.
func TestExportURIV2rayNRoundTripStillHolds(t *testing.T) {
	items := fixtureNodes(t)
	body, _, _, err := Export(items, FormatV2rayN, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	result, err := subscription.ParseWithReport(body)
	if err != nil {
		t.Fatalf("parse v2rayn body: %v", err)
	}
	if len(result.Nodes) != len(items) {
		t.Fatalf("parsed %d nodes, want %d", len(result.Nodes), len(items))
	}
	want := expectedHashes(t, items)
	for _, parsed := range result.Nodes {
		hash := node.HashFromRawOptions(parsed.RawOptions).Hex()
		if !want[hash] {
			t.Fatalf("unexpected node after the v2rayn round trip: %s", parsed.RawOptions)
		}
	}
}

// TestExportURIKubernetesStyleObfsIsNotSilentlyDropped checks a hysteria2 node
// whose byte rate cannot be expressed as Mbps is skipped with a reason.
func TestExportURIRateFieldsThatCannotBeExpressedAreSkipped(t *testing.T) {
	item := Item{
		Name:       "hy2-byterate",
		RawOptions: []byte(`{"type":"hysteria2","tag":"hy2-byterate","server":"a.example.com","server_port":443,"password":"pw","up_mbps":"100 Mbps","tls":{"enabled":true}}`),
	}
	_, _, report, err := Export([]Item{item}, FormatURI, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Reason != "NOT_REPRESENTABLE:uri" {
		t.Fatalf("report = %+v", report)
	}
}
