package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/auth"
	"prism/internal/model"
	"prism/internal/service"
)

// --- fakes -------------------------------------------------------------------

// fakeSessions stands in for the session service.
type fakeSessions struct {
	mu sync.Mutex

	// authenticate answers a fixed user and session, or an error.
	user    *model.User
	session *model.Session
	authErr error

	loginResult auth.LoginResult
	loginErr    error
	loginCalls  []loginCall

	logoutCalls  []string
	revokeCalls  []string
	revokedCount int64
}

type loginCall struct {
	username  string
	password  string
	totp      string
	clientKey string
	ip        string
	userAgent string
}

func (f *fakeSessions) Authenticate(sessionID string) (*model.User, *model.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.authErr != nil {
		return nil, nil, f.authErr
	}
	if f.user == nil {
		return nil, nil, auth.ErrNoSession
	}
	return f.user, f.session, nil
}

func (f *fakeSessions) Login(username, password, totpCode, clientKey, ip, userAgent string) (auth.LoginResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loginCalls = append(f.loginCalls, loginCall{username, password, totpCode, clientKey, ip, userAgent})
	if f.loginErr != nil {
		return auth.LoginResult{}, f.loginErr
	}
	return f.loginResult, nil
}

func (f *fakeSessions) Logout(sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logoutCalls = append(f.logoutCalls, sessionID)
	return nil
}

func (f *fakeSessions) RevokeUserSessions(userID string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokeCalls = append(f.revokeCalls, userID)
	return f.revokedCount, nil
}

// fakeAdminKeys accepts one credential.
type fakeAdminKeys struct {
	credential string
	identity   AdminKeyIdentity
}

func (f fakeAdminKeys) VerifyAdminKey(credential string) (AdminKeyIdentity, bool) {
	if credential != f.credential || f.credential == "" {
		return AdminKeyIdentity{}, false
	}
	return f.identity, true
}

// fakeAccounts stands in for the account service.
type fakeAccounts struct {
	mu sync.Mutex

	account service.Account
	getErr  error

	enrolment    service.TOTPEnrolment
	enrolErr     error
	confirmErr   error
	disableErr   error
	changePwErr  error
	changePwArgs []string
	disableCalls int
	confirmCalls int
}

func (f *fakeAccounts) GetAccount(string) (service.Account, error) {
	return f.account, f.getErr
}

func (f *fakeAccounts) StartTOTPEnrolment(string, string) (service.TOTPEnrolment, error) {
	return f.enrolment, f.enrolErr
}

func (f *fakeAccounts) ConfirmTOTPEnrolment(_, _, _ string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirmCalls++
	return f.confirmErr
}

func (f *fakeAccounts) DisableTOTP(string, int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disableCalls++
	return f.disableErr
}

func (f *fakeAccounts) ChangeAccountPassword(_, current, next string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changePwArgs = []string{current, next}
	return f.changePwErr
}

// --- identity middleware ------------------------------------------------------

func adminUser() *model.User {
	return &model.User{
		ID:       "u-admin",
		Username: "admin",
		Role:     model.UserRoleAdmin,
		Status:   model.UserStatusActive,
	}
}

// sessionRequest builds a request carrying a session cookie.
func sessionRequest(method, path, sessionID string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	if sessionID != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionID})
	}
	return req
}

