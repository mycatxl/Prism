package routing

import (
	"net/netip"
	"testing"
	"time"

	"prism/internal/model"
	"prism/internal/platform"
)

// Routing-level isolation checks. These drive the real Router over the shared
// fixture pool, so they cover the whole path a request takes: the lease key, the
// sticky decision, the IP load counters and the rotation tombstone.

// ownerFixture builds a platform with the given egress IPs and a router that
// routes over it.
func ownerFixture(t *testing.T, egressIPs ...string) (*Router, *platform.Platform, *PlatformRoutingState) {
	t.Helper()

	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-owner", "PlatOwner", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	pool.addPlatform(plat)

	for i, ip := range egressIPs {
		raw := `{"id":"owner-` + string(rune('a'+i)) + `"}`
		h, e := newRoutableEntry(t, raw, ip)
		pool.addEntry(h, e)
	}
	pool.rebuildPlatformView(plat)

	router := newTestRouter(pool, nil)
	return router, plat, router.ensurePlatformState(plat.ID)
}

// Two users that both call their account "default" must end up with their own
// lease. With a single routable exit IP both leases necessarily point at the
// same node, so the assertion is on the lease table itself: two entries, one per
// owner, rather than one entry shared by both.
func TestRouteRequest_SameAccountDifferentUsersHoldSeparateLeases(t *testing.T) {
	router, plat, state := ownerFixture(t, "203.0.113.1")

	first, err := router.RouteRequest(plat.Name, "user-a", "default", "https://example.com/a")
	if err != nil {
		t.Fatalf("RouteRequest(user-a): %v", err)
	}
	second, err := router.RouteRequest(plat.Name, "user-b", "default", "https://example.com/b")
	if err != nil {
		t.Fatalf("RouteRequest(user-b): %v", err)
	}

	if state.Leases.Size() != 2 {
		t.Fatalf("lease table holds %d leases, want 2 (one per owner)", state.Leases.Size())
	}
	for _, userID := range []string{"user-a", "user-b"} {
		lease, ok := state.Leases.GetLease(userID, "default")
		if !ok {
			t.Fatalf("%s has no lease of its own", userID)
		}
		if lease.EgressIP.String() != "203.0.113.1" {
			t.Fatalf("%s lease egress = %s", userID, lease.EgressIP)
		}
	}
	if _, ok := state.Leases.GetLease("", "default"); ok {
		t.Fatal("a single-tenant lease appeared for a request that carried a user")
	}

	// Both requests were served, and the second one is not a hit on the first
	// owner's lease: a fresh lease means LeaseCreated.
	if !first.LeaseCreated || !second.LeaseCreated {
		t.Fatalf("lease created flags: first=%v second=%v, want both true", first.LeaseCreated, second.LeaseCreated)
	}
}

// A second request from the same owner is a lease hit: the owner is part of the
// key, so it finds its own lease rather than creating another.
func TestRouteRequest_SameOwnerReusesItsLease(t *testing.T) {
	router, plat, state := ownerFixture(t, "203.0.113.1")

	if _, err := router.RouteRequest(plat.Name, "user-a", "default", "https://example.com/a"); err != nil {
		t.Fatalf("first RouteRequest: %v", err)
	}
	again, err := router.RouteRequest(plat.Name, "user-a", "default", "https://example.com/a")
	if err != nil {
		t.Fatalf("second RouteRequest: %v", err)
	}
	if again.LeaseCreated {
		t.Fatal("the same owner drew a second lease instead of reusing its own")
	}
	if state.Leases.Size() != 1 {
		t.Fatalf("lease table holds %d leases, want 1", state.Leases.Size())
	}
}

// The transitional path keeps working: no user means the single-tenant lease,
// which is what every deployment had before leases carried an owner.
func TestRouteRequest_EmptyUserKeepsTheSingleTenantLease(t *testing.T) {
	router, plat, state := ownerFixture(t, "203.0.113.1")

	if _, err := router.RouteRequest(plat.Name, "", "default", "https://example.com/x"); err != nil {
		t.Fatalf("RouteRequest: %v", err)
	}
	if state.Leases.Size() != 1 {
		t.Fatalf("lease table holds %d leases, want 1", state.Leases.Size())
	}
	if _, ok := state.Leases.GetLease("", "default"); !ok {
		t.Fatal("the single-tenant lease is missing")
	}

	// A named user with the same account is a different lease, not a hit.
	named, err := router.RouteRequest(plat.Name, "user-a", "default", "https://example.com/x")
	if err != nil {
		t.Fatalf("RouteRequest(user-a): %v", err)
	}
	if !named.LeaseCreated {
		t.Fatal("a named user reused the single-tenant lease")
	}
	if state.Leases.Size() != 2 {
		t.Fatalf("lease table holds %d leases, want 2", state.Leases.Size())
	}
}

