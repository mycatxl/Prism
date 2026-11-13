package api

import (
	"net/http"
	"reflect"
	"testing"
)

func TestPaymentConfigurationRequiresConsoleSession(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	before := env.cp.RuntimeCfg.Load()
	fields := map[string]any{
		"ldc_enabled": false, "ldc_gateway_url": "https://payments.example/epay", "ldc_pid": "other-merchant",
		"ldc_key": "other-secret", "ldc_rate": 2, "ldc_notify_url": "https://merchant.example/notify", "ldc_return_url": "https://merchant.example/return",
		"tron_deposit_enabled": false, "tron_address": e2eUSDTAddress, "tron_api_key": "other-grid-key",
		"tron_usdt_contract": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", "tron_trx_enabled": false, "tron_trx_ratio": 2, "tron_window_minutes": 30,
	}
	for field, value := range fields {
		t.Run(field, func(t *testing.T) {
			rec := env.doAsKey(http.MethodPatch, "/api/v1/system/config", map[string]any{field: value})
			tenantAssertStatus(t, rec, http.StatusForbidden)
			assertErrorCode(t, rec, "CREDENTIAL_MANAGEMENT_REQUIRES_SESSION")
		})
	}
	if !reflect.DeepEqual(before, env.cp.RuntimeCfg.Load()) {
		t.Fatal("refused automation key changed payment configuration")
	}
	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	tenantAssertStatus(t, admin.do(http.MethodPatch, "/api/v1/system/config", map[string]any{"ldc_key": "console-secret", "ldc_rate": 2}), http.StatusOK)
	// Ordinary operational settings retain the existing automation capability.
	tenantAssertStatus(t, env.doAsKey(http.MethodPatch, "/api/v1/system/config", map[string]any{"max_consecutive_failures": 4}), http.StatusOK)
}

func TestPaymentConfigurationRejectsAmbiguousSignedValues(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	before := env.cp.RuntimeCfg.Load()
	for field, value := range map[string]string{
		"ldc_pid":        "merchant&extra=value",
		"ldc_notify_url": "https://merchant.example/callback&extra",
		"ldc_return_url": "https://merchant.example/return&extra",
	} {
		tenantAssertStatus(t, admin.do(http.MethodPatch, "/api/v1/system/config", map[string]any{field: value}), http.StatusBadRequest)
	}
	if !reflect.DeepEqual(before, env.cp.RuntimeCfg.Load()) {
		t.Fatal("invalid payment configuration was persisted")
	}
}
