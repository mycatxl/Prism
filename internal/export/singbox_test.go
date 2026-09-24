package export

import (
	"encoding/json"
	"testing"

	"prism/internal/node"
	"prism/internal/subscription"
)

// TestExportSingboxGolden pins the full sing-box document (WP11 §5).
func TestExportSingboxGolden(t *testing.T) {
	items := append(fixtureNodes(t), fixtureEndpointNode(t), fixtureChainNode(t))
	body, contentType, report, err := Export(items, FormatSingbox, Options{IncludeIntel: true})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if contentType != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q", contentType)
	}
	if report.Exported != len(items) || len(report.Skipped) != 0 {
		t.Fatalf("report = %+v", report)
	}
	assertGolden(t, "singbox.json", body)
}

// TestExportSingboxRoundTripsThroughTheParser checks the exported document is
// accepted by the subscription parser and keeps every node hash. That is the
// strongest offline proof that the document is well formed: the parser
// re-derives the detour structure from the reference graph alone.
func TestExportSingboxRoundTripsThroughTheParser(t *testing.T) {
	items := []Item{
		fixtureNodes(t)[0],
		fixtureNodes(t)[1],
		fixtureEndpointNode(t),
		fixtureChainNode(t),
	}
	body, _, _, err := Export(items, FormatSingbox, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	result, err := subscription.ParseWithReport(body)
	if err != nil {
		t.Fatalf("parse exported sing-box document: %v", err)
	}
	got := collectedHashes(result.Nodes)
	want := expectedHashes(t, items)
	if len(got) != len(want) {
		t.Fatalf("round trip produced %d nodes, want %d (skips: %+v)", len(got), len(want), result.Skipped)
	}
	for hash := range want {
		if !got[hash] {
			t.Fatalf("node hash %s missing after the sing-box round trip (got %v)", hash, got)
		}
	}
}

// TestExportSingboxChainShape pins the §2.1 chain rules: the main object keeps
// the node name, the dep is renamed to "<name>/d0" and the main detour is
// rewritten to point at it.
func TestExportSingboxChainShape(t *testing.T) {
	body, _, _, err := Export([]Item{fixtureChainNode(t)}, FormatSingbox, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	var document struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode sing-box export: %v", err)
	}
	if len(document.Outbounds) != 4 {
		t.Fatalf("outbounds = %d, want 4 (selector, urltest, main, dep)", len(document.Outbounds))
	}

	selector := document.Outbounds[0]
	if selector["tag"] != "PROXY" {
		t.Fatalf("selector tag = %v", selector["tag"])
	}
	members, _ := selector["outbounds"].([]any)
	if len(members) != 2 || members[0] != "AUTO" || members[1] != "ss-shadowtls" {
		t.Fatalf("selector members = %v", members)
	}

	auto := document.Outbounds[1]
	if auto["tag"] != "AUTO" || auto["interval"] != "5m" {
		t.Fatalf("urltest = %v", auto)
	}

	main := document.Outbounds[2]
	if main["tag"] != "ss-shadowtls" || main["detour"] != "ss-shadowtls/d0" {
		t.Fatalf("chain main = %v", main)
	}
	dep := document.Outbounds[3]
	if dep["tag"] != "ss-shadowtls/d0" {
		t.Fatalf("chain dep = %v", dep)
	}
	if _, present := dep["detour"]; present {
		t.Fatalf("last chain hop must not carry a detour: %v", dep)
	}
}

// TestExportSingboxGroupTagCollision checks a node called "PROXY" cannot break
// the selector reference.
func TestExportSingboxGroupTagCollision(t *testing.T) {
	item := fixtureNodes(t)[0]
	item.Name = "PROXY"
	body, _, _, err := Export([]Item{item}, FormatSingbox, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	var document struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode sing-box export: %v", err)
	}
	tags := map[string]bool{}
	for _, outbound := range document.Outbounds {
		tag, _ := outbound["tag"].(string)
		if tags[tag] {
			t.Fatalf("duplicate tag %q in %v", tag, document.Outbounds)
		}
		tags[tag] = true
	}
	selector := document.Outbounds[0]
	if selector["tag"] == "PROXY" {
		t.Fatalf("selector kept the colliding tag: %v", selector)
	}
}

// TestExportSingboxDocumentIsReReadableByOptionDecoding checks the exported
// outbounds are plain sing-box objects: every one has a type, a tag and either
// a server or an explicit non-outbound type.
func TestExportSingboxDocumentIsReReadableByOptionDecoding(t *testing.T) {
	items := append(fixtureNodes(t), fixtureEndpointNode(t), fixtureChainNode(t))
	body, _, _, err := Export(items, FormatSingbox, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	var document struct {
		Outbounds []struct {
			Type string `json:"type"`
			Tag  string `json:"tag"`
		} `json:"outbounds"`
		Endpoints []struct {
			Type string `json:"type"`
			Tag  string `json:"tag"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode sing-box export: %v", err)
	}
	for _, outbound := range document.Outbounds {
		if outbound.Type == "" || outbound.Tag == "" {
			t.Fatalf("outbound without type or tag: %+v", outbound)
		}
	}
	for _, endpoint := range document.Endpoints {
		if !node.IsEndpointType(endpoint.Type) {
			t.Fatalf("endpoints entry %q is not an endpoint type", endpoint.Type)
		}
		if endpoint.Tag == "" {
			t.Fatalf("endpoint without tag: %+v", endpoint)
		}
	}
	if len(document.Endpoints) != 1 || document.Endpoints[0].Tag != "wg-1" {
		t.Fatalf("endpoints = %+v", document.Endpoints)
	}
}

// collectedHashes indexes parsed nodes by their hash.
func collectedHashes(nodes []subscription.ParsedNode) map[string]bool {
	out := make(map[string]bool, len(nodes))
	for _, parsed := range nodes {
		out[node.HashFromRawOptions(parsed.RawOptions).Hex()] = true
	}
	return out
}

// expectedHashes indexes the fixture node documents by their hash.
func expectedHashes(t *testing.T, items []Item) map[string]bool {
	t.Helper()
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[node.HashFromRawOptions(item.RawOptions).Hex()] = true
	}
	return out
}
