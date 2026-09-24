// Package assess implements the explainable purity scoring of WP10 §1
// (profile "prism-purity-v2"). Every function here is pure: the same input
// always produces the same output, so the whole algorithm is unit-testable
// without a database, a network or a clock.
package assess

import (
	"fmt"
	"math"
	"net/netip"
	"sort"
	"strings"
	"time"

	"prism/internal/quality"
)

// Profile is the assessment profile written to ip_assessment.profile. WP10 §1.8
// recomputes every row whose profile differs from this value.
const Profile = "prism-purity-v2"

// Scoring source ids (§1.1). The order is the stable output order of
// Assessment.Components.
const (
	SourceProxycheck = "proxycheck"
	SourceIPPure     = "ippure"
	SourceIPQS       = "ipqs"
	SourceAbuseIPDB  = "abuseipdb"
	SourceIPAPIIs    = "ipapi_is"
	SourceIPAPI      = "ip_api"
	SourceDNSBL      = "dnsbl"
)

// Non-scoring sources referenced by the algorithm.
const (
	SourceTorProject = "torproject"
	SourceGeoCountry = "geo_country"
)

// Scoring sources with their §1.1 weights.
var scoringWeights = map[string]int{
	SourceProxycheck: 3,
	SourceIPPure:     3,
	SourceIPQS:       3,
	SourceAbuseIPDB:  2,
	SourceIPAPIIs:    1,
	SourceIPAPI:      1,
	SourceDNSBL:      1,
}

// ScoringSources returns the scoring source ids in weight order. It is the
// canonical list a caller uses to build the "enabled sources" map.
func ScoringSources() []string {
	out := []string{SourceProxycheck, SourceIPPure, SourceIPQS, SourceAbuseIPDB, SourceIPAPIIs, SourceIPAPI, SourceDNSBL}
	return out
}

// offlineSources carry the registry/registration data used by the offline
// fallbacks of §1.4 and §1.5.
var offlineSources = map[string]bool{
	SourceGeoCountry:   true,
	"dbip_lite":        true,
	"maxmind_geolite2": true,
	"ipinfo_lite":      true,
	SourceTorProject:   true,
}

// componentOrder is the deterministic output order of components.
var componentOrder = []string{
	SourceProxycheck, SourceIPPure, SourceIPQS, SourceAbuseIPDB, SourceIPAPIIs, SourceIPAPI, SourceDNSBL,
}

// Flags is the 16-bit summary of an assessment that the routing admission path
// reads from the in-memory projection (WP08 §5). The bit values are part of the
// ip_assessment.flags contract and must not be renumbered.
type Flags uint16

// Assessment flag bits (§1.1).
const (
	FlagCompromised   Flags = 1 << 0
	FlagTor           Flags = 1 << 1
	FlagVPN           Flags = 1 << 2
	FlagProxy         Flags = 1 << 3
	FlagScraper       Flags = 1 << 4
	FlagAbuse         Flags = 1 << 5
	FlagAnonymous     Flags = 1 << 6
	FlagHosting       Flags = 1 << 7
	FlagDNSBL         Flags = 1 << 8
	FlagAttackHistory Flags = 1 << 9
)

// Emission point costs of §1.1. Each flag is applied at most once, no matter how
// many sources report it.
const (
	penaltyCompromised = -40
	penaltyTor         = -30
	penaltyVPN         = -10
	penaltyProxy       = -10
	penaltyScraper     = -10
	penaltyAbuse       = -10
)

// abuseDeductionConfidence is the AbuseConfidence threshold of the §1.1
// "abuse" deduction.
const abuseDeductionConfidence = 25

// abuseHighRiskConfidence is the AbuseConfidence threshold of the §1.6
// high_risk verdict.
const abuseHighRiskConfidence = 75

// dnsblPenaltyPerZone is the per-listed-zone penalty of §1.1.
const dnsblPenaltyPerZone = 34

// Verdict values of §1.6.
const (
	VerdictHighRisk    = "high_risk"
	VerdictConflicting = "conflicting"
	VerdictReview      = "review"
	VerdictPending     = "pending"
	VerdictIncomplete  = "incomplete"
	VerdictCaution     = "caution"
	VerdictFavorable   = "favorable"
)

// State values of §1.1.
const (
	StateValid       = "valid"
	StatePending     = "pending"
	StateUnsupported = "unsupported"
)

