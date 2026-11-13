package quality

import "testing"

func TestPurityBandsRequireAnAuthoritativeScore(t *testing.T) {
	for _, tc := range []struct {
		score int
		band  string
	}{{100, "excellent"}, {95, "excellent"}, {94, "clean"}, {90, "clean"}, {89, "fair"}, {80, "fair"}, {79, "mixed"}, {60, "mixed"}, {59, "poor"}, {0, "poor"}, {-1, "unknown"}, {101, "unknown"}} {
		if got := DefaultBands().Name(&tc.score); got != tc.band {
			t.Fatalf("%d: %s", tc.score, got)
		}
	}
	if DefaultBands().Name(nil) != "unknown" {
		t.Fatal("missing score got a band")
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
