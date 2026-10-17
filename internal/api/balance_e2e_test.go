package api

import (
	"net/http"
	"testing"

	"prism/internal/config"
)

// The money surface, driven through the real HTTP stack: the real session, the
// real role gate, the real service layer over a live state engine.
//
// What matters here is not that the handlers call the service - the service
// tests already prove the arithmetic - but that the console's own request shapes
// work, that a tenant cannot reach the operator's side of the surface, and that
// a refusal never tells an attacker which codes exist.

func TestBalance_E2E_RedeemAndAdjust(t *testing.T) {
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

	// A user who has never been credited reads zero, not an error: the balance
	// table is a cache of the ledger, and no ledger rows is a zero balance.
	tenant := env.session()
	tenant.mustLogin(tenantE2ETenantUser, tenantE2ETenantPass)
	rec = tenant.do(http.MethodGet, "/api/v1/me/balance", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	if got := decodeJSONMap(t, rec)["balance_micro"]; got != float64(0) {
		t.Fatalf("balance before any credit: got %v, want 0", got)
	}

	// Mint a batch. The plaintext comes back once, and only here.
	rec = admin.do(http.MethodPost, "/api/v1/admin/redeem-codes", map[string]any{
		"amount_micro":    int64(12_500_000),
		"max_uses":        1,
		"expires_in_days": 0,
		"note":            "e2e",
		"count":           2,
	})
	tenantAssertStatus(t, rec, http.StatusCreated)
	minted, _ := decodeJSONMap(t, rec)["codes"].([]any)
	if len(minted) != 2 {
		t.Fatalf("minted codes: got %d, want 2", len(minted))
	}
	first, _ := minted[0].(map[string]any)
	code, _ := first["code"].(string)
	if code == "" {
		t.Fatalf("a minted code must carry its plaintext: %v", first)
	}

	// The listing afterwards carries the hint and never the plaintext.
	rec = admin.do(http.MethodGet, "/api/v1/admin/redeem-codes", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	listed := tenantItems(t, rec)
	if len(listed) != 2 {
		t.Fatalf("listed codes: got %d, want 2", len(listed))
	}
	for _, item := range listed {
		if _, present := item["code"]; present {
			t.Fatalf("a listed code must not carry its plaintext: %v", item)
		}
		if hint, _ := item["code_hint"].(string); hint == "" {
			t.Fatalf("a listed code must carry its hint: %v", item)
		}
	}

	// Redeem it. The response carries the new balance, which is what the panel
	// renders; the ledger is read back separately.
	rec = tenant.do(http.MethodPost, "/api/v1/me/redeem", map[string]any{"code": code})
	tenantAssertStatus(t, rec, http.StatusOK)
	if got := decodeJSONMap(t, rec)["balance_micro"]; got != float64(12_500_000) {
		t.Fatalf("balance after redeem: got %v, want 12500000", got)
	}

	rec = tenant.do(http.MethodGet, "/api/v1/me/balance", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	body := decodeJSONMap(t, rec)
	if got := body["balance_micro"]; got != float64(12_500_000) {
		t.Fatalf("read-back balance: got %v, want 12500000", got)
	}
	ledger, _ := body["ledger"].([]any)
	if len(ledger) != 1 {
		t.Fatalf("ledger rows: got %d, want 1", len(ledger))
	}
	entry, _ := ledger[0].(map[string]any)
	if got := entry["reason"]; got != "redeem" {
		t.Fatalf("ledger reason: got %v, want redeem", got)
	}

	// The same code again: a one-use code is spent, and the refusal is the same
	// one an unknown code gets - the endpoint must not confirm which codes exist.
	spent := tenant.do(http.MethodPost, "/api/v1/me/redeem", map[string]any{"code": code})
	tenantAssertStatus(t, spent, http.StatusBadRequest)
	unknown := tenant.do(http.MethodPost, "/api/v1/me/redeem", map[string]any{
		"code": "AAAA-BBBB-CCCC-DDDD-EEEE",
	})
	tenantAssertStatus(t, unknown, http.StatusBadRequest)
	if spent.Body.String() != unknown.Body.String() {
		t.Fatalf("a spent code and an unknown code must read the same:\n spent=%s\nunknown=%s",
			spent.Body.String(), unknown.Body.String())
	}

	// An operator adjustment needs a note, and a debit larger than the balance is
	// refused rather than allowed to go negative.
	rec = admin.do(http.MethodPost, "/api/v1/admin/users/"+userID+"/balance", map[string]any{
		"delta_micro": int64(-1_000_000),
	})
	tenantAssertStatus(t, rec, http.StatusBadRequest)

	rec = admin.do(http.MethodPost, "/api/v1/admin/users/"+userID+"/balance", map[string]any{
		"delta_micro": int64(-2_500_000),
		"note":        "e2e debit",
	})
	tenantAssertStatus(t, rec, http.StatusOK)
	if got := decodeJSONMap(t, rec)["balance_micro"]; got != float64(10_000_000) {
		t.Fatalf("balance after debit: got %v, want 10000000", got)
	}

	rec = admin.do(http.MethodPost, "/api/v1/admin/users/"+userID+"/balance", map[string]any{
		"delta_micro": int64(-20_000_000),
		"note":        "overdraw",
	})
	tenantAssertStatus(t, rec, http.StatusBadRequest)

	// The operator reads the same balance the tenant does.
	rec = admin.do(http.MethodGet, "/api/v1/admin/users/"+userID+"/balance", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	if got := decodeJSONMap(t, rec)["balance_micro"]; got != float64(10_000_000) {
		t.Fatalf("operator read of the balance: got %v, want 10000000", got)
	}

	// Withdrawing a code stops it working and leaves the money it already moved.
	remaining, _ := minted[1].(map[string]any)
	remainingID, _ := remaining["id"].(string)
	remainingCode, _ := remaining["code"].(string)
	rec = admin.do(http.MethodDelete, "/api/v1/admin/redeem-codes/"+remainingID, nil)
	tenantAssertStatus(t, rec, http.StatusNoContent)
	rec = tenant.do(http.MethodPost, "/api/v1/me/redeem", map[string]any{"code": remainingCode})
	tenantAssertStatus(t, rec, http.StatusBadRequest)
	rec = tenant.do(http.MethodGet, "/api/v1/me/balance", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	if got := decodeJSONMap(t, rec)["balance_micro"]; got != float64(10_000_000) {
		t.Fatalf("balance after a withdrawn code: got %v, want 10000000", got)
	}
}

// TestBalance_E2E_TenantCannotReachTheOperatorSurface is the role gate over the
// money routes specifically. The general gate is proven elsewhere; this asserts
// that no money route was added outside it.
func TestBalance_E2E_TenantCannotReachTheOperatorSurface(t *testing.T) {
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

	tenant := env.session()
	tenant.mustLogin(tenantE2ETenantUser, tenantE2ETenantPass)

	for _, path := range []string{
		"/api/v1/admin/redeem-codes",
		"/api/v1/admin/users/" + userID + "/balance",
	} {
		rec := tenant.do(http.MethodGet, path, nil)
		tenantAssertStatus(t, rec, http.StatusNotFound)
	}

	// A write is refused the same way, not merely unauthorised: the shape of the
	// answer is what the role gate promises.
	rec = tenant.do(http.MethodPost, "/api/v1/admin/redeem-codes", map[string]any{
		"amount_micro": int64(1_000_000),
		"count":        1,
	})
	tenantAssertStatus(t, rec, http.StatusNotFound)

	rec = tenant.do(http.MethodPost, "/api/v1/admin/users/"+userID+"/balance", map[string]any{
		"delta_micro": int64(1_000_000),
		"note":        "self-service credit",
	})
	tenantAssertStatus(t, rec, http.StatusNotFound)
}