// A valid session reaches the handler and the actor is available to it.
func TestIdentity_SessionIsAccepted(t *testing.T) {
	sessions := &fakeSessions{
		user:    adminUser(),
		session: &model.Session{IDHash: "h", UserID: "u-admin"},
	}
	identity := &Identity{Sessions: sessions}

	var seen Actor
	handler := identity.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = ActorFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, sessionRequest(http.MethodGet, "/api/v1/system/info", "a-session"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if seen.UserID != "u-admin" || !seen.ViaSession || !seen.IsAdmin() {
		t.Fatalf("actor = %+v", seen)
	}
}

// No session and no credential is refused.
func TestIdentity_RefusesAnAnonymousRequest(t *testing.T) {
	identity := &Identity{Sessions: &fakeSessions{}}
	handler := identity.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the handler was reached without an identity")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// An identity with no resolvers at all lets nothing through: it is installed
// before any account exists, so it has to fail closed.
func TestIdentity_WithoutResolversFailsClosed(t *testing.T) {
	handler := (&Identity{}).Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the handler was reached with no resolvers configured")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, sessionRequest(http.MethodGet, "/api/v1/system/info", "a-session"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// The login route is reachable without an identity, because it is how one is
// obtained.
func TestIdentity_LoginPathIsPublic(t *testing.T) {
	identity := &Identity{Sessions: &fakeSessions{}}
	handler := identity.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("login was not reachable: status = %d", rec.Code)
	}
}

// The public list is exact: another method on the same path is not public.
func TestIdentity_OnlyTheLoginMethodIsPublic(t *testing.T) {
	identity := &Identity{Sessions: &fakeSessions{}}
	handler := identity.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a GET on the login path", rec.Code)
	}
}

// A state-changing request over a cookie needs the double-submit token.
func TestIdentity_CSRFIsRequiredForSessionWrites(t *testing.T) {
	sessions := &fakeSessions{
		user:    adminUser(),
		session: &model.Session{IDHash: "h", UserID: "u-admin"},
	}
	identity := &Identity{Sessions: sessions}
	reached := false
	handler := identity.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	const token = "the-csrf-token"

	cases := []struct {
		name       string
		method     string
		csrfCookie string
		csrfHeader string
		want       int
	}{
		{"no token at all", http.MethodPost, "", "", http.StatusForbidden},
		{"cookie only", http.MethodPost, token, "", http.StatusForbidden},
		{"header only", http.MethodPost, "", token, http.StatusForbidden},
		{"a different token", http.MethodPost, token, "another", http.StatusForbidden},
		{"matching token", http.MethodPost, token, token, http.StatusOK},
		{"matching on a delete", http.MethodDelete, token, token, http.StatusOK},
		// A read needs no token: requiring one would break every page load.
		{"a read needs no token", http.MethodGet, "", "", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			req := sessionRequest(tc.method, "/api/v1/platforms", "a-session")
			if tc.csrfCookie != "" {
				req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: tc.csrfCookie})
			}
			if tc.csrfHeader != "" {
				req.Header.Set(auth.CSRFHeaderName, tc.csrfHeader)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if want := tc.want == http.StatusOK; reached != want {
				t.Fatalf("handler reached = %v, want %v", reached, want)
			}
		})
	}
}

// An admin key needs no CSRF token: there is no cookie for another site to ride
// on, so the mechanism does not apply.
func TestIdentity_AdminKeyNeedsNoCSRF(t *testing.T) {
	identity := &Identity{
		AdminKeys: fakeAdminKeys{
			credential: "pk_key",
			identity:   AdminKeyIdentity{KeyID: "k-1", UserID: "u-admin", Role: model.UserRoleAdmin},
		},
	}
	var seen Actor
	handler := identity.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = ActorFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/platforms", nil)
	req.Header.Set("Authorization", "Bearer pk_key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if seen.ViaSession {
		t.Fatal("an admin key was recorded as a session")
	}
	if seen.UserID != "u-admin" {
		t.Fatalf("actor user = %q", seen.UserID)
	}
}

// The session is preferred when a request carries both, so the CSRF requirement
// stays attached to the cookie - the credential that needs it.
func TestIdentity_PrefersTheSessionOverAKey(t *testing.T) {
	identity := &Identity{
		Sessions: &fakeSessions{
			user:    adminUser(),
			session: &model.Session{IDHash: "h", UserID: "u-admin"},
		},
		AdminKeys: fakeAdminKeys{
			credential: "pk_key",
			identity:   AdminKeyIdentity{KeyID: "k-1", UserID: "u-other", Role: model.UserRoleAdmin},
		},
	}
	var seen Actor
	handler := identity.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = ActorFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := sessionRequest(http.MethodPost, "/api/v1/platforms", "a-session")
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "t"})
	req.Header.Set(auth.CSRFHeaderName, "t")
	req.Header.Set("Authorization", "Bearer pk_key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !seen.ViaSession || seen.UserID != "u-admin" {
		t.Fatalf("actor = %+v, want the session's owner", seen)
	}
}

