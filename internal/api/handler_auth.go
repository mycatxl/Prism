package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"prism/internal/auth"
	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/service"
)

// failedLoginAuditAction is the action of the audit row a refused login writes.
// It is deliberately not the bare route pattern: the action itself is what tells
// a login that succeeded from one that did not when the log is read back.
const failedLoginAuditAction = "POST /api/v1/auth/login <failed>"

// AccountService is the account operations the console's own security endpoints
// need. *service.ControlPlaneService satisfies it.
type AccountService interface {
	GetAccount(id string) (service.Account, error)
	StartTOTPEnrolment(id, issuer string) (service.TOTPEnrolment, error)
	ConfirmTOTPEnrolment(id, secret, code string, nowNs int64) error
	DisableTOTP(id string, nowNs int64) error
	ChangeAccountPassword(userID, currentPassword, newPassword string, nowNs int64) error
}

// AuthHandlers serves the console's authentication endpoints.
type AuthHandlers struct {
	// Sessions authenticates and issues sessions. Required.
	Sessions SessionResolver
	// Accounts performs the account-level operations. Nil disables the
	// second-factor and password endpoints.
	Accounts AccountService
	// Registration performs self-service sign-up. Nil disables
	// POST /api/v1/auth/register, which is what a deployment that never meant
	// to open sign-ups should get.
	Registration TenantRegistrar
	// RegistrationMode reports the deployment's policy - off, invite or open.
	// Nil is read as off: a policy that cannot be read must not open the door.
	RegistrationMode func() config.RegistrationMode
	// ClientKey names the address a failed login is counted against. Nil means
	// failures are not counted, which is what a deployment without a limiter
	// gets.
	ClientKey func(*http.Request) string
	// FailureLimiter counts a refused authentication. Logins are counted by the
	// session service, which holds the same limiter; this is the same instance,
	// and it is what lets the sign-up endpoint count its refusals too. That
	// route is public and does real work before it can refuse - one indexed read
	// of the invite code, then a password hash - so leaving it out of the budget
	// would leave the one unauthenticated way to make Prism work. Nil counts
	// nothing, which is what a deployment without a limiter gets.
	FailureLimiter auth.Blocklist
	// SecureTransport reports whether a request reached the console over HTTPS
	// as far as the end user is concerned: the connection is TLS, or a trusted
	// proxy in front of Prism terminated it and said so. Nil means the
	// connection's own TLS state is the only evidence, which is the behaviour of
	// a deployment that trusts no forwarded header.
	SecureTransport func(*http.Request) bool
	// AuditLog records a failed login. It is the one management write the audit
	// middleware never sees, because that middleware only records the
	// successful ones and a failure would otherwise leave no trace at all. Nil
	// disables the row.
	AuditLog AuditLogStore
	// Logf receives authentication failures. Nil silences them. It never
	// receives a password, a session id or a second-factor secret.
	Logf func(format string, args ...any)
	// Now is the clock, in Unix nanoseconds. Nil uses the wall clock.
	Now func() int64
}

func (h *AuthHandlers) nowNs() int64 {
	if h != nil && h.Now != nil {
		return h.Now()
	}
	return time.Now().UnixNano()
}

func (h *AuthHandlers) logf(format string, args ...any) {
	if h != nil && h.Logf != nil {
		h.Logf(format, args...)
	}
}

func (h *AuthHandlers) clientKey(r *http.Request) string {
	if h == nil || h.ClientKey == nil {
		return ""
	}
	return h.ClientKey(r)
}

// secureTransport reports whether the request is HTTPS for the end user. A
// handler that was never given a transport policy falls back to the connection
// itself, which is the same "trust nothing forwarded" default the failure
// limiter has.
func (h *AuthHandlers) secureTransport(r *http.Request) bool {
	if h == nil || h.SecureTransport == nil {
		return r != nil && r.TLS != nil
	}
	return h.SecureTransport(r)
}

// auditFailedLogin appends the audit row for a refused login.
//
// It records the route and the address it came from, and nothing else: no
// username, no password and no second factor, because the audit log is read back
// through the console and a stored credential would be handed to whoever can
// list it.
func (h *AuthHandlers) auditFailedLogin(r *http.Request) {
	if h == nil || h.AuditLog == nil || r == nil {
		return
	}
	entry := model.AuditEntry{
		AtNs:       h.nowNs(),
		Action:     failedLoginAuditAction,
		RemoteAddr: r.RemoteAddr,
	}
	if err := h.AuditLog.AppendAudit(entry); err != nil {
		h.logf("api: could not record a failed login: %v", err)
	}
}

