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
