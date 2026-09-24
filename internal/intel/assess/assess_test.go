package assess

import (
	"encoding/json"
	"net/netip"
	"testing"
	"time"

	"prism/internal/quality"
)

var (
	testNow  = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	testIPv4 = "203.0.113.9"
	testIPv6 = "2606:4700:4700::1111"
)

func bp(v bool) *bool { return &v }
func ip(v int) *int   { return &v }

// evb builds one quality.Evidence row with sane defaults so a case only states
// what it is about.
type evb struct{ e quality.Evidence }

func ev(provider string) evb {
	return evb{quality.Evidence{
		Provider:   provider,
		Profile:    provider + "-v1",
		IP:         testIPv4,
		ObservedAt: testNow,
		ValidUntil: testNow.Add(24 * time.Hour),
	}}
}

func (b evb) on(addr string) evb         { b.e.IP = addr; return b }
func (b evb) risk(v int) evb             { b.e.RiskScore = ip(v); return b }
func (b evb) fraud(v int) evb            { b.e.FraudScore = ip(v); return b }
func (b evb) typeOf(v string) evb        { b.e.IPType = v; return b }
func (b evb) sourceType(v string) evb    { b.e.SourceType = v; return b }
func (b evb) native(v bool) evb          { b.e.Native = bp(v); return b }
func (b evb) residential(v bool) evb     { b.e.IsResidential = bp(v); return b }
func (b evb) roles(v ...string) evb      { b.e.TorRoles = v; return b }
func (b evb) asn(v int) evb              { b.e.ASNNumber = v; return b }
func (b evb) org(v string) evb           { b.e.Organization = v; return b }
func (b evb) country(v string) evb       { b.e.CountryCode = v; return b }
func (b evb) registered(v string) evb    { b.e.RegisteredCC = v; return b }
func (b evb) observedAt(t time.Time) evb { b.e.ObservedAt = t; return b }
func (b evb) until(t time.Time) evb      { b.e.ValidUntil = t; return b }
func (b evb) expired() evb               { b.e.ValidUntil = testNow.Add(-time.Minute); return b }
func (b evb) dnsbl(checked, listed []string) evb {
	b.e.DNSBLChecked = checked
	b.e.DNSBLListed = listed
	return b
}

func (b evb) abuse(conf, reports int) evb {
	b.e.AbuseConfidence = ip(conf)
	b.e.TotalReports = ip(reports)
	return b
}

func (b evb) attacks(v ...string) evb {
	b.e.AttackHistory = make(map[string]int, len(v))
	for _, key := range v {
		b.e.AttackHistory[key] = 1
	}
	return b
}

// flag sets one signal pointer.
func (b evb) flag(name string, value bool) evb {
	v := bp(value)
	switch name {
	case "proxy":
		b.e.Signals.Proxy = v
	case "vpn":
		b.e.Signals.VPN = v
	case "tor":
		b.e.Signals.Tor = v
	case "hosting":
		b.e.Signals.Hosting = v
	case "compromised":
		b.e.Signals.Compromised = v
	case "scraper":
		b.e.Signals.Scraper = v
	case "anonymous":
		b.e.Signals.Anonymous = v
	default:
		panic("unknown signal " + name)
	}
	return b
}

func (b evb) build() quality.Evidence { return b.e }

func evidence(items ...evb) []quality.Evidence {
	out := make([]quality.Evidence, 0, len(items))
	for _, item := range items {
		out = append(out, item.build())
	}
	return out
}

// ------------------------------------------------------------------
// Table-driven coverage of every branch of §1.1 – §1.6.
// ------------------------------------------------------------------

