package assess

import (
	"testing"
	"time"

	"prism/internal/quality"
)

func TestVoteIPTypeCountsIndependentProviders(t *testing.T) {
	cases := []struct {
		name     string
		evidence []quality.Evidence
		want     string
	}{
		{
			name: "repeated_residential_rows_do_not_make_a_majority",
			evidence: evidence(
				ev(SourceProxycheck).typeOf(IPTypeResidential),
				ev(SourceProxycheck).typeOf(IPTypeResidential),
				ev(SourceProxycheck).typeOf(IPTypeResidential),
				ev(SourceIPAPI).typeOf(IPTypeDatacenter),
			),
			want: IPTypeConflicting,
		},
		{
			name: "repeated_datacenter_rows_do_not_make_a_majority",
			evidence: evidence(
				ev(SourceProxycheck).typeOf(IPTypeResidential),
				ev(SourceIPAPI).typeOf(IPTypeDatacenter),
				ev(SourceIPAPI).typeOf(IPTypeDatacenter),
				ev(SourceIPAPI).typeOf(IPTypeDatacenter),
			),
			want: IPTypeConflicting,
		},
		{
			name: "proxycheck_routes_are_one_provider",
			evidence: evidence(
				ev(SourceProxycheck).typeOf(IPTypeResidential),
				ev(SourceProxycheckNode).typeOf(IPTypeResidential),
				ev(SourceIPAPI).typeOf(IPTypeDatacenter),
			),
			want: IPTypeConflicting,
		},
		{
			name: "ipapi_is_routes_are_one_provider",
			evidence: evidence(
				ev(SourceIPAPIIs).typeOf(IPTypeResidential),
				ev(SourceIPAPIIsNode).typeOf(IPTypeResidential),
				ev(SourceProxycheck).typeOf(IPTypeDatacenter),
			),
			want: IPTypeConflicting,
		},
		{
			name: "newer_alias_replaces_old_host_vote",
			evidence: evidence(
				ev(SourceProxycheck).typeOf(IPTypeDatacenter).observedAt(testNow.Add(-time.Minute)),
				ev(SourceProxycheckNode).typeOf(IPTypeResidential),
				ev(SourceIPPure).residential(true),
			),
			want: IPTypeResidential,
		},
		{
			name: "newer_unknown_does_not_reuse_old_vote",
			evidence: evidence(
				ev(SourceProxycheck).typeOf(IPTypeResidential).observedAt(testNow.Add(-time.Minute)),
				ev(SourceProxycheck).typeOf(IPTypeUnknown),
				ev(SourceIPAPI).typeOf(IPTypeDatacenter),
			),
			want: IPTypeDatacenter,
		},
		{
			name: "host_wins_equal_time_alias_tie",
			evidence: evidence(
				ev(SourceProxycheckNode).typeOf(IPTypeDatacenter),
				ev(SourceProxycheck).typeOf(IPTypeResidential),
				ev(SourceIPPure).residential(true),
			),
			want: IPTypeResidential,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, reverse := range []bool{false, true} {
				rows := append([]quality.Evidence(nil), tc.evidence...)
				if reverse {
					for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
						rows[i], rows[j] = rows[j], rows[i]
					}
				}
				var reasons []string
				if got := networkType(independentEvidence(rows), &reasons); got != tc.want {
					t.Errorf("voteIPType(reverse=%t) = %s, want %s", reverse, got, tc.want)
				}
				out := AssessInput(Input{IP: testIPv4Addr(t), Evidence: rows, Now: testNow})
				if out.IPType != tc.want {
					t.Errorf("AssessInput(reverse=%t) = %s, want %s", reverse, out.IPType, tc.want)
				}
			}
		})
	}
}

