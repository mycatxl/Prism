package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/service"
)

// Tests for the closeout batch at the HTTP layer: the administrator's user patch
// as one atomic operation, the server-side role filter on the user list, the
// has_totp answer /ui/session.json gives, and the registration path's failures
// counting towards the blocklist the middleware enforces.

// TestAdminUpdateUser_OneAtomicPatch drives the patch the console sends when the
// user detail page has both a status control and a role control dirty.
//
// The request is refused as a whole: a role that is not a role must not leave a
// status change behind, because the console re-reads the account after the
// answer, and a half-applied patch would be one it never asked for. The refusal
// says nothing was changed, which is why the account is read back here.
func TestAdminUpdateUser_OneAtomicPatch(t *testing.T) {
	env := newTenantE2EEnv(t, config.RegistrationOff)

	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	planID := tenantCreatePlan(t, admin, 2)

	rec := admin.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username":      tenantE2ETenantUser,
		"password":      tenantE2ETenantPass,
		"role":          "user",
		"plan_id":       planID,
		"duration_days": 30,
	})
	tenantAssertStatus(t, rec, http.StatusCreated)
	userID := tenantField(t, decodeJSONMap(t, rec), "id")

	// A patch naming one good field and one bad one is refused, and the good
	// field is not applied.
	rec = admin.do(http.MethodPatch, "/api/v1/admin/users/"+userID, map[string]any{
		"status": "disabled",
		"role":   "not-a-role",
	})
	tenantAssertStatus(t, rec, http.StatusBadRequest)
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	rec = admin.do(http.MethodGet, "/api/v1/admin/users/"+userID, nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	after := decodeJSONMap(t, rec)
	if got := tenantField(t, after, "status"); got != "active" {
		t.Fatalf("status after the refused patch: got %q, want active (nothing may be applied)", got)
	}
	if got := tenantField(t, after, "role"); got != "user" {
		t.Fatalf("role after the refused patch: got %q, want user", got)
	}

	// The same field on its own is accepted, so the refusal above was about the
	// patch and not about the endpoint.
	rec = admin.do(http.MethodPatch, "/api/v1/admin/users/"+userID, map[string]any{"status": "disabled"})
	tenantAssertStatus(t, rec, http.StatusOK)
	if got := tenantField(t, decodeJSONMap(t, rec), "status"); got != "disabled" {
		t.Fatalf("status after the accepted patch: got %q, want disabled", got)
	}
}

