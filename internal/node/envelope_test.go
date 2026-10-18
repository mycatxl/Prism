package node

// The node document parser is the boundary between untrusted subscription
// content and the engine: every form a customer can submit is decoded here, and
// every refusal has to happen here. These tests pin the two shapes (a plain
// sing-box object and the prism_node envelope), the refusals that keep a
// document out of the engine, and the local-file rule that keeps a document
// from making Prism read a file off the host.

import (
	"encoding/json"
	"strings"
	"testing"
)

const inlineCACertificate = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

func TestIsEnvelope(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"envelope marker", `{"prism_node":1,"engine":"singbox"}`, true},
		{"plain outbound", `{"type":"socks","server":"1.2.3.4"}`, false},
		{"marker only", `{"prism_node":1}`, true},
		{"json array", `[1,2,3]`, false},
		{"json null", `null`, false},
		{"not json", `not json at all`, false},
		{"empty", ``, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := IsEnvelope([]byte(testCase.raw)); got != testCase.want {
				t.Errorf("IsEnvelope(%s) = %v, want %v", testCase.raw, got, testCase.want)
			}
		})
	}
}

// TestParseEnvelope_ReportsNotAnEnvelope pins the ok flag: a caller has to be
// able to tell "this is a plain object" from "this envelope is broken" without
// looking at the error.
func TestParseEnvelope_ReportsNotAnEnvelope(t *testing.T) {
	envelope, ok, err := ParseEnvelope([]byte(`{"type":"socks"}`))
	if ok || err != nil {
		t.Fatalf("ParseEnvelope(plain object) = (%v, %v, %v), want ok=false and no error", envelope, ok, err)
	}
}

func TestParseEnvelope_Rejections(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		detail string
	}{
		{"unsupported version", `{"prism_node":2,"engine":"singbox"}`, "unsupported prism_node version"},
		{"version zero", `{"prism_node":0,"engine":"singbox"}`, "unsupported prism_node version"},
		{"unknown engine", `{"prism_node":1,"engine":"xray"}`, "unknown engine"},
		{"missing engine", `{"prism_node":1}`, "unknown engine"},
		{"marker is not a number", `{"prism_node":"1","engine":"singbox"}`, "invalid node envelope"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, ok, err := ParseEnvelope([]byte(testCase.raw))
			if !ok {
				t.Fatal("ok = false, want true: the object carries a prism_node marker")
			}
			if err == nil {
				t.Fatal("err = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), testCase.detail) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), testCase.detail)
			}
		})
	}
}

func TestParseNodeDoc_FormA(t *testing.T) {
	raw := []byte(`{"type":"socks","server":"1.2.3.4","server_port":1080}`)
	doc, err := ParseNodeDoc(raw)
	if err != nil {
		t.Fatalf("ParseNodeDoc: unexpected error: %v", err)
	}
	if doc.Kind != DocOutbound {
		t.Errorf("Kind = %v, want %v", doc.Kind, DocOutbound)
	}
	if doc.Engine != EngineSingbox {
		t.Errorf("Engine = %q, want %q", doc.Engine, EngineSingbox)
	}
	if doc.Type != "socks" {
		t.Errorf("Type = %q, want %q", doc.Type, "socks")
	}
	if doc.Chain {
		t.Error("Chain = true, want false for a single outbound")
	}
	if string(doc.Main) != string(raw) {
		t.Errorf("Main = %s, want the input copied verbatim", doc.Main)
	}
	if doc.Hash != HashFromRawOptions(raw) {
		t.Error("Hash does not match the hash of the input document")
	}
}

// TestParseNodeDoc_FormAEndpointType pins that an endpoint type stays form A
// and is classified as an endpoint without being rewritten.
func TestParseNodeDoc_FormAEndpointType(t *testing.T) {
	raw := []byte(`{"type":"openvpn-client","server":"vpn.example.com"}`)
	doc, err := ParseNodeDoc(raw)
	if err != nil {
		t.Fatalf("ParseNodeDoc: unexpected error: %v", err)
	}
	if doc.Kind != DocEndpoint {
		t.Errorf("Kind = %v, want %v", doc.Kind, DocEndpoint)
	}
	if doc.Type != "openvpn-client" {
		t.Errorf("Type = %q, want %q", doc.Type, "openvpn-client")
	}
}

