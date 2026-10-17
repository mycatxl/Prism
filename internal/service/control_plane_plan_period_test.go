package service

import (
	"math"
	"strings"
	"testing"

	"prism/internal/model"
)

// A plan's period is typed by an operator and read by the rollover in
// internal/usage, which accepts the case and spacing variants of the named
// periods as well as the "<n>d" form. Two spellings of one period in the table
// would invite a comparison that misses one of them, so the value is normalized
// on the way in.
func TestPlanPeriodIsNormalizedOnTheWayIn(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "30d"},
		{"  Month ", "month"},
		{"MONTH", "month"},
		{" 30D ", "30d"},
		{"7d", "7d"},
		{"once", "once"},
	} {
		if got := defaultPlanPeriod(tc.in); got != tc.want {
			t.Fatalf("defaultPlanPeriod(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A period no rollover can act on is refused rather than stored: it would be
// enforced as a quota and never reset, so the plan would take the user's
// allowance and never give it back - and nothing in the log would say so.
func TestPlanPeriodRejectsWhatNoRolloverCanActOn(t *testing.T) {
	for _, in := range []string{"monthly", "30", "d", "0d", "-7d", "every month", "12h"} {
		if validPlanPeriod(in) {
			t.Fatalf("validPlanPeriod(%q) = true, want false", in)
		}
	}
	for _, in := range []string{"", "month", "week", "day", "once", "30d", " 7D ", "1d"} {
		if !validPlanPeriod(in) {
			t.Fatalf("validPlanPeriod(%q) = false, want true", in)
		}
	}
}

func TestValidatePlanRefusesAnUnknownPeriod(t *testing.T) {
	plan := model.Plan{
		ID: "p-1", Name: "p-1", DurationDays: 30, Period: "monthly", Status: planStatusActive,
	}
	svcErr := validatePlan(plan)
	if svcErr == nil {
		t.Fatal("validatePlan accepted a period no rollover can act on")
	}
	if !strings.Contains(svcErr.Error(), "period:") {
		t.Fatalf("validatePlan error %q does not name the offending field", svcErr.Error())
	}
}

// A term long enough to overflow the expiry arithmetic is refused at the door.
// The multiplication used to wrap: 213504 days landed on a negative offset, so
// a plan an operator typed as "effectively forever" created subscriptions that
// were already expired, and 1000000 days wrapped to a *shorter* term than 30.
func TestValidatePlanRefusesATermThatWouldOverflowTheExpiry(t *testing.T) {
	base := model.Plan{
		ID: "p-1", Name: "p-1", DurationDays: 30, Period: "month", Status: planStatusActive,
	}
	for _, days := range []int{213504, 1_000_000, 100_000_000} {
		plan := base
		plan.DurationDays = days
		svcErr := validatePlan(plan)
		if svcErr == nil {
			t.Fatalf("validatePlan accepted duration_days=%d", days)
		}
		if !strings.Contains(svcErr.Error(), "duration_days:") {
			t.Fatalf("validatePlan error %q does not name the offending field", svcErr.Error())
		}
	}
	plan := base
	plan.DurationDays = maxExpiryDays
	if svcErr := validatePlan(plan); svcErr != nil {
		t.Fatalf("validatePlan refused duration_days=%d: %v", maxExpiryDays, svcErr)
	}
}

// The helper saturates, so even a row written before the ceiling existed cannot
// produce an expiry in the past.
func TestExpiryFromDaysNeverGoesBackwards(t *testing.T) {
	now := int64(1_700_000_000_000_000_000)
	for _, days := range []int{1, 30, 365, 36500, 213503, 213504, 1_000_000, 100_000_000} {
		got := expiryFromDays(days, now)
		if got <= now {
			t.Fatalf("expiryFromDays(%d) = %d, want after %d", days, got, now)
		}
	}
	if got := expiryFromDays(0, now); got != 0 {
		t.Fatalf("expiryFromDays(0) = %d, want 0 (no expiry)", got)
	}
	if got := expiryFromDays(1_000_000, now); got != math.MaxInt64 {
		t.Fatalf("expiryFromDays(1000000) = %d, want saturation at MaxInt64", got)
	}
}
