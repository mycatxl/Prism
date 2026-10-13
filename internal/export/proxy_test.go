package export

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"prism/internal/node"
)

// proxyFixtureItem builds one node document for the address export.
func proxyFixtureItem(t *testing.T, name string, raw string) Item {
	t.Helper()
	document := json.RawMessage(raw)
	if _, err := node.ParseNodeDoc(document); err != nil {
		t.Fatalf("fixture %s does not parse: %v", name, err)
	}
	return Item{Name: name, RawOptions: document}
}

// proxyLines renders items in the proxy format and returns the non-empty lines.
func proxyLines(t *testing.T, items []Item) ([]string, Report) {
	t.Helper()
	body, contentType, report, err := Export(items, FormatProxy, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if contentType != "text/plain; charset=utf-8" {
		t.Fatalf("content type = %q", contentType)
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, report
	}
	return strings.Split(trimmed, "\n"), report
}

func TestExportProxyRendersEveryAddressableShape(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "sing-box socks with credentials",
			raw:  `{"type":"socks","tag":"s","server":"socks.example.com","server_port":1080,"username":"user","password":"pass"}`,
			want: "socks5://user:pass@socks.example.com:1080",
		},
		{
			name: "sing-box socks4 keeps its version in the scheme",
			raw:  `{"type":"socks","tag":"s","server":"socks.example.com","server_port":1080,"version":"4","username":"user","password":"pass"}`,
			want: "socks4://user:pass@socks.example.com:1080",
		},
		{
			name: "sing-box socks4a keeps its version in the scheme",
			raw:  `{"type":"socks","tag":"s","server":"socks.example.com","server_port":1080,"version":"4a"}`,
			want: "socks4a://socks.example.com:1080",
		},
		{
			name: "sing-box http with credentials",
			raw:  `{"type":"http","tag":"h","server":"http.example.com","server_port":8080,"username":"user","password":"pass"}`,
			want: "http://user:pass@http.example.com:8080",
		},
		{
			name: "sing-box http over TLS is https",
			raw:  `{"type":"http","tag":"h","server":"http.example.com","server_port":443,"username":"user","password":"pass","tls":{"enabled":true,"server_name":"http.example.com"}}`,
			want: "https://user:pass@http.example.com:443",
		},
		{
			name: "mihomo socks proxy",
			raw:  `{"prism_node":1,"engine":"mihomo","kind":"proxy","proxy":{"type":"socks5","name":"m","server":"198.51.100.30","port":1080,"username":"user","password":"pass"}}`,
			want: "socks5://user:pass@198.51.100.30:1080",
		},
		{
			name: "mihomo http proxy",
			raw:  `{"prism_node":1,"engine":"mihomo","kind":"proxy","proxy":{"type":"http","name":"m","server":"198.51.100.30","port":8080}}`,
			want: "http://198.51.100.30:8080",
		},
		{
			name: "mihomo http proxy with tls is https",
			raw:  `{"prism_node":1,"engine":"mihomo","kind":"proxy","proxy":{"type":"http","name":"m","server":"198.51.100.30","port":8443,"tls":true}}`,
			want: "https://198.51.100.30:8443",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines, report := proxyLines(t, []Item{proxyFixtureItem(t, "n", tc.raw)})
			if len(lines) != 1 {
				t.Fatalf("lines = %v (report %+v)", lines, report)
			}
			// The name is a fragment, so compare the address part only.
			address := strings.SplitN(lines[0], "#", 2)[0]
			if address != tc.want {
				t.Fatalf("address = %q, want %q", address, tc.want)
			}
			if report.Exported != 1 || report.SkipCount() != 0 {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

// A node of any other protocol has no address a browser can dial, so it is
// skipped with a reason rather than emitted in a spelling the target cannot
// use. One wrong line in a pasted list fails much later, far from the export.
func TestExportProxySkipsProtocolsWithoutAnAddress(t *testing.T) {
	items := []Item{
		fixtureNodes(t)[0],     // shadowsocks
		fixtureNodes(t)[1],     // vmess
		fixtureNodes(t)[4],     // hysteria2
		fixtureMihomoNode(t),   // ssr
		fixtureChainNode(t),    // chain: the reachable address is its detour's
		fixtureEndpointNode(t), // wireguard endpoint
	}
	lines, report := proxyLines(t, items)
	if len(lines) != 0 {
		t.Fatalf("lines = %v, want none", lines)
	}
	if report.Exported != 0 {
		t.Fatalf("exported = %d, want 0", report.Exported)
	}
	if report.SkipCount() != len(items) {
		t.Fatalf("skipped = %d, want %d (%+v)", report.SkipCount(), len(items), report.Skipped)
	}
	for _, entry := range report.Skipped {
		if entry.Reason != NotRepresentable(FormatProxy) {
			t.Fatalf("skip reason = %q, want %q", entry.Reason, NotRepresentable(FormatProxy))
		}
		if entry.Name == "" {
			t.Fatal("a skip entry must name the node")
		}
	}
}

// SOCKS over TLS has no address spelling a browser understands; exporting it as
// plain socks5 would produce a connection that fails on the transport.
func TestExportProxyRefusesSocksOverTLS(t *testing.T) {
	raw := `{"type":"socks","tag":"s","server":"socks.example.com","server_port":1080,"username":"user","password":"pass","tls":{"enabled":true}}`
	lines, report := proxyLines(t, []Item{proxyFixtureItem(t, "socks-tls", raw)})
	if len(lines) != 0 {
		t.Fatalf("lines = %v, want none", lines)
	}
	if report.SkipCount() != 1 {
		t.Fatalf("report = %+v", report)
	}
}

// An address the client cannot dial is worse than a skip: the failure would
// surface at connection time instead of at export time.
func TestExportProxySkipsUnaddressableNodes(t *testing.T) {
	cases := map[string]string{
		"no port":       `{"type":"socks","server":"socks.example.com"}`,
		"port too big":  `{"type":"socks","server":"socks.example.com","server_port":70000}`,
		"no server":     `{"type":"socks","server":"","server_port":1080}`,
		"empty address": `{"type":"http","server":"","server_port":0}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			lines, report := proxyLines(t, []Item{proxyFixtureItem(t, "n", raw)})
			if len(lines) != 0 {
				t.Fatalf("lines = %v, want none", lines)
			}
			if report.SkipCount() != 1 {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

// A password with no user name is carried in the user position, which is how
// the share-link parsers read it back.
func TestExportProxyCarriesAPasswordWithoutAUser(t *testing.T) {
	raw := `{"type":"socks","server":"socks.example.com","server_port":1080,"password":"only-a-password"}`
	lines, report := proxyLines(t, []Item{proxyFixtureItem(t, "n", raw)})
	if len(lines) != 1 {
		t.Fatalf("lines = %v (report %+v)", lines, report)
	}
	address := strings.SplitN(lines[0], "#", 2)[0]
	if address != "socks5://only-a-password@socks.example.com:1080" {
		t.Fatalf("address = %q", address)
	}
}

// Credentials that need escaping must survive as a usable address rather than
// splitting the authority in the wrong place.
func TestExportProxyEscapesCredentials(t *testing.T) {
	raw := `{"type":"http","server":"http.example.com","server_port":8080,"username":"user@corp","password":"p@ss:word/1"}`
	lines, report := proxyLines(t, []Item{proxyFixtureItem(t, "n", raw)})
	if len(lines) != 1 {
		t.Fatalf("lines = %v (report %+v)", lines, report)
	}
	address := strings.SplitN(lines[0], "#", 2)[0]
	if address != "http://user%40corp:p%40ss%3Aword%2F1@http.example.com:8080" {
		t.Fatalf("address = %q", address)
	}
}

// The name is the fragment, so a list pasted into a tool that shows names stays
// readable, and a name needing escaping must not break the address.
func TestExportProxyCarriesTheNodeName(t *testing.T) {
	raw := `{"type":"socks","server":"socks.example.com","server_port":1080}`
	item := proxyFixtureItem(t, "东京 #1", raw)
	lines, _ := proxyLines(t, []Item{item})
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	if !strings.Contains(lines[0], "#") {
		t.Fatalf("line %q carries no name", lines[0])
	}
	address := strings.SplitN(lines[0], "#", 2)[0]
	if address != "socks5://socks.example.com:1080" {
		t.Fatalf("address = %q", address)
	}
}

// Every line must be a parseable URL whose host and port survive the round trip.
func TestExportProxyLinesAreParseable(t *testing.T) {
	items := []Item{
		proxyFixtureItem(t, "socks", `{"type":"socks","server":"socks.example.com","server_port":1080,"username":"u","password":"p"}`),
		proxyFixtureItem(t, "http", `{"type":"http","server":"10.0.0.1","server_port":3128}`),
		proxyFixtureItem(t, "https", `{"type":"http","server":"http.example.com","server_port":8443,"tls":{"enabled":true}}`),
	}
	lines, report := proxyLines(t, items)
	if len(lines) != len(items) {
		t.Fatalf("lines = %v (report %+v)", lines, report)
	}
	for _, line := range lines {
		address := strings.SplitN(line, "#", 2)[0]
		parsed, err := url.Parse(address)
		if err != nil {
			t.Fatalf("parse %q: %v", address, err)
		}
		if parsed.Host == "" || parsed.Port() == "" {
			t.Fatalf("%q has no host:port", address)
		}
	}
}

// An empty selection is an empty list, not an error: a filter that currently
// matches nothing should download an empty file.
func TestExportProxyEmptyInputIsAnEmptyBody(t *testing.T) {
	body, _, report, err := Export(nil, FormatProxy, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("body = %q, want empty", body)
	}
	if report.Exported != 0 || report.SkipCount() != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestProxyFormatIsRegistered(t *testing.T) {
	if !ValidFormat(FormatProxy) {
		t.Fatal("the proxy format is not registered")
	}
	if FileName(FormatProxy) != "prism-proxy.txt" {
		t.Fatalf("file name = %q", FileName(FormatProxy))
	}
	if ContentType(FormatProxy) != "text/plain; charset=utf-8" {
		t.Fatalf("content type = %q", ContentType(FormatProxy))
	}
}