// TestParseNodeDoc_FormALegacyWireGuard pins the one form A rewrite: a legacy
// wireguard outbound becomes an endpoint, while its hash keeps describing the
// document as submitted so a subscription does not churn on upgrade.
func TestParseNodeDoc_FormALegacyWireGuard(t *testing.T) {
	raw := []byte(`{"type":"wireguard","server":"1.2.3.4","server_port":51820,` +
		`"private_key":"aGVsbG8=","peer_public_key":"d29ybGQ=","local_address":["10.8.0.2"]}`)
	doc, err := ParseNodeDoc(raw)
	if err != nil {
		t.Fatalf("ParseNodeDoc: unexpected error: %v", err)
	}
	if doc.Kind != DocEndpoint {
		t.Errorf("Kind = %v, want %v", doc.Kind, DocEndpoint)
	}
	if doc.Type != "wireguard" {
		t.Errorf("Type = %q, want %q", doc.Type, "wireguard")
	}
	if doc.Hash != HashFromRawOptions(raw) {
		t.Error("Hash changed, want the hash of the document as submitted")
	}
	var main map[string]any
	if err := json.Unmarshal(doc.Main, &main); err != nil {
		t.Fatalf("Main is not an object: %v", err)
	}
	if main["type"] != "wireguard" {
		t.Errorf("Main type = %v, want %q", main["type"], "wireguard")
	}
}

func TestParseNodeDoc_Rejections(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		detail string
	}{
		{"empty document", ``, "empty node document"},
		{"missing outbound type", `{"server":"1.2.3.4"}`, "missing outbound type"},
		{"not json", `not json`, "parse node document"},
		{"unknown engine", `{"prism_node":1,"engine":"xray","kind":"endpoint","main":{"type":"wireguard"}}`, "unknown engine"},
		{"envelope without main or proxy", `{"prism_node":1,"engine":"singbox","kind":"endpoint"}`, "no main or proxy object"},
		{"unknown kind", `{"prism_node":1,"engine":"singbox","kind":"widget","main":{"type":"socks"}}`, "unknown envelope kind"},
		{"singbox proxy kind", `{"prism_node":1,"engine":"singbox","kind":"proxy","proxy":{"type":"ss"}}`, "cannot carry kind"},
		{"mihomo non-proxy kind", `{"prism_node":1,"engine":"mihomo","kind":"endpoint","main":{"type":"ss"}}`, "must use kind"},
		{"endpoint with a non-endpoint type", `{"prism_node":1,"engine":"singbox","kind":"endpoint","main":{"type":"socks"}}`, "is not an endpoint type"},
		{"endpoint main without a type", `{"prism_node":1,"engine":"singbox","kind":"endpoint","main":{"server":"1.2.3.4"}}`, "is not an endpoint type"},
		{"chain without deps", `{"prism_node":1,"engine":"singbox","kind":"chain","main":{"type":"socks"}}`, "chain node has no deps"},
		{"chain main without a type", `{"prism_node":1,"engine":"singbox","kind":"chain","main":{"server":"1.2.3.4"},"deps":[{"type":"socks"}]}`, "chain main object has no type"},
		{"mihomo proxy without a type", `{"prism_node":1,"engine":"mihomo","kind":"proxy","proxy":{"cipher":"aes-128-gcm"}}`, "mihomo proxy has no type"},
		{"mihomo proxy is not an object", `{"prism_node":1,"engine":"mihomo","kind":"proxy","proxy":[]}`, "parse mihomo proxy"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ParseNodeDoc([]byte(testCase.raw))
			if err == nil {
				t.Fatal("ParseNodeDoc: expected a refusal, got none")
			}
			if !strings.Contains(err.Error(), testCase.detail) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), testCase.detail)
			}
		})
	}
}

// TestParseNodeDoc_ChainDepthBound pins MaxChainDeps: the chain depth is a
// resource bound, so a document that asks for more is refused rather than
// accepted and trimmed.
func TestParseNodeDoc_ChainDepthBound(t *testing.T) {
	dep := `{"type":"socks","server":"1.2.3.4"}`
	build := func(count int) string {
		deps := make([]string, 0, count)
		for i := 0; i < count; i++ {
			deps = append(deps, dep)
		}
		return `{"prism_node":1,"engine":"singbox","kind":"chain","main":{"type":"socks"},` +
			`"deps":[` + strings.Join(deps, ",") + `]}`
	}
	doc, err := ParseNodeDoc([]byte(build(MaxChainDeps)))
	if err != nil {
		t.Fatalf("ParseNodeDoc(%d deps): unexpected error: %v", MaxChainDeps, err)
	}
	if !doc.Chain || len(doc.Deps) != MaxChainDeps {
		t.Errorf("Chain = %v with %d deps, want true with %d", doc.Chain, len(doc.Deps), MaxChainDeps)
	}
	_, err = ParseNodeDoc([]byte(build(MaxChainDeps + 1)))
	if err == nil {
		t.Fatal("ParseNodeDoc: expected a refusal for an over-deep chain")
	}
	if !strings.Contains(err.Error(), "chain depth") {
		t.Errorf("error = %q, want it to name the chain depth", err.Error())
	}
}