// requireSessionActor refuses a caller that authenticated with an access key from
// the endpoints that manage credentials.
//
// An admin key is an automation credential: it may drive the management API, but
// it may not change a password or touch a second factor, because a key that can
// do that is a key that can outlive its revocation. The rule is enforced here
// rather than by the role gate, because both kinds of caller carry the
// administrator role and only one of them is a person at a console.
func requireSessionActor(w http.ResponseWriter, actor Actor) bool {
	if actor.ViaSession {
		return true
	}
	WriteError(w, http.StatusForbidden, "CREDENTIAL_MANAGEMENT_REQUIRES_SESSION",
		"credential management must come from a console session")
	return false
}

// loginRequest is the login body.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTP     string `json:"totp"`
}

// sessionUser is the account shape the console renders.
type sessionUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	HasTOTP  bool   `json:"has_totp"`
}

// originAllowed reports whether a sign-in or sign-up write may be served.
//
// Login and sign-up are the two management writes a browser can make without a
// CSRF token, because they are the requests that obtain one. A page on another
// site can therefore post a form here and sign a visitor in as the attacker's
// own account - login CSRF - and the visitor only notices once they act on
// somebody else's data.
//
// Browsers label such a request: Origin carries the posting site and modern ones
// also send Sec-Fetch-Site. Either is enough to refuse. A caller that sends
// neither - curl, the smoke test, a CI script - is let through, because a client
// that is not a browser carries no ambient credential to abuse and refusing it
// would break every scripted deployment check.
func originAllowed(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		// "null", a malformed value, and anything else that does not name a
		// host is not this site, so it is refused.
		return false
	}
	return hostWithoutPort(parsed.Host) == hostWithoutPort(r.Host)
}

// hostWithoutPort lowercases a host and drops its port. The comparison is
// site-level on purpose: the request has already arrived at this host, so what
// has to match is which site it came from, not which port that site used.
func hostWithoutPort(host string) string {
	name := strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(name); err == nil {
		name = h
	}
	return strings.ToLower(strings.Trim(name, "[]"))
}

// HandleLogin authenticates a console user and issues a session.
//
// It is the one management route reachable without an identity, because it is
// how an identity is obtained.
func (h *AuthHandlers) HandleLogin() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h == nil || h.Sessions == nil {
			WriteError(w, http.StatusServiceUnavailable, "AUTH_UNAVAILABLE", "authentication is not configured")
			return
		}

		if !originAllowed(r) {
			WriteError(w, http.StatusForbidden, "ORIGIN_REJECTED",
				"a request from another site may not sign in or sign up")
			return
		}
		var req loginRequest
		if !decodeJSONBody(w, r, &req) {
			return
		}
		req.Username = strings.TrimSpace(req.Username)
		if req.Username == "" || req.Password == "" {
			WriteError(w, http.StatusBadRequest, "INVALID_REQUEST", "a username and a password are required")
			return
		}

		// The cookie policy is decided before the credential is checked, so a
		// deployment serving the console over plain HTTP to a real host refuses
		// the login instead of handing out a cookie anyone on the path can
		// read.
		policy, err := auth.NewCookiePolicy(h.secureTransport(r), r.Host)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "HTTPS_REQUIRED",
				"the console must be reached over HTTPS, or over plain HTTP on loopback")
			return
		}

		result, err := h.Sessions.Login(
			req.Username, req.Password, req.TOTP,
			h.clientKey(r), sessionSourceIP(r), r.UserAgent(),
		)
		if err != nil {
			h.writeLoginError(w, r, err)
			return
		}

		// The audit middleware runs before this handler, but the login route is
		// public, so nothing has named an actor yet: without this the one login
		// that succeeded is recorded as coming from nobody.
		setAuditActor(r, result.User.ID, result.User.ID)
		writeSessionCookies(w, policy, result.SessionID, result.CSRFToken, result.ExpiresAtNs)
		WriteJSON(w, http.StatusOK, map[string]any{
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

// writeLoginError turns a login failure into a response.
//
// The codes are distinct so the console can react - prompt for a code, back off,
// or say the credentials are wrong - without the message ever saying which half
// of a credential was wrong. Every refusal also leaves an audit row, except the
// missing second factor: that is the ordinary first step of a two-step login
// rather than a failure, and recording it would fill the log with a row per
// correct password.
func (h *AuthHandlers) writeLoginError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrTOTPRequired):
		WriteError(w, http.StatusUnauthorized, "TOTP_REQUIRED", "a second factor is required")
	case errors.Is(err, auth.ErrTOTPInvalid):
		h.auditFailedLogin(r)
		WriteError(w, http.StatusUnauthorized, "TOTP_INVALID", "the second factor is invalid")
	case errors.Is(err, auth.ErrTOTPAlreadyUsed):
		// The code was right and had already been spent. A one-time password
		// replayed inside its own tolerance window is a credential failure, not
		// a server fault, and it is audited like one.
		h.auditFailedLogin(r)
		WriteError(w, http.StatusUnauthorized, "TOTP_ALREADY_USED",
			"that second-factor code has already been used; wait for the next one")
	case errors.Is(err, auth.ErrTooManyAttempts):
		h.auditFailedLogin(r)
		w.Header().Set("Retry-After", "60")
		WriteError(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many failed attempts; retry later")
	case errors.Is(err, auth.ErrInvalidCredentials):
		h.auditFailedLogin(r)
		WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
	default:
		// Anything else - a store error, an unopenable second factor - is a
		// server-side problem, and saying so is better than reporting it as a
		// credential failure the user would retry forever.
		h.auditFailedLogin(r)
		h.logf("api: login failed: %v", err)
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not complete the login")
	}
}

