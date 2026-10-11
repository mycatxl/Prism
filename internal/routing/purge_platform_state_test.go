package routing

import (
	"net/netip"
	"testing"
	"time"

	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/platform"
)

// TestPurgePlatformState_RemovesStateAndEmitsRemoveEvents covers
// : a deleted platform used to keep its whole PlatformRoutingState (lease
// table + per-IP load counters) alive in Router.states for the lifetime of the
// process, and every LeaseCleaner sweep had to walk it.
func TestPurgePlatformState_RemovesStateAndEmitsRemoveEvents(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-purge", "Plat-Purge", nil, nil)
	pool.addPlatform(plat)

	var events []LeaseEvent
	router := newTestRouter(pool, func(e LeaseEvent) {
		events = append(events, e)
	})

	now := time.Now().UnixNano()
	leases := []model.Lease{
		{
			PlatformID:     plat.ID,
			Account:        "alice",
			NodeHash:       node.HashFromRawOptions([]byte(`{"id":"purge-a"}`)).Hex(),
			EgressIP:       "203.0.113.10",
			CreatedAtNs:    now - int64(time.Minute),
			ExpiryNs:       now + int64(time.Hour),
			LastAccessedNs: now,
		},
		{
			PlatformID:     plat.ID,
			Account:        "bob",
			NodeHash:       node.HashFromRawOptions([]byte(`{"id":"purge-b"}`)).Hex(),
			EgressIP:       "203.0.113.11",
			CreatedAtNs:    now - int64(2*time.Minute),
			ExpiryNs:       now + int64(time.Hour),
			LastAccessedNs: now,
		},
	}
	router.RestoreLeases(leases)

	state, ok := router.states.Load(plat.ID)
	if !ok {
		t.Fatalf("precondition: no routing state for %s after RestoreLeases", plat.ID)
	}
	if got := state.Leases.Size(); got != len(leases) {
		t.Fatalf("precondition: lease table size = %d, want %d", got, len(leases))
	}
	if got := state.IPLoadStats.Get(netip.MustParseAddr("203.0.113.10")); got != 1 {
		t.Fatalf("precondition: ip load for 203.0.113.10 = %d, want 1", got)
	}

	events = nil
	if got := router.PurgePlatformState(plat.ID); got != len(leases) {
		t.Fatalf("PurgePlatformState = %d, want %d", got, len(leases))
	}

	if _, ok := router.states.Load(plat.ID); ok {
		t.Fatal("Router.states kept the purged platform (leak not fixed)")
	}
	if snap := router.SnapshotIPLoad(plat.ID); len(snap) != 0 {
		t.Fatalf("IP load stats survived the purge: %v", snap)
	}
	if router.RangeLeases(plat.ID, func(string, Lease) bool { return true }) {
		t.Fatal("RangeLeases still reports state for the purged platform")
	}

	removed := map[string]LeaseEvent{}
	for _, e := range events {
		if e.Type == LeaseRemove && e.PlatformID == plat.ID {
			removed[e.Account] = e
		}
	}
	if len(removed) != len(leases) {
		t.Fatalf("LeaseRemove events = %d, want %d (events=%v)", len(removed), len(leases), events)
	}
	for _, want := range leases {
		got, ok := removed[want.Account]
		if !ok {
			t.Fatalf("no LeaseRemove event for account %q", want.Account)
		}
		if got.NodeHash.Hex() != want.NodeHash {
			t.Errorf("event node hash = %s, want %s", got.NodeHash.Hex(), want.NodeHash)
		}
		if got.EgressIP.String() != want.EgressIP {
			t.Errorf("event egress ip = %s, want %s", got.EgressIP, want.EgressIP)
		}
		if got.CreatedAtNs != want.CreatedAtNs {
			t.Errorf("event created_at_ns = %d, want %d", got.CreatedAtNs, want.CreatedAtNs)
		}
	}

	// Idempotent: the platform is already gone.
	if got := router.PurgePlatformState(plat.ID); got != 0 {
		t.Fatalf("second PurgePlatformState = %d, want 0", got)
	}

	// A platform that never had routing state is a no-op too.
	if got := router.PurgePlatformState("plat-never-seen"); got != 0 {
		t.Fatalf("PurgePlatformState(unknown) = %d, want 0", got)
	}
}

func TestPurgePlatformState_NilRouterIsSafe(t *testing.T) {
	var nilRouter *Router
	if got := nilRouter.PurgePlatformState("plat-purge"); got != 0 {
		t.Fatalf("nil Router PurgePlatformState = %d, want 0", got)
	}
}