func TestParseNodeDoc_FormBMihomo(t *testing.T) {
	raw := []byte(`{"prism_node":1,"engine":"mihomo","kind":"proxy","name":"tokyo",` +
		`"proxy":{"type":"ss","server":"1.2.3.4","port":8388,"cipher":"aes-128-gcm"}}`)
	doc, err := ParseNodeDoc(raw)
	if err != nil {
		t.Fatalf("ParseNodeDoc: unexpected error: %v", err)
	}
	if doc.Kind != DocProxy {
		t.Errorf("Kind = %v, want %v", doc.Kind, DocProxy)
	}
	if doc.Engine != EngineMihomo {
		t.Errorf("Engine = %q, want %q", doc.Engine, EngineMihomo)
	}
	if doc.Name != "tokyo" {
		t.Errorf("Name = %q, want %q", doc.Name, "tokyo")
	}
	if doc.Type != "ss" {
		t.Errorf("Type = %q, want %q", doc.Type, "ss")
	}
}

// TestParseNodeDoc_RejectsLocalFileRefs is the security boundary this parser
// exists for: a document that names a file makes sing-box read it while it
// builds the outbound, and a failed parse puts that content into an error string
// the node APIs return. Every shape has to be refused.
func TestParseNodeDoc_RejectsLocalFileRefs(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		fieldPath string
	}{
		{
			"form A tls certificate path",
			`{"type":"trojan","server":"1.2.3.4","tls":{"certificate_path":"/etc/passwd"}}`,
			"tls.certificate_path",
		},
		{
			"form A nested array",
			`{"type":"vmess","tls":{"client_certificate_path":["/etc/shadow"]}}`,
			"tls.client_certificate_path",
		},
		{
			"envelope endpoint main",
			`{"prism_node":1,"engine":"singbox","kind":"endpoint",` +
				`"main":{"type":"wireguard","private_key_path":"/proc/self/environ"}}`,
			"main.private_key_path",
		},
		{
			"envelope chain dep",
			`{"prism_node":1,"engine":"singbox","kind":"chain","main":{"type":"socks"},` +
				`"deps":[{"type":"trojan","tls":{"certificate_path":"/dev/zero"}}]}`,
			"deps[0].tls.certificate_path",
		},
		{
			"key case and padding do not hide the option",
			`{"type":"trojan","tls":{"  CERTIFICATE_PATH  ":"/etc/passwd"}}`,
			// The detail echoes the field name with every character outside
			// [A-Za-z0-9_-] replaced, so the padding shows up as underscores.
			"tls.__CERTIFICATE_PATH__",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ParseNodeDoc([]byte(testCase.raw))
			if err == nil {
				t.Fatal("ParseNodeDoc: expected a refusal, got none")
			}
			reason, detail, ok := LocalFileRefReason(err)
			if !ok {
				t.Fatalf("LocalFileRefReason(%v) = ok false, want a local-file refusal", err)
			}
			if reason != ReasonUnsupportedFeature {
				t.Errorf("reason = %q, want %q", reason, ReasonUnsupportedFeature)
			}
			if !strings.HasPrefix(detail, testCase.fieldPath) {
				t.Errorf("detail = %q, want it to start with %q", detail, testCase.fieldPath)
			}
			// The detail names the field only: echoing the path back would
			// hand the submitter a probe for what exists on the host.
			if strings.Contains(detail, "/etc/") || strings.Contains(detail, "/proc/") || strings.Contains(detail, "/dev/") {
				t.Errorf("detail = %q, want it to leak no path", detail)
			}
		})
	}
}