// HandleLogout ends the session the request carried.
func (h *AuthHandlers) HandleLogout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h != nil && h.Sessions != nil {
			if cookie, err := r.Cookie(auth.SessionCookieName); err == nil && cookie != nil {
				if err := h.Sessions.Logout(cookie.Value); err != nil {
					h.logf("api: logout could not end the session: %v", err)
				}
			}
		}
		clearSessionCookies(w, r, h.secureTransport(r))
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleSession reports who the caller is.
func (h *AuthHandlers) HandleSession() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := ActorFromContext(r.Context())
		if !ok {
			WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}

		body := map[string]any{
			"authenticated": true,
			"user": sessionUser{
				ID:       actor.UserID,
				Username: actor.Username,
				Role:     actor.Role,
			},
			"via_session":     actor.ViaSession,
			"must_enrol_totp": actor.MustEnrolTOTP,
		}

		// has_totp is filled from the account when it is available. A session
		// that cannot read the account still gets a usable answer: the console
		// only uses the flag to decide whether to offer enrolment.
		if h != nil && h.Accounts != nil && actor.UserID != "" {
			if account, err := h.Accounts.GetAccount(actor.UserID); err == nil {
				user := body["user"].(sessionUser)
				user.HasTOTP = account.HasTOTP
				body["user"] = user
			}
		}
		WriteJSON(w, http.StatusOK, body)
	}
}

