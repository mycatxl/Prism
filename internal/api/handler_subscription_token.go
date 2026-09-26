package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"prism/internal/model"
	"prism/internal/service"
)

// Public subscription endpoint (WP11 §4.3).
//
// It is registered on the main listener and deliberately bypasses the admin
// token: the token in the path *is* the credential. The plaintext token is
// hashed with SHA-256 and looked up in state.db; an unknown token and a
// disabled profile both answer 404 so the endpoint cannot be used to discover
// which tokens exist. The token never reaches a log, an error message or the
// audit trail.

// SubscriptionHandler serves GET /sub/{token}.
type SubscriptionHandler struct {
	cp      *service.ControlPlaneService
	limiter *exportSubscriptionLimiter
}

// NewSubscriptionHandler builds the /sub/{token} handler.
func NewSubscriptionHandler(cp *service.ControlPlaneService) *SubscriptionHandler {
	return &SubscriptionHandler{
		cp:      cp,
		limiter: newExportSubscriptionLimiter(),
	}
}

// ServeHTTP implements http.Handler so the route can be registered on the main
// mux without the management auth middleware.
func (h *SubscriptionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.cp == nil || h.cp.Engine == nil {
		writeSubscriptionNotFound(w)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		WriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	// §4.3: an endpoint that disabled allow_management does not serve /sub/*
	// either, exactly like the management surface.
	if !subscriptionsEnabled(h.cp) {
		writeSubscriptionNotFound(w)
		return
	}

	raw := strings.TrimPrefix(r.URL.Path, "/sub/")
	if raw == "" || len(raw) > 256 || strings.Contains(raw, "/") {
		writeSubscriptionNotFound(w)
		return
	}
	tokenHash := ExportProfileTokenSHA256(raw)

	if ok, retry := h.limiter.allow(tokenHash, clientIPForRateLimit(r)); !ok {
		writeExportRateLimited(w, retry)
		return
	}

	profile, err := h.cp.Engine.GetExportProfileByTokenSHA256(tokenHash)
	if err != nil || profile == nil || !profile.Enabled {
		// An unknown token and a disabled profile are indistinguishable.
		writeSubscriptionNotFound(w)
		return
	}

	filter, err := decodeStoredFilter(profile.FilterJSON)
	if err != nil {
		writeSubscriptionNotFound(w)
		return
	}

	body, contentType, report, err := runNodeExport(h.cp, exportRequest{
		Format:       profile.Format,
		NameTemplate: profile.NameTemplate,
		Filter:       filter,
		Limit:        defaultExportLimit,
	}, 0)
	if err != nil {
		writeSubscriptionNotFound(w)
		return
	}

	// §4.3.4: record the access and write an audit entry. The actor is the
	// export profile ID, never the token. The model.AuditActorExportPrefix actor
	// puts the entry in the subscription bucket of the retention policy, so a
	// caller holding only a subscription URL cannot push management entries out
	// of the audit trail.
	at := time.Now().UTC().UnixNano()
	_ = h.cp.Engine.TouchExportProfileAccess(profile.ID, at)
	_ = h.cp.Engine.AppendAudit(model.AuditEntry{
		AtNs:       at,
		Actor:      model.AuditActorExportPrefix + profile.ID,
		RemoteAddr: r.RemoteAddr,
		Action:     "GET /sub/{token}",
		Target:     profile.ID,
		Detail: "{\"exported\":" + strconv.Itoa(report.Exported) +
			",\"skipped\":" + strconv.Itoa(report.SkipCount()) + "}",
	})

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Prism-Export-Exported", strconv.Itoa(report.Exported))
	w.Header().Set("X-Prism-Export-Skipped", strconv.Itoa(report.SkipCount()))
	switch profile.Format {
	case "v2rayn":
		// v2rayN refreshes a subscription on this hint.
		w.Header().Set("Profile-Update-Interval", "12")
	case "mihomo":
		w.Header().Set("Content-Disposition", "inline; filename=prism.yaml")
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// writeSubscriptionNotFound is the single 404 shape of /sub: it never reveals
// whether the token exists or the profile is merely disabled.
func writeSubscriptionNotFound(w http.ResponseWriter) {
	WriteError(w, http.StatusNotFound, "NOT_FOUND", "not found")
}

// decodeStoredFilter parses the stored filter JSON.
func decodeStoredFilter(raw string) (exportProfileFilter, error) {
	var filter exportProfileFilter
	if strings.TrimSpace(raw) == "" {
		return filter, nil
	}
	if err := json.Unmarshal([]byte(raw), &filter); err != nil {
		return exportProfileFilter{}, err
	}
	return filter, nil
}

// subscriptionsEnabled reports whether at least one enabled endpoint allows
// management traffic. The environment-defined default endpoint always does, so
// /sub only disappears when every configured listener is closed to management.
func subscriptionsEnabled(cp *service.ControlPlaneService) bool {
	endpoints, err := cp.ListEndpoints()
	if err != nil {
		return false
	}
	for _, endpoint := range endpoints {
		if endpoint.Enabled && endpoint.AllowManagement {
			return true
		}
	}
	return false
}

// clientIPForRateLimit is the /sub rate-limit key. X-Forwarded-For is
// deliberately ignored: the public subscription path has no trusted-proxy
// configuration of its own, so honouring the header would let any caller
// pick its own bucket.
func clientIPForRateLimit(r *http.Request) string {
	if r == nil {
		return ""
	}
	return hostOnly(r.RemoteAddr)
}
