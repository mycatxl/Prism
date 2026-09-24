package routing

import (
	"runtime"
	"sync"
	"time"

	"prism/internal/platform"
	"prism/internal/scanloop"
)

// ScheduledRotator periodically rotates leases for platforms with scheduled rotation enabled.
type ScheduledRotator struct {
	router      *Router
	pool        PoolAccessor
	stopCh      chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
	minInterval time.Duration
	jitterRange time.Duration

	// test hook: called after each sweep completes (used as a barrier).
	sweepHook func()
}

func NewScheduledRotator(router *Router, pool PoolAccessor) *ScheduledRotator {
	return newScheduledRotatorWithIntervals(router, pool, 7*time.Second, 3*time.Second)
}

func newScheduledRotatorWithIntervals(router *Router, pool PoolAccessor, minInterval, jitterRange time.Duration) *ScheduledRotator {
	return &ScheduledRotator{
		router:      router,
		pool:        pool,
		stopCh:      make(chan struct{}),
		minInterval: minInterval,
		jitterRange: jitterRange,
	}
}

func (r *ScheduledRotator) Start() {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		scanloop.Run(r.stopCh, r.minInterval, r.jitterRange, r.sweep)
	}()
}

func (r *ScheduledRotator) Stop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
	r.wg.Wait()
}

func (r *ScheduledRotator) sweep() {
	// The hook signals sweep completion: it runs after all per-platform
	// rotations below have finished, so tests can use it as a barrier.
	if r.sweepHook != nil {
		defer r.sweepHook()
	}

	now := time.Now()
	nowNs := now.UnixNano()

	// Collect platforms with scheduled rotation enabled
	type platformInfo struct {
		platID      string
		interval    time.Duration
		stickyTTLNs int64
		state       *PlatformRoutingState
	}
	platforms := make([]platformInfo, 0)

	r.pool.RangePlatforms(func(plat *platform.Platform) bool {
		select {
		case <-r.stopCh:
			return false
		default:
		}

		if !plat.ScheduledRotationEnabled {
			return true
		}

		interval := time.Duration(plat.ScheduledRotationIntervalNs)
		if interval <= 0 {
			return true
		}

		state, ok := r.router.states.Load(plat.ID)
		if !ok || state == nil {
			return true
		}

		platforms = append(platforms, platformInfo{
			platID:      plat.ID,
			interval:    interval,
			stickyTTLNs: plat.StickyTTLNs,
			state:       state,
		})
		return true
	})

	if len(platforms) == 0 {
		return
	}

	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > len(platforms) {
		workers = len(platforms)
	}

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, info := range platforms {
		select {
		case <-r.stopCh:
			wg.Wait()
			return
		default:
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(platID string, interval time.Duration, stickyTTLNs int64, state *PlatformRoutingState) {
			defer wg.Done()
			defer func() { <-sem }()
			r.rotatePlatformLeases(platID, interval, stickyTTLNs, state, nowNs)
		}(info.platID, info.interval, info.stickyTTLNs, info.state)
	}
	wg.Wait()
}

func (r *ScheduledRotator) rotatePlatformLeases(platID string, interval time.Duration, stickyTTLNs int64, state *PlatformRoutingState, nowNs int64) {
	intervalNs := int64(interval)

	// Collect accounts that need rotation
	candidates := make([]string, 0)

	state.Leases.Range(func(account string, lease Lease) bool {
		select {
		case <-r.stopCh:
			return false
		default:
		}

		// Check if lease age exceeds rotation interval
		if nowNs-lease.CreatedAtNs >= intervalNs {
			candidates = append(candidates, account)
		}
		return true
	})

	// Rotate collected candidates.
	// Re-check the lease age atomically before deleting: between the scan above
	// and this point a concurrent request may have replaced the lease, and
	// deleting that fresh lease would rotate an account twice.
	floorNs := nowNs - intervalNs
	tombstoneTTL := rotationTombstoneTTL(interval, stickyTTLNs)
	for _, account := range candidates {
		select {
		case <-r.stopCh:
			return
		default:
		}

		// Delete the lease to trigger re-routing on next request and remember
		// its egress IP so the next lease avoids it (WP10 §3).
		if lease, deleted := r.router.DeleteLeaseIfOlderThan(platID, account, floorNs); deleted {
			r.router.recordRotationTombstone(platID, account, lease.EgressIP, nowNs, tombstoneTTL)
		}
	}
}
