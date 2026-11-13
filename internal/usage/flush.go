package usage

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"prism/internal/model"
	"prism/internal/safego"
)

// Flush scheduling defaults.
const (
	// DefaultFlushInterval controls request/error aggregation. Production byte
	// liability is journaled before service, independently of this interval.
	DefaultFlushInterval = 10 * time.Second
)

// FlushStore is the persistence surface the flusher needs. It is deliberately
// narrow: a *state.StateRepo satisfies it, and so does a test double.
type FlushStore interface {
	// UpsertUsageHourly adds rows to the hourly table, summing into existing
	// buckets rather than replacing them.
	UpsertUsageHourly(rows []model.UsageRow) error
	// GetActiveUserSubscription finds the subscription a user's traffic counts
	// against. It may report no subscription, which is not an error: a key with
	// no plan still moves bytes and those bytes are still worth recording.
	GetActiveUserSubscription(userID string) (*model.UserSubscription, error)
	// AddSubscriptionUsedBytes adds delta to a subscription's period usage.
	AddSubscriptionUsedBytes(id string, delta int64) error
}

// PeriodGuardedFlushStore is the optional half of FlushStore: a store that can
// charge a subscription's period only while that period is still the current
// one.
//
// It exists because the two writes of a flush are not simultaneous with the
// clock. Bytes are drained into memory, and by the time the subscription is
// updated the rollover may have started a new period and reset the usage that
// the drained bytes belong to. Charging the new period for the old one's
// traffic bills a user for a period they had not started to use, so a store
// that can express "only if the period still starts here" should say so. A
// store that cannot is not left unprotected: FlushOnce re-reads the window and
// refuses the add itself, which costs one extra read per user per pass.
type PeriodGuardedFlushStore interface {
	// AddSubscriptionUsedBytesInPeriod adds delta to a subscription's period
	// usage, and does nothing when the period no longer starts at
	// periodStartNs. It reports whether the bytes were charged.
	AddSubscriptionUsedBytesInPeriod(id string, periodStartNs, delta int64) (bool, error)
}

// AtomicUsageFlushStore lets a persistence layer commit the hourly rows and
// the matching subscription-period increments together. Without this hook a
// successful hourly write followed by a failed subscription update drains the
// in-memory bucket permanently and undercharges the quota after a restart.
type AtomicUsageFlushStore interface {
	FlushUsageHourlyAndCharge(rows []model.UsageRow, charges []UsageCharge) (map[string]UsageChargeResult, error)
}

// UsageCharge and UsageChargeResult are aliases so callers can use the usage
// package's flush vocabulary while the persistence layer keeps the shared
// contract in model.
type UsageCharge = model.UsageCharge
type UsageChargeResult = model.UsageChargeResult

type usageGroup struct {
	userID         string
	subscriptionID string
	periodStartNs  int64
	generation     uint64
}

func usageGroupKey(group usageGroup) string {
	return group.userID + "\x00" + group.subscriptionID + "\x00" + fmt.Sprint(group.periodStartNs) + "\x00" + fmt.Sprint(group.generation)
}

// Flusher writes the in-memory counters to the usage tables.
//
// Production bytes are settled through Journal; this worker persists request
// counts and legacy in-process callers. It must not duplicate journaled bytes.
type Flusher struct {
	// Counters is the live view the data plane writes to. Required.
	Counters *Counters
	// Store is where the drained counters are written. Nil disables flushing.
	Store FlushStore
	// Interval is how often Start runs a pass. Zero uses DefaultFlushInterval.
	Interval time.Duration
	// Logf receives progress and error messages. Nil silences them.
	Logf func(format string, args ...any)
	// Now is the clock, in Unix nanoseconds. Nil uses the wall clock. It is a
	// field so the tests can place a flush in a known hour bucket.
	Now func() int64

	// mu serializes passes. A pass that is still running when the next tick
	// arrives must not overlap it: two passes would drain disjoint sets of
	// buckets and could interleave their writes to the same subscription.
	mu sync.Mutex
}

