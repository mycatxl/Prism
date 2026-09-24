package export

import (
	"encoding/csv"
	"encoding/json"
	"net/netip"
	"strconv"
	"strings"

	"prism/internal/node"
	"prism/internal/quality"
)

// Analysis formats (WP11 §2.4). Both csv and json carry the same columns, and
// neither ever contains a node credential: no password, uuid, key or psk is
// emitted, only the node identity, the egress facts and the WP10 assessment.
var exportColumns = []string{
	"name",
	"node_hash",
	"engine",
	"protocol",
	"protocol_detail",
	"subscription",
	"egress_ipv4",
	"egress_ipv6",
	"colo",
	"country",
	"city",
	"asn",
	"as_org",
	"ip_type",
	"native",
	"purity_score",
	"purity_band",
	"confidence",
	"verdict",
	"flags",
	"checks",
	"latency_ms",
	"healthy",
	"assessed_at",
}

// exportRow is one analysis row.
type exportRow struct {
	Name           string `json:"name"`
	NodeHash       string `json:"node_hash"`
	Engine         string `json:"engine"`
	Protocol       string `json:"protocol"`
	ProtocolDetail string `json:"protocol_detail"`
	Subscription   string `json:"subscription"`
	EgressIPv4     string `json:"egress_ipv4"`
	EgressIPv6     string `json:"egress_ipv6"`
	Colo           string `json:"colo"`
	Country        string `json:"country"`
	City           string `json:"city"`
	ASN            string `json:"asn"`
	ASOrg          string `json:"as_org"`
	IPType         string `json:"ip_type"`
	Native         string `json:"native"`
	PurityScore    string `json:"purity_score"`
	PurityBand     string `json:"purity_band"`
	Confidence     string `json:"confidence"`
	Verdict        string `json:"verdict"`
	Flags          string `json:"flags"`
	Checks         string `json:"checks"`
	LatencyMs      string `json:"latency_ms"`
	Healthy        string `json:"healthy"`
	AssessedAt     string `json:"assessed_at"`
}

func (r exportRow) values() []string {
	return []string{
		r.Name, r.NodeHash, r.Engine, r.Protocol, r.ProtocolDetail, r.Subscription,
		r.EgressIPv4, r.EgressIPv6, r.Colo, r.Country, r.City, r.ASN, r.ASOrg,
		r.IPType, r.Native, r.PurityScore, r.PurityBand, r.Confidence, r.Verdict,
		r.Flags, r.Checks, r.LatencyMs, r.Healthy, r.AssessedAt,
	}
}

// exportCSV writes the analysis columns as CSV.
func exportCSV(items []preparedItem, opt Options) ([]byte, int) {
	var builder strings.Builder
	writer := csv.NewWriter(&builder)
	if err := writer.Write(exportColumns); err != nil {
		return []byte{}, 0
	}
	for _, item := range items {
		if err := writer.Write(rowFor(item, opt).values()); err != nil {
			return []byte{}, 0
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return []byte{}, 0
	}
	return []byte(builder.String()), len(items)
}

// exportJSON writes the analysis rows plus the export report in the body, as
// §4.1 requires for the json format.
func exportJSON(items []preparedItem, opt Options, report *Report) []byte {
	rows := make([]exportRow, 0, len(items))
	for _, item := range items {
		rows = append(rows, rowFor(item, opt))
	}
	report.Exported = len(rows)
	body := struct {
		Exported  int         `json:"exported"`
		Skipped   []Skip      `json:"skipped"`
		Truncated int         `json:"truncated,omitempty"`
		Items     []exportRow `json:"items"`
	}{
		Exported:  len(rows),
		Skipped:   report.Skipped,
		Truncated: report.Truncated,
		Items:     rows,
	}
	encoded, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return []byte("{}\n")
	}
	return append(encoded, '\n')
}

// rowFor renders one analysis row. These formats describe the node itself
// rather than a client configuration, so every node produces a row.
func rowFor(item preparedItem, opt Options) exportRow {
	row := exportRow{
		Name:           item.Name,
		NodeHash:       item.Hash,
		Engine:         item.Doc.Engine,
		Protocol:       normalizeExportProtocol(item.Doc.Engine, item.Doc.Type),
		ProtocolDetail: protocolDetail(item),
		Subscription:   item.Subscription,
		Colo:           strings.ToUpper(strings.TrimSpace(item.Region)),
		LatencyMs:      latencyString(item.LatencyMs),
		Healthy:        boolString(item.Healthy),
	}

	intel := item.Intel
	if !opt.IncludeIntel || intel == nil {
		return row
	}
	if egress := strings.TrimSpace(intel.IP); egress != "" {
		if addr, err := netip.ParseAddr(egress); err == nil {
			addr = addr.Unmap()
			if addr.Is4() {
				row.EgressIPv4 = addr.String()
			} else {
				row.EgressIPv6 = addr.String()
			}
		} else {
			row.EgressIPv4 = egress
		}
	}
	if evidence := intel.Evidence; evidence != nil {
		row.Country = strings.ToUpper(strings.TrimSpace(evidence.CountryCode))
		row.City = strings.TrimSpace(evidence.City)
		row.ASN = strings.TrimSpace(evidence.ASN)
		row.ASOrg = strings.TrimSpace(evidence.Organization)
	}
	row.IPType, row.Native, row.PurityScore, row.PurityBand, row.Verdict, row.Confidence, row.Flags = intelCSVValues(item.Item, opt)
	row.Checks = renderChecks(intel)
	row.AssessedAt = renderAssessedAt(intel)
	return row
}

