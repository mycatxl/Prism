package subscription

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"prism/internal/node"
)

// TestSkipDeferredOrUnknown_InfrastructureIsSilent pins the guard in
// skipDeferredOrUnknown: config-level sing-box objects are not nodes and must
// not be reported, while deferred and unknown types keep their reason.
func TestSkipDeferredOrUnknown_InfrastructureIsSilent(t *testing.T) {
	infrastructure := []string{
		"direct", "block", "bridge", "selector", "urltest", "dns", "openvpn-server",
		"DIRECT", "  Selector  ",
	}
	for _, typeName := range infrastructure {
		skipped, ok := skipDeferredOrUnknown("some-tag", typeName, SourceSingbox)
		if ok {
			t.Errorf("type %q: got skip %+v, want no report entry", typeName, skipped)
		}
	}

	deferred, ok := skipDeferredOrUnknown("ssr-node", "ssr", SourceClash)
	if !ok {
		t.Fatal(`type "ssr": got no report entry, want ENGINE_NOT_BUILT`)
	}
	if deferred.Reason != node.ReasonEngineNotBuilt {
		t.Errorf(`type "ssr": reason %q, want %q`, deferred.Reason, node.ReasonEngineNotBuilt)
	}
	if deferred.Name != "ssr-node" || deferred.Source != SourceClash {
		t.Errorf(`type "ssr": name/source %q/%q, want "ssr-node"/%q`, deferred.Name, deferred.Source, SourceClash)
	}

	unknown, ok := skipDeferredOrUnknown("mystery", "definitely-not-a-protocol", SourceSingbox)
	if !ok {
		t.Fatal("unknown type: got no report entry, want UNSUPPORTED_PROTOCOL")
	}
	if unknown.Reason != node.ReasonUnsupportedProtocol {
		t.Errorf("unknown type: reason %q, want %q", unknown.Reason, node.ReasonUnsupportedProtocol)
	}

	if _, ok := skipDeferredOrUnknown("empty", "   ", SourceSingbox); ok {
		t.Error("empty type: got a report entry, want none")
	}
}

