// Package usage accounts for the traffic each access key moves.
//
// The data plane must not touch the database, so every counter here is an
// in-memory atomic and the caller persists them asynchronously. The package is
// deliberately free of the proxy's types: it deals in ids and byte counts, so
// the same counters serve the data plane, the management API and the tests
// without either side importing the other.
package usage

import (
	"sync/atomic"

	"github.com/puzpuzpuz/xsync/v4"
)

// Counters is the live view of what has been moved since the process started.
//
// Two levels are kept because they answer different questions:
//
//   - per (key, platform) drives the usage rows an operator reads and the
//     per-key quotas;
//   - per user drives the subscription's period usage, which is one number
//     regardless of how many keys the user holds.
//
// The user level is not derivable from the key level on the hot path: deriving
// it would mean walking every key of the user on each byte, and a user may hold
// several keys (max_keys), so the aggregate is maintained as the deltas arrive.
type Counters struct {
	// entries is keyed by CounterKey. The value is a *keyCounter, which is
	// immutable after creation except for its atomics, so readers never need a
	// lock.
	entries *xsync.Map[CounterKey, *keyCounter]
	// users is keyed by user id. It exists so a quota check does not have to
	// find every key the user holds.
	users *xsync.Map[string, *atomic.Int64]
	// persisted is keyed by user id and holds the water mark: the number of
	// bytes of that user's running total which the persisted period usage
	// (user_subscriptions.period_used_bytes) is known to cover.
	//
	// It exists because the flusher charges the subscription without draining
	// the aggregate: after a flush the same bytes are in both, and a seed that
	// reads the persisted total back cannot tell "this total was never loaded
	// into memory" from "these bytes were already charged". Adding the whole
	// total on the second reading bills the user twice for traffic that moved
	// once; adding nothing on the first hands the allowance out twice. The water
	// mark answers what the total alone cannot: only the part of it above the
	// mark is missing from memory.
	//
	// The flusher is the only writer, and it advances the mark by the bytes it
	// really charged to a subscription (MarkPersisted). The seed reads the mark
	// and moves it to the total it restored against, so it can run again.
	persisted *xsync.Map[string, *atomic.Int64]
	// keyBytes is keyed by key id and holds the bytes of every one of that
	// key's buckets which has not been drained yet. It exists so the fallback
	// a quota check makes - "this key has moved N bytes" - is a single atomic
	// load rather than a walk over every bucket in the process.
	keyBytes *xsync.Map[string, *atomic.Int64]
}

// CounterKey identifies one metering bucket: one key on one platform.
//
// PlatformID is part of the key because a plan can allow several platforms and
// an operator wants to see which one the traffic went to. It is empty for a
// request that never resolved a platform (a rejected request, or the direct
// bypass path), which is still worth counting against the user.
type CounterKey struct {
	KeyID      string
	UserID     string
	PlatformID string
}

// keyCounter is the per-bucket accumulator.
//
// The fields are atomics rather than a mutex-protected struct: every byte on
// the hot path adds to exactly one bucket, and a shared lock would serialize
// all of them.
type keyCounter struct {
	ingress  atomic.Int64
	egress   atomic.Int64
	requests atomic.Int64
	errors   atomic.Int64
	// bytes is the key-wide accumulator shared by every bucket of the same key
	// id, so the per-key total is maintained as the bytes arrive instead of
	// being re-derived on each check. It is nil for traffic with no key id,
	// which is counted in its bucket and in no aggregate.
	bytes *atomic.Int64
}

// Delta is one reported amount of traffic.
type Delta struct {
	IngressBytes int64
	EgressBytes  int64
	// Requests and Errors are counted once per request, not per byte, so a
	// caller reports them on the request that produced them.
	Requests int64
	Errors   int64
}

// Total returns the bytes counted in both directions.
func (d Delta) Total() int64 { return d.IngressBytes + d.EgressBytes }

