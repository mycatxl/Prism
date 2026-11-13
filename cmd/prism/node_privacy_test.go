package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/state"
	"prism/internal/topology"
)

func TestOwnEgressDiscoveryAndRestartPersistence(t *testing.T) {
	value := "203.0.113.5"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(value)) }))
	defer srv.Close()
	// Ambient proxies must never turn the proxy's exit into our own address.
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	ip, err := discoverOwnEgress(context.Background(), srv.URL, "tcp4")
	if err != nil || ip.String() != value {
		t.Fatal(ip, err)
	}
	root := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(filepath.Join(root, "state"), filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	cfg := config.NewDefaultRuntimeConfig()
	cfg.NodePrivacy.SelfProbeURL = srv.URL
	if err := engine.SaveSystemConfig(cfg, 1, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := initializeNodePrivacy(engine, nil); err != nil {
		t.Fatal(err)
	}
	first, _, _ := engine.GetSystemConfig()
	if first.NodePrivacy.ProbeStatus != "ready" || !slices.Contains(first.NodePrivacy.DetectedIPs, value) {
		t.Fatal("discovered address was not persisted")
	}
	old := value
	value = "203.0.113.6"
	if err := initializeNodePrivacy(engine, nil); err != nil {
		t.Fatal(err)
	}
	second, _, _ := engine.GetSystemConfig()
	if !slices.Contains(second.NodePrivacy.DetectedIPs, old) || !slices.Contains(second.NodePrivacy.DetectedIPs, value) {
		t.Fatal("old own asset forgotten")
	}
	value = "invalid"
	if err := initializeNodePrivacy(engine, nil); err != nil {
		t.Fatal(err)
	}
	failed, _, _ := engine.GetSystemConfig()
	if failed.NodePrivacy.ProbeStatus != "failed" || !slices.Contains(failed.NodePrivacy.DetectedIPs, old) {
		t.Fatal("failure opened gate or lost old deny entries")
	}
}

func TestBootstrapCachedNodesApplyPrivacyClassification(t *testing.T) {
	root := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(filepath.Join(root, "state"), filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	raw := []byte(`{"type":"http","server":"203.0.113.5","server_port":80}`)
	hash := node.HashFromRawOptions(raw)
	if err := engine.BulkUpsertNodesStatic([]model.NodeStatic{{Hash: hash.Hex(), RawOptions: raw, CreatedAtNs: time.Now().UnixNano()}}); err != nil {
		t.Fatal(err)
	}
	policy, _ := node.NewPrivacyPolicy(config.NodePrivacyConfig{ProbeStatus: "ready"})
	pool := topology.NewGlobalNodePool(topology.PoolConfig{PrivacyPolicy: policy, MaxConsecutiveFailures: func() int { return 3 }})
	if _, err := loadBootstrapNodeStatics(engine, pool, &config.EnvConfig{MaxLatencyTableEntries: 16}); err != nil {
		t.Fatal(err)
	}
	_, ok := pool.GetEntry(hash)
	if ok || !pool.PrivacyDeleted(hash) {
		t.Fatal("cached plaintext node escaped deletion")
	}
}

func TestOwnEgressProbeRejectsUnsafeResponses(t *testing.T) {
	for _, body := range []string{"127.0.0.1", "10.0.0.1", "bad", string(make([]byte, 514))} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		_, err := discoverOwnEgress(context.Background(), srv.URL, "tcp4")
		srv.Close()
		if err == nil {
			t.Fatal("accepted unsafe response")
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com", http.StatusFound)
	}))
	defer srv.Close()
	if _, err := discoverOwnEgress(context.Background(), srv.URL, "tcp4"); err == nil {
		t.Fatal("followed discovery redirect")
	}
}
