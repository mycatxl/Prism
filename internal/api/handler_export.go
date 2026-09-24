package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"prism/internal/export"
	"prism/internal/model"
	"prism/internal/service"
	"prism/internal/state"
)

// Export API (WP11 §4.1 and §4.2). Everything here is admin-authenticated
// (rule R7); the public subscription entrypoint lives in handler_subscription.go.

// exportProfileFilter is the stored/queried node filter of an export. It
// mirrors the query parameters accepted by GET /api/v1/nodes so one filter
// vocabulary covers both the node list and every export surface.
//
// The WP10 §4 intel filters are part of it: a saved export profile filters a
// node exactly the way the live query does.
type exportProfileFilter struct {
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

// toNodeFilters validates the stored filter and converts it. An invalid stored
// value is a CONFLICT rather than a silent ignore: a subscription that quietly
// exports the whole pool would be worse than an error.
func (f exportProfileFilter) toNodeFilters() (service.NodeFilters, error) {
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
	// WP10 §4 intel filters. They share the node list validators, so a stored
	// profile and a live query accept exactly the same values.
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
func exportFilterFromQuery(r *http.Request) (exportProfileFilter, error) {
	q := r.URL.Query()
	filter := exportProfileFilter{
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
	Filter       exportProfileFilter
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

// maxExportNameTemplateBytes bounds the stored template.
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

// --- export profiles (§4.2) ---

// exportProfileRequest is the POST/PATCH body of an export profile.
type exportProfileRequest struct {
	Name         *string              `json:"name,omitempty"`
	Format       *string              `json:"format,omitempty"`
	PlatformID   *string              `json:"platform_id,omitempty"`
	Filter       *exportProfileFilter `json:"filter,omitempty"`
	NameTemplate *string              `json:"name_template,omitempty"`
	Enabled      *bool                `json:"enabled,omitempty"`
}

// exportProfileResponse is the wire shape of an export profile. The plaintext
// token is never part of it: URL carries it once, and only when the caller just
// created or rotated it. The SHA-256 digest never leaves the server.
type exportProfileResponse struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Format         string `json:"format"`
	PlatformID     string `json:"platform_id,omitempty"`
	Filter         any    `json:"filter"`
	NameTemplate   string `json:"name_template"`
	Enabled        bool   `json:"enabled"`
	LastAccessAtNs int64  `json:"last_access_at_ns"`
	AccessCount    int64  `json:"access_count"`
	CreatedAtNs    int64  `json:"created_at_ns"`
	UpdatedAtNs    int64  `json:"updated_at_ns"`
	// URL is present only in the create and rotate-token responses.
	URL string `json:"url,omitempty"`
}

func exportProfileToResponse(profile model.ExportProfile) exportProfileResponse {
	var filter any
	if strings.TrimSpace(profile.FilterJSON) == "" {
		filter = map[string]any{}
	} else if err := json.Unmarshal([]byte(profile.FilterJSON), &filter); err != nil {
		filter = map[string]any{}
	}
	return exportProfileResponse{
		ID:             profile.ID,
		Name:           profile.Name,
		Format:         profile.Format,
		PlatformID:     profile.PlatformID,
		Filter:         filter,
		NameTemplate:   profile.NameTemplate,
		Enabled:        profile.Enabled,
		LastAccessAtNs: profile.LastAccessAtNs,
		AccessCount:    profile.AccessCount,
		CreatedAtNs:    profile.CreatedAtNs,
		UpdatedAtNs:    profile.UpdatedAtNs,
	}
}

// exportProfileSubscriptionURL renders the one-time subscription URL. The base
// comes from the request, so a deployment behind a reverse proxy works without
// extra configuration.
func exportProfileSubscriptionURL(r *http.Request, token string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	return scheme + "://" + host + "/sub/" + token
}

// validateExportProfileRequest validates the mutable fields.
func validateExportProfileRequest(req exportProfileRequest, requireName bool) error {
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len(name) > 128 {
			return fmt.Errorf("name: must be 1..128 characters")
		}
	} else if requireName {
		return fmt.Errorf("name: required")
	}
	if req.Format != nil {
		if !export.ValidFormat(*req.Format) {
			return fmt.Errorf("format: must be one of: %s", strings.Join(export.Formats(), ", "))
		}
	} else if requireName {
		return fmt.Errorf("format: required")
	}
	if req.NameTemplate != nil && len(*req.NameTemplate) > maxExportNameTemplateBytes {
		return fmt.Errorf("name_template: too long")
	}
	if req.PlatformID != nil {
		trimmed := strings.TrimSpace(*req.PlatformID)
		if trimmed != "" && !ValidateUUID(trimmed) {
			return fmt.Errorf("platform_id: must be a UUID")
		}
	}
	if req.Filter != nil {
		if _, err := req.Filter.toNodeFilters(); err != nil {
			return fmt.Errorf("filter: %w", err)
		}
	}
	return nil
}

// HandleListExportProfiles returns a handler for GET /api/v1/export-profiles.
func HandleListExportProfiles(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cp == nil || cp.Engine == nil {
			writeServiceError(w, exportInternalError("list export profiles", fmt.Errorf("service not initialized")))
			return
		}
		profiles, err := cp.Engine.ListExportProfiles()
		if err != nil {
			writeServiceError(w, exportInternalError("list export profiles", err))
			return
		}
		items := make([]exportProfileResponse, 0, len(profiles))
		for _, profile := range profiles {
			items = append(items, exportProfileToResponse(profile))
		}
		pg, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		WritePage(w, http.StatusOK, items, pg)
	}
}

// HandleGetExportProfile returns a handler for GET /api/v1/export-profiles/{id}.
func HandleGetExportProfile(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		profile, ok := loadExportProfile(w, cp, PathParam(r, "id"))
		if !ok {
			return
		}
		WriteJSON(w, http.StatusOK, exportProfileToResponse(*profile))
	}
}

