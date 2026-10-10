package topology

import (
	"net/netip"
	"testing"
	"time"

	"prism/internal/intel"
	"prism/internal/node"
	"prism/internal/platform"
	"prism/internal/subscription"
	"prism/internal/testutil"
)

// fakeSnapshot is the minimal intel projection the rebuild test needs.
type fakeSnapshot struct {
	assessments map[netip.Addr]intel.AssessmentLite
}

func (f fakeSnapshot) Assessment(ip netip.Addr) (intel.AssessmentLite, bool) {
	lite, ok := f.assessments[ip.Unmap()]
	return lite, ok
}

func (fakeSnapshot) CheckOutcome(string, string) (intel.OutcomeLite, bool) {
	return intel.OutcomeLite{}, false
}

// TestRebuildPlatform_InstallsTheProjectionBeforeEvaluating pins the ordering the
// node-selection criteria depend on: ip_type and purity_band read the intel
// projection and fail closed without it, so a rebuild that ran before the
// snapshot was installed would publish an empty routable view. CreatePlatform
// rebuilds before it registers the platform, which is exactly that case.
func TestRebuildPlatform_InstallsTheProjectionBeforeEvaluating(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "Sub1", "url", true, false)
	subMgr.Register(sub)
	pool := newTestPool(subMgr)

	raw := []byte(`{"type":"ss","server":"1.1.1.1","port":443}`)
	hash := node.HashFromRawOptions(raw)
	pool.AddNodeFromSub(hash, raw, "s1")
	// The pool's subscription lookup only reports a node as held while the
	// subscription's managed-node set still references it.
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"all"}})

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("node missing from the pool")
	}
	outbound := testutil.NewNoopOutbound()
	entry.Outbound.Store(&outbound)
	entry.SetEgressIP(netip.MustParseAddr("1.1.1.1"))
	entry.LatencyTable.Update("example.com", 20*time.Millisecond, 10*time.Minute)
	// A node that was just added starts circuit-broken; one successful passive
	// result is what makes it routable (the same call the node-list fixture uses).
	pool.RecordResult(hash, true)
	if !entry.IsHealthy() {
		t.Fatal("fixture node is not healthy")
	}

	plat := platform.NewPlatformWithTagFilter("plat-1", "plat", node.TagFilter{}, nil)
	plat.SetNodeCriteria(platform.NodeCriteria{IPTypes: []string{"residential"}})

	// Without a projection the criterion fails closed: no node can be admitted.
	pool.RebuildPlatform(plat)
	if plat.View().Size() != 0 {
		t.Fatalf("view size without a projection = %d, want 0 (fail closed)", plat.View().Size())
	}

	// A projection makes the same rebuild admit the node: RebuildPlatform installs
	// the pool's snapshot itself, so the create path (rebuild, then register) does
	// not publish an empty view.
	pool.SetQualitySnapshot(fakeSnapshot{assessments: map[netip.Addr]intel.AssessmentLite{
		netip.MustParseAddr("1.1.1.1"): {
			Band:       intel.BandCode("clean"),
			IPType:     intel.IPTypeCode("residential"),
			Verdict:    intel.VerdictCode("favorable"),
			ValidUntil: time.Now().Add(time.Hour).UnixNano(),
		},
	}})
	pool.RebuildPlatform(plat)
	if plat.View().Size() != 1 {
		t.Fatalf("view size with a projection = %d, want 1", plat.View().Size())
	}
}

// qualityPlatformFixture builds a pool holding one healthy, routable node plus
// one registered platform that filters on the intel projection. It mirrors the
// fixture of TestRebuildPlatform_InstallsTheProjectionBeforeEvaluating.
func qualityPlatformFixture(t *testing.T, criteria platform.NodeCriteria) (*GlobalNodePool, *platform.Platform) {
	t.Helper()

	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "Sub1", "url", true, false)
	subMgr.Register(sub)
	pool := newTestPool(subMgr)

	raw := []byte(`{"type":"ss","server":"1.1.1.1","port":443}`)
	hash := node.HashFromRawOptions(raw)
	pool.AddNodeFromSub(hash, raw, "s1")
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"all"}})

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("node missing from the pool")
	}
	outbound := testutil.NewNoopOutbound()
	entry.Outbound.Store(&outbound)
	entry.SetEgressIP(netip.MustParseAddr("1.1.1.1"))
	entry.LatencyTable.Update("example.com", 20*time.Millisecond, 10*time.Minute)
	pool.RecordResult(hash, true)
	if !entry.IsHealthy() {
		t.Fatal("fixture node is not healthy")
	}

	plat := platform.NewPlatformWithTagFilter("plat-1", "plat", node.TagFilter{}, nil)
	plat.SetNodeCriteria(criteria)
	pool.RegisterPlatform(plat)
	return pool, plat
}

// residentialCleanSnapshot is the projection of the fixture node's egress IP.
func residentialCleanSnapshot() fakeSnapshot {
	return fakeSnapshot{assessments: map[netip.Addr]intel.AssessmentLite{
		netip.MustParseAddr("1.1.1.1"): {
			Band:       intel.BandCode("clean"),
			IPType:     intel.IPTypeCode("residential"),
			Verdict:    intel.VerdictCode("favorable"),
			ValidUntil: time.Now().Add(time.Hour).UnixNano(),
		},
	}}
}