// voteOrder is the deterministic iteration order of the §1.4 vote types.
var voteOrder = []string{
	IPTypeResidential, IPTypeMobile, IPTypeBusiness, IPTypeWireless, IPTypeDatacenter,
}

// Confidence values of §1.2.
const (
	ConfidenceNone   = "none"
	ConfidenceLow    = "low"
	ConfidenceMedium = "medium"
	ConfidenceHigh   = "high"
)

// IP types of §1.4.
const (
	IPTypeUnknown        = "unknown"
	IPTypeResidential    = "residential"
	IPTypeMobile         = "mobile"
	IPTypeBusiness       = "business"
	IPTypeWireless       = "wireless"
	IPTypeDatacenter     = "datacenter"
	IPTypeNonResidential = "non_residential"
	IPTypeConflicting    = "conflicting"
)

// Reason codes. They are stable strings: the API exposes them as-is.
const (
	ReasonCompromised        = "COMPROMISED"
	ReasonTorExit            = "TOR_EXIT"
	ReasonTorRelay           = "TOR_RELAY"
	ReasonVPN                = "VPN_DETECTED"
	ReasonProxy              = "PROXY_DETECTED"
	ReasonScraper            = "SCRAPER_DETECTED"
	ReasonAnonymous          = "ANONYMOUS_DETECTED"
	ReasonRecentAbuse        = "RECENT_ABUSE"
	ReasonAbuseConfidence    = "ABUSE_CONFIDENCE"
	ReasonAttackHistory      = "ATTACK_HISTORY"
	ReasonHosting            = "HOSTING_DETECTED"
	ReasonASNHosting         = "ASN_HOSTING_HEURISTIC"
	ReasonNativeHeuristic    = "NATIVE_HEURISTIC"
	ReasonIPTypeConflicting  = "IP_TYPE_CONFLICTING"
	ReasonIPTypeUnknown      = "IP_TYPE_UNKNOWN"
	ReasonLowPurity          = "LOW_PURITY"
	ReasonNoValidEvidence    = "NO_VALID_EVIDENCE"
	ReasonAllSourcesFailed   = "ALL_ENABLED_SOURCES_FAILED"
	ReasonNoScoringSource    = "NO_SCORING_SOURCE_ENABLED"
	ReasonIncompleteCoverage = "INCOMPLETE_COVERAGE"
)

// dnsblListedReason renders the per-zone listing reason of §1.7.
func dnsblListedReason(zone string) string {
	return "DNSBL_LISTED:" + zone
}

// Component is one scored source item of §1.1's components_json.
type Component struct {
	Source     string    `json:"source"`
	Raw        int       `json:"raw"`
	Clean      int       `json:"clean"`
	Weight     int       `json:"weight"`
	ObservedAt time.Time `json:"observed_at"`
}

// Penalty is one applied deduction. Points is always negative.
type Penalty struct {
	Flag    string   `json:"flag"`
	Points  int      `json:"points"`
	Sources []string `json:"sources"`
}

// Assessment is the complete, explainable result of one scoring run.
type Assessment struct {
	IP            netip.Addr  `json:"-"`
	IPText        string      `json:"ip"`
	Profile       string      `json:"profile"`
	State         string      `json:"state"`
	Verdict       string      `json:"verdict"`
	PurityScore   *int        `json:"purity_score"`
	PurityBand    string      `json:"purity_band"`
	Confidence    string      `json:"confidence"`
	Coverage      float64     `json:"coverage"`
	Agreement     float64     `json:"agreement"`
	IPType        string      `json:"ip_type"`
	Native        *bool       `json:"native"`
	Flags         Flags       `json:"flags"`
	Reasons       []string    `json:"reasons"`
	Components    []Component `json:"components"`
	Penalties     []Penalty   `json:"penalties"`
	PenaltyPoints int         `json:"penalty_points"`
	SourceCount   int         `json:"source_count"`
	ASN           int         `json:"asn,omitempty"`
	ASOrg         string      `json:"as_org,omitempty"`
	Country       string      `json:"country,omitempty"`
	City          string      `json:"city,omitempty"`
	ComputedAt    time.Time   `json:"computed_at"`
	ValidUntil    time.Time   `json:"valid_until"`
}