// changePasswordRequest is the password-change body.
type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// HandleChangePassword replaces the caller's password.
//
// Every session of the account is revoked afterwards, including the one making
// the request. That is the only answer that makes a password change mean
// something: leaving the caller's own session alive would leave the old
// credential usable by anyone already holding its cookie.
func (h *AuthHandlers) HandleChangePassword() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := ActorFromContext(r.Context())
		if !ok {
			WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
		if !requireSessionActor(w, actor) {
			return
		}
		if h == nil || h.Accounts == nil || h.Sessions == nil {
			WriteError(w, http.StatusServiceUnavailable, "ACCOUNTS_UNAVAILABLE", "account management is not configured")
			return
		}

		var req changePasswordRequest
		if !decodeJSONBody(w, r, &req) {
			return
		}
		if req.CurrentPassword == "" || req.NewPassword == "" {
			WriteError(w, http.StatusBadRequest, "INVALID_REQUEST",
				"the current and the new password are both required")
			return
		}

		err := h.Accounts.ChangeAccountPassword(actor.UserID, req.CurrentPassword, req.NewPassword, h.nowNs())
		switch {
		case errors.Is(err, service.ErrCurrentPasswordWrong):
			WriteError(w, http.StatusForbidden, "PASSWORD_MISMATCH", "the current password is wrong")
			return
		case errors.Is(err, service.ErrPasswordTooShort):
			WriteError(w, http.StatusBadRequest, "PASSWORD_TOO_SHORT",
				fmt.Sprintf("the password must be at least %d characters", service.MinPasswordLength))
			return
		case errors.Is(err, service.ErrPasswordUnchanged):
			WriteError(w, http.StatusBadRequest, "PASSWORD_UNCHANGED",
				"the new password is the same as the current one")
			return
		case err != nil:
			h.logf("api: password change for user %s failed: %v", actor.UserID, err)
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not change the password")
			return
		}

		if _, err := h.Sessions.RevokeUserSessions(actor.UserID); err != nil {
			h.logf("api: could not revoke the sessions of user %s after a password change: %v", actor.UserID, err)
		}
		clearSessionCookies(w, r, h.secureTransport(r))
		h.logf("api: user %s changed their password; every session was ended", actor.UserID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleStartTOTPEnrolment returns a fresh second-factor secret.
//
// The secret is not stored: it is written only once the account proves it can
// generate a code from it, which is what stops a mistyped enrolment from locking
// the account out of its own console.
func (h *AuthHandlers) HandleStartTOTPEnrolment(issuer string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := ActorFromContext(r.Context())
		if !ok {
			WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
		if !requireSessionActor(w, actor) {
			return
		}
		if h == nil || h.Accounts == nil {
			WriteError(w, http.StatusServiceUnavailable, "ACCOUNTS_UNAVAILABLE", "account management is not configured")
			return
		}

		enrolment, err := h.Accounts.StartTOTPEnrolment(actor.UserID, issuer)
		switch {
		case errors.Is(err, service.ErrTOTPAlreadyEnrolled):
			WriteError(w, http.StatusConflict, "TOTP_ALREADY_ENROLLED",
				"the account already has a second factor; remove it first to enrol another")
			return
		case err != nil:
			h.logf("api: could not start second-factor enrolment for user %s: %v", actor.UserID, err)
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not start enrolment")
			return
		}
		WriteJSON(w, http.StatusOK, enrolment)
	}
}

// confirmTOTPRequest is the enrolment-confirmation body.
type confirmTOTPRequest struct {
	Secret string `json:"secret"`
	Code   string `json:"code"`
}

// HandleConfirmTOTPEnrolment stores the secret once a code proves it works.
func (h *AuthHandlers) HandleConfirmTOTPEnrolment() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := ActorFromContext(r.Context())
		if !ok {
			WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
		if !requireSessionActor(w, actor) {
			return
		}
		if h == nil || h.Accounts == nil {
			WriteError(w, http.StatusServiceUnavailable, "ACCOUNTS_UNAVAILABLE", "account management is not configured")
			return
		}

		var req confirmTOTPRequest
		if !decodeJSONBody(w, r, &req) {
			return
		}
		if req.Secret == "" || req.Code == "" {
			WriteError(w, http.StatusBadRequest, "INVALID_REQUEST", "the secret and a code are both required")
			return
		}

		err := h.Accounts.ConfirmTOTPEnrolment(actor.UserID, req.Secret, req.Code, h.nowNs())
		switch {
		case errors.Is(err, service.ErrTOTPCodeInvalid):
			WriteError(w, http.StatusBadRequest, "TOTP_INVALID",
				"the code does not match the secret; check that the app was set up correctly")
			return
		case errors.Is(err, service.ErrTOTPAlreadyEnrolled):
			WriteError(w, http.StatusConflict, "TOTP_ALREADY_ENROLLED", "the account already has a second factor")
			return
		case errors.Is(err, service.ErrNoPepper):
			WriteError(w, http.StatusServiceUnavailable, "NO_PEPPER",
				"no key pepper is configured, so a second factor cannot be stored")
			return
		case err != nil:
			h.logf("api: could not confirm second-factor enrolment for user %s: %v", actor.UserID, err)
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not complete enrolment")
			return
		}
		h.logf("api: user %s enrolled a second factor", actor.UserID)
		WriteJSON(w, http.StatusOK, map[string]any{"has_totp": true})
	}
}

// HandleDisableTOTP removes the caller's second factor.
func (h *AuthHandlers) HandleDisableTOTP() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := ActorFromContext(r.Context())
		if !ok {
			WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
		if !requireSessionActor(w, actor) {
			return
		}
		if h == nil || h.Accounts == nil {
			WriteError(w, http.StatusServiceUnavailable, "ACCOUNTS_UNAVAILABLE", "account management is not configured")
			return
		}

		err := h.Accounts.DisableTOTP(actor.UserID, h.nowNs())
		switch {
		case errors.Is(err, service.ErrTOTPNotEnrolled):
			WriteError(w, http.StatusConflict, "TOTP_NOT_ENROLLED", "the account has no second factor")
			return
		case err != nil:
			h.logf("api: could not remove the second factor of user %s: %v", actor.UserID, err)
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not remove the second factor")
			return
		}
		// Removing the second factor is a downgrade of the account's security,
		// so every other session is ended: whoever else was logged in should have
		// to authenticate again rather than keep a session that was established
		// under the stronger policy.
		if _, err := h.Sessions.RevokeUserSessions(actor.UserID); err != nil {
			h.logf("api: could not revoke the sessions of user %s after removing a second factor: %v", actor.UserID, err)
		}
		clearSessionCookies(w, r, h.secureTransport(r))
		h.logf("api: user %s removed their second factor; every session was ended", actor.UserID)
		WriteJSON(w, http.StatusOK, map[string]any{"has_totp": false})
	}
}