// TestParseWithReport_InfrastructureOutboundsAreNotReported checks the guard on
// a real sing-box document: selector/urltest/direct/dns entries must not show up
// in the report, while an unsupported outbound still must.
func TestParseWithReport_InfrastructureOutboundsAreNotReported(t *testing.T) {
	body := []byte(`{"outbounds":[
		{"type":"shadowsocks","tag":"ok-node","server":"1.1.1.1","server_port":443,"method":"aes-256-gcm","password":"secret"},
		{"type":"selector","tag":"proxy","outbounds":["ok-node"]},
		{"type":"urltest","tag":"auto","outbounds":["ok-node"]},
		{"type":"direct","tag":"direct"},
		{"type":"dns","tag":"dns-out"},
		{"type":"block","tag":"blocked"},
		{"type":"ssr","tag":"legacy-ssr","server":"2.2.2.2","server_port":443}
	]}`)

	result, err := ParseWithReport(body)
	if err != nil {
		t.Fatalf("ParseWithReport: %v", err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("imported nodes: got %d, want 1", len(result.Nodes))
	}
	if len(result.Skipped) != 1 {
		t.Fatalf("skip records: got %d (%+v), want only the ssr node", len(result.Skipped), result.Skipped)
	}
	skipped := result.Skipped[0]
	if skipped.Type != "ssr" || skipped.Reason != node.ReasonEngineNotBuilt {
		t.Fatalf("skip record: got %+v, want type ssr with %s", skipped, node.ReasonEngineNotBuilt)
	}
	if result.Stats.Imported != 1 || result.Stats.Skipped != 1 {
		t.Fatalf("stats: got imported=%d skipped=%d, want 1/1", result.Stats.Imported, result.Stats.Skipped)
	}
}

// TestSummarizeParseResult_GroupsBoundedAndRedacts pins the summary that the
// subscription API carries: grouping by reason with counts that match the input,
// a bounded sample per reason and no credentials in the sampled names.
func TestSummarizeParseResult_GroupsBoundedAndRedacts(t *testing.T) {
	skipped := []SkippedNode{
		{Name: "ssr-a", Type: "ssr", Reason: node.ReasonEngineNotBuilt, Detail: "ssr"},
		{Name: "ssr-b", Type: "ssr", Reason: node.ReasonEngineNotBuilt, Detail: "ssr"},
		{Name: "mieru-a", Type: "mieru", Reason: node.ReasonEngineNotBuilt, Detail: "mieru"},
		{Name: "weird", Type: "wat", Reason: node.ReasonUnsupportedProtocol, Detail: "type is not importable"},
	}
	result := ParseResult{
		Skipped: skipped,
		Stats:   ParseStats{Total: 10, Imported: 6, Skipped: 4},
	}
	summary := SummarizeParseResult(result)
	if summary.Total != 10 || summary.Imported != 6 || summary.Skipped != 4 {
		t.Fatalf("summary totals: got %+v, want total=10 imported=6 skipped=4", summary)
	}
	if len(summary.Reasons) != 2 {
		t.Fatalf("summary reasons: got %d (%+v), want 2", len(summary.Reasons), summary.Reasons)
	}
	if summary.Reasons[0].Reason != node.ReasonEngineNotBuilt || summary.Reasons[0].Count != 3 {
		t.Errorf("first bucket: got %+v, want ENGINE_NOT_BUILT with 3", summary.Reasons[0])
	}
	if summary.Reasons[1].Reason != node.ReasonUnsupportedProtocol || summary.Reasons[1].Count != 1 {
		t.Errorf("second bucket: got %+v, want UNSUPPORTED_PROTOCOL with 1", summary.Reasons[1])
	}
	bucket := summary.Reasons[0]
	if len(bucket.SampleNames) != 3 {
		t.Errorf("sample names: got %v, want the 3 names of the bucket", bucket.SampleNames)
	}
	if bucket.SamplesTruncated {
		t.Error("samples_truncated: got true, want false for 3 of 5 slots")
	}
	if len(bucket.SampleTypes) != 2 {
		t.Errorf("sample types: got %v, want ssr and mieru", bucket.SampleTypes)
	}
	if bucket.Detail != "ssr" {
		t.Errorf("detail: got %q, want the first detail of the bucket", bucket.Detail)
	}
}

// TestSummarizeParseResult_AppliesBounds checks the documented over-limit
// behaviour: reason buckets are capped (the rest counted in ReasonsOverflow),
// samples per bucket are capped (SamplesTruncated) and one name is truncated.
func TestSummarizeParseResult_AppliesBounds(t *testing.T) {
	skipped := make([]SkippedNode, 0, 64)
	for reason := 0; reason < MaxSkipSummaryReasons+3; reason++ {
		for name := 0; name < MaxSkipSummarySamples+2; name++ {
			skipped = append(skipped, SkippedNode{
				Name:   fmt.Sprintf("reason-%02d-node-%02d", reason, name),
				Type:   "ssr",
				Reason: fmt.Sprintf("REASON_%02d", reason),
			})
		}
	}
	summary := SummarizeParseResult(ParseResult{
		Skipped: skipped,
		Stats:   ParseStats{Skipped: len(skipped), SkippedOverflow: 7},
	})
	if len(summary.Reasons) != MaxSkipSummaryReasons {
		t.Fatalf("reason buckets: got %d, want the bound %d", len(summary.Reasons), MaxSkipSummaryReasons)
	}
	if summary.ReasonsOverflow != 3 {
		t.Errorf("reasons_overflow: got %d, want 3", summary.ReasonsOverflow)
	}
	if summary.Skipped != len(skipped) || summary.SkippedOverflow != 7 {
		t.Errorf("summary counts: got %+v, want skipped=%d overflow=7", summary, len(skipped))
	}
	for _, bucket := range summary.Reasons {
		if len(bucket.SampleNames) != MaxSkipSummarySamples {
			t.Fatalf("bucket %s: got %d sample names, want the bound %d", bucket.Reason, len(bucket.SampleNames), MaxSkipSummarySamples)
		}
		if !bucket.SamplesTruncated {
			t.Errorf("bucket %s: samples_truncated is false although names were dropped", bucket.Reason)
		}
	}

	// A name longer than the bound is cut, and authority credentials are redacted.
	long := strings.Repeat("n", MaxSampleNameRunes+20)
	redacted := SummarizeParseResult(ParseResult{
		Skipped: []SkippedNode{
			{Name: "https://user:secret@example.com/path", Type: "wat", Reason: node.ReasonUnsupportedProtocol},
			{Name: long, Type: "wat", Reason: node.ReasonUnsupportedProtocol},
		},
		Stats: ParseStats{Total: 2, Skipped: 2},
	})
	names := redacted.Reasons[0].SampleNames
	if len(names) != 2 {
		t.Fatalf("redacted sample names: got %v, want 2", names)
	}
	if strings.Contains(names[0], "secret") {
		t.Errorf("sample name %q still contains the password", names[0])
	}
	if !strings.Contains(names[0], "example.com") {
		t.Errorf("sample name %q lost the host", names[0])
	}
	if runes := []rune(names[1]); len(runes) > MaxSampleNameRunes+1 {
		t.Errorf("long sample name: got %d runes, want at most %d plus the ellipsis", len(runes), MaxSampleNameRunes)
	}
}

// TestSortedSkipSummary_IsReasonCountsOnly pins the log line: the summary names
// reasons and counts, never a node name.
func TestSortedSkipSummary_IsReasonCountsOnly(t *testing.T) {
	text := SortedSkipSummary([]SkippedNode{
		{Name: "secret-name", Type: "ssr", Reason: node.ReasonEngineNotBuilt},
		{Name: "other-name", Type: "wat", Reason: node.ReasonUnsupportedProtocol},
		{Name: "third", Type: "ssr", Reason: node.ReasonEngineNotBuilt},
	})
	if text != "ENGINE_NOT_BUILT=2 UNSUPPORTED_PROTOCOL=1" {
		t.Errorf("summary: got %q", text)
	}
	if strings.Contains(text, "secret-name") {
		t.Error("summary must never contain a node name")
	}
	if got := SortedSkipSummary(nil); got != "" {
		t.Errorf("empty summary: got %q, want an empty string", got)
	}
}

// TestSummarizeParseResult_EmptyListsSerialiseAsArrays pins the wire shape the
// WebUI reads: an empty reason list, an empty sample list and an empty summary
// must be `[]`, never `null`.
func TestSummarizeParseResult_EmptyListsSerialiseAsArrays(t *testing.T) {
	empty := SummarizeParseResult(ParseResult{})
	encoded, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty summary: %v", err)
	}
	if !strings.Contains(string(encoded), `"reasons":[]`) {
		t.Errorf("empty summary: got %s, want an empty reasons array", encoded)
	}

	named := SummarizeParseResult(ParseResult{
		Skipped: []SkippedNode{{Type: "wat", Reason: node.ReasonUnsupportedProtocol}},
		Stats:   ParseStats{Total: 1, Skipped: 1},
	})
	if len(named.Reasons) != 1 {
		t.Fatalf("summary reasons: got %v, want one bucket", named.Reasons)
	}
	encoded, err = json.Marshal(named)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	if !strings.Contains(string(encoded), `"sample_names":[]`) {
		t.Errorf("nameless bucket: got %s, want an empty sample_names array", encoded)
	}

	// The runtime round-trip through the subscription must keep that shape.
	sub := NewSubscription("sub-shape", "shape", "", true, false)
	sub.SetParseSummary(&empty)
	stored := sub.ParseSummary()
	if stored == nil {
		t.Fatal("ParseSummary returned nil after SetParseSummary")
	}
	encoded, err = json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal stored summary: %v", err)
	}
	if !strings.Contains(string(encoded), `"reasons":[]`) {
		t.Errorf("stored summary: got %s, want an empty reasons array", encoded)
	}
	sub.SetParseSummary(nil)
	if sub.ParseSummary() != nil {
		t.Error("SetParseSummary(nil) must clear the summary")
	}
}
