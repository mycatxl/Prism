package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"prism/internal/auth"
)

func TestLDCPaymentRejectsHostWithSignatureDelimiters(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	tenantAssertStatus(t, admin.do(http.MethodPatch, "/api/v1/system/config", map[string]any{"ldc_enabled": true, "ldc_pid": "merchant", "ldc_key": "test-secret"}), http.StatusOK)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/me/ldc/pay", strings.NewReader(`{"points":1}`))
	req.Host = "checkout.test&trade_status=TRADE_SUCCESS&tx=x"
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: admin.session})
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: admin.csrf})
	req.Header.Set(auth.CSRFHeaderName, admin.csrf)
	rec := httptest.NewRecorder()
	env.server.Handler().ServeHTTP(rec, req)
	tenantAssertStatus(t, rec, http.StatusBadRequest)
}
