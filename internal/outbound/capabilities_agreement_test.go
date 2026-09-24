package outbound

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"prism/internal/node"
)

// TestEngineCapabilitiesMatchRuntimeBehaviour pins the capabilities API to what
// the linked runtime can actually do (docs/ENGINE_DECISIONS.md D-1).
//
// Rules asserted here, for whatever tag set this binary was compiled with:
//   - an engine may only be reported as built when its runtime layer registered
//     itself (internal/outbound init), and
//   - a reported engine must really build a node document, while an engine that
//     is not reported must really fail.
//
// Without this, `with_mihomo` in release.yml/Dockerfile could make
// GET /api/v1/system/capabilities claim mihomo again while every mihomo node
// still fails with ENGINE_NOT_BUILT.
func TestEngineCapabilitiesMatchRuntimeBehaviour(t *testing.T) {
	byName := make(map[string]node.EngineCapability)
	for _, capability := range node.EngineCapabilities() {
		byName[capability.Name] = capability
	}
	for name, capability := range byName {
		if capability.Built && !node.EngineRuntimeRegistered(name) {
			t.Errorf("engine %s: reported built=true but no runtime layer registered it", name)
		}
	}

	sb := newTestSingboxBuilder(t)
	defer sb.Close()

	singboxCapability, ok := byName[node.EngineSingbox]
	if !ok {
		t.Fatalf("capabilities are missing the %s engine", node.EngineSingbox)
	}
	if !singboxCapability.Built {
		t.Fatalf("engine %s: reported built=false although this binary embeds it", node.EngineSingbox)
	}
	if !node.EngineRuntimeRegistered(node.EngineSingbox) {
		t.Fatalf("engine %s: reported built=true without a runtime registration", node.EngineSingbox)
	}
	handle, err := sb.Build(json.RawMessage(`{"type":"socks","tag":"caps-socks","server":"1.1.1.1","server_port":1080}`))
	if err != nil {
		t.Fatalf("engine %s: reported built=true but building a node failed: %v", node.EngineSingbox, err)
	}
	if closer, ok := handle.(io.Closer); ok {
		_ = closer.Close()
	}

	mihomoCapability, ok := byName[node.EngineMihomo]
	if !ok {
		t.Fatalf("capabilities are missing the %s engine", node.EngineMihomo)
	}
	if mihomoCapability.Built {
		t.Error("engine mihomo: reported built=true although this tree has no mihomo runtime")
	}
	if node.EngineRuntimeRegistered(node.EngineMihomo) {
		t.Error("engine mihomo: a runtime registered itself although Build rejects mihomo documents")
	}
	if len(mihomoCapability.FallbackTypes) == 0 {
		t.Error("engine mihomo: fallback_types must keep listing the types D-1 rejects")
	}

	mihomoDoc := json.RawMessage(`{"prism_node":1,"engine":"mihomo","kind":"proxy","name":"caps-ssr",` +
		`"proxy":{"type":"ssr","server":"1.2.3.4","port":443,"cipher":"aes-256-cfb","password":"synthetic-password",` +
		`"obfs":"plain","protocol":"origin"}}`)
	if _, err := sb.Build(mihomoDoc); err == nil || !strings.Contains(err.Error(), "ENGINE_NOT_BUILT") {
		t.Fatalf("engine mihomo: reported built=false but the build error is %v, want ENGINE_NOT_BUILT", err)
	}
}
