// Package measurement provides explicit measured snapshots for test fixtures.
package measurement

import (
	"net/netip"
	"prism/internal/intel"
	"time"
)

// Snapshot seeds only the explicitly listed fixture IPs. Absent IPs
// remain unmeasured, including in health/filter tests that use this helper.
func Snapshot(ips ...netip.Addr) *intel.Snapshot {
	s := intel.NewSnapshot()
	for _, ip := range ips {
		s.SetAssessment(ip, intel.AssessmentLite{State: intel.StateValid, Score: 50, Native: -1, Verdict: intel.VerdictCaution, ValidUntil: time.Now().Add(24 * time.Hour).UnixNano()})
	}
	return s
}