// The transitional token still works, and is recorded as an administrator with
// no user - which is exactly the property the session flow replaces.
func TestIdentity_LegacyTokenIsAccepted(t *testing.T) {
	identity := &Identity{LegacyAdminToken: "the-shared-token"}
	var seen Actor
	handler := identity.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = ActorFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	req.Header.Set("Authorization", "Bearer the-shared-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !seen.IsAdmin() {
		t.Fatalf("actor = %+v, want an administrator", seen)
	}
	if seen.UserID != "" {
		t.Fatalf("the shared token was attributed to user %q", seen.UserID)
	}

	// A different token is refused.
	bad := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	bad.Header.Set("Authorization", "Bearer the-wrong-token")
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong shared token got %d, want 401", badRec.Code)
	}
}

// An administrator without a second factor, when the policy requires one, is
// held at the enrolment endpoints.
func TestIdentity_MustEnrolTOTPGate(t *testing.T) {
	sessions := &fakeSessions{
		user:    adminUser(),
		session: &model.Session{IDHash: "h", UserID: "u-admin"},
	}
	identity := &Identity{
		Sessions:         sessions,
		RequireAdminTOTP: func() bool { return true },
	}
	reached := false
	handler := identity.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	// The console itself is refused.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, sessionRequest(http.MethodGet, "/api/v1/platforms", "a-session"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if reached {
		t.Fatal("the console was reached without a second factor")
	}

	// Enrolling is allowed, or the requirement could never be satisfied.
	for _, path := range []string{
		"/api/v1/auth/totp/enrol",
		"/api/v1/auth/totp/confirm",
		"/api/v1/auth/session",
		"/api/v1/auth/logout",
	} {
		reached = false
		req := sessionRequest(http.MethodPost, path, "a-session")
		// The enrolment calls are writes, so they need the CSRF token.
		req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "t"})
		req.Header.Set(auth.CSRFHeaderName, "t")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s was refused: status = %d (body %s)", path, rec.Code, rec.Body.String())
		}
		if !reached {
			t.Errorf("%s did not reach the handler", path)
		}
	}
}

