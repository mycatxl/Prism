package routing

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"runtime"
	"sync"
	"testing"
	"time"

	"prism/internal/node"
	"prism/internal/platform"
	"prism/internal/testutil"
)

// TestRouterCapacity_ConcurrentRouteRequests tests routing throughput under load.
func TestRouterCapacity_ConcurrentRouteRequests(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping capacity test in short mode")
	}

	pool := newRouterTestPool()
	plat := platform.NewPlatform("cap-route", "CapRoute", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	pool.addPlatform(plat)

	nodeCount := 10_000
	for i := 0; i < nodeCount; i++ {
		raw := json.RawMessage(fmt.Sprintf(`{"id":"route-cap-%d"}`, i))
		h := node.HashFromRawOptions(raw)
		ip := netip.AddrFrom4([4]byte{10, byte(i>>16), byte(i>>8), byte(i)})
		e := node.NewNodeEntry(h, raw, time.Now(), 16)
		e.AddSubscriptionID("cap-sub")
		e.LatencyTable.Update("cloudflare.com", 100*time.Millisecond, 10*time.Minute)
		ob := testutil.NewNoopOutbound()
		e.Outbound.Store(&ob)
		e.SetEgressIP(ip)
		pool.addEntry(h, e)
	}
	pool.rebuildPlatformView(plat)

	if plat.View().Size() != nodeCount {
		t.Fatalf("setup: expected %d nodes in view, got %d", nodeCount, plat.View().Size())
	}

	router := newTestRouter(pool, nil)

	tests := []struct {
		name        string
		concurrency int
		requests    int
	}{
		{"10_concurrent", 10, 10_000},
		{"100_concurrent", 100, 50_000},
		{"1000_concurrent", 1000, 100_000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var m0, m1 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m0)

			var wg sync.WaitGroup
			errCh := make(chan error, tc.requests)
			start := time.Now()

			requestsPerWorker := tc.requests / tc.concurrency
			for w := 0; w < tc.concurrency; w++ {
				wg.Add(1)
				go func(workerID int) {
					defer wg.Done()
					for i := 0; i < requestsPerWorker; i++ {
						account := fmt.Sprintf("acct-%d-%d", workerID, i)
						_, err := router.RouteRequest(plat.Name, account, "https://example.com/test")
						if err != nil {
							errCh <- err
							return
						}
					}
				}(w)
			}

			wg.Wait()
			close(errCh)
			elapsed := time.Since(start)

			runtime.GC()
			runtime.ReadMemStats(&m1)

			var firstErr error
			for err := range errCh {
				if firstErr == nil {
					firstErr = err
				}
			}

			if firstErr != nil {
				t.Fatalf("routing error: %v", firstErr)
			}

			allocMB := float64(m1.Alloc-m0.Alloc) / 1024 / 1024
			throughput := float64(tc.requests) / elapsed.Seconds()
			avgLatency := elapsed / time.Duration(tc.requests)

			t.Logf("Processed %d requests with %d concurrent workers in %v", tc.requests, tc.concurrency, elapsed)
			t.Logf("Throughput: %.0f req/sec", throughput)
			t.Logf("Average latency: %v per request", avgLatency)
			t.Logf("Memory: Alloc=%.2fMB", allocMB)

			state, ok := router.states.Load(plat.ID)
			if !ok {
				t.Fatal("expected routing state to exist")
			}
			leaseCount := 0
			state.Leases.Range(func(_ string, _ Lease) bool {
				leaseCount++
				return true
			})
			t.Logf("Created %d leases", leaseCount)
		})
	}
}