// NewCounters returns an empty counter set.
func NewCounters() *Counters {
	return &Counters{
		entries:   xsync.NewMap[CounterKey, *keyCounter](),
		users:     xsync.NewMap[string, *atomic.Int64](),
		persisted: xsync.NewMap[string, *atomic.Int64](),
		keyBytes:  xsync.NewMap[string, *atomic.Int64](),
	}
}

// keyTotalFor returns the shared byte accumulator of one key id, creating it on
// first use. An empty key id has none: traffic that belongs to no key is still
// counted in its bucket, and attributed to no aggregate.
func (c *Counters) keyTotalFor(keyID string) *atomic.Int64 {
	if c == nil || c.keyBytes == nil || keyID == "" {
		return nil
	}
	total, _ := c.keyBytes.LoadOrCompute(keyID, func() (*atomic.Int64, bool) {
		return new(atomic.Int64), false
	})
	return total
}

// Add records a delta for one key on one platform.
//
// It is safe for concurrent use and allocates nothing on the steady-state path
// once the bucket exists, which is what keeps it usable per byte.
//
// The update is done inside Compute rather than on a counter fetched by
// LoadOrCompute. Fetching first loses bytes: Drain removes the bucket, the
// fetch returns the removed counter, and the increment lands in a counter
// nobody will ever read again. Doing the arithmetic under the same critical
// section that owns the bucket is what makes "every byte is either drained or
// still counted" true.
//
// The key-wide total is updated in that same section, so it stays the exact sum
// of the buckets that are still present.
func (c *Counters) Add(key CounterKey, delta Delta) {
	if c == nil || c.entries == nil || delta == (Delta{}) {
		return
	}
	c.addToBuckets(key, delta)

	// The user aggregate counts bytes only: request counts belong to a key, and
	// adding them here would double-count a user's requests once per key. It is
	// never drained, so it needs no such protection - only its own atomicity.
	if total := delta.Total(); total != 0 && key.UserID != "" {
		userTotal, _ := c.users.LoadOrCompute(key.UserID, func() (*atomic.Int64, bool) {
			return new(atomic.Int64), false
		})
		userTotal.Add(total)
	}
}

// Requeue puts a drained batch back into its buckets after a failed write,
// without touching the user aggregate.
//
// The two levels are restored differently on purpose. The buckets are what the
// next pass writes to the hourly table, so they have to come back. The user
// aggregate was already incremented when the bytes arrived and is never
// drained, so adding the batch to it again would charge the subscription twice
// for traffic that moved once: a requeue is a retry of a write, not a second
// sighting of the traffic.
func (c *Counters) Requeue(drained map[CounterKey]Delta) {
	if c == nil || c.entries == nil || len(drained) == 0 {
		return
	}
	for key, delta := range drained {
		if delta == (Delta{}) {
			continue
		}
		c.addToBuckets(key, delta)
	}
}

// addToBuckets adds a delta to one bucket and to that key's byte total.
//
// Both updates run inside the same critical section, the one that owns the
// bucket, so the key total is exactly the sum of the buckets still present at
// every instant - which is what lets a quota check read it instead of walking
// the buckets itself.
func (c *Counters) addToBuckets(key CounterKey, delta Delta) {
	_, _ = c.entries.Compute(key, func(counter *keyCounter, _ bool) (*keyCounter, xsync.ComputeOp) {
		if counter == nil {
			counter = &keyCounter{bytes: c.keyTotalFor(key.KeyID)}
		}
		counter.ingress.Add(delta.IngressBytes)
		counter.egress.Add(delta.EgressBytes)
		counter.requests.Add(delta.Requests)
		counter.errors.Add(delta.Errors)
		if total := delta.Total(); total != 0 && counter.bytes != nil {
			counter.bytes.Add(total)
		}
		return counter, xsync.UpdateOp
	})
}

// KeyTotal returns the bytes counted for one key id across every bucket of that
// key which has not been drained yet. It is the per-key fallback a quota check
// uses when the owning user's aggregate is unavailable.
func (c *Counters) KeyTotal(keyID string) int64 {
	if c == nil || c.keyBytes == nil || keyID == "" {
		return 0
	}
	total, ok := c.keyBytes.Load(keyID)
	if !ok || total == nil {
		return 0
	}
	return total.Load()
}

