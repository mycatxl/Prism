package api

import (
	"net/http"
	"testing"
	"time"

	"prism/internal/model"
	"prism/internal/platform"
	"prism/internal/proxy"
	"prism/internal/requestlog"
	"prism/internal/routing"
	"prism/internal/service"
	"prism/internal/topology"
)

func newRoundTwoTenant(t *testing.T, env *tenantE2EEnv, name string) (string, *tenantE2ESession) {
	t.Helper()
	account, err := env.cp.CreateAccount(name, "round-two-tenant-password", model.UserRoleUser, time.Now().UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	session := env.session()
	session.mustLogin(name, "round-two-tenant-password")
	return account.ID, session
}

func TestSelfLeaseHTTPIsolationWithSharedAccountNames(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	aliceID, alice := newRoundTwoTenant(t, env, "lease-alice")
	bobID, bob := newRoundTwoTenant(t, env, "lease-bob")
	pool := topology.NewGlobalNodePool(topology.PoolConfig{MaxConsecutiveFailures: func() int { return 3 }})
	pool.RegisterPlatform(platform.NewPlatform("shared-platform", "Shared", nil, nil))
	env.cp.Pool = pool
	env.cp.Router = routing.NewRouter(routing.RouterConfig{Pool: pool})
	env.cp.Router.RestoreLeases([]model.Lease{
		{PlatformID: "shared-platform", UserID: aliceID, Account: "shared", NodeHash: "11111111111111111111111111111111", EgressIP: "203.0.113.1"},
		{PlatformID: "shared-platform", UserID: bobID, Account: "shared", NodeHash: "11111111111111111111111111111111", EgressIP: "203.0.113.2"},
		{PlatformID: "shared-platform", UserID: bobID, Account: "bob-only", NodeHash: "11111111111111111111111111111111", EgressIP: "203.0.113.3"},
	})
	for _, query := range []string{"", "?user_id=" + bobID} {
		rec := alice.do(http.MethodGet, "/api/v1/me/leases"+query, nil)
		tenantAssertStatus(t, rec, http.StatusOK)
		items := tenantItems(t, rec)
		if len(items) != 1 || items[0]["egress_ip"] != "203.0.113.1" {
			t.Fatalf("lease list leaked another owner: %s", rec.Body.String())
		}
	}
	foreign := alice.do(http.MethodDelete, "/api/v1/me/leases/shared-platform/bob-only", nil)
	missing := alice.do(http.MethodDelete, "/api/v1/me/leases/shared-platform/missing", nil)
	tenantAssertStatus(t, foreign, http.StatusNotFound)
	if foreign.Body.String() != missing.Body.String() {
		t.Fatal("foreign lease existence was disclosed")
	}
	tenantAssertStatus(t, alice.do(http.MethodDelete, "/api/v1/me/leases/shared-platform/shared?user_id="+bobID, nil), http.StatusNoContent)
	if items := tenantItems(t, bob.do(http.MethodGet, "/api/v1/me/leases", nil)); len(items) != 2 {
		t.Fatal("Alice's release removed Bob's lease")
	}
}

func TestBalanceAndPurchaseReplayAreScopedToTenant(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	aliceID, alice := newRoundTwoTenant(t, env, "balance-alice")
	bobID, bob := newRoundTwoTenant(t, env, "balance-bob")
	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	planID := tenantCreatePlan(t, admin, 2)
	tenantAssertStatus(t, admin.do(http.MethodPatch, "/api/v1/admin/plans/"+planID, map[string]any{"price_micro": 2_000_000}), http.StatusOK)
	for _, tc := range []struct {
		id     string
		amount int64
	}{{aliceID, 20_000_000}, {bobID, 30_000_000}} {
		tenantAssertStatus(t, admin.do(http.MethodPost, "/api/v1/admin/users/"+tc.id+"/balance", map[string]any{"delta_micro": tc.amount, "note": "private-credit-" + tc.id}), http.StatusOK)
	}
	subIDs := make(map[string]bool)
	for _, tc := range []struct {
		id, other string
		browser   *tenantE2ESession
		balance   int64
	}{{aliceID, bobID, alice, 18_000_000}, {bobID, aliceID, bob, 28_000_000}} {
		input := map[string]any{"plan_id": planID, "request_id": "same-request-id"}
		rec := tc.browser.do(http.MethodPost, "/api/v1/me/purchases?user_id="+tc.other, input)
		tenantAssertStatus(t, rec, http.StatusCreated)
		result := decodeJSONMap(t, rec)
		if result["charged_micro"] != float64(2_000_000) || result["balance_micro"] != float64(tc.balance) {
			t.Fatalf("cross-owner purchase replay: %s", rec.Body.String())
		}
		sub := tenantNested(t, result, "subscription")
		if sub["user_id"] != tc.id {
			t.Fatalf("subscription assigned to another owner: %v", sub)
		}
		subIDs[tenantField(t, sub, "id")] = true
		replayed := tc.browser.do(http.MethodPost, "/api/v1/me/purchases", input)
		tenantAssertStatus(t, replayed, http.StatusCreated)
		if decodeJSONMap(t, replayed)["charged_micro"] != float64(0) {
			t.Fatal("same-owner purchase replay charged twice")
		}
		own := tc.browser.do(http.MethodGet, "/api/v1/me/balance", nil)
		selected := tc.browser.do(http.MethodGet, "/api/v1/me/balance?user_id="+tc.other, nil)
		tenantAssertStatus(t, own, http.StatusOK)
		if selected.Code != own.Code || selected.Body.String() != own.Body.String() {
			t.Fatal("query selected another tenant's balance or ledger")
		}
		view := decodeJSONMap(t, own)
		if view["balance_micro"] != float64(tc.balance) {
			t.Fatalf("wrong balance: %v", view)
		}
		ledger, ok := view["ledger"].([]any)
		if !ok || len(ledger) != 2 {
			t.Fatalf("unexpected ledger: %v", view)
		}
		for _, row := range ledger {
			entry, ok := row.(map[string]any)
			if !ok || entry["user_id"] != tc.id {
				t.Fatalf("foreign ledger entry: %v", row)
			}
		}
	}
	if len(subIDs) != 2 {
		t.Fatal("two tenants shared a purchase result")
	}
}

func TestGlobalTelemetryRejectsTenantWithIndistinguishableNotFound(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	_, tenant := newRoundTwoTenant(t, env, "telemetry-tenant")
	logs := requestlog.NewRepo(t.TempDir(), 1<<20, 2)
	if err := logs.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logs.Close() })
	if _, err := logs.InsertBatch([]proxy.RequestLogEntry{{StartedAtNs: time.Now().UnixNano(), ProxyType: proxy.ProxyTypeReverse, TargetHost: "private.example", ReqBody: []byte("private-request-body"), ReqBodyLen: 20}}); err != nil {
		t.Fatal(err)
	}
	rows, _, _, err := logs.List(requestlog.ListFilter{Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("request-log fixture: %v %v", rows, err)
	}
	env.server = NewServer(0, service.SystemInfo{}, env.cp.RuntimeCfg, env.cp.EnvCfg, env.cp, 1<<20, logs, newTestMetricsManager(t, "plat-id"), nil, &AuthDeps{Identity: env.identity})
	missing := tenant.do(http.MethodGet, "/api/v1/not-a-real-route", nil)
	tenantAssertStatus(t, missing, http.StatusNotFound)
	paths := []string{
		"/api/v1/audit-logs", "/api/v1/request-logs", "/api/v1/request-logs/" + rows[0].ID, "/api/v1/request-logs/" + rows[0].ID + "/payloads",
		"/api/v1/metrics/realtime/throughput", "/api/v1/metrics/realtime/connections", "/api/v1/metrics/realtime/leases",
		"/api/v1/metrics/history/traffic", "/api/v1/metrics/history/requests", "/api/v1/metrics/history/access-latency", "/api/v1/metrics/history/probes", "/api/v1/metrics/history/node-pool", "/api/v1/metrics/history/lease-lifetime?platform_id=plat-id",
		"/api/v1/metrics/snapshots/node-pool", "/api/v1/metrics/snapshots/platform-node-pool?platform_id=plat-id", "/api/v1/metrics/snapshots/node-latency-distribution",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			// Positive control: the endpoint is mounted and returns real data.
			tenantAssertStatus(t, env.doAsKey(http.MethodGet, path, nil), http.StatusOK)
			rec := tenant.do(http.MethodGet, path, nil)
			if rec.Code != missing.Code || rec.Body.String() != missing.Body.String() || rec.Header().Get("Allow") != "" {
				t.Fatalf("telemetry route leaked existence or content: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
