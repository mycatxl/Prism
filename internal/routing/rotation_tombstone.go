package routing

import (
	"container/list"
	"net/netip"
	"sync"
	"time"

	"prism/internal/platform"
)

// rotationTombstoneMaxEntries bounds the in-memory rotation tombstone cache
// (WP10 §3). Tombstones are intentionally not persisted: losing one on restart
// only means an account may draw the same egress IP once again.
const rotationTombstoneMaxEntries = 100_000

// rotationTombstoneKey builds the cache key for one account on one platform.
// "\x00" cannot appear in a platform ID or in an account name.
func rotationTombstoneKey(platformID, account string) string {
	return platformID + "\x00" + account
}

// rotationTombstone records the egress IP an account used before its lease was
// rotated, together with the moment the record stops applying.
type rotationTombstone struct {
	key         string
	egressIP    netip.Addr
	expiresAtNs int64
}

// rotationTombstoneCache is a bounded LRU of rotation tombstones. The most
// recently used key sits at the front of order and the least recently used one
// is evicted once maxEntries is exceeded. Expired entries are dropped on read.
type rotationTombstoneCache struct {
	mu         sync.Mutex
	maxEntries int
	entries    map[string]*list.Element
	order      *list.List // front = most recently used
}

func newRotationTombstoneCache(maxEntries int) *rotationTombstoneCache {
	if maxEntries <= 0 {
		maxEntries = rotationTombstoneMaxEntries
	}
	return &rotationTombstoneCache{
		maxEntries: maxEntries,
		entries:    make(map[string]*list.Element),
		order:      list.New(),
	}
}

// store records (or refreshes) the tombstone for key.
func (c *rotationTombstoneCache) store(key string, ip netip.Addr, expiresAtNs int64) {
	if c == nil || !ip.IsValid() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.entries[key]; ok {
		tomb := el.Value.(*rotationTombstone)
		tomb.egressIP = ip
		tomb.expiresAtNs = expiresAtNs
		c.order.MoveToFront(el)
		return
	}
	c.entries[key] = c.order.PushFront(&rotationTombstone{
		key:         key,
		egressIP:    ip,
		expiresAtNs: expiresAtNs,
	})
	c.evictLocked()
}

// lookup returns the live tombstone IP for key. Expired entries are removed and
// reported as absent.
func (c *rotationTombstoneCache) lookup(key string, nowNs int64) (netip.Addr, bool) {
	if c == nil {
		return netip.Addr{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.entries[key]
	if !ok {
		return netip.Addr{}, false
	}
	tomb := el.Value.(*rotationTombstone)
	if tomb.expiresAtNs <= nowNs {
		c.removeElementLocked(el)
		return netip.Addr{}, false
	}
	c.order.MoveToFront(el)
	return tomb.egressIP, true
}

// len returns the number of cached tombstones (expired ones included).
func (c *rotationTombstoneCache) len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *rotationTombstoneCache) removeElementLocked(el *list.Element) {
	c.order.Remove(el)
	delete(c.entries, el.Value.(*rotationTombstone).key)
}

// evictLocked drops least-recently-used entries until the bound holds again.
func (c *rotationTombstoneCache) evictLocked() {
	for len(c.entries) > c.maxEntries {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		c.removeElementLocked(oldest)
	}
}

// rotationTombstoneTTL returns how long a rotation tombstone stays valid:
// max(scheduled rotation interval, sticky TTL) (WP10 §3).
func rotationTombstoneTTL(interval time.Duration, stickyTTLNs int64) time.Duration {
	if stickyTTL := time.Duration(stickyTTLNs); stickyTTL > interval {
		return stickyTTL
	}
	return interval
}

// rotationAvoidIP returns the egress IP the account's next lease must avoid,
// when the platform enables rotation avoidance and a live tombstone exists.
func (r *Router) rotationAvoidIP(plat *platform.Platform, account string, nowNs int64) netip.Addr {
	if r == nil || plat == nil || account == "" || !plat.RotationAvoidPreviousIP {
		return netip.Addr{}
	}
	ip, ok := r.rotationTombstones.lookup(rotationTombstoneKey(plat.ID, account), nowNs)
	if !ok {
		return netip.Addr{}
	}
	return ip
}

// recordRotationTombstone stores the egress IP an account was using when its
// lease was rotated. The record is written regardless of the platform's
// rotation_avoid_previous_ip flag; that flag only gates avoidance at pick time.
func (r *Router) recordRotationTombstone(platformID, account string, ip netip.Addr, nowNs int64, ttl time.Duration) {
	if r == nil || account == "" || !ip.IsValid() || ttl <= 0 {
		return
	}
	r.rotationTombstones.store(rotationTombstoneKey(platformID, account), ip, nowNs+int64(ttl))
}

// RotationTombstone returns the live rotation tombstone for (platformID,
// account): the egress IP the next lease for that account should avoid.
func (r *Router) RotationTombstone(platformID, account string) (netip.Addr, bool) {
	if r == nil {
		return netip.Addr{}, false
	}
	return r.rotationTombstones.lookup(rotationTombstoneKey(platformID, account), time.Now().UnixNano())
}