// KeyBytes returns the bytes counted for one key on one platform.
func (c *Counters) KeyBytes(key CounterKey) Delta {
	if c == nil {
		return Delta{}
	}
	counter, ok := c.entries.Load(key)
	if !ok || counter == nil {
		return Delta{}
	}
	return Delta{
		IngressBytes: counter.ingress.Load(),
		EgressBytes:  counter.egress.Load(),
		Requests:     counter.requests.Load(),
		Errors:       counter.errors.Load(),
	}
}

// UserBytes returns the bytes counted for one user across all of their keys.
func (c *Counters) UserBytes(userID string) int64 {
	if c == nil || userID == "" {
		return 0
	}
	total, ok := c.users.Load(userID)
	if !ok || total == nil {
		return 0
	}
	return total.Load()
}

// UserBytesPresent returns a user's running total and reports whether the user
// has an aggregate at all.
//
// The distinction between "no aggregate" and "an aggregate of zero" is what a
// quota check needs: a period rollover sets the total back to zero without
// dropping the entry, and reading that zero as "no aggregate" made the check
// fall back to the key's own counter - which a rollover deliberately leaves
// holding the previous period's bytes. The result was a user whose new period
// had just begun being refused for traffic they had not moved yet.
func (c *Counters) UserBytesPresent(userID string) (int64, bool) {
	if c == nil || userID == "" {
		return 0, false
	}
	total, ok := c.users.Load(userID)
	if !ok || total == nil {
		return 0, false
	}
	return total.Load(), true
}

// persistedFor returns the water mark of one user, creating it on first use.
//
// An entry that does not exist yet stands for zero: nothing of that user's
// running total has been charged to the persisted period usage by this process.
func (c *Counters) persistedFor(userID string) *atomic.Int64 {
	if c == nil || c.persisted == nil || userID == "" {
		return nil
	}
	mark, _ := c.persisted.LoadOrCompute(userID, func() (*atomic.Int64, bool) {
		return new(atomic.Int64), false
	})
	return mark
}

// MarkPersisted records that the persisted period usage of one user now covers
// bytes more of their running total.
//
// The flusher is the only caller, and it passes exactly what it charged to the
// subscription: a delta the store refused because the period had rolled over, a
// write that failed, and traffic that belongs to no subscription are all not on
// disk, and reporting them as persisted would hide the bytes a later seed has to
// restore - which is the difference between an allowance and two of them.
func (c *Counters) MarkPersisted(userID string, bytes int64) {
	if bytes == 0 {
		return
	}
	mark := c.persistedFor(userID)
	if mark == nil {
		return
	}
	mark.Add(bytes)
}

// PersistedBytes returns a user's water mark and reports whether one exists,
// in the shape UserBytesPresent uses for the aggregate.
//
// A missing mark is not a missing persisted total: it means nothing of that
// user's running total has been charged by this process, so the caller compares
// the persisted total against the zero it stands for.
func (c *Counters) PersistedBytes(userID string) (int64, bool) {
	if c == nil || c.persisted == nil || userID == "" {
		return 0, false
	}
	mark, ok := c.persisted.Load(userID)
	if !ok || mark == nil {
		return 0, false
	}
	return mark.Load(), true
}

// Drain removes and returns the counters for the given keys.
//
// It is how the flusher takes a consistent snapshot to persist: taking the
// values without removing them would either lose the bytes added between the
// read and the write, or count them twice. Removing first means a byte added
// during the flush lands in the next one.
// The user aggregate is deliberately not drained here. It is the running total
// a quota is checked against, and a flush that reset it would hand the user
// their whole allowance back every ten seconds.
//
// The key's own byte total is reduced by exactly what was removed: it tracks
// the buckets that are still present, so it must fall with them.
func (c *Counters) Drain(keys []CounterKey) map[CounterKey]Delta {
	drained := make(map[CounterKey]Delta, len(keys))
	if c == nil || c.entries == nil {
		return drained
	}
	for _, key := range keys {
		delta := c.drainOne(key)
		if delta != (Delta{}) {
			drained[key] = delta
		}
	}
	return drained
}

