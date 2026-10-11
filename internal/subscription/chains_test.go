package subscription

// Tests for the detour chain conversion of chains.go:
//   - a Clash shadow-tls plugin keeps the version the source declared;
//   - a dangling detour reference is stripped and reported.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"prism/internal/node"
)

// clashShadowTLSInput builds the Clash ss + shadow-tls plugin proxy and the
// shadowsocks outbound convertClashProxyPayload hands to clashShadowTLSChain.
func clashShadowTLSInput(pluginOpts map[string]any) (map[string]any, map[string]any) {
	const (
		tag    = "ss-stls"
		server = "198.51.100.7"
	)
	proxy := map[string]any{
		"name":        tag,
		"type":        "ss",
		"server":      server,
		"port":        443,
		"cipher":      "aes-256-gcm",
		"password":    "ss-pw",
		"plugin":      "shadow-tls",
		"plugin-opts": pluginOpts,
	}
	outbound := map[string]any{
		"type":        "shadowsocks",
		"tag":         tag,
		"server":      server,
		"server_port": uint64(443),
		"method":      "aes-256-gcm",
		"password":    "ss-pw",
	}
	return proxy, outbound
}

// clashShadowTLSDep runs the Clash shadow-tls conversion and returns the single
// chain dependency it produced.
func clashShadowTLSDep(t *testing.T, pluginOpts map[string]any) map[string]any {
	t.Helper()
	proxy, outbound := clashShadowTLSInput(pluginOpts)
	raw, ok := clashShadowTLSChain("ss-stls", "198.51.100.7", 443, proxy, outbound)
	if !ok {
		t.Fatalf("clashShadowTLSChain(plugin-opts=%v): refused, want a chain", pluginOpts)
	}
	envelope := parseNodeRaw(t, raw)
	deps := mustSliceField(t, envelope, "deps")
	if len(deps) != 1 {
		t.Fatalf("chain deps: got %d, want 1", len(deps))
	}
	dep, ok := deps[0].(map[string]any)
	if !ok {
		t.Fatalf("chain dep: got %T, want an object", deps[0])
	}
	return dep
}

// TestClashShadowTLSChainPassesVersionThrough pins: the version a Clash
// shadow-tls plugin declares is the sing-box shadowtls version. The converter
// used to keep only "is it 1?", so a v2 hop was rewritten to v3 — a hop that
// imports and then never completes a handshake.
func TestClashShadowTLSChainPassesVersionThrough(t *testing.T) {
	cases := []struct {
		name         string
		version      any
		wantVersion  float64
		wantPassword bool
	}{
		{name: "v1 carries neither a version nor a password", version: "1"},
		{name: "v1 as a number", version: 1},
		{name: "v2 stays v2", version: "2", wantVersion: 2, wantPassword: true},
		{name: "v2 as a number", version: 2, wantVersion: 2, wantPassword: true},
		{name: "v3 stays v3", version: "3", wantVersion: 3, wantPassword: true},
		{name: "a missing version keeps the default", version: nil, wantVersion: 3, wantPassword: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := map[string]any{"host": "front.example.com", "password": "stls-pw"}
			if tc.version != nil {
				opts["version"] = tc.version
			}
			dep := clashShadowTLSDep(t, opts)
			version, present := dep["version"]
			if tc.wantVersion == 0 {
				if present {
					t.Fatalf("v1 dep carries version %v, want none", version)
				}
				if password, present := dep["password"]; present {
					t.Fatalf("v1 dep carries password %v, want none", password)
				}
				return
			}
			if !present || version != tc.wantVersion {
				t.Fatalf("dep version = %v (present=%v), want %v", version, present, tc.wantVersion)
			}
			if password, present := dep["password"]; !present || password != "stls-pw" {
				t.Fatalf("dep password = %v (present=%v), want stls-pw", password, present)
			}
		})
	}
}

