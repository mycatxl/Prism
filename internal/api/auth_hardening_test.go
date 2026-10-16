package api

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"prism/internal/auth"
	"prism/internal/config"
	"prism/internal/intel/jobs"
	"prism/internal/model"
)

// The hardening this file covers: an anonymous request is not a failed
// authentication, a credential that may not manage credentials says so, the
// HTTPS decision behind a TLS-terminating proxy, the origin of a sign-in, what
// a login leaves in the audit log, and the SSE route's place inside the guarded
// mux.

// --- 1. anonymous requests are not failed authentications -------------------

// TestAuthRateLimit_AnonymousRequestsAreNotCounted covers the remote lockout: any
// page on the internet can point a browser at a protected route, and if a request
// that presents nothing counted as a failed authentication, one visitor's address
// could spend the operator's failure budget and lock the console out from there.
// A request that presents a credential and is refused still counts.
func TestAuthRateLimit_AnonymousRequestsAreNotCounted(t *testing.T) {
	const addr = "192.0.2.77:4321"

	limiter := NewAuthFailureLimiter(10, time.Minute, 5*time.Minute, nil)
	// The session resolver refuses everything, so the cookie below is exactly
	// what a browser left holding an expired session sends.
	identity := &Identity{
		Sessions:       &fakeSessions{authErr: auth.ErrNoSession},
		AdminKeys:      fakeAdminKeys{credential: "correct-token", identity: AdminKeyIdentity{KeyID: "k", UserID: "u-admin", Role: model.UserRoleAdmin}},
		FailureLimiter: limiter,
	}
	handler := identity.Middleware(okHandler())

	for i := 1; i <= 20; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
		req.RemoteAddr = addr
		if i%2 == 0 {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "a-stale-session"})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("request %d: got %d, want 401 (body=%s)", i, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Retry-After") != "" {
			t.Fatalf("request %d: was blocked, so a request with nothing to authenticate as was counted", i)
		}
	}

	// Presented credentials that do not resolve are failures, and still block
	// once the budget is spent.
	for i := 1; i <= 10; i++ {
		if rec := doAuthedRequest(handler, addr, "", "wrong-token"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failed authentication %d: got %d, want 401", i, rec.Code)
		}
	}
	if rec := doAuthedRequest(handler, addr, "", "wrong-token"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after the failure budget: got %d, want 429 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestAuthRateLimit_ResetClearsAKey pins the clearing the account dimension of
// the blocklist needs: failures counted against an account must stop counting
// once the account's owner has proven the credential.
func TestAuthRateLimit_ResetClearsAKey(t *testing.T) {
	limiter := NewAuthFailureLimiter(3, time.Minute, time.Minute, nil)

	for i := 0; i < 3; i++ {
		limiter.RecordFailure("user:u-admin")
	}
	if blocked, _ := limiter.Blocked("user:u-admin"); !blocked {
		t.Fatal("the key was not blocked after the threshold")
	}
	// A second key is blocked at the same time, so the clearing below has to be
	// shown to be selective.
	for i := 0; i < 3; i++ {
		limiter.RecordFailure("203.0.113.9")
	}
	if blocked, _ := limiter.Blocked("203.0.113.9"); !blocked {
		t.Fatal("the second key was not blocked after the threshold")
	}

	limiter.Reset("user:u-admin")
	if blocked, _ := limiter.Blocked("user:u-admin"); blocked {
		t.Fatal("Reset left the key blocked")
	}
	if blocked, _ := limiter.Blocked("203.0.113.9"); !blocked {
		t.Fatal("Reset cleared a key it was not given")
	}

	// Clearing an unknown key, or through a limiter that was never built, is a
	// no-op rather than a panic.
	limiter.Reset("user:nobody")
	var none *AuthFailureLimiter
	none.Reset("user:u-admin")
}

// --- 2. credential management requires a console session --------------------

// credentialManagementMux mounts the four endpoints that change a credential.
func credentialManagementMux(accounts *fakeAccounts) *http.ServeMux {
	sessions := &fakeSessions{
		user:    adminUser(),
		session: &model.Session{IDHash: "h", UserID: "u-admin"},
	}
	handlers := newAuthHandlers(sessions, accounts)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/auth/password", handlers.HandleChangePassword())
	mux.Handle("POST /api/v1/auth/totp/enrol", handlers.HandleStartTOTPEnrolment("Prism"))
	mux.Handle("POST /api/v1/auth/totp/confirm", handlers.HandleConfirmTOTPEnrolment())
	mux.Handle("POST /api/v1/auth/totp/disable", handlers.HandleDisableTOTP())
	return mux
}

// TestCredentialEndpoints_RefuseAnAdminKey covers the invariant identity.go
// states: an access key drives the management API but never manages
// credentials. A key that could change a password or remove a second factor
// would outlive its own revocation.
func TestCredentialEndpoints_RefuseAnAdminKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{"change password", "/api/v1/auth/password", `{"current_password":"old-password-1234","new_password":"new-password-1234"}`},
		{"start enrolment", "/api/v1/auth/totp/enrol", ""},
		{"confirm enrolment", "/api/v1/auth/totp/confirm", `{"secret":"JBSWY3DPEHPK3PXP","code":"123456"}`},
		{"disable", "/api/v1/auth/totp/disable", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accounts := &fakeAccounts{}
			guarded := testIdentityMiddleware(testAdminCredential, nil, credentialManagementMux(accounts))

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testAdminCredential)
			rec := httptest.NewRecorder()
			guarded.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "CREDENTIAL_MANAGEMENT_REQUIRES_SESSION") {
				t.Fatalf("body = %s, want CREDENTIAL_MANAGEMENT_REQUIRES_SESSION", rec.Body.String())
			}
			// The refusal happens before the account service is asked to do
			// anything at all.
			if accounts.disableCalls != 0 || accounts.confirmCalls != 0 || accounts.changePwArgs != nil {
				t.Fatal("a refused request reached the account service")
			}
		})
	}
}

