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

// TestYoutubeRegionMarkerIsInsideTheBodyCap pins the 2026-10-02 fix. Measured
// against a real response, "countryCode":"XX" first appears at byte offset
// ~866312 of an ~888 KB body, while youtube_premium reads at most 131072 bytes —
// so a body_regex anchored on countryCode can never match and the region stayed
// empty. The early ytcfg "GL" field carries the same value at ~56 KiB, inside the
// cap. This test builds a body with exactly that layout: GL early, countryCode
// far past the cap. It fails if the matcher regresses to countryCode only.
func TestYoutubeRegionMarkerIsInsideTheBodyCap(t *testing.T) {
	rule := builtinRuleForTest(t, "youtube_premium")
	step, ok := rule.Steps[0].Request, rule.Steps[0].Request != nil
	if !ok {
		t.Fatal("youtube_premium has no request step")
	}
	cap := step.MaxBodyBytes
	if cap <= 0 {
		t.Fatalf("youtube_premium max_body_bytes = %d, want a positive cap", cap)
	}

	const glOffset = 56000
	if cap <= glOffset {
		t.Fatalf("max_body_bytes = %d leaves no room for the measured GL offset %d", cap, glOffset)
	}
	// The filler is one full cap wide, so countryCode lands past the truncation
	// point and only the early GL marker survives.
	body := strings.Repeat("x", glOffset) +
		`{"GL":"JP","HL":"en"}` + strings.Repeat("y", cap) +
		`"countryCode":"JP"` + strings.Repeat("z", 4096)

	// The engine truncates the body at max_body_bytes before matching, so the
	// countryCode marker is gone by the time the regex runs.
	truncated := body
	if len(truncated) > cap {
		truncated = truncated[:cap]
	}
	if strings.Contains(truncated, `"countryCode"`) {
		t.Fatal("test fixture is wrong: countryCode must fall outside the cap")
	}

	steps := map[string]stepResult{"premium": {
		ID: "premium", Status: http.StatusOK, Connected: true, Body: truncated,
	}}
	if region := extractRegion(rule.Region, steps); region != "JP" {
		t.Fatalf("region = %q, want %q: the matcher must read the early GL marker", region, "JP")
	}
	if outcome := classify(rule, steps); outcome != OutcomeAvailable {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeAvailable)
	}
}

// TestTiktokRegionMarkerIsInsideTheBodyCap pins the recalibrated cap against the
// worst offset measured on real nodes, so an edit that lowers the cap below it
// fails here instead of silently emptying the region field.
//
// Measured through the pool on 2026-09-28, across two independent runs:
//   - 480 requests (the first run): 388 full pages of 366-411 KB, region at
//     53856-134607 (387 samples);
//   - 45 requests (the verification run): 40 full pages of 368-402 KB, region at
//     92867-126325 (40 samples, p50 101133).
//
// The earlier 2026-10-02 note claimed the worst offset was 105215, but that came
// from only 15 samples and was too low. Both runs agree the marker sits between
// ~19% and ~33% into the page, and 134607 is the worst case seen so far.
//
// The cap stays 163840, which leaves 29233 bytes (18%) over that worst case. The
// page itself is 366-411 KB, so anything larger would only read bytes no matcher
// looks at - and this is a bounded read: markers past the cap never match.
func TestTiktokRegionMarkerIsInsideTheBodyCap(t *testing.T) {
	rule := builtinRuleForTest(t, "tiktok")
	if rule.Steps[0].Request == nil {
		t.Fatal("tiktok has no request step")
	}
	cap := rule.Steps[0].Request.MaxBodyBytes
	if cap <= 0 {
		t.Fatalf("tiktok max_body_bytes = %d, want a positive cap", cap)
	}

	// Worst region offset measured across 427 full pages in two runs.
	const regionOffset = 134607
	if cap <= regionOffset {
		t.Fatalf("max_body_bytes = %d does not cover the measured region offset %d", cap, regionOffset)
	}
	if headroom := cap - regionOffset; headroom < 16384 {
		t.Fatalf("max_body_bytes = %d leaves only %d bytes over the worst measured offset %d, want at least 16384",
			cap, headroom, regionOffset)
	}

	// A page shaped like the measured ones: the marker sits at the measured
	// offset and ~270 KB of page follows, so the page is longer than the cap
	// while the marker stays inside it.
	body := strings.Repeat("x", regionOffset) +
		`,"region":"NL",` + strings.Repeat("y", 270000)
	truncated := body
	if len(truncated) > cap {
		truncated = truncated[:cap]
	}
	if !strings.Contains(truncated, `"region"`) {
		t.Fatal("fixture is wrong: the region marker must fall inside the cap")
	}

	steps := map[string]stepResult{"home": {
		ID: "home", Status: http.StatusOK, Connected: true, Body: truncated,
	}}
	if region := extractRegion(rule.Region, steps); region != "NL" {
		t.Fatalf("region = %q, want %q: the matcher must read the marker inside the cap", region, "NL")
	}
	if outcome := classify(rule, steps); outcome != OutcomeAvailable {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeAvailable)
	}

	// The other measured shape: the 1462-byte challenge page carries no region,
	// so the outcome is captcha and the region stays empty.
	challenge := map[string]stepResult{"home": {
		ID: "home", Status: http.StatusOK, Connected: true,
		Body: `<p id="wci" class="_wafchallengeid"></p><script>{"slardarClient": "SlardarWAF"}</script>`,
	}}
	if outcome := classify(rule, challenge); outcome != OutcomeCaptcha {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeCaptcha)
	}
	if region := extractRegion(rule.Region, challenge); region != "" {
		t.Fatalf("region = %q, want empty on a challenge page", region)
	}
}

