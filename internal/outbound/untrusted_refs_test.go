package outbound

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"prism/internal/node"
	"prism/internal/subscription"
)

// Untrusted subscription content must not be able to name a local file that
// Prism reads: sing-box opens such a path while building the outbound and puts
// the file's content into the error string, which Prism stores on the node and
// returns through the API. These tests are local-only (temp dirs, no network).

const (
	localFileRefSuffix   = ": file path is not supported; inline the material"
	localFileRefTestPath = "/etc/prism-canary.pem"
	// localFileRefCanary marks synthetic "file content" so a test can assert it
	// was neither read nor reflected, without ever printing it.
	localFileRefCanary = "PRISM-CANARY-CONTENT-MUST-NOT-LEAK"
)

// localFileRefSources lists one subscription per input format that can carry a
// file reference into a node document, with the field the reference lands in.
func localFileRefSources() []struct {
	name   string
	source string
	field  string
} {
	return []struct {
		name   string
		source string
		field  string
	}{
		{
			name: "singbox json",
			source: `{"outbounds":[{"type":"vless","tag":"sb","server":"example.com","server_port":443,` +
				`"uuid":"11111111-2222-3333-4444-555555555555",` +
				`"tls":{"enabled":true,"server_name":"example.com","certificate_path":"` + localFileRefTestPath + `"}}]}`,
			field: "tls.certificate_path",
		},
		{
			name: "clash json",
			source: `{"proxies":[{"name":"clash","type":"hysteria2","server":"example.com","port":443,` +
				`"password":"password","sni":"example.com","ca":"` + localFileRefTestPath + `"}]}`,
			field: "tls.certificate_path",
		},
		{
			name:   "share link",
			source: `hy2://example.com:443?password=password&sni=example.com&ca=` + "%2Fetc%2Fprism-canary.pem",
			field:  "tls.certificate_path",
		},
	}
}

// TestParseRefusesLocalFileRefsPerFormat asserts the refusal a subscription
// author sees, for each of the three formats that can carry the reference.
func TestParseRefusesLocalFileRefsPerFormat(t *testing.T) {
	for _, tc := range localFileRefSources() {
		t.Run(tc.name, func(t *testing.T) {
			result, err := subscription.ParseWithReport([]byte(tc.source))
			if err != nil {
				t.Fatalf("ParseWithReport: %v", err)
			}
			if len(result.Nodes) != 1 {
				t.Fatalf("parsed nodes: got %d, want 1", len(result.Nodes))
			}
			raw := result.Nodes[0].RawOptions
			if !strings.Contains(string(raw), localFileRefTestPath) {
				t.Fatalf("test input did not reach the node document: %s", string(raw))
			}

			// The document is refused where every consumer validates it.
			parseErr := node.RejectLocalFileRefs(raw)
			reason, detail, ok := node.LocalFileRefReason(parseErr)
			if !ok {
				t.Fatalf("RejectLocalFileRefs(%s) = %v, want a node.LocalFileRefError", tc.name, parseErr)
			}
			if reason != node.ReasonUnsupportedFeature {
				t.Fatalf("reason: got %q, want %q", reason, node.ReasonUnsupportedFeature)
			}
			wantDetail := tc.field + localFileRefSuffix
			if detail != wantDetail {
				t.Fatalf("detail: got %q, want %q", detail, wantDetail)
			}

			// ParseNodeDoc is the seam every consumer uses, including the
			// outbound builder and the subscription parse report.
			docErr := node.RejectLocalFileRefs(raw)
			docReason, docDetail, docOK := node.LocalFileRefReason(docErr)
			if !docOK || docReason != reason || docDetail != detail {
				t.Fatalf("parse seam disagrees: (%q, %q, %v)", docReason, docDetail, docOK)
			}
			if _, docParseErr := node.ParseNodeDoc(raw); docParseErr == nil {
				t.Fatal("ParseNodeDoc accepted a document that names a local file")
			} else if _, _, ok := node.LocalFileRefReason(docParseErr); !ok {
				t.Fatalf("ParseNodeDoc error is not a node.LocalFileRefError: %v", docParseErr)
			}
		})
	}
}