// drainOne removes one bucket and returns what it held.
//
// The read, the removal and the subtraction from the key's byte total share one
// critical section. Splitting them would let a concurrent Add slip its bytes
// into the total after the removal: the bucket would be gone, so nothing would
// ever subtract them, and the key's running total would drift upwards for the
// rest of the process.
func (c *Counters) drainOne(key CounterKey) Delta {
	var delta Delta
	_, _ = c.entries.Compute(key, func(counter *keyCounter, loaded bool) (*keyCounter, xsync.ComputeOp) {
		if !loaded || counter == nil {
			return nil, xsync.CancelOp
		}
		delta = Delta{
			IngressBytes: counter.ingress.Load(),
			EgressBytes:  counter.egress.Load(),
			Requests:     counter.requests.Load(),
			Errors:       counter.errors.Load(),
		}
		if counter.bytes != nil {
			counter.bytes.Add(-delta.Total())
		}
		return nil, xsync.DeleteOp
	})
	return delta
}

// Keys returns the keys that currently hold a counter.
func (c *Counters) Keys() []CounterKey {
	if c == nil {
		return nil
	}
	keys := make([]CounterKey, 0, c.entries.Size())
	c.entries.Range(func(key CounterKey, _ *keyCounter) bool {
		keys = append(keys, key)
		return true
	})
	return keys
}

// Size reports how many buckets are held. It is used by the tests and by the
// memory bound check.
func (c *Counters) Size() int {
	if c == nil {
		return 0
	}
	return c.entries.Size()
}

// ResetUser returns a user's running total to zero, and its water mark with it.
//
// It is what a period rollover does: the allowance belongs to a period, and a
// new period starts with nothing used. Without it the persisted counter would
// reset while the in-memory one kept the old total, and the quota would refuse
// every request of a user whose new month had just begun - the failure mode of
// a correct database and a stale cache.
//
// The water mark falls with it, for the same reason and at the same moment: the
// rollover resets period_used_bytes on disk (ResetSubscriptionPeriod) as it
// calls this, so neither the aggregate nor the mark has anything to do with the
// period that just ended. A mark left at the old period's value would claim that
// the new period's persisted total is already covered - the seed restores the
// difference between the persisted total and the mark, and that difference
// would be zero or negative for a period whose usage is just starting to grow.
//
// The per-key buckets are deliberately left alone. They are the record of what
// actually moved and are destined for the usage tables; the aggregate is only
// ever a running total against an allowance.
func (c *Counters) ResetUser(userID string) {
	if c == nil || c.users == nil || userID == "" {
		return
	}
	if total, ok := c.users.Load(userID); ok && total != nil {
		total.Store(0)
	}
	// The mark is dropped even when the aggregate is already missing: the two
	// are written together everywhere else, and a stale mark left behind would
	// suppress the next seed of this user's persisted total.
	if c.persisted != nil {
		if mark, ok := c.persisted.Load(userID); ok && mark != nil {
			mark.Store(0)
		}
	}
}

// LoadUser overwrites a user's running total, and the water mark with it.
//
// It is how a restart restores the aggregate from the persisted period usage,
// and how a period rollover sets it back to zero without dropping the entry.
//
// The mark follows the value, because both uses load a total the disk agrees
// with: a restart loads exactly what period_used_bytes holds, and a rollover
// loads the zero the row it just reset holds. Zeroing the mark instead - the
// obvious alternative - would leave the next seed free to add the persisted
// total on top of an aggregate that already carries it, which is the double
// count the mark exists to prevent.
func (c *Counters) LoadUser(userID string, bytes int64) {
	if c == nil || c.users == nil || userID == "" {
		return
	}
	total, _ := c.users.LoadOrCompute(userID, func() (*atomic.Int64, bool) {
		return new(atomic.Int64), false
	})
	total.Store(bytes)
	if mark := c.persistedFor(userID); mark != nil {
		mark.Store(bytes)
	}
}