// TestCredentialEndpoints_StillServeASession pins the other half: the guard is
// about the credential the caller used, not about the endpoint. A console
// session with its CSRF token behaves exactly as it did.
func TestCredentialEndpoints_StillServeASession(t *testing.T) {
	accounts := &fakeAccounts{}
	guarded := testIdentityMiddleware(testAdminCredential, nil, credentialManagementMux(accounts))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password",
		strings.NewReader(`{"current_password":"old-password-1234","new_password":"new-password-1234"}`))
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "a-session"})
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "the-csrf-token"})
	req.Header.Set(auth.CSRFHeaderName, "the-csrf-token")
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := accounts.changePwArgs; len(got) != 2 || got[0] != "old-password-1234" || got[1] != "new-password-1234" {
		t.Fatalf("the account service saw %v", got)
	}
}

// --- 3. the HTTPS decision behind a TLS-terminating proxy -------------------

// TestAuthFailureLimiter_IsSecureRequestTrustsOnlyAConfiguredProxy pins the rule
// both the login and the sign-up use: Prism does not terminate TLS, so the
// forwarded header is the only evidence there is - and it is believed only when
// the request arrived from a configured proxy.
func TestAuthFailureLimiter_IsSecureRequestTrustsOnlyAConfiguredProxy(t *testing.T) {
	limiter := NewAuthFailureLimiter(10, time.Minute, time.Minute, []string{"10.0.0.0/8", "127.0.0.1"})

	request := func(remoteAddr string, headers ...string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
		req.RemoteAddr = remoteAddr
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Add(headers[i], headers[i+1])
		}
		return req
	}

	for _, tc := range []struct {
		name   string
		req    *http.Request
		secure bool
	}{
		{"no request", nil, false},
		{"an untrusted peer with the header", request("192.0.2.10:5000", "X-Forwarded-Proto", "https"), false},
		{"a trusted peer with the header", request("10.1.2.3:5000", "X-Forwarded-Proto", "https"), true},
		{"a trusted peer without the header", request("10.1.2.3:5000"), false},
		{"the nearest hop said http", request("10.1.2.3:5000", "X-Forwarded-Proto", "https, http"), false},
		{"a forged chain to the left of a trusted hop", request("10.1.2.3:5000", "X-Forwarded-Proto", "http, https"), true},
		{"a repeated header", request("10.1.2.3:5000", "X-Forwarded-Proto", "http", "X-Forwarded-Proto", "https"), true},
		{"a mixed-case value", request("10.1.2.3:5000", "X-Forwarded-Proto", "HTTPS"), true},
		{"a loopback peer with the header", request("127.0.0.1:5000", "X-Forwarded-Proto", "https"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := limiter.IsSecureRequest(tc.req); got != tc.secure {
				t.Fatalf("IsSecureRequest = %v, want %v", got, tc.secure)
			}
		})
	}

	// The connection itself always wins and needs no trusted list.
	direct := request("192.0.2.10:5000")
	direct.TLS = &tls.ConnectionState{}
	if !limiter.IsSecureRequest(direct) {
		t.Fatal("a TLS connection was not reported as secure")
	}

	// A server that trusts nothing forwarded answers from the connection alone,
	// which is the behaviour of a deployment that configures no proxies.
	claiming := request("10.1.2.3:5000", "X-Forwarded-Proto", "https")
	if NewLoginBlocklist(nil).IsSecureRequest(claiming) {
		t.Fatal("a deployment with no trusted proxies believed a forwarded header")
	}
	var none *AuthFailureLimiter
	if none.IsSecureRequest(claiming) {
		t.Fatal("a nil limiter believed a forwarded header")
	}
}

