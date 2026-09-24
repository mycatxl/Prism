package export

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateGolden regenerates the golden files: go test ./internal/export -run Golden -update
var updateGolden = os.Getenv("UPDATE_GOLDEN") != ""

// assertGolden compares body against testdata/<name> and writes it when
// UPDATE_GOLDEN is set.
func assertGolden(t *testing.T, name string, body []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create testdata dir: %v", err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(body)) {
		t.Fatalf("output differs from golden %s\n--- got ---\n%s\n--- want ---\n%s",
			path, body, want)
	}
}

// credentialMarkers are the secrets the fixtures embed. No export may contain
// any of them except the client-config formats the user explicitly asked for.
var credentialMarkers = []string{
	"ss-secret-credential",
	"trojan-secret-credential",
	"hy2-secret-credential",
	"obfs-secret",
	"tuic-secret-credential",
	"anytls-secret-credential",
	"socks-secret-credential",
	"http-secret-credential",
	"chain-secret-credential",
	"shadowtls-secret-credential",
	"ssr-secret-credential",
	"WG-PRIVATE-KEY-FIXTURE",
}

func assertNoCredential(t *testing.T, format string, body []byte) {
	t.Helper()
	for _, marker := range credentialMarkers {
		if bytes.Contains(body, []byte(marker)) {
			t.Fatalf("%s body leaked credential %q", format, marker)
		}
	}
}

// TestExportRejectsUnknownFormat checks the only error path of Export.
func TestExportRejectsUnknownFormat(t *testing.T) {
	if _, _, _, err := Export(nil, "surge", Options{}); err == nil {
		t.Fatal("Export accepted an unsupported format")
	}
	if _, _, _, err := Export(nil, "", Options{}); err == nil {
		t.Fatal("Export accepted an empty format")
	}
}

// TestExportEmptyInputProducesEmptyBody checks every format survives no nodes.
func TestExportEmptyInputProducesEmptyBody(t *testing.T) {
	for _, format := range Formats() {
		body, contentType, report, err := Export(nil, format, Options{IncludeIntel: true})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if contentType != ContentType(format) {
			t.Fatalf("%s: content type %q", format, contentType)
		}
		if report.Exported != 0 || len(report.Skipped) != 0 {
			t.Fatalf("%s: unexpected report %+v", format, report)
		}
		// An empty pool legitimately renders an empty subscription document for
		// the two line-oriented formats, so a client polling a filter that
		// currently matches nothing gets an empty list instead of an error.
		if format == FormatV2rayN || format == FormatURI {
			continue
		}
		if len(body) == 0 {
			t.Fatalf("%s: empty body", format)
		}
	}
}

