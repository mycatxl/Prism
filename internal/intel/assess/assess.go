// Package assess implements the explainable purity scoring of
// (profile "prism-purity-v5-ippure"). Every function here is pure: the same input
// always produces the same output, so the whole algorithm is unit-testable
// without a database, a network or a clock.
package assess

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"prism/internal/quality"
)

// Profile is the assessment profile written to ip_assessment.profile.
// recomputes every row whose profile differs from this value.
const Profile = "prism-purity-v5-ippure"

// Scoring source ids. The order is the stable output order of
// Assessment.Components.
//
// SourceIPQS and SourceAbuseIPDB are declared but not scored: both vendors
// reject a keyless request outright (PROVIDER_AUTH), so neither can ever answer
// for an inventory of this size and neither carries a weight. Their evidence
// rows are still read by detectFlags / voteOf / identityPriority and the
// ip_assessment.flags bit contract still names them, so a historical row - or a
// deployment that deliberately enables one with a key - keeps being reported.
// It simply never moves the purity score or the coverage denominator.
const (
	SourceProxycheck = "proxycheck"
	// SourceProxycheckNode is the anonymous via-node variant of the same vendor.
	// It is never a scoring source of its own: ProviderAliases folds it onto
	// SourceProxycheck so one address still yields one component.
	SourceProxycheckNode = "proxycheck_node"
	SourceIPPure         = "ippure"
	SourceIPQS           = "ipqs"
	SourceAbuseIPDB      = "abuseipdb"
	SourceIPAPIIs        = "ipapi_is"
	// SourceIPAPIIsNode is the anonymous via-node variant of ipapi.is. Like
	// SourceProxycheckNode it is never a scoring source of its own: the alias
	// table folds it onto SourceIPAPIIs.
	SourceIPAPIIsNode = "ipapi_is_node"
	SourceIPAPI       = "ip_api"
	SourceDNSBL       = "dnsbl"
)

// Non-scoring sources referenced by the algorithm.
const (
	SourceTorProject = "torproject"
	SourceGeoCountry = "geo_country"
)

// ScoringSources names the sole primary score source. Other sources only
// supply independent risk and network evidence.
func ScoringSources() []string { return []string{SourceIPPure} }

// offlineSources carry the registry/registration data used by the offline
// fallbacks of and
var offlineSources = map[string]bool{
	SourceGeoCountry:   true,
	"dbip_lite":        true,
	"maxmind_geolite2": true,
	"ipinfo_lite":      true,
	SourceTorProject:   true,
}

// Flags is the 16-bit summary of an assessment that the routing admission path
// reads from the in-memory projection. The bit values are part of the
// ip_assessment.flags contract and must not be renumbered.
type Flags uint16

// Assessment flag bits.
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

// abuseDeductionConfidence is the AbuseConfidence threshold of the
// "abuse" deduction.
const abuseDeductionConfidence = 25

// abuseHighRiskConfidence is the AbuseConfidence threshold of the
// high_risk verdict.
const abuseHighRiskConfidence = 75

// Verdict values of
const (
	VerdictHighRisk    = "high_risk"
	VerdictConflicting = "conflicting"
	VerdictReview      = "review"
	VerdictPending     = "pending"
	VerdictIncomplete  = "incomplete"
	VerdictCaution     = "caution"
	VerdictFavorable   = "favorable"
)

// State values of
const (
	StateValid       = "valid"
	StatePending     = "pending"
	StateUnsupported = "unsupported"
)

// Confidence values of
const (
	ConfidenceNone   = "none"
	ConfidenceLow    = "low"
	ConfidenceMedium = "medium"
	ConfidenceHigh   = "high"
)

// IP types of
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

// dnsblListedReason renders the per-zone listing reason of
func dnsblListedReason(zone string) string {
	return "DNSBL_LISTED:" + zone
}

// Component is one scored source item of 's components_json.
type Component struct {
	Source     string    `json:"source"`
	Raw        int       `json:"raw"`
	Clean      int       `json:"clean"`
	Weight     int       `json:"weight"`
	ObservedAt time.Time `json:"observed_at"`
	ValidUntil time.Time `json:"valid_until"`
}

// Penalty is one applied deduction. Points is always negative.
type Penalty struct {
	Flag    string   `json:"flag"`
	Points  int      `json:"points"`
	Sources []string `json:"sources"`
}

