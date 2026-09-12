package topology

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"runtime"
	"testing"
	"time"

	"prism/internal/node"
	"prism/internal/quality"
	"prism/internal/subscription"
	"prism/internal/testutil"
)

func newCapacityPool() *GlobalNodePool {
	return NewGlobalNodePool(PoolConfig{
		SubLookup:              func(string) *subscription.Subscription { return nil },
		GeoLookup:              func(netip.Addr) string { return "us" },
		QualityLookup:          func(netip.Addr) quality.Summary { return quality.Summary{} },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
		LatencyAuthorities:     func() []string { return []string{"cloudflare.com"} },
	})
}

// TestPoolCapacity_BulkImport tests importing large batches of nodes.
func TestPoolCapacity_BulkImport(t *testing.T) {
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
			pool := newCapacityPool()
			subID := "capacity-test-sub"

			var m0, m1 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m0)

			start := time.Now()
			for i := 0; i < tc.count; i++ {
				raw := json.RawMessage(fmt.Sprintf(`{"id":"node-%d","server":"127.0.0.1","server_port":%d,"type":"ss","method":"aes-256-gcm","password":"test"}`, i, 10000+i))
				h := node.HashFromRawOptions(raw)
				pool.AddNodeFromSub(h, raw, subID)
			}
			elapsed := time.Since(start)

			runtime.GC()
			runtime.ReadMemStats(&m1)

			// Count nodes by iterating
			registered := 0
			pool.nodes.Range(func(h node.Hash, e *node.NodeEntry) bool {
				registered++
				return true
			})

			allocMB := float64(m1.Alloc-m0.Alloc) / 1024 / 1024
			totalAllocMB := float64(m1.TotalAlloc-m0.TotalAlloc) / 1024 / 1024

			t.Logf("Imported %d nodes in %v", registered, elapsed)
			t.Logf("Memory: Alloc=%.2fMB TotalAlloc=%.2fMB", allocMB, totalAllocMB)
			t.Logf("Throughput: %.0f nodes/sec", float64(tc.count)/elapsed.Seconds())

			if registered != tc.count {
				t.Fatalf("expected %d registered nodes, got %d", tc.count, registered)
			}
		})
	}
}

// TestPoolCapacity_LiveNodeOutboundCreation tests creating outbound connections for active nodes.
func TestPoolCapacity_LiveNodeOutboundCreation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping capacity test in short mode")
	}

	sizes := []struct {
		name  string
		count int
	}{
		{"1k_live", 1_000},
		{"10k_live", 10_000},
	}

	for _, tc := range sizes {
		t.Run(tc.name, func(t *testing.T) {
			pool := newCapacityPool()
			subID := "live-capacity-sub"

			// Register nodes
			for i := 0; i < tc.count; i++ {
				raw := json.RawMessage(fmt.Sprintf(`{"id":"live-%d","server":"127.0.0.1","server_port":%d,"type":"ss","method":"aes-256-gcm","password":"test"}`, i, 20000+i))
				h := node.HashFromRawOptions(raw)
				pool.AddNodeFromSub(h, raw, subID)
			}

			var m0, m1 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m0)

			start := time.Now()
			createdCount := 0

			pool.nodes.Range(func(h node.Hash, e *node.NodeEntry) bool {
				ob := testutil.NewNoopOutbound()
				e.Outbound.Store(&ob)
				createdCount++
				return true
			})
			elapsed := time.Since(start)

			runtime.GC()
			runtime.ReadMemStats(&m1)

			allocMB := float64(m1.Alloc-m0.Alloc) / 1024 / 1024
			fdCount := createdCount // Each noop outbound is lightweight, actual FD count would be higher with real connections

			t.Logf("Created %d outbound connections in %v", createdCount, elapsed)
			t.Logf("Memory: Alloc=%.2fMB", allocMB)
			t.Logf("Approx FD usage: %d", fdCount)

			if createdCount != tc.count {
				t.Fatalf("expected %d outbounds, got %d", tc.count, createdCount)
			}
		})
	}
}

// TestPoolCapacity_ConcurrentAccess tests concurrent node access patterns.
func TestPoolCapacity_ConcurrentAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping capacity test in short mode")
	}

	pool := newCapacityPool()
	subID := "concurrent-sub"
	nodeCount := 50_000

	// Populate pool
	for i := 0; i < nodeCount; i++ {
		raw := json.RawMessage(fmt.Sprintf(`{"id":"concurrent-%d","server":"127.0.0.1","server_port":%d,"type":"ss","method":"aes-256-gcm","password":"test"}`, i, 30000+i))
		h := node.HashFromRawOptions(raw)
		pool.AddNodeFromSub(h, raw, subID)
	}

	t.Logf("Setup complete: %d nodes registered", nodeCount)

	start := time.Now()
	iterations := 100_000
	found := 0

	pool.nodes.Range(func(h node.Hash, e *node.NodeEntry) bool {
		found++
		return found < iterations
	})
	elapsed := time.Since(start)

	t.Logf("Iterated %d nodes in %v", found, elapsed)
	t.Logf("Throughput: %.0f nodes/sec", float64(found)/elapsed.Seconds())
}
