package quality

import "testing"

func TestPurityDisplayBoundariesAndUnknown(t *testing.T) {
	for _, test := range []struct {
		risk, score int
		band        string
	}{
		{0, 100, "excellent"}, {5, 95, "excellent"}, {6, 94, "clean"}, {10, 90, "clean"},
		{11, 89, "fair"}, {20, 80, "fair"}, {21, 79, "mixed"}, {40, 60, "mixed"},
		{41, 59, "poor"}, {100, 0, "poor"},
	} {
		score := PurityScore(&test.risk)
		if score == nil || *score != test.score || PurityBand(score) != test.band {
			t.Errorf("risk %d did not produce %d/%s", test.risk, test.score, test.band)
		}
	}
	negative, tooHigh := -1, 101
	for _, risk := range []*int{nil, &negative, &tooHigh} {
		if PurityScore(risk) != nil || PurityBand(PurityScore(risk)) != "unknown" {
			t.Fatal("invalid risk became a purity score")
		}
	}
	risk := 0
	summary := Summary{State: "valid", Evidence: &Evidence{RiskScore: &risk}}
	if EffectivePurityBand(summary) != "unknown" {
		t.Fatal("ProxyCheck risk was substituted for an IPPure primary score")
	}
}

func TestNetworkTypePreservesAllocationMeaning(t *testing.T) {
	for raw, want := range map[string]string{"Residential": "residential", "Business": "business", "Wireless": "wireless", "Mobile": "mobile", "Hosting": "datacenter", "ISP": "unknown", "": "unknown"} {
		if got := NetworkType(raw, Signals{}); got != want {
			t.Errorf("%s: got %s, want %s", raw, got, want)
		}
	}
	positive := true
	if NetworkType("Wireless", Signals{Hosting: &positive}) != "conflicting" {
		t.Fatal("wireless/hosting conflict lost")
	}
	if NetworkType("Residential", Signals{Proxy: &positive}) != "residential" {
		t.Fatal("anonymity replaced allocation type")
	}
}
