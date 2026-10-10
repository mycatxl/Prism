package assess

import (
	"net/netip"
	"testing"

	"prism/internal/quality"
)

// PRISM-DEVIATION: none. These tests pin the alias contract between a host-side
// source and its anonymous via-node variant: both feed one component, and the
// host-side row wins when both are present.

// TestAssess_ViaNodeVariantFeedsItsHostSource pins that the anonymous via-node
// variant is a scoring source through its alias. Before the alias existed, a
// deployment whose only evidence came from the via-node path scored as if the
// source had produced nothing at all.
func TestAssess_ViaNodeVariantFeedsItsHostSource(t *testing.T) {
	enabled := map[string]bool{SourceProxycheck: true, SourceIPAPIIs: true}

	cases := []struct {
		name       string
		evidence   []quality.Evidence
		components []string
	}{
		{
			name:       "proxycheck_node_feeds_proxycheck",
			evidence:   evidence(ev(SourceProxycheckNode).risk(20)),
			components: []string{SourceProxycheck},
		},
		{
			name:       "ipapi_is_node_feeds_ipapi_is",
			evidence:   evidence(ev(SourceIPAPIIsNode).typeOf("datacenter")),
			components: []string{SourceIPAPIIs},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := AssessInput(Input{IP: testIPv4Addr(t), Evidence: tc.evidence, Now: testNow, Enabled: enabled})
			if out.State != StateValid {
				t.Fatalf("state = %q (%v), want %q: the alias must make the row a scoring source",
					out.State, out.Reasons, StateValid)
			}
			got := make([]string, 0, len(out.Components))
			for _, component := range out.Components {
				got = append(got, component.Source)
			}
			if len(got) != len(tc.components) || got[0] != tc.components[0] {
				t.Fatalf("components = %v, want %v", got, tc.components)
			}
			// The component carries the host-side source id and weight, so the
			// denominator a caller computes from ScoringSources() matches.
			weight := scoringWeights[tc.components[0]]
			if out.Components[0].Weight != weight {
				t.Fatalf("weight = %d, want %d", out.Components[0].Weight, weight)
			}
		})
	}
}

// TestAssess_ViaNodeVariantDoesNotDoubleCount pins the other half: when both the
// host-side row and its variant are present for one address, they must fold into
// a single component - the same source counted twice would silently double its
// weight in the score.
func TestAssess_ViaNodeVariantDoesNotDoubleCount(t *testing.T) {
	enabled := map[string]bool{SourceProxycheck: true, SourceIPAPIIs: true}
	out := AssessInput(Input{
		IP: testIPv4Addr(t),
		Evidence: evidence(
			ev(SourceProxycheck).risk(10),
			ev(SourceProxycheckNode).risk(90),
			ev(SourceIPAPIIs).typeOf("residential"),
			ev(SourceIPAPIIsNode).typeOf("datacenter"),
		),
		Now: testNow, Enabled: enabled,
	})
	if out.State != StateValid {
		t.Fatalf("state = %q (%v)", out.State, out.Reasons)
	}
	seen := map[string]int{}
	for _, component := range out.Components {
		seen[component.Source]++
	}
	if seen[SourceProxycheck] != 1 {
		t.Fatalf("proxycheck components = %d, want exactly 1: the variant must fold into it", seen[SourceProxycheck])
	}
	if seen[SourceIPAPIIs] != 1 {
		t.Fatalf("ipapi_is components = %d, want exactly 1: the variant must fold into it", seen[SourceIPAPIIs])
	}
	// An exact match outranks the alias, so the host-side rows are the ones read.
	for _, component := range out.Components {
		switch component.Source {
		case SourceProxycheck:
			if component.Raw != 10 {
				t.Fatalf("proxycheck raw = %d, want the host-side row's 10", component.Raw)
			}
		case SourceIPAPIIs:
			if component.Raw != 100 {
				t.Fatalf("ipapi_is raw = %d, want the host-side row's 100", component.Raw)
			}
		}
	}
}

// TestAssess_ViaNodeVariantVotesAndFlags pins that a via-node row takes part in
// the IP-type vote and the flag detection, not just the score.
func TestAssess_ViaNodeVariantVotesAndFlags(t *testing.T) {
	enabled := map[string]bool{SourceIPAPIIs: true}
	out := AssessInput(Input{
		IP: testIPv4Addr(t),
		Evidence: evidence(
			ev(SourceIPAPIIsNode).typeOf("datacenter").flag("proxy", true),
		),
		Now: testNow, Enabled: enabled,
	})
	if out.State != StateValid {
		t.Fatalf("state = %q (%v)", out.State, out.Reasons)
	}
	if out.IPType != IPTypeDatacenter {
		t.Fatalf("ip_type = %q, want %q: the variant must vote", out.IPType, IPTypeDatacenter)
	}
	if out.Flags&FlagProxy == 0 {
		t.Fatalf("flags = %d, want the proxy flag from the variant", out.Flags)
	}
	if out.IPType == IPTypeUnknown {
		t.Fatal("the variant's ip_type must not be ignored")
	}
}

// TestAssess_StaleViaNodeEvidenceIsPendingNotUnsupported pins the state branch
// that noEvidenceState decides: a row that exists but has expired is a refresh
// problem, so the variant must be recognised as an enabled scoring source.
func TestAssess_StaleViaNodeEvidenceIsPendingNotUnsupported(t *testing.T) {
	enabled := map[string]bool{SourceIPAPIIs: true}
	out := AssessInput(Input{
		IP:       testIPv4Addr(t),
		Evidence: evidence(ev(SourceIPAPIIsNode).typeOf("datacenter").expired()),
		Now:      testNow, Enabled: enabled,
	})
	if out.State != StatePending {
		t.Fatalf("state = %q (%v), want %q", out.State, out.Reasons, StatePending)
	}
	if !hasReason(out, ReasonNoValidEvidence) {
		t.Fatalf("reasons = %v, want %q", out.Reasons, ReasonNoValidEvidence)
	}
}

// hasReason reports whether one reason code is present.
func hasReason(out Assessment, want string) bool {
	for _, reason := range out.Reasons {
		if reason == want {
			return true
		}
	}
	return false
}

// testIPv4Addr parses the shared test address.
func testIPv4Addr(t *testing.T) netip.Addr {
	t.Helper()
	return netip.MustParseAddr(testIPv4)
}
