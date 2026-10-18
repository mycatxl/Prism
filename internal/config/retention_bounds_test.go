package config

import "testing"

// TestRetentionDaysRefusesAWindowThatWouldOverflow pins the upper bound on the
// retention windows.
//
// The window is turned into a nanosecond offset and subtracted from the current
// time. 106752 days overflows int64, the offset becomes negative, and the cutoff
// it produces sits in the future - where the prune it drives deletes every row
// instead of the expired ones.
func TestRetentionDaysRefusesAWindowThatWouldOverflow(t *testing.T) {
	for _, days := range []int{106752, 200000, 1000000000} {
		if got := RetentionDays(days, DefaultAuditRetentionDays); got != MaxRetentionDays {
			t.Fatalf("RetentionDays(%d) = %d, want the ceiling %d", days, got, MaxRetentionDays)
		}
	}
}

// TestRetentionDaysKeepsTheWindowItIsGiven checks the bound did not change the
// meaning of an ordinary value, or of the zero that means "use the shipped one".
func TestRetentionDaysKeepsTheWindowItIsGiven(t *testing.T) {
	cases := []struct{ configured, want int }{
		{0, DefaultAuditRetentionDays},
		{-5, DefaultAuditRetentionDays},
		{1, 1},
		{400, 400},
		{MaxRetentionDays, MaxRetentionDays},
	}
	for _, c := range cases {
		if got := RetentionDays(c.configured, DefaultAuditRetentionDays); got != c.want {
			t.Fatalf("RetentionDays(%d) = %d, want %d", c.configured, got, c.want)
		}
	}
}
