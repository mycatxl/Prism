package topology

import (
	"encoding/json"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/node"
	"prism/internal/platform"
	"prism/internal/subscription"
	"prism/internal/testutil"
)

// newEgressIndexProbePool builds a pool whose dirty side effects are countable:
// dynamicCalls counts onNodeDynamicChanged invocations and geoCalls counts the
// geo lookups that a platform dirty notification performs. Both counters are
// the observation channel of the "SeedEgressIndex has no side effects" test.
// The returned subscription is the one makeEgressIndexProbeNode binds its node
// to; the platform carries a region filter so a dirty notification of a
// routable node must consult the geo lookup.
func newEgressIndexProbePool(t *testing.T, dynamicCalls, geoCalls *atomic.Int32) (*GlobalNodePool, *subscription.Subscription) {
	t.Helper()
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "EgressSub", "url", true, false)
	subMgr.Register(sub)

	pool := NewGlobalNodePool(PoolConfig{
		SubLookup: subMgr.Lookup,
		GeoLookup: func(netip.Addr) string {
			geoCalls.Add(1)
			return "us"
		},
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		OnNodeDynamicChanged:   func(node.Hash) { dynamicCalls.Add(1) },
	})
	pool.RegisterPlatform(platform.NewPlatform("p1", "P1", nil, []string{"us"}))
	return pool, sub
}

// makeEgressIndexProbeNode adds a node and brings it to the state where a dirty
// notification is observable (healthy, latency known, egress IP known).
func makeEgressIndexProbeNode(t *testing.T, pool *GlobalNodePool, sub *subscription.Subscription, raw string, ip netip.Addr) node.Hash {
	t.Helper()
	h := node.HashFromRawOptions([]byte(raw))
	mn := subscription.NewManagedNodes()
	mn.StoreNode(h, subscription.ManagedNode{Tags: []string{"node-1"}})
	sub.SwapManagedNodes(mn)
	pool.AddNodeFromSub(h, json.RawMessage(raw), "s1")

	entry, ok := pool.GetEntry(h)
	if !ok {
		t.Fatalf("entry %s not found", h.Hex())
	}
	entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
		Ewma:        30 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	entry.SetEgressIP(ip)
	pool.RecordResult(h, true)
	return h
}

// TestSeedEgressIndex_MakesRestoredNodeVisibleToNotifyEgressIPDirty pins the
// audit finding 3.3 [#10]: the bootstrap path restores an egress IP with a
// direct entry.SetEgressIP, so without a seed call the node stays invisible to
// NotifyEgressIPDirty for the whole process lifetime.
func TestSeedEgressIndex_MakesRestoredNodeVisibleToNotifyEgressIPDirty(t *testing.T) {
	pool := newTestPool(NewSubscriptionManager())

	raw := `{"type":"ss","n":"bootstrap-restored"}`
	h := node.HashFromRawOptions([]byte(raw))
	pool.AddNodeFromSub(h, json.RawMessage(raw), "s1")

	entry, ok := pool.GetEntry(h)
	if !ok {
		t.Fatal("entry not found")
	}
	ip := netip.MustParseAddr("203.0.113.9")
	// This is exactly what cmd/prism restoreBootstrapNodeDynamics does.
	entry.SetEgressIP(ip)

	if owners := pool.EgressIPOwners(ip); len(owners) != 0 {
		t.Fatalf("precondition: a direct SetEgressIP must not populate the index, got owners %v", owners)
	}

	pool.SeedEgressIndex(h, ip)

	owners := pool.EgressIPOwners(ip)
	if len(owners) != 1 || owners[0] != h {
		t.Fatalf("EgressIPOwners(%s) = %v, want [%s]", ip, owners, h.Hex())
	}
	if got := pool.NotifyEgressIPDirty(ip); got != 1 {
		t.Fatalf("NotifyEgressIPDirty(%s) = %d, want 1: the restored node must be reachable through the index", ip, got)
	}
}

// TestSeedEgressIndex_InvalidIPIsNoOp pins that an unparsable/zero egress IP
// never creates an index bucket.
func TestSeedEgressIndex_InvalidIPIsNoOp(t *testing.T) {
	pool := newTestPool(NewSubscriptionManager())

	raw := `{"type":"ss","n":"bootstrap-invalid-ip"}`
	h := node.HashFromRawOptions([]byte(raw))
	pool.AddNodeFromSub(h, json.RawMessage(raw), "s1")

	pool.SeedEgressIndex(h, netip.Addr{})

	pool.egressMu.Lock()
	buckets := len(pool.egressIndex)
	pool.egressMu.Unlock()
	if buckets != 0 {
		t.Fatalf("egressIndex has %d buckets after seeding an invalid IP, want 0", buckets)
	}
	if got := pool.NotifyEgressIPDirty(netip.MustParseAddr("203.0.113.9")); got != 0 {
		t.Fatalf("NotifyEgressIPDirty after invalid seed = %d, want 0", got)
	}
}

// TestSeedEgressIndex_HasNoDirtySideEffects is the reverse guard: restoring an
// egress IP from persistence is not a fresh observation, so it must not fire
// onNodeDynamicChanged (which writes to cache.db) nor notify platforms.
func TestSeedEgressIndex_HasNoDirtySideEffects(t *testing.T) {
	var dynamicCalls, geoCalls atomic.Int32
	pool, sub := newEgressIndexProbePool(t, &dynamicCalls, &geoCalls)

	ip := netip.MustParseAddr("203.0.113.77")
	h := makeEgressIndexProbeNode(t, pool, sub, `{"type":"ss","n":"bootstrap-no-side-effects"}`, ip)

	// Positive control: a genuine dirty notification does move the geo counter,
	// so the assertions below are not vacuous.
	beforeGeo := geoCalls.Load()
	pool.NotifyNodeDirty(h)
	if geoCalls.Load() == beforeGeo {
		t.Fatal("positive control: NotifyNodeDirty did not reach the platform geo lookup")
	}

	beforeDynamic := dynamicCalls.Load()
	beforeGeo = geoCalls.Load()

	pool.SeedEgressIndex(h, ip)

	if got := dynamicCalls.Load(); got != beforeDynamic {
		t.Errorf("SeedEgressIndex fired onNodeDynamicChanged %d time(s), want 0", got-beforeDynamic)
	}
	if got := geoCalls.Load(); got != beforeGeo {
		t.Errorf("SeedEgressIndex notified platforms %d time(s), want 0", got-beforeGeo)
	}
	if got := pool.NotifyEgressIPDirty(ip); got != 1 {
		t.Fatalf("NotifyEgressIPDirty(%s) = %d, want 1 after the seed", ip, got)
	}
}