// TestParseKeepsInlineCertificateMaterial checks the other half of the rule: a
// subscription that carries inline material keeps working in every format.
func TestParseKeepsInlineCertificateMaterial(t *testing.T) {
	const inline = "-----BEGIN CERTIFICATE-----MIIBinline"
	sources := []struct {
		name   string
		source string
	}{
		{
			name: "singbox json",
			source: `{"outbounds":[{"type":"vless","tag":"sb","server":"example.com","server_port":443,` +
				`"uuid":"11111111-2222-3333-4444-555555555555",` +
				`"tls":{"enabled":true,"server_name":"example.com","certificate":["` + inline + `"]}}]}`,
		},
		{
			name: "clash json",
			source: `{"proxies":[{"name":"clash","type":"hysteria2","server":"example.com","port":443,` +
				`"password":"password","sni":"example.com","ca-str":"` + inline + `"}]}`,
		},
		{
			name:   "share link",
			source: `hy2://example.com:443?password=password&sni=example.com&ca-str=` + strings.ReplaceAll(inline, "-", "%2D"),
		},
	}
	for _, tc := range sources {
		t.Run(tc.name, func(t *testing.T) {
			result, err := subscription.ParseWithReport([]byte(tc.source))
			if err != nil {
				t.Fatalf("ParseWithReport: %v", err)
			}
			if len(result.Nodes) != 1 {
				t.Fatalf("parsed nodes: got %d, want 1", len(result.Nodes))
			}
			raw := result.Nodes[0].RawOptions
			if err := node.RejectLocalFileRefs(raw); err != nil {
				t.Fatalf("inline material was refused: %v", err)
			}
			if _, err := node.ParseNodeDoc(raw); err != nil {
				t.Fatalf("ParseNodeDoc(inline material): %v", err)
			}
		})
	}
}

// TestRejectLocalFileRefsCoversEveryPathOption pins the option names and the
// nesting the scan walks (tls object, chain dependencies, list values).
func TestRejectLocalFileRefsCoversEveryPathOption(t *testing.T) {
	for _, field := range []string{
		"certificate_path",
		"certificate_directory_path",
		"client_certificate_path",
		"client_key_path",
		"key_path",
		"crl_path",
		"static_key_path",
		"private_key_path",
	} {
		t.Run(field, func(t *testing.T) {
			raw := json.RawMessage(fmt.Sprintf(
				`{"type":"openvpn-client","tag":"t","server":"example.com","server_port":1194,%q:%q}`,
				field, localFileRefTestPath))
			err := node.RejectLocalFileRefs(raw)
			reason, detail, ok := node.LocalFileRefReason(err)
			if !ok {
				t.Fatalf("RejectLocalFileRefs = %v, want a refusal", err)
			}
			if reason != node.ReasonUnsupportedFeature {
				t.Fatalf("reason: got %q", reason)
			}
			if want := field + localFileRefSuffix; detail != want {
				t.Fatalf("detail: got %q, want %q", detail, want)
			}
		})
	}

	t.Run("nested tls object", func(t *testing.T) {
		raw := json.RawMessage(`{"type":"vless","tag":"t","server":"example.com","server_port":443,` +
			`"tls":{"enabled":true,"reality":{"enabled":true},"client_key_path":"/etc/prism-canary.pem"}}`)
		_, detail, ok := node.LocalFileRefReason(node.RejectLocalFileRefs(raw))
		if !ok {
			t.Fatal("a nested path option was accepted")
		}
		if want := "tls.client_key_path" + localFileRefSuffix; detail != want {
			t.Fatalf("detail: got %q, want %q", detail, want)
		}
	})

	t.Run("list value", func(t *testing.T) {
		raw := json.RawMessage(`{"type":"vless","tag":"t","server":"example.com","server_port":443,` +
			`"tls":{"enabled":true,"certificate_path":["/etc/prism-canary.pem"]}}`)
		_, detail, ok := node.LocalFileRefReason(node.RejectLocalFileRefs(raw))
		if !ok {
			t.Fatal("a list-valued path option was accepted")
		}
		if want := "tls.certificate_path" + localFileRefSuffix; detail != want {
			t.Fatalf("detail: got %q, want %q", detail, want)
		}
	})

	t.Run("chain dependency", func(t *testing.T) {
		raw := json.RawMessage(`{"prism_node":1,"engine":"singbox","kind":"chain","name":"chain",` +
			`"main":{"type":"vless","tag":"","server":"example.com","server_port":443,"detour":"d0"},` +
			`"deps":[{"type":"openvpn-client","tag":"d0","server":"example.com","server_port":1194,"static_key_path":"/etc/prism-canary.pem"}]}`)
		_, detail, ok := node.LocalFileRefReason(node.RejectLocalFileRefs(raw))
		if !ok {
			t.Fatal("a path option inside a chain dependency was accepted")
		}
		if want := "deps[0].static_key_path" + localFileRefSuffix; detail != want {
			t.Fatalf("detail: got %q, want %q", detail, want)
		}
	})

	t.Run("empty and absent values are not references", func(t *testing.T) {
		for _, raw := range []string{
			`{"type":"vless","tag":"t","server":"example.com","server_port":443,"tls":{"enabled":true}}`,
			`{"type":"vless","tag":"t","server":"example.com","server_port":443,"tls":{"enabled":true,"certificate_path":""}}`,
			`{"type":"vless","tag":"t","server":"example.com","server_port":443,"tls":{"enabled":true,"certificate_path":"   "}}`,
		} {
			if err := node.RejectLocalFileRefs(json.RawMessage(raw)); err != nil {
				t.Fatalf("RejectLocalFileRefs(%s) = %v, want nil", raw, err)
			}
		}
	})

	t.Run("detail never echoes attacker text", func(t *testing.T) {
		// A key is attacker-controlled too: the detail must stay short and
		// printable even when the key is not.
		raw := json.RawMessage(`{"type":"vless","tag":"t","server":"example.com","server_port":443,"` +
			strings.Repeat("k", 400) + `_path":"/etc/prism-canary.pem"}`)
		_, detail, ok := node.LocalFileRefReason(node.RejectLocalFileRefs(raw))
		if !ok {
			t.Fatal("a long-key path option was accepted")
		}
		if len(detail) > 160 {
			t.Fatalf("detail length %d: attacker text is reflected unbounded", len(detail))
		}
		if strings.ContainsAny(detail, "\x00\n\r\t") {
			t.Fatal("detail contains control characters")
		}
	})
}

