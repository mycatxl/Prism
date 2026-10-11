package export

import (
	"encoding/json"
	"strconv"
	"testing"

	"prism/internal/node"
	"prism/internal/subscription"
)

// TestExportSingboxGolden pins the full sing-box document.
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

// TestExportSingboxChainShape pins the chain rules: the main object keeps
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

// singboxCollisionItems is the tag-collision fixture: the
// chain node "x" emits its dependency as "x/d0", and the second node is
// literally called "x/d0". Both tags used to reach the document, which sing-box
// rejects as a whole ("duplicate outbound/endpoint tag").
//
// The plain node is exported first on purpose: its name claims "x/d0" before the
// chain dependency asks for the same tag, so it is the dependency that has to be
// renamed — the case where a reference would silently point at the wrong object.
func singboxCollisionItems(t *testing.T) []Item {
	t.Helper()
	chain := json.RawMessage(`{"prism_node":1,"engine":"singbox","kind":"chain","name":"x","main":{"type":"shadowsocks","server":"198.51.100.20","server_port":443,"method":"aes-256-gcm","password":"chain-secret-credential","detour":"d0"},"deps":[{"type":"shadowtls","tag":"d0","server":"198.51.100.20","server_port":443,"version":3,"password":"shadowtls-secret-credential"}]}`)
	plain := json.RawMessage(`{"type":"shadowsocks","tag":"x/d0","server":"198.51.100.30","server_port":8388,"method":"aes-256-gcm","password":"plain-secret-credential"}`)
	for _, raw := range []json.RawMessage{chain, plain} {
		if _, err := node.ParseNodeDoc(raw); err != nil {
			t.Fatalf("the collision fixture does not parse: %v", err)
		}
	}
	return []Item{
		{Name: "x/d0", Hash: "dddddddddddddddddddddddddddddddd", RawOptions: plain},
		{Name: "x", Hash: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", RawOptions: chain},
	}
}

// singboxDocument is the decoded shape of an exported sing-box document.
type singboxDocument struct {
	Outbounds []map[string]any `json:"outbounds"`
	Endpoints []map[string]any `json:"endpoints"`
}

// decodeSingboxDocument decodes an exported document into its objects.
func decodeSingboxDocument(t *testing.T, body []byte) singboxDocument {
	t.Helper()
	var document singboxDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode sing-box export: %v", err)
	}
	return document
}

// objects returns every outbound and endpoint object of the document.
func (d singboxDocument) objects() []map[string]any {
	return append(append([]map[string]any{}, d.Outbounds...), d.Endpoints...)
}

