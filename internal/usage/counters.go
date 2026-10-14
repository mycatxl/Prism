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
		entries: xsync.NewMap[CounterKey, *keyCounter](),
		users:   xsync.NewMap[string, *atomic.Int64](),
	}
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
func (c *Counters) Add(key CounterKey, delta Delta) {
	if c == nil || c.entries == nil || delta == (Delta{}) {
		return
	}
	// The empty-delta case is handled above, but Compute's return value must
	// still be used to satisfy the API: the counter is mutated in place and
	// returned unchanged.
	_, _ = c.entries.Compute(key, func(counter *keyCounter, _ bool) (*keyCounter, xsync.ComputeOp) {
		if counter == nil {
			counter = &keyCounter{}
		}
		counter.ingress.Add(delta.IngressBytes)
		counter.egress.Add(delta.EgressBytes)
		counter.requests.Add(delta.Requests)
		counter.errors.Add(delta.Errors)
		return counter, xsync.UpdateOp
	})

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

// Drain removes and returns the counters for the given keys.
//
// It is how the flusher takes a consistent snapshot to persist: taking the
// values without removing them would either lose the bytes added between the
// read and the write, or count them twice. Removing first means a byte added
// during the flush lands in the next one.
//
// The user aggregate is deliberately not drained here. It is the running total
// a quota is checked against, and a flush that reset it would hand the user
// their whole allowance back every ten seconds.
func (c *Counters) Drain(keys []CounterKey) map[CounterKey]Delta {
	drained := make(map[CounterKey]Delta, len(keys))
	if c == nil || c.entries == nil {
		return drained
	}
	for _, key := range keys {
		counter, ok := c.entries.LoadAndDelete(key)
		if !ok || counter == nil {
			continue
		}
		delta := Delta{
			IngressBytes: counter.ingress.Load(),
			EgressBytes:  counter.egress.Load(),
			Requests:     counter.requests.Load(),
			Errors:       counter.errors.Load(),
		}
		if delta != (Delta{}) {
			drained[key] = delta
		}
	}
	return drained
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