// TestExportJSONCarriesReportInBody checks the json format reports skips in the
// response body, as §4.1 requires.
func TestExportJSONCarriesReportInBody(t *testing.T) {
	items := append(fixtureNodes(t), fixtureMihomoNode(t))
	body, _, report, err := Export(items, FormatJSON, Options{IncludeIntel: true})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if report.Exported != len(items) {
		t.Fatalf("exported = %d, want %d", report.Exported, len(items))
	}
	var decoded struct {
		Exported int    `json:"exported"`
		Skipped  []Skip `json:"skipped"`
		Items    []struct {
			Name     string `json:"name"`
			Purity   string `json:"purity_score"`
			Band     string `json:"purity_band"`
			EgressV4 string `json:"egress_ipv4"`
			Colo     string `json:"colo"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode json export: %v", err)
	}
	if decoded.Exported != len(items) || len(decoded.Items) != len(items) {
		t.Fatalf("json export shape: %+v", decoded)
	}
	if decoded.Skipped == nil {
		t.Fatal("json export skipped must be an array, not null")
	}
	first := decoded.Items[0]
	if first.Name != "ss-tokyo" || first.Purity != "96" || first.Band != "excellent" || first.EgressV4 != "198.51.100.10" || first.Colo != "NRT" {
		t.Fatalf("first json row = %+v", first)
	}
	assertNoCredential(t, FormatJSON, body)
}

// TestExportAnalysisFormatsNeverCarryCredentials is the R6 guard for csv/json.
func TestExportAnalysisFormatsNeverCarryCredentials(t *testing.T) {
	items := []Item{
		fixtureNodes(t)[0],
		fixtureEndpointNode(t),
		fixtureChainNode(t),
		fixtureMihomoNode(t),
	}
	for _, format := range []string{FormatCSV, FormatJSON} {
		body, _, _, err := Export(items, format, Options{IncludeIntel: true})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		assertNoCredential(t, format, body)
	}
}

// TestExportSkipsUnrepresentableSingboxNodes checks the singbox report.
func TestExportSkipsUnrepresentableSingboxNodes(t *testing.T) {
	items := []Item{fixtureNodes(t)[0], fixtureMihomoNode(t)}
	_, _, report, err := Export(items, FormatSingbox, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if report.Exported != 1 {
		t.Fatalf("exported = %d, want 1", report.Exported)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Reason != "NOT_REPRESENTABLE:singbox" {
		t.Fatalf("skipped = %+v", report.Skipped)
	}
	if report.Skipped[0].Name != "ssr-1" {
		t.Fatalf("skipped name = %q", report.Skipped[0].Name)
	}
}

// TestExportSkipsUnrepresentableMihomoNodes checks the two distinct mihomo
// reasons: a specific proxy type and a chain shape.
func TestExportSkipsUnrepresentableMihomoNodes(t *testing.T) {
	openvpn := Item{
		Name:       "ovpn-1",
		RawOptions: json.RawMessage(`{"prism_node":1,"engine":"singbox","kind":"endpoint","name":"ovpn-1","main":{"type":"openvpn-client","mode":"static_key","static_key":["KEY"],"address":["10.0.0.2/32"]}}`),
	}
	items := []Item{fixtureNodes(t)[0], openvpn}
	_, _, report, err := Export(items, FormatMihomo, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if report.Exported != 1 {
		t.Fatalf("exported = %d, want 1", report.Exported)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Reason != "NOT_REPRESENTABLE:mihomo" {
		t.Fatalf("skipped = %+v", report.Skipped)
	}

	// A chain that mihomo cannot express uses the dedicated chain reason.
	chain := fixtureChainNode(t)
	vmessWithDetour := Item{
		Name:       "vmess-chain",
		RawOptions: json.RawMessage(`{"prism_node":1,"engine":"singbox","kind":"chain","name":"vmess-chain","main":{"type":"vmess","server":"a.example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555","detour":"d0"},"deps":[{"type":"shadowtls","tag":"d0","server":"b.example.com","server_port":443,"version":3,"password":"x"}]}`),
	}
	_, _, report, err = Export([]Item{vmessWithDetour}, FormatMihomo, Options{})
	if err != nil {
		t.Fatalf("Export chain: %v", err)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Reason != ReasonNotRepresentableMihomoChain {
		t.Fatalf("chain skipped = %+v", report.Skipped)
	}

	// The shadowsocks + shadowtls chain is expressible as an ss plugin.
	_, _, report, err = Export([]Item{chain}, FormatMihomo, Options{})
	if err != nil {
		t.Fatalf("Export ss chain: %v", err)
	}
	if report.Exported != 1 || len(report.Skipped) != 0 {
		t.Fatalf("ss+shadowtls chain report = %+v", report)
	}
}

// TestExportSkipsMalformedDocuments checks a node document that cannot be
// TestExportSkipsMalformedDocuments checks a node document that cannot be
// parsed at all is reported, never silently dropped.
func TestExportSkipsMalformedDocuments(t *testing.T) {
	items := []Item{
		{Name: "broken", RawOptions: json.RawMessage(`{"prism_node":1,"engine":"nope","kind":"proxy"}`)},
		{Name: "not-json", RawOptions: json.RawMessage(`not json`)},
	}
	for _, format := range Formats() {
		_, _, report, err := Export(items, format, Options{})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if len(report.Skipped) != 2 {
			t.Fatalf("%s: skipped = %+v", format, report.Skipped)
		}
	}
}

// TestExportNamesAreUniqueAcrossFormats checks the " #2" suffix rule.
func TestExportNamesAreUniqueAcrossFormats(t *testing.T) {
	node := fixtureNodes(t)[0]
	items := []Item{node, node, node}
	for _, format := range []string{FormatSingbox, FormatMihomo, FormatURI} {
		names := RenderNames(items, Options{NameTemplate: "{name}"})
		if names[0] != "ss-tokyo" || names[1] != "ss-tokyo #2" || names[2] != "ss-tokyo #3" {
			t.Fatalf("%s names = %v", format, names)
		}
	}
}

// TestExportNameSuffixFitsBudget checks the suffix is applied inside the 64
// character budget.
func TestExportNameSuffixFitsBudget(t *testing.T) {
	long := strings.Repeat("x", maxNameLength)
	items := []Item{
		{Name: long, RawOptions: fixtureNodes(t)[0].RawOptions},
		{Name: long, RawOptions: fixtureNodes(t)[0].RawOptions},
	}
	names := RenderNames(items, Options{})
	if len(names[0]) != maxNameLength {
		t.Fatalf("first name length = %d", len(names[0]))
	}
	if len(names[1]) > maxNameLength || !strings.HasSuffix(names[1], " #2") {
		t.Fatalf("second name = %q", names[1])
	}
}
