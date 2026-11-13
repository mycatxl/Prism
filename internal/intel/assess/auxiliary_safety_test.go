package assess

import (
	"testing"

	"prism/internal/quality"
)

// Preserve the auxiliary safety assertions from the former weighted-score
// table, independently of the primary purity formula.
func TestAuxiliarySafetyReasonsSurviveSingleSourcePurity(t *testing.T) {
	for _, tc := range []struct {
		name                string
		row                 evb
		flag                Flags
		required, forbidden string
	}{
		{"unchecked_dnsbl", ev(SourceDNSBL).dnsbl([]string{"checked"}, []string{"unchecked"}), 0, "", "DNSBL_LISTED:unchecked"},
		{"no_checked_zones", ev(SourceDNSBL).dnsbl(nil, []string{"unchecked"}), 0, "", "DNSBL_LISTED:unchecked"},
		{"reported_abuse", ev(SourceAbuseIPDB).abuse(25, 1), FlagAbuse, ReasonAbuseConfidence, ReasonRecentAbuse},
		{"recent_abuse", ev(SourceAbuseIPDB).abuse(80, 5), FlagAbuse, ReasonRecentAbuse, ""},
		{"no_abuse_reports", ev(SourceAbuseIPDB).abuse(76, 0), 0, "", ReasonRecentAbuse},
		{"ipapi_compromised", ev(SourceIPAPIIs).completeNegatives().flag("compromised", true), FlagCompromised, ReasonCompromised, ""},
		{"ipapi_proxy", ev(SourceIPAPIIs).completeNegatives().flag("proxy", true), FlagProxy, ReasonProxy, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, withPrimary := range []bool{false, true} {
				rows := []quality.Evidence{tc.row.e}
				if withPrimary {
					rows = append(rows, ev(SourceIPPure).fraud(20).e)
				}
				out := Assess(testIPv4Addr(t), rows, testNow, nil)
				if withPrimary && (out.PurityScore == nil || *out.PurityScore != 80) || !withPrimary && out.PurityScore != nil {
					t.Fatal("auxiliary evidence changed primary purity")
				}
				if out.Flags&tc.flag != tc.flag || tc.required != "" && !hasReason(out, tc.required) || tc.forbidden != "" && hasReason(out, tc.forbidden) {
					t.Fatal("auxiliary safety reason changed")
				}
				if tc.name == "recent_abuse" && out.Verdict != VerdictHighRisk {
					t.Fatal("recent abuse lost high risk verdict")
				}
			}
		})
	}
}

func TestNativeProvenanceKeepsClaimsAndHeuristicsDistinct(t *testing.T) {
	for _, tc := range []struct {
		country, registered string
		want                bool
	}{{"US", "us", true}, {"DE", "US", false}} {
		out := Assess(testIPv4Addr(t), evidence(ev("maxmind_geolite2").country(tc.country).registered(tc.registered)), testNow, nil)
		if out.Native == nil || *out.Native != tc.want || out.Facts["native"].SourceClass != "heuristic" || !hasReason(out, ReasonNativeHeuristic) || out.PurityScore != nil {
			t.Fatal("native heuristic lost provenance or invented purity")
		}
	}
	out := Assess(testIPv4Addr(t), evidence(ev(SourceIPPure).fraud(20).native(true)), testNow, nil)
	if out.Native == nil || !*out.Native || hasReason(out, ReasonNativeHeuristic) || out.Facts["native"].Provider != SourceIPPure {
		t.Fatal("provider native claim was replaced by heuristic")
	}
}
