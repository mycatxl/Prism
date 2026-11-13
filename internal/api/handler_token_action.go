package api

import (
	"net/http"
	"net/netip"
	"strings"

	"prism/internal/keys"
	"prism/internal/model"
	"prism/internal/service"
)

type inheritLeaseRequest struct {
	ParentAccount string `json:"parent_account"`
	NewAccount    string `json:"new_account"`
}

// TokenActionKeyResolver resolves the credential in the token position of a
// token-path route.
//
// It is the same contract the data plane uses: an unknown, disabled or expired
// key answers nil, and so does a credential that is not key-shaped at all. The
// The caller rejects every credential that is not a valid AccessKey. Keeping
// this resolver separate makes the key store and client-address policy explicit.
type TokenActionKeyResolver interface {
	// VerifyCredential returns the key the credential names, or nil.
	//
	// clientAddr is where the request came from; the key's own IP allowlist is
	// checked against it, and the zero Addr satisfies no allowlist.
	VerifyCredential(credential string, clientAddr netip.Addr) *keys.KeyState
}

// NewTokenActionHandler returns the handler for token-path actions.
//
// The route changes lease state, so a successful mutation is recorded in the
// audit trail exactly like the management writes of: the actor is the sha256
// prefix of the credential, never the credential itself, and the credential
// path parameter is not copied into the record. The request-body limit wraps
// the audit middleware so the audit payload peek reads a body that is already
// bounded by apiMaxBodyBytes.
//
// The token position accepts only a valid proxy AccessKey. keys may be nil, in
// which case every token-path request is rejected.
// clientIP names the request's client address for the per-key IP allowlist;
// cmd/prism passes proxy.ProxyClientIP, and a nil one reads the peer address
// only.
//
// proxyToken remains in the signature for callers that still load the legacy
// configuration field; it is deliberately ignored by authentication.
func NewTokenActionHandler(proxyToken string, keyResolver TokenActionKeyResolver, cp *service.ControlPlaneService, apiMaxBodyBytes int64, clientIP func(*http.Request) string) http.Handler {
	return NewTokenActionHandlerWithAuthFailureRecorder(proxyToken, keyResolver, cp, apiMaxBodyBytes, clientIP, nil)
}

// NewTokenActionHandlerWithAuthFailureRecorder is the token-action constructor
// used by the endpoint mux when proxy-entry authentication failures are rate
// limited. The recorder is called only after a key-shaped credential fails
// verification; malformed or otherwise unrelated API requests are not charged
// to the proxy-auth limiter.
func NewTokenActionHandlerWithAuthFailureRecorder(
	proxyToken string,
	keyResolver TokenActionKeyResolver,
	cp *service.ControlPlaneService,
	apiMaxBodyBytes int64,
	clientIP func(*http.Request) string,
	recordAuthFailure func(*http.Request),
) http.Handler {
	if cp == nil {
		return http.NotFoundHandler()
	}

	mux := http.NewServeMux()
	mux.Handle("POST /{token}/api/v1/{platform}/actions/inherit-lease", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := PathParam(r, "token")
		identity, ok := resolveTokenActionIdentity(r, token, proxyToken, keyResolver, clientIP)
		if !ok {
			if keys.IsCredentialShape(token) && recordAuthFailure != nil {
				recordAuthFailure(r)
			}
			http.NotFound(w, r)
			return
		}

		platformName := strings.TrimSpace(PathParam(r, "platform"))
		if platformName == "" {
			writeInvalidArgument(w, "platform: must be non-empty")
			return
		}

		// An access key is scoped to platforms, and this route changes routing
		// state for one, so the same restriction the data plane enforces
		// applies here. Without it a key restricted to one platform could
		// rewrite another platform's leases.
		platformID, found := platformIDByName(cp, platformName)
		if !found {
			writeServiceError(w, &service.ServiceError{Code: "NOT_FOUND", Message: "platform not found"})
			return
		}
		if !identity.Key.AllowsPlatform(platformID) {
			writeServiceError(w, &service.ServiceError{
				Code:    "FORBIDDEN",
				Message: "access key is not allowed to use this platform",
			})
			return
		}

		var req inheritLeaseRequest
		if err := DecodeBody(r, &req); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		fresh, ok := resolveTokenActionIdentity(r, token, proxyToken, keyResolver, clientIP)
		if !ok || fresh.Key.UserID != identity.Key.UserID || fresh.Key.SessionEpoch != identity.Key.SessionEpoch || !fresh.Key.AllowsPlatform(platformID) {
			http.NotFound(w, r)
			return
		}

		if err := cp.InheritLeaseByPlatformNameForUser(identity.Key.UserID, platformName, req.ParentAccount, req.NewAccount); err != nil {
			writeServiceError(w, err)
			return
		}

		// The audit row names the credential that actually acted. It is set
		// before the response is written, so the middleware reads it after the
		// handler returns and records the key rather than the admin token.
		setAuditActor(r, CredentialActor(identity.Credential), identity.Key.UserID)

		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))

	var handler http.Handler = mux
	if cp.Engine != nil {
		handler = AuditMiddleware(cp.Engine, apiMaxBodyBytes, mux)
	}
	handler = RequestBodyLimitMiddleware(apiMaxBodyBytes, handler)
	// This route carries a body and is served by the same listener as the
	// management API, so it needs the same body-phase bound: the listener's
	// ReadHeaderTimeout stops at the end of the header block, and ReadTimeout is
	// unset for the SSE stream. It wraps the body-limit layer because the
	// controller has to reach the connection.
	return RequestBodyDeadlineMiddleware(RequestBodyTimeout, handler)
}