// HandleCreateExportProfile returns a handler for POST /api/v1/export-profiles.
// The plaintext token is returned exactly once, in `url`.
func HandleCreateExportProfile(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cp == nil || cp.Engine == nil {
			writeServiceError(w, exportInternalError("create export profile", fmt.Errorf("service not initialized")))
			return
		}
		var req exportProfileRequest
		if err := DecodeBody(r, &req); err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		if err := validateExportProfileRequest(req, true); err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}

		token, err := NewExportProfileToken()
		if err != nil {
			writeServiceError(w, exportInternalError("create export profile", err))
			return
		}
		now := exportNowNs()
		profile := model.ExportProfile{
			ID:           newExportProfileID(),
			Name:         strings.TrimSpace(*req.Name),
			Format:       *req.Format,
			TokenSHA256:  ExportProfileTokenSHA256(token),
			Enabled:      req.Enabled == nil || *req.Enabled,
			FilterJSON:   "{}",
			NameTemplate: strings.TrimSpace(derefString(req.NameTemplate)),
			CreatedAtNs:  now,
			UpdatedAtNs:  now,
		}
		if req.PlatformID != nil {
			profile.PlatformID = strings.TrimSpace(*req.PlatformID)
		}
		if req.Filter != nil {
			encoded, err := json.Marshal(req.Filter)
			if err != nil {
				writeInvalidArgument(w, "filter: "+err.Error())
				return
			}
			profile.FilterJSON = string(encoded)
		}
		if err := cp.Engine.UpsertExportProfile(profile); err != nil {
			writeServiceError(w, exportProfileStateError("create export profile", err))
			return
		}

		response := exportProfileToResponse(profile)
		response.URL = exportProfileSubscriptionURL(r, token)
		WriteJSON(w, http.StatusCreated, response)
	}
}

