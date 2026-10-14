package state

import (
	"errors"
	"testing"

	"prism/internal/model"
)

// seedSubscription inserts a user, a plan and a subscription, returning the
// subscription id. It is the smallest fixture that exercises the period reset.
func seedSubscription(t *testing.T, repo *StateRepo, periodStartNs, usedBytes int64) string {
	t.Helper()

	if err := repo.InsertUser(model.User{
		ID:       "u-period",
		Username: "period-user",
		Role:     model.UserRoleUser,
		Status:   model.UserStatusActive,
	}); err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	if err := repo.UpsertUserSubscription(model.UserSubscription{
		ID:               "s-period",
		UserID:           "u-period",
		PlanSnapshotJSON: `{}`,
		StartsAtNs:       periodStartNs,
		ExpiresAtNs:      periodStartNs + 1_000_000_000,
		PeriodStartNs:    periodStartNs,
		PeriodUsedBytes:  usedBytes,
		Status:           model.SubscriptionStatusActive,
	}); err != nil {
		t.Fatalf("UpsertUserSubscription: %v", err)
	}
	return "s-period"
}

// A reset balances the counter and moves the period in one write.
func TestStateRepo_ResetSubscriptionPeriod(t *testing.T) {
	repo := newTestStateRepo(t)
	start := int64(1_700_000_000_000_000_000)
	id := seedSubscription(t, repo, start, 4096)

	next := start + 2_592_000_000_000_000 // 30 days in ns
	if err := repo.ResetSubscriptionPeriod(id, start, next); err != nil {
		t.Fatalf("ResetSubscriptionPeriod: %v", err)
	}

	sub, err := repo.GetUserSubscription(id)
	if err != nil {
		t.Fatalf("GetUserSubscription: %v", err)
	}
	if sub.PeriodStartNs != next {
		t.Fatalf("period start = %d, want %d", sub.PeriodStartNs, next)
	}
	if sub.PeriodUsedBytes != 0 {
		t.Fatalf("period usage = %d after the reset, want 0", sub.PeriodUsedBytes)
	}
}

// The guard on the period start is what keeps two writers from both resetting:
// the second one matches nothing and leaves the fresh period alone.
func TestStateRepo_ResetSubscriptionPeriodIsGuardedOnTheObservedStart(t *testing.T) {
	repo := newTestStateRepo(t)
	start := int64(1_700_000_000_000_000_000)
	id := seedSubscription(t, repo, start, 4096)

	next := start + 1_000
	if err := repo.ResetSubscriptionPeriod(id, start, next); err != nil {
		t.Fatalf("first reset: %v", err)
	}

	// A second worker that read the old start tries to reset again. If the
	// guard were missing, this would wipe usage that belongs to the period that
	// just began.
	if err := repo.ResetSubscriptionPeriod(id, start, next+1); err != nil {
		t.Fatalf("a lost race must not be an error, got %v", err)
	}

	sub, err := repo.GetUserSubscription(id)
	if err != nil {
		t.Fatalf("GetUserSubscription: %v", err)
	}
	if sub.PeriodStartNs != next {
		t.Fatalf("the second reset moved the period to %d, want it left at %d", sub.PeriodStartNs, next)
	}
}

// A subscription that does not exist is reported as such, so a caller can tell
// a deleted row from a lost race.
func TestStateRepo_ResetSubscriptionPeriodReportsAMissingRow(t *testing.T) {
	repo := newTestStateRepo(t)

	err := repo.ResetSubscriptionPeriod("s-absent", 1, 2)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResetSubscriptionPeriod for a missing row = %v, want ErrNotFound", err)
	}
}

// Usage still accumulates into the period after a reset, which is what makes
// the new period usable rather than just empty.
func TestStateRepo_UsageAccumulatesAfterAReset(t *testing.T) {
	repo := newTestStateRepo(t)
	start := int64(1_700_000_000_000_000_000)
	id := seedSubscription(t, repo, start, 4096)

	if err := repo.ResetSubscriptionPeriod(id, start, start+1); err != nil {
		t.Fatalf("ResetSubscriptionPeriod: %v", err)
	}
	if err := repo.AddSubscriptionUsedBytes(id, 777); err != nil {
		t.Fatalf("AddSubscriptionUsedBytes: %v", err)
	}

	sub, err := repo.GetUserSubscription(id)
	if err != nil {
		t.Fatalf("GetUserSubscription: %v", err)
	}
	if sub.PeriodUsedBytes != 777 {
		t.Fatalf("period usage = %d, want 777", sub.PeriodUsedBytes)
	}
}
