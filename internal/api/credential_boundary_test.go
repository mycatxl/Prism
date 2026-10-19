package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"prism/internal/auth"
	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/service"
)

// The five boundaries this file pins, all of them about the same question: what
// a credential is allowed to do to other credentials.
//
//  1. An automation key may read the administration surface and may revoke, but
//     it may not mint - not an account, not a password, not a key, not an
//     invitation and not a redeem code. Every one of those is a credential (or
//     the money behind one) whose lifetime is independent of the key's, which is
//     what "outlives its revocation" means concretely.
//  2. A blocked address is blocked on every route that spends the failure budget,
//     sign-up included. It was not: the count went up and nothing ever read it.
//  3. /ui/session.json answers from the session cookie alone. It sits outside the
//     middleware that counts, blocks and audits /api/, so accepting a bearer
//     credential there made it a free key-validity oracle.
//  4. Removing a second factor needs the current password, because a session
//     cookie on its own must not be able to downgrade an account to one factor.
//  5. Ending a session is credential management, so the self-service endpoint
//     that does it requires a session like the key-delete next to it.

// --- 1. minting requires a session, revoking does not -----------------------

// TestAdminMintEndpoints_RequireAConsoleSession drives the real server: the same
// administrator account, the same role, the only difference being the kind of
// credential the request arrived with. Every minting write must answer 403
// CREDENTIAL_MANAGEMENT_REQUIRES_SESSION to the key and work for the session.
func TestAdminMintEndpoints_RequireAConsoleSession(t *testing.T) {
	env := newTenantE2EEnv(t, config.RegistrationOff)
	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)

	// What the console mints, so the key has something to attack. If any of
	// this stopped working the test would fail here rather than prove anything
	// about the key, which is the point of doing it through HTTP.
	planID := tenantCreatePlan(t, admin, 1)
	rec := admin.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username":      tenantE2ETenantUser,
		"password":      tenantE2ETenantPass,
		"role":          "user",
		"plan_id":       planID,
		"duration_days": 30,
	})
	tenantAssertStatus(t, rec, http.StatusCreated)
	userID := tenantField(t, decodeJSONMap(t, rec), "id")

	rec = admin.do(http.MethodPost, "/api/v1/admin/users/"+userID+"/keys", map[string]any{"name": "laptop"})
	tenantAssertStatus(t, rec, http.StatusCreated)
	keyID := tenantField(t, tenantNested(t, decodeJSONMap(t, rec), "key"), "id")

	rec = admin.do(http.MethodPost, "/api/v1/admin/invites", map[string]any{
		"plan_id":         planID,
		"max_uses":        1,
		"expires_in_days": 7,
	})
	tenantAssertStatus(t, rec, http.StatusCreated)

	rec = admin.do(http.MethodPost, "/api/v1/admin/redeem-codes", map[string]any{
		"amount_micro": int64(12_500_000),
		"max_uses":     1,
		"count":        1,
	})
	tenantAssertStatus(t, rec, http.StatusCreated)
	minted, _ := decodeJSONMap(t, rec)["codes"].([]any)
	if len(minted) != 1 {
		t.Fatalf("minted codes: got %d, want 1", len(minted))
	}
	if _, ok := minted[0].(map[string]any); !ok {
		t.Fatalf("a minted code is not an object: %v", minted[0])
	}

	usersBefore := len(tenantItems(t, admin.do(http.MethodGet, "/api/v1/admin/users", nil)))

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   map[string]any
	}{
		{
			// The escalation the whole rule exists for: a key that can do
			// this can open an administrator account, sign into it, and keep
			// a session no key revocation reaches.
			name:   "create an administrator account",
			method: http.MethodPost,
			path:   "/api/v1/admin/users",
			body:   map[string]any{"username": "minted-by-a-key", "password": "minted-by-a-key-password", "role": "admin"},
		},
		{
			name:   "set another account's password and role",
			method: http.MethodPatch,
			path:   "/api/v1/admin/users/" + userID,
			body:   map[string]any{"password": "replaced-by-a-key-password", "role": "admin"},
		},
		{
			name:   "issue an access key",
			method: http.MethodPost,
			path:   "/api/v1/admin/users/" + userID + "/keys",
			body:   map[string]any{"name": "issued-by-a-key"},
		},
		{
			name:   "widen an existing access key",
			method: http.MethodPatch,
			path:   "/api/v1/admin/users/" + userID + "/keys/" + keyID,
			body:   map[string]any{"name": "renamed-by-a-key"},
		},
		{
			name:   "mint an invitation",
			method: http.MethodPost,
			path:   "/api/v1/admin/invites",
			body:   map[string]any{"max_uses": 1, "expires_in_days": 7},
		},
		{
			name:   "mint redeem codes",
			method: http.MethodPost,
			path:   "/api/v1/admin/redeem-codes",
			body:   map[string]any{"amount_micro": int64(1_000_000), "max_uses": 1, "count": 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.doAsKey(tc.method, tc.path, tc.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
			}
			assertErrorCode(t, rec, "CREDENTIAL_MANAGEMENT_REQUIRES_SESSION")
		})
	}

	// Nothing the key asked for happened. Each assertion names the thing that
	// would have been forged, because "the response said no" is only half of
	// the promise - the other half is that no write landed.
	if got := len(tenantItems(t, admin.do(http.MethodGet, "/api/v1/admin/users", nil))); got != usersBefore {
		t.Fatalf("accounts after the key's refusals: got %d, want %d", got, usersBefore)
	}
	if rec := env.session().login("minted-by-a-key", "minted-by-a-key-password"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("an account the key was refused still signs in: status = %d, want 401", rec.Code)
	}
	if rec := env.session().login(tenantE2ETenantUser, "replaced-by-a-key-password"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the password the key was refused is in force: status = %d, want 401", rec.Code)
	}
	if got := len(tenantItems(t, admin.do(http.MethodGet, "/api/v1/admin/invites", nil))); got != 1 {
		t.Fatalf("invitations: got %d, want the 1 the console minted", got)
	}
	if got := len(tenantItems(t, admin.do(http.MethodGet, "/api/v1/admin/redeem-codes", nil))); got != 1 {
		t.Fatalf("redeem codes: got %d, want the 1 the console minted", got)
	}

	// And the session does reach the same routes: the rule is about the kind of
	// credential, not about the endpoint being closed.
	rec = admin.do(http.MethodPatch, "/api/v1/admin/users/"+userID, map[string]any{
		"password": "changed-by-the-console",
	})
	tenantAssertStatus(t, rec, http.StatusOK)
	if rec := env.session().login(tenantE2ETenantUser, "changed-by-the-console"); rec.Code != http.StatusOK {
		t.Fatalf("a session's password change did not take effect: status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminRevocationEndpoints_AcceptAnAutomationKey pins the other half of the
// boundary: revocation is not an escalation. An operator who notices a leaked key
// must be able to kill what it made from a script, so the delete routes keep
// answering an automation credential.
func TestAdminRevocationEndpoints_AcceptAnAutomationKey(t *testing.T) {
	env := newTenantE2EEnv(t, config.RegistrationOff)
	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)

	planID := tenantCreatePlan(t, admin, 1)
	rec := admin.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username":      tenantE2ETenantUser,
		"password":      tenantE2ETenantPass,
		"role":          "user",
		"plan_id":       planID,
		"duration_days": 30,
	})
	tenantAssertStatus(t, rec, http.StatusCreated)
	userID := tenantField(t, decodeJSONMap(t, rec), "id")

	rec = admin.do(http.MethodPost, "/api/v1/admin/users/"+userID+"/keys", map[string]any{"name": "laptop"})
	tenantAssertStatus(t, rec, http.StatusCreated)
	keyID := tenantField(t, tenantNested(t, decodeJSONMap(t, rec), "key"), "id")

	rec = admin.do(http.MethodPost, "/api/v1/admin/invites", map[string]any{"max_uses": 1, "expires_in_days": 7})
	tenantAssertStatus(t, rec, http.StatusCreated)
	inviteID := tenantField(t, decodeJSONMap(t, rec), "id")

	rec = admin.do(http.MethodPost, "/api/v1/admin/redeem-codes", map[string]any{
		"amount_micro": int64(1_000_000),
		"max_uses":     1,
		"count":        1,
	})
	tenantAssertStatus(t, rec, http.StatusCreated)
	codes, _ := decodeJSONMap(t, rec)["codes"].([]any)
	firstCode, _ := codes[0].(map[string]any)
	codeID := tenantField(t, firstCode, "id")

	for _, path := range []string{
		"/api/v1/admin/users/" + userID + "/keys/" + keyID,
		"/api/v1/admin/invites/" + inviteID,
		"/api/v1/admin/redeem-codes/" + codeID,
		"/api/v1/admin/users/" + userID,
	} {
		rec := env.doAsKey(http.MethodDelete, path, nil)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE %s with an automation key: status = %d, want 204 (body=%s)",
				path, rec.Code, rec.Body.String())
		}
	}
}

