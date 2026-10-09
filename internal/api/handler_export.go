package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"prism/internal/export"
	"prism/internal/service"
)

// Node export API (WP11 §4.1). Admin-authenticated (rule R7): the admin
// downloads a rendered file of the selected nodes; there is no public
// subscription surface.

// exportNodeFilter is the stored/queried node filter of an export. It
// mirrors the query parameters accepted by GET /api/v1/nodes so one filter
// vocabulary covers both the node list and every export surface.
//
// The WP10 §4 intel filters are part of it, so an export filters a node exactly
// the way the live node list does.
type exportNodeFilter struct {
	IPType         string   `json:"ip_type,omitempty"`
	QualityState   string   `json:"quality_state,omitempty"`
	RiskGrade      string   `json:"risk_grade,omitempty"`
	PurityBand     string   `json:"purity_band,omitempty"`
	Protocol       string   `json:"protocol,omitempty"`
	PlatformID     string   `json:"platform_id,omitempty"`
	SubscriptionID string   `json:"subscription_id,omitempty"`
	Enabled        *bool    `json:"enabled,omitempty"`
	Region         string   `json:"region,omitempty"`
	CircuitOpen    *bool    `json:"circuit_open,omitempty"`
	HasOutbound    *bool    `json:"has_outbound,omitempty"`
	EgressIP       string   `json:"egress_ip,omitempty"`
	TagKeyword     string   `json:"tag_keyword,omitempty"`
	ProbedSince    string   `json:"probed_since,omitempty"`
	PurityMin      *int     `json:"purity_min,omitempty"`
	PurityMax      *int     `json:"purity_max,omitempty"`
	Verdict        string   `json:"verdict,omitempty"`
	ConfidenceMin  string   `json:"confidence_min,omitempty"`
	Native         *bool    `json:"native,omitempty"`
	ASN            *int     `json:"asn,omitempty"`
	Country        string   `json:"country,omitempty"`
	Checks         []string `json:"checks,omitempty"`
}

// toNodeFilters validates the filter and converts it. An invalid value is an
// error rather than a silent ignore: an export that quietly covers the whole
// pool would be worse than an error.
func (f exportNodeFilter) toNodeFilters() (service.NodeFilters, error) {
	var filters service.NodeFilters

	assign := func(raw string, allowed []string, target **string, field string) error {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil
		}
		if len(allowed) > 0 {
			found := false
			for _, candidate := range allowed {
				if candidate == raw {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%s: unsupported value", field)
			}
		}
		value := raw
		*target = &value
		return nil
	}

	ipTypes := []string{"unknown", "residential", "non_residential", "datacenter", "business", "wireless", "mobile", "conflicting"}
	qualityStates := []string{"unobserved", "pending", "partial", "valid", "stale", "conflicting", "unsupported"}
	riskGrades := []string{"unknown", "low", "moderate", "high", "severe", "review"}
	purityBands := []string{"unknown", "excellent", "clean", "fair", "mixed", "poor", "review"}

	for _, item := range []struct {
		raw     string
		allowed []string
		field   string
		target  **string
	}{
		{f.IPType, ipTypes, "ip_type", &filters.IPType},
		{f.QualityState, qualityStates, "quality_state", &filters.QualityState},
		{f.RiskGrade, riskGrades, "risk_grade", &filters.RiskGrade},
		{f.PurityBand, purityBands, "purity_band", &filters.PurityBand},
		{f.PlatformID, nil, "platform_id", &filters.PlatformID},
		{f.SubscriptionID, nil, "subscription_id", &filters.SubscriptionID},
		{f.Region, nil, "region", &filters.Region},
		{f.EgressIP, nil, "egress_ip", &filters.EgressIP},
		{f.TagKeyword, nil, "tag_keyword", &filters.TagKeyword},
	} {
		if err := assign(item.raw, item.allowed, item.target, item.field); err != nil {
			return filters, err
		}
	}
	if f.Protocol != "" && !validProtocolFilter(f.Protocol) {
		return filters, fmt.Errorf("protocol: unsupported value")
	}
	if err := assign(f.Protocol, nil, &filters.Protocol, "protocol"); err != nil {
		return filters, err
	}

	filters.Enabled = f.Enabled
	filters.CircuitOpen = f.CircuitOpen
	filters.HasOutbound = f.HasOutbound
	if strings.TrimSpace(f.ProbedSince) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, f.ProbedSince)
		if err != nil {
			return filters, fmt.Errorf("probed_since: invalid RFC3339 timestamp")
		}
		parsed = parsed.UTC()
		filters.ProbedSince = &parsed
	}
	// WP10 §4 intel filters. They share the node list validators, so an export
	// and a live query accept exactly the same values.
	intelValues := nodeIntelFilterValues{
		Verdict:       f.Verdict,
		ConfidenceMin: f.ConfidenceMin,
		Native:        f.Native,
		Country:       f.Country,
		Checks:        f.Checks,
	}
	if f.PurityMin != nil {
		intelValues.PurityMin = strconv.Itoa(*f.PurityMin)
	}
	if f.PurityMax != nil {
		intelValues.PurityMax = strconv.Itoa(*f.PurityMax)
	}
	if f.ASN != nil {
		intelValues.ASN = strconv.Itoa(*f.ASN)
	}
	if err := intelValues.apply(&filters); err != nil {
		return filters, err
	}
	return filters, nil
}

