package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"prism/internal/model"
)

// Audit logging (WP04 §4.7).
//
// Every successful management write (POST, PUT, PATCH, DELETE, including the
// /actions/* routes) is appended to the audit_log table. Only the key names of
// the request body are recorded, never the values.
const (
	auditRetentionDays   = 90
	auditMaxEntries      = 100000
	auditDefaultLimit    = 100
	auditMaxLimit        = 200
	auditDetailBodyBytes = 64 << 10
	auditPruneInterval   = 24 * time.Hour
	// auditActorHashBytes is the sha256 prefix that renders as 8 hex characters.
	auditActorHashBytes = 4
)

// AuditLogStore is the persistence surface required by audit logging.
// *state.StateRepo satisfies it.
type AuditLogStore interface {
	AppendAudit(model.AuditEntry) error
	ListAudit(beforeID int64, limit int) ([]model.AuditEntry, error)
	PruneAudit(olderThanNs int64, keepMax int) (int64, error)
}

// auditWriteMethods are the management API methods that produce audit records.
var auditWriteMethods = map[string]bool{
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
}

// AuditMiddleware records successful management write operations. A nil store
// disables auditing entirely.
func AuditMiddleware(store AuditLogStore, adminToken string, maxBodyBytes int64, next http.Handler) http.Handler {
	if store == nil {
		return next
	}
	actor := AuditActor(adminToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r == nil || !auditWriteMethods[r.Method] {
			next.ServeHTTP(w, r)
			return
		}

		// Peek at the body before the handler consumes it; only key names are
		// kept and the full body is replayed to the handler afterwards.
		detail := auditDetail(r, maxBodyBytes)

		recorder := &auditStatusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)

		if recorder.status < 200 || recorder.status >= 300 {
			// Failed mutations are not audited: nothing changed.
			return
		}
		action := auditAction(r)
		if action == "" {
			return
		}
		entry := model.AuditEntry{
			AtNs:       time.Now().UnixNano(),
			Actor:      actor,
			RemoteAddr: r.RemoteAddr,
			Action:     action,
			Target:     auditTarget(r),
			Detail:     detail,
		}
		if err := store.AppendAudit(entry); err != nil {
			log.Printf("audit: append failed for %s: %v", action, err)
		}
	})
}

// AuditActor returns the audit actor for an admin token: the first 8 hex
// characters of its sha256 digest. The token itself is never recorded.
func AuditActor(adminToken string) string {
	if adminToken == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(adminToken))
	return hex.EncodeToString(sum[:auditActorHashBytes])
}

// auditAction renders "METHOD <route pattern>", for example
// "PATCH /api/v1/platforms/{id}".
func auditAction(r *http.Request) string {
	if r == nil {
		return ""
	}
	pattern := r.Pattern
	if pattern == "" {
		return ""
	}
	if strings.HasPrefix(pattern, r.Method+" ") {
		return pattern
	}
	return r.Method + " " + pattern
}

// auditTarget records the request's path parameter values.
func auditTarget(r *http.Request) string {
	if r == nil || r.Pattern == "" {
		return ""
	}
	parts := make([]string, 0, 4)
	for _, name := range auditPatternParamNames(r.Pattern) {
		if value := r.PathValue(name); value != "" {
			parts = append(parts, name+"="+value)
		}
	}
	return strings.Join(parts, ",")
}

// auditPatternParamNames extracts the "{name}" / "{name...}" names of a ServeMux
// pattern.
func auditPatternParamNames(pattern string) []string {
	var names []string
	for {
		open := strings.IndexByte(pattern, '{')
		if open < 0 {
			return names
		}
		closeIdx := strings.IndexByte(pattern[open:], '}')
		if closeIdx < 0 {
			return names
		}
		name := strings.TrimSuffix(pattern[open+1:open+closeIdx], "...")
		if name != "" {
			names = append(names, name)
		}
		pattern = pattern[open+closeIdx+1:]
	}
}

