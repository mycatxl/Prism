package api

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"prism/internal/auth"
	"prism/internal/config"
	"prism/internal/model"
)

// Actor is who a management request comes from.
//
// It replaces the single "is this the admin token" answer the console used to
// get. Two kinds of caller reach the management API - a person through the
// console, and an automation holding an admin key - and they need to be told
// apart, because only the first one needs CSRF protection and only the second
// one can be revoked by deleting a key.
type Actor struct {
	UserID   string
	Username string
	Role     string
	// ViaSession is true when the caller authenticated with a console session
	// cookie rather than an access key.
	ViaSession bool
	// MustEnrolTOTP marks a session that may only reach the second-factor
	// enrolment endpoints. It is set when the policy requires administrators to
	// have a second factor and this account has none: refusing the login
	// outright would leave no way to enrol.
	MustEnrolTOTP bool
	// HasTOTP reports whether the account behind this actor has a second factor.
	//
	// It is read from the account, not from the session, and it is what
	// /ui/session.json reports as has_totp. The console's security page decides
	// from that answer whether to show the factor as enabled or to push the user
	// into enrolling, so a wrong answer hides a protection or claims one that is
	// not there. An admin key has no account behind it in this sense and leaves
	// the field false.
	HasTOTP bool
}

// IsAdmin reports whether the caller may use the administrator surface.
func (a Actor) IsAdmin() bool {
	return a.Role == model.UserRoleAdmin
}

// actorContextKey holds the Actor of one request.
type actorContextKey struct{}

// ActorFromContext returns the actor a request was authenticated as.
func ActorFromContext(ctx context.Context) (Actor, bool) {
	if ctx == nil {
		return Actor{}, false
	}
	actor, ok := ctx.Value(actorContextKey{}).(Actor)
	return actor, ok
}

// SessionResolver is the console-session half of the authentication surface.
//
// It is an interface so the API package does not depend on how sessions are
// stored, and so the tests can drive the middleware without a database.
type SessionResolver interface {
	// Authenticate resolves a session id to its owner and session.
	Authenticate(sessionID string) (*model.User, *model.Session, error)
	// Login authenticates a username, password and optional second factor.
	Login(username, password, totpCode, clientKey, ip, userAgent string) (auth.LoginResult, error)
	// Logout ends one session.
	Logout(sessionID string) error
	// RevokeUserSessions ends every session of one user.
	RevokeUserSessions(userID string) (int64, error)
}

// AdminKeyResolver is the automation half of the authentication surface.
//
// An admin key is an access key with the admin scope, held by an administrator.
// It exists so a script can drive the API without a browser session, and it is
// deliberately not accepted for anything that manages credentials: a stolen
// automation key must not be able to mint more keys or change a password.
type AdminKeyResolver interface {
	// VerifyAdminKey resolves an admin-scope credential to its owner.
	// VerifyAdminKey resolves an admin-scope credential to its owner. clientAddr
	// is the request's source address, which the key's own IP allowlist is
	// checked against.
	VerifyAdminKey(credential string, clientAddr netip.Addr) (AdminKeyIdentity, bool)
}

// AdminKeyIdentity is what an admin key resolves to.
type AdminKeyIdentity struct {
	KeyID  string
	UserID string
	Role   string
}

// Identity authenticates management requests.
type Identity struct {
	// Sessions resolves console cookies. Nil disables cookie authentication.
	Sessions SessionResolver
	// AdminKeys resolves automation credentials. Nil disables that path.
	AdminKeys AdminKeyResolver
	// FailureLimiter blocks and counts failed management authentication attempts.
	// Login failures are counted by the session service itself; this limiter
	// covers credentials that were actually presented and did not resolve. A
	// request that presents none is an anonymous visitor, not a failed
	// authentication, so it is not counted - otherwise any page on the internet
	// could spend an operator's failure budget and lock them out of the console.
	FailureLimiter *AuthFailureLimiter
	// RequireAdminTOTP makes a second factor mandatory for administrators. An
	// administrator without one is let in only far enough to enrol.
	RequireAdminTOTP func() bool
	// RegistrationMode reports the deployment's sign-up policy so the console
	// knows whether to offer the form. Nil is read as off, the same fail-closed
	// reading the register endpoint applies.
	RegistrationMode func() config.RegistrationMode
	// Logf receives authentication refusals. Nil silences them.
	Logf func(format string, args ...any)
}

func (i *Identity) logf(format string, args ...any) {
	if i != nil && i.Logf != nil {
		i.Logf(format, args...)
	}
}

