package model

import "math"

// UsageCharge identifies the subscription period a batch of in-memory traffic
// was observed against.
type UsageCharge struct {
	// Key identifies one user/window group in a flush. It is opaque to the
	// persistence layer and lets a pass contain old-period and new-period bytes
	// for the same user without collapsing their guarded outcomes.
	Key            string
	UserID         string
	SubscriptionID string
	PeriodStartNs  int64
	Bytes          int64
}

// UsageChargeResult reports the outcome of a guarded period increment.
type UsageChargeResult struct {
	Charged bool
	Rolled  bool
}

// SaturatingUsageAdd keeps nonnegative usage totals integral and fail-closed
// at the storage boundary. Money uses checked arithmetic instead of saturation.
func SaturatingUsageAdd(current, delta int64) int64 {
	if delta <= 0 {
		return current
	}
	if current > math.MaxInt64-delta {
		return math.MaxInt64
	}
	return current + delta
}
