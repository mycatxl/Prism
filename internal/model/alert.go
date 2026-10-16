package model

// Alert kinds. usage_anomaly is the only detector so far: one account crossed a
// configured share of its plan quota inside a short window. The column is a text
// kind rather than a boolean so a second detector does not need a schema change.
const (
	// AlertKindUsageAnomaly marks traffic that ran ahead of the plan quota.
	AlertKindUsageAnomaly = "usage_anomaly"
)

// Alert is one recorded anomaly about a single account's traffic.
//
// The measurement - the window, the ratio it was compared against, what was used
// and the quota it was compared to - is copied onto the row rather than
// recomputed from the plan when the alert is read: the plan can be edited or
// deleted afterwards and the alert has to keep saying what it saw.
// SubscriptionID is what bounds deduplication: one open alert per subscription
// and kind inside the window.
//
// AcknowledgedAtNs of 0 means the alert is still open, which is the state the
// console's unread list filters on. Note is free text for whoever annotates the
// alert; the scanner leaves it empty.
type Alert struct {
	ID               string  `json:"id"`
	Kind             string  `json:"kind"`
	UserID           string  `json:"user_id"`
	PlanID           string  `json:"plan_id"`
	SubscriptionID   string  `json:"subscription_id"`
	WindowMinutes    int     `json:"window_minutes"`
	ThresholdRatio   float64 `json:"threshold_ratio"`
	UsedBytes        int64   `json:"used_bytes"`
	QuotaBytes       int64   `json:"quota_bytes"`
	DetectedAtNs     int64   `json:"detected_at_ns"`
	AcknowledgedAtNs int64   `json:"acknowledged_at_ns"`
	Note             string  `json:"note"`
}