// publicPaths are the management routes that must be reachable without an
// identity, because they are how a caller obtains one.
//
// The keys are "METHOD path" rather than route patterns, and they are matched
// against the raw request rather than against r.Pattern. Pattern is only filled
// in by the mux that finally serves the request, and this middleware runs
// before that inner mux is reached - so matching on Pattern here would compare
// against the outer "/api/" match and let nothing through.
var publicPaths = map[string]bool{
	"POST /api/v1/auth/login":    true,
	"POST /api/v1/auth/register": true,
}

// Middleware authenticates a management request and notes the actor.
//
// It fails closed: with no resolver configured nothing gets through, which is
// what makes this safe to install before every account exists.
//
// The public paths return before the limiter check, on purpose: they are the
// routes that spend their own budget, and each of them enforces the resulting
// block itself - the login path through the session service, sign-up through
// AuthHandlers.refuseIfBlocked. What this middleware must not do is answer 429
// for a path it did not count, because that would give a caller a cheap way to
// discover a block and would move the sign-in refusal away from the one place
// that knows whether a second factor is still owed.
func (i *Identity) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicManagementPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		clientIP := ""
		if i != nil && i.FailureLimiter != nil {
			clientIP = i.FailureLimiter.ClientIP(r)
			if blocked, retryAfter := i.FailureLimiter.Blocked(clientIP); blocked {
				if retryAfter > 0 {
					w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
				}
				WriteError(w, http.StatusTooManyRequests, "RATE_LIMITED",
					"too many failed authentication attempts; retry later")
				return
			}
		}

		actor, ok := i.resolve(r)
		if !ok {
			// Only a request that presented a bearer credential counts as a
			// failed authentication. A request with no credential at all, or
			// with nothing but an invalid or expired session cookie, is not an
			// attempt to authenticate: a session id is 256 bits of randomness,
			// so guessing one is pointless, and a browser left holding a stale
			// cookie would otherwise lock itself out by polling.
			if _, presented := bearerCredential(r); presented && i != nil && i.FailureLimiter != nil {
				i.FailureLimiter.RecordFailure(clientIP)
			}
			WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}

		// A cookie rides along with whatever the browser is asked to send, so a
		// state-changing request needs proof that it came from the console
		// rather than from another site.
		if actor.ViaSession && !isSafeMethod(r.Method) && !csrfMatches(r) {
			i.logf("api: refused a state-changing request from user %s: the CSRF token did not match", actor.UserID)
			WriteError(w, http.StatusForbidden, "CSRF_FAILED",
				"the "+auth.CSRFHeaderName+" header must repeat the "+auth.CSRFCookieName+" cookie")
			return
		}

		if actor.MustEnrolTOTP && !isTOTPEnrolmentPath(r.URL.Path) {
			WriteError(w, http.StatusForbidden, "TOTP_ENROLMENT_REQUIRED",
				"an administrator must enrol a second factor before using the console")
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorContextKey{}, actor)))
	})
}

// resolve answers the actor behind a request, or ok=false.
//
// The session cookie is tried first. A browser sends it automatically, and an
// automation credential is an explicit header, so a request that carries both is
// a browser with an odd extension rather than a script - preferring the session
// keeps the CSRF requirement attached to the cookie, which is the one that needs
// it.
func (i *Identity) resolve(r *http.Request) (Actor, bool) {
	if i == nil {
		return Actor{}, false
	}

	if actor, ok := i.resolveSession(r); ok {
		return actor, true
	}
	if actor, ok := i.resolveAdminKey(r); ok {
		return actor, true
	}
	return Actor{}, false
}

func (i *Identity) resolveSession(r *http.Request) (Actor, bool) {
	if i.Sessions == nil {
		return Actor{}, false
	}
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || cookie == nil || cookie.Value == "" {
		return Actor{}, false
	}
	user, _, err := i.Sessions.Authenticate(cookie.Value)
	if err != nil || user == nil {
		return Actor{}, false
	}

	actor := Actor{
		UserID:     user.ID,
		Username:   user.Username,
		Role:       user.Role,
		ViaSession: true,
		HasTOTP:    len(user.TOTPSecretEnc) > 0,
	}
	// An administrator without a second factor is held at the enrolment
	// endpoints when the policy requires one. Letting them in and asking later
	// would mean the requirement could be ignored for the length of a session.
	if i.requiresAdminTOTP() && actor.IsAdmin() && len(user.TOTPSecretEnc) == 0 {
		actor.MustEnrolTOTP = true
	}
	return actor, true
}

