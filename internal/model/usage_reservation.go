package model

// UsageReservation is durable liability, not a claim that bytes were delivered.
// It survives until settlement atomically records actual bytes and removes it.
type UsageReservation struct {
	ID, KeyID, UserID, PlatformID, SubscriptionID string
	PeriodStartNs, HourNs, Bytes                  int64
	Ingress                                       bool
}
