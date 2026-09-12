package routing

import (
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/node"
	"prism/internal/platform"
)

func TestScheduledRotator_Basic(t *testing.T) {
	pool := newRouterTestPool()
	router := NewRouter(RouterConfig{
		Pool:       pool,
		Authorities: func() []string { return []string{"example.com"} },
		P2CWindow:  func() time.Duration { return 10 * time.Second },
	})

	// Create a platform with scheduled rotation enabled
	plat := platform.NewPlatform("test-plat", "TestPlatform", nil, nil)
	plat.ScheduledRotationEnabled = true
	plat.ScheduledRotationIntervalNs = int64(100 * time.Millisecond)
	plat.StickyTTLNs = int64(1 * time.Hour)
	pool.addPlatform(plat)

	// Add two test nodes
	h1, e1 := newRoutableEntry(t, `{"type":"ss","server":"1.1.1.1"}`, "1.1.1.1")
	h2, e2 := newRoutableEntry(t, `{"type":"ss","server":"1.1.1.2"}`, "1.1.1.2")
	pool.addEntry(h1, e1)
	pool.addEntry(h2, e2)
	pool.rebuildPlatformView(plat)

	// Create two leases with old timestamps
	now := time.Now()
	oldCreatedAtNs := now.Add(-200 * time.Millisecond).UnixNano()

	state := router.ensurePlatformState("test-plat")
	state.Leases.CreateLease("user1", Lease{
		NodeHash:       h1,
		EgressIP:       netip.MustParseAddr("1.1.1.1"),
		CreatedAtNs:    oldCreatedAtNs,
		ExpiryNs:       now.Add(1 * time.Hour).UnixNano(),
		LastAccessedNs: oldCreatedAtNs,
	})
	state.Leases.CreateLease("user2", Lease{
		NodeHash:       h2,
		EgressIP:       netip.MustParseAddr("1.1.1.2"),
		CreatedAtNs:    oldCreatedAtNs,
		ExpiryNs:       now.Add(1 * time.Hour).UnixNano(),
		LastAccessedNs: oldCreatedAtNs,
	})

	// Verify leases exist
	if _, ok := state.Leases.GetLease("user1"); !ok {
		t.Fatal("user1 lease not found before rotation")
	}
	if _, ok := state.Leases.GetLease("user2"); !ok {
		t.Fatal("user2 lease not found before rotation")
	}

	// Create rotator with short intervals
	rotator := newScheduledRotatorWithIntervals(router, pool, 50*time.Millisecond, 0)

	var sweepCount atomic.Int32
	rotator.sweepHook = func() {
		sweepCount.Add(1)
	}

	rotator.Start()
	defer rotator.Stop()

	// Wait for at least one sweep
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if sweepCount.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if sweepCount.Load() == 0 {
		t.Fatal("No sweeps occurred")
	}

	// Verify leases were rotated (deleted)
	if _, ok := state.Leases.GetLease("user1"); ok {
		t.Error("user1 lease should have been rotated")
	}
	if _, ok := state.Leases.GetLease("user2"); ok {
		t.Error("user2 lease should have been rotated")
	}
}

func TestScheduledRotator_NoRotationWhenDisabled(t *testing.T) {
	pool := newRouterTestPool()
	router := NewRouter(RouterConfig{
		Pool:       pool,
		Authorities: func() []string { return []string{"example.com"} },
		P2CWindow:  func() time.Duration { return 10 * time.Second },
	})

	// Create a platform with scheduled rotation disabled
	plat := platform.NewPlatform("test-plat", "TestPlatform", nil, nil)
	plat.ScheduledRotationEnabled = false
	plat.ScheduledRotationIntervalNs = int64(50 * time.Millisecond)
	plat.StickyTTLNs = int64(1 * time.Hour)
	pool.addPlatform(plat)

	h1, e1 := newRoutableEntry(t, `{"type":"ss","server":"1.1.1.1"}`, "1.1.1.1")
	pool.addEntry(h1, e1)
	pool.rebuildPlatformView(plat)

	now := time.Now()
	oldCreatedAtNs := now.Add(-200 * time.Millisecond).UnixNano()

	state := router.ensurePlatformState("test-plat")
	state.Leases.CreateLease("user1", Lease{
		NodeHash:       h1,
		EgressIP:       netip.MustParseAddr("1.1.1.1"),
		CreatedAtNs:    oldCreatedAtNs,
		ExpiryNs:       now.Add(1 * time.Hour).UnixNano(),
		LastAccessedNs: oldCreatedAtNs,
	})

	rotator := newScheduledRotatorWithIntervals(router, pool, 30*time.Millisecond, 0)
	rotator.Start()
	defer rotator.Stop()

	// Wait for multiple sweep cycles
	time.Sleep(150 * time.Millisecond)

	// Verify lease was NOT rotated
	if _, ok := state.Leases.GetLease("user1"); !ok {
		t.Error("user1 lease should NOT have been rotated (rotation disabled)")
	}
}