// exportFilterFromQuery reads the same filter vocabulary from URL query
// parameters. Unknown values are rejected, never ignored.
func exportFilterFromQuery(r *http.Request) (exportNodeFilter, error) {
	q := r.URL.Query()
	filter := exportNodeFilter{
		IPType:         q.Get("ip_type"),
		QualityState:   q.Get("quality_state"),
		RiskGrade:      q.Get("risk_grade"),
		PurityBand:     q.Get("purity_band"),
		Protocol:       q.Get("protocol"),
		PlatformID:     q.Get("platform_id"),
		SubscriptionID: q.Get("subscription_id"),
		Region:         q.Get("region"),
		EgressIP:       q.Get("egress_ip"),
		TagKeyword:     strings.TrimSpace(q.Get("tag_keyword")),
		ProbedSince:    q.Get("probed_since"),
		Verdict:        q.Get("verdict"),
		ConfidenceMin:  q.Get("confidence_min"),
		Country:        q.Get("country"),
		Checks:         q["check"],
	}
	// The WP10 §4 intel filters are read in their text form and converted by
	// the same validators the node list uses.
	intelValues := nodeIntelFilterValues{
		PurityMin: q.Get("purity_min"),
		PurityMax: q.Get("purity_max"),
	}
	var err error
	if intelValues.PurityMin != "" {
		value, parseErr := parsePurityBound(intelValues.PurityMin, "purity_min")
		if parseErr != nil {
			return filter, parseErr
		}
		filter.PurityMin = value
	}
	if intelValues.PurityMax != "" {
		value, parseErr := parsePurityBound(intelValues.PurityMax, "purity_max")
		if parseErr != nil {
			return filter, parseErr
		}
		filter.PurityMax = value
	}
	if raw := strings.TrimSpace(q.Get("asn")); raw != "" {
		value, parseErr := parseASNFilter(raw)
		if parseErr != nil {
			return filter, parseErr
		}
		filter.ASN = value
	}
	if filter.Enabled, err = ParseBoolQuery(r, "enabled"); err != nil {
		return filter, err
	}
	if filter.CircuitOpen, err = ParseBoolQuery(r, "circuit_open"); err != nil {
		return filter, err
	}
	if filter.HasOutbound, err = ParseBoolQuery(r, "has_outbound"); err != nil {
		return filter, err
	}
	if filter.Native, err = ParseBoolQuery(r, "native"); err != nil {
		return filter, err
	}
	return filter, nil
}

// exportRequest is the resolved form of one export request.
type exportRequest struct {
	Format       string
	NameTemplate string
	Filter       exportNodeFilter
	HealthyOnly  bool
	Limit        int
}

// defaultExportLimit bounds one export when the caller does not say otherwise.
const defaultExportLimit = export.MaxItems