// latencyString renders an optional measured latency.
func latencyString(ms float64) string {
	if ms <= 0 {
		return ""
	}
	return strconv.FormatFloat(ms, 'f', -1, 64)
}

// normalizeExportProtocol mirrors the node package's protocol naming so the
// analysis formats label a protocol the same way the API does.
func normalizeExportProtocol(engine string, typeName string) string {
	name := strings.ToLower(strings.TrimSpace(typeName))
	if engine != node.EngineMihomo {
		return name
	}
	switch name {
	case "ss":
		return "shadowsocks"
	case "socks5":
		return "socks"
	default:
		return name
	}
}

// protocolDetail renders the display protocol of a node document.
func protocolDetail(item preparedItem) string {
	base := normalizeExportProtocol(item.Doc.Engine, item.Doc.Type)
	if base == "" {
		return ""
	}
	if item.Doc.Chain {
		if len(item.Doc.Deps) > 0 {
			if depType, err := node.TypeOfObject(item.Doc.Deps[0]); err == nil && depType != "" {
				return base + "+" + strings.ToLower(depType)
			}
		}
		return base
	}
	detail := base
	probe := item.Doc.Main
	if len(probe) == 0 {
		probe = item.Doc.Proxy
	}
	object := mustObject(probe)
	if object == nil {
		return detail
	}
	if transport := normalizeExportTransport(mapString(mapObject(object, "transport"), "type")); transport != "" {
		detail += "+" + transport
	}
	if tls, ok := parseClashTLS(object); ok && tls.Reality {
		detail += "+reality"
	}
	return detail
}

// mustObject decodes a JSON object, returning nil when it is not one.
func mustObject(raw json.RawMessage) map[string]any {
	object, err := objectMap(raw)
	if err != nil {
		return nil
	}
	return object
}

// normalizeExportTransport maps a sing-box transport name onto the share-link
// spelling used in UI details.
func normalizeExportTransport(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "tcp":
		return ""
	case "splithttp":
		return "xhttp"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

// renderChecks flattens the per-source detection results into one column, so a
// CSV reader can see which source reported what without extra files. Only the
// provider, its state and its headline facts are rendered; no credential can
// appear here.
func renderChecks(intel *quality.Summary) string {
	if intel == nil || len(intel.Sources) == 0 {
		return ""
	}
	parts := make([]string, 0, len(intel.Sources))
	for _, source := range intel.Sources {
		provider := strings.TrimSpace(source.Provider)
		if provider == "" {
			continue
		}
		facts := []string{provider, source.State}
		if evidence := source.Evidence; evidence != nil {
			if evidence.IPType != "" {
				facts = append(facts, "ip_type="+evidence.IPType)
			}
			if evidence.Grade != "" {
				facts = append(facts, "grade="+evidence.Grade)
			}
			if evidence.RiskScore != nil {
				facts = append(facts, "risk="+strconv.Itoa(*evidence.RiskScore))
			}
			if evidence.AbuseConfidence != nil {
				facts = append(facts, "abuse="+strconv.Itoa(*evidence.AbuseConfidence))
			}
			if evidence.SourceConfidence != nil {
				facts = append(facts, "confidence="+strconv.Itoa(*evidence.SourceConfidence))
			}
		}
		parts = append(parts, strings.Join(facts, ":"))
	}
	return strings.Join(parts, " | ")
}

// renderAssessedAt renders the newest observation time behind the assessment.
func renderAssessedAt(intel *quality.Summary) string {
	if intel == nil {
		return ""
	}
	latest := ""
	for _, source := range intel.Sources {
		if source.Evidence == nil || source.Evidence.ObservedAt.IsZero() {
			continue
		}
		observed := source.Evidence.ObservedAt.UTC().Format("2006-01-02T15:04:05Z")
		if observed > latest {
			latest = observed
		}
	}
	return latest
}
