package export

import (
	"strings"
	"testing"

	"prism/internal/quality"
)

// TestRenderNamesVariables covers every documented template variable (WP11 §3).
func TestRenderNamesVariables(t *testing.T) {
	item := fixtureNodes(t)[0]
	item.Intel = intelFixture()

	cases := []struct {
		template string
		want     string
	}{
		{"{name}", "ss-tokyo"},
		{"{index}", "1"},
		{"{country}", "JP"},
		{"{flag}", "🇯🇵"},
		{"{city}", "Tokyo"},
		{"{asn}", "AS64500"},
		{"{org}", "Example Residential ISP"},
		{"{ip_type}", "residential"},
		{"{purity}", "96"},
		{"{band}", "excellent"},
		{"{verdict}", "favorable"},
		{"{engine}", "singbox"},
		{"{protocol}", "shadowsocks"},
		{"{latency}", ""},
		{"{unknown}", ""},
		{"{flag} {name} | {band}", "🇯🇵 ss-tokyo | excellent"},
	}
	for _, tc := range cases {
		got := RenderNames([]Item{item}, Options{NameTemplate: tc.template, IncludeIntel: true})
		if len(got) != 1 || got[0] != tc.want {
			t.Fatalf("template %q = %q, want %q", tc.template, got, tc.want)
		}
	}
}

// TestRenderNamesWithoutIntel checks the intel variables collapse to empty
// strings when no assessment is attached.
func TestRenderNamesWithoutIntel(t *testing.T) {
	item := fixtureNodes(t)[0]
	item.Intel = nil
	got := RenderNames([]Item{item}, Options{NameTemplate: "{flag}{country} {name}"})
	if len(got) != 1 || got[0] != "ss-tokyo" {
		t.Fatalf("name = %v", got)
	}
}

// TestRenderNamesCompressesAndTruncates checks the §3 whitespace and length
// rules.
func TestRenderNamesCompressesAndTruncates(t *testing.T) {
	item := fixtureNodes(t)[0]
	item.Name = "  many   spaces\tand\nnewlines  "
	got := RenderNames([]Item{item}, Options{})
	if got[0] != "many spaces and newlines" {
		t.Fatalf("compressed name = %q", got[0])
	}

	long := strings.Repeat("a", 100)
	item.Name = long
	got = RenderNames([]Item{item}, Options{})
	if len(got[0]) != maxNameLength {
		t.Fatalf("truncated length = %d", len(got[0]))
	}
}

// TestRenderNamesKeepsExplicitSuffixNames checks a node literally named
// "<base> #2" is not stolen by the deduplication of "<base>".
func TestRenderNamesKeepsExplicitSuffixNames(t *testing.T) {
	first := fixtureNodes(t)[0]
	first.Name = "node"
	second := fixtureNodes(t)[1]
	second.Name = "node #2"
	third := fixtureNodes(t)[2]
	third.Name = "node"

	got := RenderNames([]Item{first, second, third}, Options{})
	if got[0] != "node" || got[1] != "node #2" || got[2] != "node #3" {
		t.Fatalf("names = %v", got)
	}
}

// TestRenderNamesIndexFollowsInputOrder checks the {index} variable.
func TestRenderNamesIndexFollowsInputOrder(t *testing.T) {
	items := fixtureNodes(t)[:3]
	got := RenderNames(items, Options{NameTemplate: "{index}-{name}"})
	want := []string{"1-ss-tokyo", "2-vmess-ws", "3-vless-reality"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("names = %v, want %v", got, want)
		}
	}
}

// TestRenderNamesLatencyVariableIsAlwaysEmpty documents that the latency
// variable has no source in the export package: latency lives on the node
// entry, not on the WP10 summary, so the variable renders empty rather than
// inventing a number.
func TestRenderNamesLatencyVariableIsAlwaysEmpty(t *testing.T) {
	item := fixtureNodes(t)[0]
	item.Intel = intelFixture()
	got := RenderNames([]Item{item}, Options{NameTemplate: "[{latency}]", IncludeIntel: true})
	if got[0] != "[]" {
		t.Fatalf("latency variable = %q", got[0])
	}
}

// TestIntelCSVValuesWithoutAssessment checks a summary that has evidence but no
// assessment still yields the egress facts.
func TestIntelCSVValuesWithoutAssessment(t *testing.T) {
	summary := &quality.Summary{
		IP:       "203.0.113.5",
		State:    "valid",
		Evidence: &quality.Evidence{IPType: "datacenter", CountryCode: "US"},
	}
	item := Item{Intel: summary}
	ipType, native, purity, band, verdict, confidence, flags := intelCSVValues(item, Options{IncludeIntel: true})
	if ipType != "datacenter" || native != "" || purity != "" || band != "" || verdict != "" || confidence != "" || flags != "" {
		t.Fatalf("values = %q %q %q %q %q %q %q", ipType, native, purity, band, verdict, confidence, flags)
	}
}

// TestIntelCSVValuesWithoutIntelFlag checks IncludeIntel gates the columns.
func TestIntelCSVValuesWithoutIntelFlag(t *testing.T) {
	item := Item{Intel: intelFixture()}
	ipType, _, purity, band, verdict, _, _ := intelCSVValues(item, Options{IncludeIntel: false})
	if ipType != "" || purity != "" || band != "" || verdict != "" {
		t.Fatalf("intel leaked without the opt-in: %q %q %q %q", ipType, purity, band, verdict)
	}
}

// TestPurityConfidenceUsesHighestSource checks the confidence column.
func TestPurityConfidenceUsesHighestSource(t *testing.T) {
	low, high := 20, 90
	summary := &quality.Summary{
		Sources: []quality.SourceSummary{
			{Evidence: &quality.Evidence{SourceConfidence: &low}},
			{Evidence: &quality.Evidence{SourceConfidence: &high}},
			{Evidence: &quality.Evidence{}},
		},
	}
	if got := purityConfidence(summary); got != "90" {
		t.Fatalf("confidence = %q", got)
	}
	if got := purityConfidence(nil); got != "" {
		t.Fatalf("nil summary confidence = %q", got)
	}
}
