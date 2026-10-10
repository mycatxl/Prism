package main

import (
	"context"
	"database/sql"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"prism/internal/intel"
	"prism/internal/intel/jobs"
	"prism/internal/intel/store"
	"prism/internal/node"
	"prism/internal/platform"
	"prism/internal/subscription"
	"prism/internal/testutil"
	"prism/internal/topology"
)

// projectionPlatformID is the platform the fixture registers.
const projectionPlatformID = "projection-start"

// projectionEgressIP is the egress address the fixture assessment is keyed on.
const projectionEgressIP = "203.0.113.77"

// TestStartIntelRebuildsPlatformsOnceTheProjectionIsLoaded pins the last link of
// the F7 chain, the one that lives in this package.
//
// The startup order is: bootstrapFromPersistence rebuilds every platform
// (cmd/prism/app_runtime.go:221 -> :404 RebuildAllPlatforms), initIntelJobs
// injects the projection object (intel_runtime.go:179), and only later does
// startBackgroundServices call startIntel -> Service.Start ->
// ReloadSnapshot (internal/intel/service.go:520), which is what actually fills
// that object with the persisted assessments.
//
// The quality criteria read the projection fail-closed, so a platform carrying
// quality_policy / ip_types / purity_bands is rebuilt - twice - against a
// projection that holds no assessment yet, and its routable view stays empty.
// Nothing else repairs it: a node whose circuit is already closed produces no
// dirty event when a probe succeeds, and a disabled subscription is never
// refreshed. startIntel therefore has to rebuild once after Start() loaded the
// projection.
func TestStartIntelRebuildsPlatformsOnceTheProjectionIsLoaded(t *testing.T) {
	app, plat := newProjectionStartApp(t)

	// Before startIntel: the projection object exists but holds nothing, exactly
	// as it does when bootstrapFromPersistence ran its rebuild.
	if got := plat.View().Size(); got != 0 {
		t.Fatalf("view size before the projection is loaded = %d, want 0", got)
	}

	if err := app.startIntel(); err != nil {
		t.Fatalf("startIntel: %v", err)
	}
	t.Cleanup(func() {
		if app.intelSvc != nil {
			app.intelSvc.Stop()
		}
	})

	// Service.Start loaded the assessment, so the platform must be re-evaluated
	// against it. Without the rebuild in startIntel the view stays at 0.
	if got := plat.View().Size(); got != 1 {
		t.Errorf("view size after startIntel = %d, want 1: the projection was loaded "+
			"but the platform was never rebuilt against it", got)
	}
}

// newProjectionStartApp builds the minimum app state startIntel needs: a pool
// holding one healthy node, a platform whose criteria read the intel projection,
// and an intel service over a real store that already holds the assessment and
// egress observation of that node.
func newProjectionStartApp(t *testing.T) (*prismApp, *platform.Platform) {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(filepath.Join(t.TempDir(), "state", store.FileName))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	raw := []byte(`{"type":"ss","server":"198.51.100.9","port":443}`)
	hash := node.HashFromRawOptions(raw)
	egress := netip.MustParseAddr(projectionEgressIP)

	// The evidence Service.Start loads into the projection: one egress
	// observation and the assessment keyed on that address.
	if err := st.UpsertNodeEgress(ctx, store.NodeEgress{
		NodeHash: hash.String(), IPv4: projectionEgressIP,
	}); err != nil {
		t.Fatalf("UpsertNodeEgress: %v", err)
	}
	if err := st.UpsertAssessment(ctx, store.Assessment{
		IP:           projectionEgressIP,
		Profile:      "prism-purity-v2",
		State:        "valid",
		Verdict:      "favorable",
		PurityScore:  sql.NullInt64{Int64: 90, Valid: true},
		PurityBand:   "clean",
		Confidence:   "high",
		IPType:       "residential",
		ComputedAtNs: time.Now().UnixNano(),
		ValidUntilNs: time.Now().Add(time.Hour).UnixNano(),
	}); err != nil {
		t.Fatalf("UpsertAssessment: %v", err)
	}

	subMgr := topology.NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "Sub1", "url", true, false)
	subMgr.Register(sub)
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
	})
	pool.AddNodeFromSub(hash, raw, "s1")
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"all"}})

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("node missing from the pool")
	}
	outbound := testutil.NewNoopOutbound()
	entry.Outbound.Store(&outbound)
	entry.SetEgressIP(egress)
	entry.LatencyTable.Update("example.com", 20*time.Millisecond, 10*time.Minute)
	pool.RecordResult(hash, true)
	if !entry.IsHealthy() {
		t.Fatal("fixture node is not healthy")
	}

	plat := platform.NewPlatformWithTagFilter(projectionPlatformID, "Projection", node.TagFilter{}, nil)
	plat.SetNodeCriteria(platform.NodeCriteria{PurityBands: []string{"clean"}})
	pool.RegisterPlatform(plat)

	svc, err := intel.NewService(intel.Options{
		Store: st,
		Scope: jobs.ScopeResolverFunc(func(context.Context, jobs.Scope) ([]string, error) {
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatalf("intel.NewService: %v", err)
	}
	// The projection object the pool holds: non-nil but empty until Start runs.
	pool.SetQualitySnapshot(svc.Snapshot())

	return &prismApp{
		intelStore:  st,
		intelSvc:    svc,
		topoRuntime: &topologyRuntime{pool: pool},
	}, plat
}
