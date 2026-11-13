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

func (b evb) on(addr string) evb { b.e.IP = addr; return b }

// completeNegatives is explicit observed data for tests of clean scoring;
// missing-signal tests deliberately use ev() without this fixture.
func (b evb) completeNegatives() evb {
	return b.flag("proxy", false).flag("vpn", false).flag("tor", false).flag("compromised", false)
}
func (b evb) risk(v int) evb             { b.e.RiskScore = ip(v); return b }
func (b evb) fraud(v int) evb            { b.e.FraudScore = ip(v); return b }
func (b evb) typeOf(v string) evb        { b.e.IPType = v; return b }
func (b evb) native(v bool) evb          { b.e.Native = bp(v); return b }
func (b evb) residential(v bool) evb     { b.e.IsResidential = bp(v); return b }
func (b evb) roles(v ...string) evb      { b.e.TorRoles = v; return b }
func (b evb) asn(v int) evb              { b.e.ASNNumber = v; return b }
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
// Table-driven coverage of every branch of –
// ------------------------------------------------------------------

// Auxiliary risk remains actionable without ever deducting from purity.
func TestAssess_AuxiliaryRisksDoNotChangePrimary(t *testing.T) {
	cases := []struct {
		name     string
		evidence evb
		flag     Flags
		verdict  string
		reason   string
	}{
		{"compromised", ev(SourceProxycheck).flag("compromised", true), FlagCompromised, VerdictHighRisk, ReasonCompromised},
		{"tor", ev(SourceProxycheck).flag("tor", true), FlagTor, VerdictHighRisk, ReasonTorExit},
		{"vpn", ev(SourceProxycheck).flag("vpn", true), FlagVPN, VerdictReview, ReasonVPN},
		{"proxy", ev(SourceProxycheck).flag("proxy", true), FlagProxy, VerdictReview, ReasonProxy},
		{"scraper", ev(SourceProxycheck).flag("scraper", true), FlagScraper, VerdictReview, ReasonScraper},
		{"anonymous", ev(SourceProxycheck).flag("anonymous", true), FlagAnonymous, VerdictReview, ReasonAnonymous},
		{"abuse", ev(SourceAbuseIPDB).abuse(80, 1), FlagAbuse, VerdictHighRisk, ReasonRecentAbuse},
		{"dnsbl", ev(SourceDNSBL).dnsbl([]string{"zone"}, []string{"zone"}), FlagDNSBL, VerdictReview, "DNSBL_LISTED:zone"},
		{"attack", ev(SourceProxycheck).attacks("proxy"), FlagAttackHistory, VerdictReview, ReasonAttackHistory},
		{"tor_exit", ev(SourceTorProject).roles("exit"), FlagTor, VerdictHighRisk, ReasonTorExit},
		{"tor_relay", ev(SourceTorProject).roles("guard"), 0, VerdictReview, ReasonTorRelay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Assess(testIPv4Addr(t), evidence(ev(SourceIPPure).fraud(20), tc.evidence), testNow, nil)
			if got.PurityScore == nil || *got.PurityScore != 80 || got.PenaltyPoints != 0 || len(got.Penalties) != 0 || got.Flags&tc.flag != tc.flag || got.Verdict != tc.verdict || !hasReason(got, tc.reason) {
				t.Fatalf("independent risk lost: %+v", got)
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

	t.Run("primary_valid_until_wins", func(t *testing.T) {
		got := Assess(netip.MustParseAddr(testIPv4), evidence(
			ev("ippure").fraud(0).until(testNow.Add(2*time.Hour)),
			ev("ipqs").fraud(0).until(testNow.Add(30*time.Minute)),
		), testNow, nil)
		want := testNow.Add(2 * time.Hour)
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

// TestAssess_Deterministic guards: identical input must produce identical
// output, including the tie-breaks and the component ordering.
func TestAssess_Deterministic(t *testing.T) {
	input := Input{
		IP: netip.MustParseAddr(testIPv4),
		Evidence: evidence(
			ev("proxycheck").risk(0).typeOf("business"),
			ev("ipapi_is").completeNegatives().typeOf("wireless"),
			ev("dnsbl").dnsbl([]string{"a", "b"}, []string{"b"}),
			ev("abuseipdb").abuse(30, 2),
			ev("ipqs").fraud(10).typeOf("datacenter"),
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
	older := ev("ippure").fraud(90).observedAt(testNow.Add(-time.Hour))
	newer := ev("ippure").fraud(10).observedAt(testNow)
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
			evidence(ev("ippure").fraud(tc.risk)),
			testNow, map[string]bool{SourceProxycheck: true})
		if got.PurityBand != tc.band {
			t.Fatalf("risk %d: band = %q, want %q", tc.risk, got.PurityBand, tc.band)
		}
	}
}

func TestAssess_ConfiguredBandThresholds(t *testing.T) {
	// The band thresholds are a setting, so the same score must be able to
	// land in a different band without the scoring itself changing: score 75
	// is "mixed" under the shipped bands and "clean" under these.
	bands := quality.Bands{Excellent: 80, Clean: 70, Fair: 60, Mixed: 50}
	input := func() Input {
		return Input{
			IP:       netip.MustParseAddr(testIPv4),
			Evidence: evidence(ev("ippure").fraud(25)),
			Enabled:  map[string]bool{SourceProxycheck: true},
			Now:      testNow,
		}
	}

	configured := input()
	configured.Bands = bands
	if got := AssessInput(configured); got.PurityBand != "clean" {
		t.Fatalf("configured bands: band = %q, want %q", got.PurityBand, "clean")
	}

	// The zero value keeps the shipped bands, which is what a caller with no
	// settings to read must get.
	if got := AssessInput(input()); got.PurityBand != "mixed" {
		t.Fatalf("default bands: band = %q, want %q", got.PurityBand, "mixed")
	}
}

func TestAssess_ConfidenceDoesNotClaimCalibration(t *testing.T) {
	for _, rows := range [][]quality.Evidence{nil, evidence(ev(SourceProxycheck).risk(0))} {
		if got := Assess(testIPv4Addr(t), rows, testNow, nil); got.Confidence != ConfidenceNone {
			t.Fatal("auxiliary evidence created primary confidence")
		}
	}
	got := Assess(testIPv4Addr(t), evidence(ev(SourceIPPure).fraud(0)), testNow, nil)
	if got.Confidence != ConfidenceLow || got.Coverage != 1 || got.SourceCount != 1 {
		t.Fatalf("single vendor is not calibrated high confidence: %+v", got)
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

func formatInt(v *int) string {
	if v == nil {
		return "nil"
	}
	return string(rune('0'+*v/100%10)) + string(rune('0'+*v/10%10)) + string(rune('0'+*v%10))
}

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
