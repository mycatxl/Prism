package api

import (
	"net/http"
	"reflect"
	"testing"
)

func TestNodePrivacyRequiresAdminConsoleAndCannotSpoofDiscovery(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	before := env.cp.RuntimeCfg.Load()
	body := map[string]any{"node_privacy": map[string]any{"echo_enabled": true, "own_cidrs": []string{"192.0.2.0/24"}}}
	tenantAssertStatus(t, env.doAsKey(http.MethodPatch, "/api/v1/system/config", body), http.StatusForbidden)
	if !reflect.DeepEqual(before, env.cp.RuntimeCfg.Load()) {
		t.Fatal("automation mutated privacy policy")
	}
	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	tenantAssertStatus(t, admin.do(http.MethodPatch, "/api/v1/system/config", body), http.StatusOK)
	tenantAssertStatus(t, admin.do(http.MethodPatch, "/api/v1/system/config", map[string]any{"node_privacy": map[string]any{"probe_status": "ready"}}), http.StatusBadRequest)
}