// TestHandleLogin_TrustsForwardedProtoOnlyFromATrustedProxy covers the login
// side of the dead HTTPS branch: behind a proxy that terminates TLS, the
// connection is plain HTTP and the cookie policy has to be told so by the
// proxy, while a peer that is not configured stays as untrusted as before.
func TestHandleLogin_TrustsForwardedProtoOnlyFromATrustedProxy(t *testing.T) {
	const body = `{"username":"admin","password":"a-long-enough-password"}`

	limiter := NewAuthFailureLimiter(10, time.Minute, time.Minute, []string{"10.0.0.0/8"})
	for _, tc := range []struct {
		name       string
		remoteAddr string
		host       string
		proto      string
		wantStatus int
		wantSecure bool
	}{
		{"a trusted proxy that terminated TLS", "10.1.2.3:5000", "prism.example.com", "https", http.StatusOK, true},
		{"an untrusted peer claiming TLS", "192.0.2.10:5000", "prism.example.com", "https", http.StatusBadRequest, false},
		{"a trusted proxy without the header", "10.1.2.3:5000", "prism.example.com", "", http.StatusBadRequest, false},
		{"a trusted proxy over plain HTTP", "10.1.2.3:5000", "prism.example.com", "http", http.StatusBadRequest, false},
		{"loopback without the header", "127.0.0.1:5000", "127.0.0.1:8080", "", http.StatusOK, false},
		{"a trusted proxy serving the loopback console", "10.1.2.3:5000", "127.0.0.1:8080", "https", http.StatusOK, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handlers := newAuthHandlers(&fakeSessions{loginResult: auth.LoginResult{
				SessionID:   "the-session-id",
				CSRFToken:   "the-csrf-token",
				User:        adminUser(),
				ExpiresAtNs: time.Now().Add(time.Hour).UnixNano(),
			}}, &fakeAccounts{})
			handlers.SecureTransport = limiter.IsSecureRequest

			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
			req.Host = tc.host
			req.RemoteAddr = tc.remoteAddr
			if tc.proto != "" {
				req.Header.Set("X-Forwarded-Proto", tc.proto)
			}
			rec := httptest.NewRecorder()
			handlers.HandleLogin().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			cookies := rec.Result().Cookies()
			if tc.wantStatus != http.StatusOK {
				if len(cookies) != 0 {
					t.Fatal("a refused login handed out cookies")
				}
				return
			}
			if len(cookies) == 0 {
				t.Fatal("no cookies were issued")
			}
			for _, cookie := range cookies {
				if cookie.Secure != tc.wantSecure {
					t.Fatalf("cookie %s: Secure = %v, want %v", cookie.Name, cookie.Secure, tc.wantSecure)
				}
			}
		})
	}
}

