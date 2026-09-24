package subscription

import (
	"fmt"
	"sort"
	"strings"

	"prism/internal/node"
)

// Subscription parse report (WP06 §9).
//
// The report makes every drop explainable. A node that is recognised but not
// imported must appear in Skipped with a reason and a detail string; there are
// no silent drops left in the parser.

// MaxSkippedNodes caps the number of reported skip records.
const MaxSkippedNodes = 500

// Parse-report source values.
const (
	SourceSingbox = "singbox"
	SourceClash   = "clash"
	SourceSurge   = "surge"
	SourceURI     = "uri"
	SourceOpenVPN = "ovpn"
	SourcePlain   = "plain"
)

// ReasonComplexityExceeded is the second report-only reason of the YAML
// resource guard: the body stayed within MaxYAMLNestingDepth but one of its
// mappings carries more than MaxYAMLMappingKeys keys, which costs yaml.v3's
// decoder quadratic work (see yaml_guard.go).
const ReasonComplexityExceeded = "COMPLEXITY_EXCEEDED"

// Parse-report reason produced by the parser itself for a body that a
// parser-side resource limit refused wholesale, rather than for a node the
// parser could not import: the nesting-depth guard of the Clash YAML path
// (see MaxYAMLNestingDepth in yaml_depth.go). It is a report-only reason; the
// node.Reason* codes stay the vocabulary for per-node drops.
const ReasonDepthExceeded = "DEPTH_EXCEEDED"

// ParseResult is the outcome of one subscription parse.
type ParseResult struct {
	Nodes   []ParsedNode  `json:"-"`
	Skipped []SkippedNode `json:"skipped"`
	Stats   ParseStats    `json:"stats"`
}

// ParseStats summarises a parse.
type ParseStats struct {
	Total      int            `json:"total"`
	Imported   int            `json:"imported"`
	Skipped    int            `json:"skipped"`
	ByEngine   map[string]int `json:"by_engine"`
	ByProtocol map[string]int `json:"by_protocol"`
	// SkippedOverflow counts skip records beyond MaxSkippedNodes.
	SkippedOverflow int `json:"skipped_overflow,omitempty"`
}

