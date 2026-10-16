package service

import (
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