// TestRouterCapacity_LeaseManagement tests lease lifecycle operations at scale.
func TestRouterCapacity_LeaseManagement(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping capacity test in short mode")
	}

	pool := newRouterTestPool()
	plat := platform.NewPlatform("lease-cap", "LeaseCap", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	pool.addPlatform(plat)

	// Setup 5k nodes
	nodeCount := 5_000
	for i := 0; i < nodeCount; i++ {
		raw := json.RawMessage(fmt.Sprintf(`{"id":"lease-cap-%d"}`, i))
		h := node.HashFromRawOptions(raw)
		ip := netip.AddrFrom4([4]byte{10, byte(i>>8), byte(i), 1})
		e := node.NewNodeEntry(h, raw, time.Now(), 16)
		e.AddSubscriptionID("lease-sub")
		e.LatencyTable.Update("cloudflare.com", 100*time.Millisecond, 10*time.Minute)
		ob := testutil.NewNoopOutbound()
		e.Outbound.Store(&ob)
		e.SetEgressIP(ip)
		pool.addEntry(h, e)
	}
	pool.rebuildPlatformView(plat)

	router := newTestRouter(pool, nil)

	// Create 50k leases
	leaseCount := 50_000
	t.Logf("Creating %d leases...", leaseCount)
	start := time.Now()
	for i := 0; i < leaseCount; i++ {
		account := fmt.Sprintf("lease-user-%d", i)
		_, err := router.RouteRequest(plat.Name, account, "https://example.com/lease")
		if err != nil {
			t.Fatalf("failed to create lease %d: %v", i, err)
		}
	}
	createElapsed := time.Since(start)
	t.Logf("Created %d leases in %v (%.0f/sec)", leaseCount, createElapsed, float64(leaseCount)/createElapsed.Seconds())

	// Verify lease count
	state, ok := router.states.Load(plat.ID)
	if !ok {
		t.Fatal("expected routing state")
	}
	actualCount := 0
	state.Leases.Range(func(_ string, _ Lease) bool {
		actualCount++
		return true
	})
	t.Logf("Verified %d active leases", actualCount)

	// Test lookup performance
	t.Log("Testing lease lookup performance...")
	lookupStart := time.Now()
	lookupCount := 10_000
	for i := 0; i < lookupCount; i++ {
		account := fmt.Sprintf("lease-user-%d", i)
		_, _ = state.Leases.GetLease(account)
	}
	lookupElapsed := time.Since(lookupStart)
	t.Logf("Completed %d lookups in %v (%.0f/sec)", lookupCount, lookupElapsed, float64(lookupCount)/lookupElapsed.Seconds())

	// Test deletion performance
	t.Log("Testing lease deletion performance...")
	deleteStart := time.Now()
	deleteCount := 10_000
	for i := 0; i < deleteCount; i++ {
		account := fmt.Sprintf("lease-user-%d", i)
		router.DeleteLease(plat.ID, account)
	}
	deleteElapsed := time.Since(deleteStart)
	t.Logf("Deleted %d leases in %v (%.0f/sec)", deleteCount, deleteElapsed, float64(deleteCount)/deleteElapsed.Seconds())

	remainingCount := 0
	state.Leases.Range(func(_ string, _ Lease) bool {
		remainingCount++
		return true
	})
	expectedRemaining := leaseCount - deleteCount
	if remainingCount != expectedRemaining {
		t.Fatalf("expected %d remaining leases, got %d", expectedRemaining, remainingCount)
	}
}

// TestRouterCapacity_IPLoadTracking tests IP load statistics performance.
func TestRouterCapacity_IPLoadTracking(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping capacity test in short mode")
	}

	pool := newRouterTestPool()
	plat := platform.NewPlatform("ipload-cap", "IPLoadCap", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	pool.addPlatform(plat)

	// 1k unique IPs, each with multiple nodes
	uniqueIPs := 1_000
	nodesPerIP := 10
	for i := 0; i < uniqueIPs; i++ {
		ip := netip.AddrFrom4([4]byte{10, byte(i>>8), byte(i), 1})
		for j := 0; j < nodesPerIP; j++ {
			raw := json.RawMessage(fmt.Sprintf(`{"id":"ipload-%d-%d"}`, i, j))
			h := node.HashFromRawOptions(raw)
			e := node.NewNodeEntry(h, raw, time.Now(), 16)
			e.AddSubscriptionID("ipload-sub")
			e.LatencyTable.Update("cloudflare.com", 100*time.Millisecond, 10*time.Minute)
			ob := testutil.NewNoopOutbound()
			e.Outbound.Store(&ob)
			e.SetEgressIP(ip)
			pool.addEntry(h, e)
		}
	}
	pool.rebuildPlatformView(plat)

	router := newTestRouter(pool, nil)

	// Create leases to build up IP load stats
	leaseCount := 20_000
	t.Logf("Creating %d leases across %d unique IPs...", leaseCount, uniqueIPs)
	start := time.Now()
	for i := 0; i < leaseCount; i++ {
		account := fmt.Sprintf("ipload-user-%d", i)
		_, err := router.RouteRequest(plat.Name, account, "https://example.com/ipload")
		if err != nil {
			t.Fatalf("failed to create lease: %v", err)
		}
	}
	elapsed := time.Since(start)
	t.Logf("Created %d leases in %v", leaseCount, elapsed)

	// Snapshot IP load stats
	snapshotStart := time.Now()
	snapshot := router.SnapshotIPLoad(plat.ID)
	snapshotElapsed := time.Since(snapshotStart)

	t.Logf("Snapshot captured %d unique IPs in %v", len(snapshot), snapshotElapsed)

	totalLoad := int64(0)
	for _, count := range snapshot {
		totalLoad += count
	}
	t.Logf("Total load across all IPs: %d", totalLoad)

	if totalLoad != int64(leaseCount) {
		t.Fatalf("expected total load %d, got %d", leaseCount, totalLoad)
	}
}
