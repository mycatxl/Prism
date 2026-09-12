package quality

import (
	"net/netip"
	"testing"
	"time"
)

func TestPublicIPProtectsPrivateAndReservedAddresses(t *testing.T) {
	for _, input := range []string{"127.0.0.1", "10.0.1.2", "192.168.1.1", "100.64.0.1", "169.254.169.254", "192.0.2.1", "198.51.100.1", "203.0.113.1", "198.18.0.1", "240.0.0.1", "::1", "fc00::1", "fe80::1", "2001:db8::1", "2606:4700::1111%eth0", "::ffff:127.0.0.1"} {
		if _, err := PublicIP(netip.MustParseAddr(input)); err == nil {
			t.Errorf("reserved address accepted: %s", input)
		}
	}
	for _, input := range []string{"8.8.8.8", "::ffff:8.8.8.8", "2606:4700:4700::1111"} {
		ip, err := PublicIP(netip.MustParseAddr(input))
		if err != nil || ip != netip.MustParseAddr(input).Unmap() {
			t.Errorf("public address normalization: %s %v", input, err)
		}
	}
}

func TestSummaryKeepsEvidenceSeparateFromFailedRefreshAndExpiry(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	r := Record{Task: Task{IP: "8.8.8.8", State: "failed", ErrorCode: "PROVIDER_RESPONSE"},
		Evidence: &Evidence{IP: "8.8.8.8", Grade: "low", ObservedAt: now, ValidUntil: now.Add(time.Hour)}}
	if got := r.Summary(now); got.State != "valid" || got.Task.State != "failed" || got.Evidence == nil {
		t.Fatalf("successful evidence lost after failed refresh: %+v", got)
	}
	if got := r.Summary(now.Add(2 * time.Hour)); got.State != "stale" || EffectiveRiskGrade(got) != "unknown" {
		t.Fatalf("expired evidence remained qualified: %+v", got)
	}
	confidence, reports := 90, 8
	summary := r.Summary(now)
	summary.Sources = []SourceSummary{{State: "valid", Evidence: &Evidence{AbuseConfidence: &confidence, TotalReports: &reports}}}
	if EffectiveRiskGrade(summary) != "review" {
		t.Fatal("primary low score hid a current abuse report")
	}
	summary.Evidence, summary.State = nil, "unobserved"
	if EffectiveRiskGrade(summary) != "review" {
		t.Fatal("primary outage hid a current abuse report")
	}
	summary.Sources[0].State = "stale"
	if EffectiveRiskGrade(summary) != "unknown" {
		t.Fatal("expired abuse report treated as current")
	}
}