// TestParseNodeDoc_AcceptsInlineMaterial pins the other side of that rule:
// material carried inline is what the parser asks for, so it must keep working.
func TestParseNodeDoc_AcceptsInlineMaterial(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{
			"inline certificate",
			`{"type":"trojan","tls":{"certificate":["` + strings.ReplaceAll(inlineCACertificate, "\n", `\n`) + `"]}}`,
		},
		{
			"inline certificate as a bare string",
			`{"type":"trojan","tls":{"certificate":"` + strings.ReplaceAll(inlineCACertificate, "\n", `\n`) + `"}}`,
		},
		{
			"empty path names nothing",
			`{"type":"trojan","tls":{"certificate_path":""}}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := ParseNodeDoc([]byte(testCase.raw)); err != nil {
				t.Errorf("ParseNodeDoc: unexpected error: %v", err)
			}
		})
	}
}

// TestRejectLocalFileRefs_ReportsTheFirstFieldInOrder pins determinism: a
// document with several references always reports the same one.
func TestRejectLocalFileRefs_ReportsTheFirstFieldInOrder(t *testing.T) {
	raw := []byte(`{"type":"trojan","tls":{"z_path":"/etc/passwd","a_path":"/etc/shadow"}}`)
	err := RejectLocalFileRefs(raw)
	if err == nil {
		t.Fatal("RejectLocalFileRefs: expected a refusal, got none")
	}
	_, detail, ok := LocalFileRefReason(err)
	if !ok {
		t.Fatalf("LocalFileRefReason(%v) = ok false, want a local-file refusal", err)
	}
	if !strings.HasPrefix(detail, "tls.a_path") {
		t.Errorf("detail = %q, want the first field in sorted order", detail)
	}
}

// TestRejectLocalFileRefs_LeavesOtherInputAlone pins what the scan does not
// claim: input that is not a JSON object is the document parser's problem, and
// the scan must not turn it into a local-file refusal.
func TestRejectLocalFileRefs_LeavesOtherInputAlone(t *testing.T) {
	cases := []string{``, `not json`, `[1,2,3]`, `"a string"`, `{"type":"socks"}`}
	for _, raw := range cases {
		if err := RejectLocalFileRefs([]byte(raw)); err != nil {
			t.Errorf("RejectLocalFileRefs(%q) = %v, want nil", raw, err)
		}
	}
}

func TestLocalFileRefReason_IgnoresOtherErrors(t *testing.T) {
	if _, _, ok := LocalFileRefReason(nil); ok {
		t.Error("LocalFileRefReason(nil) = ok true, want false")
	}
	if _, _, ok := LocalFileRefReason(jsonError()); ok {
		t.Error("LocalFileRefReason(other error) = ok true, want false")
	}
}

func TestLocalFileRefError_Error(t *testing.T) {
	err := &LocalFileRefError{Reason: ReasonUnsupportedFeature, Detail: "tls.certificate_path: x"}
	want := ReasonUnsupportedFeature + ": tls.certificate_path: x"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// TestRewriteDepDetours pins the chain namespace: a dependency tag is rewritten
// to sit under the chain's own base, so one chain cannot address another's
// nodes, and a value that is not a dependency tag is left alone.
func TestRewriteDepDetours(t *testing.T) {
	raw := json.RawMessage(`{` +
		`"type":"socks","server":"1.2.3.4","detour":"d0",` +
		`"outbound":{"detour":"d1"},` +
		`"list":[{"detour":"d2"},{"detour":"elsewhere"},{"detour":"d0x"},{"detour":5}],` +
		`"other":"d0"}`)
	out, err := RewriteDepDetours(raw, "base")
	if err != nil {
		t.Fatalf("RewriteDepDetours: unexpected error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("output is not an object: %v", err)
	}
	if decoded["detour"] != "base/d0" {
		t.Errorf("detour = %v, want %q", decoded["detour"], "base/d0")
	}
	if decoded["other"] != "d0" {
		t.Errorf("other = %v, want it untouched: only a detour key names a tag", decoded["other"])
	}
	outbound, ok := decoded["outbound"].(map[string]any)
	if !ok || outbound["detour"] != "base/d1" {
		t.Errorf("outbound = %#v, want detour %q", decoded["outbound"], "base/d1")
	}
	list, ok := decoded["list"].([]any)
	if !ok || len(list) != 4 {
		t.Fatalf("list = %#v, want four entries", decoded["list"])
	}
	entries := make([]any, 0, len(list))
	for _, entry := range list {
		object, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("list entry = %#v, want an object", entry)
		}
		entries = append(entries, object["detour"])
	}
	want := []any{"base/d2", "elsewhere", "d0x", float64(5)}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("list[%d].detour = %v, want %v", i, entries[i], want[i])
		}
	}
}

// TestRewriteDepDetours_DoesNotMutateTheInput pins that the caller's document is
// left alone: the same raw options are hashed elsewhere.
func TestRewriteDepDetours_DoesNotMutateTheInput(t *testing.T) {
	raw := json.RawMessage(`{"type":"socks","detour":"d0"}`)
	before := string(raw)
	if _, err := RewriteDepDetours(raw, "base"); err != nil {
		t.Fatalf("RewriteDepDetours: unexpected error: %v", err)
	}
	if string(raw) != before {
		t.Errorf("input = %s, want it unchanged", raw)
	}
}

func TestRewriteDepDetours_RejectsNonJSON(t *testing.T) {
	_, err := RewriteDepDetours(json.RawMessage(`not json`), "base")
	if err == nil {
		t.Fatal("RewriteDepDetours: expected a refusal, got none")
	}
	if !strings.Contains(err.Error(), "parse chain object") {
		t.Errorf("error = %q, want it to name the parse step", err.Error())
	}
}

func TestDepTag(t *testing.T) {
	for i, want := range []string{"d0", "d1", "d2", "d10"} {
		index := i
		if index == 3 {
			index = 10
		}
		if got := DepTag(index); got != want {
			t.Errorf("DepTag(%d) = %q, want %q", index, got, want)
		}
	}
}

func TestDocKindString(t *testing.T) {
	cases := map[DocKind]string{
		DocOutbound: "outbound",
		DocEndpoint: "endpoint",
		DocChain:    "chain",
		DocProxy:    "proxy",
		DocKind(99): "unknown",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Errorf("DocKind(%d).String() = %q, want %q", int(kind), got, want)
		}
	}
}

func TestObjectKind(t *testing.T) {
	for _, typeName := range []string{"wireguard", "openvpn-client", "openconnect", "tailscale"} {
		if got := objectKind(typeName); got != DocEndpoint {
			t.Errorf("objectKind(%q) = %v, want %v", typeName, got, DocEndpoint)
		}
	}
	for _, typeName := range []string{"socks", "trojan", "vmess", "openvpn-server"} {
		if got := objectKind(typeName); got != DocOutbound {
			t.Errorf("objectKind(%q) = %v, want %v", typeName, got, DocOutbound)
		}
	}
}

// TestEndpointTypes pins the difference between "classified as an endpoint" and
// "this build can create it": tailscale is in the first set only, so a node that
// asks for it is reported as ENGINE_NOT_BUILT instead of being refused at parse
// time.
func TestEndpointTypes(t *testing.T) {
	types := EndpointTypes()
	if len(types) != 3 {
		t.Fatalf("EndpointTypes() = %v, want three types", types)
	}
	for _, typeName := range types {
		if !IsEndpointType(typeName) {
			t.Errorf("IsEndpointType(%q) = false, want true for a reported type", typeName)
		}
	}
	if !IsEndpointType("tailscale") {
		t.Error("IsEndpointType(tailscale) = false, want true: it is classified but not built")
	}
	for _, typeName := range types {
		if typeName == "tailscale" {
			t.Error("EndpointTypes() lists tailscale, want it absent: this build has no with_tailscale tag")
		}
	}
	if IsEndpointType("openvpn-server") {
		t.Error("IsEndpointType(openvpn-server) = true, want false: it is not a node candidate")
	}
}

func TestTypeOfObject(t *testing.T) {
	typeName, err := TypeOfObject(json.RawMessage(`{"type":"socks","tag":"x"}`))
	if err != nil || typeName != "socks" {
		t.Errorf("TypeOfObject = (%q, %v), want (%q, nil)", typeName, err, "socks")
	}
	if _, err := TypeOfObject(json.RawMessage(`{"tag":"x"}`)); err == nil {
		t.Error("TypeOfObject without a type: expected a refusal, got none")
	} else if !strings.Contains(err.Error(), "no type") {
		t.Errorf("error = %q, want it to name the missing type", err.Error())
	}
	if _, err := TypeOfObject(json.RawMessage(`not json`)); err == nil {
		t.Error("TypeOfObject(not json): expected a refusal, got none")
	}
}

func TestNodeDocSubProtocol(t *testing.T) {
	doc := NodeDoc{Type: "vless"}
	if got := doc.SubProtocol(); got != "vless" {
		t.Errorf("SubProtocol() = %q, want %q", got, "vless")
	}
}

// jsonError builds an error that is not a LocalFileRefError, so the extraction
// helper can be shown to refuse it.
func jsonError() error {
	return &json.SyntaxError{Offset: 1}
}
