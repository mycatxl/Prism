package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"prism/internal/service"
)

// boolQuery reads an optional boolean query parameter. Only the spellings
// strconv accepts are taken; anything else is refused rather than silently read
// as false, because a mistyped filter that quietly widens a result is worse than
// an error message.
func boolQuery(r *http.Request, key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback, errors.New(key + ": must be true or false")
	}
	return value, nil
}

// HandleAdminListAlerts implements GET /api/v1/admin/alerts.
//
// limit, since_ns and unacknowledged are optional: with none of them the page
// opens on the newest alerts of every kind, which is what an operator wants
// before they know what they are looking for.
func HandleAdminListAlerts(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if cp == nil {
			WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "control plane not configured")
			return
		}
		limit, err := intQuery(r, "limit", 0)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		sinceNs, err := int64Query(r, "since_ns", 0)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		unacknowledged, err := boolQuery(r, "unacknowledged", false)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		items, svcErr := cp.ListAlerts(limit, sinceNs, unacknowledged)
		if svcErr != nil {
			writeServiceError(w, svcErr)
			return
		}
		writeTenantItems(w, items)
	}
}

// HandleAdminAcknowledgeAlert implements
// POST /api/v1/admin/alerts/{id}/actions/acknowledge.
//
// Acknowledging keeps the row: an incident that was handled is still a fact the
// next operator needs, and the detector uses "no open alert for this
// subscription" to decide whether a crossing was already reported.
func HandleAdminAcknowledgeAlert(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if cp == nil {
			WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "control plane not configured")
			return
		}
		view, err := cp.AcknowledgeAlert(PathParam(r, "id"), adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, view)
	}
}
