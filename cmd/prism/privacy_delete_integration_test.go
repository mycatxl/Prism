package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/netip"
	"path/filepath"
	"prism/internal/config"
	"prism/internal/intel"
	"prism/internal/intel/store"
	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/platform"
	"prism/internal/probe"
	"prism/internal/routing"
	"prism/internal/state"
	"prism/internal/subscription"
	"prism/internal/testutil"
	"prism/internal/topology"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrivacyDeleteRemovesEverySurfaceAndCannotResurrect(t *testing.T) {
	root := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(filepath.Join(root, "state"), filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	st, err := store.Open(filepath.Join(root, "intel", store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.NewDefaultRuntimeConfig()
	cfg.NodePrivacy = config.NodePrivacyConfig{ProbeStatus: "ready", OwnCIDRs: []string{"192.0.2.0/24"}}
	policy, _ := node.NewPrivacyPolicy(cfg.NodePrivacy)
	sm := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{PrivacyPolicy: policy, SubLookup: sm.Lookup, MaxConsecutiveFailures: func() int { return 3 }, MaxLatencyTableEntries: 16})
	sm.PrivacyFilter = pool.RejectPrivacyDocument
	var fetches atomic.Int32
	pm := probe.NewProbeManager(probe.ProbeConfig{Pool: pool, Concurrency: 1, Fetcher: func(node.Hash, string) ([]byte, time.Duration, error) {
		fetches.Add(1)
		return []byte("ip=203.0.113.90\n"), time.Millisecond, nil
	}})
	router := routing.NewRouter(routing.RouterConfig{Pool: pool, Authorities: func() []string { return nil }, P2CWindow: func() time.Duration { return time.Hour }})
	app := &prismApp{stateEngine: engine, intelStore: st, runtimeCfg: &atomic.Pointer[config.RuntimeConfig]{}, topoRuntime: &topologyRuntime{pool: pool, subManager: sm, probeMgr: pm, router: router}}
	app.runtimeCfg.Store(cfg)
	if err := app.initPrivacyDeletion(); err != nil {
		t.Fatal(err)
	}
	raw := `{"outbounds":[{"type":"http","tag":"leak","server":"203.0.113.10","server_port":443,"tls":{"enabled":true},"password":"delete-canary"},{"type":"http","tag":"impure","server":"203.0.113.11","server_port":443,"tls":{"enabled":true},"password":"keep-canary"}]}`
	parsed, err := subscription.ParseGeneralSubscription([]byte(raw))
	if err != nil || len(parsed) != 2 {
		t.Fatal(err)
	}
	hashes := []node.Hash{node.HashFromRawOptions(parsed[0].RawOptions), node.HashFromRawOptions(parsed[1].RawOptions)}
	for _, id := range []string{"source-a", "source-b"} {
		if err := engine.UpsertSubscription(model.Subscription{ID: id, Name: id, SourceType: "local", Content: raw, UpdateIntervalNs: int64(time.Minute), Enabled: true}); err != nil {
			t.Fatal(err)
		}
		sub := subscription.NewSubscription(id, id, "", true, false)
		sub.SetSourceType("local")
		sub.SetContent(raw)
		sm.Register(sub)
		for i, n := range parsed {
			sub.ManagedNodes().StoreNode(hashes[i], subscription.ManagedNode{Tags: []string{n.Tag}})
			pool.AddNodeFromSub(hashes[i], n.RawOptions, id)
			engine.MarkSubscriptionNode(id, hashes[i].Hex())
		}
	}
	snap := intel.NewSnapshot()
	pool.SetQualitySnapshot(snap)
	plat := platform.NewPlatform("privacy-test", "privacy-test", nil, nil)
	pool.RegisterPlatform(plat)
	for i, h := range hashes {
		ip := netip.MustParseAddr([]string{"203.0.113.80", "203.0.113.81"}[i])
		e, _ := pool.GetEntry(h)
		ob := testutil.NewNoopOutbound()
		e.Outbound.Store(&ob)
		e.SetEgressIP(ip)
		e.SetIdentityMeasurement(ip, "measured")
		pool.SeedEgressIndex(h, ip)
		e.LatencyTable.LoadEntry("example.test", node.DomainLatencyStats{Ewma: time.Millisecond, LastUpdated: time.Now()})
		pool.RecordResult(h, true)
		snap.SetAssessment(ip, intel.AssessmentLite{State: intel.StateValid, Score: 0, Verdict: intel.VerdictHighRisk, ValidUntil: time.Now().Add(time.Hour).UnixNano()})
		engine.MarkNodeStatic(h.Hex())
		engine.MarkNodeDynamic(h.Hex())
		engine.MarkNodeLatency(h.Hex(), "example.test")
		_, _, err := st.RecordEgress(context.Background(), store.EgressObservation{NodeHash: h.Hex(), IPv4: ip, NowNs: time.Now().UnixNano()})
		if err != nil {
			t.Fatal(err)
		}
	}
	pool.RebuildAllPlatforms()
	if plat.View().Size() != 2 {
		t.Fatal("measured impure fixture should be sellable under {}", plat.View().Size())
	}
	target := hashes[0]
	ip := netip.MustParseAddr("203.0.113.80")
	router.RestoreLeases([]model.Lease{{PlatformID: plat.ID, UserID: "tenant", Account: "sticky", NodeHash: target.Hex(), EgressIP: ip.String(), CreatedAtNs: time.Now().UnixNano(), ExpiryNs: time.Now().Add(time.Hour).UnixNano()}})
	engine.MarkLease(plat.ID, "tenant", "sticky")
	ctx := context.Background()
	db := st.DB()
	statements := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO node_checks VALUES(?, 'fixture',1,?,'ok','','{}',1,1,999999999999999999)`, []any{target.Hex(), ip.String()}},
		{`INSERT INTO evidence VALUES(?,'fixture','fixture',?,'ok',1,999999999999999999,'{}',NULL,'')`, []any{ip.String(), target.Hex()}},
		{`INSERT INTO jobs(id,kind,status,priority,request_json,created_by,created_at_ns) VALUES('fixture','full','queued',1,?,'test',1)`, []any{`{"scope":{"node_hashes":["` + target.Hex() + `"]}}`}},
		{`INSERT INTO job_items(job_id,node_hash,status,next_run_at_ns,updated_at_ns) VALUES('fixture',?,'queued',1,1)`, []any{target.Hex()}},
	}
	for _, q := range statements {
		if _, err := db.Exec(q.q, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := engine.FlushDirtySets(newFlushReaders(pool, sm, router)); err != nil {
		t.Fatal(err)
	}
	pm.TriggerImmediateEgressProbe(target)
	before := pool.Size()
	owned := netip.MustParseAddr("192.0.2.55")
	pool.UpdateNodeEgressIP(target, &owned, nil)
	if pool.Size() != before-1 {
		t.Fatal("total did not fall by one")
	}
	if _, ok := pool.GetEntry(target); ok {
		t.Fatal("inventory retained")
	}
	if plat.View().Contains(target) {
		t.Fatal("routing retained")
	}
	router.RangeLeases(plat.ID, func(_, _ string, l routing.Lease) bool {
		if l.NodeHash == target {
			t.Fatal("lease retained")
		}
		return true
	})
	subs, _ := engine.ListSubscriptions()
	for _, s := range subs {
		if strings.Contains(s.Content, "delete-canary") || !strings.Contains(s.Content, "keep-canary") {
			t.Fatal("source document not scrubbed")
		}
		if _, ok := sm.Lookup(s.ID).ManagedNodes().LoadNode(target); ok {
			t.Fatal("managed mapping retained")
		}
	}
	for _, table := range []string{"node_egress", "egress_history", "node_checks", "job_items", "provider_node_state"} {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM "+table+" WHERE node_hash=?", target.Hex()).Scan(&n); err != nil || n != 0 {
			t.Fatal(table, n, err)
		}
	}
	cache, err := sql.Open("sqlite", filepath.Join(root, "cache", "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	for table, col := range map[string]string{"nodes_static": "hash", "nodes_dynamic": "hash", "node_latency": "node_hash", "subscription_nodes": "node_hash", "leases": "node_hash"} {
		var n int
		if err := cache.QueryRow("SELECT count(*) FROM "+table+" WHERE "+col+"=?", target.Hex()).Scan(&n); err != nil || n != 0 {
			t.Fatal(table, n, err)
		}
	}
	// A held normalized refresh and a queued/late measurement cannot restore it.
	pool.AddNodeFromSub(target, parsed[0].RawOptions, "source-a")
	if err := engine.BulkUpsertNodesStatic([]model.NodeStatic{{Hash: target.Hex(), RawOptions: parsed[0].RawOptions}}); err != nil {
		t.Fatal(err)
	}
	_, _, _ = st.RecordEgress(ctx, store.EgressObservation{NodeHash: target.Hex(), IPv4: ip, NowNs: 2})
	if _, ok, _ := st.GetNodeEgress(ctx, target.Hex()); ok {
		t.Fatal("late intel write revived node")
	}
	pm.Start()
	pm.TriggerImmediateEgressProbe(target)
	pm.Stop()
	if fetches.Load() != 0 {
		t.Fatal("deleted node probed")
	}
	events, err := engine.PrivacyDeletions()
	if err != nil || len(events) != 2 {
		t.Fatal("source audit", events, err)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), "canary") {
		t.Fatal("audit contains credential")
	}
	if _, ok := pool.GetEntry(hashes[1]); !ok {
		t.Fatal("impure sibling deleted")
	}
	pool.RecordResult(hashes[1], false)
	pool.RecordResult(hashes[1], false)
	pool.RecordResult(hashes[1], false)
	if _, ok := pool.GetEntry(hashes[1]); !ok {
		t.Fatal("unhealthy sibling deleted")
	}
	if err := app.initPrivacyDeletion(); err != nil {
		t.Fatal("replay", err)
	}
	if pool.Size() != 1 {
		t.Fatal("replay changed surviving inventory")
	}
}

func TestStartupDeletesDisabledLocalOnlyPrivacyDocuments(t *testing.T) {
	root := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(filepath.Join(root, "state"), filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	content := `{"outbounds":[{"type":"http","server":"203.0.113.8","server_port":80,"password":"local-only-canary"}]}`
	if err := engine.UpsertSubscription(model.Subscription{ID: "disabled", Name: "disabled", SourceType: "local", Content: content, UpdateIntervalNs: int64(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewDefaultRuntimeConfig()
	cfg.NodePrivacy.ProbeStatus = "ready"
	policy, _ := node.NewPrivacyPolicy(cfg.NodePrivacy)
	sm := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{PrivacyPolicy: policy, SubLookup: sm.Lookup, MaxConsecutiveFailures: func() int { return 3 }})
	pm := probe.NewProbeManager(probe.ProbeConfig{Pool: pool})
	app := &prismApp{stateEngine: engine, runtimeCfg: &atomic.Pointer[config.RuntimeConfig]{}, topoRuntime: &topologyRuntime{pool: pool, subManager: sm, probeMgr: pm}}
	app.runtimeCfg.Store(cfg)
	if err := app.initPrivacyDeletion(); err != nil {
		t.Fatal(err)
	}
	subs, _ := engine.ListSubscriptions()
	if len(subs) != 1 || strings.Contains(subs[0].Content, "local-only-canary") {
		t.Fatal("disabled source retained document")
	}
	events, _ := engine.PrivacyDeletions()
	if len(events) != 1 || events[0].SubscriptionID != "disabled" {
		t.Fatal("missing audit", events)
	}
}
