package usage

import (
	"testing"
	"time"
)

// TestPeriodLengthRefusesADayCountThatWouldOverflow pins the bound on the
// "<n>d" form.
//
// The digit string comes from the plans table as free text. Before the bound,
// "106752d" multiplied into a negative duration, and advancePeriod read that as
// a period that never elapses: the subscription kept the allowance it had
// already spent, for good, with no error anywhere.
func TestPeriodLengthRefusesADayCountThatWouldOverflow(t *testing.T) {
	for _, period := range []string{
		"106752d",
		"200000d",
		"100000000d",
		"9223372036854775807d",
	} {
		length, ok := periodLength(period)
		if ok {
			t.Fatalf("periodLength(%q) = (%v, true), want refusal: the length overflows a duration", period, length)
		}
		if periodRecognized(period) {
			t.Fatalf("periodRecognized(%q) = true, want false for a period nothing can act on", period)
		}
	}
}

// TestPeriodLengthKeepsTheLongestUsableWindow checks the bound did not cost the
// values below it.
func TestPeriodLengthKeepsTheLongestUsableWindow(t *testing.T) {
	for _, period := range []string{"1d", "30d", "365d", "36500d"} {
		length, ok := periodLength(period)
		if !ok {
			t.Fatalf("periodLength(%q) refused a usable period", period)
		}
		if length <= 0 {
			t.Fatalf("periodLength(%q) = %v, want a positive length", period, length)
		}
	}
	if length, ok := periodLength("36500d"); !ok || length != 36500*24*time.Hour {
		t.Fatalf("periodLength(%q) = (%v, %v), want exactly 36500 days", "36500d", length, ok)
	}
}

// TestAdvancePeriodNeverMovesBackwardsForAnOverflowingPeriod covers the caller
// that acts on the length: an unusable period must leave the window alone.
func TestAdvancePeriodNeverMovesBackwardsForAnOverflowingPeriod(t *testing.T) {
	start := int64(1700000000000000000)
	now := start + int64(400*24*time.Hour)
	for _, period := range []string{"106752d", "200000d", "100000000d"} {
		got, moved := advancePeriod(start, now, period)
		if moved {
			t.Fatalf("advancePeriod(%q) moved to %d, want no move", period, got)
		}
		if got != start {
			t.Fatalf("advancePeriod(%q) = %d, want the unchanged start %d", period, got, start)
		}
	}
}