// An administrator who already has a second factor is not held at the gate.
func TestIdentity_GateIsSkippedWhenTheFactorExists(t *testing.T) {
	user := adminUser()
	user.TOTPSecretEnc = []byte("sealed")
	identity := &Identity{
		Sessions:         &fakeSessions{user: user, session: &model.Session{IDHash: "h"}},
		RequireAdminTOTP: func() bool { return true },
	}
	handler := identity.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, sessionRequest(http.MethodGet, "/api/v1/platforms", "a-session"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// A plain user is not asked for a second factor even when administrators are.
func TestIdentity_GateAppliesOnlyToAdministrators(t *testing.T) {
	user := adminUser()
	user.Role = model.UserRoleUser
	identity := &Identity{
		Sessions:         &fakeSessions{user: user, session: &model.Session{IDHash: "h"}},
		RequireAdminTOTP: func() bool { return true },
	}
	handler := identity.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, sessionRequest(http.MethodGet, "/api/v1/platforms", "a-session"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// A session that no longer resolves is refused even though the cookie is there.
func TestIdentity_RefusesARevokedSession(t *testing.T) {
	identity := &Identity{Sessions: &fakeSessions{authErr: auth.ErrNoSession}}
	handler := identity.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a revoked session reached the handler")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, sessionRequest(http.MethodGet, "/api/v1/system/info", "a-stale-session"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// --- the auth endpoints -------------------------------------------------------

func newAuthHandlers(sessions *fakeSessions, accounts *fakeAccounts) *AuthHandlers {
	return &AuthHandlers{
		Sessions:  sessions,
		Accounts:  accounts,
		ClientKey: func(*http.Request) string { return "203.0.113.9" },
	}
}

// A successful login sets both cookies with the attributes the browser needs to
// keep them safe.
func TestHandleLogin_IssuesSecureCookies(t *testing.T) {
	sessions := &fakeSessions{
		loginResult: auth.LoginResult{
			SessionID:   "the-session-id",
			CSRFToken:   "the-csrf-token",
			User:        adminUser(),
			ExpiresAtNs: time.Now().Add(time.Hour).UnixNano(),
		},
	}
	handler := newAuthHandlers(sessions, &fakeAccounts{}).HandleLogin()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"username":"admin","password":"a-long-enough-password"}`))
	req.Host = "127.0.0.1:8080" // plain HTTP on loopback is allowed
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	cookies := rec.Result().Cookies()
	byName := make(map[string]*http.Cookie, len(cookies))
	for _, cookie := range cookies {
		byName[cookie.Name] = cookie
	}

	sessionCookie := byName[auth.SessionCookieName]
	if sessionCookie == nil {
		t.Fatal("no session cookie was set")
	}
	if sessionCookie.Value != "the-session-id" {
		t.Fatalf("session cookie = %q", sessionCookie.Value)
	}
	if !sessionCookie.HttpOnly {
		t.Error("the session cookie is not HttpOnly, so a script could read it")
	}
	if sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", sessionCookie.SameSite)
	}
	if sessionCookie.Secure {
		t.Error("the session cookie is Secure on plain HTTP, so the browser would not send it back")
	}
	if sessionCookie.Path != "/" {
		t.Errorf("path = %q, want /", sessionCookie.Path)
	}

	csrfCookie := byName[auth.CSRFCookieName]
	if csrfCookie == nil {
		t.Fatal("no CSRF cookie was set")
	}
	if csrfCookie.HttpOnly {
		t.Error("the CSRF cookie is HttpOnly, so the page cannot read it and the mechanism cannot work")
	}

	// The body tells the console who it now is.
	var body struct {
		Authenticated bool        `json:"authenticated"`
		User          sessionUser `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (body %s)", err, rec.Body.String())
	}
	if !body.Authenticated || body.User.Username != "admin" || body.User.Role != model.UserRoleAdmin {
		t.Fatalf("body = %+v", body)
	}
}

// Over TLS the cookie is Secure.
func TestHandleLogin_OverTLSIsSecure(t *testing.T) {
	sessions := &fakeSessions{
		loginResult: auth.LoginResult{
			SessionID:   "s",
			CSRFToken:   "c",
			User:        adminUser(),
			ExpiresAtNs: time.Now().Add(time.Hour).UnixNano(),
		},
	}
	handler := newAuthHandlers(sessions, &fakeAccounts{}).HandleLogin()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"username":"admin","password":"a-long-enough-password"}`))
	req.Host = "prism.example.com"
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	for _, cookie := range rec.Result().Cookies() {
		if !cookie.Secure {
			t.Errorf("cookie %s is not Secure over TLS", cookie.Name)
		}
	}
}

// Plain HTTP to a real host is refused rather than served insecurely.
func TestHandleLogin_RefusesPlainHTTPOnARealHost(t *testing.T) {
	sessions := &fakeSessions{
		loginResult: auth.LoginResult{SessionID: "s", CSRFToken: "c", User: adminUser()},
	}
	handler := newAuthHandlers(sessions, &fakeAccounts{}).HandleLogin()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"username":"admin","password":"a-long-enough-password"}`))
	req.Host = "prism.example.com"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("a session cookie was issued over plain HTTP to a real host")
	}
	if got := sessions.loginCalls; len(got) != 0 {
		t.Fatalf("the credential was checked %d times although the transport was refused", len(got))
	}
}