// --- 2. a blocked address is blocked on sign-up too -------------------------

// stubRegistrar stands in for the tenant service's self-service sign-up, with an
// outcome the test chooses.
type stubRegistrar struct {
	calls int
	err   error
}

func (s *stubRegistrar) RegisterTenantUser(_ config.RegistrationMode, _, username, _ string, _ int64) (*model.User, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &model.User{ID: "u-" + username, Username: username, Role: model.UserRoleUser, Status: model.UserStatusActive}, nil
}

// registerAuthHandlers builds the sign-up handler with a limiter and a stub
// registrar, so the test can see both the answer and whether the work behind it
// was done at all.
func registerAuthHandlers(limiter *AuthFailureLimiter, registrar *stubRegistrar, clientKey string) *AuthHandlers {
	return &AuthHandlers{
		Sessions: &fakeSessions{loginResult: auth.LoginResult{
			SessionID:   "the-session-id",
			CSRFToken:   "the-csrf-token",
			User:        &model.User{ID: "u-new-user", Username: "new-user", Role: model.UserRoleUser},
			ExpiresAtNs: time.Now().Add(time.Hour).UnixNano(),
		}},
		Registration:     registrar,
		RegistrationMode: func() config.RegistrationMode { return config.RegistrationInvite },
		ClientKey:        func(*http.Request) string { return clientKey },
		FailureLimiter:   limiter,
		Logf:             func(string, ...any) {},
	}
}

