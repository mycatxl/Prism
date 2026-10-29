package service

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

	"prism/internal/node"
	"prism/internal/subscription"
	"prism/internal/testutil"
	"prism/internal/topology"
)

type previewFilterFixture struct {
	cp          *ControlPlaneService
	hkHash      string
	usHash      string
	unknownHash string
}

func buildPreviewFilterFixture(t *testing.T) previewFilterFixture {
	t.Helper()

	subMgr := topology.NewSubscriptionManager()
	sub := subscription.NewSubscription("sub-1", "sub-1", "https://example.com/sub", true, false)
	subMgr.Register(sub)

	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(netip.Addr) string { return "" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})

	hkRaw := []byte(`{"type":"ss","server":"1.1.1.1","port":443}`)
	hkHash := node.HashFromRawOptions(hkRaw)
	pool.AddNodeFromSub(hkHash, hkRaw, sub.ID)
	sub.ManagedNodes().StoreNode(hkHash, subscription.ManagedNode{Tags: []string{"all", "hk"}})

	usRaw := []byte(`{"type":"ss","server":"2.2.2.2","port":443}`)
	usHash := node.HashFromRawOptions(usRaw)
	pool.AddNodeFromSub(usHash, usRaw, sub.ID)
	sub.ManagedNodes().StoreNode(usHash, subscription.ManagedNode{Tags: []string{"all", "us"}})

	unknownRaw := []byte(`{"type":"ss","server":"3.3.3.3","port":443}`)
	unknownHash := node.HashFromRawOptions(unknownRaw)
	pool.AddNodeFromSub(unknownHash, unknownRaw, sub.ID)
	sub.ManagedNodes().StoreNode(unknownHash, subscription.ManagedNode{Tags: []string{"all", "unknown"}})

	hkEntry, ok := pool.GetEntry(hkHash)
	if !ok {
		t.Fatal("hk entry missing")
	}
	hkOutbound := testutil.NewNoopOutbound()
	hkEntry.Outbound.Store(&hkOutbound)
	hkEntry.SetEgressIP(netip.MustParseAddr("1.1.1.1"))
	hkEntry.SetEgressRegion("hk")

	usEntry, ok := pool.GetEntry(usHash)
	if !ok {
		t.Fatal("us entry missing")
	}
	usOutbound := testutil.NewNoopOutbound()
	usEntry.Outbound.Store(&usOutbound)
	usEntry.SetEgressIP(netip.MustParseAddr("2.2.2.2"))
	usEntry.SetEgressRegion("us")

	unknownEntry, ok := pool.GetEntry(unknownHash)
	if !ok {
		t.Fatal("unknown entry missing")
	}
	unknownOutbound := testutil.NewNoopOutbound()
	unknownEntry.Outbound.Store(&unknownOutbound)
	unknownEntry.SetEgressIP(netip.MustParseAddr("3.3.3.3"))

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
	}
	return previewFilterFixture{
		cp:          cp,
		hkHash:      hkHash.Hex(),
		usHash:      usHash.Hex(),
		unknownHash: unknownHash.Hex(),
	}
}

func TestPreviewFilter_RegionNegation(t *testing.T) {
	fixture := buildPreviewFilterFixture(t)

	nodes, err := fixture.cp.PreviewFilter(PreviewFilterRequest{
		PlatformSpec: &PlatformSpecFilter{
			RegexFilters:  []string{".*"},
			RegionFilters: []string{"!hk"},
		},
	})
	if err != nil {
		t.Fatalf("PreviewFilter: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes len = %d, want 1", len(nodes))
	}
	if nodes[0].NodeHash != fixture.usHash {
		t.Fatalf("matched node = %s, want %s", nodes[0].NodeHash, fixture.usHash)
	}
	if nodes[0].NodeHash == fixture.hkHash {
		t.Fatalf("hk node %s should have been excluded", fixture.hkHash)
	}
}

