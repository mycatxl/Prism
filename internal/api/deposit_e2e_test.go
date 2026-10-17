package api

import (
	"net/http"
	"testing"
)

// The deposit surface, end to end: what a signed-in user can see and open, and
// what an unconfigured deployment answers.
//
// The chain listener itself is not exercised here - it needs TronGrid - but the
// intent lifecycle it writes to is covered by the state layer, and the routes
// below are the contract between the two.

const e2eUSDTAddress = "TQn9Y2khEsLJW1ChVWFMSMeRDow5KcbLSE"

// With no receiving address configured, deposits report themselves unavailable
// and opening one is refused. That is a supported state, not a broken one.
func TestDepositEndpoints_UnconfiguredReportsDisabled(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	session := env.session()
	session.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)

	rec := session.do(http.MethodGet, "/api/v1/me/deposit", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	body := decodeJSONMap(t, rec)
	if enabled, _ := body["enabled"].(bool); enabled {
		t.Fatalf("deposit info: enabled must be false without an address, got %v", body)
	}

	rec = session.do(http.MethodPost, "/api/v1/me/deposits", map[string]any{"amount_micro": 10_000_000})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("opening a deposit without an address: got %d, want 400", rec.Code)
	}
}

func TestDepositEndpoints_OpenReadAndList(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	env.cp.EnvCfg.USDTAddress = e2eUSDTAddress
	session := env.session()
	session.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)

	rec := session.do(http.MethodGet, "/api/v1/me/deposit", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	info := decodeJSONMap(t, rec)
	if enabled, _ := info["enabled"].(bool); !enabled {
		t.Fatalf("deposit info: enabled must be true with an address, got %v", info)
	}
	if got, _ := info["address"].(string); got != e2eUSDTAddress {
		t.Fatalf("deposit info: address got %q, want %q", got, e2eUSDTAddress)
	}

	rec = session.do(http.MethodPost, "/api/v1/me/deposits", map[string]any{"amount_micro": 10_000_000})
	tenantAssertStatus(t, rec, http.StatusCreated)
	opened := decodeJSONMap(t, rec)
	id, _ := opened["id"].(string)
	if id == "" {
		t.Fatalf("opened deposit: id is missing: %v", opened)
	}
	// The amount handed out carries a non-zero fractional part.
	amount, _ := opened["amount_micro"].(float64)
	if int64(amount) != 10_000_001 {
		t.Fatalf("opened deposit: amount got %v, want 10000001", amount)
	}
	if status, _ := opened["status"].(string); status != "pending" {
		t.Fatalf("opened deposit: status got %q, want pending", status)
	}

	rec = session.do(http.MethodGet, "/api/v1/me/deposits/"+id, nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	read := decodeJSONMap(t, rec)
	if got, _ := read["id"].(string); got != id {
		t.Fatalf("read deposit: id got %q, want %q", got, id)
	}

	rec = session.do(http.MethodGet, "/api/v1/me/deposits", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	items := tenantItems(t, rec)
	if len(items) != 1 {
		t.Fatalf("deposit list: got %d items, want 1: %v", len(items), items)
	}
}

// A deposit id that belongs to nobody answers exactly like one that does not
// exist, so the route cannot be used to probe for ids.
func TestDepositEndpoints_UnknownIntentIsNotFound(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	env.cp.EnvCfg.USDTAddress = e2eUSDTAddress
	session := env.session()
	session.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)

	rec := session.do(http.MethodGet, "/api/v1/me/deposits/dep_missing", nil)
	tenantAssertStatus(t, rec, http.StatusNotFound)
	first := rec.Body.String()

	rec = session.do(http.MethodGet, "/api/v1/me/deposits/dep_also_missing", nil)
	tenantAssertStatus(t, rec, http.StatusNotFound)
	if rec.Body.String() != first {
		t.Fatalf("two unknown ids answer differently:\n%s\n%s", first, rec.Body.String())
	}
}

// The deposit routes sit behind the session like every other /me route.
func TestDepositEndpoints_RequireAuth(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	session := env.session()

	for _, path := range []string{"/api/v1/me/deposit", "/api/v1/me/deposits", "/api/v1/me/deposits/dep_x"} {
		rec := session.do(http.MethodGet, path, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without a session: got %d, want 401", path, rec.Code)
		}
	}
}