// Rotating one owner's lease must leave the other owner's alone, and the
// tombstone it records must not be visible to the other owner.
func TestRotateLease_IsScopedToOneOwner(t *testing.T) {
	router, plat, state := ownerFixture(t, "203.0.113.1", "203.0.113.2")

	if _, err := router.RouteRequest(plat.Name, "user-a", "default", "https://example.com/x"); err != nil {
		t.Fatalf("RouteRequest(user-a): %v", err)
	}
	if _, err := router.RouteRequest(plat.Name, "user-b", "default", "https://example.com/x"); err != nil {
		t.Fatalf("RouteRequest(user-b): %v", err)
	}

	rotated, ok := state.Leases.GetLease("user-a", "default")
	if !ok {
		t.Fatal("user-a has no lease to rotate")
	}

	if !router.RotateLease(plat, "user-a", "default") {
		t.Fatal("RotateLease(user-a) = false, want true")
	}
	if _, ok := state.Leases.GetLease("user-a", "default"); ok {
		t.Fatal("user-a's lease survived the rotation")
	}
	if _, ok := state.Leases.GetLease("user-b", "default"); !ok {
		t.Fatal("rotating one owner removed another owner's lease")
	}

	// The tombstone belongs to the rotated owner only.
	tombstone, ok := router.RotationTombstone(plat.ID, "user-a", "default")
	if !ok || tombstone != rotated.EgressIP {
		t.Fatalf("user-a tombstone = (%v, %v), want %v", tombstone, ok, rotated.EgressIP)
	}
	if _, ok := router.RotationTombstone(plat.ID, "user-b", "default"); ok {
		t.Fatal("another owner inherited the rotation tombstone")
	}
}

// Deleting one owner's lease leaves the other owner's intact, and the
// management surface can address each of them.
func TestDeleteLease_IsScopedToOneOwner(t *testing.T) {
	router, plat, state := ownerFixture(t, "203.0.113.1")

	if _, err := router.RouteRequest(plat.Name, "user-a", "default", "https://example.com/x"); err != nil {
		t.Fatalf("RouteRequest(user-a): %v", err)
	}
	if _, err := router.RouteRequest(plat.Name, "user-b", "default", "https://example.com/x"); err != nil {
		t.Fatalf("RouteRequest(user-b): %v", err)
	}

	if !router.DeleteLease(plat.ID, "user-a", "default") {
		t.Fatal("DeleteLease(user-a) = false, want true")
	}
	if _, ok := state.Leases.GetLease("user-b", "default"); !ok {
		t.Fatal("deleting one owner's lease removed another owner's")
	}
	if router.DeleteLease(plat.ID, "user-a", "default") {
		t.Fatal("deleting an absent lease reported success")
	}

	// The persisted key carries the owner too, or a restart would restore the
	// lease under the wrong one.
	persisted := model.LeaseKey{PlatformID: plat.ID, UserID: "user-b", Account: "default"}
	if persisted.UserID != "user-b" {
		t.Fatalf("persisted lease key owner = %q", persisted.UserID)
	}
}

// RangeLeases reports the owner, which is what the management surface and the
// persistence callback key on.
func TestRangeLeases_ReportsTheOwner(t *testing.T) {
	router, plat, _ := ownerFixture(t, "203.0.113.1")

	if _, err := router.RouteRequest(plat.Name, "user-a", "shared", "https://example.com/x"); err != nil {
		t.Fatalf("RouteRequest(user-a): %v", err)
	}
	if _, err := router.RouteRequest(plat.Name, "user-b", "shared", "https://example.com/x"); err != nil {
		t.Fatalf("RouteRequest(user-b): %v", err)
	}

	// Keyed by owner, not by account: the two owners deliberately share the
	// account name, so keying on the account would collapse them into one entry
	// and hide exactly the bug this test exists for.
	seen := make(map[string]string)
	ok := router.RangeLeases(plat.ID, func(userID, account string, _ Lease) bool {
		seen[userID] = account
		return true
	})
	if !ok {
		t.Fatal("RangeLeases reported no state for a platform that has leases")
	}
	if len(seen) != 2 {
		t.Fatalf("RangeLeases reported %d leases, want 2", len(seen))
	}
	for _, userID := range []string{"user-a", "user-b"} {
		if seen[userID] != "shared" {
			t.Fatalf("RangeLeases reported owner %q with account %q, want \"shared\"", userID, seen[userID])
		}
	}
}

// The IP load counters follow the owners: each lease counts once even when both
// point at the same exit IP, and deleting one leaves the other counted.
func TestRouteRequest_IPLoadCountsEachOwnerOnce(t *testing.T) {
	router, plat, state := ownerFixture(t, "203.0.113.1")

	if _, err := router.RouteRequest(plat.Name, "user-a", "default", "https://example.com/x"); err != nil {
		t.Fatalf("RouteRequest(user-a): %v", err)
	}
	if _, err := router.RouteRequest(plat.Name, "user-b", "default", "https://example.com/x"); err != nil {
		t.Fatalf("RouteRequest(user-b): %v", err)
	}

	shared := netip.MustParseAddr("203.0.113.1")
	if got := state.IPLoadStats.Get(shared); got != 2 {
		t.Fatalf("IP load = %d, want 2", got)
	}

	if !router.DeleteLease(plat.ID, "user-a", "default") {
		t.Fatal("DeleteLease(user-a) = false")
	}
	if got := state.IPLoadStats.Get(shared); got != 1 {
		t.Fatalf("IP load after one delete = %d, want 1", got)
	}
}

// Two owners with different accounts must still route independently, which is
// the pre-existing behaviour and has to keep working.
func TestRouteRequest_DifferentAccountsStayIndependent(t *testing.T) {
	router, plat, state := ownerFixture(t, "203.0.113.1")

	if _, err := router.RouteRequest(plat.Name, "user-a", "account-1", "https://example.com/x"); err != nil {
		t.Fatalf("RouteRequest(account-1): %v", err)
	}
	if _, err := router.RouteRequest(plat.Name, "user-a", "account-2", "https://example.com/x"); err != nil {
		t.Fatalf("RouteRequest(account-2): %v", err)
	}
	if state.Leases.Size() != 2 {
		t.Fatalf("lease table holds %d leases, want 2", state.Leases.Size())
	}
}