func TestNetworkProviderConflictCannotBeOutvoted(t *testing.T) {
	cases := []struct {
		name     string
		evidence []quality.Evidence
		want     string
	}{
		{
			name:     "conflict_without_clear_votes_overrides_asn_heuristic",
			evidence: evidence(ev(SourceProxycheck).typeOf(IPTypeConflicting)),
			want:     IPTypeConflicting,
		},
		{
			name: "one_clear_provider_cannot_resolve_conflict",
			evidence: evidence(
				ev(SourceProxycheck).typeOf(IPTypeConflicting),
				ev(SourceIPPure).residential(true),
			),
			want: IPTypeConflicting,
		},
		{
			name: "four_agreeing_providers_override_one_conflicting_provider",
			evidence: evidence(
				ev(SourceProxycheck).typeOf(IPTypeConflicting),
				ev(SourceIPPure).residential(true),
				ev(SourceIPAPI).typeOf(IPTypeResidential),
				ev(SourceIPAPIIs).typeOf(IPTypeResidential),
				ev(SourceIPQS).typeOf(IPTypeResidential),
			),
			want: IPTypeConflicting,
		},
		{
			name: "two_clear_providers_resolve_conflict_at_exact_two_thirds",
			evidence: evidence(
				ev(SourceProxycheck).typeOf(IPTypeConflicting),
				ev(SourceIPPure).residential(true),
				ev(SourceIPAPI).typeOf(IPTypeResidential),
			),
			want: IPTypeConflicting,
		},
		{
			name: "conflict_does_not_veto_datacenter_majority",
			evidence: evidence(
				ev(SourceProxycheck).typeOf(IPTypeConflicting),
				ev(SourceIPAPIIs).typeOf(IPTypeDatacenter),
				ev(SourceIPAPI).typeOf(IPTypeDatacenter),
			),
			want: IPTypeConflicting,
		},
		{
			name: "duplicated_clear_provider_cannot_resolve_conflict",
			evidence: evidence(
				ev(SourceProxycheck).typeOf(IPTypeConflicting),
				ev(SourceIPAPIIs).typeOf(IPTypeResidential),
				ev(SourceIPAPIIsNode).typeOf(IPTypeResidential),
				ev(SourceIPAPIIs).typeOf(IPTypeResidential),
			),
			want: IPTypeConflicting,
		},
		{
			name: "duplicated_conflict_cannot_block_independent_majority",
			evidence: evidence(
				ev(SourceProxycheck).typeOf(IPTypeConflicting),
				ev(SourceProxycheckNode).typeOf(IPTypeConflicting),
				ev(SourceProxycheck).typeOf(IPTypeConflicting),
				ev(SourceIPPure).residential(true),
				ev(SourceIPAPI).typeOf(IPTypeResidential),
			),
			want: IPTypeConflicting,
		},
		{
			name: "two_clear_of_four_providers_is_not_two_thirds",
			evidence: evidence(
				ev(SourceProxycheck).typeOf(IPTypeConflicting),
				ev(SourceIPPure).residential(true),
				ev(SourceIPAPI).typeOf(IPTypeResidential),
				ev(SourceIPAPIIs).typeOf(IPTypeDatacenter),
			),
			want: IPTypeConflicting,
		},
		{
			name: "unknown_provider_abstains",
			evidence: evidence(
				ev(SourceProxycheck).typeOf(IPTypeConflicting),
				ev(SourceIPPure).residential(true),
				ev(SourceIPAPI).typeOf(IPTypeResidential),
				ev(SourceIPAPIIs).typeOf(IPTypeUnknown),
			),
			want: IPTypeConflicting,
		},
		{
			name: "non_residential_cannot_resolve_internal_conflict",
			evidence: evidence(
				ev(SourceProxycheck).typeOf(IPTypeConflicting),
				ev(SourceIPPure).residential(false),
			),
			want: IPTypeConflicting,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reasons []string
			if got := networkType(independentEvidence(tc.evidence), &reasons); got != tc.want {
				t.Errorf("voteIPType = %s, want %s", got, tc.want)
			}
			if got := containsString(reasons, ReasonIPTypeConflicting); got != (tc.want == IPTypeConflicting) {
				t.Errorf("conflict reason = %t, want %t", got, tc.want == IPTypeConflicting)
			}
			out := AssessInput(Input{IP: testIPv4Addr(t), Evidence: tc.evidence, Now: testNow})
			if out.IPType != tc.want || (out.Verdict == VerdictConflicting) != (tc.want == IPTypeConflicting) {
				t.Errorf("assessment type/verdict = %s/%s, want type %s", out.IPType, out.Verdict, tc.want)
			}
			if hasReason(out, ReasonIPTypeConflicting) != (tc.want == IPTypeConflicting) {
				t.Errorf("assessment has wrong conflict reasons: %v", out.Reasons)
			}
		})
	}
}
