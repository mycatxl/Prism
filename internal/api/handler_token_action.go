package api

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"prism/internal/service"
)

type inheritLeaseRequest struct {
	ParentAccount string `json:"parent_account"`
	NewAccount    string `json:"new_account"`
}

// NewTokenActionHandler returns the handler for token-path actions.
//
// The route changes lease state, so a successful mutation is recorded in the
// audit trail exactly like the management writes of WP04 §4.7 (G-07): the actor
// is the sha256 prefix of the proxy token, never the token itself, and the
// credential path parameter is not copied into the record. The request-body
// limit wraps the audit middleware so the audit payload peek reads a body that
// is already bounded by apiMaxBodyBytes.
func NewTokenActionHandler(proxyToken string, cp *service.ControlPlaneService, apiMaxBodyBytes int64) http.Handler {
	if cp == nil {
		return http.NotFoundHandler()
	}

	mux := http.NewServeMux()
	mux.Handle("POST /{token}/api/v1/{platform}/actions/inherit-lease", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := PathParam(r, "token")
		if proxyToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(proxyToken)) != 1 {
			http.NotFound(w, r)
			return
		}

		platformName := strings.TrimSpace(PathParam(r, "platform"))
		if platformName == "" {
			writeInvalidArgument(w, "platform: must be non-empty")
			return
		}

		var req inheritLeaseRequest
		if err := DecodeBody(r, &req); err != nil {
			writeDecodeBodyError(w, err)
			return
		}

		if err := cp.InheritLeaseByPlatformName(platformName, req.ParentAccount, req.NewAccount); err != nil {
			writeServiceError(w, err)
			return
		}

		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))

	var handler http.Handler = mux
	if cp.Engine != nil {
		handler = AuditMiddleware(cp.Engine, proxyToken, apiMaxBodyBytes, mux)
	}
	return RequestBodyLimitMiddleware(apiMaxBodyBytes, handler)
}