func doRegister(h *AuthHandlers) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register",
		strings.NewReader(`{"invite_code":"guess","username":"new-user","password":"a-long-enough-password"}`))
	req.Host = "127.0.0.1:8080"
	req.RemoteAddr = "192.0.2.9:1234"
	rec := httptest.NewRecorder()
	h.HandleRegister().ServeHTTP(rec, req)
	return rec
}

// TestHandleRegister_RefusesABlockedAddress covers the hole this endpoint had:
// it counted its own failures into the shared limiter and then never asked it
// again, so an address that was already blocked - and blocked by the management
// middleware for every other route - could keep guessing invite codes forever.
// Each guess costs an indexed read and every hit costs an argon2id hash, which
// made this the one unauthenticated way to make Prism work without limit.
func TestHandleRegister_RefusesABlockedAddress(t *testing.T) {
	const addr = "192.0.2.9"
	limiter := NewAuthFailureLimiter(3, time.Minute, 5*time.Minute, nil)
	registrar := &stubRegistrar{err: errors.New("that invitation does not work")}
	h := registerAuthHandlers(limiter, registrar, addr)

	// Three refusals spend the budget. They are counted, which is what makes
	// the block exist at all.
	for i := 1; i <= 3; i++ {
		rec := doRegister(h)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d was already blocked, want the budget to run out on the fourth", i)
		}
		if registrar.calls != i {
			t.Fatalf("attempt %d reached the registrar %d times", i, registrar.calls)
		}
	}
	if blocked, _ := limiter.Blocked(addr); !blocked {
		t.Fatal("the limiter did not block the address after the budget was spent")
	}

	// The fourth is refused before any work happens, which is the whole point:
	// an expensive refusal would be the same CPU amplification one step later.
	rec := doRegister(h)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body=%s)", rec.Code, rec.Body.String())
	}
	assertErrorCode(t, rec, "RATE_LIMITED")
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("a blocked sign-up must say how long to wait")
	}
	if registrar.calls != 3 {
		t.Fatalf("a blocked sign-up reached the registrar (calls = %d, want 3)", registrar.calls)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("a blocked sign-up was handed a session")
	}

	// The block is per address: another one is still served.
	other := registerAuthHandlers(limiter, &stubRegistrar{}, "198.51.100.4")
	if rec := doRegister(other); rec.Code != http.StatusCreated {
		t.Fatalf("an address that is not blocked: status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestHandleRegister_ServesAnAddressWithBudgetLeft is the other half: the check
// must not turn a normal first sign-up into a refusal, and a limiter that was
// never built (a deployment without one) must block nothing.
func TestHandleRegister_ServesAnAddressWithBudgetLeft(t *testing.T) {
	limiter := NewAuthFailureLimiter(10, time.Minute, 5*time.Minute, nil)
	registrar := &stubRegistrar{}
	h := registerAuthHandlers(limiter, registrar, "192.0.2.9")

	rec := doRegister(h)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	if registrar.calls != 1 {
		t.Fatalf("registrar calls = %d, want 1", registrar.calls)
	}

	// Failures of a different address do not spend this one's budget.
	for i := 0; i < 5; i++ {
		limiter.RecordFailure("198.51.100.9")
	}
	if rec := doRegister(h); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}

	// A deployment with no limiter answers the same way.
	rec = doRegister(registerAuthHandlers(nil, &stubRegistrar{}, "192.0.2.9"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("without a limiter: status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
}

// --- 3. /ui/session.json answers from the cookie alone -----------------------

// TestUISessionJSON_IgnoresABearerCredential covers the oracle. /ui/ is mounted
// outside the identity middleware, so nothing on that path counts a failed
// credential, blocks a spent address or writes an audit row. A bearer credential
// accepted here could therefore be tested for validity without limit, from an
// address that is already blocked on /api/ - which is exactly what a stolen admin
// key needs to know before it is worth using.
func TestUISessionJSON_IgnoresABearerCredential(t *testing.T) {
	identity := testIdentityWithLimiter(testAdminCredential, nil)
	distFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html></html>")}}
	handler := newWebUIHandlerFromFS(distFS, true, identity.sessionInfo)

	sessionInfo := func(req *http.Request) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v body=%s", err, rec.Body.String())
		}
		return body
	}

	// 1. A valid admin key is not an identity here, and the answer says so
	// without confirming whether the key works: it is the same body an
	// anonymous visitor gets.
	withKey := httptest.NewRequest(http.MethodGet, "/ui/session.json", nil)
	withKey.Header.Set("Authorization", "Bearer "+testAdminCredential)
	body := sessionInfo(withKey)
	if body["authenticated"] != false {
		t.Fatalf("authenticated = %v with a bearer credential, want false", body["authenticated"])
	}
	if _, ok := body["user"]; ok {
		t.Fatalf("a bearer credential was resolved to a user: %v", body)
	}
	if body["auth_required"] != true {
		t.Fatalf("auth_required = %v, want true", body["auth_required"])
	}
	if _, ok := body["registration_mode"]; !ok {
		t.Fatalf("registration_mode missing from %v", body)
	}

	// An invalid one gets the same body: the file is not an oracle either way.
	withBadKey := httptest.NewRequest(http.MethodGet, "/ui/session.json", nil)
	withBadKey.Header.Set("Authorization", "Bearer not-a-real-key")
	if got := sessionInfo(withBadKey); got["authenticated"] != false {
		t.Fatalf("authenticated = %v with an invalid bearer credential, want false", got["authenticated"])
	}

	// 2. A session cookie is answered exactly as before: that is the credential
	// the login page has to read once it exists.
	withCookie := httptest.NewRequest(http.MethodGet, "/ui/session.json", nil)
	withCookie.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "a-session"})
	body = sessionInfo(withCookie)
	if body["authenticated"] != true {
		t.Fatalf("authenticated = %v with a session cookie, want true (body=%v)", body["authenticated"], body)
	}
	user, ok := body["user"].(map[string]any)
	if !ok {
		t.Fatalf("user missing from %v", body)
	}
	if user["id"] != "u-admin" || user["role"] != model.UserRoleAdmin {
		t.Fatalf("user = %v", user)
	}
	if body["auth_required"] != true {
		t.Fatalf("auth_required = %v, want true", body["auth_required"])
	}
}

