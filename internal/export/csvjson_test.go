package export

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
)

// TestExportCSVGolden pins the analysis columns (WP11 §2.4).
func TestExportCSVGolden(t *testing.T) {
	items := []Item{
		fixtureNodes(t)[0],
		fixtureEndpointNode(t),
		fixtureChainNode(t),
		fixtureMihomoNode(t),
	}
	body, contentType, report, err := Export(items, FormatCSV, Options{IncludeIntel: true})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if contentType != "text/csv; charset=utf-8" {
		t.Fatalf("content type = %q", contentType)
	}
	if report.Exported != len(items) {
		t.Fatalf("report = %+v", report)
	}
	assertGolden(t, "export.csv", body)
}

// TestExportCSVColumns checks the header and the value mapping of one row.
func TestExportCSVColumns(t *testing.T) {
	item := fixtureNodes(t)[0]
	item.Subscription = "sub-a"
	item.Healthy = true
	body, _, _, err := Export([]Item{item}, FormatCSV, Options{IncludeIntel: true})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("rows = %d, want 2", len(records))
	}
	header := map[string]int{}
	for i, name := range records[0] {
		header[name] = i
	}
	for _, column := range exportColumns {
		if _, ok := header[column]; !ok {
			t.Fatalf("missing column %q in %v", column, records[0])
		}
	}
	row := records[1]
	expect := map[string]string{
		"name":            "ss-tokyo",
		"node_hash":       "11111111111111111111111111111111",
		"engine":          "singbox",
		"protocol":        "shadowsocks",
		"protocol_detail": "shadowsocks",
		"subscription":    "sub-a",
		"egress_ipv4":     "198.51.100.10",
		"egress_ipv6":     "",
		"colo":            "NRT",
		"country":         "JP",
		"city":            "Tokyo",
		"asn":             "AS64500",
		"as_org":          "Example Residential ISP",
		"ip_type":         "residential",
		"native":          "true",
		"purity_score":    "96",
		"purity_band":     "excellent",
		"confidence":      "80",
		"verdict":         "favorable",
		"flags":           "",
		"healthy":         "true",
		"assessed_at":     "2026-09-24T12:00:00Z",
		"latency_ms":      "42.5",
	}
	for column, want := range expect {
		if got := row[header[column]]; got != want {
			t.Fatalf("column %s = %q, want %q", column, got, want)
		}
	}
	if checks := row[header["checks"]]; !strings.Contains(checks, "proxycheck:valid") {
		t.Fatalf("checks = %q", checks)
	}
}

// TestExportCSVOmitsIntelWhenNotRequested checks the opt-in flag.
func TestExportCSVOmitsIntelWhenNotRequested(t *testing.T) {
	item := fixtureNodes(t)[0]
	item.Intel = intelFixture()
	body, _, _, err := Export([]Item{item}, FormatCSV, Options{IncludeIntel: false})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if strings.Contains(string(body), "198.51.100.10") {
		t.Fatalf("egress IP leaked without the opt-in: %s", body)
	}
	if strings.Contains(string(body), "excellent") {
		t.Fatalf("purity band leaked without the opt-in: %s", body)
	}
}

// TestExportJSONUsesTheSameColumnsAsCSV checks the two analysis formats stay in
// step: the golden json keys must be exactly the csv columns.
func TestExportJSONUsesTheSameColumnsAsCSV(t *testing.T) {
	items := []Item{fixtureNodes(t)[0], fixtureEndpointNode(t)}
	body, _, _, err := Export(items, FormatJSON, Options{IncludeIntel: true})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	for _, column := range exportColumns {
		if !strings.Contains(string(body), `"`+column+`"`) {
			t.Fatalf("json export is missing column %q", column)
		}
	}
}

// TestExportCSVQuotesNamesWithCommas checks encoding/csv escaping keeps a comma
// inside a template-rendered name from breaking the file.
func TestExportCSVQuotesNamesWithCommas(t *testing.T) {
	item := fixtureNodes(t)[0]
	item.Name = "tokyo, japan"
	body, _, _, err := Export([]Item{item}, FormatCSV, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if records[1][0] != "tokyo, japan" {
		t.Fatalf("name = %q", records[1][0])
	}
}

// TestExportCSVChainProtocolDetail checks the chain detail rendering.
func TestExportCSVChainProtocolDetail(t *testing.T) {
	body, _, _, err := Export([]Item{fixtureChainNode(t)}, FormatCSV, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	index := map[string]int{}
	for i, name := range records[0] {
		index[name] = i
	}
	if got := records[1][index["protocol_detail"]]; got != "shadowsocks+shadowtls" {
		t.Fatalf("protocol_detail = %q", got)
	}
	if got := records[1][index["protocol"]]; got != "shadowsocks" {
		t.Fatalf("protocol = %q", got)
	}
}

// TestExportJSONMihomoProtocolName checks a mihomo node is named the way the
// API names it.
func TestExportJSONMihomoProtocolName(t *testing.T) {
	body, _, _, err := Export([]Item{fixtureMihomoNode(t)}, FormatJSON, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !strings.Contains(string(body), `"engine": "singbox"`) && !strings.Contains(string(body), `"engine": "mihomo"`) {
		t.Fatalf("engine missing from %s", body)
	}
	if !strings.Contains(string(body), `"protocol": "ssr"`) {
		t.Fatalf("ssr protocol missing from %s", body)
	}
}
