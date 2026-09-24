package api

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"prism/internal/intel"
	"prism/internal/service"
)

// WP10 §4 node filters. The live query parameters and the stored export filter
// JSON are validated by these helpers, so a saved export profile and a live
// query accept exactly the same values and can never disagree about a node.
//
// Bounds (R4): every list input is bounded and an over-limit value is rejected
// with INVALID_ARGUMENT instead of being truncated silently.

const (
	// MaxNodeCheckFilters is how many repeatable `check=<id>:<outcome>` filters
	// one node query may carry.
	MaxNodeCheckFilters = 20
	// MaxNodeVerdicts is how many comma-separated values `verdict` may carry.
	MaxNodeVerdicts = 8
	// maxNodeCheckIDBytes bounds one check id.
	maxNodeCheckIDBytes = 64
	// maxNodeCountryBytes bounds the `country` filter.
	maxNodeCountryBytes = 4
)

// nodeIntelFilterValues is the raw text form of the §4 intel filters. Both the
// query parameters and the stored export filter are projected onto it before
// validation.
type nodeIntelFilterValues struct {
	PurityMin     string
	PurityMax     string
	Verdict       string
	ConfidenceMin string
	Native        *bool
	ASN           string
	Country       string
	Checks        []string
}

// apply validates every value and writes the result onto filters.
func (v nodeIntelFilterValues) apply(filters *service.NodeFilters) error {
	if filters == nil {
		return fmt.Errorf("filters: required")
	}
	if raw := strings.TrimSpace(v.PurityMin); raw != "" {
		value, err := parsePurityBound(raw, "purity_min")
		if err != nil {
			return err
		}
		filters.PurityMin = value
	}
	if raw := strings.TrimSpace(v.PurityMax); raw != "" {
		value, err := parsePurityBound(raw, "purity_max")
		if err != nil {
			return err
		}
		filters.PurityMax = value
	}
	if raw := strings.TrimSpace(v.Verdict); raw != "" {
		values, err := parseVerdictFilter(raw)
		if err != nil {
			return err
		}
		filters.Verdicts = values
	}
	if raw := strings.TrimSpace(v.ConfidenceMin); raw != "" {
		value, err := parseConfidenceMin(raw)
		if err != nil {
			return err
		}
		filters.ConfidenceMin = value
	}
	if v.Native != nil {
		value := *v.Native
		filters.Native = &value
	}
	if raw := strings.TrimSpace(v.ASN); raw != "" {
		value, err := parseASNFilter(raw)
		if err != nil {
			return err
		}
		filters.ASN = value
	}
	if raw := strings.TrimSpace(v.Country); raw != "" {
		value, err := parseCountryFilter(raw)
		if err != nil {
			return err
		}
		filters.Country = value
	}
	if len(v.Checks) > 0 {
		checks, err := parseCheckFilters(v.Checks)
		if err != nil {
			return err
		}
		filters.Checks = checks
	}
	return nil
}

// parsePurityBound parses `purity_min`/`purity_max` (0..100).
func parsePurityBound(raw, field string) (*int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || value > 100 {
		return nil, fmt.Errorf("%s: must be an integer in 0..100", field)
	}
	return &value, nil
}

// parseVerdictFilter parses the comma-separated `verdict` filter. Duplicates
// collapse; unknown verdicts and over-limit lists are rejected.
func parseVerdictFilter(raw string) ([]string, error) {
	allowed := intel.VerdictValues()
	out := make([]string, 0, len(allowed))
	for _, part := range strings.Split(raw, ",") {
		value := strings.ToLower(strings.TrimSpace(part))
		if value == "" {
			continue
		}
		if !slices.Contains(allowed, value) {
			return nil, fmt.Errorf("verdict: unsupported value %q", value)
		}
		if !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	if len(out) > MaxNodeVerdicts {
		return nil, fmt.Errorf("verdict: at most %d values", MaxNodeVerdicts)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("verdict: at least one value is required")
	}
	return out, nil
}

// parseConfidenceMin parses `confidence_min`. "none" is not a minimum: it would
// match every node including the unassessed ones.
func parseConfidenceMin(raw string) (*string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "low", "medium", "high":
		return &value, nil
	default:
		return nil, fmt.Errorf("confidence_min: must be one of low, medium, high")
	}
}

// parseASNFilter parses `asn`.
func parseASNFilter(raw string) (*int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > math.MaxUint32 {
		return nil, fmt.Errorf("asn: must be a positive integer")
	}
	return &value, nil
}

// parseCountryFilter parses `country`. The value is normalised to upper case so
// it matches the stored ISO code whatever case the caller used.
func parseCountryFilter(raw string) (*string, error) {
	value := strings.ToUpper(strings.TrimSpace(raw))
	if value == "" || len(value) > maxNodeCountryBytes {
		return nil, fmt.Errorf("country: must be an ISO country code")
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		return nil, fmt.Errorf("country: must be an ISO country code")
	}
	return &value, nil
}

// parseCheckFilters parses the repeatable `check=<id>:<outcome>` filter. The
// list itself is bounded by MaxNodeCheckFilters.
func parseCheckFilters(raw []string) ([]service.NodeCheckFilter, error) {
	if len(raw) > MaxNodeCheckFilters {
		return nil, fmt.Errorf("check: at most %d filters", MaxNodeCheckFilters)
	}
	out := make([]service.NodeCheckFilter, 0, len(raw))
	for _, entry := range raw {
		check, err := parseCheckFilter(entry)
		if err != nil {
			return nil, err
		}
		out = append(out, check)
	}
	return out, nil
}

// parseCheckFilter parses one `check=<id>:<outcome>` value.
func parseCheckFilter(raw string) (service.NodeCheckFilter, error) {
	id, outcome, ok := strings.Cut(strings.TrimSpace(raw), ":")
	if !ok {
		return service.NodeCheckFilter{}, fmt.Errorf("check: must be <id>:<outcome>")
	}
	id = strings.ToLower(strings.TrimSpace(id))
	outcome = strings.ToLower(strings.TrimSpace(outcome))
	if id == "" || len(id) > maxNodeCheckIDBytes || !validCheckID(id) {
		return service.NodeCheckFilter{}, fmt.Errorf("check: invalid check id")
	}
	if !slices.Contains(intel.OutcomeValues(), outcome) {
		return service.NodeCheckFilter{}, fmt.Errorf("check: unsupported outcome %q", outcome)
	}
	return service.NodeCheckFilter{ID: id, Outcome: outcome}, nil
}

// validCheckID accepts the lowercase identifiers checks are registered under.
func validCheckID(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
