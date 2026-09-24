package platform

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"prism/internal/intel"
	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/quality"
)

// QualityLookupFunc retrieves the legacy quality summary for an egress IP.
//
// It is kept for callers that only have the quality inspection summary; the
// authoritative admission path uses QualitySnapshotReader (WP10 §2).
type QualityLookupFunc func(netip.Addr) quality.Summary

// QualitySnapshotReader is the read-only intel projection the admission path
// reads (WP10 §2). It never touches the database or the network, which keeps
// R3 intact. *intel.Snapshot satisfies it.
type QualitySnapshotReader interface {
	Assessment(ip netip.Addr) (intel.AssessmentLite, bool)
	CheckOutcome(nodeHash, checkID string) (intel.OutcomeLite, bool)
}

// Admission reason codes (WP10 §2 rule 2..12).
const (
	ReasonQualityEgressUnknown = "QUALITY_EGRESS_UNKNOWN"
	ReasonQualityUnknown       = "QUALITY_UNKNOWN"
	ReasonQualityTor           = "QUALITY_TOR"
	ReasonQualityHighRisk      = "QUALITY_HIGH_RISK"
	ReasonQualityVerdict       = "QUALITY_VERDICT"
	ReasonQualityMinPurity     = "QUALITY_MIN_PURITY"
	ReasonQualityIPType        = "QUALITY_IP_TYPE"
	ReasonQualityConfidence    = "QUALITY_CONFIDENCE"
	ReasonQualityNative        = "QUALITY_NATIVE"
	ReasonQualityCheckPrefix   = "QUALITY_CHECK:"
	ReasonQualityStale         = "QUALITY_STALE"
	ReasonQualityEgressStale   = "QUALITY_EGRESS_STALE"
)

