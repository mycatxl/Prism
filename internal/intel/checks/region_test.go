package checks

import (
	"net/http"
	"strings"
	"testing"
)

// TestExtractRegionFromRedirectLocation covers the 2026-09-25 Netflix capture:
// the title page answers 301 with an empty body and only the Location header
// carries the region ("/jp-en/"), which is what header_regex reads.
func TestExtractRegionFromRedirectLocation(t *testing.T) {
	rule, err := ParseRule(regionFixtureYAML("region:\n  step: home\n  header_regex: {Location: \"/([a-z]{2})-en/\"}\n"), "user", "fixture.yaml")
	if err != nil {
		t.Fatalf("ParseRule = %v, want success", err)
	}
	redirect := map[string]stepResult{"home": {
		ID: "home", Status: http.StatusMovedPermanently, Connected: true,
		Header: http.Header{"Location": []string{"https://www.netflix.com/jp-en/title/80100172"}},
	}}
	if region := extractRegion(rule.Region, redirect); region != "JP" {
		t.Fatalf("region = %q, want %q", region, "JP")
	}

	// A Location without a region prefix must not be turned into a region code.
	plain := map[string]stepResult{"home": {
		ID: "home", Status: http.StatusOK, Connected: true,
		Header: http.Header{"Location": []string{"https://www.netflix.com/title/80100172"}},
	}}
	if region := extractRegion(rule.Region, plain); region != "" {
		t.Fatalf("region = %q, want empty", region)
	}
}

// TestExtractRegionPrefersBodyOverHeader pins the documented order: the body
// matcher runs first, header_regex only covers the cases where the body is empty.
func TestExtractRegionPrefersBodyOverHeader(t *testing.T) {
	rule, err := ParseRule(regionFixtureYAML(
		"region:\n  step: home\n  body_regex: '\"countryCode\":\"([A-Z]{2})\"'\n  header_regex: {Location: \"/([a-z]{2})-en/\"}\n"),
		"user", "fixture.yaml")
	if err != nil {
		t.Fatalf("ParseRule = %v, want success", err)
	}
	both := map[string]stepResult{"home": {
		ID: "home", Status: http.StatusOK, Connected: true, Body: `{"countryCode":"US"}`,
		Header: http.Header{"Location": []string{"https://www.netflix.com/jp-en/title/80100172"}},
	}}
	if region := extractRegion(rule.Region, both); region != "US" {
		t.Fatalf("region = %q, want %q (body matcher wins)", region, "US")
	}

	headerOnly := map[string]stepResult{"home": {
		ID: "home", Status: http.StatusMovedPermanently, Connected: true,
		Header: http.Header{"Location": []string{"https://www.netflix.com/jp-en/title/80100172"}},
	}}
	if region := extractRegion(rule.Region, headerOnly); region != "JP" {
		t.Fatalf("region = %q, want %q (header fallback)", region, "JP")
	}
}