// TestAdminListUsers_FiltersByRoleOnTheServer pins the filter the console asks
// for with ?role=. It has to be applied by the server because the list is paged:
// cutting the page first and filtering it afterwards is how a request for the
// administrators returns a page with none of them in it.
func TestAdminListUsers_FiltersByRoleOnTheServer(t *testing.T) {
	env := newTenantE2EEnv(t, config.RegistrationOff)

	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	planID := tenantCreatePlan(t, admin, 2)

	rec := admin.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username":      tenantE2ETenantUser,
		"password":      tenantE2ETenantPass,
		"role":          "user",
		"plan_id":       planID,
		"duration_days": 30,
	})
	tenantAssertStatus(t, rec, http.StatusCreated)
	userID := tenantField(t, decodeJSONMap(t, rec), "id")

	// Unfiltered: the administrator and the tenant.
	rec = admin.do(http.MethodGet, "/api/v1/admin/users", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	if total, _ := decodeJSONMap(t, rec)["total"].(float64); total != 2 {
		t.Fatalf("unfiltered total: got %v, want 2", total)
	}

	rec = admin.do(http.MethodGet, "/api/v1/admin/users?role=user", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	body := decodeJSONMap(t, rec)
	if total, _ := body["total"].(float64); total != 1 {
		t.Fatalf("total for role=user: got %v, want 1 (the administrator is not a user)", total)
	}
	items := tenantItems(t, rec)
	if len(items) != 1 {
		t.Fatalf("items for role=user: got %d, want 1", len(items))
	}
	if got := items[0]["id"]; got != userID {
		t.Fatalf("the filtered item is %v, want %s", got, userID)
	}

	// The filter is applied before the page is cut, so a page of one still
	// holds the account the filter selected even when the administrator sorts
	// first.
	rec = admin.do(http.MethodGet, "/api/v1/admin/users?role=user&limit=1", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	items = tenantItems(t, rec)
	if len(items) != 1 || items[0]["id"] != userID {
		t.Fatalf("the filtered, paged answer is %v, want the tenant on its own", items)
	}

	// An unknown role matches nothing rather than being ignored: the console
	// offers a selection, and a filter that quietly widens is worse than an
	// empty table.
	rec = admin.do(http.MethodGet, "/api/v1/admin/users?role=not-a-role", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	if total, _ := decodeJSONMap(t, rec)["total"].(float64); total != 0 {
		t.Fatalf("total for an unknown role: got %v, want 0", total)
	}
}

// TestWebUISessionJSON_ReportsHasTOTP covers the endpoint the console bootstraps
// from. The security page decides from user.has_totp whether the second factor is
// enrolled, and it reads it from here on every reload - so a missing field would
// show an enrolled factor as not enrolled and offer to enrol the one that is
// already there.
func TestWebUISessionJSON_ReportsHasTOTP(t *testing.T) {
	env := newTenantE2EEnv(t, config.RegistrationOff)

	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)

	sessionJSON := func() map[string]any {
		t.Helper()
		rec := env.session()
		rec.session = admin.session
		answer := rec.do(http.MethodGet, "/ui/session.json", nil)
		tenantAssertStatus(t, answer, http.StatusOK)
		return decodeJSONMap(t, answer)
	}

	before := sessionJSON()
	if before["authenticated"] != true {
		t.Fatalf("the session cookie was not resolved: %v", before)
	}
	user := tenantNested(t, before, "user")
	if got, ok := user["has_totp"].(bool); !ok || got {
		t.Fatalf("has_totp before enrolment: got %v, want false", user["has_totp"])
	}

	// Enrol a second factor for the administrator the way the enrolment
	// endpoint does, and ask again with the same session.
	stored, err := env.cp.Engine.GetUserByName(tenantE2EAdminUser)
	if err != nil {
		t.Fatalf("GetUserByName: %v", err)
	}
	if err := env.cp.Engine.UpdateUserTOTPSecret(stored.ID, []byte("sealed-second-factor"), time.Now().UnixNano()); err != nil {
		t.Fatalf("UpdateUserTOTPSecret: %v", err)
	}

	after := sessionJSON()
	user = tenantNested(t, after, "user")
	if got, ok := user["has_totp"].(bool); !ok || !got {
		t.Fatalf("has_totp after enrolment: got %v, want true", user["has_totp"])
	}
}

// TestRegisterE2E_RefusalsCountTowardsTheBlocklist is the DoS half of the public
// registration path: every refusal spends a failure on the address it came from,
// and the address is then blocked by the middleware - before the handler, and so
// before any password hashing.
func TestRegisterE2E_RefusalsCountTowardsTheBlocklist(t *testing.T) {
	env := newTenantE2EEnv(t, config.RegistrationInvite)

	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	planID := tenantCreatePlan(t, admin, 1)

	rec := admin.do(http.MethodPost, "/api/v1/admin/invites", map[string]any{
		"plan_id":         planID,
		"max_uses":        1,
		"expires_in_days": 7,
	})
	tenantAssertStatus(t, rec, http.StatusCreated)
	code := tenantField(t, decodeJSONMap(t, rec), "code")

	// A good code still works: the screening must not have made the door unusable.
	invited := env.session()
	rec = invited.do(http.MethodPost, "/api/v1/auth/register", map[string]any{
		"invite_code": code,
		"username":    "invited-by-closeout",
		"password":    "invited-by-closeout-password",
	})
	tenantAssertStatus(t, rec, http.StatusCreated)

	// A code that cannot be one of ours is refused without ever reaching the
	// password work, and the refusal is counted.
	anon := env.session()
	rec = anon.do(http.MethodPost, "/api/v1/auth/register", map[string]any{
		"invite_code": "nope",
		"username":    "closeout-victim",
		"password":    "closeout-victim-password",
	})
	tenantAssertStatus(t, rec, http.StatusBadRequest)
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	// Nine more refusals fill the address's failure budget
	// (authFailuresPerWindow = 10).
	for i := 0; i < authFailuresPerWindow-1; i++ {
		rec = anon.do(http.MethodPost, "/api/v1/auth/register", map[string]any{
			"invite_code": "2345-6789",
			"username":    "closeout-victim",
			"password":    "closeout-victim-password",
		})
		tenantAssertStatus(t, rec, http.StatusBadRequest)
		assertErrorCode(t, rec, "INVALID_ARGUMENT")
	}

	// The next request from that address is refused by the middleware, before
	// the handler and before any account work.
	rec = anon.do(http.MethodPost, "/api/v1/auth/register", map[string]any{
		"invite_code": "2345-6789",
		"username":    "closeout-victim",
		"password":    "closeout-victim-password",
	})
	tenantAssertStatus(t, rec, http.StatusTooManyRequests)
	assertErrorCode(t, rec, "RATE_LIMITED")
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("a blocked address must be told how long to wait")
	}

	// No account was created by any of the refusals. The check reads the
	// account store directly, because the block that just fired covers every
	// route from that address - the console's own session included.
	if _, err := env.cp.Engine.GetUserByName("closeout-victim"); err == nil {
		t.Fatal("a refused registration created an account")
	}
}

// closeoutRefusingRegistrar refuses every sign-up the way the service refuses a
// code that does not work.
type closeoutRefusingRegistrar struct {
	calls int
}

func (f *closeoutRefusingRegistrar) RegisterTenantUser(config.RegistrationMode, string, string, string, int64) (*model.User, error) {
	f.calls++
	return nil, &service.ServiceError{Code: "INVALID_ARGUMENT", Message: "invite_code: the code is not valid"}
}

// closeoutLimiterKeys snapshots the limiter's keys, for a failure message.
func closeoutLimiterKeys(l *AuthFailureLimiter) []string {
	keys := make([]string, 0, len(l.entries))
	for key := range l.entries {
		keys = append(keys, key)
	}
	return keys
}

// TestRegisterFailures_CountOnlyTheSourceAddress pins the one dimension a
// refused sign-up is counted against.
//
// The username in the body used to buy a bucket of its own
// ("register:<name>"), and nothing ever read that bucket back: the login path
// resolves an account id, so a name a stranger typed could only ever be a key.
// In a bounded LRU limiter those keys are free to mint, and a full table evicts
// its oldest entry - blocks that are still in force included - so the second
// dimension was a lever for pushing other clients out of the blocklist. What is
// counted instead is the source address, the part of the request the caller
// cannot choose.
func TestRegisterFailures_CountOnlyTheSourceAddress(t *testing.T) {
	const source = "203.0.113.7"

	limiter := NewAuthFailureLimiter(authFailuresPerWindow, authFailureWindow, authFailureBlock, nil)
	registrar := &closeoutRefusingRegistrar{}
	handlers := &AuthHandlers{
		Sessions:         &fakeSessions{},
		Registration:     registrar,
		RegistrationMode: func() config.RegistrationMode { return config.RegistrationInvite },
		ClientKey:        func(*http.Request) string { return source },
		SecureTransport:  limiter.IsSecureRequest,
		FailureLimiter:   limiter,
	}
	handler := handlers.HandleRegister()

	for _, username := range []string{"closeout-first", "closeout-second"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(
			`{"invite_code":"2345-6789","username":"`+username+`","password":"closeout-victim-password"}`))
		req.Host = "127.0.0.1:8080"
		req.RemoteAddr = "127.0.0.1:5000"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		tenantAssertStatus(t, rec, http.StatusBadRequest)
	}
	if registrar.calls != 2 {
		t.Fatalf("the registrar was called %d times, want 2", registrar.calls)
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	entry, ok := limiter.entries[source]
	if !ok {
		t.Fatalf("the refusals were not counted against the source address %q: %v", source, closeoutLimiterKeys(limiter))
	}
	if entry.failures != 2 {
		t.Fatalf("the source address carries %d failures, want 2", entry.failures)
	}
	if len(limiter.entries) != 1 {
		t.Fatalf("%d limiter keys were written, want 1 - only the source address: %v",
			len(limiter.entries), closeoutLimiterKeys(limiter))
	}
}

// TestAdminCreateSubscription_RefusesAMissingPlan drives the hole the review
// found through the console's own endpoint. POST /api/v1/admin/subscriptions
// enforces user_id and nothing else, so a body that left plan_id out used to be
// accepted and stored an active row carrying the zero snapshot - which
// keys.Store reads as unrestricted (an empty status is not an inactive plan,
// a quota of zero is unlimited), so every key that row authorised was unlimited.
func TestAdminCreateSubscription_RefusesAMissingPlan(t *testing.T) {
	env := newTenantE2EEnv(t, config.RegistrationOff)

	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)

	rec := admin.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username": tenantE2ETenantUser,
		"password": tenantE2ETenantPass,
		"role":     "user",
	})
	tenantAssertStatus(t, rec, http.StatusCreated)
	userID := tenantField(t, decodeJSONMap(t, rec), "id")

	// What the console sends when the plan selector was never filled in.
	rec = admin.do(http.MethodPost, "/api/v1/admin/subscriptions", map[string]any{
		"user_id":       userID,
		"duration_days": 30,
	})
	tenantAssertStatus(t, rec, http.StatusBadRequest)
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	// Nothing was written: the account holds no subscription at all, so there is
	// no active row for the read side to mistake for an unrestricted plan.
	rec = admin.do(http.MethodGet, "/api/v1/admin/subscriptions", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	if total, _ := decodeJSONMap(t, rec)["total"].(float64); total != 0 {
		t.Fatalf("the refused assignment left %v subscriptions behind, want 0", total)
	}

	// The same request carrying a real plan is accepted, so the refusal above
	// was about the missing value and not about the endpoint.
	planID := tenantCreatePlan(t, admin, 1)
	rec = admin.do(http.MethodPost, "/api/v1/admin/subscriptions", map[string]any{
		"user_id":       userID,
		"plan_id":       planID,
		"duration_days": 30,
	})
	tenantAssertStatus(t, rec, http.StatusCreated)
	created := decodeJSONMap(t, rec)
	if got := tenantField(t, created, "plan_id"); got != planID {
		t.Fatalf("the accepted subscription is on plan %q, want %s", got, planID)
	}
	if got := tenantField(t, created, "status"); got != "active" {
		t.Fatalf("the accepted subscription is %q, want active", got)
	}
}