func (i *Identity) resolveAdminKey(r *http.Request) (Actor, bool) {
	if i.AdminKeys == nil {
		return Actor{}, false
	}
	credential, ok := bearerCredential(r)
	if !ok {
		return Actor{}, false
	}
	identity, ok := i.AdminKeys.VerifyAdminKey(credential, i.clientAddr(r))
	if !ok {
		return Actor{}, false
	}
	return Actor{
		UserID:     identity.UserID,
		Role:       identity.Role,
		ViaSession: false,
	}, true
}

// clientAddr names the source address of a request for the per-key IP
// allowlist, using the same trusted-proxy rule the auth-failure limiter applies.
// Without a limiter only the peer address is read, which is the strictest
// reading: an allowlisted key then refuses the request.
func (i *Identity) clientAddr(r *http.Request) netip.Addr {
	if i == nil || i.FailureLimiter == nil {
		return requestClientAddr(r, nil)
	}
	return requestClientAddr(r, i.FailureLimiter.ClientIP)
}
func (i *Identity) requiresAdminTOTP() bool {
	return i.RequireAdminTOTP != nil && i.RequireAdminTOTP()
}

// bearerCredential extracts the token from an Authorization header.
func bearerCredential(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, prefix) {
		return "", false
	}
	credential := strings.TrimSpace(value[len(prefix):])
	if credential == "" {
		return "", false
	}
	return credential, true
}

// csrfMatches performs the double-submit comparison.
//
// The header is compared against the cookie, not against anything stored: the
// protection is that another site cannot read the cookie, so it cannot know what
// to put in the header. SameSite=Strict already stops the browser sending the
// session on a cross-site request; this is the second lock on the same door.
func csrfMatches(r *http.Request) bool {
	cookie, err := r.Cookie(auth.CSRFCookieName)
	if err != nil || cookie == nil {
		return false
	}
	return auth.VerifyCSRFToken(cookie.Value, r.Header.Get(auth.CSRFHeaderName))
}

// isSafeMethod reports whether a method is read-only by definition, and so needs
// no CSRF token.
func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// sessionInfo answers what /ui/session.json should say for a request.
//
// It never fails: an unauthenticated caller is the normal case for that file
// (it is served before the login page renders), so a failure would turn the
// console's first screen into an error.
//
// Only the session cookie is resolved here, never a bearer credential, and that
// is a security boundary rather than an accident of implementation. /ui/ is
// registered on the outer mux, outside Middleware (server.go), so a request to
// this path gets none of what /api/ gives a credential: no failure counting, no
// block for an address that has spent its budget, no audit row, no CSRF rule, no
// role gate. Letting a bearer credential in would have made this file a free
// oracle - "is this admin key still valid?" asked from an address that is already
// blocked, as often as the asker likes, with nothing recorded anywhere. A session
// cookie is the credential this file exists to report on, and it is the only one
// it now accepts.
func (i *Identity) sessionInfo(r *http.Request) sessionInfoForWebUI {
	mode := i.registrationMode()
	actor, ok := i.resolveSession(r)
	if !ok {
		return sessionInfoForWebUI{Authenticated: false, RegistrationMode: string(mode)}
	}
	// has_totp is filled from the account the session resolved to, which is the
	// same answer the login response and GET /api/v1/auth/session give. Without
	// it the console's security page would show a second factor as not enrolled
	// immediately after a reload, and offer to enrol one that is already there.
	return sessionInfoForWebUI{
		Authenticated: true,
		User: &sessionUser{
			ID:       actor.UserID,
			Username: actor.Username,
			Role:     actor.Role,
			HasTOTP:  actor.HasTOTP,
		},
		MustEnrolTOTP:    actor.MustEnrolTOTP,
		RegistrationMode: string(mode),
	}
}

// registrationMode reads the deployment's sign-up policy, treating a nil reader
// as off. A policy that cannot be read must not advertise a door.
func (i *Identity) registrationMode() config.RegistrationMode {
	if i == nil || i.RegistrationMode == nil {
		return config.RegistrationOff
	}
	mode := i.RegistrationMode()
	if !mode.Valid() {
		return config.RegistrationOff
	}
	return mode
}

// isPublicManagementPath reports whether a route is reachable without an
// identity.
func isPublicManagementPath(r *http.Request) bool {
	return publicPaths[r.Method+" "+r.URL.Path]
}

// isTOTPEnrolmentPath reports whether a path is part of enrolling a second
// factor, and so reachable by a session that is held at that step.
//
// Logging out and reading the session are included: an administrator who cannot
// enrol yet must still be able to see who they are and to leave.
func isTOTPEnrolmentPath(path string) bool {
	switch path {
	case "/api/v1/auth/totp/enrol",
		"/api/v1/auth/totp/confirm",
		"/api/v1/auth/logout",
		"/api/v1/auth/session":
		return true
	default:
		return false
	}
}
