// Package export renders the node pool into the formats a user actually
// consumes: a sing-box config, a mihomo (Clash Meta) config, v2rayN / plain
// share-link lists and analysis CSV/JSON.
//
// The package is pure serialisation. It never dials, never touches the network
// and never imports a runtime kernel. Decision D-1 rejects mihomo as a runtime
// kernel; emitting a mihomo configuration file is still a deliverable because
// it adds no dependency.
package export

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"prism/internal/quality"
)

// Supported export formats (WP11 §2).
const (
	FormatSingbox = "singbox"
	FormatMihomo  = "mihomo"
	FormatV2rayN  = "v2rayn"
	FormatURI     = "uri"
	FormatCSV     = "csv"
	FormatJSON    = "json"
)

// DefaultNameTemplate is the identity template (WP11 §3).
const DefaultNameTemplate = "{name}"

// MaxItems bounds one export request. The API layer enforces it by truncating
// the selected node list and reporting the drop in Report.Truncated.
const MaxItems = 5000

// ErrUnsupportedFormat is returned for a format name outside Formats().
var ErrUnsupportedFormat = errors.New("unsupported export format")

// Formats returns the supported format names in a stable order.
func Formats() []string {
	return []string{FormatSingbox, FormatMihomo, FormatV2rayN, FormatURI, FormatCSV, FormatJSON}
}

// ValidFormat reports whether format is one of the supported names.
func ValidFormat(format string) bool {
	return slices.Contains(Formats(), format)
}

