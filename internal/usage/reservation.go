package usage

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"prism/internal/keys"
)

// UnknownTrafficBytes tells a completion that I/O was interrupted before its
// accepted byte count became known. It must never be interpreted as zero.
const UnknownTrafficBytes int64 = -1

// ReserveTraffic debits the tenant's shared allowance before bytes are handed
// to a peer. The completion records only transferred bytes and refunds a short
// read/write. Reservations keep their period so a late completion cannot charge
// or refund the next subscription window. No lock is held during network I/O.
// A negative completion retains the reservation; durable accounting also closes
// the user's access until audited offline reconciliation establishes the amount.
func (g *Gate) ReserveTraffic(key *keys.KeyState, platformID string, ingress bool, requested int64) (int64, func(int64)) {
	noop := func(int64) {}
	if g == nil || g.Limiter == nil || g.Limiter.counters == nil || key == nil || key.KeyID == "" || key.UserID == "" || requested <= 0 {
		return 0, noop
	}
	c := g.Limiter.counters
	if !c.SeedReady() {
		return 0, noop
	}
	if g.Journal != nil {
		return g.reserveDurableTraffic(key, platformID, ingress, requested)
	}
	unlock := c.lockUser(key.UserID)
	if g.Keys != nil {
		current, ok := g.Keys.Get(key.KeyID)
		if !ok || current.UserID != key.UserID || current.SessionEpoch != key.SessionEpoch || !current.Usable(time.Now().UnixNano()) {
			unlock()
			return 0, noop
		}
		key = current
	}
	c.userMu.Lock()
	period := c.periods[key.UserID]
	if g.Keys != nil && period.subscriptionID != "" &&
		(period.subscriptionID != key.Plan.SubscriptionID || period.planPeriodIndex != key.Plan.PeriodIndex) {
		c.userMu.Unlock()
		unlock()
		return 0, noop
	}
	bucket := CounterKey{KeyID: key.KeyID, UserID: key.UserID, PlatformID: platformID,
		generation: period.generation, subscriptionID: period.subscriptionID, periodStartNs: period.periodStartNs}
	total, _ := c.users.LoadOrCompute(key.UserID, func() (*atomic.Int64, bool) {
		return new(atomic.Int64), false
	})
	limit := key.Plan.TrafficBytes
	if limit <= 0 {
		limit = math.MaxInt64
	}
	var reserved int64
	for {
		used := total.Load()
		if used >= limit {
			reserved = 0
			break
		}
		reserved = min(requested, limit-used)
		if total.CompareAndSwap(used, used+reserved) {
			break
		}
	}
	c.reserved[key.UserID] += reserved
	c.userMu.Unlock()
	unlock()
	if reserved == 0 {
		return 0, noop
	}
	var once sync.Once
	return reserved, func(transferred int64) {
		once.Do(func() {
			if transferred < 0 {
				return // retain the outstanding allowance, never invent a refund
			}
			transferred = max(0, min(reserved, transferred))
			defer c.lockUser(key.UserID)()
			c.userMu.Lock()
			defer c.userMu.Unlock()
			current, present := c.users.Load(key.UserID)
			if present && current == total && c.periods[key.UserID] == period {
				total.Add(transferred - reserved)
				c.reserved[key.UserID] -= reserved
			}
			delta := Delta{EgressBytes: transferred}
			if ingress {
				delta = Delta{IngressBytes: transferred}
			}
			if transferred > 0 {
				c.addToBuckets(bucket, delta)
			}
		})
	}
}