func TestPreviewFilter_RegexRulesAnyMustAndMustNot(t *testing.T) {
	fixture := buildPreviewFilterFixture(t)

	nodes, err := fixture.cp.PreviewFilter(PreviewFilterRequest{
		PlatformSpec: &PlatformSpecFilter{
			RegexFilters: []string{"hk", "us"},
		},
	})
	if err != nil {
		t.Fatalf("PreviewFilter ANY: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("ANY nodes len = %d, want 2", len(nodes))
	}

	nodes, err = fixture.cp.PreviewFilter(PreviewFilterRequest{
		PlatformSpec: &PlatformSpecFilter{
			RegexFilters: []string{"hk", "us", `*^sub-1/`},
		},
	})
	if err != nil {
		t.Fatalf("PreviewFilter MUST: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("MUST nodes len = %d, want 2", len(nodes))
	}

	nodes, err = fixture.cp.PreviewFilter(PreviewFilterRequest{
		PlatformSpec: &PlatformSpecFilter{
			RegexFilters: []string{"all", "!hk", "!unknown"},
		},
	})
	if err != nil {
		t.Fatalf("PreviewFilter MUST_NOT: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeHash != fixture.usHash {
		t.Fatalf("MUST_NOT nodes = %+v, want only %s", nodes, fixture.usHash)
	}
}

func TestPreviewFilter_RegionMixedIncludeExclude(t *testing.T) {
	fixture := buildPreviewFilterFixture(t)

	nodes, err := fixture.cp.PreviewFilter(PreviewFilterRequest{
		PlatformSpec: &PlatformSpecFilter{
			RegexFilters:  []string{".*"},
			RegionFilters: []string{"hk", "!us"},
		},
	})
	if err != nil {
		t.Fatalf("PreviewFilter: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes len = %d, want 1", len(nodes))
	}
	if nodes[0].NodeHash != fixture.hkHash {
		t.Fatalf("matched node = %s, want %s", nodes[0].NodeHash, fixture.hkHash)
	}

	nodes, err = fixture.cp.PreviewFilter(PreviewFilterRequest{
		PlatformSpec: &PlatformSpecFilter{
			RegexFilters:  []string{".*"},
			RegionFilters: []string{"hk", "!hk"},
		},
	})
	if err != nil {
		t.Fatalf("PreviewFilter: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("nodes len = %d, want 0", len(nodes))
	}
}

func TestPreviewFilter_RegionNegation_UnknownRegionExcluded(t *testing.T) {
	fixture := buildPreviewFilterFixture(t)

	nodes, err := fixture.cp.PreviewFilter(PreviewFilterRequest{
		PlatformSpec: &PlatformSpecFilter{
			RegexFilters:  []string{".*"},
			RegionFilters: []string{"!hk"},
		},
	})
	if err != nil {
		t.Fatalf("PreviewFilter: %v", err)
	}

	for _, node := range nodes {
		if node.NodeHash == fixture.unknownHash {
			t.Fatalf("node with unknown region %s should not match region filters", fixture.unknownHash)
		}
	}
}

func TestPlatformPreviewsBoundLargePoolsAndReportTruncation(t *testing.T) {
	const poolSize = previewScanLimit + 1
	subMgr := topology.NewSubscriptionManager()
	sub := subscription.NewSubscription("bulk", "bulk", "https://example.com/bulk", true, false)
	subMgr.Register(sub)
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		MaxLatencyTableEntries: 1,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})
	for i := 0; i < poolSize; i++ {
		raw := []byte(fmt.Sprintf("{\"type\":\"ss\",\"server\":\"node-%d.example\",\"port\":443}", i))
		hash := node.HashFromRawOptions(raw)
		pool.AddNodeFromSub(hash, raw, sub.ID)
		sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"all"}})
		entry, ok := pool.GetEntry(hash)
		if !ok {
			t.Fatalf("entry %s missing after insertion", hash)
		}
		entry.SetEgressIP(netip.MustParseAddr("192.0.2.1"))
		outbound := testutil.NewNoopOutbound()
		entry.Outbound.Store(&outbound)
	}
	cp := &ControlPlaneService{Pool: pool, SubMgr: subMgr}
	makeFixtureNodesRoutable(t, cp)
	req := PreviewFilterRequest{PlatformSpec: &PlatformSpecFilter{RegexFilters: []string{".*"}}}

	scope, err := cp.PreviewPlatformScope(req)
	if err != nil {
		t.Fatalf("PreviewPlatformScope: %v", err)
	}
	if scope.Scanned != previewScanLimit || scope.Matched != previewScanLimit || !scope.Truncated {
		t.Fatalf("scope scanned/matched/truncated = %d/%d/%v, want %d/%d/true",
			scope.Scanned, scope.Matched, scope.Truncated, previewScanLimit, previewScanLimit)
	}
	if len(scope.Sample) != previewSampleLimit {
		t.Fatalf("scope sample length = %d, want %d", len(scope.Sample), previewSampleLimit)
	}

	facets, err := cp.PlatformNodeFacets()
	if err != nil {
		t.Fatalf("PlatformNodeFacets: %v", err)
	}
	if facets.TotalNodes != poolSize || facets.Scanned != previewScanLimit || !facets.Truncated {
		t.Fatalf("facets total/scanned/truncated = %d/%d/%v, want %d/%d/true",
			facets.TotalNodes, facets.Scanned, facets.Truncated, poolSize, previewScanLimit)
	}

	page, err := cp.PreviewFilterPageReport(req, 10, 0)
	if err != nil {
		t.Fatalf("PreviewFilterPageReport: %v", err)
	}
	if page.Scanned != previewScanLimit || page.Matched != previewScanLimit || !page.Truncated || len(page.Nodes) != 10 {
		t.Fatalf("page scanned/matched/truncated/items = %d/%d/%v/%d, want %d/%d/true/10",
			page.Scanned, page.Matched, page.Truncated, len(page.Nodes), previewScanLimit, previewScanLimit)
	}
	if _, err := cp.PreviewFilter(req); err == nil {
		t.Fatal("legacy PreviewFilter must reject a partial result")
	}
}
