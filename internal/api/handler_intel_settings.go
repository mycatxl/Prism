package api

import (
	"net/http"

	"prism/internal/intel/providers"
	"prism/internal/service"
)

// WP09 §4/§5.4: the settings surface of the intel data sources and the unlock
// checks. Both lists page with the shared limit/offset envelope (R7); no
// response ever carries a credential, only has_key (R6).

// HandleIntelListProviders implements GET /api/v1/intel/providers.
//
// Query: limit (default 50, at most 100000), offset. Over-limit values are
// rejected with 400 INVALID_ARGUMENT; the response carries the effective
// non-secret settings and today's budget/queue state of every data source.
func HandleIntelListProviders(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		pagination, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		statuses, err := cp.IntelProviderList(r.Context())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WritePage(w, http.StatusOK, statuses, pagination)
	}
}

// HandleIntelUpdateProvider implements PATCH /api/v1/intel/providers/{id}.
//
// Body: {"enabled":bool,"api_key":string|null,"daily_limit":int,"qps":number,
// "ttl":"24h","config":{...}}. api_key is write-only; "" clears it and null
// keeps it. The change is applied to the running registry (WP09 §4).
func HandleIntelUpdateProvider(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var patch providers.ProviderPatch
		if err := DecodeBody(r, &patch); err != nil {
			writeDecodeError(w, err)
			return
		}
		status, err := cp.IntelUpdateProvider(r.Context(), PathParam(r, "id"), patch)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, status)
	}
}

// HandleIntelResumeProvider implements
// POST /api/v1/intel/providers/{id}/actions/resume: it clears the paused flag
// and the 429 cooldown of one data source (WP09 §4).
func HandleIntelResumeProvider(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		status, err := cp.IntelResumeProvider(r.Context(), PathParam(r, "id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, status)
	}
}

// HandleIntelRefreshProvider implements
// POST /api/v1/intel/providers/{id}/actions/refresh: it downloads the offline
// databases of one data source now and reports the bounded outcome per file
// (WP09 §3). A data source without a downloadable database answers 409
// CONFLICT; an unconfigured database is a recorded skip, not an error.
func HandleIntelRefreshProvider(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		result, err := cp.IntelRefreshProvider(r.Context(), PathParam(r, "id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, result)
	}
}

// intelCheckListResponse is the paging envelope of the checks list. WritePage
// cannot carry the rule-load diagnostics, so the four documented fields are
// repeated here next to load_errors (R7).
type intelCheckListResponse struct {
	Items      []service.IntelCheckStatus    `json:"items"`
	Total      int                           `json:"total"`
	Limit      int                           `json:"limit"`
	Offset     int                           `json:"offset"`
	LoadErrors []service.IntelCheckLoadError `json:"load_errors"`
}

// HandleIntelListChecks implements GET /api/v1/intel/checks.
//
// Query: limit (default 50, at most 100000), offset. load_errors reports the
// user rule files under $PRISM_STATE_DIR/checks.d that failed to load, so a user
// can see why their rule did not appear.
func HandleIntelListChecks(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		pagination, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		result, err := cp.IntelChecks()
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, intelCheckListResponse{
			Items:      PaginateSlice(result.Items, pagination),
			Total:      len(result.Items),
			Limit:      pagination.Limit,
			Offset:     pagination.Offset,
			LoadErrors: result.LoadErrors,
		})
	}
}

// HandleIntelUpdateCheck implements PATCH /api/v1/intel/checks/{id}.
//
// Body: {"enabled":bool}. The toggle is stored as provider_id "check:<id>" in
// intel_provider_settings and is effective without a restart (WP09 §5.4).
func HandleIntelUpdateCheck(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var req service.IntelUpdateCheckRequest
		if err := DecodeBody(r, &req); err != nil {
			writeDecodeError(w, err)
			return
		}
		status, err := cp.IntelUpdateCheck(PathParam(r, "id"), req)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, status)
	}
}