// TestRegionValidationNeedsBodyOrHeaderRegex checks the region shape rules: at
// least one matcher is required, and each matcher needs at least one capture
// group — the engine reads group 1 as the region code and ignores any others, so
// extra groups stay loadable for backwards compatibility with rules written
// before this validation existed.
func TestRegionValidationNeedsBodyOrHeaderRegex(t *testing.T) {
	cases := []struct {
		name    string
		region  string
		wantErr string
	}{
		{
			name:   "header only",
			region: "region:\n  step: home\n  header_regex: {Location: \"/([a-z]{2})-en/\"}\n",
		},
		{
			name:   "body only",
			region: "region:\n  step: home\n  body_regex: '\"countryCode\":\"([A-Z]{2})\"'\n",
		},
		{
			name:    "neither matcher",
			region:  "region:\n  step: home\n",
			wantErr: "region needs a body_regex or a header_regex",
		},
		{
			name:    "header without capture group",
			region:  "region:\n  step: home\n  header_regex: {Location: \"/[a-z]{2}-en/\"}\n",
			wantErr: "needs a capture group",
		},
		{
			name:   "header with two capture groups stays loadable",
			region: "region:\n  step: home\n  header_regex: {Location: \"/(([a-z]{2})-en)/\"}\n",
		},
		{
			name:   "body with two capture groups stays loadable",
			region: "region:\n  step: home\n  body_regex: '\"(([A-Z]{2}))\"'\n",
		},
		{
			name:    "header regex does not compile",
			region:  "region:\n  step: home\n  header_regex: {Location: \"/([a-z\"}\n",
			wantErr: "region header_regex Location",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRule(regionFixtureYAML(tc.region), "user", "fixture.yaml")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ParseRule = %v, want success", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ParseRule succeeded, want an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestBuiltinRulesLoadAndValidate keeps the shipped set honest: every built-in
// must validate (region specs included) and Netflix must read its region from
// the Location header after the 2026-09-25 calibration.
func TestBuiltinRulesLoadAndValidate(t *testing.T) {
	rules, errs := LoadFS(builtinFiles, builtinDir, "builtin")
	if len(errs) != 0 {
		t.Fatalf("builtin rule load errors: %v", errs)
	}
	if len(rules) != 8 {
		t.Fatalf("builtin rules = %d, want 8", len(rules))
	}
	netflix := builtinRuleForTest(t, "netflix")
	if netflix.Region == nil {
		t.Fatal("netflix has no region spec")
	}
	if netflix.Region.BodyRegex != "" {
		t.Fatalf("netflix region body_regex = %q, want empty: a 301 response has no body", netflix.Region.BodyRegex)
	}
	if len(netflix.Region.headerRegex) != 1 || netflix.Region.headerRegex[0].name != "Location" {
		t.Fatalf("netflix region header patterns = %+v, want one Location matcher", netflix.Region.headerRegex)
	}
}

// TestNetflixCalibratedAgainstRealResponse pins the measured Netflix behaviour:
// both title pages answer 301 with a region-prefixed Location and an empty body.
// Before the calibration the licensed 301 fell through to default: unknown, and
// the body_regex could never match an empty body.
func TestNetflixCalibratedAgainstRealResponse(t *testing.T) {
	rule := builtinRuleForTest(t, "netflix")
	original := netflixRedirect("80100172")

	available := map[string]stepResult{"original": original, "licensed": netflixRedirect("70143836")}
	if outcome := classify(rule, available); outcome != OutcomeAvailable {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeAvailable)
	}
	if region := extractRegion(rule.Region, available); region != "JP" {
		t.Fatalf("region = %q, want %q", region, "JP")
	}

	// The licensed title is refused while the original one still redirects.
	regionLimited := map[string]stepResult{
		"original": original,
		"licensed": {ID: "licensed", Status: http.StatusForbidden, Connected: true},
	}
	if outcome := classify(rule, regionLimited); outcome != OutcomeRegionLimited {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeRegionLimited)
	}
}

// TestCalibratedBuiltinOutcomes replays the 2026-09-25 captures against the
// shipped rules.
func TestCalibratedBuiltinOutcomes(t *testing.T) {
	cases := []struct {
		check      string
		step       string
		result     stepResult
		want       string
		wantRegion string
	}{
		{
			check: "tiktok", step: "home", want: OutcomeCaptcha,
			result: stepResult{ID: "home", Status: http.StatusOK, Connected: true,
				Body: `<p id="wci" class="_wafchallengeid">Please wait...</p><script>{"slardarClient": "SlardarWAF"}</script>`},
		},
		{
			check: "claude", step: "home", want: OutcomeCaptcha,
			result: stepResult{ID: "home", Status: http.StatusForbidden, Connected: true,
				Body: `<script src="/cdn-cgi/challenge-platform/h/b/orchestrate/chl_page/v1?ray=abc"></script>`},
		},
		{
			check: "gemini", step: "home", want: OutcomeAvailable,
			result: stepResult{ID: "home", Status: http.StatusOK, Connected: true,
				Body: strings.Repeat("<html>", 16)},
		},
		{
			// The live capture was 403 with this body; the status is 200 here so
			// only the "type":"dc" marker (not the bare 403 rule) can match.
			check: "chatgpt", step: "probe", want: OutcomeBlocked,
			result: stepResult{ID: "probe", Status: http.StatusOK, Connected: true,
				Body: `{"cf_details":"Request is not allowed. Please try again later.", "type":"dc"}`},
		},
		{
			check: "youtube_premium", step: "premium", want: OutcomeAvailable, wantRegion: "JP",
			result: stepResult{ID: "premium", Status: http.StatusOK, Connected: true,
				Body: `<link rel="alternate" href="https://www.youtube.com/premium">"countryCode":"JP"</link>`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.check, func(t *testing.T) {
			rule := builtinRuleForTest(t, tc.check)
			steps := map[string]stepResult{tc.step: tc.result}
			if outcome := classify(rule, steps); outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", outcome, tc.want)
			}
			if tc.wantRegion != "" {
				if region := extractRegion(rule.Region, steps); region != tc.wantRegion {
					t.Fatalf("region = %q, want %q", region, tc.wantRegion)
				}
			}
		})
	}
}

// netflixRedirect builds the measured 301 answer of one Netflix title page: no
// body, the region only in the Location header.
func netflixRedirect(titleID string) stepResult {
	return stepResult{
		ID: "original", Status: http.StatusMovedPermanently, Connected: true,
		Header: http.Header{"Location": []string{"https://www.netflix.com/jp-en/title/" + titleID}},
	}
}

// builtinRuleForTest loads one shipped rule through the embedded rule set.
func builtinRuleForTest(t *testing.T, id string) *Rule {
	t.Helper()
	rules, errs := LoadFS(builtinFiles, builtinDir, "builtin")
	if len(errs) != 0 {
		t.Fatalf("builtin rule load errors: %v", errs)
	}
	for _, rule := range rules {
		if rule.ID == id {
			return rule
		}
	}
	t.Fatalf("builtin rule %s not found", id)
	return nil
}

// classify mirrors the outcome loop of Engine.runRule without a node/network.
func classify(rule *Rule, steps map[string]stepResult) string {
	outcome := rule.Default
	for _, candidate := range rule.Outcomes {
		if matches(candidate, steps) {
			outcome = candidate.Outcome
			break
		}
	}
	return outcome
}

// regionFixtureYAML is a minimal valid rule with the region block under test.
func regionFixtureYAML(regionBlock string) []byte {
	return []byte(`id: region_fixture
version: 1
name: Region fixture
category: other
enabled: true
ttl: 1h
timeout: 5s
calibrated: fixture
steps:
  - id: home
    request:
      method: GET
      url: https://example.test/
      follow_redirects: false
outcomes:
  - when: {step: home, status_in: [200]}
    outcome: available
default: unknown
` + regionBlock)
}