// TestClashShadowTLSChainRefusesUnmappableVersion pins the other half of:
// a version sing-box cannot express is refused instead of being guessed at.
func TestClashShadowTLSChainRefusesUnmappableVersion(t *testing.T) {
	for _, version := range []any{"4", "abc", "1.5", 0, -1} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			proxy, outbound := clashShadowTLSInput(map[string]any{
				"host": "front.example.com", "password": "stls-pw", "version": version,
			})
			if _, ok := clashShadowTLSChain("ss-stls", "198.51.100.7", 443, proxy, outbound); ok {
				t.Fatalf("shadow-tls version %v was accepted, want a refusal", version)
			}
		})
	}
}

// TestClashShadowTLSChainRequiresPasswordAndHost pins that v2/v3 without a
// password and a hop without a camouflage host are refused: sing-box would
// build a hop that can never handshake.
func TestClashShadowTLSChainRequiresPasswordAndHost(t *testing.T) {
	cases := []struct {
		name string
		opts map[string]any
	}{
		{name: "v2 without a password", opts: map[string]any{"host": "front.example.com", "version": "2"}},
		{name: "v3 without a password", opts: map[string]any{"host": "front.example.com", "version": "3"}},
		{name: "no version without a password", opts: map[string]any{"host": "front.example.com"}},
		{name: "without a host", opts: map[string]any{"password": "stls-pw", "version": "2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxy, outbound := clashShadowTLSInput(tc.opts)
			if _, ok := clashShadowTLSChain("ss-stls", "198.51.100.7", 443, proxy, outbound); ok {
				t.Fatalf("plugin-opts %v was accepted, want a refusal", tc.opts)
			}
		})
	}
}

// TestParseWithReport_ClashShadowTLSKeepsVersionTwo drives the whole Clash path
// for the audit case: `version: 2` must reach the chain dependency as 2.
func TestParseWithReport_ClashShadowTLSKeepsVersionTwo(t *testing.T) {
	body := []byte(`{"proxies":[{"name":"ss-v2","type":"ss","server":"198.51.100.7","port":443,` +
		`"cipher":"aes-256-gcm","password":"ss-pw","plugin":"shadow-tls",` +
		`"plugin-opts":{"host":"front.example.com","password":"stls-pw","version":2}}]}`)
	result, err := ParseWithReport(body)
	if err != nil {
		t.Fatalf("ParseWithReport: %v", err)
	}
	if len(result.Nodes) != 1 || len(result.Skipped) != 0 {
		t.Fatalf("nodes=%d skipped=%+v, want one imported chain", len(result.Nodes), result.Skipped)
	}
	doc, err := node.ParseNodeDoc(result.Nodes[0].RawOptions)
	if err != nil {
		t.Fatalf("ParseNodeDoc: %v", err)
	}
	if !doc.Chain || len(doc.Deps) != 1 {
		t.Fatalf("chain=%v deps=%d, want a one hop chain", doc.Chain, len(doc.Deps))
	}
	dep := parseNodeRaw(t, doc.Deps[0])
	if dep["version"] != float64(2) {
		t.Fatalf("hop version = %v, want 2 (a v2 hop must not be promoted to v3)", dep["version"])
	}
	if dep["password"] != "stls-pw" {
		t.Fatalf("hop password = %v, want stls-pw", dep["password"])
	}
}

