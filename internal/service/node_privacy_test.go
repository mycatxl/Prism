package service

import (
	"encoding/json"
	"slices"
	"testing"

	"prism/internal/config"
	"prism/internal/node"
	"prism/internal/state"
	"prism/internal/topology"
)

func TestNodePrivacyConfigPersistsAndReverses(t *testing.T) {
	h := newPatchHarness(t)
	cfg := config.NewDefaultRuntimeConfig()
	cfg.NodePrivacy = config.NodePrivacyConfig{ProbeStatus: "ready", DetectedIPs: []string{"192.0.2.1"}}
	h.runtimeCfg.Store(cfg)
	p, _ := node.NewPrivacyPolicy(cfg.NodePrivacy)
	h.cp.Pool = topology.NewGlobalNodePool(topology.PoolConfig{PrivacyPolicy: p, MaxConsecutiveFailures: func() int { return 3 }})
	raw := json.RawMessage(`{"type":"http","server":"203.0.113.10","tls":{"enabled":true}}`)
	hash := node.HashFromRawOptions(raw)
	h.cp.Pool.AddNodeFromSub(hash, raw, "fixture")
	entry, _ := h.cp.Pool.GetEntry(hash)
	if cfg.NodePrivacy.EchoEnabled {
		t.Fatal("echo must default off")
	}
	body, _ := json.Marshal(map[string]any{"node_privacy": map[string]any{"echo_enabled": true, "own_cidrs": []string{"198.51.100.0/24"}, "manual_quarantine": []string{hash.Hex()}}})
	if _, err := h.cp.PatchRuntimeConfig(body); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(entry.Privacy().Reasons, node.PrivacyManual) {
		t.Fatal("policy not applied to imported node")
	}
	h.closeDB()
	engine, closer, err := state.PersistenceBootstrap(h.stateDir, h.cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	restored, _, err := engine.GetSystemConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !restored.NodePrivacy.EchoEnabled || len(restored.NodePrivacy.OwnCIDRs) != 1 || len(restored.NodePrivacy.ManualQuarantine) != 1 || len(restored.NodePrivacy.DetectedIPs) != 1 {
		t.Fatal("deny entries lost on restart")
	}
	h.cp.Engine = engine
	if _, err := h.cp.PatchRuntimeConfig([]byte(`{"node_privacy":{"echo_enabled":false,"own_cidrs":[],"manual_quarantine":[]}}`)); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(entry.Privacy().Reasons, node.PrivacyManual) {
		t.Fatal("admin could not reverse manual quarantine")
	}
	before := h.runtimeCfg.Load()
	for _, body := range []string{`{"node_privacy":{"own_cidrs":["bad"]}}`, `{"node_privacy":{"probe_status":"ready"}}`, `{"node_privacy":{"active_ips":[]}}`, `{"node_privacy":{"own_cidrs":null}}`, `{"node_privacy":{"self_probe_url":"http://example.com"}}`} {
		if _, err := h.cp.PatchRuntimeConfig([]byte(body)); err == nil {
			t.Fatal("accepted invalid or read-only field", body)
		}
		if h.runtimeCfg.Load() != before {
			t.Fatal("failed write mutated policy")
		}
	}
}