// fakeRegistrarStub stands in for the tenant service's self-service sign-up.
type fakeRegistrarStub struct {
	calls int
	user  *model.User
}

func (f *fakeRegistrarStub) RegisterTenantUser(_ config.RegistrationMode, _, username, _ string, _ int64) (*model.User, error) {
	f.calls++
	if f.user != nil {
		return f.user, nil
	}
	return &model.User{ID: "u-" + username, Username: username, Role: model.UserRoleUser, Status: model.UserStatusActive}, nil
}

// TestHandleRegister_TrustsForwardedProtoOnlyFromATrustedProxy covers the same
// decision on the sign-up route, which issues the same cookies.
func TestHandleRegister_TrustsForwardedProtoOnlyFromATrustedProxy(t *testing.T) {
	limiter := NewAuthFailureLimiter(10, time.Minute, time.Minute, []string{"10.0.0.0/8"})
	for _, tc := range []struct {
		name       string
		remoteAddr string
		host       string
		proto      string
		wantStatus int
		wantSecure bool
	}{
		{"a trusted proxy that terminated TLS", "10.1.2.3:5000", "prism.example.com", "https", http.StatusCreated, true},
		{"an untrusted peer claiming TLS", "192.0.2.10:5000", "prism.example.com", "https", http.StatusBadRequest, false},
		{"loopback without the header", "127.0.0.1:5000", "127.0.0.1:8080", "", http.StatusCreated, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registrar := &fakeRegistrarStub{}
			handlers := &AuthHandlers{
				Sessions: &fakeSessions{loginResult: auth.LoginResult{
					SessionID:   "the-session-id",
					CSRFToken:   "the-csrf-token",
					User:        &model.User{ID: "u-new-user", Username: "new-user", Role: model.UserRoleUser},
					ExpiresAtNs: time.Now().Add(time.Hour).UnixNano(),
				}},
				Registration:     registrar,
				RegistrationMode: func() config.RegistrationMode { return config.RegistrationOpen },
				ClientKey:        func(*http.Request) string { return "192.0.2.1" },
				SecureTransport:  limiter.IsSecureRequest,
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register",
				strings.NewReader(`{"username":"new-user","password":"a-long-enough-password"}`))
			req.Host = tc.host
			req.RemoteAddr = tc.remoteAddr
			if tc.proto != "" {
				req.Header.Set("X-Forwarded-Proto", tc.proto)
			}
			rec := httptest.NewRecorder()
			handlers.HandleRegister().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			cookies := rec.Result().Cookies()
			if tc.wantStatus != http.StatusCreated {
				if registrar.calls != 0 {
					t.Fatal("a refused sign-up created an account")
				}
				if len(cookies) != 0 {
					t.Fatal("a refused sign-up handed out cookies")
				}
				return
			}
			if registrar.calls != 1 {
				t.Fatalf("the registrar was called %d times, want 1", registrar.calls)
			}
			for _, cookie := range cookies {
				if cookie.Secure != tc.wantSecure {
					t.Fatalf("cookie %s: Secure = %v, want %v", cookie.Name, cookie.Secure, tc.wantSecure)
				}
			}
		})
	}
}

// --- 5. the origin of a sign-in or a sign-up --------------------------------