// TestParseWithReport_DanglingDetourIsStrippedAndReported pins for the
// sing-box path: an unresolvable reference is reported instead of being
// guessed, and the detour is stripped so the object stays dialable (it would
// otherwise fail every dial with "outbound detour not found").
func TestParseWithReport_DanglingDetourIsStrippedAndReported(t *testing.T) {
	body := []byte(`{"outbounds":[
		{"type":"shadowsocks","tag":"ss-dangling","server":"1.1.1.1","server_port":443,"method":"aes-256-gcm","password":"pw","detour":"nope"},
		{"type":"vmess","tag":"vm-d0","server":"2.2.2.2","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555","detour":"d0"}
	]}`)
	result, err := ParseWithReport(body)
	if err != nil {
		t.Fatalf("ParseWithReport: %v", err)
	}
	if len(result.Nodes) != 2 {
		t.Fatalf("imported nodes: got %d (skipped %+v), want both nodes kept", len(result.Nodes), result.Skipped)
	}
	byTag := parseNodesByTag(t, result.Nodes)
	for _, tag := range []string{"ss-dangling", "vm-d0"} {
		object, ok := byTag[tag]
		if !ok {
			t.Fatalf("node %q was dropped", tag)
		}
		if detour, present := object["detour"]; present {
			t.Fatalf("node %q kept the dangling detour %v, want it stripped", tag, detour)
		}
		if _, err := node.ParseNodeDoc(result.Nodes[0].RawOptions); err != nil {
			t.Fatalf("stripped node does not parse: %v", err)
		}
	}
	// The report names the unresolvable reference as a note: the node itself is
	// kept, so it must not be counted as a drop (one node cannot be both
	// imported and skipped).
	if len(result.Skipped) != 0 {
		t.Fatalf("skip records: got %+v, want none - both nodes were kept", result.Skipped)
	}
	if len(result.Notes) != 2 {
		t.Fatalf("notes: got %+v, want one per dangling reference", result.Notes)
	}
	if result.Stats.Notes != 2 || result.Stats.Skipped != 0 || result.Stats.Total != 2 {
		t.Errorf("stats = %+v, want notes=2 skipped=0 total=2", result.Stats)
	}
	for _, skipped := range result.Notes {
		if skipped.Reason != node.ReasonInvalid {
			t.Errorf("skip reason = %q, want %q", skipped.Reason, node.ReasonInvalid)
		}
		if skipped.Source != SourceSingbox {
			t.Errorf("skip source = %q, want %q", skipped.Source, SourceSingbox)
		}
		if !strings.Contains(skipped.Detail, "dangling detour") {
			t.Errorf("skip detail = %q, want it to name the dangling reference", skipped.Detail)
		}
	}
}

// TestParseWithReport_ClashDanglingDialerProxyIsStripped pins for the Clash
// path: a dialer-proxy that names no proxy of the document is stripped, the
// node survives with its other dial fields.
func TestParseWithReport_ClashDanglingDialerProxyIsStripped(t *testing.T) {
	body := []byte(`{"proxies":[
		{"name":"vm-dangling","type":"vmess","server":"1.1.1.1","port":443,"uuid":"11111111-2222-3333-4444-555555555555","dialer-proxy":"nope","bind-interface":"eth1"},
		{"name":"hy2-dangling","type":"hysteria2","server":"2.2.2.2","port":443,"password":"pw","sni":"hy2.example.com","dialer-proxy":"chain-b"}
	]}`)
	result, err := ParseWithReport(body)
	if err != nil {
		t.Fatalf("ParseWithReport: %v", err)
	}
	if len(result.Nodes) != 2 {
		t.Fatalf("imported nodes: got %d (skipped %+v), want both nodes kept", len(result.Nodes), result.Skipped)
	}
	byTag := parseNodesByTag(t, result.Nodes)
	vmess, ok := byTag["vm-dangling"]
	if !ok {
		t.Fatal("the vmess node was dropped")
	}
	if detour, present := vmess["detour"]; present {
		t.Fatalf("vmess kept the dangling detour %v, want it stripped", detour)
	}
	if vmess["bind_interface"] != "eth1" {
		t.Fatalf("vmess bind_interface = %v, want eth1 (only the detour is stripped)", vmess["bind_interface"])
	}
	hy2, ok := byTag["hy2-dangling"]
	if !ok {
		t.Fatal("the hysteria2 node was dropped")
	}
	if detour, present := hy2["detour"]; present {
		t.Fatalf("hysteria2 kept the dangling detour %v, want it stripped", detour)
	}
}

