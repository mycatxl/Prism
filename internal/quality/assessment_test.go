package quality

import "testing"

func TestProviderEvidenceCannotInventCanonicalPurity(t *testing.T) {
	rawRisk := 0
	summary := Summary{State: "valid", Evidence: &Evidence{RiskScore: &rawRisk}, Sources: []SourceSummary{{Provider: "ippure", State: "valid", Evidence: &Evidence{RiskScore: &rawRisk}}}}
	if EffectivePurityBand(summary) != "unknown" {
		t.Fatal("provider raw risk became authoritative purity")
	}
	score := 60
	summary.Assessment = &Assessment{ScoreSource: "assessment", PurityScore: &score, PurityBand: "mixed"}
	if EffectivePurityBand(summary) != "mixed" || *summary.Assessment.PurityScore != 60 {
		t.Fatal("canonical assessment was replaced by provider inversion")
	}
}
