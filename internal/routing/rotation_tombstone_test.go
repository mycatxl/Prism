package routing

import (
	"net/netip"
	"testing"
	"time"

	"prism/internal/platform"
)

func TestRotationTombstoneCache_BoundedAtDocumentedLimit(t *testing.T) {
	if rotationTombstoneMaxEntries != 100_000 {
		t.Fatalf("rotationTombstoneMaxEntries = %d, want 100000 (WP10 §3)", rotationTombstoneMaxEntries)
	}
	router := newTestRouter(newRouterTestPool(), nil)
	if got := router.rotationTombstones.maxEntries; got != rotationTombstoneMaxEntries {
		t.Fatalf("router tombstone capacity = %d, want %d", got, rotationTombstoneMaxEntries)
	}
}

func TestRotationTombstoneCache_EvictsLeastRecentlyUsed(t *testing.T) {
	cache := newRotationTombstoneCache(2)
	now := time.Now().UnixNano()
	ip := func(last byte) netip.Addr { return netip.AddrFrom4([4]byte{203, 0, 113, last}) }

	cache.store("k1", ip(1), now+int64(time.Hour))
	cache.store("k2", ip(2), now+int64(time.Hour))
	// Touch k1 so k2 becomes the least recently used entry.
	if got, ok := cache.lookup("k1", now); !ok || got != ip(1) {
		t.Fatalf("lookup k1 = (%v, %v), want (%v, true)", got, ok, ip(1))
	}
	cache.store("k3", ip(3), now+int64(time.Hour))

	if _, ok := cache.lookup("k2", now); ok {
		t.Fatal("least recently used tombstone k2 should have been evicted")
	}
	if got, ok := cache.lookup("k1", now); !ok || got != ip(1) {
		t.Fatalf("recently used tombstone k1 missing: (%v, %v)", got, ok)
	}
	if got, ok := cache.lookup("k3", now); !ok || got != ip(3) {
		t.Fatalf("newest tombstone k3 missing: (%v, %v)", got, ok)
	}
	if got := cache.len(); got != 2 {
		t.Fatalf("cache size = %d, want 2", got)
	}
}

func TestRotationTombstoneCache_ExpiresOnRead(t *testing.T) {
	cache := newRotationTombstoneCache(4)
	now := time.Now().UnixNano()
	target := netip.MustParseAddr("198.51.100.7")

	cache.store("k", target, now+int64(time.Minute))
	if got, ok := cache.lookup("k", now); !ok || got != target {
		t.Fatalf("live tombstone lookup = (%v, %v), want (%v, true)", got, ok, target)
	}
	if _, ok := cache.lookup("k", now+int64(time.Minute)); ok {
		t.Fatal("tombstone must be gone once its expiry is reached")
	}
	if got := cache.len(); got != 0 {
		t.Fatalf("expired tombstone should be dropped, size = %d", got)
	}
}

func TestRotationTombstoneTTL_UsesMaxOfIntervalAndStickyTTL(t *testing.T) {
	if got := rotationTombstoneTTL(30*time.Minute, int64(time.Hour)); got != time.Hour {
		t.Fatalf("ttl = %v, want 1h (sticky ttl wins)", got)
	}
	if got := rotationTombstoneTTL(2*time.Hour, int64(time.Hour)); got != 2*time.Hour {
		t.Fatalf("ttl = %v, want 2h (interval wins)", got)
	}
	if got := rotationTombstoneTTL(30*time.Minute, 0); got != 30*time.Minute {
		t.Fatalf("ttl = %v, want 30m when sticky ttl is unset", got)
	}
}

// rotationFixture builds a platform with scheduled rotation + avoidance enabled
// and one routable node per egress IP.
func rotationFixture(t *testing.T, egressIPs ...string) (*routerTestPool, *Router, *platform.Platform, *PlatformRoutingState) {
	t.Helper()

	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-rotation", "PlatRotation", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	plat.ScheduledRotationEnabled = true
	plat.ScheduledRotationIntervalNs = int64(10 * time.Millisecond)
	plat.RotationAvoidPreviousIP = true
	pool.addPlatform(plat)

	for i, ip := range egressIPs {
		raw := `{"id":"rotate-` + string(rune('a'+i)) + `"}`
		h, e := newRoutableEntry(t, raw, ip)
		pool.addEntry(h, e)
	}
	pool.rebuildPlatformView(plat)

	router := newTestRouter(pool, nil)
	state := router.ensurePlatformState(plat.ID)
	return pool, router, plat, state
}

// ageLease backdates the account's lease so the next sweep regards it as
// eligible for scheduled rotation.
func ageLease(t *testing.T, state *PlatformRoutingState, account string, age time.Duration) {
	t.Helper()

	lease, ok := state.Leases.GetLease(account)
	if !ok {
		t.Fatalf("account %q has no lease to age", account)
	}
	now := time.Now()
	lease.CreatedAtNs = now.Add(-age).UnixNano()
	lease.LastAccessedNs = lease.CreatedAtNs
	state.Leases.CreateLease(account, lease)
}

