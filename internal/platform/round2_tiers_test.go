package platform

import "testing"

func TestPlanIPTiersRequireExactPairsAndAssessment(t *testing.T) {
	entry := criteriaEntry(t, `{"type":"vless"}`, "us", "203.0.113.1", "sub-1")
	plat := NewPlatform("tier-plan", "tiers", nil, nil)
	tiers := []string{"residential:clean", "mobile:excellent"}
	for _, tc := range []struct {
		ipType, band string
		allowed      bool
	}{
		{"residential", "clean", true},
		{"mobile", "excellent", true},
		{"residential", "excellent", false},
		{"mobile", "clean", false},
		{"datacenter", "clean", false},
	} {
		t.Run(tc.ipType+":"+tc.band, func(t *testing.T) {
			plat.SetQualitySnapshot(newFakeSnapshot().set("203.0.113.1", cleanAssessment(tc.band, tc.ipType)))
			if got := plat.AllowsIPTiers(entry, tiers); got != tc.allowed {
				t.Fatalf("allowed=%v, want %v", got, tc.allowed)
			}
		})
	}
	plat.SetQualitySnapshot(newFakeSnapshot())
	if plat.AllowsIPTiers(entry, tiers) {
		t.Fatal("unassessed node satisfied a plan tier")
	}
	plat.SetQualitySnapshot(nil)
	if plat.AllowsIPTiers(entry, tiers) {
		t.Fatal("missing projection satisfied a plan tier")
	}
}