// TestAdminUpdateSubscription_AnUnchangedPatchIsNotAMissingRow settles what a
// zero row count means for the terms write, through the request that reaches it:
// PATCH {"status":"active"} on a subscription that is already active changes
// nothing, and a row count of zero read as "no such row" would answer the
// console's own save with a 404.
//
// The no-op is paired with an id that names nothing, which must still be a 404,
// so the guarantee being pinned is "a no-op patch applies", not "missing rows
// stop being reported".
func TestAdminUpdateSubscription_AnUnchangedPatchIsNotAMissingRow(t *testing.T) {
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

	rec = admin.do(http.MethodGet, "/api/v1/admin/subscriptions?user_id="+userID, nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	items := tenantItems(t, rec)
	if len(items) != 1 {
		t.Fatalf("the account holds %d subscriptions, want 1", len(items))
	}
	subID, _ := items[0]["id"].(string)
	if subID == "" {
		t.Fatalf("the subscription carries no id: %v", items[0])
	}

	// Already active: this patch writes back exactly what it read.
	rec = admin.do(http.MethodPatch, "/api/v1/admin/subscriptions/"+subID, map[string]any{"status": "active"})
	tenantAssertStatus(t, rec, http.StatusOK)
	if got := tenantField(t, decodeJSONMap(t, rec), "status"); got != "active" {
		t.Fatalf("status after the no-op patch: got %q, want active", got)
	}

	// An id that names no row is still a missing row.
	rec = admin.do(http.MethodPatch, "/api/v1/admin/subscriptions/us_no_such_closeout", map[string]any{"status": "active"})
	tenantAssertStatus(t, rec, http.StatusNotFound)
	assertErrorCode(t, rec, "NOT_FOUND")

	// And the no-op left the record in place.
	rec = admin.do(http.MethodGet, "/api/v1/admin/subscriptions/"+subID, nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	if got := tenantField(t, decodeJSONMap(t, rec), "status"); got != "active" {
		t.Fatalf("status after re-reading the subscription: got %q, want active", got)
	}
}
