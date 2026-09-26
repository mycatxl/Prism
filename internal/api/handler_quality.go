package api

import (
	"net/http"

	"prism/internal/service"
)

// The legacy /api/v1/quality/* handlers (WP08 §9).
//
// Every handler is a thin alias over the intel-backed ControlPlaneService: the
// in-memory internal/inspection manager is gone (WP08 §8), so there is no
// provider error type to unwrap here any more. Provider failures travel inside
// the projected summary (summary.task.error_code) and are rendered by the WebUI,
// while the request itself succeeded.

// HandleReviewIPPure returns a handler for
// POST /api/v1/nodes/{hash}/actions/review-ippure.
//
// The review waits up to 15 s for the IPPure evidence of the node's egress IP
// (WP08 §9). When the evidence is ready the response is 200 with it; otherwise
// the job is still running and the response is 202 with its id, so the caller
// can poll /api/v1/intel/jobs/{id}.
func HandleReviewIPPure(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		result, err := cp.ReviewIPPure(r.Context(), PathParam(r, "hash"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		status := http.StatusOK
		if result.Queued {
			status = http.StatusAccepted
		}
		WriteJSON(w, status, result)
	}
}

// HandleQualityStatus returns a handler for GET /api/v1/quality/status.
//
// The handler never fails: a missing intel subsystem or a failing read produces
// the documented shape with storage_error set, which the WebUI renders.
func HandleQualityStatus(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, cp.QualityStatus(r.Context()))
	}
}

// HandleQualityList returns a handler for GET /api/v1/quality/assessments.
func HandleQualityList(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pagination, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		items, err := cp.ListQuality(r.Context(), r.URL.Query().Get("q"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WritePage(w, http.StatusOK, items, pagination)
	}
}

// HandleQualityIP returns a handler for GET /api/v1/quality/ip/{ip}.
func HandleQualityIP(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := cp.QualityIP(r.Context(), PathParam(r, "ip"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, result)
	}
}

// HandleQualityProbeIP returns a handler for
// POST /api/v1/quality/ip/{ip}/actions/probe.
func HandleQualityProbeIP(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := cp.RequestIPQuality(r.Context(), PathParam(r, "ip"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		status := http.StatusOK
		if result.Queued {
			status = http.StatusAccepted
		}
		WriteJSON(w, status, result)
	}
}

// HandleProbeQuality returns a handler for
// POST /api/v1/nodes/{hash}/actions/probe-quality.
//
// It creates an intel job for the node and answers 202 with its id (WP08 §9).
func HandleProbeQuality(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := cp.ProbeQuality(r.Context(), PathParam(r, "hash"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		status := http.StatusOK
		if result.Queued {
			status = http.StatusAccepted
		}
		WriteJSON(w, status, result)
	}
}
