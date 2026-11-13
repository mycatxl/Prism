package testutil

import (
	"net/netip"

	"prism/internal/quality"
)

// MeasuredQuality is an explicit legacy measurement fixture for tests whose
// subject is health, regex, or capacity, not provider availability.
func MeasuredQuality(ip netip.Addr) quality.Summary {
	score := 50
	return quality.Summary{IP: ip.String(), State: "valid", Assessment: &quality.Assessment{State: "valid", PurityScore: &score, Verdict: "caution"}}
}
