package usage

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"prism/internal/keys"
	"prism/internal/model"
)

func (g *Gate) reserveDurableTraffic(key *keys.KeyState, platformID string, ingress bool, requested int64) (int64, func(int64)) {
	noop := func(int64) {}
	c := g.Limiter.counters
	// Match seeding/flush lock order. Never hold userMu across a disk operation.
	c.persistenceMu.Lock()
	defer c.persistenceMu.Unlock()
	unlock := c.lockUser(key.UserID)
	defer unlock()
	key = g.currentKey(key)
	if key == nil || g.Journal.Blocked(key.UserID) {
		return 0, noop
	}
	c.userMu.Lock()
	period := c.periods[key.UserID]
	if g.Keys != nil && period.subscriptionID != "" && (period.subscriptionID != key.Plan.SubscriptionID || period.planPeriodIndex != key.Plan.PeriodIndex) {
		c.userMu.Unlock()
		return 0, noop
	}
	total, _ := c.users.LoadOrCompute(key.UserID, func() (*atomic.Int64, bool) { return new(atomic.Int64), false })
	limit := key.Plan.TrafficBytes
	if limit <= 0 {
		limit = math.MaxInt64
	}
	reserved := min(requested, max(0, limit-total.Load()))
	c.userMu.Unlock()
	if reserved == 0 {
		return 0, noop
	}
	v := model.UsageReservation{ID: uuid.NewString(), KeyID: key.KeyID, UserID: key.UserID, PlatformID: platformID,
		SubscriptionID: period.subscriptionID, PeriodStartNs: period.periodStartNs, HourNs: time.Now().UTC().Truncate(time.Hour).UnixNano(), Bytes: reserved, Ingress: ingress}
	if err := g.Journal.store.ReserveUsage(v); err != nil {
		// No bytes were handed out. Even an uncertain INSERT commit can therefore
		// be safely reconciled to zero, with the affected user closed until success.
		g.Journal.queue(v.ID, v.UserID, func() error { _, err := g.Journal.store.SettleUsage(v.ID, 0); return err })
		return 0, noop
	}
	c.userMu.Lock()
	total.Add(reserved)
	c.reserved[key.UserID] += reserved
	c.userMu.Unlock()
	var once sync.Once
	return reserved, func(actual int64) {
		once.Do(func() {
			if actual < 0 {
				c.persistenceMu.Lock()
				defer c.persistenceMu.Unlock()
				g.Journal.unknown(v.UserID)
				return
			}
			actual = max(0, min(reserved, actual))
			var apply func() error
			apply = func() error {
				c.persistenceMu.Lock()
				defer c.persistenceMu.Unlock()
				defer c.lockUser(v.UserID)()
				if _, err := g.Journal.store.SettleUsage(v.ID, actual); err != nil {
					// Publish the hold before releasing the admission lock. Otherwise
					// another stream can reserve in the failure-to-queue window.
					g.Journal.queue(v.ID, v.UserID, apply)
					return err
				}
				c.userMu.Lock()
				defer c.userMu.Unlock()
				current, present := c.users.Load(v.UserID)
				active := c.periods[v.UserID]
				// A historical window may have been reactivated. Its outstanding
				// reservations were restored too, so settle by durable identity even
				// though the in-memory generation changed in the meantime.
				restored := c.resumedReservations[v.UserID][v.ID]
				sameWindow := active == period || restored && active.subscriptionID == period.subscriptionID && active.periodStartNs == period.periodStartNs
				if present && current == total && sameWindow {
					total.Add(actual - reserved)
					c.reserved[v.UserID] -= reserved
					if mark := c.persistedFor(v.UserID); mark != nil {
						mark.Add(actual)
					}
					delete(c.resumedReservations[v.UserID], v.ID)
					if len(c.resumedReservations[v.UserID]) == 0 {
						delete(c.resumedReservations, v.UserID)
					}
				}
				// Bytes are already in hourly history. Sending them through the async
				// buckets would debit twice. Request/error counts still use the flusher.
				return nil
			}
			_ = apply() // failures are queued while admission is still excluded
		})
	}
}