// auditDetail renders the request body key names as {"keys":[...]}.
func auditDetail(r *http.Request, maxBodyBytes int64) string {
	keys := auditRequestBodyKeys(r, maxBodyBytes)
	payload, err := json.Marshal(struct {
		Keys []string `json:"keys"`
	}{Keys: keys})
	if err != nil {
		return ""
	}
	return string(payload)
}

// auditRequestBodyKeys returns the top-level JSON key names of the request body
// and restores the body so the handler sees the complete payload.
func auditRequestBodyKeys(r *http.Request, maxBodyBytes int64) []string {
	keys := []string{}
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return keys
	}
	limit := int64(auditDetailBodyBytes)
	if maxBodyBytes > 0 && maxBodyBytes < limit {
		limit = maxBodyBytes
	}
	if limit <= 0 {
		return keys
	}
	buffered, readErr := io.ReadAll(io.LimitReader(r.Body, limit))
	if len(buffered) > 0 {
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buffered), r.Body))
	}
	if readErr != nil && len(buffered) == 0 {
		return keys
	}
	return topLevelJSONKeys(buffered)
}

// topLevelJSONKeys streams the top-level object keys of raw. It tolerates
// truncated payloads and returns everything parsed so far.
func topLevelJSONKeys(raw []byte) []string {
	keys := []string{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return keys
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return keys
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			break
		}
		key, ok := keyToken.(string)
		if !ok {
			break
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			break
		}
	}
	return keys
}

// auditStatusRecorder captures the response status written by a handler.
type auditStatusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (rec *auditStatusRecorder) WriteHeader(status int) {
	if rec.wroteHeader {
		return
	}
	rec.status = status
	rec.wroteHeader = true
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *auditStatusRecorder) Write(payload []byte) (int, error) {
	if !rec.wroteHeader {
		rec.WriteHeader(http.StatusOK)
	}
	return rec.ResponseWriter.Write(payload)
}

// Flush keeps streaming responses working through the recorder.
func (rec *auditStatusRecorder) Flush() {
	if flusher, ok := rec.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// auditLogPageResponse is the GET /api/v1/audit-logs envelope.
type auditLogPageResponse struct {
	Items []model.AuditEntry `json:"items"`
	Limit int                `json:"limit"`
}

// HandleListAuditLogs handles GET /api/v1/audit-logs?before_id=&limit=.
// It returns audit entries in descending ID order; limit is capped at 200.
func HandleListAuditLogs(store AuditLogStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", "audit log storage is unavailable")
			return
		}
		q := r.URL.Query()

		var beforeID int64
		if v := q.Get("before_id"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				writeInvalidArgument(w, "before_id: must be a non-negative integer")
				return
			}
			beforeID = n
		}

		limit := auditDefaultLimit
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeInvalidArgument(w, "limit: must be a non-negative integer")
				return
			}
			if n > auditMaxLimit {
				writeInvalidArgument(w, fmt.Sprintf("limit: must be <= %d", auditMaxLimit))
				return
			}
			if n > 0 {
				limit = n
			}
		}

		entries, err := store.ListAudit(beforeID, limit)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", "failed to read audit log")
			return
		}
		if entries == nil {
			entries = []model.AuditEntry{}
		}
		WriteJSON(w, http.StatusOK, auditLogPageResponse{Items: entries, Limit: limit})
	})
}

// PruneAuditLogs applies the audit retention policy: 90 days, at most 100000
// entries. It returns the number of removed rows.
func PruneAuditLogs(store AuditLogStore) (int64, error) {
	if store == nil {
		return 0, nil
	}
	olderThanNs := time.Now().Add(-auditRetentionDays * 24 * time.Hour).UnixNano()
	return store.PruneAudit(olderThanNs, auditMaxEntries)
}

// StartAuditPruner runs the retention job every interval until the returned
// stop function is called. A nil store makes it a no-op.
func StartAuditPruner(store AuditLogStore, interval time.Duration) func() {
	if store == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = auditPruneInterval
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				removed, err := PruneAuditLogs(store)
				if err != nil {
					log.Printf("audit: retention prune failed: %v", err)
					continue
				}
				if removed > 0 {
					log.Printf("audit: retention prune removed %d entries", removed)
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		once.Do(func() { close(done) })
	}
}
