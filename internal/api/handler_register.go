package api

import (
	"net/http"
	"strconv"
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
// it is one of the two entries in publicPaths and, like the login, it refuses a
// request another site made (see originAllowed). The refusals carry the same
// shape whether the invitation is unknown, disabled, lapsed or used up: a
// caller holding a code has to be told it does not work, but a caller guessing
// one must not be able to tell those four apart.
//
// A successful registration signs the account in, because asking somebody who
// just chose a password to type it again is friction with no security value.
func (h *AuthHandlers) HandleRegister() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The block is checked first, and here rather than in the identity
		// middleware, because sign-up is one of the two public paths that
		// middleware lets through (identity.go: publicPaths) and the login
		// path's block comes from the session service, which owns the same
		// limiter and refuses before it reads anything. Sign-up has no such
		// owner: it spent its budget through recordRegistrationFailure and
		// then never asked the limiter again, so an address that had already
		// been blocked for guessing invite codes could keep guessing - one
		// indexed read per attempt, and an argon2id hash on every hit - and
		// never see a 429. Answering it here keeps that rule in the one place
		// that does the work, and leaves the middleware's contract (a public
		// path is reachable without an identity) and the login path's
		// behaviour exactly as they were.
		if h.refuseIfBlocked(w, r) {
			return
		}
		if !originAllowed(r) {
			WriteError(w, http.StatusForbidden, "ORIGIN_REJECTED",
				"a request from another site may not sign in or sign up")
			return
		}
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
		policy, err := auth.NewCookiePolicy(h.secureTransport(r), r.Host)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "HTTPS_REQUIRED",
				"the console must be reached over HTTPS, or over plain HTTP on loopback")
			return
		}

		user, err := h.Registration.RegisterTenantUser(
			h.registrationMode(), req.InviteCode, req.Username, req.Password, h.nowNs(),
		)
		if err != nil {
			// A refusal that reaches here already spent a database read, and
			// one that got past the code would have spent a password hash. The
			// cheap shape checks above are deliberately not counted: a request
			// body that is not even a sign-up should not be able to spend a
			// visitor's budget.
			h.recordRegistrationFailure(r)
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

// refuseIfBlocked answers 429 and reports true when the request's address has
// spent its authentication failure budget.
//
// Sign-up needs it because the block it earns is enforced by a layer it never
// reaches: the identity middleware lets the public paths past before it looks at
// the limiter, and the login path gets its own refusal from the session service.
// Without this check a blocked address was told "too many failed attempts" on
// every management route while /auth/register kept answering at full cost -
// which is the one route where an unauthenticated caller can make Prism work.
//
// The answer is the same one the middleware and the login path give: 429
// RATE_LIMITED with a whole-second Retry-After. A limiter that was never built
// blocks nothing, which is what a deployment without one gets.
func (h *AuthHandlers) refuseIfBlocked(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || h.FailureLimiter == nil {
		return false
	}
	blocked, retryAfter := h.FailureLimiter.Blocked(h.clientKey(r))
	if !blocked {
		return false
	}
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
	}
	WriteError(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many failed attempts; retry later")
	return true
}

// recordRegistrationFailure counts a refused sign-up against the same limiter a
// failed login is counted against.
//
// Registration is public and does real work before it can refuse: one indexed
// read of the invite code and, for a code that works, an argon2id password hash.
// Counting the refusal is what keeps the budget honest; the budget itself is
// enforced at the top of HandleRegister (refuseIfBlocked), because sign-up is a
// public path that the identity middleware deliberately lets past its own
// limiter check. Without the two together, this would be the one unauthenticated
// way to make the server busy forever.
// One dimension is counted: the address the request came from, which is the same
// dimension the login path counts. The account name in the body is deliberately
// not counted, even though the login path counts the name it was given. Nothing
// ever reads a name-shaped bucket here - the login path resolves an account id
// and looks that up, not a name a stranger typed - so a second dimension would
// only hand an unauthenticated caller a way to mint unbounded distinct keys in a
// bounded LRU limiter (rate_limiter.go: a full table evicts its head, entries
// that are still blocked included) and push other clients' blocks out of it.
// Counting by address has no such lever: the address is the part of the request
// the caller cannot choose freely.
func (h *AuthHandlers) recordRegistrationFailure(r *http.Request) {
	if h == nil || h.FailureLimiter == nil {
		return
	}
	h.FailureLimiter.RecordFailure(h.clientKey(r))
}
