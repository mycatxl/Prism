package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"prism/internal/intel/jobs"
	"prism/internal/service"
)

// sseKeepAlive is the SSE heartbeat interval; the progress rate limit of one
// frame per second is enforced in the job hub.
const sseKeepAlive = 15 * time.Second

// HandleIntelCreateJob implements POST /api/v1/intel/jobs.
func HandleIntelCreateJob(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")

		var req service.IntelJobRequest
		if err := DecodeBody(r, &req); err != nil {
			writeDecodeError(w, err)
			return
		}
		job, err := cp.CreateIntelJob(r.Context(), req)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
	}
}

// HandleIntelListJobs implements GET /api/v1/intel/jobs.
func HandleIntelListJobs(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pagination, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		jobsList, total, err := cp.ListIntelJobs(r.Context(), r.URL.Query().Get("status"), pagination.Limit, pagination.Offset)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeIntelPage(w, jobsList, total, pagination)
	}
}

// HandleIntelGetJob implements GET /api/v1/intel/jobs/{id}.
func HandleIntelGetJob(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		detail, err := cp.GetIntelJob(r.Context(), PathParam(r, "id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, detail)
	}
}

// HandleIntelListJobItems implements GET /api/v1/intel/jobs/{id}/items.
func HandleIntelListJobItems(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pagination, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		page, err := cp.ListIntelJobItems(r.Context(), PathParam(r, "id"),
			r.URL.Query().Get("status"), pagination.Limit, pagination.Offset)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeIntelPage(w, page.Items, page.Total, pagination)
	}
}

// HandleIntelCancelJob implements POST /api/v1/intel/jobs/{id}/actions/cancel.
func HandleIntelCancelJob(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		job, err := cp.CancelIntelJob(r.Context(), PathParam(r, "id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
	}
}

// HandleIntelRetryFailedJob implements
// POST /api/v1/intel/jobs/{id}/actions/retry-failed.
func HandleIntelRetryFailedJob(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		job, retried, err := cp.RetryFailedIntelJob(r.Context(), PathParam(r, "id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusAccepted, map[string]any{"job": job, "retried": retried})
	}
}

// HandleIntelStatus implements GET /api/v1/intel/status.
func HandleIntelStatus(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report, err := cp.IntelStatusReport(r.Context())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, report)
	}
}

// HandleIntelNode implements GET /api/v1/intel/nodes/{hash}.
func HandleIntelNode(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		detail, err := cp.IntelNodeReport(r.Context(), PathParam(r, "hash"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, detail)
	}
}

// HandleIntelIP implements GET /api/v1/intel/ip/{ip}.
func HandleIntelIP(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		detail, err := cp.IntelIPReport(r.Context(), PathParam(r, "ip"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, detail)
	}
}

// HandleIntelJobEvents implements GET /api/v1/intel/jobs/{id}/events (SSE).
// The management identity middleware authenticates the request before it reaches
// this handler; EventSource sends the session cookie automatically.
func HandleIntelJobEvents(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := ActorFromContext(r.Context()); !ok {
			WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
		jobID := PathParam(r, "id")

		flusher, ok := w.(http.Flusher)
		if !ok {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", "streaming unsupported")
			return
		}

		sub, err := cp.SubscribeIntelJob(r.Context(), jobID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		defer sub.Close()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		// The first frames replay the current state so a reconnecting client does
		// not have to poll GET /jobs/{id} first.
		writeSSEProgress(w, flusher, nil)
		if detail, err := cp.GetIntelJob(r.Context(), jobID); err == nil {
			frame := jobs.Progress{
				Done:    detail.Progress.Done,
				Failed:  detail.Progress.Failed,
				Skipped: detail.Progress.Skipped,
				Total:   detail.Progress.Total,
				Status:  detail.Progress.Status,
			}
			writeSSEProgress(w, flusher, &frame)
			// A job that already finished before the subscriber arrived will never
			// push another frame: the hub only broadcasts transitions. Closing the
			// stream here is what terminates it. Without this the connection stays
			// open forever, kept alive by the keep-alive ticker, and the client
			// never learns that the job is done.
			if sseTerminalStatus(frame.Status) {
				writeSSEEvent(w, flusher, "end", nil)
				return
			}
		}

		keepAlive := time.NewTicker(sseKeepAlive)
		defer keepAlive.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-keepAlive.C:
				_, _ = w.Write([]byte(": keep-alive\n\n"))
				flusher.Flush()
			case frame, ok := <-sub.C:
				if !ok {
					writeSSEEvent(w, flusher, "end", nil)
					return
				}
				writeSSEProgress(w, flusher, &frame)
				if sseTerminalStatus(frame.Status) {
					writeSSEEvent(w, flusher, "end", nil)
					return
				}
			}
		}
	}
}

// sseTerminalStatus reports whether a job status ends the SSE stream. It is the
// single definition of "finished" for the endpoint: the status replayed on
// subscribe and the status carried by a pushed frame must agree, or a subscriber
// that arrives after the job finished would wait forever for a frame the hub
// never broadcasts again.
func sseTerminalStatus(status string) bool {
	switch status {
	case "succeeded", "partial", "failed", "canceled":
		return true
	default:
		return false
	}
}

func writeSSEProgress(w http.ResponseWriter, flusher http.Flusher, frame *jobs.Progress) {
	if frame == nil {
		writeSSEEvent(w, flusher, "progress", map[string]any{"status": "unknown"})
		return
	}
	writeSSEEvent(w, flusher, "progress", frame)
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, payload any) {
	if event != "" {
		_, _ = w.Write([]byte("event: " + event + "\n"))
	}
	if payload != nil {
		if encoded, err := json.Marshal(payload); err == nil {
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(encoded)
			_, _ = w.Write([]byte("\n"))
		}
	}
	_, _ = w.Write([]byte("\n"))
	flusher.Flush()
}

// writeIntelPage renders the limit/offset envelope shared by the intel lists.
func writeIntelPage[T any](w http.ResponseWriter, items []T, total int, p Pagination) {
	if items == nil {
		items = []T{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"total":  total,
		"limit":  p.Limit,
		"offset": p.Offset,
	})
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *requestBodyTooLargeError
	if errors.As(err, &tooLarge) {
		WriteError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", err.Error())
		return
	}
	WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
}