// Assessment is the complete, explainable result of one scoring run.
type Assessment struct {
	Facts         map[string]quality.Fact `json:"facts"`
	IP            netip.Addr              `json:"-"`
	IPText        string                  `json:"ip"`
	Profile       string                  `json:"profile"`
	State         string                  `json:"state"`
	Verdict       string                  `json:"verdict"`
	PurityScore   *int                    `json:"purity_score"`
	PurityBand    string                  `json:"purity_band"`
	Confidence    string                  `json:"confidence"`
	Coverage      float64                 `json:"coverage"`
	Agreement     float64                 `json:"agreement"`
	IPType        string                  `json:"ip_type"`
	Native        *bool                   `json:"native"`
	Flags         Flags                   `json:"flags"`
	Reasons       []string                `json:"reasons"`
	Components    []Component             `json:"components"`
	Penalties     []Penalty               `json:"penalties"`
	PenaltyPoints int                     `json:"penalty_points"`
	SourceCount   int                     `json:"source_count"`
	ASN           int                     `json:"asn,omitempty"`
	ASOrg         string                  `json:"as_org,omitempty"`
	Country       string                  `json:"country,omitempty"`
	City          string                  `json:"city,omitempty"`
	ComputedAt    time.Time               `json:"computed_at"`
	ValidUntil    time.Time               `json:"valid_until"`
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
	// moves a score-less assessment from pending to unsupported.
	Failed map[string]string
	// Now is the evaluation instant (UTC).
	Now time.Time
	// Bands are the score thresholds behind the band names. The zero value
	// means the shipped thresholds, which is what a caller with no settings
	// surface to read from should pass.
	Bands quality.Bands
}

// bandSet resolves the thresholds this run scores against.
func (in Input) bandSet() quality.Bands {
	return in.Bands.Normalized()
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
	out.Facts = fieldFacts(in, evidence)
	flags, reasons := detectFlags(evidence)
	out.Flags = flags
	out.Reasons = append(out.Reasons, reasons...)
	attachIdentity(&out, evidence)
	out.IPType = networkType(evidence, &out.Reasons)
	out.Native = nativeFlag(evidence, &out.Reasons)
	primary, fact := primaryEvidence(in)
	out.Facts["purity"] = fact
	if primary != nil {
		score := 100 - *primary.FraudScore
		out.PurityScore = &score
		out.PurityBand = in.bandSet().Name(&score)
		out.State = StateValid
		out.ValidUntil = primary.ValidUntil
		out.Components = []Component{{Source: SourceIPPure, Raw: *primary.FraudScore, Clean: score, Weight: 1, ObservedAt: primary.ObservedAt, ValidUntil: primary.ValidUntil}}
		out.SourceCount, out.Coverage, out.Agreement, out.Confidence = 1, 1, 1, ConfidenceLow
	} else {
		out.Reasons = append(out.Reasons, fact.Cause)
		if fact.ErrorCode != "" {
			out.Reasons = append(out.Reasons, "IPPURE_ERROR:"+fact.ErrorCode)
		}
		if fact.Cause != "IPPURE_NOT_RUN" && fact.Cause != "EVIDENCE_EXPIRED" {
			out.State = StateUnsupported
		}
	}

	out.Verdict = verdict(&out, evidence)
	out.Reasons = dedupeReasons(out.Reasons)
	return out
}

// Assess is the signature. Evidence must already be status=ok rows; expiry is
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
	rows := make([]quality.Evidence, 0, len(in.Evidence))
	for _, ev := range in.Evidence {
		if _, failed := in.Failed[ev.Provider]; !failed {
			rows = append(rows, ev)
		}
	}
	return CurrentEvidence(in.IP, rows, in.Now)
}

// CurrentEvidence is shared by assessment and presentation. Only a current,
// address-matching observation participates, once per vendor (newest alias).
func CurrentEvidence(ip netip.Addr, evidence []quality.Evidence, at time.Time) []quality.Evidence {
	if !ip.IsValid() {
		return nil
	}
	now := at.UTC()
	want := ip.Unmap().String()
	current := make([]quality.Evidence, 0, len(evidence))
	for _, ev := range independentEvidence(evidence) {
		ev.NormalizeLegacySignals()
		if ev.Provider == "" {
			continue
		}
		addr, err := netip.ParseAddr(ev.IP)
		if err != nil || addr.Unmap().String() != want {
			continue
		}
		if ev.ObservedAt.IsZero() || ev.ObservedAt.After(now) || ev.ValidUntil.IsZero() || !now.Before(ev.ValidUntil.UTC()) {
			continue
		}
		current = append(current, ev)
	}
	return current
}