// HandleUpdateExportProfile returns a handler for PATCH /api/v1/export-profiles/{id}.
func HandleUpdateExportProfile(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		profile, ok := loadExportProfile(w, cp, PathParam(r, "id"))
		if !ok {
			return
		}
		var req exportProfileRequest
		if err := DecodeBody(r, &req); err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		if err := validateExportProfileRequest(req, false); err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		next := *profile
		if req.Name != nil {
			next.Name = strings.TrimSpace(*req.Name)
		}
		if req.Format != nil {
			next.Format = *req.Format
		}
		if req.PlatformID != nil {
			next.PlatformID = strings.TrimSpace(*req.PlatformID)
		}
		if req.NameTemplate != nil {
			next.NameTemplate = strings.TrimSpace(*req.NameTemplate)
		}
		if req.Enabled != nil {
			next.Enabled = *req.Enabled
		}
		if req.Filter != nil {
			encoded, err := json.Marshal(req.Filter)
			if err != nil {
				writeInvalidArgument(w, "filter: "+err.Error())
				return
			}
			next.FilterJSON = string(encoded)
		}
		next.UpdatedAtNs = exportNowNs()
		if err := cp.Engine.UpsertExportProfile(next); err != nil {
			writeServiceError(w, exportProfileStateError("update export profile", err))
			return
		}
		WriteJSON(w, http.StatusOK, exportProfileToResponse(next))
	}
}

// HandleDeleteExportProfile returns a handler for DELETE /api/v1/export-profiles/{id}.
func HandleDeleteExportProfile(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cp == nil || cp.Engine == nil {
			writeServiceError(w, exportInternalError("delete export profile", fmt.Errorf("service not initialized")))
			return
		}
		if err := cp.Engine.DeleteExportProfile(PathParam(r, "id")); err != nil {
			writeServiceError(w, exportProfileStateError("delete export profile", err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleRotateExportProfileToken returns a handler for
// POST /api/v1/export-profiles/{id}/actions/rotate-token. The new plaintext
// token is returned exactly once, in `url`; the previous token stops working
// immediately because only the digest is stored.
func HandleRotateExportProfileToken(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		profile, ok := loadExportProfile(w, cp, PathParam(r, "id"))
		if !ok {
			return
		}
		token, err := NewExportProfileToken()
		if err != nil {
			writeServiceError(w, exportInternalError("rotate export profile token", err))
			return
		}
		next := *profile
		next.TokenSHA256 = ExportProfileTokenSHA256(token)
		next.UpdatedAtNs = exportNowNs()
		if err := cp.Engine.UpsertExportProfile(next); err != nil {
			writeServiceError(w, exportProfileStateError("rotate export profile token", err))
			return
		}
		response := exportProfileToResponse(next)
		response.URL = exportProfileSubscriptionURL(r, token)
		WriteJSON(w, http.StatusOK, response)
	}
}

// loadExportProfile resolves {id} or writes the error response.
func loadExportProfile(w http.ResponseWriter, cp *service.ControlPlaneService, id string) (*model.ExportProfile, bool) {
	if cp == nil || cp.Engine == nil {
		writeServiceError(w, exportInternalError("export profile", fmt.Errorf("service not initialized")))
		return nil, false
	}
	if id == "" || len(id) > 64 {
		writeInvalidArgument(w, "id: invalid")
		return nil, false
	}
	profile, err := cp.Engine.GetExportProfile(id)
	if err != nil {
		writeServiceError(w, exportProfileStateError("get export profile", err))
		return nil, false
	}
	return profile, true
}

// exportProfileStateError maps repository errors onto the API error codes.
func exportProfileStateError(message string, err error) error {
	if errors.Is(err, state.ErrNotFound) {
		return &service.ServiceError{Code: "NOT_FOUND", Message: "export profile not found", Err: err}
	}
	if errors.Is(err, state.ErrConflict) {
		return &service.ServiceError{Code: "CONFLICT", Message: "export profile name or token already exists", Err: err}
	}
	return exportInternalError(message, err)
}
func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// exportNowNs is the UTC Unix-nanosecond clock used for profile timestamps
// (rule R8).
func exportNowNs() int64 { return time.Now().UTC().UnixNano() }

// newExportProfileID generates a fresh export profile identifier.
func newExportProfileID() string { return uuid.NewString() }

// exportInternalError renders an INTERNAL service error.
func exportInternalError(message string, err error) error {
	return &service.ServiceError{Code: "INTERNAL", Message: message, Err: err}
}
