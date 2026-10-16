package service

import (
	"sync"
	"time"
)

// SubscriptionSweepInterval is how often a deployment closes out the
// subscriptions whose window has passed.
//
// Five minutes is chosen against the two things the sweep changes. For the
// status-only reads (the console's "active" tiles, ListActiveUserSubscriptions)
// it is a reporting lag, and five minutes sits inside the noise of a console
// view. For the period worker it bounds how often a dead subscription can still
// be rolled: the worker runs once a minute (usage.DefaultPeriodCheckInterval),
// so a window that closes just after a sweep is rolled a handful of times before
// the next one balances a counter nothing can spend. Both are minutes-scale
// effects, which is what lets the sweep be this cheap - one UPDATE over an
// indexed status - and still frequent. It is deliberately not the period
// worker's interval: the sweep is about the stored status, the worker is about
// allowances.
const SubscriptionSweepInterval = 5 * time.Minute

// SweepExpiredSubscriptions closes out every active subscription whose window has
// passed before nowNs, and reports how many rows it moved to 'expired'.
//
// The write itself is the repository's ExpireStaleSubscriptions: one UPDATE, no
// read-modify-write (see the long comment there for the "never expires" sentinel
// and the lost-update window that shape avoids). This method is the service
// layer's door to it, so the loop below - and any future caller with a state
// engine - has the same input validation and error mapping as the other
// background entry points (ScanUsageAlerts).
//
// Access decisions do not depend on the status this moves. The paths that decide
// whether a request is served already compare expires_at_ns with the clock, and
// they keep doing so between sweeps, so nothing becomes servable or unservable
// because a sweep has or has not run yet:
//
//	by expires_at_ns:
//	  - keys.KeyState.Usable - internal/keys/store.go:82 (the key's own window)
//	    and :91 (the plan snapshot's)
//	  - the published plan snapshot - internal/keys/publish.go:50 carries
//	    ExpiresAtNs next to the status
//	  - the startup usage seed - internal/usage/flush.go:392 skips a
//	    subscription that is past its expiry
//
//	by status alone:
//	  - state.ListActiveUserSubscriptions - internal/state/repo_user.go:1379,
//	    the read behind "who is on a plan right now"
//	  - the console summary - internal/service/control_plane_usage.go:95-116
//	    builds ActiveUsers / ExpiringSoonUsers / PeriodBytes from those rows
//	  - activeSubscriptionFor - control_plane_tenant_subscription.go:439
//	  - maxKeysForPlan - control_plane_accesskey.go:427
//	  - the period worker - internal/usage/period.go:122-130 reads every row and
//	    skips the ones that are not active
//
//	after the sweep:
//	  - the status-only readers stop seeing a subscription whose window has
//	    passed, which is the skew this exists to remove
//	  - the period worker no longer rolls those subscriptions: its own
//	    ResetSubscriptionPeriod never moves the window (it writes period_start_ns
//	    and period_used_bytes only), so a row it had been advancing was already
//	    dead, and the allowance it kept rebalancing was one nobody could spend.
//	    That is the expected effect, not a side effect: a subscription is rolled
//	    while it is in force, and closed out once it is not.
//
// A row that never expires (expires_at_ns = 0, the value 000021 writes) is left
// alone, and so is a row that is already suspended or expired.
func (s *ControlPlaneService) SweepExpiredSubscriptions(nowNs int64) (int, error) {
	if s == nil || s.Engine == nil {
		return 0, internal("no state engine", nil)
	}
	// Zero is the value the readers treat as "no window has passed"; refusing it
	// is cheaper than explaining a sweep that closed nothing.
	if nowNs <= 0 {
		return 0, invalidArg("now_ns: must be positive")
	}

	expired, err := s.Engine.ExpireStaleSubscriptions(nowNs)
	if err != nil {
		return 0, internal("expire stale subscriptions", err)
	}
	return expired, nil
}

// StartSubscriptionExpirySweeper runs SweepExpiredSubscriptions every interval
// and reports what it closed through logf, until the returned function is called.
//
// The loop never stops on a sweep error, for the same reason the usage alert
// scanner does not: a background component that dies on a transient database
// error leaves the statuses behind forever, which is the state this exists to
// end. A failure is logged and retried at the next tick, and a cancelled sweep
// (a shutdown in progress) is not an error an operator needs to see.
//
// The returned stop function is idempotent, like the other Start helpers in the
// tree (StartUsageAlertScanner, usage.Retention.Start,
// metrics.StartRetentionPruner): stopping an already stopped sweeper is a no-op
// rather than a panic on the closed channel, which is what a shutdown path that
// runs twice would hit. Each call still waits for the goroutine to exit, so once
// stop has returned no further sweep can start - and no further write can reach
// state.db, which is why the shutdown sequence stops this before the database is
// closed.
func StartSubscriptionExpirySweeper(cp *ControlPlaneService, interval time.Duration, logf func(string, ...any)) func() {
	if interval <= 0 {
		interval = SubscriptionSweepInterval
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				expired, err := cp.SweepExpiredSubscriptions(time.Now().UnixNano())
				if err != nil {
					logf("subscription expiry sweep failed: %v", err)
					continue
				}
				if expired > 0 {
					logf("subscriptions expired: %d closed out", expired)
				}
			}
		}
	}()
	// close(stop) happens exactly once; the wait for done stays outside the Once,
	// so a repeated call returns only after the loop has really stopped.
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}
