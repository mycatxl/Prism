package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"prism/internal/auth"
	"prism/internal/model"
)

func TestTOTPEnrollmentRevokesBothExistingBrowserSessions(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	first, second := env.session(), env.session()
	first.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	second.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	oldToken, oldCSRF := first.session, first.csrf
	rec := first.do(http.MethodPost, "/api/v1/auth/totp/enrol", map[string]any{})
	tenantAssertStatus(t, rec, http.StatusOK)
	secret := tenantField(t, decodeJSONMap(t, rec), "secret")
	code, err := auth.TOTPCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec = first.do(http.MethodPost, "/api/v1/auth/totp/confirm", map[string]any{"secret": secret, "code": code})
	tenantAssertStatus(t, rec, http.StatusOK)
	for _, name := range []string{auth.SessionCookieName, auth.CSRFCookieName} {
		cleared := false
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == name && cookie.MaxAge < 0 {
				cleared = true
			}
		}
		if !cleared {
			t.Fatalf("confirmation did not clear %s", name)
		}
	}
	first.session, first.csrf = oldToken, oldCSRF // replay the revoked cookie, not just an empty jar
	for _, browser := range []*tenantE2ESession{first, second} {
		tenantAssertStatus(t, browser.do(http.MethodGet, "/api/v1/me/overview", nil), http.StatusUnauthorized)
	}
	tenantAssertStatus(t, env.session().login(tenantE2EAdminUser, tenantE2EAdminPassword), http.StatusUnauthorized)
}

func TestLDCAPIKeepsDecimalPrecisionUntilValidation(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	tenantAssertStatus(t, admin.do(http.MethodPatch, "/api/v1/system/config", map[string]any{"ldc_enabled": true, "ldc_pid": "merchant", "ldc_key": "test-secret"}), http.StatusOK)
	for _, raw := range []string{"12.5000000001", "1000000.0000000001", "1e6", "-1", "0.001", "9223372036854775808"} {
		tenantAssertStatus(t, admin.do(http.MethodPost, "/api/v1/me/ldc/pay", json.RawMessage(`{"points":`+raw+`}`)), http.StatusBadRequest)
	}
	for _, raw := range []string{"0.01", "12.50", "1000000"} {
		tenantAssertStatus(t, admin.do(http.MethodPost, "/api/v1/me/ldc/pay", json.RawMessage(`{"points":`+raw+`}`)), http.StatusCreated)
	}
}

func TestTenantCannotRevokeForeignSessionOrSelectForeignLists(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	owner := env.session()
	owner.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	if _, err := env.cp.CreateAccount("boundary-tenant", "boundary-tenant-password", model.UserRoleUser, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	tenant := env.session()
	tenant.mustLogin("boundary-tenant", "boundary-tenant-password")
	rec := owner.do(http.MethodGet, "/api/v1/me/security/sessions", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	var body struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Items) == 0 {
		t.Fatalf("session response: %s err=%v", rec.Body.String(), err)
	}
	tenantAssertStatus(t, tenant.do(http.MethodDelete, "/api/v1/me/security/sessions/"+body.Items[0].ID, nil), http.StatusNotFound)
	tenantAssertStatus(t, owner.do(http.MethodGet, "/api/v1/me/overview", nil), http.StatusOK)
	for _, path := range []string{"keys", "deposits", "security/sessions", "usage"} {
		own := tenant.do(http.MethodGet, "/api/v1/me/"+path, nil)
		foreign := tenant.do(http.MethodGet, "/api/v1/me/"+path+"?user_id="+env.adminID, nil)
		tenantAssertStatus(t, own, http.StatusOK)
		if foreign.Code != own.Code || foreign.Body.String() != own.Body.String() {
			t.Fatalf("caller-selected owner changed %s", path)
		}
	}
}

func TestEveryAdminEndpointRejectsTenantSession(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	if _, err := env.cp.CreateAccount("admin-boundary-tenant", "admin-boundary-password", model.UserRoleUser, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	tenant := env.session()
	tenant.mustLogin("admin-boundary-tenant", "admin-boundary-password")
	routes := []struct{ method, path string }{
		{"GET", "/api/v1/admin/plans"},
		{"POST", "/api/v1/admin/plans"},
		{"GET", "/api/v1/admin/plans/foreign-id"},
		{"PATCH", "/api/v1/admin/plans/foreign-id"},
		{"DELETE", "/api/v1/admin/plans/foreign-id"},
		{"GET", "/api/v1/admin/users"},
		{"POST", "/api/v1/admin/users"},
		{"GET", "/api/v1/admin/users/foreign-id"},
		{"PATCH", "/api/v1/admin/users/foreign-id"},
		{"DELETE", "/api/v1/admin/users/foreign-id"},
		{"GET", "/api/v1/admin/users/foreign-id/keys"},
		{"POST", "/api/v1/admin/users/foreign-id/keys"},
		{"PATCH", "/api/v1/admin/users/foreign-id/keys/foreign-id"},
		{"DELETE", "/api/v1/admin/users/foreign-id/keys/foreign-id"},
		{"GET", "/api/v1/admin/users/foreign-id/usage"},
		{"GET", "/api/v1/admin/subscriptions"},
		{"POST", "/api/v1/admin/subscriptions"},
		{"GET", "/api/v1/admin/subscriptions/foreign-id"},
		{"PATCH", "/api/v1/admin/subscriptions/foreign-id"},
		{"DELETE", "/api/v1/admin/subscriptions/foreign-id"},
		{"GET", "/api/v1/admin/invites"},
		{"POST", "/api/v1/admin/invites"},
		{"DELETE", "/api/v1/admin/invites/foreign-id"},
		{"GET", "/api/v1/admin/usage/summary"},
		{"GET", "/api/v1/admin/usage/users"},
		{"GET", "/api/v1/admin/usage/daily"},
		{"GET", "/api/v1/admin/usage/platforms"},
		{"GET", "/api/v1/admin/usage/plans"},
		{"GET", "/api/v1/admin/alerts"},
		{"POST", "/api/v1/admin/alerts/foreign-id/actions/acknowledge"},
		{"GET", "/api/v1/admin/redeem-codes"},
		{"POST", "/api/v1/admin/redeem-codes"},
		{"DELETE", "/api/v1/admin/redeem-codes/foreign-id"},
		{"GET", "/api/v1/admin/users/foreign-id/balance"},
		{"POST", "/api/v1/admin/users/foreign-id/balance"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			tenantAssertStatus(t, tenant.do(route.method, route.path, map[string]any{}), http.StatusNotFound)
		})
	}
}