// Input is the full input of one scoring run.
type Input struct {
	// IP is the address being assessed.
	IP netip.Addr
	// Evidence holds every status=ok evidence row of IP. Rows that are expired
	// at Now are ignored, so callers may pass the raw store rows.
	Evidence []quality.Evidence
	// Enabled lists the currently enabled data sources. It is used for the
	// coverage denominator: nil means "every scoring source is enabled", an
	// empty (non-nil) map means none is.
	Enabled map[string]bool
	// Failed maps an enabled source to the error code of its last failed
	// lookup. An enabled source that failed and produced no valid evidence
	// moves a score-less assessment from pending to unsupported (§1.1).
	Failed map[string]string
	// Now is the evaluation instant (UTC).
	Now time.Time
}

// AssessInput is the full entry point of the algorithm.
func AssessInput(in Input) Assessment {
	now := in.Now.UTC()
	out := Assessment{
		IP:         in.IP.Unmap(),
		IPText:     in.IP.Unmap().String(),
		Profile:    Profile,
		State:      StatePending,
		Verdict:    VerdictPending,
		PurityBand: "unknown",
		Confidence: ConfidenceNone,
		IPType:     IPTypeUnknown,
		Reasons:    []string{},
		Components: []Component{},
		Penalties:  []Penalty{},
		ASN:        0,
		ComputedAt: now,
		ValidUntil: now.Add(time.Hour),
	}

	evidence := validEvidence(in)
	if len(evidence) > 0 {
		out.ValidUntil = earliestValidUntil(evidence)
	}

	out.Components = components(evidence)
	out.SourceCount = len(out.Components)
	out.Coverage, out.Agreement = coverageAndAgreement(out.Components, in.Enabled)
	out.Confidence = confidence(out.SourceCount, out.Coverage, out.Agreement)

	flags, reasons := detectFlags(evidence)
	out.Flags = flags
	out.Reasons = append(out.Reasons, reasons...)

	// Identity first: the §1.4 hosting-ASN fallback needs the ASN.
	attachIdentity(&out, evidence)

	out.IPType = voteIPType(evidence, out.ASN, &out.Reasons)
	out.Native = nativeFlag(evidence, &out.Reasons)

	out.Penalties, out.PenaltyPoints = penalties(out.Flags, evidence)

	if out.SourceCount > 0 {
		base := weightedBase(out.Components)
		total := base + out.PenaltyPoints
		if total < 0 {
			total = 0
		}
		if total > 100 {
			total = 100
		}
		score := total
		out.PurityScore = &score
		out.PurityBand = quality.PurityBand(out.PurityScore)
		out.State = StateValid
	} else {
		state, reason := noEvidenceState(in, evidence)
		out.State = state
		out.Reasons = append(out.Reasons, reason)
	}

	out.Verdict = verdict(&out, evidence)
	out.Reasons = dedupeReasons(out.Reasons)
	return out
}

// Assess is the §1 signature. Evidence must already be status=ok rows; expiry is
// evaluated against Now. A caller that knows which sources failed and wants the
// pending/unsupported distinction uses AssessInput instead.
func Assess(ip netip.Addr, ev []quality.Evidence, now time.Time, enabled map[string]bool) Assessment {
	return AssessInput(Input{IP: ip, Evidence: ev, Now: now, Enabled: enabled})
}

// ------------------------------------------------------------------
// Evidence selection
// ------------------------------------------------------------------