// SkippedNode is one explainable drop.
type SkippedNode struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Source string `json:"source"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// deferredProtocolTypes lists input types that sing-box cannot represent and
// that the rejected mihomo fallback kernel would have handled. They are
// reported as ENGINE_NOT_BUILT with the missing kernel capability.
var deferredProtocolTypes = map[string]string{
	"ssr":          "ssr",
	"shadowsocksr": "ssr",
	"mieru":        "mieru",
	"masque":       "masque",
	"trusttunnel":  "trusttunnel",
	"sudoku":       "sudoku",
	"shadowquic":   "shadowquic",
	"gost-relay":   "gost-relay",
	"restls":       "ss(restls)",
	"kcptun":       "ss(kcptun)",
	"gost-plugin":  "ss(gost-plugin)",
	"easytier":     "easytier",
	"zerotier":     "zerotier",
	"openvpn":      "openvpn (use a .ovpn profile instead)",
	"tailscale":    "tailscale",
}

// deferredShareLinkSchemes lists share-link schemes delegated to the rejected
// mihomo fallback kernel.
var deferredShareLinkSchemes = map[string]string{
	"ssr":    "ssr",
	"mierus": "mieru",
}

// singboxInfrastructureTypes are sing-box objects that belong to a config but
// are not nodes of their own; they are skipped without a report entry.
var singboxInfrastructureTypes = map[string]bool{
	"direct":         true,
	"block":          true,
	"bridge":         true,
	"selector":       true,
	"urltest":        true,
	"dns":            true,
	"openvpn-server": true,
}

// parseReport accumulates one parse.
type parseReport struct {
	nodes    []ParsedNode
	skipped  []SkippedNode
	overflow int
}

func newParseReport() *parseReport {
	return &parseReport{}
}

// addNodes records the imported nodes of a successful attempt.
func (r *parseReport) addNodes(parsed []ParsedNode) {
	r.nodes = append(r.nodes, parsed...)
}

// addSkip records one explainable drop. Records beyond MaxSkippedNodes are
// counted only, so a pathological subscription cannot bloat the report.
func (r *parseReport) addSkip(skipped SkippedNode) {
	if skipped.Reason == "" {
		skipped.Reason = node.ReasonInvalid
	}
	if len(r.skipped) >= MaxSkippedNodes {
		r.overflow++
		return
	}
	r.skipped = append(r.skipped, skipped)
}

// result renders the accumulated state.
func (r *parseReport) result() ParseResult {
	stats := ParseStats{
		Imported:        len(r.nodes),
		Skipped:         len(r.skipped) + r.overflow,
		ByEngine:        map[string]int{},
		ByProtocol:      map[string]int{},
		SkippedOverflow: r.overflow,
	}
	stats.Total = stats.Imported + stats.Skipped
	for _, parsed := range r.nodes {
		doc, err := node.ParseNodeDoc(parsed.RawOptions)
		if err != nil {
			stats.ByEngine[node.EngineSingbox]++
			continue
		}
		stats.ByEngine[doc.Engine]++
		protocol := strings.ToLower(strings.TrimSpace(doc.Type))
		if protocol != "" {
			stats.ByProtocol[protocol]++
		}
	}
	for _, skipped := range r.skipped {
		if skipped.Type == "" {
			continue
		}
		stats.ByProtocol[strings.ToLower(skipped.Type)]++
	}
	return ParseResult{
		Nodes:   append([]ParsedNode(nil), r.nodes...),
		Skipped: append([]SkippedNode(nil), r.skipped...),
		Stats:   stats,
	}
}

// skipDeferredOrUnknown classifies an object whose type Prism does not import.
// It returns false when the type is infrastructure that should stay silent.
func skipDeferredOrUnknown(name string, typeName string, source string) (SkippedNode, bool) {
	normalized := strings.ToLower(strings.TrimSpace(typeName))
	if normalized == "" {
		return SkippedNode{}, false
	}
	// Infrastructure objects (direct/block/selector/urltest/dns/bridge/…) belong
	// to a config but are not nodes of their own, so they are silently ignored:
	// reporting them as UNSUPPORTED_PROTOCOL would bury the real drops in noise.
	if singboxInfrastructureTypes[normalized] {
		return SkippedNode{}, false
	}
	if deferred, ok := deferredProtocolTypes[normalized]; ok {
		return SkippedNode{
			Name:   strings.TrimSpace(name),
			Type:   normalized,
			Source: source,
			Reason: node.ReasonEngineNotBuilt,
			Detail: deferred,
		}, true
	}
	return SkippedNode{
		Name:   strings.TrimSpace(name),
		Type:   normalized,
		Source: source,
		Reason: node.ReasonUnsupportedProtocol,
		Detail: fmt.Sprintf("type %q is not importable by Prism", normalized),
	}, true
}

// skipSnellVersion classifies a Snell node by version. sing-box 1.14 supports
// version 4 and 6 only (fact F9).
func skipSnellVersion(name string, source string, version uint64, hasVersion bool) SkippedNode {
	label := "unspecified"
	if hasVersion {
		label = fmt.Sprintf("v%d", version)
	}
	return SkippedNode{
		Name:   strings.TrimSpace(name),
		Type:   "snell",
		Source: source,
		Reason: node.ReasonEngineNotBuilt,
		Detail: fmt.Sprintf("snell %s (sing-box supports v4 and v6)", label),
	}
}

// skipNode renders a skip for an invalid but recognised node.
func skipNode(name string, typeName string, source string, reason string, detail string) SkippedNode {
	return SkippedNode{
		Name:   strings.TrimSpace(name),
		Type:   strings.ToLower(strings.TrimSpace(typeName)),
		Source: source,
		Reason: reason,
		Detail: detail,
	}
}

// SortedSkipSummary renders the skip reasons for logging (reason=count pairs,
// sorted by reason). It never contains node names, so a log line can never leak
// subscription content.
func SortedSkipSummary(skipped []SkippedNode) string {
	if len(skipped) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, entry := range skipped {
		counts[entry.Reason]++
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, " ")
}

// ------------------------------------------------------------------
// Grouped summary (the surface carried by the subscription API)
// ------------------------------------------------------------------

// Bounds of the grouped summary. The full report is already bounded to
// MaxSkippedNodes records; the summary is bounded again so the list/get
// responses stay small even for a pathological subscription:
//
//   - at most MaxSkipSummaryReasons reason buckets are listed; further distinct
//     reasons are counted only, in SkipSummary.ReasonsOverflow;
//   - at most MaxSkipSummarySamples sample names and types are listed per bucket;
//     SkipReason.SamplesTruncated reports that more exist;
//   - each sampled name is capped at MaxSampleNameRunes runes.
const (
	// MaxSkipSummaryReasons caps the reason buckets of a summary.
	MaxSkipSummaryReasons = 16
	// MaxSkipSummarySamples caps the sample names/types per reason bucket.
	MaxSkipSummarySamples = 5
	// MaxSampleNameRunes caps one sampled name.
	MaxSampleNameRunes = 80
)

// SkipReason is one reason bucket of a parse summary.
type SkipReason struct {
	// Reason is the node.Reason* code shared by every node in the bucket.
	Reason string `json:"reason"`
	// Count is the number of skipped nodes with this reason.
	Count int `json:"count"`
	// Detail is the detail string of the first node in the bucket.
	Detail string `json:"detail,omitempty"`
	// SampleNames holds up to MaxSkipSummarySamples redacted names.
	SampleNames []string `json:"sample_names"`
	// SampleTypes holds up to MaxSkipSummarySamples distinct input types.
	SampleTypes []string `json:"sample_types,omitempty"`
	// SamplesTruncated is true when the bucket has more names/types than listed.
	SamplesTruncated bool `json:"samples_truncated"`
}

// SkipSummary is the grouped view of one subscription parse report.
type SkipSummary struct {
	// Total is imported + skipped for the parse that produced the report.
	Total int `json:"total"`
	// Imported is the number of imported nodes.
	Imported int `json:"imported"`
	// Skipped is the true number of dropped nodes, including the overflow
	// records the report did not store.
	Skipped int `json:"skipped"`
	// SkippedOverflow counts skipped nodes beyond MaxSkippedNodes; they are part
	// of Skipped but not of any reason bucket.
	SkippedOverflow int `json:"skipped_overflow,omitempty"`
	// Reasons lists the buckets, ordered by count (desc) then reason.
	Reasons []SkipReason `json:"reasons"`
	// ReasonsOverflow counts distinct reasons beyond MaxSkipSummaryReasons.
	ReasonsOverflow int `json:"reasons_overflow,omitempty"`
}

// SummarizeParseResult renders the grouped summary of one parse report.
func SummarizeParseResult(result ParseResult) SkipSummary {
	summary := SkipSummary{
		Total:           result.Stats.Total,
		Imported:        result.Stats.Imported,
		Skipped:         result.Stats.Skipped,
		SkippedOverflow: result.Stats.SkippedOverflow,
		Reasons:         []SkipReason{},
	}

	type bucket struct {
		count    int
		detail   string
		names    []string
		types    []string
		seenName map[string]bool
		seenType map[string]bool
		more     bool
	}
	buckets := map[string]*bucket{}
	for _, skipped := range result.Skipped {
		entry, ok := buckets[skipped.Reason]
		if !ok {
			entry = &bucket{seenName: map[string]bool{}, seenType: map[string]bool{}}
			buckets[skipped.Reason] = entry
		}
		entry.count++
		if entry.detail == "" {
			entry.detail = skipped.Detail
		}
		name := sanitizeSampleName(skipped.Name)
		if name != "" && !entry.seenName[name] {
			if len(entry.names) < MaxSkipSummarySamples {
				entry.names = append(entry.names, name)
			} else {
				entry.more = true
			}
			entry.seenName[name] = true
		} else if name != "" {
			entry.more = true
		}
		nodeType := strings.TrimSpace(skipped.Type)
		if nodeType != "" && !entry.seenType[nodeType] {
			if len(entry.types) < MaxSkipSummarySamples {
				entry.types = append(entry.types, nodeType)
			} else {
				entry.more = true
			}
			entry.seenType[nodeType] = true
		}
	}

	reasons := make([]string, 0, len(buckets))
	for reason := range buckets {
		reasons = append(reasons, reason)
	}
	sort.Slice(reasons, func(i, j int) bool {
		if buckets[reasons[i]].count != buckets[reasons[j]].count {
			return buckets[reasons[i]].count > buckets[reasons[j]].count
		}
		return reasons[i] < reasons[j]
	})
	if len(reasons) > MaxSkipSummaryReasons {
		summary.ReasonsOverflow = len(reasons) - MaxSkipSummaryReasons
		reasons = reasons[:MaxSkipSummaryReasons]
	}
	for _, reason := range reasons {
		entry := buckets[reason]
		summary.Reasons = append(summary.Reasons, SkipReason{
			Reason: reason,
			Count:  entry.count,
			Detail: entry.detail,
			// Non-nil so an empty list serialises as [] rather than null.
			SampleNames:      append([]string{}, entry.names...),
			SampleTypes:      append([]string{}, entry.types...),
			SamplesTruncated: entry.more,
		})
	}
	return summary
}

// sanitizeSampleName bounds one name taken from untrusted subscription input
// before it is echoed back through the API: control characters become spaces,
// credentials in an authority (scheme://user:pass@host) are redacted and the
// result is capped at MaxSampleNameRunes runes.
func sanitizeSampleName(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, name)
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	cleaned = redactAuthorityCredentials(cleaned)
	if runes := []rune(cleaned); len(runes) > MaxSampleNameRunes {
		return string(runes[:MaxSampleNameRunes]) + "…"
	}
	return cleaned
}

// redactAuthorityCredentials replaces a `user:pass@` userinfo section with
// `***@`, keeping the host readable.
func redactAuthorityCredentials(value string) string {
	at := strings.Index(value, "@")
	if at < 0 {
		return value
	}
	sep := strings.Index(value, "://")
	if sep < 0 || sep > at {
		return value
	}
	authorityStart := sep + 3
	if !strings.Contains(value[authorityStart:at], ":") {
		return value
	}
	return value[:authorityStart] + "***@" + value[at+1:]
}
