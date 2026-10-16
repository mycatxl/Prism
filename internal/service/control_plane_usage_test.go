package service

import (
	"testing"
	"time"
)

// The dashboard's "expiring soon" count is a list of people to call, so a
// subscription whose window has already passed must not land in it: it cannot
// be renewed, it just has not been closed out by the sweep yet
// (SubscriptionSweepInterval, five minutes at most). These tests pin that
// lower bound, and the two ends of the horizon it opens and closes.

// usageHorizonEnd is the last instant inside the window, nowNs + 7 days.
func usageHorizonEnd(nowNs int64) int64 {
	return nowNs + int64(expiringSoonDays)*int64(24*time.Hour)
}

// TestUsageSummary_ExpiringSoonCountsOnlyWindowsStillInForce walks the four
// shapes an operator can meet on the dashboard at once: a window that has
// already passed, one ending in three days, one ending in thirty days, and the
// expires_at_ns = 0 row the backfill writes for "never expires". Only the
// three-day window is a renewal the operator can still act on.
func TestUsageSummary_ExpiringSoonCountsOnlyWindowsStillInForce(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	passed := sweepTestSubscription(t, cp, nowNs, "usage_expiring_passed", nowNs-int64(time.Minute))
	soon := sweepTestSubscription(t, cp, nowNs, "usage_expiring_soon", nowNs+int64(3*24*time.Hour))
	late := sweepTestSubscription(t, cp, nowNs, "usage_expiring_late", nowNs+int64(30*24*time.Hour))
	never := sweepTestSubscription(t, cp, nowNs, "usage_expiring_never", 0)

	summary, err := cp.UsageSummary(nowNs)
	if err != nil {
		t.Fatalf("UsageSummary: %v", err)
	}
	if summary.ExpiringWithinDays != expiringSoonDays {
		t.Fatalf("ExpiringWithinDays = %d, want %d", summary.ExpiringWithinDays, expiringSoonDays)
	}
	if summary.ExpiringSoonUsers != 1 {
		// The window that has passed is the row a horizon without a lower
		// bound counts, so it is reported on its own line first.
		t.Errorf("usage_expiring_passed (expires_at_ns = %d) ended before now (%d): that window is already over and cannot be renewed, so it must not be counted as expiring soon",
			passed.ExpiresAtNs, nowNs)
		t.Errorf("usage_expiring_soon ends at %d, usage_expiring_late at %d and usage_expiring_never is %d, the \"never expires\" sentinel: only the first of the three is inside (nowNs, nowNs+%d]",
			soon.ExpiresAtNs, late.ExpiresAtNs, never.ExpiresAtNs, usageHorizonEnd(nowNs)-nowNs)
		t.Fatalf("ExpiringSoonUsers = %d, want 1 (usage_expiring_soon)", summary.ExpiringSoonUsers)
	}
}

// TestUsageSummary_ExpiringSoonPinsTheHorizonEdges pins the two values a
// rewritten bound gets wrong: a subscription ending exactly now is already over
// - the sweep closes it out on expires_at_ns <= now, the same instant this read
// stops counting - and one ending exactly at the end of the seventh day is
// still inside the window.
func TestUsageSummary_ExpiringSoonPinsTheHorizonEdges(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	endsNow := sweepTestSubscription(t, cp, nowNs, "usage_edge_now", nowNs)
	endsAtHorizon := sweepTestSubscription(t, cp, nowNs, "usage_edge_horizon", usageHorizonEnd(nowNs))

	summary, err := cp.UsageSummary(nowNs)
	if err != nil {
		t.Fatalf("UsageSummary: %v", err)
	}
	if summary.ExpiringSoonUsers != 1 {
		t.Errorf("usage_edge_now (expires_at_ns = %d) ends exactly at now (%d): its window is over, so it must not be counted", endsNow.ExpiresAtNs, nowNs)
		t.Errorf("usage_edge_horizon (expires_at_ns = %d) is the last instant inside the horizon (%d) and must be counted", endsAtHorizon.ExpiresAtNs, usageHorizonEnd(nowNs))
		t.Fatalf("ExpiringSoonUsers = %d, want 1 (usage_edge_horizon)", summary.ExpiringSoonUsers)
	}
}
