package routing

import (
	"math"
	"time"
)

// MaxStickyLeaseLifetime caps the idle TTL at seven days from
// acquisition. Renewal neither resets creation time nor reserves another slot.
const MaxStickyLeaseLifetime = 7 * 24 * time.Hour

func addLeaseDuration(ns int64, d time.Duration) int64 {
	if ns > math.MaxInt64-int64(d) {
		return math.MaxInt64
	}
	return ns + int64(d)
}

func (l Lease) expiryNs() int64 {
	if l.CreatedAtNs <= 0 {
		return l.ExpiryNs
	}
	return min(l.ExpiryNs, addLeaseDuration(l.CreatedAtNs, MaxStickyLeaseLifetime))
}

func stickyIdleTTL(ttl int64) time.Duration {
	if ttl <= 0 {
		return 24 * time.Hour
	}
	return min(time.Duration(ttl), MaxStickyLeaseLifetime)
}

func renewStickyLease(current Lease, nowNs, ttl int64) Lease {
	current.LastAccessedNs = max(current.LastAccessedNs, nowNs)
	// Old persisted leases without a creation stamp keep their fixed expiry;
	// the next acquisition creates a properly bounded lease.
	if current.CreatedAtNs <= 0 {
		return current
	}
	deadline := addLeaseDuration(current.CreatedAtNs, MaxStickyLeaseLifetime)
	candidate := addLeaseDuration(nowNs, stickyIdleTTL(ttl))
	current.ExpiryNs = min(deadline, max(current.ExpiryNs, candidate))
	return current
}