// ContentType returns the response content type of a format.
func ContentType(format string) string {
	switch format {
	case FormatSingbox, FormatJSON:
		return "application/json; charset=utf-8"
	case FormatMihomo:
		return "text/yaml; charset=utf-8"
	case FormatCSV:
		return "text/csv; charset=utf-8"
	case FormatV2rayN, FormatURI:
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

// FileName returns the default download file name of a format.
func FileName(format string) string {
	switch format {
	case FormatSingbox:
		return "prism.json"
	case FormatMihomo:
		return "prism.yaml"
	case FormatV2rayN:
		return "prism.txt"
	case FormatURI:
		return "prism-uri.txt"
	case FormatCSV:
		return "prism.csv"
	case FormatJSON:
		return "prism-export.json"
	}
	return "prism.export"
}

// Item is one node candidate for export.
//
// Name is the already rendered node name (see RenderNames). RawOptions is the
// node document in form A or form B (WP06 §1). Intel is the optional WP10
// quality/assessment summary of the node's egress IP; a nil Intel, or
// Options.IncludeIntel == false, leaves the intel-derived name-template
// variables and the intel CSV columns empty.
type Item struct {
	Name         string
	Hash         string // node hash; analysis formats only, never a credential
	Subscription string
	// Healthy mirrors the node-summary health rule (enabled, outbound ready,
	// not circuit-open). It is analysis data only; it never gates a format.
	Healthy bool
	// Region is the resolved egress region (the Cloudflare colo when the probe
	// reported one) and LatencyMs is the node's reference latency. Both feed
	// the analysis columns only.
	Region    string
	LatencyMs float64
	// RawOptions is the node document in form A or form B (WP06 §1).
	RawOptions json.RawMessage
	// Intel is the optional WP10 quality/assessment summary of the egress IP.
	Intel *quality.Summary
}

// Options controls one export.
type Options struct {
	// Format is one of Formats().
	Format string
	// NameTemplate renders each node name (§3). Empty means DefaultNameTemplate.
	NameTemplate string
	// SelectorTag and AutoTag are the sing-box / mihomo group tags.
	// Empty values mean "PROXY" and "AUTO".
	SelectorTag string
	AutoTag     string
	// IncludeIntel gates the intel-derived name variables and CSV columns.
	IncludeIntel bool
}

func (o Options) selectorTag() string {
	if tag := strings.TrimSpace(o.SelectorTag); tag != "" {
		return tag
	}
	return "PROXY"
}

func (o Options) autoTag() string {
	if tag := strings.TrimSpace(o.AutoTag); tag != "" {
		return tag
	}
	return "AUTO"
}

// Skip records one node the target format cannot represent. Name is the node
// name and Reason is one of the Reason* constants.
type Skip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Report records the outcome of one export, mirroring the subscription parse
// report: a node that cannot be represented is skipped with a reason, never
// dropped silently.
type Report struct {
	Exported  int    `json:"exported"`
	Skipped   []Skip `json:"skipped"`
	Truncated int    `json:"truncated,omitempty"`
}

// SkipCount returns the number of skipped nodes.
func (r Report) SkipCount() int { return len(r.Skipped) }

// Reason strings. They are stable, machine-readable and never carry
// credentials.
const (
	// ReasonNotRepresentablePrefix is prefixed with the target format, for
	// example "NOT_REPRESENTABLE:singbox".
	ReasonNotRepresentablePrefix = "NOT_REPRESENTABLE:"
	// ReasonNotRepresentableMihomoChain is the dedicated chain reason of §2.2.
	ReasonNotRepresentableMihomoChain = "NOT_REPRESENTABLE:mihomo(chain)"
	// ReasonInvalidNode marks a node document that cannot be parsed at all.
	ReasonInvalidNode = "INVALID:node document"
	// ReasonDuplicateName marks a node dropped because its rendered name and
	// every suffixed replacement collided inside the bounded name budget.
	ReasonDuplicateName = "INVALID:duplicate node name"
)

// NotRepresentable renders the per-format skip reason.
func NotRepresentable(format string) string {
	return ReasonNotRepresentablePrefix + format
}

// Export renders nodes into the requested format.
//
// It returns the response body, the response content type and the report. A
// node the target format cannot express is skipped and recorded in
// report.Skipped; that is never an error. An error is returned only when the
// request itself is invalid (unknown format).
func Export(nodes []Item, format string, opt Options) (body []byte, contentType string, report Report, err error) {
	if !ValidFormat(format) {
		return nil, "", Report{}, fmt.Errorf("%w: %s", ErrUnsupportedFormat, format)
	}
	opt.Format = format
	if strings.TrimSpace(opt.NameTemplate) == "" {
		opt.NameTemplate = DefaultNameTemplate
	}

	items, report := prepareItems(nodes, opt)

	switch format {
	case FormatSingbox:
		body, report.Exported = exportSingbox(items, opt, &report)
	case FormatMihomo:
		body, report.Exported = exportMihomo(items, opt, &report)
	case FormatV2rayN:
		body, report.Exported = exportV2rayN(items, &report)
	case FormatURI:
		body, report.Exported = exportURI(items, &report)
	case FormatCSV:
		body, report.Exported = exportCSV(items, opt)
	case FormatJSON:
		body = exportJSON(items, opt, &report)
	}
	return body, ContentType(format), report, nil
}

// preparedItem is an Item whose document has been parsed once.
type preparedItem struct {
	Item
	Doc     NodeDoc
	RawName string
}

// prepareItems renders and deduplicates names and parses every document.
func prepareItems(nodes []Item, opt Options) ([]preparedItem, Report) {
	report := Report{Skipped: []Skip{}}
	names := RenderNames(nodes, opt)

	out := make([]preparedItem, 0, len(nodes))
	for i, n := range nodes {
		name := ""
		if i < len(names) {
			name = names[i]
		}
		if name == "" {
			report.Skipped = append(report.Skipped, Skip{Name: n.Name, Reason: ReasonDuplicateName})
			continue
		}
		doc, err := ParseNodeDoc(n.RawOptions)
		if err != nil {
			report.Skipped = append(report.Skipped, Skip{Name: name, Reason: ReasonInvalidNode})
			continue
		}
		out = append(out, preparedItem{Item: n, Doc: doc, RawName: n.Name})
	}
	return out, report
}

// skip appends one entry to the report and returns the zero value of T.
func skip[T any](report *Report, name string, reason string) T {
	report.Skipped = append(report.Skipped, Skip{Name: name, Reason: reason})
	var zero T
	return zero
}
