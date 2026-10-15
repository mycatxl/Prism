package api

import (
	"net/http"

	"prism/internal/auth"
	"prism/internal/service"
)

// The self-service surface: what a signed-in user may do with their own
// account.
//
// Every route here is keyed by the session's actor rather than by a path
// parameter, so there is nothing for a caller to point at somebody else - the
// shape of the API is the authorisation. The routes live under /api/v1/me,
// which is one of the two prefixes the role gate lets a user-role session
// reach (see rolegate.go).

// meActor resolves the caller, or answers 401.
//
// The role gate already refused an anonymous request, but a handler that
// indexes a map by actor.UserID must not depend on that: the interface is
// resolved here, once, so no downstream call can be reached without an
// identity.
func meActor(w http.ResponseWriter, r *http.Request) (Actor, bool) {
	actor, ok := ActorFromContext(r.Context())
	if !ok || actor.UserID == "" {
		WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return Actor{}, false
	}
	return actor, true
}

// currentSessionID reads the session cookie of the request, which is what lets
// the session list mark the row the caller is reading it from.
func currentSessionID(r *http.Request) string {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || cookie == nil {
		return ""
	}
	return cookie.Value
}

// HandleMeOverview implements GET /api/v1/me/overview.
//
// One call answers the whole landing page - what plan is in force, how much
// traffic is left, which platforms are usable - so the panel opens with a
// single round trip.
func HandleMeOverview(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		overview, err := cp.SelfOverview(actor.UserID, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, overview)
	}
}

// HandleMeSubscription implements GET /api/v1/me/subscription.
//
// An account without a subscription is not an error: it is a user who has not
// bought anything yet, and the panel says so with a null.
func HandleMeSubscription(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		view, err := cp.SelfSubscription(actor.UserID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"subscription": view})
	}
}

// HandleMeUsage implements GET /api/v1/me/usage.
func HandleMeUsage(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		days, err := intQuery(r, "days", 0)
		if err != nil {
			writeInvalidArgument(w, err.Error())
			return
		}
		rows, err := cp.SelfUsage(actor.UserID, days, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeTenantItems(w, rows)
	}
}

// HandleMeKeys implements GET /api/v1/me/keys.
func HandleMeKeys(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		keys, err := cp.ListUserAccessKeys(actor.UserID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeTenantItems(w, keys)
	}
}

// HandleMeCreateKey implements POST /api/v1/me/keys.
//
// A user mints their own key, and the plan decides how many they may hold: the
// allowance is checked against the plan in force, so opening the door here does
// not open it wider than what was bought.
func HandleMeCreateKey(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		var in service.AccessKeyInput
		if err := DecodeBody(r, &in); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		view, credential, err := cp.CreateAccessKey(actor.UserID, in, adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, map[string]any{
			"key":        view,
			"credential": credential,
		})
	}
}

// HandleMeDeleteKey implements DELETE /api/v1/me/keys/{keyID}.
func HandleMeDeleteKey(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		keyID := PathParam(r, "keyID")
		if keyID == "" {
			writeInvalidArgument(w, "key id: required")
			return
		}
		if err := cp.DeleteAccessKey(actor.UserID, keyID); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleMeSessions implements GET /api/v1/me/security/sessions.
//
// The identifier in each row is the stored hash, and the request that lists
// them is the only thing that can mark one as current - the server compares the
// cookie it received against the hashes it holds.
func HandleMeSessions(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		sessions, err := cp.ListUserSessions(actor.UserID, currentSessionID(r), adminNowNs())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeTenantItems(w, sessions)
	}
}

// HandleMeRevokeSession implements
// DELETE /api/v1/me/security/sessions/{sessionID}.
//
// The console sends back the opaque id it was given. Ownership is part of the
// delete, so an id belonging to somebody else removes nothing.
func HandleMeRevokeSession(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := meActor(w, r)
		if !ok {
			return
		}
		sessionID := PathParam(r, "sessionID")
		if sessionID == "" {
			writeInvalidArgument(w, "session id: required")
			return
		}
		if err := cp.RevokeUserSession(actor.UserID, sessionID); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