func TestScheduledRotator_OnlyRotatesOldLeases(t *testing.T) {
	pool := newRouterTestPool()
	router := NewRouter(RouterConfig{
		Pool:       pool,
		Authorities: func() []string { return []string{"example.com"} },
		P2CWindow:  func() time.Duration { return 10 * time.Second },
	})

	plat := platform.NewPlatform("test-plat", "TestPlatform", nil, nil)
	plat.ScheduledRotationEnabled = true
	plat.ScheduledRotationIntervalNs = int64(100 * time.Millisecond)
	plat.StickyTTLNs = int64(1 * time.Hour)
	pool.addPlatform(plat)

	h1, e1 := newRoutableEntry(t, `{"type":"ss","server":"1.1.1.1"}`, "1.1.1.1")
	h2, e2 := newRoutableEntry(t, `{"type":"ss","server":"1.1.1.2"}`, "1.1.1.2")
	pool.addEntry(h1, e1)
	pool.addEntry(h2, e2)
	pool.rebuildPlatformView(plat)

	now := time.Now()
	state := router.ensurePlatformState("test-plat")

	// Old lease (should be rotated)
	state.Leases.CreateLease("old-user", Lease{
		NodeHash:       h1,
		EgressIP:       netip.MustParseAddr("1.1.1.1"),
		CreatedAtNs:    now.Add(-200 * time.Millisecond).UnixNano(),
		ExpiryNs:       now.Add(1 * time.Hour).UnixNano(),
		LastAccessedNs: now.Add(-200 * time.Millisecond).UnixNano(),
	})

	// New lease (should NOT be rotated)
	state.Leases.CreateLease("new-user", Lease{
		NodeHash:       h2,
		EgressIP:       netip.MustParseAddr("1.1.1.2"),
		CreatedAtNs:    now.UnixNano(),
		ExpiryNs:       now.Add(1 * time.Hour).UnixNano(),
		LastAccessedNs: now.UnixNano(),
	})

	rotator := newScheduledRotatorWithIntervals(router, pool, 30*time.Millisecond, 0)
	var sweepCount atomic.Int32
	rotator.sweepHook = func() {
		sweepCount.Add(1)
	}

	rotator.Start()
	defer rotator.Stop()

	// Wait for sweep
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if sweepCount.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Verify old lease was rotated
	if _, ok := state.Leases.GetLease("old-user"); ok {
		t.Error("old-user lease should have been rotated")
	}

	// Verify new lease was NOT rotated
	if _, ok := state.Leases.GetLease("new-user"); !ok {
		t.Error("new-user lease should NOT have been rotated")
	}
}

func TestScheduledRotator_StopGracefully(t *testing.T) {
	pool := newRouterTestPool()
	router := NewRouter(RouterConfig{
		Pool:       pool,
		Authorities: func() []string { return []string{"example.com"} },
		P2CWindow:  func() time.Duration { return 10 * time.Second },
	})

	rotator := newScheduledRotatorWithIntervals(router, pool, 10*time.Millisecond, 0)
	rotator.Start()

	// Let it run for a bit
	time.Sleep(50 * time.Millisecond)

	// Stop should complete quickly
	done := make(chan struct{})
	go func() {
		rotator.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("Stop() did not complete in time")
	}

	// Multiple Stop() calls should be safe
	rotator.Stop()
	rotator.Stop()
}

func TestScheduledRotator_ParallelPlatformProcessing(t *testing.T) {
	pool := newRouterTestPool()
	router := NewRouter(RouterConfig{
		Pool:       pool,
		Authorities: func() []string { return []string{"example.com"} },
		P2CWindow:  func() time.Duration { return 10 * time.Second },
	})

	// Create multiple platforms
	numPlatforms := 5
	for i := 0; i < numPlatforms; i++ {
		platID := fmt.Sprintf("plat-%d", i)
		platName := fmt.Sprintf("Platform%d", i)
		plat := platform.NewPlatformWithTagFilter(
			platID,
			platName,
			node.TagFilter{},
			nil,
		)
		plat.ScheduledRotationEnabled = true
		plat.ScheduledRotationIntervalNs = int64(50 * time.Millisecond)
		plat.StickyTTLNs = int64(1 * time.Hour)
		pool.addPlatform(plat)

		// Add a node and lease for each platform
		nodeIP := fmt.Sprintf("1.1.1.%d", i+1)
		nodeRaw := fmt.Sprintf(`{"type":"ss","server":"%s"}`, nodeIP)
		h, e := newRoutableEntry(t, nodeRaw, nodeIP)
		pool.addEntry(h, e)
		pool.rebuildPlatformView(plat)

		state := router.ensurePlatformState(plat.ID)
		state.Leases.CreateLease("user", Lease{
			NodeHash:       h,
			EgressIP:       netip.MustParseAddr(nodeIP),
			CreatedAtNs:    time.Now().Add(-100 * time.Millisecond).UnixNano(),
			ExpiryNs:       time.Now().Add(1 * time.Hour).UnixNano(),
			LastAccessedNs: time.Now().Add(-100 * time.Millisecond).UnixNano(),
		})
	}

	rotator := newScheduledRotatorWithIntervals(router, pool, 30*time.Millisecond, 0)

	var processedPlatforms sync.Map
	rotator.sweepHook = func() {
		// Track which platforms get processed
		pool.RangePlatforms(func(plat *platform.Platform) bool {
			if plat.ScheduledRotationEnabled {
				processedPlatforms.Store(plat.ID, true)
			}
			return true
		})
	}

	rotator.Start()
	defer rotator.Stop()

	// Wait for sweep
	time.Sleep(150 * time.Millisecond)

	// Verify all platforms were processed
	processedCount := 0
	processedPlatforms.Range(func(_, _ interface{}) bool {
		processedCount++
		return true
	})

	if processedCount != numPlatforms {
		t.Errorf("Expected %d platforms processed, got %d", numPlatforms, processedCount)
	}

	// Verify all leases were rotated
	for i := 0; i < numPlatforms; i++ {
		platID := fmt.Sprintf("plat-%d", i)
		state, ok := router.states.Load(platID)
		if !ok {
			t.Errorf("Platform %d state not found", i)
			continue
		}
		if _, ok := state.Leases.GetLease("user"); ok {
			t.Errorf("Platform %d lease should have been rotated", i)
		}
	}
}
