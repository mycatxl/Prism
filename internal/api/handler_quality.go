package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"prism/internal/inspection"
	"prism/internal/service"
)

func HandleReviewIPPure(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		result, err := cp.ReviewIPPure(r.Context(), PathParam(r, "hash"))
		if err != nil {
			var providerErr *inspection.ProviderError
			if errors.As(err, &providerErr) {
				status := http.StatusBadGateway
				if providerErr.Code == "IPPURE_LIMIT" {
					status = http.StatusTooManyRequests
					w.Header().Set("Retry-After", strconv.Itoa(int((providerErr.RetryAfter+time.Second-1)/time.Second)))
				}
				WriteError(w, status, providerErr.Code, providerErr.Message)
				return
			}
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, result)
	}
}

func HandleQualityStatus(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { WriteJSON(w, http.StatusOK, cp.QualityStatus()) }
}

func HandleQualityList(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pagination, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		WritePage(w, http.StatusOK, cp.ListQuality(r.URL.Query().Get("q")), pagination)
	}
}

func HandleQualityIP(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := cp.QualityIP(PathParam(r, "ip"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, result)
	}
}

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