func TestAssess_Table(t *testing.T) {
	threeZones := []string{"zen.spamhaus.org", "bl.spamcop.net", "psbl.surriel.com"}

	cases := []struct {
		name       string
		ip         string
		evidence   []quality.Evidence
		enabled    map[string]bool
		failed     map[string]string
		score      *int
		band       string
		state      string
		verdict    string
		confidence string
		ipType     string
		native     *bool
		components []string
		penalty    int
		hasReasons []string
		noReasons  []string
	}{
		{
			name: "clean_all_seven_sources",
			evidence: evidence(
				ev("proxycheck").risk(5),
				ev("ippure").fraud(5).residential(true),
				ev("ipqs").fraud(5).sourceType("Residential"),
				ev("abuseipdb").abuse(0, 0),
				ev("ipapi_is").typeOf("residential"),
				ev("ip_api").flag("proxy", false),
				ev("dnsbl").dnsbl(threeZones, nil),
			),
			score:      ip(97),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictFavorable,
			confidence: ConfidenceHigh,
			ipType:     IPTypeResidential,
			components: []string{"proxycheck", "ippure", "ipqs", "abuseipdb", "ipapi_is", "ip_api", "dnsbl"},
			penalty:    0,
		},
		{
			name:       "single_source_low_coverage_is_incomplete",
			evidence:   evidence(ev("proxycheck").risk(10)),
			score:      ip(90),
			band:       "clean",
			state:      StateValid,
			verdict:    VerdictIncomplete,
			confidence: ConfidenceLow,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck"},
			penalty:    0,
			hasReasons: []string{ReasonIncompleteCoverage, ReasonIPTypeUnknown},
		},
		{
			name:       "single_source_full_coverage_is_favorable",
			evidence:   evidence(ev("proxycheck").risk(10)),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      ip(90),
			band:       "clean",
			state:      StateValid,
			verdict:    VerdictFavorable,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck"},
		},
		{
			name:       "caution_band",
			evidence:   evidence(ev("proxycheck").risk(25)),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      ip(75),
			band:       "mixed",
			state:      StateValid,
			verdict:    VerdictCaution,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck"},
		},
		{
			name:       "high_risk_by_score",
			evidence:   evidence(ev("proxycheck").risk(45)),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      ip(55),
			band:       "poor",
			state:      StateValid,
			verdict:    VerdictHighRisk,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck"},
			hasReasons: []string{ReasonLowPurity},
		},
		{
			name:       "high_risk_by_tor_signal",
			evidence:   evidence(ev("proxycheck").risk(0).flag("tor", true)),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      ip(70),
			band:       "mixed",
			state:      StateValid,
			verdict:    VerdictHighRisk,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck"},
			penalty:    -30,
			hasReasons: []string{ReasonTorExit},
		},
		{
			name:       "high_risk_by_tor_exit_role",
			evidence:   evidence(ev("proxycheck").risk(0), ev("torproject").roles("exit")),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      ip(70),
			band:       "mixed",
			state:      StateValid,
			verdict:    VerdictHighRisk,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck"},
			penalty:    -30,
			hasReasons: []string{ReasonTorExit},
		},
		{
			name:       "tor_relay_is_review_not_high_risk",
			evidence:   evidence(ev("proxycheck").risk(0), ev("torproject").roles("guard")),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      ip(100),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictReview,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck"},
			penalty:    0,
			hasReasons: []string{ReasonTorRelay},
			noReasons:  []string{ReasonTorExit},
		},
		{
			name:       "compromised_deducts_40",
			evidence:   evidence(ev("proxycheck").risk(0).flag("compromised", true)),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      ip(60),
			band:       "mixed",
			state:      StateValid,
			verdict:    VerdictHighRisk,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck"},
			penalty:    -40,
			hasReasons: []string{ReasonCompromised},
		},
		{
			name:       "vpn_deducts_10_and_reviews",
			evidence:   evidence(ev("ipqs").fraud(0).flag("vpn", true)),
			enabled:    map[string]bool{SourceIPQS: true},
			score:      ip(90),
			band:       "clean",
			state:      StateValid,
			verdict:    VerdictReview,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"ipqs"},
			penalty:    -10,
			hasReasons: []string{ReasonVPN},
		},
		{
			name:       "proxy_flag_with_clean_partner_scores_80",
			evidence:   evidence(ev("proxycheck").risk(0), ev("ip_api").flag("proxy", true)),
			enabled:    map[string]bool{SourceProxycheck: true, SourceIPAPI: true},
			score:      ip(80),
			band:       "fair",
			state:      StateValid,
			verdict:    VerdictReview,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck", "ip_api"},
			penalty:    -10,
			hasReasons: []string{ReasonProxy},
		},
		{
			name:       "scraper_deducts_10_and_reviews",
			evidence:   evidence(ev("ipqs").fraud(0).flag("scraper", true)),
			enabled:    map[string]bool{SourceIPQS: true},
			score:      ip(90),
			band:       "clean",
			state:      StateValid,
			verdict:    VerdictReview,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"ipqs"},
			penalty:    -10,
			hasReasons: []string{ReasonScraper},
		},
		{
			name:       "anonymous_signal_reviews_without_deduction",
			evidence:   evidence(ev("proxycheck").risk(0).flag("anonymous", true)),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      ip(100),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictReview,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck"},
			penalty:    0,
			hasReasons: []string{ReasonAnonymous},
		},
		{
			name:       "attack_history_reviews",
			evidence:   evidence(ev("proxycheck").risk(0).attacks("proxy", "bruteforce")),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      ip(100),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictReview,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck"},
			hasReasons: []string{ReasonAttackHistory},
		},
		{
			name:       "dnsbl_two_listed_zones",
			evidence:   evidence(ev("proxycheck").risk(0), ev("dnsbl").dnsbl(threeZones, []string{"zen.spamhaus.org", "bl.spamcop.net"})),
			enabled:    map[string]bool{SourceProxycheck: true, SourceDNSBL: true},
			score:      ip(83),
			band:       "fair",
			state:      StateValid,
			verdict:    VerdictReview,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck", "dnsbl"},
			penalty:    0,
			hasReasons: []string{"DNSBL_LISTED:zen.spamhaus.org", "DNSBL_LISTED:bl.spamcop.net"},
		},
		{
			name:       "dnsbl_listed_but_not_checked_is_clean",
			evidence:   evidence(ev("dnsbl").dnsbl([]string{"zen.spamhaus.org", "bl.spamcop.net"}, []string{"psbl.surriel.com"})),
			enabled:    map[string]bool{SourceDNSBL: true},
			score:      ip(100),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictFavorable,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"dnsbl"},
			noReasons:  []string{"DNSBL_LISTED:psbl.surriel.com"},
		},
		{
			name:     "dnsbl_without_checked_zone_is_not_scored",
			evidence: evidence(ev("dnsbl").dnsbl(nil, []string{"zen.spamhaus.org"})),
			enabled:  map[string]bool{SourceDNSBL: true},
			score:    nil,
			band:     "unknown",
			state:    StatePending,
			verdict:  VerdictPending,
			ipType:   IPTypeUnknown,
			hasReasons: []string{
				ReasonNoValidEvidence,
			},
		},
		{
			name:       "abuse_25_deducts_but_is_not_recent_abuse",
			evidence:   evidence(ev("abuseipdb").abuse(25, 1)),
			enabled:    map[string]bool{SourceAbuseIPDB: true},
			score:      ip(65),
			band:       "mixed",
			state:      StateValid,
			verdict:    VerdictCaution,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"abuseipdb"},
			penalty:    -10,
			hasReasons: []string{ReasonAbuseConfidence},
			noReasons:  []string{ReasonRecentAbuse},
		},
		{
			name:       "abuse_80_with_reports_is_high_risk",
			evidence:   evidence(ev("abuseipdb").abuse(80, 5)),
			enabled:    map[string]bool{SourceAbuseIPDB: true},
			score:      ip(10),
			band:       "poor",
			state:      StateValid,
			verdict:    VerdictHighRisk,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"abuseipdb"},
			penalty:    -10,
			hasReasons: []string{ReasonRecentAbuse},
		},
		{
			name:       "abuse_76_without_reports_is_not_high_risk",
			evidence:   evidence(ev("abuseipdb").abuse(76, 0), ev("proxycheck").risk(0), ev("ipapi_is").typeOf("business")),
			enabled:    map[string]bool{SourceAbuseIPDB: true, SourceProxycheck: true, SourceIPAPIIs: true},
			score:      ip(65),
			band:       "mixed",
			state:      StateValid,
			verdict:    VerdictCaution,
			confidence: ConfidenceMedium,
			ipType:     IPTypeBusiness,
			components: []string{"proxycheck", "abuseipdb", "ipapi_is"},
			penalty:    -10,
			noReasons:  []string{ReasonRecentAbuse},
		},
		{
			name:       "ipapi_is_abuser_is_compromised",
			evidence:   evidence(ev("ipapi_is").flag("compromised", true), ev("proxycheck").risk(0)),
			enabled:    map[string]bool{SourceProxycheck: true, SourceIPAPIIs: true},
			score:      ip(45),
			band:       "poor",
			state:      StateValid,
			verdict:    VerdictHighRisk,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck", "ipapi_is"},
			penalty:    -40,
			hasReasons: []string{ReasonCompromised},
		},
		{
			name:       "ipapi_is_proxy_flag",
			evidence:   evidence(ev("ipapi_is").flag("proxy", true), ev("proxycheck").risk(0)),
			enabled:    map[string]bool{SourceProxycheck: true, SourceIPAPIIs: true},
			score:      ip(80),
			band:       "fair",
			state:      StateValid,
			verdict:    VerdictReview,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck", "ipapi_is"},
			penalty:    -10,
			hasReasons: []string{ReasonProxy},
		},
		{
			name: "ipv6_ippure_component_excluded",
			ip:   testIPv6,
			evidence: evidence(
				ev("ippure").on(testIPv6).fraud(0).residential(true),
				ev("proxycheck").on(testIPv6).risk(10),
			),
			enabled:    map[string]bool{SourceProxycheck: true, SourceIPPure: true},
			score:      ip(90),
			band:       "clean",
			state:      StateValid,
			verdict:    VerdictFavorable,
			confidence: ConfidenceMedium,
			ipType:     IPTypeResidential,
			components: []string{"proxycheck"},
		},
		{
			name: "conflicting_residential_vs_datacenter",
			evidence: evidence(
				ev("proxycheck").risk(0).typeOf("mobile"),
				ev("ipqs").fraud(0).sourceType("Residential"),
				ev("ipapi_is").typeOf("datacenter"),
				ev("ip_api").on(testIPv4).typeOf("datacenter").flag("proxy", false),
			),
			enabled:    map[string]bool{SourceProxycheck: true, SourceIPQS: true, SourceIPAPIIs: true, SourceIPAPI: true},
			score:      ip(100),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictConflicting,
			confidence: ConfidenceHigh,
			ipType:     IPTypeConflicting,
			components: []string{"proxycheck", "ipqs", "ipapi_is", "ip_api"},
			hasReasons: []string{ReasonIPTypeConflicting},
		},
		{
			name: "tie_prefers_non_datacenter_without_residential_vote",
			evidence: evidence(
				ev("proxycheck").risk(0).typeOf("datacenter"),
				ev("ipqs").fraud(0).typeOf("business"),
			),
			score:      ip(100),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictFavorable,
			confidence: ConfidenceMedium,
			ipType:     IPTypeBusiness,
			components: []string{"proxycheck", "ipqs"},
		},
		{
			name: "tie_between_business_and_wireless_is_deterministic",
			evidence: evidence(
				ev("proxycheck").risk(0).typeOf("business"),
				ev("ipqs").fraud(0).typeOf("wireless"),
			),
			enabled:    map[string]bool{SourceProxycheck: true, SourceIPQS: true},
			score:      ip(100),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictFavorable,
			confidence: ConfidenceHigh,
			ipType:     IPTypeBusiness,
			components: []string{"proxycheck", "ipqs"},
		},
		{
			name:       "non_residential_only_vote",
			evidence:   evidence(ev("ippure").fraud(0).residential(false)),
			enabled:    map[string]bool{SourceIPPure: true},
			score:      ip(100),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictFavorable,
			confidence: ConfidenceMedium,
			ipType:     IPTypeNonResidential,
			components: []string{"ippure"},
		},
		{
			name: "non_residential_is_dropped_when_others_vote",
			evidence: evidence(
				ev("ippure").fraud(0).residential(false),
				ev("proxycheck").risk(0).typeOf("residential"),
			),
			enabled:    map[string]bool{SourceIPPure: true, SourceProxycheck: true},
			score:      ip(100),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictFavorable,
			confidence: ConfidenceHigh,
			ipType:     IPTypeResidential,
			components: []string{"proxycheck", "ippure"},
		},
		{
			name:       "wireless_is_preserved",
			evidence:   evidence(ev("proxycheck").risk(0).typeOf("wireless")),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      ip(100),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictFavorable,
			confidence: ConfidenceMedium,
			ipType:     IPTypeWireless,
			components: []string{"proxycheck"},
		},
		{
			name:       "offline_hosting_asn_fallback",
			evidence:   evidence(ev("dbip_lite").asn(16509).org("Amazon")),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      nil,
			band:       "unknown",
			state:      StatePending,
			verdict:    VerdictPending,
			ipType:     IPTypeDatacenter,
			hasReasons: []string{ReasonASNHosting},
		},
		{
			name:       "offline_non_hosting_asn_has_no_type",
			evidence:   evidence(ev("dbip_lite").asn(64512).org("Local ISP")),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      nil,
			band:       "unknown",
			state:      StatePending,
			verdict:    VerdictPending,
			ipType:     IPTypeUnknown,
			hasReasons: []string{ReasonIPTypeUnknown},
			noReasons:  []string{ReasonASNHosting},
		},
		{
			name:       "native_from_ippure",
			evidence:   evidence(ev("ippure").fraud(0).native(true), ev("proxycheck").risk(0)),
			enabled:    map[string]bool{SourceIPPure: true, SourceProxycheck: true},
			score:      ip(100),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictFavorable,
			confidence: ConfidenceHigh,
			ipType:     IPTypeUnknown,
			native:     bp(true),
			components: []string{"proxycheck", "ippure"},
			noReasons:  []string{ReasonNativeHeuristic},
		},
		{
			name:       "native_heuristic_match",
			evidence:   evidence(ev("maxmind_geolite2").country("US").registered("us")),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      nil,
			band:       "unknown",
			state:      StatePending,
			verdict:    VerdictPending,
			ipType:     IPTypeUnknown,
			native:     bp(true),
			hasReasons: []string{ReasonNativeHeuristic},
		},
		{
			name:       "native_heuristic_mismatch",
			evidence:   evidence(ev("maxmind_geolite2").country("DE").registered("US")),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      nil,
			band:       "unknown",
			state:      StatePending,
			verdict:    VerdictPending,
			ipType:     IPTypeUnknown,
			native:     bp(false),
			hasReasons: []string{ReasonNativeHeuristic},
		},
		{
			name:       "native_unknown",
			evidence:   evidence(ev("proxycheck").risk(0)),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      ip(100),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictFavorable,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			native:     nil,
			components: []string{"proxycheck"},
		},
		{
			name:       "all_enabled_sources_failed_is_unsupported",
			enabled:    map[string]bool{SourceProxycheck: true, SourceIPAPIIs: true},
			failed:     map[string]string{SourceProxycheck: "PROVIDER_UNAVAILABLE", SourceIPAPIIs: "PROVIDER_AUTH"},
			score:      nil,
			band:       "unknown",
			state:      StateUnsupported,
			verdict:    VerdictPending,
			ipType:     IPTypeUnknown,
			hasReasons: []string{ReasonAllSourcesFailed},
		},
		{
			name:       "partial_failure_stays_pending",
			enabled:    map[string]bool{SourceProxycheck: true, SourceIPQS: true},
			failed:     map[string]string{SourceProxycheck: "PROVIDER_UNAVAILABLE"},
			score:      nil,
			band:       "unknown",
			state:      StatePending,
			verdict:    VerdictPending,
			ipType:     IPTypeUnknown,
			hasReasons: []string{ReasonNoValidEvidence},
			noReasons:  []string{ReasonAllSourcesFailed},
		},
		{
			name:       "no_evidence_at_all_is_pending",
			score:      nil,
			band:       "unknown",
			state:      StatePending,
			verdict:    VerdictPending,
			ipType:     IPTypeUnknown,
			hasReasons: []string{ReasonNoValidEvidence},
		},
		{
			name:       "stale_evidence_is_ignored",
			evidence:   evidence(ev("proxycheck").risk(10).expired()),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      nil,
			band:       "unknown",
			state:      StatePending,
			verdict:    VerdictPending,
			ipType:     IPTypeUnknown,
			hasReasons: []string{ReasonNoValidEvidence},
		},
		{
			name:       "confidence_medium_for_two_sources_without_full_coverage",
			evidence:   evidence(ev("proxycheck").risk(0), ev("ipqs").fraud(0)),
			score:      ip(100),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictFavorable,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck", "ipqs"},
		},
		{
			name:       "confidence_high_needs_coverage_and_agreement",
			evidence:   evidence(ev("proxycheck").risk(0), ev("ipqs").fraud(0), ev("abuseipdb").abuse(0, 0), ev("ipapi_is").typeOf("business")),
			enabled:    map[string]bool{SourceProxycheck: true, SourceIPQS: true, SourceAbuseIPDB: true, SourceIPAPIIs: true},
			score:      ip(100),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictFavorable,
			confidence: ConfidenceHigh,
			ipType:     IPTypeBusiness,
			components: []string{"proxycheck", "ipqs", "abuseipdb", "ipapi_is"},
		},
		{
			name:       "disagreement_blocks_high_confidence",
			evidence:   evidence(ev("proxycheck").risk(0), ev("ipapi_is").flag("compromised", true)),
			enabled:    map[string]bool{SourceProxycheck: true, SourceIPAPIIs: true},
			score:      ip(45),
			band:       "poor",
			state:      StateValid,
			verdict:    VerdictHighRisk,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck", "ipapi_is"},
			penalty:    -40,
		},
		{
			name:       "penalty_is_applied_once_per_flag",
			evidence:   evidence(ev("proxycheck").risk(0).flag("proxy", true), ev("ipqs").fraud(0).flag("proxy", true)),
			enabled:    map[string]bool{SourceProxycheck: true, SourceIPQS: true},
			score:      ip(90),
			band:       "clean",
			state:      StateValid,
			verdict:    VerdictReview,
			confidence: ConfidenceHigh,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck", "ipqs"},
			penalty:    -10,
			hasReasons: []string{ReasonProxy},
		},
		{
			name:       "score_is_clamped_to_zero",
			evidence:   evidence(ev("proxycheck").risk(100).flag("compromised", true).flag("tor", true).flag("vpn", true)),
			enabled:    map[string]bool{SourceProxycheck: true},
			score:      ip(0),
			band:       "poor",
			state:      StateValid,
			verdict:    VerdictHighRisk,
			confidence: ConfidenceMedium,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck"},
			penalty:    -80,
		},
		{
			name:       "ip_api_without_proxy_value_does_not_score",
			evidence:   evidence(ev("ip_api").typeOf("mobile")),
			enabled:    map[string]bool{SourceIPAPI: true},
			score:      nil,
			band:       "unknown",
			state:      StatePending,
			verdict:    VerdictPending,
			ipType:     IPTypeMobile,
			hasReasons: []string{ReasonNoValidEvidence},
		},
		{
			name:       "score_100_is_excellent",
			evidence:   evidence(ev("proxycheck").risk(0), ev("ipqs").fraud(0)),
			enabled:    map[string]bool{SourceProxycheck: true, SourceIPQS: true},
			score:      ip(100),
			band:       "excellent",
			state:      StateValid,
			verdict:    VerdictFavorable,
			confidence: ConfidenceHigh,
			ipType:     IPTypeUnknown,
			components: []string{"proxycheck", "ipqs"},
		},
	}

	if len(cases) < 25 {
		t.Fatalf("the scoring table must cover at least 25 cases, got %d", len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr := netip.MustParseAddr(testIPv4)
			if tc.ip != "" {
				addr = netip.MustParseAddr(tc.ip)
			}
			got := AssessInput(Input{
				IP:       addr,
				Evidence: tc.evidence,
				Enabled:  tc.enabled,
				Failed:   tc.failed,
				Now:      testNow,
			})

			if !equalInt(got.PurityScore, tc.score) {
				t.Fatalf("purity score = %s, want %s", formatInt(got.PurityScore), formatInt(tc.score))
			}
			if got.PurityBand != tc.band {
				t.Fatalf("band = %q, want %q", got.PurityBand, tc.band)
			}
			if got.State != tc.state {
				t.Fatalf("state = %q, want %q", got.State, tc.state)
			}
			if got.Verdict != tc.verdict {
				t.Fatalf("verdict = %q, want %q", got.Verdict, tc.verdict)
			}
			if tc.confidence != "" && got.Confidence != tc.confidence {
				t.Fatalf("confidence = %q, want %q (coverage=%.3f agreement=%.3f)", got.Confidence, tc.confidence, got.Coverage, got.Agreement)
			}
			if got.IPType != tc.ipType {
				t.Fatalf("ip type = %q, want %q", got.IPType, tc.ipType)
			}
			if !equalBool(got.Native, tc.native) {
				t.Fatalf("native = %v, want %v", got.Native, tc.native)
			}
			if tc.components != nil {
				sources := componentSources(got)
				if len(sources) != len(tc.components) {
					t.Fatalf("components = %v, want %v", sources, tc.components)
				}
				for i, want := range tc.components {
					if sources[i] != want {
						t.Fatalf("components = %v, want %v", sources, tc.components)
					}
				}
			}
			if got.PenaltyPoints != tc.penalty {
				t.Fatalf("penalty points = %d, want %d (penalties=%v)", got.PenaltyPoints, tc.penalty, got.Penalties)
			}
			for _, reason := range tc.hasReasons {
				if !hasString(got.Reasons, reason) {
					t.Fatalf("reasons %v missing %q", got.Reasons, reason)
				}
			}
			for _, reason := range tc.noReasons {
				if hasString(got.Reasons, reason) {
					t.Fatalf("reasons %v must not contain %q", got.Reasons, reason)
				}
			}
			if got.IPText != addr.Unmap().String() {
				t.Fatalf("ip text = %q, want %q", got.IPText, addr.Unmap().String())
			}
			if got.Profile != Profile {
				t.Fatalf("profile = %q, want %q", got.Profile, Profile)
			}
		})
	}
}