// Every failure maps to a distinct code without ever saying which half of the
// credential was wrong.
func TestHandleLogin_ErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		want     int
		wantCode string
	}{
		{"bad credentials", auth.ErrInvalidCredentials, http.StatusUnauthorized, "UNAUTHORIZED"},
		{"second factor required", auth.ErrTOTPRequired, http.StatusUnauthorized, "TOTP_REQUIRED"},
		{"bad second factor", auth.ErrTOTPInvalid, http.StatusUnauthorized, "TOTP_INVALID"},
		{"rate limited", auth.ErrTooManyAttempts, http.StatusTooManyRequests, "RATE_LIMITED"},
		{"a server fault", errors.New("the database is gone"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := newAuthHandlers(&fakeSessions{loginErr: tc.err}, &fakeAccounts{}).HandleLogin()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
				strings.NewReader(`{"username":"admin","password":"whatever-long-enough"}`))
			req.Host = "127.0.0.1"
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
			}
			if body.Error.Code != tc.wantCode {
				t.Fatalf("error code = %q, want %q", body.Error.Code, tc.wantCode)
			}
			// No response ever echoes the password.
			if strings.Contains(rec.Body.String(), "whatever-long-enough") {
				t.Fatal("the response body contains the password")
			}
		})
	}
}

// A missing field is a bad request, not a credential failure.
func TestHandleLogin_RejectsAnIncompleteBody(t *testing.T) {
	handler := newAuthHandlers(&fakeSessions{}, &fakeAccounts{}).HandleLogin()

	for _, body := range []string{
		`{"password":"a-long-enough-password"}`,
		`{"username":"admin"}`,
		`{}`,
		`not json`,
		``,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
		req.Host = "127.0.0.1"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q got %d, want 400", body, rec.Code)
		}
	}
}

// Logout clears both cookies and ends the session.
func TestHandleLogout(t *testing.T) {
	sessions := &fakeSessions{}
	handler := newAuthHandlers(sessions, &fakeAccounts{}).HandleLogout()

	req := sessionRequest(http.MethodPost, "/api/v1/auth/logout", "the-session-id")
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if len(sessions.logoutCalls) != 1 || sessions.logoutCalls[0] != "the-session-id" {
		t.Fatalf("logout calls = %v", sessions.logoutCalls)
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.MaxAge >= 0 {
			t.Errorf("cookie %s was not expired (MaxAge %d)", cookie.Name, cookie.MaxAge)
		}
		if cookie.Value != "" {
			t.Errorf("cookie %s was cleared but still carries a value", cookie.Name)
		}
	}
}

// Logout without a session cookie still clears the cookies, so a stale client
// is not left holding something.
func TestHandleLogout_WithoutACookieStillClears(t *testing.T) {
	handler := newAuthHandlers(&fakeSessions{}, &fakeAccounts{}).HandleLogout()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if len(rec.Result().Cookies()) != 2 {
		t.Fatalf("cleared %d cookies, want 2", len(rec.Result().Cookies()))
	}
}

