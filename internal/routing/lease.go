package routing

import (
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"prism/internal/node"
)

// Lease represents a sticky routing lease.
// It is a value type to avoid pointer aliasing races.
type Lease struct {
	NodeHash       node.Hash
	EgressIP       netip.Addr
	CreatedAtNs    int64
	ExpiryNs       int64
	LastAccessedNs int64
}

// leaseOwner identifies one sticky lease.
//
// The account alone is not unique once several users share a platform: two
// tenants that both call their account "default" must not share an egress IP,
// because sharing one is exactly what the tenant boundary is supposed to
// prevent. An empty UserID is the single-tenant caller - the transitional
// global proxy token and every path that has no account behind it - and it
// behaves as it always did.
type leaseOwner struct {
	UserID  string
	Account string
}

// owner returns the lease owner for a (user, account) pair.
func owner(userID, account string) leaseOwner {
	return leaseOwner{UserID: userID, Account: account}
}

// LeaseTable manages sticky leases for a single platform.
type LeaseTable struct {
	leases *xsync.Map[leaseOwner, Lease]
	stats  *IPLoadStats
}

// IPLoadStats tracks the number of active leases per egress IP.
type IPLoadStats struct {
	counts *xsync.Map[netip.Addr, *atomic.Int64]
}

// NewLeaseTable creates a new lease table linked to the given stats.
func NewLeaseTable(stats *IPLoadStats) *LeaseTable {
	return &LeaseTable{
		leases: xsync.NewMap[leaseOwner, Lease](),
		stats:  stats,
	}
}

// NewIPLoadStats creates a new IP load stats tracker.
func NewIPLoadStats() *IPLoadStats {
	return &IPLoadStats{
		counts: xsync.NewMap[netip.Addr, *atomic.Int64](),
	}
}

// GetLease returns the lease for the given owner.
func (t *LeaseTable) GetLease(userID, account string) (Lease, bool) {
	return t.leases.Load(owner(userID, account))
}

// Range iterates over all leases. The callback receives the owner, so a caller
// that has to re-address one lease can do it without guessing.
func (t *LeaseTable) Range(fn func(o leaseOwner, lease Lease) bool) {
	t.leases.Range(fn)
}

// CreateLease adds a new lease and increments the IP load count.
// If a lease explicitly exists (e.g. race), it is overwritten (and counts adjusted).
func (t *LeaseTable) CreateLease(userID, account string, lease Lease) {
	t.leases.Compute(owner(userID, account), func(oldVal Lease, loaded bool) (Lease, xsync.ComputeOp) {
		if loaded {
			t.stats.Dec(oldVal.EgressIP)
		}
		t.stats.Inc(lease.EgressIP)
		return lease, xsync.UpdateOp
	})
}

// DeleteLease atomically removes a lease and decrements IP load stats.
// Returns the deleted lease and true when a lease was actually deleted.
//
// onDelete (optional) runs inside the same per-owner critical section that
// removes the lease. Rotation needs that: the tombstone that tells the next
// lease which egress IP to avoid must exist before the account can draw a new
// lease, and the allocation path reads it under this same key. Writing the
// tombstone after Compute returned left a window where a concurrent request
// could be handed the IP that had just been rotated away.
func (t *LeaseTable) DeleteLease(userID, account string, onDelete func(Lease)) (Lease, bool) {
	var deleted Lease
	ok := false
	t.leases.Compute(owner(userID, account), func(oldVal Lease, loaded bool) (Lease, xsync.ComputeOp) {
		if loaded {
			t.stats.Dec(oldVal.EgressIP)
			deleted = oldVal
			ok = true
			if onDelete != nil {
				onDelete(oldVal)
			}
			return oldVal, xsync.DeleteOp
		}
		return oldVal, xsync.CancelOp
	})
	return deleted, ok
}

// DeleteLeaseIfOlderThan atomically removes a lease only when the lease that is
// currently stored is still old enough to be rotated. The age check happens
// inside the map lock, so a lease that was concurrently created or renewed
// after the caller collected candidates is never removed.
// Returns the deleted lease and true when a lease was actually deleted.
//
// onDelete (optional) runs inside the same critical section; see DeleteLease for
// why the rotation tombstone has to be written there.
func (t *LeaseTable) DeleteLeaseIfOlderThan(userID, account string, createdBeforeOrAtNs int64, onDelete func(Lease)) (Lease, bool) {
	var deleted Lease
	ok := false
	t.leases.Compute(owner(userID, account), func(current Lease, loaded bool) (Lease, xsync.ComputeOp) {
		if !loaded || current.CreatedAtNs > createdBeforeOrAtNs {
			return current, xsync.CancelOp
		}
		t.stats.Dec(current.EgressIP)
		deleted = current
		ok = true
		if onDelete != nil {
			onDelete(current)
		}
		return current, xsync.DeleteOp
	})
	return deleted, ok
}

// Size returns the number of leases in the table.
func (t *LeaseTable) Size() int {
	return t.leases.Size()
}

// Inc increments the lease count for an IP.
func (s *IPLoadStats) Inc(ip netip.Addr) {
	if !ip.IsValid() {
		return
	}
	ctr, _ := s.counts.LoadOrStore(ip, new(atomic.Int64))
	ctr.Add(1)
}

// Dec decrements the lease count for an IP.
func (s *IPLoadStats) Dec(ip netip.Addr) {
	if !ip.IsValid() {
		return
	}
	if ctr, ok := s.counts.Load(ip); ok {
		ctr.Add(-1)
		// We don't remove zero counters to avoid race conditions with concurrent Inc.
		// Since the set of egress IPs is bounded (by number of nodes), this leakage
		// is acceptable and bounded.
	}
}

// Get returns the current lease count for an IP.
func (s *IPLoadStats) Get(ip netip.Addr) int64 {
	if !ip.IsValid() {
		return 0
	}
	if ctr, ok := s.counts.Load(ip); ok {
		return ctr.Load()
	}
	return 0
}

// Snapshot returns a best-effort point-in-time copy of positive lease counts.
// Entries with non-positive counts are skipped.
func (s *IPLoadStats) Snapshot() map[netip.Addr]int64 {
	out := make(map[netip.Addr]int64)
	s.counts.Range(func(ip netip.Addr, ctr *atomic.Int64) bool {
		if !ip.IsValid() || ctr == nil {
			return true
		}
		n := ctr.Load()
		if n <= 0 {
			return true
		}
		out[ip] = n
		return true
	})
	return out
}

// IsExpired checks if a lease is expired relative to the given time.
func (l Lease) IsExpired(now time.Time) bool {
	return l.ExpiryNs < now.UnixNano()
}