// writeSessionCookies issues the session and the double-submit token.
func writeSessionCookies(w http.ResponseWriter, policy auth.CookiePolicy, sessionID, csrfToken string, expiresAtNs int64) {
	// A cookie's Max-Age is in seconds and is relative, so it is derived from
	// the session's absolute expiry. It is set to the remaining lifetime rather
	// than to the full TTL so a client cannot keep sending a cookie the server
	// has already expired.
	maxAge := int(time.Until(time.Unix(0, expiresAtNs)).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    sessionID,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   policy.Secure,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:  auth.CSRFCookieName,
		Value: csrfToken,
		Path:  "/",
		// Readable by the page on purpose: the whole mechanism is the page
		// echoing this value back in a header, and a script cannot read an
		// HttpOnly cookie.
		MaxAge:   maxAge,
		HttpOnly: false,
		Secure:   policy.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// clearSessionCookies removes both cookies. secure is the transport decision the
// handler already made (see secureTransport), passed in rather than re-derived
// so the clearing cookie carries the same Secure attribute as the one it clears.
func clearSessionCookies(w http.ResponseWriter, r *http.Request, secure bool) {
	policy, err := auth.NewCookiePolicy(secure, r.Host)
	if err != nil {
		// The request could not have carried a usable cookie anyway, but the
		// clearing still has to happen so a stale cookie is not left behind.
		policy = auth.CookiePolicy{Secure: secure}
	}
	for _, name := range []string{auth.SessionCookieName, auth.CSRFCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: name == auth.SessionCookieName,
			Secure:   policy.Secure,
			SameSite: http.SameSiteStrictMode,
		})
	}
}

// decodeJSONBody reads a JSON request body into target, answering the client
// and returning false when it cannot.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.Body == nil {
		WriteError(w, http.StatusBadRequest, "INVALID_REQUEST", "a request body is required")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			WriteError(w, http.StatusBadRequest, "INVALID_REQUEST", "a request body is required")
			return false
		}
		WriteError(w, http.StatusBadRequest, "INVALID_REQUEST", "the request body is not valid JSON")
		return false
	}
	return true
}

// sessionSourceIP extracts the address recorded on a session.
//
// It reads RemoteAddr rather than any forwarded header: the recorded address is
// for an operator recognising a session in a list, and a header a client
// controls would let them write whatever they liked there. The forwarded address
// is already consulted where it matters - the failure limiter, which only trusts
// it from a configured proxy.
func sessionSourceIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := r.RemoteAddr
	if idx := strings.LastIndexByte(host, ':'); idx > 0 {
		host = host[:idx]
	}
	return strings.Trim(host, "[]")
}

// sessionInfoForWebUI is the shape /ui/session.json serves.
//
// It is what tells the single-page console whether to render the login page,
// which is why it carries the same information as the session endpoint: the
// page must not have to make two calls to decide.
type sessionInfoForWebUI struct {
	AuthRequired  bool         `json:"auth_required"`
	Authenticated bool         `json:"authenticated"`
	User          *sessionUser `json:"user,omitempty"`
	MustEnrolTOTP bool         `json:"must_enrol_totp,omitempty"`
}
