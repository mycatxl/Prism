package api

import (
	"net/http"
	"strings"
)

// The management API is split by who may use it.
//
// The administration surface - every route that reads or changes the control
// plane, from platforms to the audit log - belongs to the administrator. What a
// signed-in user reaches is deliberately small: their own account, and their
// own panel. Anything else is refused, so a new route is private to the
// administrator by default rather than by remembering to protect it, which is
// the only direction that fails safe.

// userReachablePaths are the management paths a non-administrator may use.
//
// They are prefixes rather than route patterns because this middleware runs on
// the outer mux, before the inner one has matched a pattern: r.Pattern is still
// the outer "/api/" match at that point.
var userReachablePaths = []string{
	"/api/v1/auth",
	"/api/v1/me",
}

// isUserReachablePath reports whether a path belongs to the self-service
// surface: the authentication routes themselves, and the caller's own panel.
func isUserReachablePath(path string) bool {
	for _, prefix := range userReachablePaths {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// requireAdmin refuses a management request whose actor is not an administrator.
//
// It runs inside the identity middleware, so a caller that reaches it has
// already authenticated. That is what makes 403 the right answer rather than
// 401: the credential is fine, the role is not, and telling the console which
// of the two is wrong is what lets it render something useful.
func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := ActorFromContext(r.Context())
		if !ok {
			// The routes a caller uses to obtain a credential - sign-in and
			// sign-up - reach this middleware without one on purpose, and the
			// identity middleware already decided they may. Refusing them here
			// would answer 401 to the very request that creates the session.
			if isPublicManagementPath(r) {
				next.ServeHTTP(w, r)
				return
			}
			WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
		if actor.IsAdmin() || isUserReachablePath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		WriteError(w, http.StatusForbidden, "FORBIDDEN", "administrator access required")
	})
}