func TestScheduledRotation_ConsecutiveRotationsChangeEgressIP(t *testing.T) {
	pool, router, plat, state := rotationFixture(t, "203.0.113.1", "203.0.113.2", "203.0.113.3")

	rotator := newScheduledRotatorWithIntervals(router, pool, 30*time.Millisecond, 0)
	account := "acct-rotate"

	previousIP := netip.Addr{}
	for round := 0; round < 10; round++ {
		res, err := router.RouteRequest(plat.Name, account, "https://example.com/rotate")
		if err != nil {
			t.Fatalf("round %d: RouteRequest: %v", round, err)
		}
		if previousIP.IsValid() && res.EgressIP == previousIP {
			t.Fatalf("round %d: egress IP %s repeated despite rotation avoidance", round, res.EgressIP)
		}
		previousIP = res.EgressIP

		// Age the lease, then rotate it with a real sweep so the tombstone is
		// written by the scheduled rotator (not by the test).
		ageLease(t, state, account, time.Minute)
		rotator.sweep()

		if _, ok := state.Leases.GetLease(account); ok {
			t.Fatalf("round %d: lease should have been rotated away", round)
		}
		tombstone, ok := router.RotationTombstone(plat.ID, account)
		if !ok {
			t.Fatalf("round %d: rotation must record a tombstone", round)
		}
		if tombstone != previousIP {
			t.Fatalf("round %d: tombstone = %s, want %s", round, tombstone, previousIP)
		}
	}
}

func TestScheduledRotation_SingleEgressIPFallsBackAndRecordsEvent(t *testing.T) {
	pool, router, plat, state := rotationFixture(t, "198.51.100.42")

	rotator := newScheduledRotatorWithIntervals(router, pool, 30*time.Millisecond, 0)
	account := "acct-single"

	first, err := router.RouteRequest(plat.Name, account, "https://example.com/single")
	if err != nil {
		t.Fatalf("first RouteRequest: %v", err)
	}
	if len(first.Events) != 0 {
		t.Fatalf("unexpected first-request events: %v", first.Events)
	}
	singleIP := first.EgressIP

	ageLease(t, state, account, time.Minute)
	rotator.sweep()

	tombstone, ok := router.RotationTombstone(plat.ID, account)
	if !ok || tombstone != singleIP {
		t.Fatalf("tombstone = (%v, %v), want (%v, true)", tombstone, ok, singleIP)
	}

	second, err := router.RouteRequest(plat.Name, account, "https://example.com/single")
	if err != nil {
		t.Fatalf("second RouteRequest: %v", err)
	}
	if second.EgressIP != singleIP {
		t.Fatalf("only one egress IP exists, got %s want %s", second.EgressIP, singleIP)
	}
	if !second.LeaseCreated {
		t.Fatal("expected a fresh lease after rotation")
	}
	if len(second.Events) != 1 || second.Events[0] != RouteEventRotationFallbackSameIP {
		t.Fatalf("events = %v, want [%s]", second.Events, RouteEventRotationFallbackSameIP)
	}
}

func TestRouteRequest_RotationAvoidanceDisabledIgnoresTombstone(t *testing.T) {
	_, router, plat, _ := rotationFixture(t, "203.0.113.9")
	plat.RotationAvoidPreviousIP = false

	// Seed a tombstone for the only egress IP: with avoidance disabled it must
	// not influence the pick and must not produce a fallback event.
	router.recordRotationTombstone(plat.ID, "acct-avoid-off", netip.MustParseAddr("203.0.113.9"), time.Now().UnixNano(), time.Hour)

	res, err := router.RouteRequest(plat.Name, "acct-avoid-off", "https://example.com/off")
	if err != nil {
		t.Fatalf("RouteRequest: %v", err)
	}
	if res.EgressIP != netip.MustParseAddr("203.0.113.9") {
		t.Fatalf("egress IP = %s, want the only available IP", res.EgressIP)
	}
	if len(res.Events) != 0 {
		t.Fatalf("events = %v, want none", res.Events)
	}
}

func TestRouterRotateLease_RemovesLeaseAndRecordsTombstone(t *testing.T) {
	_, router, plat, state := rotationFixture(t, "203.0.113.11")

	first, err := router.RouteRequest(plat.Name, "acct-manual", "https://example.com/manual")
	if err != nil {
		t.Fatalf("RouteRequest: %v", err)
	}
	lease, ok := state.Leases.GetLease("acct-manual")
	if !ok {
		t.Fatal("expected lease before manual rotation")
	}

	if !router.RotateLease(plat, "acct-manual") {
		t.Fatal("RotateLease = false, want true")
	}
	if _, ok := state.Leases.GetLease("acct-manual"); ok {
		t.Fatal("manual rotation must delete the lease")
	}
	tombstone, ok := router.RotationTombstone(plat.ID, "acct-manual")
	if !ok || tombstone != lease.EgressIP {
		t.Fatalf("tombstone = (%v, %v), want (%v, true)", tombstone, ok, lease.EgressIP)
	}
	if router.RotateLease(plat, "acct-manual") {
		t.Fatal("second RotateLease for the same account must report no lease")
	}

	next, err := router.RouteRequest(plat.Name, "acct-manual", "https://example.com/manual")
	if err != nil {
		t.Fatalf("RouteRequest after rotation: %v", err)
	}
	if next.EgressIP != first.EgressIP {
		t.Fatalf("single-node platform must fall back to %s, got %s", first.EgressIP, next.EgressIP)
	}
	if len(next.Events) != 1 || next.Events[0] != RouteEventRotationFallbackSameIP {
		t.Fatalf("events = %v, want [%s]", next.Events, RouteEventRotationFallbackSameIP)
	}
	if !next.LeaseCreated {
		t.Fatal("expected a fresh lease after manual rotation")
	}
}
