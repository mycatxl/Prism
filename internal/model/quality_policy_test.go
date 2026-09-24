package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// qualityPolicyLegacyJSON is the pre-Prism (Resin) on-disk encoding, including
// keys that were dropped in the Prism rewrite.
const qualityPolicyLegacyJSON = `{"min_score":80,"max_assessment_age_seconds":3600,` +
	`"max_egress_age_seconds":1800,"profile_id":"x","pending_action":"allow","conflict_action":"exclude"}`

func TestQualityPolicyIsEmpty(t *testing.T) {
	if !(QualityPolicy{}).IsEmpty() {
		t.Fatal("the zero value QualityPolicy must be empty")
	}

	cases := []struct {
		name   string
		mutate func(*QualityPolicy)
	}{
		{"MinPurity", func(q *QualityPolicy) { v := 80; q.MinPurity = &v }},
		{"IPTypes", func(q *QualityPolicy) { q.IPTypes = []string{"residential"} }},
		{"AllowedVerdicts", func(q *QualityPolicy) { q.AllowedVerdicts = []string{"favorable"} }},
		{"MinConfidence", func(q *QualityPolicy) { q.MinConfidence = "medium" }},
		{"RequireNative", func(q *QualityPolicy) { q.RequireNative = true }},
		{"RequiredChecks", func(q *QualityPolicy) { q.RequiredChecks = map[string]string{"chatgpt": "available"} }},
		{"MaxAssessmentAge", func(q *QualityPolicy) { q.MaxAssessmentAge = "1h0m0s" }},
		{"MaxEgressAge", func(q *QualityPolicy) { q.MaxEgressAge = "30m0s" }},
		{"UnknownAction", func(q *QualityPolicy) { q.UnknownAction = "exclude" }},
		{"ExcludeTor", func(q *QualityPolicy) { v := true; q.ExcludeTor = &v }},
		{"ExcludeHighRisk", func(q *QualityPolicy) { v := true; q.ExcludeHighRisk = &v }},
	}
	for _, tc := range cases {
		var q QualityPolicy
		tc.mutate(&q)
		if q.IsEmpty() {
			t.Errorf("policy with %s set must not be empty", tc.name)
		}
	}

	// A pointer-to-zero and an explicit false are deliberate settings: nil is
	// what means "unset" for MinPurity/ExcludeTor/ExcludeHighRisk.
	zero := 0
	if q := (QualityPolicy{MinPurity: &zero}); q.IsEmpty() {
		t.Error("policy with min_purity=0 must not be empty")
	}
	no := false
	if q := (QualityPolicy{ExcludeTor: &no}); q.IsEmpty() {
		t.Error("policy with exclude_tor=false must not be empty")
	}
	if q := (QualityPolicy{ExcludeHighRisk: &no}); q.IsEmpty() {
		t.Error("policy with exclude_high_risk=false must not be empty")
	}
}

func TestQualityPolicyUnmarshalLegacyKeys(t *testing.T) {
	var q QualityPolicy
	if err := json.Unmarshal([]byte(qualityPolicyLegacyJSON), &q); err != nil {
		t.Fatalf("decoding legacy quality policy must not fail: %v", err)
	}

	if q.MinPurity == nil {
		t.Fatal("legacy min_score was not migrated into MinPurity")
	}
	if *q.MinPurity != 80 {
		t.Errorf("MinPurity: got %d, want 80", *q.MinPurity)
	}
	if q.MaxAssessmentAge != "1h0m0s" {
		t.Errorf("MaxAssessmentAge: got %q, want %q", q.MaxAssessmentAge, "1h0m0s")
	}
	if q.MaxEgressAge != "30m0s" {
		t.Errorf("MaxEgressAge: got %q, want %q", q.MaxEgressAge, "30m0s")
	}
	// The dropped keys must not resurrect any field.
	if q.UnknownAction != "" {
		t.Errorf("UnknownAction: got %q, want empty (pending_action must be ignored)", q.UnknownAction)
	}
	if q.ExcludeTor != nil || q.ExcludeHighRisk != nil {
		t.Error("legacy conflict_action must not produce an exclude_* pointer")
	}
	if q.IsEmpty() {
		t.Error("policy decoded from legacy keys enforces something and must not be empty")
	}

	// Current keys win over legacy ones when both are present.
	mixed := `{"min_purity":50,"max_assessment_age":"10m0s","min_score":99,"max_assessment_age_seconds":7200}`
	var mixedPolicy QualityPolicy
	if err := json.Unmarshal([]byte(mixed), &mixedPolicy); err != nil {
		t.Fatalf("decoding mixed quality policy: %v", err)
	}
	if mixedPolicy.MinPurity == nil || *mixedPolicy.MinPurity != 50 {
		t.Errorf("mixed min_purity: got %v, want 50", mixedPolicy.MinPurity)
	}
	if mixedPolicy.MaxAssessmentAge != "10m0s" {
		t.Errorf("mixed max_assessment_age: got %q, want %q", mixedPolicy.MaxAssessmentAge, "10m0s")
	}

	// An empty object is the empty policy.
	var empty QualityPolicy
	if err := json.Unmarshal([]byte(`{}`), &empty); err != nil {
		t.Fatalf("decoding empty policy: %v", err)
	}
	if !empty.IsEmpty() {
		t.Error("policy decoded from {} must be empty")
	}
}