// TestBuildRefusesLocalFileRefWithoutReadingTheFile walks the import path: the
// document reaches the embedded sing-box runtime, which must refuse it instead
// of opening the file.
func TestBuildRefusesLocalFileRefWithoutReadingTheFile(t *testing.T) {
	dir := t.TempDir()
	canary := filepath.Join(dir, "canary.pem")
	if err := os.WriteFile(canary, []byte(localFileRefCanary+"\n"), 0o600); err != nil {
		t.Fatalf("write canary: %v", err)
	}
	// The file is readable, so a refusal cannot be an artefact of permissions.
	if _, err := os.ReadFile(canary); err != nil {
		t.Fatalf("canary is not readable: %v", err)
	}

	raw := json.RawMessage(fmt.Sprintf(
		`{"type":"vless","tag":"t","server":"example.com","server_port":443,`+
			`"uuid":"11111111-2222-3333-4444-555555555555",`+
			`"tls":{"enabled":true,"server_name":"example.com","certificate_path":%q}}`, canary))

	builder := newTestSingboxBuilder(t)
	defer builder.Close()

	ob, err := builder.Build(raw)
	if err == nil {
		closeMatrixOutbound(t, ob, "vless")
		t.Fatal("Build accepted a document that names a local file")
	}
	if _, _, ok := node.LocalFileRefReason(err); !ok {
		t.Fatalf("Build error is not a node.LocalFileRefError: %v", err)
	}
	if strings.Contains(err.Error(), localFileRefCanary) {
		t.Fatal("the refusal error echoed the content of the file")
	}

	nodeDoc, parseErr := node.ParseNodeDoc(raw)
	if parseErr == nil {
		t.Fatalf("ParseNodeDoc accepted the document (kind=%s)", nodeDoc.Kind)
	}
}

// TestEnsureNodeOutboundRecordsBoundedRefusal checks the value a user sees on
// the node APIs: the refusal reason, and a cap on whatever an engine error
// tries to quote.
func TestEnsureNodeOutboundRecordsBoundedRefusal(t *testing.T) {
	t.Run("refusal reason reaches the node entry", func(t *testing.T) {
		raw := json.RawMessage(`{"type":"vless","tag":"t","server":"example.com","server_port":443,` +
			`"tls":{"enabled":true,"certificate_path":"/etc/prism-canary.pem"}}`)
		entry := node.NewNodeEntry(node.HashFromRawOptions(raw), raw, time.Now(), 0)
		pool := &mockPool{}
		pool.addEntry(entry)

		builder := newTestSingboxBuilder(t)
		defer builder.Close()
		NewOutboundManager(pool, builder).EnsureNodeOutbound(entry.Hash)

		got := entry.GetLastError()
		if !strings.Contains(got, node.ReasonUnsupportedFeature) {
			t.Fatalf("LastError %q lacks the reason code %q", got, node.ReasonUnsupportedFeature)
		}
		if !strings.Contains(got, localFileRefSuffix) {
			t.Fatalf("LastError %q lacks the explanation", got)
		}
	})

	t.Run("engine errors are capped", func(t *testing.T) {
		raw := json.RawMessage(`{"type":"vless","tag":"t","server":"example.com","server_port":443}`)
		entry := node.NewNodeEntry(node.HashFromRawOptions(raw), raw, time.Now(), 0)
		pool := &mockPool{}
		pool.addEntry(entry)

		huge := strings.Repeat("x", 4096) + localFileRefCanary
		NewOutboundManager(pool, &errorStringBuilder{err: fmt.Errorf("%s", huge)}).EnsureNodeOutbound(entry.Hash)

		got := entry.GetLastError()
		if got == "" {
			t.Fatal("LastError is empty after a build failure")
		}
		if len(got) > 600 {
			t.Fatalf("LastError length %d: engine errors are stored unbounded", len(got))
		}
		if strings.Contains(got, localFileRefCanary) {
			t.Fatal("LastError reflects unbounded engine output")
		}
		if !strings.Contains(got, "(truncated)") {
			t.Fatalf("LastError %q does not report truncation", got)
		}
	})
}

// errorStringBuilder fails with a caller-supplied error, so a test can pin how
// much of an engine error reaches a node entry.
type errorStringBuilder struct{ err error }

func (b *errorStringBuilder) Build(json.RawMessage) (adapter.Outbound, error) {
	return nil, b.err
}