func TestAssess_ValidityWindow(t *testing.T) {
	t.Run("no_evidence_defaults_to_one_hour", func(t *testing.T) {
		got := Assess(netip.MustParseAddr(testIPv4), nil, testNow, nil)
		if !got.ValidUntil.Equal(testNow.Add(time.Hour)) {
			t.Fatalf("valid_until = %s, want %s", got.ValidUntil, testNow.Add(time.Hour))
		}
	})

	t.Run("earliest_valid_until_wins", func(t *testing.T) {
		got := Assess(netip.MustParseAddr(testIPv4), evidence(
			ev("proxycheck").risk(0).until(testNow.Add(2*time.Hour)),
			ev("ipqs").fraud(0).until(testNow.Add(30*time.Minute)),
		), testNow, nil)
		want := testNow.Add(30 * time.Minute)
		if !got.ValidUntil.Equal(want) {
			t.Fatalf("valid_until = %s, want %s", got.ValidUntil, want)
		}
	})

	t.Run("computed_at_is_now", func(t *testing.T) {
		got := Assess(netip.MustParseAddr(testIPv4), nil, testNow, nil)
		if !got.ComputedAt.Equal(testNow) {
			t.Fatalf("computed_at = %s, want %s", got.ComputedAt, testNow)
		}
	})
}