// TestExportSingboxTagsAreUniqueAcrossNodesAndDeps pins the collision: a node
// name that repeats a chain dependency tag used to write the same tag into the
// document twice, and sing-box refuses the *whole* configuration on a duplicate
// outbound/endpoint tag, so one colliding node made the entire export unusable.
func TestExportSingboxTagsAreUniqueAcrossNodesAndDeps(t *testing.T) {
	body, _, report, err := Export(singboxCollisionItems(t), FormatSingbox, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if report.Exported != 2 || len(report.Skipped) != 0 {
		t.Fatalf("report = %+v", report)
	}

	document := decodeSingboxDocument(t, body)
	seen := make(map[string]bool, len(document.Outbounds)+len(document.Endpoints))
	for _, object := range document.objects() {
		tag, _ := object["tag"].(string)
		if tag == "" {
			t.Fatalf("object without a tag: %v", object)
		}
		if seen[tag] {
			t.Fatalf("duplicate tag %q in the exported sing-box document:\n%s", tag, body)
		}
		seen[tag] = true
	}

	// Every group member has to name a tag that really exists, otherwise the
	// selector is as unusable as a duplicate tag is.
	for _, group := range document.Outbounds {
		groupType, _ := group["type"].(string)
		if groupType != "selector" && groupType != "urltest" {
			continue
		}
		members, _ := group["outbounds"].([]any)
		for _, member := range members {
			name, _ := member.(string)
			if !seen[name] {
				t.Fatalf("%s %v references %q, which is no tag of the document: %s", groupType, group["tag"], name, body)
			}
		}
	}
}

// TestExportSingboxChainReferencesSurviveTagDedup pins the reference half of the
// rename: when a dependency tag has to be renamed, every reference to
// it has to follow the rename. A reference left on the canonical "<name>/d0"
// spelling names either nothing or — worse — an unrelated node that happens to
// carry that tag.
func TestExportSingboxChainReferencesSurviveTagDedup(t *testing.T) {
	items := singboxCollisionItems(t)
	body, _, _, err := Export(items, FormatSingbox, Options{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	document := decodeSingboxDocument(t, body)

	objects := make(map[string]map[string]any, len(document.Outbounds)+len(document.Endpoints))
	for _, object := range document.objects() {
		tag, _ := object["tag"].(string)
		if _, duplicate := objects[tag]; duplicate {
			t.Fatalf("the document carries the tag %q twice, so the reference below cannot be judged: %s", tag, body)
		}
		objects[tag] = object
	}

	chain, ok := objects["x"]
	if !ok {
		t.Fatalf("the chain node lost its own tag: %s", body)
	}
	detour, _ := chain["detour"].(string)
	if detour == "" {
		t.Fatalf("the chain main object has no detour: %v", chain)
	}
	target, ok := objects[detour]
	if !ok {
		t.Fatalf("the chain detour %q names no tag of the document: %s", detour, body)
	}
	if target["type"] != "shadowtls" {
		t.Fatalf("the chain detour %q points at a %v object instead of the chain dependency: %s", detour, target["type"], body)
	}
	if detour == "x/d0" {
		t.Fatalf("the chain detour still carries the canonical dependency tag, which the collision renamed: %s", body)
	}

	// The subscription parser re-derives the chain from the reference graph
	// alone, so the hop it materialises is the end-to-end proof that the
	// reference resolved to the dependency and not to the look-alike node.
	result, err := subscription.ParseWithReport(body)
	if err != nil {
		t.Fatalf("parse the exported sing-box document: %v", err)
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
	if depType := chainDependencyType(t, result.Nodes); depType != "shadowtls" {
		t.Fatalf("the round trip materialised a %q dependency, want %q (nodes: %+v)", depType, "shadowtls", result.Nodes)
	}
}

// chainDependencyType returns the type of the first dependency of the chain node
// of a parsed node list, or "" when the list holds no chain node.
func chainDependencyType(t *testing.T, nodes []subscription.ParsedNode) string {
	t.Helper()
	for _, parsed := range nodes {
		var envelope struct {
			Kind string `json:"kind"`
			Deps []struct {
				Type string `json:"type"`
			} `json:"deps"`
		}
		if err := json.Unmarshal(parsed.RawOptions, &envelope); err != nil {
			continue
		}
		if envelope.Kind == node.KindChain && len(envelope.Deps) > 0 {
			return envelope.Deps[0].Type
		}
	}
	return ""
}

// TestClaimTagStaysUniqueWhenTheVariantBudgetIsExhausted pins the last resort of
// the deduplication: even when every " #k" variant is taken, claimTag may not
// hand back a tag that is already in the document — a duplicate tag is what
// makes sing-box refuse the whole configuration.
func TestClaimTagStaysUniqueWhenTheVariantBudgetIsExhausted(t *testing.T) {
	taken := map[string]struct{}{"x": {}}
	for k := 2; k <= maxNameVariants; k++ {
		taken["x #"+strconv.Itoa(k)] = struct{}{}
	}
	reserved := make(map[string]struct{}, len(taken))
	for tag := range taken {
		reserved[tag] = struct{}{}
	}
	tag := claimTag("x", taken)
	if tag == "x" {
		t.Fatalf("claimTag returned the colliding candidate %q", tag)
	}
	if _, duplicate := reserved[tag]; duplicate {
		t.Fatalf("claimTag returned %q, which the document already carries", tag)
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