// TestParseWithReport_ResolvableDetourStillBuildsChain is the regression guard:
// a reference that resolves inside the document still becomes a chain node.
func TestParseWithReport_ResolvableDetourStillBuildsChain(t *testing.T) {
	body := []byte(`{"outbounds":[
		{"type":"shadowsocks","tag":"ss-chain","server":"127.0.0.1","server_port":1,"method":"aes-256-gcm","password":"pw","detour":"stls"},
		{"type":"shadowtls","tag":"stls","server":"127.0.0.1","server_port":1,"version":3,"password":"stls-pw","tls":{"enabled":true,"server_name":"example.com"}}
	]}`)
	result, err := ParseWithReport(body)
	if err != nil {
		t.Fatalf("ParseWithReport: %v", err)
	}
	if len(result.Nodes) != 1 || len(result.Skipped) != 0 {
		t.Fatalf("nodes=%d skipped=%+v, want one chain node and no report", len(result.Nodes), result.Skipped)
	}
	doc, err := node.ParseNodeDoc(result.Nodes[0].RawOptions)
	if err != nil {
		t.Fatalf("ParseNodeDoc: %v", err)
	}
	if !doc.Chain || len(doc.Deps) != 1 || doc.Deps[0] == nil {
		t.Fatalf("chain=%v deps=%d, want the shadowtls hop materialised", doc.Chain, len(doc.Deps))
	}
	if got := objectType(doc.Deps[0]); got != "shadowtls" {
		t.Fatalf("hop type = %q, want shadowtls", got)
	}
}

// TestParseWithReport_ResolvableDialerProxyStillBuildsChain is the same guard
// for the Clash dialer-proxy path.
func TestParseWithReport_ResolvableDialerProxyStillBuildsChain(t *testing.T) {
	body := []byte(`{"proxies":[
		{"name":"front","type":"socks5","server":"127.0.0.1","port":1},
		{"name":"back","type":"vmess","server":"127.0.0.1","port":1,"uuid":"11111111-2222-3333-4444-555555555555","dialer-proxy":"front"}
	]}`)
	result, err := ParseWithReport(body)
	if err != nil {
		t.Fatalf("ParseWithReport: %v", err)
	}
	if len(result.Nodes) != 2 || len(result.Skipped) != 0 {
		t.Fatalf("nodes=%d skipped=%+v, want both nodes imported", len(result.Nodes), result.Skipped)
	}
	// A chain envelope carries "name", not "tag", so look the node up by the
	// tag the parser assigned instead of by a JSON field.
	var backRaw json.RawMessage
	for _, parsed := range result.Nodes {
		if parsed.Tag == "back" {
			backRaw = parsed.RawOptions
		}
	}
	if backRaw == nil {
		t.Fatal("the dialing node was dropped")
	}
	back := parseNodeRaw(t, backRaw)
	deps := mustSliceField(t, back, "deps")
	if len(deps) != 1 {
		t.Fatalf("chain deps: got %d, want the front hop", len(deps))
	}
	main := mustMapField(t, back, "main")
	if detour, _ := main["detour"].(string); detour != node.DepTag(0) {
		t.Fatalf("main detour = %q, want %q", detour, node.DepTag(0))
	}
}

// TestParseWithReport_DetourCycleIsStillRefused keeps the cycle rule intact
// while the dangling rule changes.
func TestParseWithReport_DetourCycleIsStillRefused(t *testing.T) {
	body := []byte(`{"outbounds":[
		{"type":"shadowsocks","tag":"a","server":"1.1.1.1","server_port":443,"method":"aes-256-gcm","password":"pw","detour":"b"},
		{"type":"shadowsocks","tag":"b","server":"2.2.2.2","server_port":443,"method":"aes-256-gcm","password":"pw","detour":"a"}
	]}`)
	result, err := ParseWithReport(body)
	if err != nil {
		t.Fatalf("ParseWithReport: %v", err)
	}
	if len(result.Nodes) != 0 {
		t.Fatalf("imported nodes: got %d, want a cycle to refuse both", len(result.Nodes))
	}
	if len(result.Skipped) != 2 {
		t.Fatalf("skip records: got %+v, want two", result.Skipped)
	}
	for _, skipped := range result.Skipped {
		if skipped.Reason != node.ReasonInvalid || skipped.Detail != "detour cycle" {
			t.Errorf("skip record = %+v, want INVALID detour cycle", skipped)
		}
	}
}