func (f *Flusher) interval() time.Duration {
	if f.Interval > 0 {
		return f.Interval
	}
	return DefaultFlushInterval
}

func (f *Flusher) now() int64 {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now().UnixNano()
}

func (f *Flusher) logf(format string, args ...any) {
	if f.Logf != nil {
		f.Logf(format, args...)
	}
}

// FlushResult reports what one pass wrote.
type FlushResult struct {
	// Rows is how many hourly rows were written.
	Rows int
	// Bytes is how many bytes were persisted.
	Bytes int64
	// Subscriptions is how many subscriptions had their period usage updated.
	Subscriptions int
	// RolledBytes is traffic whose period rolled over between the drain and the
	// write. It is recorded in usage_hourly like everything else; it just
	// counts against no allowance, because the period it was moved in is over.
	RolledBytes int64
	// UnclaimedBytes is traffic that belonged to a user with no active
	// subscription. It is still recorded in usage_hourly - the operator can see
	// it - but it counts against no allowance because there is none.
	UnclaimedBytes int64
}

// FlushOnce drains the counters and writes them to disk.
//
// The counters are drained first, so a byte added while this runs lands in the
// next pass rather than being written twice. If the write then fails, the
// drained amounts are put back: losing them would silently under-bill, which is
// worse than retrying a row.
func (f *Flusher) FlushOnce(nowNs int64) (FlushResult, error) {
	var result FlushResult
	if f == nil || f.Counters == nil || f.Store == nil {
		return result, nil
	}
	if nowNs <= 0 {
		nowNs = f.now()
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.Counters.persistenceMu.Lock()
	defer f.Counters.persistenceMu.Unlock()

	keys := f.Counters.Keys()
	if len(keys) == 0 {
		return result, nil
	}
	drained := f.Counters.Drain(keys)
	if len(drained) == 0 {
		return result, nil
	}

	// Every bucket drained in this pass shares one hour: the hour the flush
	// ran in. A bucket cannot span an hour boundary, because a drain empties it
	// completely - anything added afterwards is the next pass's, in the next
	// hour if the boundary was crossed in between.
	hourTS := (nowNs / int64(time.Hour)) * int64(time.Hour)

	rows := make([]model.UsageRow, 0, len(drained))
	perUser := make(map[usageGroup]int64)
	for key, delta := range drained {
		rows = append(rows, model.UsageRow{
			KeyID:        key.KeyID,
			UserID:       key.UserID,
			PlatformID:   key.PlatformID,
			TSNs:         hourTS,
			IngressBytes: delta.IngressBytes,
			EgressBytes:  delta.EgressBytes,
			Requests:     delta.Requests,
			Errors:       delta.Errors,
		})
		if key.UserID != "" && delta.Total() > 0 {
			group := usageGroup{
				userID: key.UserID, subscriptionID: key.subscriptionID,
				periodStartNs: key.periodStartNs, generation: key.generation,
			}
			perUser[group] = saturatingBytesAdd(perUser[group], delta.Total())
		}
	}

	// The subscription update is normally a second write, but it is not a second
	// accounting: it is the same bytes, summed per user. StateRepo implements
	// AtomicUsageFlushStore, so its hourly rows and period increments commit as
	// one unit; generic stores retain the historical two-write fallback below.
	//
	// The billing window is snapshotted first, at drain time, and the write
	// below is guarded on that same snapshot. A rollover that lands in between
	// must not charge the new period for the previous one's traffic: those
	// bytes belong to a period that has already been closed and reset, and
	// billing them to its successor would take allowance away from a period
	// the user has not used yet.
	atomicStore, atomicFlush := f.Store.(AtomicUsageFlushStore)
	windows := make(map[usageGroup]subscriptionWindow, len(perUser))
	for group := range perUser {
		userID := group.userID
		if group.subscriptionID != "" {
			windows[group] = subscriptionWindow{id: group.subscriptionID, periodStartNs: group.periodStartNs}
			continue
		}
		subscription, err := f.Store.GetActiveUserSubscription(userID)
		if err != nil {
			f.logf("usage flush: look up subscription for user %s: %v", userID, err)
			if atomicFlush {
				// The atomic path must not commit the hourly row without a
				// chance to charge this user's period. Keep the bucket for a
				// later retry when the lookup is available again.
				f.requeue(drained)
				return result, fmt.Errorf("look up subscription for user %s: %w", userID, err)
			}
			continue
		}
		if subscription == nil {
			continue
		}
		windows[group] = subscriptionWindow{
			id:            subscription.ID,
			periodStartNs: subscription.PeriodStartNs,
		}
	}

	if atomicFlush {
		charges := make([]UsageCharge, 0, len(windows))
		for group, window := range windows {
			charges = append(charges, UsageCharge{
				Key: usageGroupKey(group), UserID: group.userID,
				SubscriptionID: window.id, PeriodStartNs: window.periodStartNs,
				Bytes: perUser[group],
			})
		}
		chargeResults, err := atomicStore.FlushUsageHourlyAndCharge(rows, charges)
		if err != nil {
			// The atomic store rolled the hourly rows back with the failed
			// period writes, so the drained buckets are safe to retry.
			f.requeue(drained)
			return result, fmt.Errorf("write usage and subscription charge: %w", err)
		}
		result.Rows = len(rows)
		for _, row := range rows {
			result.Bytes = saturatingBytesAdd(result.Bytes, saturatingBytesAdd(row.IngressBytes, row.EgressBytes))
		}
		for group, bytes := range perUser {
			userID := group.userID
			charge, ok := chargeResults[usageGroupKey(group)]
			if !ok {
				result.UnclaimedBytes = saturatingBytesAdd(result.UnclaimedBytes, bytes)
				continue
			}
			switch {
			case charge.Charged:
				result.Subscriptions++
				f.Counters.markPersistedGeneration(userID, bytes, group.generation)
			case charge.Rolled:
				result.RolledBytes = saturatingBytesAdd(result.RolledBytes, bytes)
			default:
				result.UnclaimedBytes = saturatingBytesAdd(result.UnclaimedBytes, bytes)
			}
		}
		return result, nil
	}

	if err := f.Store.UpsertUsageHourly(rows); err != nil {
		f.requeue(drained)
		return result, fmt.Errorf("write hourly usage: %w", err)
	}
	result.Rows = len(rows)
	for _, row := range rows {
		result.Bytes = saturatingBytesAdd(result.Bytes, saturatingBytesAdd(row.IngressBytes, row.EgressBytes))
	}

	for group, bytes := range perUser {
		userID := group.userID
		window, ok := windows[group]
		if !ok {
			result.UnclaimedBytes = saturatingBytesAdd(result.UnclaimedBytes, bytes)
			continue
		}
		charged, err := f.chargeSubscription(userID, window, bytes)
		switch {
		case err != nil:
			f.logf("usage flush: add %d bytes to subscription %s: %v", bytes, window.id, err)
			result.UnclaimedBytes = saturatingBytesAdd(result.UnclaimedBytes, bytes)
		case !charged:
			// A period boundary was crossed between the drain and the write.
			// The hourly rows still record the traffic; the period it belonged
			// to is gone.
			f.logf("usage flush: %d bytes for subscription %s arrived after its period rolled over", bytes, window.id)
			result.RolledBytes = saturatingBytesAdd(result.RolledBytes, bytes)
		default:
			result.Subscriptions++
			// Exactly what was charged, and only that: the water mark is what
			// keeps a later seed of this subscription from adding the same
			// bytes to the aggregate a second time. A refused or failed write
			// is not on disk, so it must not move the mark.
			f.Counters.markPersistedGeneration(userID, bytes, group.generation)
		}
	}

	return result, nil
}

// subscriptionWindow identifies one billing period of one subscription. It is
// the idempotency key of the period write: the same bytes are charged to the
// period the bytes were counted in.
type subscriptionWindow struct {
	id            string
	periodStartNs int64
}

func saturatingBytesAdd(current, delta int64) int64 {
	if delta <= 0 {
		return current
	}
	if current == math.MaxInt64 || delta > math.MaxInt64-current {
		return math.MaxInt64
	}
	return current + delta
}

// chargeSubscription adds bytes to a subscription's period usage, refusing to
// do so once the period has moved on.
//
// A store that implements PeriodGuardedFlushStore decides this atomically with
// the write. Any other store is guarded here, by re-reading the window and
// comparing it with the snapshot the bytes were drained under. Both routes
// answer the same question - "is this still the period these bytes belong to?"
// - and both are safer than a blind add, whose failure mode is charging a new
// period for the previous one's traffic.
func (f *Flusher) chargeSubscription(userID string, window subscriptionWindow, bytes int64) (bool, error) {
	if guarded, ok := f.Store.(PeriodGuardedFlushStore); ok {
		return guarded.AddSubscriptionUsedBytesInPeriod(window.id, window.periodStartNs, bytes)
	}

	current, err := f.Store.GetActiveUserSubscription(userID)
	if err != nil {
		return false, err
	}
	if current == nil {
		return false, nil
	}
	if current.ID != window.id || current.PeriodStartNs != window.periodStartNs {
		return false, nil
	}
	if err := f.Store.AddSubscriptionUsedBytes(window.id, bytes); err != nil {
		return false, err
	}
	return true, nil
}

// requeue puts drained amounts back after a failed write.
//
// The bytes are added back to the same buckets they came from, so the next pass
// writes them again. This is what keeps a database hiccup from silently
// under-billing: the alternative is traffic that was moved and never recorded.
//
// It restores the buckets only. The user aggregate is a running total of what
// arrived, not of what has been written out, and it was already incremented
// when the bytes were counted; adding the batch to it again here would make the
// next successful pass charge the subscription a second time for the same
// traffic.
func (f *Flusher) requeue(drained map[CounterKey]Delta) {
	f.Counters.Requeue(drained)
}

// Start runs a pass every interval until ctx is cancelled.
//
// The first pass is deliberately not immediate: a process that restarts in a
// loop should not hammer the database with empty flushes.
func (f *Flusher) Start(ctx context.Context) {
	if f == nil || f.Counters == nil || f.Store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(f.interval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// A final pass on the way out, so a clean shutdown loses nothing.
			// The context is already cancelled, so it gets its own deadline
			// rather than inheriting the cancelled one.
			f.flushOnShutdown()
			return
		case <-ticker.C:
			result, err := f.FlushOnce(0)
			if err != nil {
				f.logf("usage flush failed: %v", err)
				continue
			}
			if result.Rows > 0 {
				f.logf("usage flush: %d rows, %d bytes, %d subscriptions",
					result.Rows, result.Bytes, result.Subscriptions)
			}
		}
	}
}

