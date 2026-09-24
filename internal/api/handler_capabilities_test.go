package api

import (
	"net/http"
	"testing"
)

// TestSystemCapabilities_NeverClaimsAnUnbuiltEngine pins the promise of
// docs/ENGINE_DECISIONS.md D-1 at the wire level: GET
// /api/v1/system/capabilities must never advertise a kernel the binary cannot
// deliver. mihomo has no runtime in this tree, so `built` stays false and
// `fallback_types` is the only mihomo information a client may rely on.
//
// The paired runtime assertion lives in internal/outbound
// TestEngineCapabilitiesMatchRuntimeBehaviour.
func TestSystemCapabilities_NeverClaimsAnUnbuiltEngine(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/system/capabilities", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := decodeJSONMap(t, rec)

	engines, ok := body["engines"].([]any)
	if !ok || len(engines) == 0 {
		t.Fatalf("capabilities engines: got %T (%v)", body["engines"], body["engines"])
	}
	seen := map[string]map[string]any{}
	for _, raw := range engines {
		entry, _ := raw.(map[string]any)
		name, _ := entry["name"].(string)
		seen[name] = entry
	}

	singbox, ok := seen["singbox"]
	if !ok {
		t.Fatalf("capabilities are missing the singbox engine: %v", body["engines"])
	}
	if singbox["built"] != true {
		t.Errorf("singbox built: got %v, want true", singbox["built"])
	}

	mihomo, ok := seen["mihomo"]
	if !ok {
		t.Fatalf("capabilities are missing the mihomo engine: %v", body["engines"])
	}
	if mihomo["built"] != false {
		t.Errorf("mihomo built: got %v, want false (no mihomo runtime exists)", mihomo["built"])
	}
	fallback, _ := mihomo["fallback_types"].([]any)
	if len(fallback) == 0 {
		t.Error("mihomo fallback_types must stay listed: it is the only honest mihomo signal")
	}
}