// TestTiktokBlockedMarkerPrecedesAvailable pins the ordering of the one tiktok
// matcher that has never been observed in a real response.
//
// "Your account is currently unavailable" does not exist anywhere else in the
// repository and did not come from a capture: it entered this rule in 0d0553b,
// which built the built-in rules from docs/plan/09-intel-providers-checks.md §5.4
// -- and that section only says "a blocked page is judged blocked" without naming
// a marker. Measured 2026-09-28 over 480 requests (43 challenge pages, 388 full
// pages): zero hits. TikTok's actual region notice is a different string, "This
// account isn't available in your country or region.", and it sits in the page's
// i18n dictionary at byte ~199026 -- past the 163840 cap -- and appears on 386 of
// 391 pages including every US page, so it is not a signal either.
//
// The matcher therefore stays as an unobserved fallback, and what this test can
// still guarantee is that it is evaluated before the bare status_in: [200] rule.
// If the order were reversed, a blocked page answering 200 would be reported as
// available, which is the failure mode youtube_premium had on 2026-10-02.
func TestTiktokBlockedMarkerPrecedesAvailable(t *testing.T) {
	rule := builtinRuleForTest(t, "tiktok")

	blockedIndex, availableIndex := -1, -1
	for i, outcome := range rule.Outcomes {
		switch outcome.Outcome {
		case OutcomeBlocked:
			if blockedIndex < 0 {
				blockedIndex = i
			}
		case OutcomeAvailable:
			if availableIndex < 0 {
				availableIndex = i
			}
		}
	}
	if blockedIndex < 0 || availableIndex < 0 {
		t.Fatalf("tiktok outcomes = %+v, want both a blocked and an available rule", rule.Outcomes)
	}
	if blockedIndex > availableIndex {
		t.Fatalf("the first blocked rule is at %d but available is at %d: a 200 blocked page would be reported as available",
			blockedIndex, availableIndex)
	}

	// A 200 whose body carries the marker must classify as blocked, not available.
	blocked := map[string]stepResult{"home": {
		ID: "home", Status: http.StatusOK, Connected: true,
		Body: `<html>Your account is currently unavailable</html>`,
	}}
	if outcome := classify(rule, blocked); outcome != OutcomeBlocked {
		t.Fatalf("outcome = %q, want %q for a 200 carrying the blocked marker", outcome, OutcomeBlocked)
	}

	// And the marker is inside the window, so the bounded read does not hide it.
	if cap := rule.Steps[0].Request.MaxBodyBytes; cap < 1024 {
		t.Fatalf("max_body_bytes = %d is too small for the marker to be readable at all", cap)
	}
}

// TestNetflixBodyIsNotReadByAnyMatcher pins the 2026-10-02 trim. Measured across
// 208 real nodes, most Netflix answers carry a full page and the old 262144 cap
// was read to the end on both steps (116 requests: mean 251523, median 262144),
// about 137 KB per node -- yet the outcomes match on status codes only and the
// region comes from the Location header. A cap above a few KiB can only buy
// bytes that nothing reads, so this test fails if it grows back.
func TestNetflixBodyIsNotReadByAnyMatcher(t *testing.T) {
	rule := builtinRuleForTest(t, "netflix")
	const maxUseful = 8192
	for _, step := range rule.Steps {
		if step.Request == nil {
			continue
		}
		if step.Request.MaxBodyBytes > maxUseful {
			t.Fatalf("step %s max_body_bytes = %d, want <= %d: no matcher reads the body",
				step.ID, step.Request.MaxBodyBytes, maxUseful)
		}
	}

	// A full page with no region prefix in Location: still classified as
	// available (status code only) and the region stays empty.
	full := map[string]stepResult{
		"original": {ID: "original", Status: http.StatusOK, Connected: true, Body: strings.Repeat("x", maxUseful)},
		"licensed": {ID: "licensed", Status: http.StatusOK, Connected: true, Body: strings.Repeat("x", maxUseful)},
	}
	if outcome := classify(rule, full); outcome != OutcomeAvailable {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeAvailable)
	}
	if region := extractRegion(rule.Region, full); region != "" {
		t.Fatalf("region = %q, want empty without a region-prefixed Location", region)
	}

	// The measured 301 shape still yields its region from the header alone.
	withLocation := map[string]stepResult{"original": netflixRedirect("80100172")}
	if region := extractRegion(rule.Region, withLocation); region != "JP" {
		t.Fatalf("region = %q, want %q", region, "JP")
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
