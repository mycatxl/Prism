package routing

import (
	"net/netip"
	"testing"

	"prism/internal/model"
)

// The lease key carries the owner, so two tenants that both call their account
// "default" get their own lease. Sharing one would hand them the same egress IP,
// which is exactly what the tenant boundary has to prevent.

func TestLeaseTable_SameAccountDifferentUsersAreDistinct(t *testing.T) {
	table := NewLeaseTable(NewIPLoadStats())

	first := Lease{EgressIP: mustAddr(t, "203.0.113.10"), CreatedAtNs: 1, ExpiryNs: 2}
	second := Lease{EgressIP: mustAddr(t, "203.0.113.20"), CreatedAtNs: 3, ExpiryNs: 4}

	table.CreateLease("user-a", "default", first)
	table.CreateLease("user-b", "default", second)

	if table.Size() != 2 {
		t.Fatalf("table holds %d leases, want 2", table.Size())
	}

	gotA, ok := table.GetLease("user-a", "default")
	if !ok || gotA.EgressIP.String() != "203.0.113.10" {
		t.Fatalf("user-a lease = %+v, want 203.0.113.10", gotA)
	}
	gotB, ok := table.GetLease("user-b", "default")
	if !ok || gotB.EgressIP.String() != "203.0.113.20" {
		t.Fatalf("user-b lease = %+v, want 203.0.113.20", gotB)
	}

	// Deleting one owner's lease must not touch the other's.
	if _, deleted := table.DeleteLease("user-a", "default", nil); !deleted {
		t.Fatal("user-a lease was not deleted")
	}
	if _, ok := table.GetLease("user-b", "default"); !ok {
		t.Fatal("deleting one owner's lease removed another owner's")
	}
}

// The empty owner is the single-tenant path, and it must behave exactly as the
// account-only table did: the transitional global proxy token depends on it.
func TestLeaseTable_EmptyUserKeepsTheOldBehaviour(t *testing.T) {
	table := NewLeaseTable(NewIPLoadStats())

	lease := Lease{EgressIP: mustAddr(t, "198.51.100.7"), CreatedAtNs: 1, ExpiryNs: 2}
	table.CreateLease("", "account-1", lease)

	got, ok := table.GetLease("", "account-1")
	if !ok {
		t.Fatal("the single-tenant lease is not readable")
	}
	if got.EgressIP != lease.EgressIP {
		t.Fatalf("lease = %+v, want %+v", got, lease)
	}

	// A named user with the same account is a different lease.
	if _, ok := table.GetLease("user-a", "account-1"); ok {
		t.Fatal("a named user resolved the single-tenant lease")
	}
	if table.Size() != 1 {
		t.Fatalf("table holds %d leases, want 1", table.Size())
	}
}

// The IP load counters follow the owner split: each lease counts once, and
// deleting one owner's lease leaves the other owner's count intact.
func TestLeaseTable_IPLoadCountsPerOwner(t *testing.T) {
	stats := NewIPLoadStats()
	table := NewLeaseTable(stats)

	shared := mustAddr(t, "203.0.113.30")
	table.CreateLease("user-a", "default", Lease{EgressIP: shared, CreatedAtNs: 1, ExpiryNs: 2})
	table.CreateLease("user-b", "default", Lease{EgressIP: shared, CreatedAtNs: 1, ExpiryNs: 2})

	if got := stats.Get(shared); got != 2 {
		t.Fatalf("IP load = %d, want 2", got)
	}

	table.DeleteLease("user-a", "default", nil)
	if got := stats.Get(shared); got != 1 {
		t.Fatalf("IP load after one delete = %d, want 1", got)
	}
}

// DeleteLeaseIfOlderThan is addressed to one owner: the age check must not
// rotate a lease that belongs to somebody else.
func TestLeaseTable_DeleteIfOlderThanIsPerOwner(t *testing.T) {
	table := NewLeaseTable(NewIPLoadStats())

	old := Lease{EgressIP: mustAddr(t, "203.0.113.40"), CreatedAtNs: 100, ExpiryNs: 1 << 40}
	fresh := Lease{EgressIP: mustAddr(t, "203.0.113.50"), CreatedAtNs: 900, ExpiryNs: 1 << 40}
	table.CreateLease("user-a", "default", old)
	table.CreateLease("user-b", "default", fresh)

	if _, deleted := table.DeleteLeaseIfOlderThan("user-a", "default", 500, nil); !deleted {
		t.Fatal("the old lease was not rotated")
	}
	if _, ok := table.GetLease("user-b", "default"); !ok {
		t.Fatal("another owner's fresh lease was rotated")
	}
	if _, deleted := table.DeleteLeaseIfOlderThan("user-b", "default", 500, nil); deleted {
		t.Fatal("a fresh lease was rotated despite the age check")
	}
}

// The rotation tombstone is keyed per owner too: two tenants sharing an account
// name must not inherit each other's avoided egress IP.
func TestRotationTombstoneKey_SeparatesUsers(t *testing.T) {
	first := rotationTombstoneKey("plat", "user-a", "default")
	second := rotationTombstoneKey("plat", "user-b", "default")
	if first == second {
		t.Fatalf("tombstone keys collide: %q", first)
	}

	// The single-tenant key is distinct from every named-user key, and a user
	// id that itself contains the separator cannot forge another owner's key.
	if rotationTombstoneKey("plat", "", "default") == first {
		t.Fatal("the single-tenant key collides with a named user's")
	}
	if rotationTombstoneKey("plat", "user-a", "default") != first {
		t.Fatal("the same owner must map to the same key")
	}
}

// The model carries the owner end to end: a lease written for a user must read
// back with that user, or a restart would move it to the single-tenant path.
func TestLeaseModelCarriesTheOwner(t *testing.T) {
	lease := model.Lease{
		PlatformID:     "plat-1",
		UserID:         "user-a",
		Account:        "default",
		NodeHash:       "11111111111111111111111111111111",
		EgressIP:       "203.0.113.60",
		CreatedAtNs:    1,
		ExpiryNs:       2,
		LastAccessedNs: 1,
	}
	if lease.UserID != "user-a" {
		t.Fatalf("lease owner = %q", lease.UserID)
	}

	key := model.LeaseKey{PlatformID: lease.PlatformID, UserID: lease.UserID, Account: lease.Account}
	if key.UserID != "user-a" {
		t.Fatalf("lease key owner = %q", key.UserID)
	}

	// Two owners of one account must produce different keys, which is what the
	// persistence layer keys on.
	other := model.LeaseKey{PlatformID: lease.PlatformID, UserID: "user-b", Account: lease.Account}
	if key == other {
		t.Fatal("two owners produced the same persistence key")
	}
}

func mustAddr(t *testing.T, raw string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return addr
}