// The session endpoint reports who the caller is without a second call.
func TestHandleSession(t *testing.T) {
	accounts := &fakeAccounts{account: service.Account{HasTOTP: true}}
	handlers := newAuthHandlers(&fakeSessions{}, accounts)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	req = req.WithContext(contextWithActor(req.Context(), Actor{
		UserID: "u-admin", Username: "admin", Role: model.UserRoleAdmin, ViaSession: true,
	}))
	rec := httptest.NewRecorder()
	handlers.HandleSession().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Authenticated bool        `json:"authenticated"`
		User          sessionUser `json:"user"`
		ViaSession    bool        `json:"via_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Authenticated || body.User.Username != "admin" || !body.ViaSession {
		t.Fatalf("body = %+v", body)
	}
	if !body.User.HasTOTP {
		t.Error("has_totp was not reported, so the console cannot decide whether to offer enrolment")
	}
}

// Without an actor the session endpoint refuses rather than reporting nobody.
func TestHandleSession_WithoutAnActor(t *testing.T) {
	handlers := newAuthHandlers(&fakeSessions{}, &fakeAccounts{})
	rec := httptest.NewRecorder()
	handlers.HandleSession().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// A password change ends every session, including the one that made it.
func TestHandleChangePassword_RevokesEverySession(t *testing.T) {
	sessions := &fakeSessions{revokedCount: 3}
	accounts := &fakeAccounts{}
	handlers := newAuthHandlers(sessions, accounts)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password",
		strings.NewReader(`{"current_password":"the-old-one","new_password":"a-brand-new-long-one"}`))
	req.Host = "127.0.0.1"
	req = req.WithContext(contextWithActor(req.Context(), Actor{UserID: "u-admin", Role: model.UserRoleAdmin, ViaSession: true}))
	rec := httptest.NewRecorder()
	handlers.HandleChangePassword().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if len(accounts.changePwArgs) != 2 || accounts.changePwArgs[1] != "a-brand-new-long-one" {
		t.Fatalf("the account service was called with %v", accounts.changePwArgs)
	}
	if len(sessions.revokeCalls) != 1 || sessions.revokeCalls[0] != "u-admin" {
		t.Fatalf("revoke calls = %v, want the account's sessions to be ended", sessions.revokeCalls)
	}
	// The caller's own cookies are cleared, so the console cannot keep using a
	// session that no longer exists.
	if len(rec.Result().Cookies()) != 2 {
		t.Fatalf("cleared %d cookies, want 2", len(rec.Result().Cookies()))
	}
}

// A wrong current password is refused and nothing is revoked.
func TestHandleChangePassword_RefusesAWrongCurrentPassword(t *testing.T) {
	sessions := &fakeSessions{}
	accounts := &fakeAccounts{changePwErr: service.ErrCurrentPasswordWrong}
	handlers := newAuthHandlers(sessions, accounts)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password",
		strings.NewReader(`{"current_password":"wrong","new_password":"a-brand-new-long-one"}`))
	req.Host = "127.0.0.1"
	req = req.WithContext(contextWithActor(req.Context(), Actor{UserID: "u-admin", Role: model.UserRoleAdmin, ViaSession: true}))
	rec := httptest.NewRecorder()
	handlers.HandleChangePassword().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if len(sessions.revokeCalls) != 0 {
		t.Fatal("sessions were revoked although the password did not change")
	}
}

// The policy failure has its own code so the console can show the rule.
func TestHandleChangePassword_ReportsThePolicy(t *testing.T) {
	accounts := &fakeAccounts{changePwErr: service.ErrPasswordTooShort}
	handlers := newAuthHandlers(&fakeSessions{}, accounts)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password",
		strings.NewReader(`{"current_password":"x","new_password":"short"}`))
	req.Host = "127.0.0.1"
	req = req.WithContext(contextWithActor(req.Context(), Actor{UserID: "u-admin", ViaSession: true}))
	rec := httptest.NewRecorder()
	handlers.HandleChangePassword().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "PASSWORD_TOO_SHORT" {
		t.Fatalf("error code = %q", body.Error.Code)
	}
}

// Enrolment returns a secret and a URI, and confirming a bad code stores
// nothing.
func TestTOTPEnrolmentEndpoints(t *testing.T) {
	accounts := &fakeAccounts{
		enrolment: service.TOTPEnrolment{Secret: "JBSWY3DPEHPK3PXP", URI: "otpauth://totp/Prism:admin?secret=JBSWY3DPEHPK3PXP"},
	}
	handlers := newAuthHandlers(&fakeSessions{}, accounts)
	actor := Actor{UserID: "u-admin", Role: model.UserRoleAdmin, ViaSession: true}

	t.Run("start", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/totp/enrol", nil)
		req.Host = "127.0.0.1"
		req = req.WithContext(contextWithActor(req.Context(), actor))
		rec := httptest.NewRecorder()
		handlers.HandleStartTOTPEnrolment("Prism").ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		var body service.TOTPEnrolment
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Secret == "" || body.URI == "" {
			t.Fatalf("body = %+v", body)
		}
	})

	t.Run("start on an enrolled account", func(t *testing.T) {
		conflicting := &fakeAccounts{enrolErr: service.ErrTOTPAlreadyEnrolled}
		h := newAuthHandlers(&fakeSessions{}, conflicting)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/totp/enrol", nil)
		req.Host = "127.0.0.1"
		req = req.WithContext(contextWithActor(req.Context(), actor))
		rec := httptest.NewRecorder()
		h.HandleStartTOTPEnrolment("Prism").ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", rec.Code)
		}
	})

	t.Run("confirm a bad code", func(t *testing.T) {
		bad := &fakeAccounts{confirmErr: service.ErrTOTPCodeInvalid}
		h := newAuthHandlers(&fakeSessions{}, bad)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/totp/confirm",
			strings.NewReader(`{"secret":"JBSWY3DPEHPK3PXP","code":"000000"}`))
		req.Host = "127.0.0.1"
		req = req.WithContext(contextWithActor(req.Context(), actor))
		rec := httptest.NewRecorder()
		h.HandleConfirmTOTPEnrolment().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("confirm without a pepper", func(t *testing.T) {
		noPepper := &fakeAccounts{confirmErr: service.ErrNoPepper}
		h := newAuthHandlers(&fakeSessions{}, noPepper)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/totp/confirm",
			strings.NewReader(`{"secret":"JBSWY3DPEHPK3PXP","code":"123456"}`))
		req.Host = "127.0.0.1"
		req = req.WithContext(contextWithActor(req.Context(), actor))
		rec := httptest.NewRecorder()
		h.HandleConfirmTOTPEnrolment().ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
	})

	t.Run("disable", func(t *testing.T) {
		sessions := &fakeSessions{revokedCount: 1}
		h := newAuthHandlers(sessions, &fakeAccounts{})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/totp/disable", nil)
		req.Host = "127.0.0.1"
		req = req.WithContext(contextWithActor(req.Context(), actor))
		rec := httptest.NewRecorder()
		h.HandleDisableTOTP().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		// Removing the factor downgrades the account, so sessions are ended.
		if len(sessions.revokeCalls) != 1 {
			t.Fatalf("revoke calls = %v, want the account's sessions to be ended", sessions.revokeCalls)
		}
	})
}

// The account endpoints refuse without their service, and login refuses without
// the session service, rather than answering as though the caller were somebody.
//
// The session endpoint is deliberately not in this list. It reports the actor
// that the middleware already authenticated, so it can answer correctly with no
// account service at all - the account is only consulted for the "has a second
// factor" flag, which the console uses to decide whether to offer enrolment.
func TestAccountSecurityEndpoints_RefuseWithoutDependencies(t *testing.T) {
	empty := &AuthHandlers{}
	actor := Actor{UserID: "u-admin", Role: model.UserRoleAdmin, ViaSession: true}

	endpoints := map[string]http.HandlerFunc{
		"password":     empty.HandleChangePassword(),
		"totp enrol":   empty.HandleStartTOTPEnrolment("Prism"),
		"totp confirm": empty.HandleConfirmTOTPEnrolment(),
		"totp disable": empty.HandleDisableTOTP(),
		"login":        empty.HandleLogin(),
	}

	for name, endpoint := range endpoints {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/x", strings.NewReader(`{}`))
			req.Host = "127.0.0.1"
			req = req.WithContext(contextWithActor(req.Context(), actor))
			rec := httptest.NewRecorder()
			endpoint.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK {
				t.Fatalf("%s answered 200 with no dependencies configured", name)
			}
		})
	}
}

// The session endpoint answers from the context alone when no account service is
// installed, and still reports who the caller is.
func TestHandleSession_WithoutAnAccountService(t *testing.T) {
	handlers := &AuthHandlers{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	req = req.WithContext(contextWithActor(req.Context(), Actor{
		UserID: "u-admin", Username: "admin", Role: model.UserRoleAdmin, ViaSession: true,
	}))
	rec := httptest.NewRecorder()
	handlers.HandleSession().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Authenticated bool        `json:"authenticated"`
		User          sessionUser `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Authenticated || body.User.Username != "admin" {
		t.Fatalf("body = %+v", body)
	}
}

// contextWithActor is the test's way of installing an actor, matching what the
// middleware does.
func contextWithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorContextKey{}, actor)
}
