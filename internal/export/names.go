package export

import (
	"fmt"
	"strings"

	"prism/internal/quality"
)

// Name template rules (WP11 §3).
const (
	// maxNameLength is the hard cap of a rendered node name.
	maxNameLength = 64
	// maxNameVariants bounds the deduplication search per node so a large pool
	// of identically named nodes cannot turn name rendering into O(n²).
	maxNameVariants = 4096
)

// templateVariables lists the supported placeholders in a stable order.
func templateVariables() []string {
	return []string{
		"name", "flag", "country", "city", "asn", "org", "ip_type",
		"purity", "band", "verdict", "engine", "protocol", "latency", "index",
	}
}

// RenderNames renders one name per item with opt.NameTemplate, compresses
// whitespace, truncates to 64 characters and resolves duplicates by appending
// " #2", " #3", ... in input order. An item whose name cannot be made unique
// inside the bounded variant budget renders as an empty string; the caller
// records that as a skip.
func RenderNames(items []Item, opt Options) []string {
	template := strings.TrimSpace(opt.NameTemplate)
	if template == "" {
		template = DefaultNameTemplate
	}
	names := make([]string, len(items))
	if len(items) == 0 {
		return names
	}

	// Reserve the root names before assigning suffixed variants so that a node
	// literally called "<name> #2" is not stolen by the deduplication of
	// "<name>".
	bases := make([]string, len(items))
	taken := make(map[string]bool, len(items))
	for i, item := range items {
		bases[i] = clampName(renderTemplate(template, item, i, opt))
		if bases[i] == "" {
			continue
		}
		taken[bases[i]] = true
	}

	for i := range items {
		base := bases[i]
		if base == "" {
			continue
		}
		if !usedName(bases, i, base) {
			names[i] = base
			continue
		}
		names[i] = suffixedName(base, taken)
		if names[i] != "" {
			taken[names[i]] = true
		}
	}
	return names
}

// usedName reports whether base already names an earlier item, which means the
// current item needs a suffix.
func usedName(bases []string, index int, base string) bool {
	for i := 0; i < index; i++ {
		if bases[i] == base {
			return true
		}
	}
	return false
}

// suffixedName returns base+" #k" for the smallest k whose result is free and
// fits the name budget, or "" when the variant budget is exhausted.
func suffixedName(base string, taken map[string]bool) string {
	for k := 2; k <= maxNameVariants; k++ {
		suffix := fmt.Sprintf(" #%d", k)
		room := maxNameLength - len(suffix)
		candidate := base
		if len(candidate) > room {
			candidate = truncateName(candidate, room)
		}
		candidate += suffix
		if !taken[candidate] {
			return candidate
		}
	}
	return ""
}

// renderTemplate substitutes every known placeholder.
func renderTemplate(template string, item Item, index int, opt Options) string {
	vars := templateValues(item, index, opt)
	var builder strings.Builder
	builder.Grow(len(template) + 16)
	for i := 0; i < len(template); {
		if template[i] != '{' {
			builder.WriteByte(template[i])
			i++
			continue
		}
		end := strings.IndexByte(template[i:], '}')
		if end < 0 {
			builder.WriteString(template[i:])
			break
		}
		token := template[i+1 : i+end]
		value, known := vars[token]
		if !known {
			value = ""
		}
		builder.WriteString(value)
		i += end + 1
	}
	return builder.String()
}

// templateValues resolves every placeholder for one item.
func templateValues(item Item, index int, opt Options) map[string]string {
	values := make(map[string]string, len(templateVariables()))
	values["name"] = item.Name
	values["index"] = fmt.Sprintf("%d", index+1)
	if doc, err := ParseNodeDoc(item.RawOptions); err == nil {
		values["engine"] = doc.Engine
		values["protocol"] = doc.Type
	}

	intel := item.Intel
	if !opt.IncludeIntel {
		intel = nil
	}
	if intel == nil {
		return values
	}
	if intel.Evidence != nil {
		values["country"] = strings.ToUpper(strings.TrimSpace(intel.Evidence.CountryCode))
		values["flag"] = countryFlag(intel.Evidence.CountryCode)
		values["city"] = strings.TrimSpace(intel.Evidence.City)
		values["asn"] = strings.TrimSpace(intel.Evidence.ASN)
		values["org"] = strings.TrimSpace(intel.Evidence.Organization)
	}
	if assessment := intel.Assessment; assessment != nil {
		if assessment.PurityScore != nil {
			values["purity"] = fmt.Sprintf("%d", *assessment.PurityScore)
		}
		values["band"] = assessment.PurityBand
		values["verdict"] = assessment.Verdict
		if values["ip_type"] == "" {
			values["ip_type"] = assessment.NetworkType
		}
	}
	return values
}

// countryFlag renders a two-letter country code as its regional-indicator flag
// emoji, or "" when the code is unusable.
func countryFlag(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 {
		return ""
	}
	first, second := rune(code[0]), rune(code[1])
	if first < 'A' || first > 'Z' || second < 'A' || second > 'Z' {
		return ""
	}
	const base = rune(0x1F1E6)
	return string([]rune{base + (first - 'A'), base + (second - 'A')})
}

// clampName compresses runs of whitespace and truncates to maxNameLength.
func clampName(raw string) string {
	return strings.TrimSpace(truncateName(compressSpace(raw), maxNameLength))
}

// compressSpace collapses every whitespace run into a single space.
func compressSpace(raw string) string {
	var builder strings.Builder
	builder.Grow(len(raw))
	space := false
	for _, r := range raw {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			space = true
			continue
		}
		if space && builder.Len() > 0 {
			builder.WriteByte(' ')
		}
		space = false
		builder.WriteRune(r)
	}
	return builder.String()
}

// truncateName cuts a name to at most limit bytes on a rune boundary.
func truncateName(raw string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(raw) <= limit {
		return raw
	}
	cut := limit
	for cut > 0 && !isRuneStart(raw[cut]) {
		cut--
	}
	return raw[:cut]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// intelCSVValues renders the intel-derived CSV columns shared by the csv and
// json formats. Every field is a plain string so the two formats agree.
func intelCSVValues(item Item, opt Options) (ipType, native, purity, band, verdict, confidence, flags string) {
	intel := item.Intel
	if !opt.IncludeIntel || intel == nil {
		return "", "", "", "", "", "", ""
	}
	if intel.Evidence != nil {
		if intel.Evidence.Native != nil {
			native = boolString(*intel.Evidence.Native)
		}
	}
	if intel.Assessment != nil {
		ipType = intel.Assessment.NetworkType
		if intel.Assessment.PurityScore != nil {
			purity = fmt.Sprintf("%d", *intel.Assessment.PurityScore)
		}
		band = intel.Assessment.PurityBand
		verdict = intel.Assessment.Verdict
		flags = strings.Join(intel.Assessment.Reasons, "|")
	}
	if intel.Evidence != nil && ipType == "" {
		ipType = intel.Evidence.IPType
	}
	confidence = purityConfidence(intel)
	return ipType, native, purity, band, verdict, confidence, flags
}

// purityConfidence renders the highest source confidence behind the
// assessment, or "" when no source reported one.
func purityConfidence(summary *quality.Summary) string {
	if summary == nil {
		return ""
	}
	best := -1
	for _, source := range summary.Sources {
		if source.Evidence == nil || source.Evidence.SourceConfidence == nil {
			continue
		}
		if value := *source.Evidence.SourceConfidence; value > best {
			best = value
		}
	}
	if best < 0 {
		return ""
	}
	return fmt.Sprintf("%d", best)
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