// TestHandleLogin_RefusesCrossSiteRequests covers login CSRF: the login form is
// the one write a browser can make without a CSRF token, so a page on another
// site could sign a visitor in as the attacker's own account. A caller that
// sends no origin information at all - curl, the smoke test, a CI script - is
// let through, because it carries no ambient credential to abuse.
func TestHandleLogin_RefusesCrossSiteRequests(t *testing.T) {
	for _, tc := range []struct {
		name      string
		origin    string
		fetchSite string
		want      int
	}{
		{"no origin information", "", "", http.StatusOK},
		{"a same-site origin", "http://127.0.0.1:8080", "", http.StatusOK},
		// The scheme is not compared: a console behind a proxy that terminates
		// TLS is served over plain HTTP while its browser sends an https Origin.
		{"an https origin for the same site", "https://127.0.0.1:8080", "", http.StatusOK},
		{"another site", "https://evil.example", "", http.StatusForbidden},
		{"an opaque origin", "null", "", http.StatusForbidden},
		{"cross-site fetch metadata", "", "cross-site", http.StatusForbidden},
		{"same-origin fetch metadata", "http://127.0.0.1:8080", "same-origin", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := &fakeSessions{loginResult: auth.LoginResult{
				SessionID:   "the-session-id",
				CSRFToken:   "the-csrf-token",
				User:        adminUser(),
				ExpiresAtNs: time.Now().Add(time.Hour).UnixNano(),
			}}
			handler := newAuthHandlers(sessions, &fakeAccounts{}).HandleLogin()

			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
				strings.NewReader(`{"username":"admin","password":"a-long-enough-password"}`))
			req.Host = "127.0.0.1:8080"
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusForbidden {
				if !strings.Contains(rec.Body.String(), "ORIGIN_REJECTED") {
					t.Fatalf("body = %s, want ORIGIN_REJECTED", rec.Body.String())
				}
				if len(sessions.loginCalls) != 0 {
					t.Fatal("a cross-site request reached the credential check")
				}
				if len(rec.Result().Cookies()) != 0 {
					t.Fatal("a cross-site request was handed a session")
				}
			}
		})
	}
}

// TestHandleRegister_RefusesCrossSiteRequests applies the same rule to sign-up,
// which creates an account before anything else is checked.
func TestHandleRegister_RefusesCrossSiteRequests(t *testing.T) {
	for _, tc := range []struct {
		name      string
		origin    string
		fetchSite string
		want      int
	}{
		{"no origin information", "", "", http.StatusCreated},
		{"a same-site origin", "http://127.0.0.1:8080", "", http.StatusCreated},
		{"another site", "https://evil.example", "", http.StatusForbidden},
		{"cross-site fetch metadata", "", "cross-site", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registrar := &fakeRegistrarStub{}
			handlers := &AuthHandlers{
				Sessions: &fakeSessions{loginResult: auth.LoginResult{
					SessionID:   "the-session-id",
					CSRFToken:   "the-csrf-token",
					User:        &model.User{ID: "u-new-user", Username: "new-user", Role: model.UserRoleUser},
					ExpiresAtNs: time.Now().Add(time.Hour).UnixNano(),
				}},
				Registration:     registrar,
				RegistrationMode: func() config.RegistrationMode { return config.RegistrationOpen },
				ClientKey:        func(*http.Request) string { return "192.0.2.1" },
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register",
				strings.NewReader(`{"username":"new-user","password":"a-long-enough-password"}`))
			req.Host = "127.0.0.1:8080"
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			}
			rec := httptest.NewRecorder()
			handlers.HandleRegister().ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusForbidden && registrar.calls != 0 {
				t.Fatal("a cross-site sign-up created an account")
			}
		})
	}
}

// --- 6. what a login leaves in the audit log --------------------------------