// QualityDecision is one explainable admission check (§2.1).
type QualityDecision struct {
	Rule   string `json:"rule"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// qualityFacts is the neutral view the rule engine works on. Both the legacy
// quality.Summary path and the intel projection path are converted into it, so
// the rules exist exactly once.
type qualityFacts struct {
	IP          netip.Addr
	HasEgress   bool
	State       string
	Score       *int
	Verdict     string
	IPType      string
	Confidence  string
	Native      *bool
	Tor         bool
	HighRisk    bool
	ComputedAt  time.Time
	EgressAt    time.Time
	CheckLookup func(checkID string) (intel.OutcomeLite, bool)
	NodeHash    string
}

// AdmitQuality applies the WP10 §2 rule list and reports the first failure.
//
// An empty policy always passes, which reproduces the upstream Resin behaviour
// (deviation X5). A non-empty policy is fail-closed: a node whose quality cannot
// be established is rejected unless UnknownAction says otherwise.
func AdmitQuality(
	policy model.QualityPolicy,
	entry *node.NodeEntry,
	snap QualitySnapshotReader,
	now time.Time,
) (bool, string) {
	if policy.IsEmpty() || entry == nil {
		return true, ""
	}
	return admit(policy, factsFromEntry(entry, snap, now), now)
}

// EvaluateQuality is the legacy adapter for callers that only have a
// quality.Summary. It runs the same rule engine as AdmitQuality.
func EvaluateQuality(
	policy model.QualityPolicy,
	summary quality.Summary,
	egressObservedAt time.Time,
	now time.Time,
) bool {
	if policy.IsEmpty() {
		return true
	}
	return admitFactsOnly(policy, factsFromSummary(summary, egressObservedAt), now)
}

// ExplainQuality returns the per-rule result of the §2 admission list for one
// node, in the order the rules are evaluated (§2.1 explain endpoint).
func ExplainQuality(
	policy model.QualityPolicy,
	entry *node.NodeEntry,
	snap QualitySnapshotReader,
	now time.Time,
) []QualityDecision {
	if policy.IsEmpty() {
		return []QualityDecision{{Rule: "quality", Passed: true, Detail: "no quality policy configured"}}
	}
	facts := factsFromEntry(entry, snap, now)
	rules := qualityRules(policy, facts, now)
	out := make([]QualityDecision, 0, len(rules))
	for _, rule := range rules {
		out = append(out, QualityDecision{Rule: rule.name, Passed: rule.passed, Detail: rule.detail})
	}
	return out
}

// rule is one evaluated admission rule.
type rule struct {
	name   string
	reason string
	passed bool
	detail string
}

// admit returns the first failing rule.
func admit(policy model.QualityPolicy, facts qualityFacts, now time.Time) (bool, string) {
	for _, r := range qualityRules(policy, facts, now) {
		if !r.passed {
			return false, r.reason
		}
	}
	return true, ""
}

// admitFactsOnly is admit without a check lookup (the legacy summary path never
// carries per-node check outcomes).
func admitFactsOnly(policy model.QualityPolicy, facts qualityFacts, now time.Time) bool {
	for _, r := range qualityRules(policy, facts, now) {
		if !r.passed {
			return false
		}
	}
	return true
}

// qualityRules evaluates every §2 rule, in order. Reasons are prefixed with
// "quality." so the explain response and the rejection reason share one name.
func qualityRules(policy model.QualityPolicy, facts qualityFacts, now time.Time) []rule {
	rules := make([]rule, 0, 12)

	// 1. Egress IP.
	egress := rule{name: "quality.egress", reason: ReasonQualityEgressUnknown, passed: facts.HasEgress}
	if !facts.HasEgress {
		egress.detail = "no egress ip observed"
	} else {
		egress.detail = facts.IP.String()
	}
	rules = append(rules, egress)
	if !facts.HasEgress {
		return rules
	}

	// 2. Assessment availability (unknown_action).
	unknown := facts.State != "valid" && facts.State != "conflicting"
	if unknown {
		allowed := policy.UnknownAction == "allow"
		rules = append(rules, rule{
			name:   "quality.unknown",
			reason: ReasonQualityUnknown,
			passed: allowed,
			detail: "state=" + facts.State,
		})
		if !allowed {
			return rules
		}
		// The caller explicitly accepted unassessed nodes: no other rule can be
		// evaluated, so admission ends here.
		return rules
	}

	// 3. Tor.
	if excludeTor(policy) {
		rules = append(rules, rule{
			name:   "quality.exclude_tor",
			reason: ReasonQualityTor,
			passed: !facts.Tor,
			detail: boolDetail(facts.Tor, "tor flag set", "no tor flag"),
		})
	}

	// 4. High risk verdict.
	if excludeHighRisk(policy) {
		high := facts.HighRisk
		rules = append(rules, rule{
			name:   "quality.exclude_high_risk",
			reason: ReasonQualityHighRisk,
			passed: !high,
			detail: "verdict=" + facts.Verdict,
		})
	}

	// 5. Verdict allow-list.
	if len(policy.AllowedVerdicts) > 0 {
		ok := containsString(policy.AllowedVerdicts, facts.Verdict)
		rules = append(rules, rule{
			name:   "quality.allowed_verdicts",
			reason: ReasonQualityVerdict,
			passed: ok,
			detail: "verdict=" + facts.Verdict + " allowed=" + strings.Join(policy.AllowedVerdicts, "|"),
		})
	}

	// 6. Minimum purity. A NULL score never satisfies a configured minimum.
	if policy.MinPurity != nil {
		ok := facts.Score != nil && *facts.Score >= *policy.MinPurity
		rules = append(rules, rule{
			name:   "quality.min_purity",
			reason: ReasonQualityMinPurity,
			passed: ok,
			detail: scoreDetail(facts.Score, *policy.MinPurity),
		})
	}

	// 7. IP type allow-list.
	if len(policy.IPTypes) > 0 {
		ok := containsString(policy.IPTypes, facts.IPType)
		rules = append(rules, rule{
			name:   "quality.ip_types",
			reason: ReasonQualityIPType,
			passed: ok,
			detail: "ip_type=" + facts.IPType + " allowed=" + strings.Join(policy.IPTypes, "|"),
		})
	}

	// 8. Minimum confidence.
	if policy.MinConfidence != "" {
		want := confidenceRank(policy.MinConfidence)
		got := confidenceRank(facts.Confidence)
		rules = append(rules, rule{
			name:   "quality.min_confidence",
			reason: ReasonQualityConfidence,
			passed: got >= want,
			detail: fmt.Sprintf("confidence=%s<%s", facts.Confidence, policy.MinConfidence),
		})
	}

	// 9. Native egress. Missing evidence never satisfies the requirement.
	if policy.RequireNative {
		ok := facts.Native != nil && *facts.Native
		rules = append(rules, rule{
			name:   "quality.require_native",
			reason: ReasonQualityNative,
			passed: ok,
			detail: nativeDetail(facts.Native),
		})
	}

	// 10. Required checks.
	for _, checkID := range sortedCheckIDs(policy.RequiredChecks) {
		want := policy.RequiredChecks[checkID]
		outcome, found := intel.OutcomeLite{}, false
		if facts.CheckLookup != nil {
			outcome, found = facts.CheckLookup(checkID)
		}
		passed := false
		detail := "outcome=missing"
		switch {
		case !found:
		case !outcome.Valid(now.UnixNano()):
			detail = "outcome=stale"
		default:
			got := intel.OutcomeName(outcome.Outcome)
			passed = got == want
			detail = fmt.Sprintf("outcome=%s want=%s", got, want)
		}
		rules = append(rules, rule{
			name:   "quality.check." + checkID,
			reason: ReasonQualityCheckPrefix + checkID,
			passed: passed,
			detail: detail,
		})
	}

	// 11. Assessment (evidence) age, measured against the stored computed_at.
	if maxAge, ok := parsePolicyDuration(policy.MaxAssessmentAge); ok && maxAge > 0 {
		passed := true
		detail := "computed_at=unknown"
		if !facts.ComputedAt.IsZero() {
			age := now.Sub(facts.ComputedAt)
			passed = age <= maxAge
			detail = fmt.Sprintf("age=%s>%s", age.Round(time.Second), maxAge)
		} else {
			// Fail closed: an assessment without a timestamp cannot be proven fresh.
			passed = false
		}
		rules = append(rules, rule{name: "quality.max_assessment_age", reason: ReasonQualityStale, passed: passed, detail: detail})
	}

	// 12. Egress observation age.
	if maxAge, ok := parsePolicyDuration(policy.MaxEgressAge); ok && maxAge > 0 {
		passed := true
		detail := "egress_observed_at=unknown"
		if !facts.EgressAt.IsZero() {
			age := now.Sub(facts.EgressAt)
			passed = age <= maxAge
			detail = fmt.Sprintf("age=%s>%s", age.Round(time.Second), maxAge)
		} else {
			passed = false
		}
		rules = append(rules, rule{name: "quality.max_egress_age", reason: ReasonQualityEgressStale, passed: passed, detail: detail})
	}

	return rules
}

// ------------------------------------------------------------------
// Facts adapters
// ------------------------------------------------------------------

// factsFromEntry projects the intel snapshot plus the node entry.
func factsFromEntry(entry *node.NodeEntry, snap QualitySnapshotReader, now time.Time) qualityFacts {
	facts := qualityFacts{
		State:      "unobserved",
		IPType:     "unknown",
		Verdict:    "pending",
		EgressAt:   observedAt(entry),
		NodeHash:   entry.Hash.Hex(),
		Tor:        false,
		HighRisk:   false,
		ComputedAt: time.Time{},
	}
	ip := entry.GetEgressIP()
	if !ip.IsValid() {
		return facts
	}
	facts.IP = ip.Unmap()
	facts.HasEgress = true
	if snap == nil {
		return facts
	}
	lite, ok := snap.Assessment(facts.IP)
	if !ok {
		facts.State = "unobserved"
		return facts
	}
	facts.CheckLookup = func(checkID string) (intel.OutcomeLite, bool) {
		lite, found := snap.CheckOutcome(facts.NodeHash, checkID)
		return lite, found
	}
	facts.Score = lite.ScoreValue()
	facts.IPType = intel.IPTypeName(lite.IPType)
	facts.Confidence = intel.ConfidenceName(lite.Confidence)
	facts.Verdict = intel.VerdictName(lite.Verdict)
	facts.Native = lite.NativeValue()
	facts.Tor = lite.Flags&intel.FlagTor != 0
	facts.HighRisk = facts.Verdict == "high_risk"
	if lite.ComputedAt > 0 {
		facts.ComputedAt = time.Unix(0, lite.ComputedAt).UTC()
	}
	// WP08's projection has no state column: a pending verdict is exactly the
	// "no usable verdict yet" case (state pending/unsupported in §2 rule 2), and
	// an expired valid_until is the "stale" case.
	switch {
	case !lite.Valid(now.UnixNano()):
		facts.State = "stale"
	case facts.Verdict == "pending":
		facts.State = "pending"
	case facts.IPType == "conflicting" || facts.Verdict == "conflicting":
		facts.State = "conflicting"
	default:
		facts.State = "valid"
	}
	return facts
}

// factsFromSummary keeps the legacy quality.Summary path working. The caller
// already resolved the node egress IP to obtain the summary, so the egress rule
// is satisfied by construction.
func factsFromSummary(summary quality.Summary, egressObservedAt time.Time) qualityFacts {
	facts := qualityFacts{
		State:     summary.State,
		HasEgress: true,
		IPType:    "unknown",
		Verdict:   "pending",
		EgressAt:  egressObservedAt,
	}
	if summary.IP != "" {
		if ip, err := netip.ParseAddr(summary.IP); err == nil {
			facts.IP = ip.Unmap()
		}
	}
	assessment := summary.Assessment
	if assessment == nil {
		if facts.State == "" {
			facts.State = "unobserved"
		}
		return facts
	}
	facts.Score = assessment.PurityScore
	facts.Verdict = assessment.Verdict
	facts.IPType = assessment.NetworkType
	facts.Native = assessment.Native
	facts.Tor = len(assessment.TorRoles) > 0
	facts.HighRisk = assessment.Verdict == "high_risk"
	if assessment.State != "" && assessment.State != "valid" {
		facts.State = assessment.State
	} else {
		facts.State = "valid"
	}
	return facts
}

// observedAt reads the last successful egress observation of a node.
func observedAt(entry *node.NodeEntry) time.Time {
	if entry == nil {
		return time.Time{}
	}
	if ns := entry.LastEgressUpdate.Load(); ns > 0 {
		return time.Unix(0, ns).UTC()
	}
	return time.Time{}
}

// ------------------------------------------------------------------
// helpers
// ------------------------------------------------------------------

func excludeHighRisk(policy model.QualityPolicy) bool {
	return policy.ExcludeHighRisk == nil || *policy.ExcludeHighRisk
}

func excludeTor(policy model.QualityPolicy) bool {
	return policy.ExcludeTor == nil || *policy.ExcludeTor
}

// confidenceRank orders the §1.2 confidence values.
func confidenceRank(name string) int {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func parsePolicyDuration(raw string) (time.Duration, bool) {
	if raw == "" {
		return 0, false
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, false
	}
	return d, true
}

func boolDetail(present bool, yes, no string) string {
	if present {
		return yes
	}
	return no
}

func scoreDetail(score *int, minimum int) string {
	if score == nil {
		return fmt.Sprintf("score=unknown<%d", minimum)
	}
	return fmt.Sprintf("score=%d<%d", *score, minimum)
}

func nativeDetail(native *bool) string {
	switch {
	case native == nil:
		return "native=unknown"
	case *native:
		return "native=true"
	default:
		return "native=false"
	}
}

// sortedCheckIDs keeps the rule order deterministic.
func sortedCheckIDs(checks map[string]string) []string {
	out := make([]string, 0, len(checks))
	for id := range checks {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