// tokenActionIdentity is who a token-path request authenticated as.
//
// Credential is kept only so the audit actor can be derived from it; it is
// never written to a log or a database row.
type tokenActionIdentity struct {
	Credential string
	// Key is always populated for a successful token-path authentication.
	Key *keys.KeyState
}

// resolveTokenActionIdentity resolves the token position of a token-path route.
//
// The token must be key-shaped and must resolve to a proxy-scoped key. There is
// no global-token or empty-token fallback.
func resolveTokenActionIdentity(
	r *http.Request,
	token string,
	_ string,
	keyResolver TokenActionKeyResolver,
	clientIP func(*http.Request) string,
) (tokenActionIdentity, bool) {
	if !keys.IsCredentialShape(token) || keyResolver == nil {
		return tokenActionIdentity{}, false
	}
	state := keyResolver.VerifyCredential(token, requestClientAddr(r, clientIP))
	if state == nil {
		return tokenActionIdentity{}, false
	}
	// This route is a data-plane action, so an admin-scoped key must not reach
	// it. The data-plane verifier already refuses that scope, but the check is
	// repeated here so the rule holds for any resolver.
	if state.Scope != model.KeyScopeProxy {
		return tokenActionIdentity{}, false
	}
	return tokenActionIdentity{Credential: token, Key: state}, true
}

// platformIDByName resolves a platform name to its ID through the pool.
func platformIDByName(cp *service.ControlPlaneService, name string) (string, bool) {
	if cp == nil || cp.Pool == nil {
		return "", false
	}
	plat, ok := cp.Pool.GetPlatformByName(strings.TrimSpace(name))
	if !ok || plat == nil {
		return "", false
	}
	return plat.ID, true
}

// requestClientAddr resolves the source address of a token-path request for the
// per-key IP allowlist.
//
// clientIP is the same rule the proxy entry points apply - cmd/prism passes
// proxy.ProxyClientIP - so a deployment behind a trusted proxy sees the real
// client. A handler built without one reads the peer address only, which is the
// strictest reading: an address that does not parse leaves the Addr invalid,
// and an allowlisted key then refuses the request.
func requestClientAddr(r *http.Request, clientIP func(*http.Request) string) netip.Addr {
	if r == nil {
		return netip.Addr{}
	}
	host := hostOnly(r.RemoteAddr)
	if clientIP != nil {
		host = clientIP(r)
	}
	addr, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(host), "[]"))
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}