// independentEvidence selects one observation per vendor, including host/node
// aliases. Selection precedes interpretation: a newer unknown replaces an old
// vote too. Callers that need current observations must filter time/IP first.
func independentEvidence(evidence []quality.Evidence) []quality.Evidence {
	byProvider := make(map[string]quality.Evidence, len(evidence))
	for _, ev := range evidence {
		if ev.Provider == "" {
			continue
		}
		provider := scoringSource(ev.Provider)
		current, ok := byProvider[provider]
		if !ok || ev.ObservedAt.After(current.ObservedAt) || (ev.ObservedAt.Equal(current.ObservedAt) && ev.Provider < current.Provider) {
			byProvider[provider] = ev
		}
	}
	out := make([]quality.Evidence, 0, len(byProvider))
	for _, ev := range byProvider {
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// ProviderAliases maps a data source id onto the scoring source it feeds.
//
// proxycheck_node and ipapi_is_node are the anonymous via-node variants of their
// vendors: they report the same vendor's verdict for the same address, obtained
// through the node so the vendor's anonymous quota is spent per node instead of
// per Prism host. Folding each onto its host-side source keeps exactly one
// component per source - lookup returns the first match - no matter which variant
// answered.
var ProviderAliases = map[string]string{
	"proxycheck_node": SourceProxycheck,
	"ipapi_is_node":   SourceIPAPIIs,
}

// scoringSource folds a data source id onto the scoring source it feeds: an
// aliased via-node variant returns its host-side source, everything else returns
// itself.
func scoringSource(provider string) string {
	if mapped, ok := ProviderAliases[provider]; ok {
		return mapped
	}
	return provider
}

// lookup returns one provider's selected evidence. CurrentEvidence has already
// selected the newest alias; the exact match is only a deterministic fallback.
func lookup(evidence []quality.Evidence, provider string) (quality.Evidence, bool) {
	for _, ev := range evidence {
		if ev.Provider == provider {
			return ev, true
		}
	}
	for _, ev := range evidence {
		if ProviderAliases[ev.Provider] == provider {
			return ev, true
		}
	}
	return quality.Evidence{}, false
}

// ------------------------------------------------------------------
// Components
// ------------------------------------------------------------------

// componentScore accepts only the provider's actual fraudScore, never RiskScore.
func componentScore(source string, ev quality.Evidence) (raw, clean int, ok bool) {
	if source != SourceIPPure || ev.FraudScore == nil || *ev.FraudScore < 0 || *ev.FraudScore > 100 || !isIPv4(ev.IP) {
		return 0, 0, false
	}
	return *ev.FraudScore, 100 - *ev.FraudScore, true
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
	recentAbuse := false
	for _, ev := range evidence {
		recentAbuse = recentAbuse || boolValue(ev.RecentAbuse)
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

	// Tor roles come from the torproject registry.
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
	if recentAbuse {
		reasons = append(reasons, "VENDOR_RECENT_ABUSE")
	}

	// Abuse confidence (and use two thresholds).
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

// voteOf maps one source's evidence to a vote, or "" when it does not vote.
func voteOf(ev quality.Evidence) string {
	switch ev.Provider {
	case SourceProxycheck, SourceProxycheckNode:
		return normalizeRawType(ev.IPType)
	case SourceIPQS:
		if mapped := ipqsConnectionType(ev.SourceType); mapped != "" {
			return mapped
		}
		return normalizeRawType(ev.IPType)
	case SourceIPAPIIs, SourceIPAPIIsNode:
		// Historical decoders promoted company.type=isp to residential. A
		// company category cannot prove a residential access network.
		if strings.EqualFold(strings.TrimSpace(ev.SourceType), "isp") && ev.IPType == IPTypeResidential {
			return ""
		}
		if kind := normalizeRawType(ev.IPType); kind != "" {
			return kind
		}
		switch strings.ToLower(strings.TrimSpace(ev.SourceType)) {
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

// ipqsConnectionType maps IPQualityScore's connection_type.
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
	case "conflicting":
		return IPTypeConflicting
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
// Native
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
	fields := out.Facts
	if f := fields["asn"]; f.State == "known" {
		out.ASN = fieldValue(f).(int)
	}
	if f := fields["as_org"]; f.State == "known" {
		out.ASOrg = fieldValue(f).(string)
	}
	if f := fields["country"]; f.State == "known" {
		out.Country = fieldValue(f).(string)
	}
	if f := fields["city"]; f.State == "known" {
		out.City = fieldValue(f).(string)
	}
}

// ------------------------------------------------------------------
// Verdict
// ------------------------------------------------------------------

// verdict applies the decision list in order; the first match wins.
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
	// is reviewed instead of passing as favorable. only names the exit
	// role explicitly; this is the documented reading of rule 3.
	if tor, ok := lookup(evidence, SourceTorProject); ok && len(tor.TorRoles) > 0 {
		return VerdictReview
	}

	for _, ev := range evidence {
		if boolValue(ev.RecentAbuse) {
			return VerdictReview
		}
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

	// A score describes the supplied risk evidence. It cannot fill missing
	// network flags with false to manufacture a favorable overall verdict.
	seen := [4]bool{}
	for _, ev := range evidence {
		for i, value := range []*bool{ev.Signals.Proxy, ev.Signals.VPN, ev.Signals.Tor, ev.Signals.Compromised} {
			seen[i] = seen[i] || value != nil
		}
	}
	for _, known := range seen {
		if !known {
			out.Reasons = append(out.Reasons, "NETWORK_EVIDENCE_INCOMPLETE")
			return VerdictIncomplete
		}
	}
	return VerdictFavorable
}

// ------------------------------------------------------------------
// Small helpers
// ------------------------------------------------------------------

func isIPv4(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false // missing target identity is never scoreable
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