// validEvidence keeps only unexpired, address-matching rows and keeps the newest
// row per provider.
func validEvidence(in Input) []quality.Evidence {
	now := in.Now.UTC()
	want := in.IP.Unmap().String()
	byProvider := make(map[string]quality.Evidence, len(in.Evidence))
	for _, ev := range in.Evidence {
		if ev.Provider == "" {
			continue
		}
		if ev.IP != "" && ev.IP != want {
			continue
		}
		if !ev.ValidUntil.IsZero() && !now.Before(ev.ValidUntil.UTC()) {
			continue
		}
		current, ok := byProvider[ev.Provider]
		if !ok || ev.ObservedAt.After(current.ObservedAt) {
			byProvider[ev.Provider] = ev
		}
	}
	out := make([]quality.Evidence, 0, len(byProvider))
	for _, ev := range byProvider {
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// earliestValidUntil is the §1.7 validity window: the earliest expiry of all
// valid evidence.
func earliestValidUntil(evidence []quality.Evidence) time.Time {
	earliest := time.Time{}
	for _, ev := range evidence {
		if ev.ValidUntil.IsZero() {
			continue
		}
		if earliest.IsZero() || ev.ValidUntil.UTC().Before(earliest) {
			earliest = ev.ValidUntil.UTC()
		}
	}
	if earliest.IsZero() {
		return time.Time{}
	}
	return earliest
}

// lookup returns the valid evidence of one provider.
func lookup(evidence []quality.Evidence, provider string) (quality.Evidence, bool) {
	for _, ev := range evidence {
		if ev.Provider == provider {
			return ev, true
		}
	}
	return quality.Evidence{}, false
}

// ------------------------------------------------------------------
// Components (§1.1)
// ------------------------------------------------------------------

// components builds the clean sub-scores in the stable component order.
func components(evidence []quality.Evidence) []Component {
	out := make([]Component, 0, len(componentOrder))
	for _, source := range componentOrder {
		ev, ok := lookup(evidence, source)
		if !ok {
			continue
		}
		weight := scoringWeights[source]
		raw, clean, ok := componentScore(source, ev)
		if !ok {
			continue
		}
		out = append(out, Component{
			Source:     source,
			Raw:        raw,
			Clean:      clean,
			Weight:     weight,
			ObservedAt: ev.ObservedAt.UTC(),
		})
	}
	return out
}

// componentScore converts one provider's evidence into a 0–100 clean score.
// ok is false when the provider produced no scoring value.
func componentScore(source string, ev quality.Evidence) (raw, clean int, ok bool) {
	switch source {
	case SourceProxycheck:
		if ev.RiskScore == nil {
			return 0, 0, false
		}
		raw = clampInt(*ev.RiskScore, 0, 100)
		return raw, 100 - raw, true
	case SourceIPPure:
		// IPPure's fraud score is IPv4-only (§1.1, §3).
		if ev.FraudScore == nil || !isIPv4(ev.IP) {
			return 0, 0, false
		}
		raw = clampInt(*ev.FraudScore, 0, 100)
		return raw, 100 - raw, true
	case SourceIPQS:
		if ev.FraudScore == nil {
			return 0, 0, false
		}
		raw = clampInt(*ev.FraudScore, 0, 100)
		return raw, 100 - raw, true
	case SourceAbuseIPDB:
		if ev.AbuseConfidence == nil {
			return 0, 0, false
		}
		raw = clampInt(*ev.AbuseConfidence, 0, 100)
		return raw, 100 - raw, true
	case SourceIPAPIIs:
		switch {
		case boolValue(ev.Signals.Compromised):
			return 40, 40, true
		case boolValue(ev.Signals.Proxy), boolValue(ev.Signals.VPN), boolValue(ev.Signals.Tor):
			return 60, 60, true
		default:
			return 100, 100, true
		}
	case SourceIPAPI:
		if ev.Signals.Proxy == nil {
			return 0, 0, false
		}
		if *ev.Signals.Proxy {
			return 60, 60, true
		}
		return 100, 100, true
	case SourceDNSBL:
		// Only zones that were queried successfully are counted (§1.1).
		if len(ev.DNSBLChecked) == 0 {
			return 0, 0, false
		}
		hits := listedZones(ev)
		raw = len(hits)
		clean = 100 - dnsblPenaltyPerZone*len(hits)
		if clean < 0 {
			clean = 0
		}
		return raw, clean, true
	default:
		return 0, 0, false
	}
}

// listedZones returns the listed zones that were actually checked.
func listedZones(ev quality.Evidence) []string {
	if len(ev.DNSBLListed) == 0 {
		return nil
	}
	checked := make(map[string]bool, len(ev.DNSBLChecked))
	for _, zone := range ev.DNSBLChecked {
		checked[strings.ToLower(strings.TrimSpace(zone))] = true
	}
	out := make([]string, 0, len(ev.DNSBLListed))
	for _, zone := range ev.DNSBLListed {
		zone = strings.TrimSpace(zone)
		if zone == "" || !checked[strings.ToLower(zone)] {
			continue
		}
		out = append(out, zone)
	}
	sort.Strings(out)
	return out
}

// weightedBase is Σ(w·clean)/Σw over the available components.
func weightedBase(items []Component) int {
	sum, weight := 0, 0
	for _, item := range items {
		sum += item.Weight * item.Clean
		weight += item.Weight
	}
	if weight == 0 {
		return 0
	}
	return int(math.Round(float64(sum) / float64(weight)))
}

// ------------------------------------------------------------------
// Confidence (§1.2)
// ------------------------------------------------------------------

// coverageAndAgreement computes the §1.2 confidence inputs.
func coverageAndAgreement(items []Component, enabled map[string]bool) (coverage, agreement float64) {
	denominator := 0
	for _, source := range ScoringSources() {
		if sourceEnabled(enabled, source) {
			denominator += scoringWeights[source]
		}
	}
	numerator := 0
	for _, item := range items {
		numerator += item.Weight
	}
	if denominator > 0 {
		coverage = float64(numerator) / float64(denominator)
		if coverage > 1 {
			coverage = 1
		}
	}
	if len(items) == 0 {
		return coverage, 0
	}
	mean := 0.0
	for _, item := range items {
		mean += float64(item.Clean)
	}
	mean /= float64(len(items))
	variance := 0.0
	for _, item := range items {
		d := float64(item.Clean) - mean
		variance += d * d
	}
	variance /= float64(len(items))
	agreement = 1 - math.Min(1, math.Sqrt(variance)/50)
	return coverage, agreement
}

// sourceEnabled reports whether a source is enabled. A nil map means every
// source is enabled (the caller does not know better).
func sourceEnabled(enabled map[string]bool, source string) bool {
	if enabled == nil {
		return true
	}
	return enabled[source]
}

// confidence maps the §1.2 table.
func confidence(sourceCount int, coverage, agreement float64) string {
	switch {
	case sourceCount == 0:
		return ConfidenceNone
	case sourceCount >= 2 && coverage >= 0.6 && agreement >= 0.7:
		return ConfidenceHigh
	case sourceCount >= 2 || coverage >= 0.4:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

// ------------------------------------------------------------------
// Signals and deductions (§1.1)
// ------------------------------------------------------------------

// detectFlags derives the flag bitmask and the reason codes.
func detectFlags(evidence []quality.Evidence) (Flags, []string) {
	var flags Flags
	reasons := make([]string, 0, 8)
	report := func(flag Flags, reason string, present bool) {
		if !present {
			return
		}
		flags |= flag
		reasons = append(reasons, reason)
	}

	compromised, torSignal, vpn, proxy, scraper, anonymous, hosting := false, false, false, false, false, false, false
	attackHistory := false
	for _, ev := range evidence {
		compromised = compromised || boolValue(ev.Signals.Compromised)
		torSignal = torSignal || boolValue(ev.Signals.Tor)
		vpn = vpn || boolValue(ev.Signals.VPN)
		proxy = proxy || boolValue(ev.Signals.Proxy)
		scraper = scraper || boolValue(ev.Signals.Scraper)
		anonymous = anonymous || boolValue(ev.Signals.Anonymous)
		hosting = hosting || boolValue(ev.Signals.Hosting)
		if len(ev.AttackHistory) > 0 {
			attackHistory = true
		}
	}

	// Tor roles come from the torproject registry (§1.6).
	if tor, ok := lookup(evidence, SourceTorProject); ok {
		if hasRole(tor.TorRoles, "exit") {
			torSignal = true
			reasons = append(reasons, ReasonTorExit)
		} else if len(tor.TorRoles) > 0 {
			reasons = append(reasons, ReasonTorRelay)
		}
	}

	report(FlagCompromised, ReasonCompromised, compromised)
	if torSignal {
		flags |= FlagTor
		if !containsString(reasons, ReasonTorExit) {
			reasons = append(reasons, ReasonTorExit)
		}
	}
	report(FlagVPN, ReasonVPN, vpn)
	report(FlagProxy, ReasonProxy, proxy)
	report(FlagScraper, ReasonScraper, scraper)
	report(FlagAnonymous, ReasonAnonymous, anonymous)
	report(FlagHosting, ReasonHosting, hosting)
	report(FlagAttackHistory, ReasonAttackHistory, attackHistory)

	// Abuse confidence (§1.1 and §1.6 use two thresholds).
	if abuse, ok := lookup(evidence, SourceAbuseIPDB); ok && abuse.AbuseConfidence != nil {
		confidence := clampInt(*abuse.AbuseConfidence, 0, 100)
		if confidence >= abuseHighRiskConfidence && abuse.TotalReports != nil && *abuse.TotalReports > 0 {
			reasons = append(reasons, ReasonRecentAbuse)
		}
		if confidence >= abuseDeductionConfidence {
			flags |= FlagAbuse
			reasons = append(reasons, ReasonAbuseConfidence)
		}
	}

	// DNSBL listings.
	if dnsbl, ok := lookup(evidence, SourceDNSBL); ok {
		for _, zone := range listedZones(dnsbl) {
			flags |= FlagDNSBL
			reasons = append(reasons, dnsblListedReason(zone))
		}
	}
	return flags, reasons
}

// penalties turns the flags into the §1.1 deductions. Each flag is applied at
// most once.
func penalties(flags Flags, evidence []quality.Evidence) ([]Penalty, int) {
	order := []struct {
		flag   Flags
		name   string
		points int
	}{
		{FlagCompromised, "compromised", penaltyCompromised},
		{FlagTor, "tor", penaltyTor},
		{FlagVPN, "vpn", penaltyVPN},
		{FlagProxy, "proxy", penaltyProxy},
		{FlagScraper, "scraper", penaltyScraper},
		{FlagAbuse, "abuse", penaltyAbuse},
	}
	out := make([]Penalty, 0, len(order))
	total := 0
	for _, entry := range order {
		if flags&entry.flag == 0 {
			continue
		}
		total += entry.points
		out = append(out, Penalty{
			Flag:    entry.name,
			Points:  entry.points,
			Sources: flagSources(entry.flag, evidence),
		})
	}
	return out, total
}

// flagSources lists the providers that reported one flag.
func flagSources(flag Flags, evidence []quality.Evidence) []string {
	out := make([]string, 0, 3)
	for _, ev := range evidence {
		reported := false
		switch flag {
		case FlagCompromised:
			reported = boolValue(ev.Signals.Compromised)
		case FlagTor:
			reported = boolValue(ev.Signals.Tor) ||
				(ev.Provider == SourceTorProject && hasRole(ev.TorRoles, "exit"))
		case FlagVPN:
			reported = boolValue(ev.Signals.VPN)
		case FlagProxy:
			reported = boolValue(ev.Signals.Proxy)
		case FlagScraper:
			reported = boolValue(ev.Signals.Scraper)
		case FlagAbuse:
			reported = ev.AbuseConfidence != nil && *ev.AbuseConfidence >= abuseDeductionConfidence
		}
		if reported {
			out = append(out, ev.Provider)
		}
	}
	sort.Strings(out)
	return out
}

// ------------------------------------------------------------------
// IP type (§1.4)
// ------------------------------------------------------------------

// voteIPType votes on the allocation type. asn is only used by the offline
// hosting fallback.
func voteIPType(evidence []quality.Evidence, asn int, reasons *[]string) string {
	counts := map[string]int{}
	votes := 0
	for _, ev := range evidence {
		kind := voteOf(ev)
		if kind == "" {
			continue
		}
		counts[kind]++
		votes++
	}

	// non_residential only wins when nothing else voted (§1.4).
	if votes > 0 {
		if _, ok := counts[IPTypeNonResidential]; ok && len(counts) == 1 {
			return IPTypeNonResidential
		}
		delete(counts, IPTypeNonResidential)
		votes = 0
		for _, n := range counts {
			votes += n
		}
	}

	if votes == 0 {
		if _, ok := hostingASNName(asn); ok && asn > 0 {
			*reasons = append(*reasons, ReasonASNHosting)
			return IPTypeDatacenter
		}
		*reasons = append(*reasons, ReasonIPTypeUnknown)
		return IPTypeUnknown
	}

	// Count in the fixed vote order so the result is deterministic (map
	// iteration would randomize tie-breaking).
	max := 0
	for _, kind := range voteOrder {
		if counts[kind] > max {
			max = counts[kind]
		}
	}
	best := ""
	for _, kind := range voteOrder {
		if counts[kind] != max {
			continue
		}
		if best == "" {
			best = kind
			continue
		}
		// Tie-break: prefer the non-datacenter side (§1.4).
		if best == IPTypeDatacenter && kind != IPTypeDatacenter {
			best = kind
		}
	}

	// Conflicting: residential or mobile vs datacenter, both with ≥1 vote, and no
	// type holds 2/3 of the votes.
	residentialish := counts[IPTypeResidential] + counts[IPTypeMobile]
	datacenter := counts[IPTypeDatacenter]
	if residentialish > 0 && datacenter > 0 && float64(max) < 2.0/3.0*float64(votes) {
		*reasons = append(*reasons, ReasonIPTypeConflicting)
		return IPTypeConflicting
	}
	return best
}

// voteOf maps one source's evidence to a §1.4 vote, or "" when it does not vote.
func voteOf(ev quality.Evidence) string {
	switch ev.Provider {
	case SourceProxycheck:
		return normalizeRawType(ev.IPType)
	case SourceIPQS:
		if mapped := ipqsConnectionType(ev.SourceType); mapped != "" {
			return mapped
		}
		return normalizeRawType(ev.IPType)
	case SourceIPAPIIs:
		if kind := normalizeRawType(ev.IPType); kind != "" {
			return kind
		}
		switch strings.ToLower(strings.TrimSpace(ev.SourceType)) {
		case "isp":
			return IPTypeResidential
		case "business":
			return IPTypeBusiness
		case "hosting":
			return IPTypeDatacenter
		}
		return ""
	case SourceIPAPI:
		return normalizeRawType(ev.IPType)
	case SourceIPPure:
		if ev.IsResidential == nil {
			return ""
		}
		if *ev.IsResidential {
			return IPTypeResidential
		}
		return IPTypeNonResidential
	default:
		return ""
	}
}

// ipqsConnectionType maps IPQualityScore's connection_type (§1.4).
func ipqsConnectionType(connectionType string) string {
	switch strings.ToLower(strings.TrimSpace(connectionType)) {
	case "residential":
		return IPTypeResidential
	case "mobile":
		return IPTypeMobile
	case "corporate", "education":
		return IPTypeBusiness
	case "data center", "datacenter":
		return IPTypeDatacenter
	default:
		return ""
	}
}

// normalizeRawType maps a provider network/usage type onto the vote set.
func normalizeRawType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "residential":
		return IPTypeResidential
	case "mobile":
		return IPTypeMobile
	case "business":
		return IPTypeBusiness
	case "wireless":
		return IPTypeWireless
	case "hosting", "datacenter", "data center":
		return IPTypeDatacenter
	case "non_residential", "non-residential":
		return IPTypeNonResidential
	default:
		return ""
	}
}

// ------------------------------------------------------------------
// Native (§1.5)
// ------------------------------------------------------------------

// nativeFlag resolves the native dimension.
func nativeFlag(evidence []quality.Evidence, reasons *[]string) *bool {
	if pure, ok := lookup(evidence, SourceIPPure); ok && pure.Native != nil {
		return pure.Native
	}
	for _, provider := range []string{"maxmind_geolite2", "dbip_lite", "ipinfo_lite", "geo_country"} {
		ev, ok := lookup(evidence, provider)
		if !ok {
			continue
		}
		country := strings.ToUpper(strings.TrimSpace(ev.CountryCode))
		registered := strings.ToUpper(strings.TrimSpace(ev.RegisteredCC))
		if country == "" || registered == "" {
			continue
		}
		native := country == registered
		*reasons = append(*reasons, ReasonNativeHeuristic)
		return &native
	}
	return nil
}

// ------------------------------------------------------------------
// Identity fields
// ------------------------------------------------------------------

// identityPriority is the source preference for the IP's ASN/country/city.
var identityPriority = []string{
	SourceProxycheck, SourceIPAPIIs, SourceIPQS, SourceIPAPI, SourceIPPure,
	"dbip_lite", "maxmind_geolite2", "ipinfo_lite", SourceGeoCountry,
}

// attachIdentity fills ASN/ASOrg/Country/City from the best available source.
func attachIdentity(out *Assessment, evidence []quality.Evidence) {
	for _, provider := range identityPriority {
		ev, ok := lookup(evidence, provider)
		if !ok {
			continue
		}
		if out.ASN == 0 && ev.ASNNumber > 0 {
			out.ASN = ev.ASNNumber
		}
		if out.ASOrg == "" && strings.TrimSpace(ev.Organization) != "" {
			out.ASOrg = ev.Organization
		}
		if out.Country == "" && strings.TrimSpace(ev.CountryCode) != "" {
			out.Country = strings.ToUpper(strings.TrimSpace(ev.CountryCode))
		}
		if out.City == "" && strings.TrimSpace(ev.City) != "" {
			out.City = ev.City
		}
	}
}

// ------------------------------------------------------------------
// Verdict (§1.6)
// ------------------------------------------------------------------

// verdict applies the §1.6 decision list in order; the first match wins.
func verdict(out *Assessment, evidence []quality.Evidence) string {
	// 1. high_risk.
	if out.Flags&FlagCompromised != 0 {
		return VerdictHighRisk
	}
	if tor, ok := lookup(evidence, SourceTorProject); ok && hasRole(tor.TorRoles, "exit") {
		return VerdictHighRisk
	}
	if out.Flags&FlagTor != 0 {
		return VerdictHighRisk
	}
	if abuse, ok := lookup(evidence, SourceAbuseIPDB); ok && abuse.AbuseConfidence != nil {
		if clampInt(*abuse.AbuseConfidence, 0, 100) >= abuseHighRiskConfidence &&
			abuse.TotalReports != nil && *abuse.TotalReports > 0 {
			return VerdictHighRisk
		}
	}
	if out.PurityScore != nil && *out.PurityScore < 60 {
		out.Reasons = append(out.Reasons, ReasonLowPurity)
		return VerdictHighRisk
	}

	// 2. conflicting.
	if out.IPType == IPTypeConflicting {
		return VerdictConflicting
	}

	// 3. review.
	if out.Flags&(FlagProxy|FlagVPN|FlagScraper|FlagAnonymous) != 0 {
		return VerdictReview
	}
	if out.Flags&FlagAttackHistory != 0 {
		return VerdictReview
	}
	if out.Flags&FlagDNSBL != 0 {
		return VerdictReview
	}
	// A Tor relay/guard that is not an exit is still an anonymity signal, so it
	// is reviewed instead of passing as favorable. WP10 §1.6 only names the exit
	// role explicitly; this is the documented reading of rule 3.
	if tor, ok := lookup(evidence, SourceTorProject); ok && len(tor.TorRoles) > 0 {
		return VerdictReview
	}

	// 4. pending.
	if out.PurityScore == nil {
		return VerdictPending
	}

	// 5. incomplete.
	if out.Confidence == ConfidenceLow && out.Coverage < 0.3 {
		out.Reasons = append(out.Reasons, ReasonIncompleteCoverage)
		return VerdictIncomplete
	}

	// 6. caution.
	if *out.PurityScore < 80 {
		return VerdictCaution
	}

	// 7. favorable.
	return VerdictFavorable
}

// noEvidenceState distinguishes a genuinely unfinished assessment from one whose
// enabled sources all failed (§1.1).
func noEvidenceState(in Input, evidence []quality.Evidence) (state, reason string) {
	enabledScoring := make([]string, 0, len(scoringWeights))
	for _, source := range ScoringSources() {
		if sourceEnabled(in.Enabled, source) {
			enabledScoring = append(enabledScoring, source)
		}
	}
	if len(enabledScoring) == 0 {
		return StateUnsupported, ReasonNoScoringSource
	}
	// Stale-but-present evidence is a refresh problem, not an unsupported source.
	for _, ev := range evidence {
		if _, ok := scoringWeights[ev.Provider]; ok {
			return StatePending, ReasonNoValidEvidence
		}
	}
	failures := 0
	for _, source := range enabledScoring {
		if _, ok := in.Failed[source]; ok {
			failures++
		}
	}
	if failures == len(enabledScoring) {
		return StateUnsupported, ReasonAllSourcesFailed
	}
	return StatePending, ReasonNoValidEvidence
}

// ------------------------------------------------------------------
// Small helpers
// ------------------------------------------------------------------

func isIPv4(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return true // an unset address cannot make the value inapplicable
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return false
	}
	return addr.Is4() || addr.Is4In6()
}

func boolValue(v *bool) bool { return v != nil && *v }

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func hasRole(roles []string, want string) bool {
	for _, role := range roles {
		if strings.EqualFold(strings.TrimSpace(role), want) {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// dedupeReasons keeps the first occurrence of each reason code.
func dedupeReasons(reasons []string) []string {
	if len(reasons) == 0 {
		return []string{}
	}
	seen := make(map[string]bool, len(reasons))
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if reason == "" || seen[reason] {
			continue
		}
		seen[reason] = true
		out = append(out, reason)
	}
	return out
}

// Explain renders the score as the one-line human explanation of the API.
func (a Assessment) Explain() string {
	parts := make([]string, 0, 4)
	if a.PurityScore != nil {
		parts = append(parts, fmt.Sprintf("score=%d band=%s", *a.PurityScore, a.PurityBand))
	} else {
		parts = append(parts, "score=unknown")
	}
	parts = append(parts, fmt.Sprintf("state=%s verdict=%s confidence=%s coverage=%.2f", a.State, a.Verdict, a.Confidence, a.Coverage))
	if len(a.Reasons) > 0 {
		parts = append(parts, "reasons="+strings.Join(a.Reasons, ","))
	}
	return strings.Join(parts, " ")
}