// TestAssess_Deterministic guards §1: identical input must produce identical
// output, including the tie-breaks and the component ordering.
func TestAssess_Deterministic(t *testing.T) {
	input := Input{
		IP: netip.MustParseAddr(testIPv4),
		Evidence: evidence(
			ev("proxycheck").risk(0).typeOf("business"),
			ev("ipqs").fraud(0).typeOf("wireless"),
			ev("dnsbl").dnsbl([]string{"a", "b"}, []string{"b"}),
			ev("abuseipdb").abuse(30, 2),
			ev("ipapi_is").typeOf("business"),
		),
		Enabled: map[string]bool{
			SourceProxycheck: true, SourceIPQS: true, SourceDNSBL: true, SourceAbuseIPDB: true, SourceIPAPIIs: true,
		},
		Now: testNow,
	}
	first, err := json.Marshal(AssessInput(input))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for i := 0; i < 50; i++ {
		next, err := json.Marshal(AssessInput(input))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(next) != string(first) {
			t.Fatalf("run %d differs:\n%s\n%s", i, first, next)
		}
	}
}

func TestAssess_NewestEvidencePerProviderWins(t *testing.T) {
	older := ev("proxycheck").risk(90).observedAt(testNow.Add(-time.Hour))
	newer := ev("proxycheck").risk(10).observedAt(testNow)
	got := Assess(netip.MustParseAddr(testIPv4), evidence(older, newer), testNow, map[string]bool{SourceProxycheck: true})
	if got.PurityScore == nil || *got.PurityScore != 90 {
		t.Fatalf("score = %s, want 90", formatInt(got.PurityScore))
	}
}

