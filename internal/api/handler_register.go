package api

import (
	"net/http"
	"strings"

	"prism/internal/auth"
	"prism/internal/config"
	"prism/internal/model"
)

// TenantRegistrar is the self-service registration operation the register
// endpoint needs. *service.ControlPlaneService satisfies it.
type TenantRegistrar interface {
	RegisterTenantUser(mode config.RegistrationMode, inviteCode, username, password string, nowNs int64) (*model.User, error)
}

// registerRequest is the body of POST /api/v1/auth/register.
type registerRequest struct {
	InviteCode string `json:"invite_code"`
	Username   string `json:"username"`
	Password   string `json:"password"`
}

// registrationMode reports the deployment's policy.
//
// A nil reader or an unreadable value both mean off. Registration is the one
// door that has to fail closed: a misconfigured deployment should tell the
// person at it that sign-ups are closed, not open an account nobody meant to
// allow.
func (h *AuthHandlers) registrationMode() config.RegistrationMode {
	if h == nil || h.RegistrationMode == nil {
		return config.RegistrationOff
	}
	mode := h.RegistrationMode()
	if !mode.Valid() {
		return config.RegistrationOff
	}
	return mode
}

// HandleRegister implements POST /api/v1/auth/register.
//
// It is reachable without an identity - it is how a new user obtains one - so
// it is one of the two entries in publicPaths. The refusals carry the same
// shape whether the invitation is unknown, disabled, lapsed or used up: a
// caller holding a code has to be told it does not work, but a caller guessing
// one must not be able to tell those four apart.
//
// A successful registration signs the account in, because asking somebody who
// just chose a password to type it again is friction with no security value.
func (h *AuthHandlers) HandleRegister() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h == nil || h.Sessions == nil || h.Registration == nil {
			WriteError(w, http.StatusServiceUnavailable, "AUTH_UNAVAILABLE", "authentication is not configured")
			return
		}
		if mode := h.registrationMode(); !mode.AllowsRegistration() {
			WriteError(w, http.StatusForbidden, "REGISTRATION_CLOSED", "self-service registration is disabled")
			return
		}

		var req registerRequest
		if !decodeJSONBody(w, r, &req) {
			return
		}
		req.Username = strings.TrimSpace(req.Username)
		req.InviteCode = strings.TrimSpace(req.InviteCode)
		if req.Username == "" || req.Password == "" {
			WriteError(w, http.StatusBadRequest, "INVALID_REQUEST", "a username and a password are required")
			return
		}

		// The cookie policy is decided before anything is created, so a
		// deployment serving the console over plain HTTP to a real host refuses
		// the sign-up rather than opening an account whose session cookie
		// anyone on the path could read.
		policy, err := auth.NewCookiePolicy(r.TLS != nil, r.Host)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "HTTPS_REQUIRED",
				"the console must be reached over HTTPS, or over plain HTTP on loopback")
			return
		}

		user, err := h.Registration.RegisterTenantUser(
			h.registrationMode(), req.InviteCode, req.Username, req.Password, h.nowNs(),
		)
		if err != nil {
			writeServiceError(w, err)
			return
		}

		result, loginErr := h.Sessions.Login(
			req.Username, req.Password, "",
			h.clientKey(r), sessionSourceIP(r), r.UserAgent(),
		)
		if loginErr != nil {
			// The account exists and works; only the automatic sign-in failed.
			// Reporting that honestly leaves the console able to send the user
			// to the login page, while an error response would tell them the
			// registration itself did not happen.
			h.logf("api: registered %s but could not sign the account in: %v", user.Username, loginErr)
			WriteJSON(w, http.StatusCreated, map[string]any{
				"authenticated": false,
				"user": sessionUser{
					ID:       user.ID,
					Username: user.Username,
					Role:     user.Role,
				},
			})
			return
		}

		writeSessionCookies(w, policy, result.SessionID, result.CSRFToken, result.ExpiresAtNs)
		WriteJSON(w, http.StatusCreated, map[string]any{
			"authenticated": true,
			"user": sessionUser{
				ID:       result.User.ID,
				Username: result.User.Username,
				Role:     result.User.Role,
				HasTOTP:  len(result.User.TOTPSecretEnc) > 0,
			},
			"expires_at_ns": result.ExpiresAtNs,
		})
	}
}