// --- 4. removing a second factor needs the current password ------------------

// TestHandleDisableTOTP_RequiresTheCurrentPassword covers the downgrade path: a
// session cookie stolen from a shared machine, an XSS, or a browser left signed
// in must not be enough on its own to turn two factors back into one.
func TestHandleDisableTOTP_RequiresTheCurrentPassword(t *testing.T) {
	actor := Actor{UserID: "u-admin", Username: "admin", Role: model.UserRoleAdmin, ViaSession: true}

	call := func(t *testing.T, accounts *fakeAccounts, body string) (*httptest.ResponseRecorder, *fakeSessions) {
		t.Helper()
		sessions := &fakeSessions{revokedCount: 1}
		h := newAuthHandlers(sessions, accounts)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/totp/disable", strings.NewReader(body))
		req.Host = "127.0.0.1:8080"
		req = req.WithContext(contextWithActor(req.Context(), actor))
		rec := httptest.NewRecorder()
		h.HandleDisableTOTP().ServeHTTP(rec, req)
		return rec, sessions
	}

	t.Run("no password in the body", func(t *testing.T) {
		accounts := &fakeAccounts{}
		rec, sessions := call(t, accounts, `{}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
		}
		assertErrorCode(t, rec, "PASSWORD_MISMATCH")
		// The comparison is the account service's, which is the point: the
		// handler holds no copy of the rule and cannot be talked out of it.
		if accounts.verifyCalls != 1 {
			t.Fatalf("password verifications = %d, want 1", accounts.verifyCalls)
		}
		if accounts.verifyPwArgs[0] != "" {
			t.Fatalf("the handler verified %q, want the empty password it was given", accounts.verifyPwArgs[0])
		}
		if accounts.disableCalls != 0 {
			t.Fatal("the second factor was removed without a password")
		}
		if len(sessions.revokeCalls) != 0 {
			t.Fatal("sessions were revoked for a refused request")
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		accounts := &fakeAccounts{verifyPwErr: service.ErrCurrentPasswordWrong}
		rec, sessions := call(t, accounts, `{"current_password":"not-the-password"}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
		}
		assertErrorCode(t, rec, "PASSWORD_MISMATCH")
		if accounts.verifyPwArgs[0] != "not-the-password" {
			t.Fatalf("the handler verified %q", accounts.verifyPwArgs[0])
		}
		if accounts.disableCalls != 0 {
			t.Fatal("the second factor was removed for a wrong password")
		}
		if len(sessions.revokeCalls) != 0 {
			t.Fatal("sessions were revoked for a refused request")
		}
	})

	t.Run("right password", func(t *testing.T) {
		accounts := &fakeAccounts{}
		rec, sessions := call(t, accounts, `{"current_password":"the-real-password"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if accounts.verifyPwArgs[0] != "the-real-password" {
			t.Fatalf("the handler verified %q", accounts.verifyPwArgs[0])
		}
		if accounts.disableCalls != 1 {
			t.Fatalf("removals = %d, want 1", accounts.disableCalls)
		}
		// Removing the factor downgrades the account: every session goes,
		// including the one that made the request.
		if len(sessions.revokeCalls) != 1 || sessions.revokeCalls[0] != "u-admin" {
			t.Fatalf("revoke calls = %v, want the account's sessions to be ended", sessions.revokeCalls)
		}
		cleared := false
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == auth.SessionCookieName && cookie.MaxAge < 0 {
				cleared = true
			}
		}
		if !cleared {
			t.Fatal("the session cookie was not cleared on the response")
		}
	})

	t.Run("an automation key is refused before the comparison", func(t *testing.T) {
		accounts := &fakeAccounts{}
		sessions := &fakeSessions{}
		h := newAuthHandlers(sessions, accounts)
		keyActor := Actor{UserID: "u-admin", Role: model.UserRoleAdmin, ViaSession: false}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/totp/disable",
			strings.NewReader(`{"current_password":"the-real-password"}`))
		req.Host = "127.0.0.1:8080"
		req = req.WithContext(contextWithActor(req.Context(), keyActor))
		rec := httptest.NewRecorder()
		h.HandleDisableTOTP().ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
		}
		assertErrorCode(t, rec, "CREDENTIAL_MANAGEMENT_REQUIRES_SESSION")
		if accounts.verifyCalls != 0 {
			t.Fatal("a key spent a password comparison")
		}
	})
}

// --- 5. ending a session is credential management ---------------------------

// TestMeRevokeSession_RequiresAConsoleSession covers the last write on the
// self-service surface that skipped the rule: an automation key could end any of
// its owner's console sessions, including the one being used to revoke it.
func TestMeRevokeSession_RequiresAConsoleSession(t *testing.T) {
	env := newTenantE2EEnv(t, config.RegistrationOff)

	// Two browsers signed in as the same administrator.
	first := env.session()
	first.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	second := env.session()
	second.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)

	// The row the listing marks as not-current is the other browser's.
	rec := first.do(http.MethodGet, "/api/v1/me/security/sessions", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	otherID := ""
	for _, row := range tenantItems(t, rec) {
		if current, _ := row["current"].(bool); current {
			continue
		}
		otherID, _ = row["id"].(string)
	}
	if otherID == "" {
		t.Fatal("the session list did not carry the other session")
	}

	// A key is refused, and the session it aimed at survives.
	rec = env.doAsKey(http.MethodDelete, "/api/v1/me/security/sessions/"+otherID, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	assertErrorCode(t, rec, "CREDENTIAL_MANAGEMENT_REQUIRES_SESSION")
	if rec := second.do(http.MethodGet, "/api/v1/auth/session", nil); rec.Code != http.StatusOK {
		t.Fatalf("the session a key was refused survived with status %d, want 200", rec.Code)
	}

	// The session may: the same request from the console works, and the other
	// browser is signed out.
	rec = first.do(http.MethodDelete, "/api/v1/me/security/sessions/"+otherID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", rec.Code, rec.Body.String())
	}
	if rec := second.do(http.MethodGet, "/api/v1/auth/session", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the revoked session: status = %d, want 401", rec.Code)
	}
}