func TestAssess_EvidenceOfAnotherAddressIsIgnored(t *testing.T) {
	got := Assess(netip.MustParseAddr(testIPv4), evidence(ev("proxycheck").on("198.51.100.7").risk(0)), testNow, map[string]bool{SourceProxycheck: true})
	if got.PurityScore != nil {
		t.Fatalf("score = %s, want unknown", formatInt(got.PurityScore))
	}
}

func TestAssess_BandThresholds(t *testing.T) {
	cases := []struct {
		risk int
		band string
	}{
		{5, "excellent"},
		{10, "clean"},
		{20, "fair"},
		{40, "mixed"},
		{59, "poor"},
	}
	for _, tc := range cases {
		got := Assess(netip.MustParseAddr(testIPv4),
			evidence(ev("proxycheck").risk(tc.risk)),
			testNow, map[string]bool{SourceProxycheck: true})
		if got.PurityBand != tc.band {
			t.Fatalf("risk %d: band = %q, want %q", tc.risk, got.PurityBand, tc.band)
		}
	}
}

func TestAssess_ConfidenceBoundaries(t *testing.T) {
	cases := []struct {
		name       string
		evidence   []quality.Evidence
		enabled    map[string]bool
		confidence string
	}{
		{
			name:       "no_source",
			confidence: ConfidenceNone,
		},
		{
			// One source of many enabled ones covers 3/14 < 0.4, which is the
			// §1.2 "low" row.
			name:       "one_source_with_low_coverage_is_low",
			evidence:   evidence(ev("proxycheck").risk(0)),
			confidence: ConfidenceLow,
		},
		{
			name:       "one_source_with_full_coverage_is_medium",
			evidence:   evidence(ev("proxycheck").risk(0)),
			enabled:    map[string]bool{SourceProxycheck: true},
			confidence: ConfidenceMedium,
		},
		{
			name:       "two_sources_coverage_0_43_is_medium",
			evidence:   evidence(ev("proxycheck").risk(0), ev("ipqs").fraud(0)),
			confidence: ConfidenceMedium,
		},
		{
			name:       "two_sources_high_coverage_and_agreement_is_high",
			evidence:   evidence(ev("proxycheck").risk(0), ev("ipqs").fraud(0), ev("ipapi_is").typeOf("business")),
			enabled:    map[string]bool{SourceProxycheck: true, SourceIPQS: true, SourceIPAPIIs: true},
			confidence: ConfidenceHigh,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Assess(netip.MustParseAddr(testIPv4), tc.evidence, testNow, tc.enabled)
			if got.Confidence != tc.confidence {
				t.Fatalf("confidence = %q, want %q", got.Confidence, tc.confidence)
			}
		})
	}
}