func TestQualityPolicyJSONRoundTrip(t *testing.T) {
	minPurity := 85
	excludeTor := false
	excludeHighRisk := true
	policy := QualityPolicy{
		MinPurity:        &minPurity,
		IPTypes:          []string{"residential", "mobile"},
		AllowedVerdicts:  []string{"favorable", "caution"},
		MinConfidence:    "high",
		RequireNative:    true,
		RequiredChecks:   map[string]string{"chatgpt": "available", "netflix": "available"},
		MaxAssessmentAge: "24h0m0s",
		MaxEgressAge:     "2h0m0s",
		UnknownAction:    "allow",
		ExcludeTor:       &excludeTor,
		ExcludeHighRisk:  &excludeHighRisk,
	}

	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("marshal quality policy: %v", err)
	}

	var got QualityPolicy
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal quality policy %s: %v", raw, err)
	}

	// Field by field, so a failure names the offending field.
	if got.MinPurity == nil || *got.MinPurity != minPurity {
		t.Errorf("MinPurity: got %v, want %d", got.MinPurity, minPurity)
	}
	if !reflect.DeepEqual(got.IPTypes, policy.IPTypes) {
		t.Errorf("IPTypes: got %v, want %v", got.IPTypes, policy.IPTypes)
	}
	if !reflect.DeepEqual(got.AllowedVerdicts, policy.AllowedVerdicts) {
		t.Errorf("AllowedVerdicts: got %v, want %v", got.AllowedVerdicts, policy.AllowedVerdicts)
	}
	if got.MinConfidence != policy.MinConfidence {
		t.Errorf("MinConfidence: got %q, want %q", got.MinConfidence, policy.MinConfidence)
	}
	if got.RequireNative != policy.RequireNative {
		t.Errorf("RequireNative: got %v, want %v", got.RequireNative, policy.RequireNative)
	}
	if !reflect.DeepEqual(got.RequiredChecks, policy.RequiredChecks) {
		t.Errorf("RequiredChecks: got %v, want %v", got.RequiredChecks, policy.RequiredChecks)
	}
	if got.MaxAssessmentAge != policy.MaxAssessmentAge {
		t.Errorf("MaxAssessmentAge: got %q, want %q", got.MaxAssessmentAge, policy.MaxAssessmentAge)
	}
	if got.MaxEgressAge != policy.MaxEgressAge {
		t.Errorf("MaxEgressAge: got %q, want %q", got.MaxEgressAge, policy.MaxEgressAge)
	}
	if got.UnknownAction != policy.UnknownAction {
		t.Errorf("UnknownAction: got %q, want %q", got.UnknownAction, policy.UnknownAction)
	}
	if got.ExcludeTor == nil || *got.ExcludeTor != excludeTor {
		t.Errorf("ExcludeTor: got %v, want %v", got.ExcludeTor, excludeTor)
	}
	if got.ExcludeHighRisk == nil || *got.ExcludeHighRisk != excludeHighRisk {
		t.Errorf("ExcludeHighRisk: got %v, want %v", got.ExcludeHighRisk, excludeHighRisk)
	}
	if got.IsEmpty() {
		t.Error("fully populated policy must not decode as empty")
	}
	if !reflect.DeepEqual(got, policy) {
		t.Errorf("round-tripped policy differs from the original:\n got %+v\nwant %+v", got, policy)
	}
}

func TestQualityPolicyMarshalWritesOnlyCurrentKeys(t *testing.T) {
	var q QualityPolicy
	if err := json.Unmarshal([]byte(qualityPolicyLegacyJSON), &q); err != nil {
		t.Fatalf("decoding legacy quality policy: %v", err)
	}

	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("marshal decoded legacy policy: %v", err)
	}
	encoded := string(raw)

	for _, forbidden := range []string{
		"min_score",
		"max_assessment_age_seconds",
		"max_egress_age_seconds",
		"profile_id",
		"pending_action",
		"conflict_action",
	} {
		if strings.Contains(encoded, forbidden) {
			t.Errorf("encoded policy still contains %q: %s", forbidden, encoded)
		}
	}

	for _, want := range []string{
		`"min_purity":80`,
		`"max_assessment_age":"1h0m0s"`,
		`"max_egress_age":"30m0s"`,
	} {
		if !strings.Contains(encoded, want) {
			t.Errorf("encoded policy is missing %s: %s", want, encoded)
		}
	}

	// The empty policy encodes to an empty object, not to a bag of nulls.
	emptyRaw, err := json.Marshal(QualityPolicy{})
	if err != nil {
		t.Fatalf("marshal zero policy: %v", err)
	}
	if string(emptyRaw) != "{}" {
		t.Errorf("zero policy encodes to %s, want {}", emptyRaw)
	}
}
