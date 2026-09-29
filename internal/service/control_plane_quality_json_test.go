package service

import (
	"encoding/json"
	"strings"
	"testing"

	"prism/internal/intel"
	"prism/internal/intel/store"
)

// TestQualityAssessmentReasonsIsAlwaysAList pins the wire shape the WebUI reads.
//
// Both projections used to leave Reasons as a nil slice, and a nil slice marshals
// to `null` rather than `[]`. The frontend read `.length` on that value, and the
// root error boundary turned the resulting TypeError into a full-page error
// screen: opening a node detail drawer for an assessed node blanked the whole
// panel. Measured on the live instance, 7 of 7 assessed nodes had
// `assessment.reasons == null` before this test existed.
//
// An empty list has to look like an empty list. This asserts the wire form, not
// the Go value, because that is what the browser sees.
func TestQualityAssessmentReasonsIsAlwaysAList(t *testing.T) {
	encoded := func(t *testing.T, value any) string {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(body)
	}

	cases := []struct {
		name  string
		value any
	}{
		{
			name:  "lite projection",
			value: qualityAssessmentFromLite(intel.AssessmentLite{}),
		},
		{
			name:  "row projection without stored reasons",
			value: qualityAssessmentFromRow(store.Assessment{}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := encoded(t, tc.value)
			if strings.Contains(body, `"reasons":null`) {
				t.Fatalf("reasons marshalled as null, which crashes the drawer: %s", body)
			}
			if !strings.Contains(body, `"reasons":[]`) {
				t.Fatalf("reasons is not an empty JSON list: %s", body)
			}
		})
	}
}

// TestQualityAssessmentPreservesStoredReasons is the other half of the contract:
// the default above must not swallow reasons that really were stored.
func TestQualityAssessmentPreservesStoredReasons(t *testing.T) {
	row := store.Assessment{ReasonsJSON: `["RECENT_ABUSE","COMPROMISED"]`}
	assessment := qualityAssessmentFromRow(row)

	if len(assessment.Reasons) != 2 {
		t.Fatalf("reasons = %v, want the 2 stored entries", assessment.Reasons)
	}
	body, err := json.Marshal(assessment)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{"RECENT_ABUSE", "COMPROMISED"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("stored reason %q missing from %s", want, body)
		}
	}
}