func TestHostingASNs_CoverRequiredProviders(t *testing.T) {
	required := map[string]bool{
		"Amazon": false, "Google": false, "Microsoft": false, "Oracle": false,
		"Alibaba": false, "Tencent": false, "Huawei": false, "DigitalOcean": false,
		"Akamai": false, "Vultr": false, "Hetzner": false, "OVH": false, "Cloudflare": false,
	}
	for _, name := range HostingASNs() {
		for want := range required {
			if name == want || hasPrefixFold(name, want) {
				required[want] = true
			}
		}
	}
	for name, found := range required {
		if !found {
			t.Fatalf("hosting ASN list is missing provider %q", name)
		}
	}
	if _, ok := HostingASN(16509); !ok {
		t.Fatal("ASN 16509 (AWS) must be a hosting ASN")
	}
	if _, ok := HostingASN(0); ok {
		t.Fatal("ASN 0 must not be a hosting ASN")
	}
}

func TestIsOfflineSource(t *testing.T) {
	for _, id := range []string{"geo_country", "dbip_lite", "maxmind_geolite2", "ipinfo_lite", "torproject", "DBIP_LITE"} {
		if !IsOfflineSource(id) {
			t.Fatalf("%q must be an offline source", id)
		}
	}
	for _, id := range []string{"proxycheck", "ipqs", "", "unknown"} {
		if IsOfflineSource(id) {
			t.Fatalf("%q must not be an offline source", id)
		}
	}
}

