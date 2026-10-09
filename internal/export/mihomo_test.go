package export

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"prism/internal/subscription"
)

// mihomoDocument is the structural shape the mihomo tests assert on.
type mihomoDocument struct {
	Proxies []map[string]any `yaml:"proxies"`
	Groups  []struct {
		Name     string   `yaml:"name"`
		Type     string   `yaml:"type"`
		URL      string   `yaml:"url"`
		Interval int      `yaml:"interval"`
		Proxies  []string `yaml:"proxies"`
	} `yaml:"proxy-groups"`
	Rules []string `yaml:"rules"`
}

func decodeMihomo(t *testing.T, body []byte) mihomoDocument {
	t.Helper()
	var document mihomoDocument
	if err := yaml.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode mihomo yaml: %v", err)
	}
	return document
}

// TestExportMihomoGolden pins the full mihomo document (WP11 §5).
func TestExportMihomoGolden(t *testing.T) {
	items := append(fixtureNodes(t), fixtureEndpointNode(t), fixtureChainNode(t))
	body, contentType, report, err := Export(items, FormatMihomo, Options{IncludeIntel: true})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if contentType != "text/yaml; charset=utf-8" {
		t.Fatalf("content type = %q", contentType)
	}
	if report.Exported != len(items) || len(report.Skipped) != 0 {
		t.Fatalf("report = %+v", report)
	}
	assertGolden(t, "mihomo.yaml", body)
}