// TestParseWithReport_UnmappableShadowTLSVersionIsRefused is the regression
// guard for the shadow-tls refusal path.
//
// setSSPluginFromClash writes plugin=shadow-tls into the shadowsocks outbound
// before the chain is attempted, so a version sing-box cannot express (it
// supports 1, 2 and 3) used to fall through to buildParsedNode and produce a
// bare ss node carrying plugin: shadow-tls. sing-box rejects that node at dial
// time with "plugin not found: shadow-tls", so it imported but could never
// connect - worse than the silent v3 promotion it replaced.
func TestParseWithReport_UnmappableShadowTLSVersionIsRefused(t *testing.T) {
	body := []byte(`{"proxies":[
		{"name":"st-bad","type":"ss","server":"1.1.1.1","port":443,"cipher":"aes-256-gcm","password":"pw","plugin":"shadow-tls","plugin-opts":{"version":"4","password":"p","host":"h"}}
	]}`)
	result, err := ParseWithReport(body)
	if err != nil {
		t.Fatalf("ParseWithReport: %v", err)
	}
	if len(result.Nodes) != 0 {
		t.Fatalf("imported nodes: got %d (%s), want the node refused",
			len(result.Nodes), string(result.Nodes[0].RawOptions))
	}
	if len(result.Skipped) != 1 {
		t.Fatalf("skip records: got %+v, want exactly one explaining the refusal", result.Skipped)
	}
	skipped := result.Skipped[0]
	if skipped.Reason != node.ReasonInvalid {
		t.Errorf("skip reason = %q, want %q", skipped.Reason, node.ReasonInvalid)
	}
	if !strings.Contains(skipped.Detail, "shadow-tls version") {
		t.Errorf("skip detail = %q, want it to name the unsupported version", skipped.Detail)
	}
}

// TestParseWithReport_MidChainDanglingDetourIsReported covers the case the
// whole-reference rule misses: A -> B -> missing. The resolvable hops are still
// built into a chain (the alternative is a node whose declared hop vanished),
// but the walk stopped early, so the cut has to be reported - otherwise the
// operator sees a chain that silently lost its tail.
func TestParseWithReport_MidChainDanglingDetourIsReported(t *testing.T) {
	body := []byte(`{"outbounds":[
		{"type":"shadowsocks","tag":"head","server":"1.1.1.1","server_port":443,"method":"aes-256-gcm","password":"pw","detour":"mid"},
		{"type":"shadowtls","tag":"mid","server":"2.2.2.2","server_port":443,"version":3,"password":"stls-pw","detour":"missing","tls":{"enabled":true,"server_name":"example.com"}}
	]}`)
	result, err := ParseWithReport(body)
	if err != nil {
		t.Fatalf("ParseWithReport: %v", err)
	}
	// A chain envelope carries "name", not "tag", so look the node up by the
	// tag the parser assigned instead of by a JSON field.
	var head json.RawMessage
	for _, parsed := range result.Nodes {
		if parsed.Tag == "head" {
			head = parsed.RawOptions
		}
	}
	if head == nil {
		t.Fatalf("the chain head was dropped (nodes: %d, skipped: %+v)", len(result.Nodes), result.Skipped)
	}
	deps := mustSliceField(t, parseNodeRaw(t, head), "deps")
	if len(deps) != 1 {
		t.Fatalf("chain deps: got %d, want the resolvable mid hop kept", len(deps))
	}
	// The last hop of a cut chain ends without a detour: it dials directly
	// rather than failing every dial with "outbound detour not found".
	lastHop, ok := deps[len(deps)-1].(map[string]any)
	if !ok {
		t.Fatalf("chain dep: got %T, want an object", deps[len(deps)-1])
	}
	if detour, present := lastHop["detour"]; present {
		t.Fatalf("the last hop kept a detour %v, want it cleared", detour)
	}
	// The cut is a note, never a drop: the node is imported.
	if len(result.Skipped) != 0 {
		t.Fatalf("skip records: got %+v, want none - the node was kept", result.Skipped)
	}
	if len(result.Notes) != 1 {
		t.Fatalf("notes: got %+v, want one for the cut chain", result.Notes)
	}
	note := result.Notes[0]
	if note.Source != SourceSingbox {
		t.Errorf("note source = %q, want %q", note.Source, SourceSingbox)
	}
	if !strings.Contains(note.Detail, "missing") {
		t.Errorf("note detail = %q, want it to name the unresolvable reference", note.Detail)
	}
	if !strings.Contains(note.Detail, "cut") {
		t.Errorf("note detail = %q, want it to say the chain was cut rather than stripped", note.Detail)
	}
	if result.Stats.Notes != 1 || result.Stats.Skipped != 0 || result.Stats.Total != 1 {
		t.Errorf("stats = %+v, want notes=1 skipped=0 total=1", result.Stats)
	}
}

