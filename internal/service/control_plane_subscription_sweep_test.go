package service

import (
	"testing"
	"time"

	"prism/internal/model"
)

// sweepTestSubscription puts one account on a plan and returns its active
// subscription with the window it is given.
//
// The fixture's "now" is fixed in 2023, so the 30 days the plan grants are
// already past as far as the wall clock is concerned - the sweeper reads
// time.Now(), and anything that is meant to be in force has to be given a
// window relative to that, not to the fixture's nowNs.
func sweepTestSubscription(t *testing.T, cp *ControlPlaneService, nowNs int64, username string, expiresAtNs int64) model.UserSubscription {
	t.Helper()

	sub, _ := alertTestSubscription(t, cp, nowNs, username, 1<<30)
	sub.Status = model.SubscriptionStatusActive
	sub.ExpiresAtNs = expiresAtNs
	if err := cp.Engine.UpdateUserSubscriptionTerms(sub); err != nil {
		t.Fatalf("UpdateUserSubscriptionTerms(%s): %v", sub.ID, err)
	}
	return sub
}

func subscriptionStatusNow(t *testing.T, cp *ControlPlaneService, id string) string {
	t.Helper()
	sub, err := cp.Engine.GetUserSubscription(id)
	if err != nil {
		t.Fatalf("GetUserSubscription(%s): %v", id, err)
	}
	return sub.Status
}

// One pass closes out the window that has passed and leaves the rest alone. The
// other two cases are the ones a naive sweep would break: a subscription that is
// still inside its window, and the expires_at_ns = 0 row 000021 writes for the
// accounts that predate plans, which means "never expires" rather than 1970.
func TestSubscriptionExpirySweep_ClosesOutThePastWindowAndNothingElse(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	dead := sweepTestSubscription(t, cp, nowNs, "sweep_dead", nowNs-int64(time.Minute))
	live := sweepTestSubscription(t, cp, nowNs, "sweep_live", time.Now().Add(time.Hour).UnixNano())
	never := sweepTestSubscription(t, cp, nowNs, "sweep_never", 0)

	expired, err := cp.SweepExpiredSubscriptions(time.Now().UnixNano())
	if err != nil {
		t.Fatalf("SweepExpiredSubscriptions: %v", err)
	}
	if expired != 1 {
		t.Fatalf("the sweep closed out %d subscription(s), want 1", expired)
	}

	if got := subscriptionStatusNow(t, cp, dead.ID); got != model.SubscriptionStatusExpired {
		t.Errorf("the subscription whose window passed: got status %q, want %q", got, model.SubscriptionStatusExpired)
	}
	if got := subscriptionStatusNow(t, cp, live.ID); got != model.SubscriptionStatusActive {
		t.Errorf("a subscription inside its window was closed out: got status %q, want %q", got, model.SubscriptionStatusActive)
	}
	if got := subscriptionStatusNow(t, cp, never.ID); got != model.SubscriptionStatusActive {
		t.Errorf("a subscription that never expires was closed out: got status %q, want %q", got, model.SubscriptionStatusActive)
	}

	// The read the sweep exists for: the console's "who is on a plan right now"
	// no longer counts the subscription whose window has passed.
	active, err := cp.Engine.ListActiveUserSubscriptions()
	if err != nil {
		t.Fatalf("ListActiveUserSubscriptions: %v", err)
	}
	for _, sub := range active {
		if sub.ID == dead.ID {
			t.Errorf("%s is still returned as an active subscription after the sweep", dead.ID)
		}
	}

	// A second pass has nothing left to match, which is what lets the ticker
	// below be unconditional.
	again, err := cp.SweepExpiredSubscriptions(time.Now().UnixNano())
	if err != nil {
		t.Fatalf("SweepExpiredSubscriptions (second pass): %v", err)
	}
	if again != 0 {
		t.Fatalf("the second sweep closed out %d subscription(s), want 0", again)
	}
}

// The loop sweeps on its own, stops idempotently, and really stops: a
// subscription that expires after the stop keeps its status, so a shutdown path
// that leaves the goroutine behind would show up here.
func TestSubscriptionExpirySweeper_StopIsIdempotentAndFinal(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	dead := sweepTestSubscription(t, cp, nowNs, "sweep_loop_dead", nowNs-int64(time.Minute))
	live := sweepTestSubscription(t, cp, nowNs, "sweep_loop_live", time.Now().Add(time.Hour).UnixNano())

	const interval = 5 * time.Millisecond
	stop := StartSubscriptionExpirySweeper(cp, interval, nil)
	deadline := time.Now().Add(5 * time.Second)

	// 1. The loop closes the window out with no manual sweep.
	for subscriptionStatusNow(t, cp, dead.ID) != model.SubscriptionStatusExpired && time.Now().Before(deadline) {
		time.Sleep(interval)
	}
	if got := subscriptionStatusNow(t, cp, dead.ID); got != model.SubscriptionStatusExpired {
		t.Fatalf("the sweeper never closed out %s: status %q", dead.ID, got)
	}
	// Running on its own did not take the subscription that is still in force.
	if got := subscriptionStatusNow(t, cp, live.ID); got != model.SubscriptionStatusActive {
		t.Fatalf("the sweeper closed out %s, which is inside its window: status %q", live.ID, got)
	}

	// 2. Stopping twice must not panic, and both calls must return: the first
	// waits for the goroutine, the second is a no-op on the closed channel.
	for call := 1; call <= 2; call++ {
		startedAt := time.Now()
		stopMustNotPanic(t, stop)
		if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
			t.Fatalf("stop call %d took %s, want a prompt return", call, elapsed)
		}
	}

	// 3. Nothing sweeps after the stop returned: a subscription that expires now
	// keeps its status for many ticks, which a running loop could not do.
	after := sweepTestSubscription(t, cp, nowNs, "sweep_loop_after", nowNs-int64(time.Minute))
	time.Sleep(20 * interval)
	if got := subscriptionStatusNow(t, cp, after.ID); got != model.SubscriptionStatusActive {
		t.Fatalf("the sweeper kept running after stop: %s reads %q, want %q", after.ID, got, model.SubscriptionStatusActive)
	}
}

// The entry point validates its input the way the other background scans do, and
// a nil logger or a zero interval falls back to the shipped defaults instead of
// panicking on the way in.
func TestSubscriptionExpirySweep_ValidatesItsInputs(t *testing.T) {
	now := time.Now().UnixNano()

	// A service with no engine has nothing to write to, and a zero timestamp is
	// the value the readers treat as "no window has passed".
	if _, err := (&ControlPlaneService{}).SweepExpiredSubscriptions(now); err != nil {
		assertServiceErrorCode(t, err, "INTERNAL")
	} else {
		t.Error("sweeping without a state engine reported success")
	}
	var absent *ControlPlaneService
	if _, err := absent.SweepExpiredSubscriptions(now); err != nil {
		assertServiceErrorCode(t, err, "INTERNAL")
	} else {
		t.Error("sweeping a nil service reported success")
	}
	cp, _, _, _ := newTenantTestService(t)
	if _, err := cp.SweepExpiredSubscriptions(0); err != nil {
		assertServiceErrorCode(t, err, "INVALID_ARGUMENT")
	} else {
		t.Error("sweeping with a zero timestamp reported success")
	}

	// A zero interval takes the shipped five minutes, and a nil logf is silence:
	// both must survive a start and an idempotent stop.
	stop := StartSubscriptionExpirySweeper(&ControlPlaneService{}, 0, nil)
	stopMustNotPanic(t, stop)
}