// TestExportMihomoGroupsResolve checks the two groups reference only proxies
// that exist in the document, and that every proxy is a member of the auto
// group.
func TestExportMihomoGroupsResolve(t *testing.T) {
	items := append(fixtureNodes(t), fixtureEndpointNode(t), fixtureChainNode(t))
	body, _, _, err := Export(items, FormatMihomo, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	document := decodeMihomo(t, body)

	if len(document.Groups) != 2 {
		t.Fatalf("groups = %+v", document.Groups)
	}
	selector, auto := document.Groups[0], document.Groups[1]
	if selector.Name != "PROXY" || selector.Type != "select" {
		t.Fatalf("selector group = %+v", selector)
	}
	if auto.Name != "AUTO" || auto.Type != "url-test" || auto.Interval != 300 ||
		auto.URL != "https://www.gstatic.com/generate_204" {
		t.Fatalf("auto group = %+v", auto)
	}
	if len(selector.Proxies) != len(auto.Proxies)+1 || selector.Proxies[0] != "AUTO" {
		t.Fatalf("selector members = %v", selector.Proxies)
	}

	defined := map[string]bool{}
	for _, proxy := range document.Proxies {
		name, _ := proxy["name"].(string)
		if name == "" {
			t.Fatalf("proxy without a name: %+v", proxy)
		}
		if defined[name] {
			t.Fatalf("duplicate proxy name %q", name)
		}
		defined[name] = true
	}
	for _, member := range selector.Proxies[1:] {
		if !defined[member] {
			t.Fatalf("selector references undefined proxy %q", member)
		}
	}
	if len(auto.Proxies) != len(document.Proxies) {
		t.Fatalf("auto group members = %d, proxies = %d", len(auto.Proxies), len(document.Proxies))
	}
	for _, member := range auto.Proxies {
		if !defined[member] {
			t.Fatalf("auto group references undefined proxy %q", member)
		}
	}
	if len(document.Rules) != 1 || document.Rules[0] != "MATCH,PROXY" {
		t.Fatalf("rules = %v", document.Rules)
	}
}

// TestExportMihomoEveryProxyIsParseableByClash checks every exported proxy is a
// Clash proxy map the Clash parser in this repository accepts, by re-reading the
// document with the subscription parser. That is the offline stand-in for
// adapter.ParseProxy, which needs the with_mihomo build tag that decision D-1
// deliberately dropped from go.mod.
func TestExportMihomoEveryProxyIsParseableByClash(t *testing.T) {
	items := append(fixtureNodes(t), fixtureEndpointNode(t), fixtureChainNode(t), fixtureMihomoNode(t))
	body, _, _, err := Export(items, FormatMihomo, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	result, err := subscription.ParseWithReport(body)
	if err != nil {
		t.Fatalf("re-parse mihomo document: %v", err)
	}
	document := decodeMihomo(t, body)
	if len(result.Nodes)+len(result.Skipped) != len(document.Proxies) {
		t.Fatalf("clash parser saw %d nodes and %d skips, proxies = %d",
			len(result.Nodes), len(result.Skipped), len(document.Proxies))
	}
	// Only the SSR node is expected to be unimportable by sing-box.
	if len(result.Skipped) != 1 || result.Skipped[0].Type != "ssr" {
		t.Fatalf("clash parser skips = %+v", result.Skipped)
	}
}

// TestExportMihomoMihomoNodeIsOutputVerbatim checks the §2.2 rule that a mihomo
// node's proxy map is written as-is with only its name replaced.
func TestExportMihomoMihomoNodeIsOutputVerbatim(t *testing.T) {
	body, _, _, err := Export([]Item{fixtureMihomoNode(t)}, FormatMihomo, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	document := decodeMihomo(t, body)
	if len(document.Proxies) != 1 {
		t.Fatalf("proxies = %+v", document.Proxies)
	}
	proxy := document.Proxies[0]
	if proxy["type"] != "ssr" || proxy["name"] != "ssr-1" || proxy["server"] != "198.51.100.30" {
		t.Fatalf("ssr proxy = %+v", proxy)
	}
	if proxy["cipher"] != "aes-256-cfb" || proxy["protocol"] != "origin" || proxy["obfs"] != "plain" {
		t.Fatalf("ssr proxy lost a field: %+v", proxy)
	}
}

// TestExportMihomoFieldMapping pins the reverse mapping of §2.2 for the
// protocols with the most fields.
func TestExportMihomoFieldMapping(t *testing.T) {
	items := fixtureNodes(t)
	body, _, _, err := Export(items, FormatMihomo, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	document := decodeMihomo(t, body)
	byName := map[string]map[string]any{}
	for _, proxy := range document.Proxies {
		name, _ := proxy["name"].(string)
		byName[name] = proxy
	}

	ss := byName["ss-tokyo"]
	if ss["type"] != "ss" || ss["cipher"] != "aes-256-gcm" || ss["password"] != "ss-secret-credential" ||
		ss["server"] != "198.51.100.10" || ss["port"] != 8388 {
		t.Fatalf("ss proxy = %+v", ss)
	}

	vmess := byName["vmess-ws"]
	if vmess["type"] != "vmess" || vmess["uuid"] != "11111111-2222-3333-4444-555555555555" ||
		vmess["alterId"] != 0 || vmess["cipher"] != "auto" || vmess["tls"] != true ||
		vmess["network"] != "ws" || vmess["sni"] != "vmess.example.com" {
		t.Fatalf("vmess proxy = %+v", vmess)
	}
	wsOpts, ok := vmess["ws-opts"].(map[string]any)
	if !ok || wsOpts["path"] != "/ws" {
		t.Fatalf("vmess ws-opts = %+v", vmess["ws-opts"])
	}
	headers, ok := wsOpts["headers"].(map[string]any)
	if !ok || headers["Host"] != "vmess.example.com" {
		t.Fatalf("vmess ws headers = %+v", wsOpts["headers"])
	}

	vless := byName["vless-reality"]
	if vless["type"] != "vless" || vless["flow"] != "xtls-rprx-vision" ||
		vless["client-fingerprint"] != "chrome" {
		t.Fatalf("vless proxy = %+v", vless)
	}
	reality, ok := vless["reality-opts"].(map[string]any)
	if !ok || reality["public-key"] != "PUBLIC-KEY-FIXTURE" || reality["short-id"] != "0123abcd" {
		t.Fatalf("vless reality-opts = %+v", vless["reality-opts"])
	}

	hy2 := byName["hy2-1"]
	if hy2["type"] != "hysteria2" || hy2["password"] != "hy2-secret-credential" ||
		hy2["obfs"] != "salamander" || hy2["obfs-password"] != "obfs-secret" {
		t.Fatalf("hysteria2 proxy = %+v", hy2)
	}

	tuic := byName["tuic-1"]
	if tuic["type"] != "tuic" || tuic["uuid"] != "bbbbbbbb-cccc-dddd-eeee-ffffffffffff" ||
		tuic["password"] != "tuic-secret-credential" {
		t.Fatalf("tuic proxy = %+v", tuic)
	}

	socks := byName["socks-1"]
	if socks["type"] != "socks5" || socks["username"] != "socks-user" || socks["password"] != "socks-secret-credential" {
		t.Fatalf("socks proxy = %+v", socks)
	}

	http := byName["http-1"]
	if http["type"] != "http" || http["username"] != "http-user" {
		t.Fatalf("http proxy = %+v", http)
	}
}

// TestExportMihomoWireGuardEndpoint checks the wireguard reverse mapping.
func TestExportMihomoWireGuardEndpoint(t *testing.T) {
	body, _, report, err := Export([]Item{fixtureEndpointNode(t)}, FormatMihomo, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if report.Exported != 1 {
		t.Fatalf("report = %+v", report)
	}
	document := decodeMihomo(t, body)
	proxy := document.Proxies[0]
	if proxy["type"] != "wireguard" || proxy["server"] != "203.0.113.9" || proxy["port"] != 51820 ||
		proxy["private-key"] != "WG-PRIVATE-KEY-FIXTURE" || proxy["public-key"] != "WG-PUBLIC-KEY-FIXTURE" ||
		proxy["ip"] != "10.7.0.2/32" {
		t.Fatalf("wireguard proxy = %+v", proxy)
	}
	allowed, ok := proxy["allowed-ips"].([]any)
	if !ok || len(allowed) != 1 || allowed[0] != "0.0.0.0/0" {
		t.Fatalf("wireguard allowed-ips = %+v", proxy["allowed-ips"])
	}
}

// TestExportMihomoWireGuardWithHostnameEndpointIsSkipped documents the one
// wireguard convergence Prism refuses: mihomo resolves a host name with the
// system resolver, which would change the dial path.
func TestExportMihomoWireGuardWithHostnameEndpointIsSkipped(t *testing.T) {
	item := Item{
		Name:       "wg-hostname",
		RawOptions: []byte(`{"prism_node":1,"engine":"singbox","kind":"endpoint","main":{"type":"wireguard","address":["10.7.0.2/32"],"private_key":"KEY","peers":[{"address":"wg.example.com","port":51820,"public_key":"PEER"}]}}`),
	}
	_, _, report, err := Export([]Item{item}, FormatMihomo, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Reason != "NOT_REPRESENTABLE:mihomo" {
		t.Fatalf("report = %+v", report)
	}
}

// TestExportMihomoShadowsocksShadowTLSChain checks the §2.2 ss+shadow-tls row.
func TestExportMihomoShadowsocksShadowTLSChain(t *testing.T) {
	body, _, report, err := Export([]Item{fixtureChainNode(t)}, FormatMihomo, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if report.Exported != 1 || len(report.Skipped) != 0 {
		t.Fatalf("report = %+v", report)
	}
	document := decodeMihomo(t, body)
	proxy := document.Proxies[0]
	if proxy["type"] != "ss" || proxy["plugin"] != "shadow-tls" {
		t.Fatalf("chain proxy = %+v", proxy)
	}
	pluginOpts, ok := proxy["plugin-opts"].(map[string]any)
	if !ok || pluginOpts["host"] != "198.51.100.20" ||
		pluginOpts["password"] != "shadowtls-secret-credential" || pluginOpts["version"] != 3 {
		t.Fatalf("chain plugin-opts = %+v", proxy["plugin-opts"])
	}
}

// TestExportMihomoGroupTagCollision checks a node named AUTO cannot shadow the
// auto group.
func TestExportMihomoGroupTagCollision(t *testing.T) {
	item := fixtureNodes(t)[0]
	item.Name = "AUTO"
	body, _, _, err := Export([]Item{item}, FormatMihomo, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	document := decodeMihomo(t, body)
	names := map[string]bool{}
	for _, proxy := range document.Proxies {
		name, _ := proxy["name"].(string)
		names[name] = true
	}
	for _, group := range document.Groups {
		if names[group.Name] {
			t.Fatalf("group name %q collides with a proxy name", group.Name)
		}
	}
	if document.Groups[1].Name == "AUTO" {
		t.Fatalf("auto group kept the colliding name: %+v", document.Groups[1])
	}
}

// TestExportMihomoSnapshotContainsNoSkippedNodes checks the golden document has
// exactly the representable nodes, so a future regression that starts dropping
// nodes silently is caught by the count.
func TestExportMihomoSnapshotContainsNoSkippedNodes(t *testing.T) {
	items := append(fixtureNodes(t), fixtureEndpointNode(t), fixtureChainNode(t))
	body, _, _, err := Export(items, FormatMihomo, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	document := decodeMihomo(t, body)
	if len(document.Proxies) != len(items) {
		t.Fatalf("proxies = %d, want %d", len(document.Proxies), len(items))
	}
	// The YAML must not need a top-level null for an empty proxies list.
	if strings.Contains(string(body), "proxies: []") {
		t.Fatalf("proxies list is empty:\n%s", body)
	}
}