// TestParseWithReport_MidChainDanglingDialerProxyIsReported is the same rule on
// the Clash path: dialer-proxy chains are built from the resolvable prefix and
// the cut is reported as a note.
func TestParseWithReport_MidChainDanglingDialerProxyIsReported(t *testing.T) {
	body := []byte(`{"proxies":[
		{"name":"front","type":"socks5","server":"127.0.0.1","port":1},
		{"name":"back","type":"vmess","server":"127.0.0.1","port":1,"uuid":"11111111-2222-3333-4444-555555555555","dialer-proxy":"front"},
		{"name":"front2","type":"socks5","server":"127.0.0.1","port":1,"dialer-proxy":"missing"}
	]}`)
	result, err := ParseWithReport(body)
	if err != nil {
		t.Fatalf("ParseWithReport: %v", err)
	}
	if len(result.Skipped) != 0 {
		t.Fatalf("skip records: got %+v, want none - every node was kept", result.Skipped)
	}
	if len(result.Notes) != 1 {
		t.Fatalf("notes: got %+v, want one for the cut chain", result.Notes)
	}
	note := result.Notes[0]
	if note.Source != SourceClash {
		t.Errorf("note source = %q, want %q", note.Source, SourceClash)
	}
	if !strings.Contains(note.Detail, "missing") {
		t.Errorf("note detail = %q, want it to name the unresolvable reference", note.Detail)
	}
	if result.Stats.Notes != 1 {
		t.Errorf("stats = %+v, want notes=1", result.Stats)
	}
}

// TestParseWithReport_NoteOverflowIsCounted pins the bound on the notes channel:
// past MaxSkippedNodes the records are counted only, so the stored report cannot
// grow without bound and the count still reports how many notes exist.
func TestParseWithReport_NoteOverflowIsCounted(t *testing.T) {
	report := newParseReport()
	total := MaxSkippedNodes + 7
	for i := 0; i < total; i++ {
		report.addNote(SkippedNode{
			Name:   fmt.Sprintf("node-%04d", i),
			Type:   "shadowsocks",
			Source: SourceSingbox,
			Reason: node.ReasonInvalid,
			Detail: "dangling detour \"x\" was stripped",
		})
	}
	result := report.result()
	if len(result.Notes) != MaxSkippedNodes {
		t.Fatalf("stored notes: got %d, want the bound %d", len(result.Notes), MaxSkippedNodes)
	}
	if result.Stats.Notes != total {
		t.Errorf("stats.notes: got %d, want the true total %d", result.Stats.Notes, total)
	}
	if result.Stats.NotesOverflow != 7 {
		t.Errorf("stats.notes_overflow: got %d, want 7", result.Stats.NotesOverflow)
	}
	// A note is never a drop: the bound must not move anything into Skipped.
	if result.Stats.Skipped != 0 || len(result.Skipped) != 0 {
		t.Errorf("skipped = %d/%+v, want none", result.Stats.Skipped, result.Skipped)
	}
	if summary := SummarizeParseResult(result); summary.Notes != total || summary.NotesOverflow != 7 {
		t.Errorf("summary = %+v, want notes=%d notes_overflow=7", summary, total)
	}
}