// parseExportLimit reads `limit`. The bound is a hard cap: a larger value is
// rejected with 400 rather than silently truncated, because the caller can
// always page with offset when a pool really has more than MaxItems nodes.
func parseExportLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return defaultExportLimit, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("limit: must be a non-negative integer")
	}
	if value > export.MaxItems {
		return 0, fmt.Errorf("limit: must be <= %d", export.MaxItems)
	}
	if value == 0 {
		return defaultExportLimit, nil
	}
	return value, nil
}

// parseExportOffset reads `offset` for the over-limit paging contract.
func parseExportOffset(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("offset"))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("offset: must be a non-negative integer")
	}
	return value, nil
}

// maxExportNameTemplateBytes bounds the name template.
const maxExportNameTemplateBytes = 256

// HandleExportNodes returns a handler for GET /api/v1/nodes/export (§4.1).
func HandleExportNodes(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		format := strings.TrimSpace(r.URL.Query().Get("format"))
		if !export.ValidFormat(format) {
			writeInvalidArgument(w, "format: must be one of "+strings.Join(export.Formats(), ", "))
			return
		}
		filter, err := exportFilterFromQuery(r)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		nameTemplate := r.URL.Query().Get("name_template")
		if len(nameTemplate) > maxExportNameTemplateBytes {
			writeInvalidArgument(w, "name_template: too long")
			return
		}
		healthyOnly, ok := parseBoolQueryOrWriteInvalid(w, r, "healthy_only")
		if !ok {
			return
		}
		limit, err := parseExportLimit(r)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		offset, err := parseExportOffset(r)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}

		body, contentType, report, err := runNodeExport(cp, exportRequest{
			Format:       format,
			NameTemplate: nameTemplate,
			Filter:       filter,
			HealthyOnly:  healthyOnly != nil && *healthyOnly,
			Limit:        limit,
		}, offset)
		if err != nil {
			writeServiceError(w, err)
			return
		}

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", "attachment; filename="+export.FileName(format))
		w.Header().Set("X-Prism-Export-Exported", strconv.Itoa(report.Exported))
		w.Header().Set("X-Prism-Export-Skipped", strconv.Itoa(report.SkipCount()))
		if report.Truncated > 0 {
			w.Header().Set("X-Prism-Export-Truncated", strconv.Itoa(report.Truncated))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

// runNodeExport selects the nodes, renders the items and calls export.Export.
// offset is the paging cursor: the selection is ordered by node hash and
// sliced to [offset, offset+limit), then the remainder is counted as truncated
// so the caller can see that more nodes exist.
func runNodeExport(cp *service.ControlPlaneService, req exportRequest, offset int) ([]byte, string, export.Report, error) {
	filters, err := req.Filter.toNodeFilters()
	if err != nil {
		return nil, "", export.Report{}, invalidArgumentError("filter: " + err.Error())
	}
	if cp == nil || cp.Pool == nil {
		return nil, "", export.Report{}, exportInternalError("export", fmt.Errorf("control plane is not initialized"))
	}

	summaries, err := cp.ListNodes(filters)
	if err != nil {
		return nil, "", export.Report{}, err
	}

	// Deterministic order: the pool iteration order is a map, so sort by hash.
	sortNodeSummaries(summaries, Sorting{SortBy: "created_at", SortOrder: "asc"})

	selected := summaries
	if offset > 0 {
		if offset >= len(selected) {
			selected = nil
		} else {
			selected = selected[offset:]
		}
	}
	truncated := 0
	if len(selected) > req.Limit {
		truncated = len(selected) - req.Limit
		selected = selected[:req.Limit]
	}

	items := make([]export.Item, 0, len(selected))
	for _, summary := range selected {
		if req.HealthyOnly && !summary.IsHealthyAndEnabled() {
			continue
		}
		item, ok := cp.ExportItem(summary)
		if !ok {
			continue
		}
		items = append(items, item)
	}

	options := export.Options{
		Format:       req.Format,
		NameTemplate: req.NameTemplate,
		IncludeIntel: true,
	}
	body, contentType, report, err := export.Export(items, req.Format, options)
	if err != nil {
		return nil, "", report, exportInternalError("export", err)
	}
	report.Truncated += truncated
	return body, contentType, report, nil
}

// exportInternalError renders an INTERNAL service error.
func exportInternalError(message string, err error) error {
	return &service.ServiceError{Code: "INTERNAL", Message: message, Err: err}
}
