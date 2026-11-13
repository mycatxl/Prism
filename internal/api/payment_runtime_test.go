package api

import (
	"net/http"
	"strings"
	"testing"

	"prism/internal/model"
)

func TestRuntimePaymentHTTPConfigurationAndCurrencyBoundary(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	rec := admin.do(http.MethodPatch, "/api/v1/system/config", map[string]any{
		"ldc_enabled": true, "ldc_pid": "merchant", "ldc_key": "private-merchant-key", "ldc_rate": 0.5,
		"tron_deposit_enabled": true, "tron_address": e2eUSDTAddress, "tron_trx_enabled": true, "tron_trx_ratio": 0.125, "tron_api_key": "private-grid-key",
	})
	tenantAssertStatus(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "private-") {
		t.Fatal("PATCH leaked gateway credentials")
	}
	rec = admin.do(http.MethodGet, "/api/v1/system/config", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "private-") {
		t.Fatal("GET leaked gateway credentials")
	}
	if !decodeJSONMap(t, rec)["ldc_key_set"].(bool) {
		t.Fatal("missing secret presence indicator")
	}
	_, err := env.cp.CreateAccount("payment-tenant", "payment-tenant-password", model.UserRoleUser, 1)
	if err != nil {
		t.Fatal(err)
	}
	tenant := env.session()
	tenant.mustLogin("payment-tenant", "payment-tenant-password")
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		rec = tenant.do(method, "/api/v1/system/config", map[string]any{"tron_trx_ratio": 9})
		tenantAssertStatus(t, rec, http.StatusNotFound)
	}
	rec = tenant.do(http.MethodPost, "/api/v1/me/deposits", map[string]any{"amount_micro": 50_000_000, "currency": "trx"})
	tenantAssertStatus(t, rec, http.StatusCreated)
	d := decodeJSONMap(t, rec)
	if d["currency"] != "trx" || d["credit_micro"] != float64(6_250_000) {
		t.Fatalf("wrong TRX quote: %v", d)
	}
	id := d["id"].(string)
	rec = admin.do(http.MethodGet, "/api/v1/me/deposits/"+id, nil)
	tenantAssertStatus(t, rec, http.StatusNotFound)
	foreign := rec.Body.String()
	rec = admin.do(http.MethodGet, "/api/v1/me/deposits/missing", nil)
	if rec.Body.String() != foreign {
		t.Fatal("deposit ownership can be enumerated")
	}
	for _, currency := range []string{"eth", "TRX", "trx ", "../usdt"} {
		rec = tenant.do(http.MethodPost, "/api/v1/me/deposits", map[string]any{"amount_micro": 1_000_000, "currency": currency})
		tenantAssertStatus(t, rec, http.StatusBadRequest)
	}
}