// TestLoginAudit_NamesTheActorAndRecordsFailures covers both halves of the login
// audit trail: the successful sign-in is attributed to the account it resolved,
// and a refused one leaves a row of its own - without the username, the password
// or the second factor.
func TestLoginAudit_NamesTheActorAndRecordsFailures(t *testing.T) {
	const (
		password = "a-long-enough-password"
		totpCode = "123456"
	)

	store := &fakeAuditStore{}
	sessions := &fakeSessions{loginResult: auth.LoginResult{
		SessionID:   "the-session-id",
		CSRFToken:   "the-csrf-token",
		User:        adminUser(),
		ExpiresAtNs: time.Now().Add(time.Hour).UnixNano(),
	}}
	handlers := newAuthHandlers(sessions, &fakeAccounts{})
	handlers.AuditLog = store

	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/auth/login", handlers.HandleLogin())
	handler := AuditMiddleware(store, 1<<20, mux)

	login := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
			strings.NewReader(`{"username":"admin","password":"`+password+`","totp":"`+totpCode+`"}`))
		req.Host = "127.0.0.1:8080"
		req.RemoteAddr = "203.0.113.7:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 1. A successful login names the account it authenticated.
	if rec := login(); rec.Code != http.StatusOK {
		t.Fatalf("login: status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	entries := store.snapshot()
	if len(entries) != 1 {
		t.Fatalf("audit rows after a successful login: got %d, want 1", len(entries))
	}
	if entries[0].Action != "POST /api/v1/auth/login" {
		t.Fatalf("action = %q", entries[0].Action)
	}
	if entries[0].Actor != "u-admin" || entries[0].UserID != "u-admin" {
		t.Fatalf("actor = %q user_id = %q, want the account", entries[0].Actor, entries[0].UserID)
	}

	// 2. A refused login writes its own row.
	sessions.loginErr = auth.ErrInvalidCredentials
	if rec := login(); rec.Code != http.StatusUnauthorized {
		t.Fatalf("refused login: status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	entries = store.snapshot()
	if len(entries) != 2 {
		t.Fatalf("audit rows after a refused login: got %d, want 2", len(entries))
	}
	failed := entries[1]
	if failed.Action != failedLoginAuditAction {
		t.Fatalf("action = %q, want %q", failed.Action, failedLoginAuditAction)
	}
	if failed.Actor != "" || failed.UserID != "" {
		t.Fatalf("a refused login named actor %q / user %q", failed.Actor, failed.UserID)
	}
	if failed.RemoteAddr != "203.0.113.7:1234" {
		t.Fatalf("remote_addr = %q", failed.RemoteAddr)
	}
	if rendered := fmt.Sprintf("%+v", failed); strings.Contains(rendered, password) ||
		strings.Contains(rendered, totpCode) || strings.Contains(rendered, "admin") {
		t.Fatalf("the refusal row carries credential material: %s", rendered)
	}

	// 3. A missing second factor is the ordinary first step of a two-step
	// login, not a failure, so it adds no row.
	sessions.loginErr = auth.ErrTOTPRequired
	if rec := login(); rec.Code != http.StatusUnauthorized {
		t.Fatalf("first step: status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := len(store.snapshot()); got != 2 {
		t.Fatalf("the first step of a two-step login wrote %d extra audit rows", got-2)
	}
}

// --- 4. the SSE route inside the guarded mux --------------------------------

// TestIntelJobEvents_RealServerServesASession is the end-to-end pin for the
// stream route. It goes through the real mux NewServer builds, because the bug
// it covers was exactly a registration that bypassed it: registered on the outer
// mux, the route won priority as the more specific pattern and the identity
// middleware never ran, so the handler found no actor and refused a session that
// was perfectly valid. The handler-level tests cannot see that - they wrap the
// route in the middleware by hand, which is the arrangement under test.
func TestIntelJobEvents_RealServerServesASession(t *testing.T) {
	env := newTenantE2EEnv(t, config.RegistrationOff)
	svc := wireIntelForTest(t, env.cp)

	job, err := svc.CreateJob(context.Background(), jobs.Request{
		Kind:  jobs.KindEgress,
		Scope: jobs.Scope{All: true},
	}, jobs.CreatedByAdmin(), jobs.PriorityManual)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)

	srv := httptest.NewServer(env.server.Handler())
	t.Cleanup(srv.Close)
	url := srv.URL + "/api/v1/intel/jobs/" + job.ID + "/events"

	// Without a credential the route is refused, and the refusal is the
	// middleware's rather than the handler's.
	anon, err := srv.Client().Get(url)
	if err != nil {
		t.Fatalf("anonymous GET: %v", err)
	}
	_ = anon.Body.Close()
	if anon.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous: status = %d, want 401", anon.StatusCode)
	}

	// With the console's session the stream opens and replays its state.
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: admin.session})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("a valid console session was refused by the stream route (body %s)", body)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if frames := readSSEUntil(t, resp.Body, "event: progress", 5*time.Second); len(frames) == 0 {
		t.Fatal("the stream produced no frame")
	}
}