// flushOnShutdown is the last pass, bounded so a stuck database cannot hold the
// process open.
func (f *Flusher) flushOnShutdown() {
	done := make(chan struct{})
	// The final flush is the last chance to record the last window: a panic
	// in it must still close done so the caller's timeout below applies.
	safego.Go("the final usage flush", func() {
		defer close(done)
		if _, err := f.FlushOnce(0); err != nil {
			f.logf("final usage flush failed: %v", err)
		}
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		f.logf("final usage aggregation timed out; durable byte reservations remain recoverable")
	}
}

// SeedFromSubscriptions restores the user aggregates after a restart.
//
// The counters are in memory and start empty, but a subscription's
// period_used_bytes is on disk and outlives the process. Without this the
// allowance would reset on every restart - the user would get their whole
// monthly traffic again each time the service bounced - so the persisted total
// is loaded back into the running aggregate the quota reads.
//
// It runs at startup, before the data plane accepts traffic, and again from the
// re-seed loop when that first call could not read the subscriptions. Both calls
// compute the same value, so the second changes nothing: what is added is the
// difference between the persisted total P and the water mark W, not P itself.
// W is the part of the aggregate the flusher has already charged to P, and those
// bytes are still in the aggregate (it is never drained), so adding the whole
// total again would bill the user twice for traffic that moved once. The
// difference is what is missing from memory, and after it is added the mark is
// P, which is what makes a repeated seed a no-op.
//
// A persisted total below the mark is left alone rather than subtracted. The
// bytes in the aggregate were counted by the data plane, and a disk total that
// has fallen since the mark was set is the signature of something outside this
// process: an operator resetting the period usage by hand, or a rollover whose
// ResetUser has not been observed yet. Subtracting on that evidence would give
// the user allowance back on the strength of a number that says nothing about
// what they moved, so the aggregate is kept and the mark still moves to P - the
// discrepancy is recorded, not re-examined on every pass. The rollover's own
// ResetUser, which zeroes the aggregate and the mark together, is the path that
// returns the allowance.
//
// It returns how many subscriptions had a restore applied, which is not how many
// it examined: a subscription whose bytes the flusher has already charged is
// examined and then restored by nothing.
func (c *Counters) SeedFromSubscriptions(subs []model.UserSubscription, nowNs int64) int {
	if c == nil || len(subs) == 0 {
		return 0
	}
	seeded := 0
	for _, subscription := range subs {
		if subscription.UserID == "" {
			continue
		}
		// Only the usage of a subscription that is still the user's current one
		// belongs in the aggregate: seeding from an old, replaced subscription
		// would hand the user a smaller allowance than they actually have.
		if subscription.Status != model.SubscriptionStatusActive {
			continue
		}
		if subscription.ExpiresAtNs > 0 && nowNs > 0 && subscription.ExpiresAtNs < nowNs {
			continue
		}
		seeded += c.seedSubscription(subscription)
	}
	return seeded
}

func (c *Counters) seedSubscription(subscription model.UserSubscription) int {
	c.persistenceMu.Lock()
	defer c.persistenceMu.Unlock()
	defer c.lockUser(subscription.UserID)()
	c.userMu.Lock()
	defer c.userMu.Unlock()
	seeded := 0
	if c.periods != nil {
		if period, ok := c.periods[subscription.UserID]; !ok ||
			(period.subscriptionID == "" && period.periodStartNs == 0) {
			period.subscriptionID = subscription.ID
			period.periodStartNs = subscription.PeriodStartNs
			period.planPeriodIndex = subscription.PeriodIndex
			c.periods[subscription.UserID] = period
		} else if period.subscriptionID != subscription.ID || period.periodStartNs != subscription.PeriodStartNs {
			return 0
		}
	}
	if subscription.PeriodUsedBytes <= 0 {
		return seeded
	}
	mark := c.persistedFor(subscription.UserID)
	if mark == nil {
		// No water mark to compare against. Only a counter set built by
		// hand rather than by NewCounters gets here, and counting the
		// total as unseeded is the conservative answer: it can make the
		// aggregate too large, never too small.
		total, _ := c.users.LoadOrCompute(subscription.UserID, func() (*atomic.Int64, bool) {
			return new(atomic.Int64), false
		})
		atomicAddSaturating(total, subscription.PeriodUsedBytes)
		seeded++
		return seeded
	}
	if restore := subscription.PeriodUsedBytes - mark.Load(); restore > 0 {
		total, _ := c.users.LoadOrCompute(subscription.UserID, func() (*atomic.Int64, bool) {
			return new(atomic.Int64), false
		})
		atomicAddSaturating(total, restore)
		seeded++
	}
	// An older database snapshot must never lower an advanced watermark.
	if subscription.PeriodUsedBytes > mark.Load() {
		mark.Store(subscription.PeriodUsedBytes)
	}
	return seeded
}