func TestFlagsEqualProjectionBits(t *testing.T) {
	// The intel projection stores these bits in a uint16 column; keep the order.
	want := []Flags{
		FlagCompromised, FlagTor, FlagVPN, FlagProxy, FlagScraper,
		FlagAbuse, FlagAnonymous, FlagHosting, FlagDNSBL, FlagAttackHistory,
	}
	for i, flag := range want {
		if uint16(flag) != 1<<uint(i) {
			t.Fatalf("flag %d = %d, want %d", i, flag, 1<<uint(i))
		}
	}
}

// ------------------------------------------------------------------
// helpers
// ------------------------------------------------------------------

func componentSources(a Assessment) []string {
	out := make([]string, 0, len(a.Components))
	for _, item := range a.Components {
		out = append(out, item.Source)
	}
	return out
}

func equalInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalBool(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func formatInt(v *int) string {
	if v == nil {
		return "nil"
	}
	return string(rune('0'+*v/100%10)) + string(rune('0'+*v/10%10)) + string(rune('0'+*v%10))
}

func hasString(values []string, want string) bool { return containsString(values, want) }

func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		a, b := s[i], prefix[i]
		if 'A' <= a && a <= 'Z' {
			a += 'a' - 'A'
		}
		if 'A' <= b && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}
