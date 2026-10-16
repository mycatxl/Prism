package usage

import (
	"testing"
	"time"

	"prism/internal/keys"
	"prism/internal/model"
)

// The billing window comes from the subscription's own plan snapshot. It is the
// plan as it was sold, the subscription carries it, and it keeps rolling the
// subscription over on the period it was bought under even after the plan has
// been edited - which is what stops an operator's plan change from moving a
// boundary the user never agreed to.
func TestPeriodReset_UsesThePeriodFromTheSubscriptionSnapshot(t *testing.T) {
	store := newFakePeriodStore()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 0, 5) // five days on: a day has elapsed, a month has not

	snapshot, err := keys.MarshalPlanSnapshot(model.Plan{ID: "p-1", Period: "day"})
	if err != nil {
		t.Fatalf("MarshalPlanSnapshot: %v", err)
	}
	sub := activePeriodSub("s-1", "u-1", "p-1", start.UnixNano())
	sub.PlanSnapshotJSON = snapshot
	store.subs = []model.UserSubscription{sub}
	// The live plan says something else entirely: it was edited after the
	// subscription was opened.
	store.plans["p-1"] = &model.Plan{ID: "p-1", Period: "month"}

	result, err := (&PeriodReset{Store: store}).RunOnce(now.UnixNano())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Rolled != 1 {
		t.Fatalf("rolled %d subscriptions, want 1: the snapshot's daily period was not used", result.Rolled)
	}
	if store.planMisses != 0 {
		t.Fatal("the live plan was consulted even though the snapshot carries the period")
	}
	resets := store.resets()
	if len(resets) != 1 {
		t.Fatalf("%d period resets, want 1", len(resets))
	}
	if resets[0].toNs != now.UnixNano() {
		t.Fatalf("the new period starts at %d, want %d: the rollover did not follow the daily period", resets[0].toNs, now.UnixNano())
	}
}

// A snapshot that carries no period leaves the live plan as the next source,
// which is the pre-snapshot behaviour for subscriptions written before the
// field existed.
func TestPeriodReset_UsesTheLivePlanWhenTheSnapshotHasNoPeriod(t *testing.T) {
	store := newFakePeriodStore()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 0, 2)

	snapshot, err := keys.MarshalPlanSnapshot(model.Plan{ID: "p-1", TrafficBytes: 1 << 20})
	if err != nil {
		t.Fatalf("MarshalPlanSnapshot: %v", err)
	}
	sub := activePeriodSub("s-1", "u-1", "p-1", start.UnixNano())
	sub.PlanSnapshotJSON = snapshot
	store.subs = []model.UserSubscription{sub}
	store.plans["p-1"] = &model.Plan{ID: "p-1", Period: "day"}

	result, err := (&PeriodReset{Store: store}).RunOnce(now.UnixNano())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Rolled != 1 {
		t.Fatalf("rolled %d subscriptions, want 1: the live plan's period was not used", result.Rolled)
	}
	resets := store.resets()
	if len(resets) != 1 || resets[0].toNs != now.UnixNano() {
		t.Fatalf("resets = %+v, want one starting at %d", resets, now.UnixNano())
	}
}

// An unreadable snapshot is treated as one with no period rather than as a hard
// failure: a subscription whose snapshot was written by an older schema still
// has to roll over.
func TestPeriodReset_UnreadableSnapshotFallsBackToTheLivePlan(t *testing.T) {
	store := newFakePeriodStore()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 0, 2)

	sub := activePeriodSub("s-1", "u-1", "p-1", start.UnixNano())
	sub.PlanSnapshotJSON = "{not json"
	store.subs = []model.UserSubscription{sub}
	store.plans["p-1"] = &model.Plan{ID: "p-1", Period: "day"}

	result, err := (&PeriodReset{Store: store}).RunOnce(now.UnixNano())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Rolled != 1 {
		t.Fatalf("rolled %d subscriptions, want 1", result.Rolled)
	}
}