// TestRebuildAllPlatforms_InstallsTheProjectionBeforeEvaluating pins the ordering
// invariant RebuildPlatform documents, for the batch path: ip_type and purity_band
// read the intel projection and fail closed, so RebuildAllPlatforms must install
// the pool's snapshot on a platform before it evaluates nodes.
//
// The second half reproduces the state a stale platform copy leaves behind:
// RegisterPlatform reads the pool field without platMu, so a platform registered
// while SetQualitySnapshot runs can keep the previous (nil) projection. The batch
// rebuild must not trust the platform's copy.
func TestRebuildAllPlatforms_InstallsTheProjectionBeforeEvaluating(t *testing.T) {
	pool, plat := qualityPlatformFixture(t, platform.NodeCriteria{IPTypes: []string{"residential"}})

	// Control: with no projection in the pool the criterion fails closed.
	pool.RebuildAllPlatforms()
	if got := plat.View().Size(); got != 0 {
		t.Fatalf("view size without a projection = %d, want 0 (fail closed)", got)
	}

	pool.SetQualitySnapshot(residentialCleanSnapshot())
	// Emulate the platform whose own copy of the projection is stale.
	plat.SetQualitySnapshot(nil)

	pool.RebuildAllPlatforms()
	if got := plat.View().Size(); got != 1 {
		t.Fatalf("view size after RebuildAllPlatforms = %d, want 1 (the pool's projection must be installed first)", got)
	}
}

// TestSetQualitySnapshot_HealsTheViewBuiltWithoutTheProjection reproduces the real
// startup order (AUDIT_REPORT #8): bootstrap registers the platforms and rebuilds
// them while the intel projection is still nil (app_runtime.go bootstrap →
// RebuildAllPlatforms, before initIntelJobs → SetQualitySnapshot). Every quality
// criterion fails closed at that point, so the view is empty, and nothing else
// rebuilds the platform later — a node that recovers, or a node of a disabled
// subscription, produces no dirty event. The projection arriving afterwards must
// therefore repair the views built without it.
func TestSetQualitySnapshot_HealsTheViewBuiltWithoutTheProjection(t *testing.T) {
	pool, plat := qualityPlatformFixture(t, platform.NodeCriteria{PurityBands: []string{"clean"}})

	// Bootstrap: platforms are registered and rebuilt before the projection exists.
	pool.RebuildAllPlatforms()
	if got := plat.View().Size(); got != 0 {
		t.Fatalf("view size without a projection = %d, want 0 (fail closed)", got)
	}

	// The projection lands (initIntelJobs → SetQualitySnapshot).
	pool.SetQualitySnapshot(residentialCleanSnapshot())
	if got := plat.View().Size(); got != 1 {
		t.Fatalf("view size after the projection arrived = %d, want 1 (the view must be rebuilt)", got)
	}
}

// mutableSnapshot mirrors *intel.Snapshot: the service hands out one instance
// while it is still empty (NewService → NewSnapshot) and fills it in place later
// (Service.Start → ReloadSnapshot).
type mutableSnapshot struct {
	assessments map[netip.Addr]intel.AssessmentLite
}

func (m *mutableSnapshot) Assessment(ip netip.Addr) (intel.AssessmentLite, bool) {
	lite, ok := m.assessments[ip.Unmap()]
	return lite, ok
}

func (m *mutableSnapshot) CheckOutcome(string, string) (intel.OutcomeLite, bool) {
	return intel.OutcomeLite{}, false
}

func (m *mutableSnapshot) fill() { m.assessments = residentialCleanSnapshot().assessments }

// TestRebuildAllPlatforms_RepairsTheViewOnceTheProjectionIsFilled pins the residual
// startup gap that the in-package fix cannot close. At startup the injected reader
// is non-nil but empty, so SetQualitySnapshot's rebuild still evaluates against an
// empty projection and the view stays empty. Only a rebuild after intel.Service.Start
// has loaded the projection repairs it — cmd/prism must call RebuildAllPlatforms
// there (intel_runtime.go, after intelSvc.Start), which is outside this package.
// This test documents that contract; it passes with and without the fix.
func TestRebuildAllPlatforms_RepairsTheViewOnceTheProjectionIsFilled(t *testing.T) {
	pool, plat := qualityPlatformFixture(t, platform.NodeCriteria{PurityBands: []string{"clean"}})

	// Bootstrap rebuild, projection still nil.
	pool.RebuildAllPlatforms()
	if got := plat.View().Size(); got != 0 {
		t.Fatalf("view size without a projection = %d, want 0 (fail closed)", got)
	}

	snap := &mutableSnapshot{}
	pool.SetQualitySnapshot(snap)
	if got := plat.View().Size(); got != 0 {
		t.Fatalf("view size with an empty projection = %d, want 0 (nothing to match yet)", got)
	}

	// intel.Service.Start → ReloadSnapshot fills the same object in place.
	snap.fill()
	pool.RebuildAllPlatforms()
	if got := plat.View().Size(); got != 1 {
		t.Fatalf("view size after the projection was filled = %d, want 1 (a rebuild must repair it)", got)
	}
}
