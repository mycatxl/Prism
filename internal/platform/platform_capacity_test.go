package platform

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"runtime"
	"testing"
	"time"

	"prism/internal/node"
	"prism/internal/quality"
	"prism/internal/testutil"
)

// makeCapacityEntry creates a fully routable node entry for capacity testing.
func makeCapacityEntry(hash node.Hash, subID string, ip netip.Addr) *node.NodeEntry {
	e := node.NewNodeEntry(hash, nil, time.Now(), 16)
	e.AddSubscriptionID(subID)
	e.LatencyTable.LoadEntry("cloudflare.com", node.DomainLatencyStats{
		Ewma:        100 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	ob := testutil.NewNoopOutbound()
	e.Outbound.Store(&ob)
	e.SetEgressIP(ip)
	return e
}

// TestPlatformCapacity_FullRebuildLargePool tests platform view rebuild with large node counts.
func TestPlatformCapacity_FullRebuildLargePool(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping capacity test in short mode")
	}

	sizes := []struct {
		name  string
		count int
	}{
		{"10k_nodes", 10_000},
		{"50k_nodes", 50_000},
		{"100k_nodes", 100_000},
	}

	for _, tc := range sizes {
		t.Run(tc.name, func(t *testing.T) {
			plat := NewPlatform("cap-test", "CapacityTest", nil, nil)
			subID := "capacity-sub"

			// Prepare nodes
			entries := make(map[node.Hash]*node.NodeEntry, tc.count)
			for i := 0; i < tc.count; i++ {
				raw := json.RawMessage(fmt.Sprintf(`{"id":"cap-%d"}`, i))
				h := node.HashFromRawOptions(raw)
				ip := netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
				entries[h] = makeCapacityEntry(h, subID, ip)
			}

			var m0, m1 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m0)

			start := time.Now()
			plat.FullRebuild(
				func(fn func(node.Hash, *node.NodeEntry) bool) {
					for h, e := range entries {
						if !fn(h, e) {
							return
						}
					}
				},
				func(_ string, _ node.Hash) (string, bool, []string, bool) { return "", true, nil, true },
				func(_ netip.Addr) string { return "us" },
				func(_ netip.Addr) quality.Summary { return quality.Summary{} },
			)
			elapsed := time.Since(start)

			runtime.GC()
			runtime.ReadMemStats(&m1)

			viewSize := plat.View().Size()
			allocMB := float64(m1.Alloc-m0.Alloc) / 1024 / 1024

			t.Logf("Rebuilt platform view with %d nodes in %v", viewSize, elapsed)
			t.Logf("Memory: Alloc=%.2fMB", allocMB)
			t.Logf("Throughput: %.0f nodes/sec", float64(tc.count)/elapsed.Seconds())

			if viewSize != tc.count {
				t.Fatalf("expected %d routable nodes, got %d", tc.count, viewSize)
			}
		})
	}
}

// TestPlatformCapacity_NotifyDirtyThroughput tests incremental view updates.
func TestPlatformCapacity_NotifyDirtyThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping capacity test in short mode")
	}

	plat := NewPlatform("dirty-test", "DirtyTest", nil, nil)
	subID := "dirty-sub"
	baseCount := 10_000
	updateCount := 1_000

	// Populate initial view
	entries := make(map[node.Hash]*node.NodeEntry)
	for i := 0; i < baseCount; i++ {
		raw := json.RawMessage(fmt.Sprintf(`{"id":"dirty-%d"}`, i))
		h := node.HashFromRawOptions(raw)
		ip := netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)})
		entries[h] = makeCapacityEntry(h, subID, ip)
	}

	plat.FullRebuild(
		func(fn func(node.Hash, *node.NodeEntry) bool) {
			for h, e := range entries {
				if !fn(h, e) {
					return
				}
			}
		},
		func(_ string, _ node.Hash) (string, bool, []string, bool) { return "", true, nil, true },
		func(_ netip.Addr) string { return "us" },
		func(_ netip.Addr) quality.Summary { return quality.Summary{} },
	)

	if plat.View().Size() != baseCount {
		t.Fatalf("setup: expected %d nodes, got %d", baseCount, plat.View().Size())
	}

	// Measure incremental updates
	getEntry := func(hash node.Hash) (*node.NodeEntry, bool) {
		e, ok := entries[hash]
		return e, ok
	}

	start := time.Now()
	for i := 0; i < updateCount; i++ {
		raw := json.RawMessage(fmt.Sprintf(`{"id":"dirty-%d"}`, i))
		h := node.HashFromRawOptions(raw)
		plat.NotifyDirty(
			h,
			getEntry,
			func(_ string, _ node.Hash) (string, bool, []string, bool) { return "", true, nil, true },
			func(_ netip.Addr) string { return "us" },
			func(_ netip.Addr) quality.Summary { return quality.Summary{} },
		)
	}
	elapsed := time.Since(start)

	t.Logf("Processed %d incremental updates in %v", updateCount, elapsed)
	t.Logf("Throughput: %.0f updates/sec", float64(updateCount)/elapsed.Seconds())
	t.Logf("Average latency: %v per update", elapsed/time.Duration(updateCount))
}

// TestPlatformCapacity_ViewRangeThroughput tests iteration performance over large views.
func TestPlatformCapacity_ViewRangeThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping capacity test in short mode")
	}

	plat := NewPlatform("range-test", "RangeTest", nil, nil)
	subID := "range-sub"
	nodeCount := 100_000

	entries := make(map[node.Hash]*node.NodeEntry)
	for i := 0; i < nodeCount; i++ {
		raw := json.RawMessage(fmt.Sprintf(`{"id":"range-%d"}`, i))
		h := node.HashFromRawOptions(raw)
		ip := netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
		entries[h] = makeCapacityEntry(h, subID, ip)
	}

	plat.FullRebuild(
		func(fn func(node.Hash, *node.NodeEntry) bool) {
			for h, e := range entries {
				if !fn(h, e) {
					return
				}
			}
		},
		func(_ string, _ node.Hash) (string, bool, []string, bool) { return "", true, nil, true },
		func(_ netip.Addr) string { return "us" },
		func(_ netip.Addr) quality.Summary { return quality.Summary{} },
	)

	// Full scan
	start := time.Now()
	count := 0
	plat.View().Range(func(h node.Hash) bool {
		count++
		return true
	})
	elapsed := time.Since(start)

	t.Logf("Full scan of %d nodes completed in %v", count, elapsed)
	t.Logf("Throughput: %.0f nodes/sec", float64(count)/elapsed.Seconds())

	if count != nodeCount {
		t.Fatalf("expected to scan %d nodes, got %d", nodeCount, count)
	}
}
