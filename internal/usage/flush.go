package usage

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"prism/internal/model"
)

// Flush scheduling defaults.
const (
	// DefaultFlushInterval is how often the in-memory counters reach disk.
	//
	// It is the bound on how much traffic a crash can lose, and the plan's
	// acceptance criterion is "usage is off by no more than ten seconds of
	// traffic", so ten seconds it is. It is also short enough that a console
	// reading the tables sees a live number rather than one from minutes ago.
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

// Flusher writes the in-memory counters to the usage tables.
//
// The data plane never touches the database: every byte is an in-memory atomic
// and this worker is the only thing that persists them. That is what keeps the
// proxy's hot path free of disk I/O, at the cost of a bounded window of traffic
// that a hard crash would lose - the flush interval.
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
	perUser := make(map[string]int64)
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
		if key.UserID != "" {
			perUser[key.UserID] += delta.Total()
		}
	}

	if err := f.Store.UpsertUsageHourly(rows); err != nil {
		f.requeue(drained)
		return result, fmt.Errorf("write hourly usage: %w", err)
	}
	result.Rows = len(rows)
	for _, row := range rows {
		result.Bytes += row.IngressBytes + row.EgressBytes
	}

	// The subscription update is a second write, but it is not a second
	// accounting: it is the same bytes, summed per user. It is not put back on
	// failure either - the hourly rows are the record of what moved, and
	// period_used_bytes is a derived running total the next pass will add to.
	// Requeueing here would double-count against the allowance.
	//
	// The billing window is snapshotted first, at drain time, and the write
	// below is guarded on that same snapshot. A rollover that lands in between
	// must not charge the new period for the previous one's traffic: those
	// bytes belong to a period that has already been closed and reset, and
	// billing them to its successor would take allowance away from a period
	// the user has not used yet.
	windows := make(map[string]subscriptionWindow, len(perUser))
	for userID, bytes := range perUser {
		subscription, err := f.Store.GetActiveUserSubscription(userID)
		if err != nil {
			f.logf("usage flush: look up subscription for user %s: %v", userID, err)
			result.UnclaimedBytes += bytes
			continue
		}
		if subscription == nil {
			result.UnclaimedBytes += bytes
			continue
		}
		windows[userID] = subscriptionWindow{
			id:            subscription.ID,
			periodStartNs: subscription.PeriodStartNs,
		}
	}

	for userID, bytes := range perUser {
		window, ok := windows[userID]
		if !ok {
			continue // already counted as unclaimed above
		}
		charged, err := f.chargeSubscription(userID, window, bytes)
		switch {
		case err != nil:
			f.logf("usage flush: add %d bytes to subscription %s: %v", bytes, window.id, err)
			result.UnclaimedBytes += bytes
		case !charged:
			// A period boundary was crossed between the drain and the write.
			// The hourly rows still record the traffic; the period it belonged
			// to is gone.
			f.logf("usage flush: %d bytes for subscription %s arrived after its period rolled over", bytes, window.id)
			result.RolledBytes += bytes
		default:
			result.Subscriptions++
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
	go func() {
		defer close(done)
		if _, err := f.FlushOnce(0); err != nil {
			f.logf("final usage flush failed: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		f.logf("final usage flush timed out; up to %s of traffic may be unrecorded", f.interval())
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
// It is called once at startup, before the data plane accepts traffic.
func (c *Counters) SeedFromSubscriptions(subs []model.UserSubscription, nowNs int64) int {
	if c == nil || len(subs) == 0 {
		return 0
	}
	seeded := 0
	for _, subscription := range subs {
		if subscription.UserID == "" || subscription.PeriodUsedBytes <= 0 {
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
		total, _ := c.users.LoadOrCompute(subscription.UserID, func() (*atomic.Int64, bool) {
			return new(atomic.Int64), false
		})
		total.Add(subscription.PeriodUsedBytes)
		seeded++
	}
	return seeded
}
