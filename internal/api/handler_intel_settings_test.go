package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prism/internal/intel"
	"prism/internal/intel/checks"
	"prism/internal/intel/jobs"
	"prism/internal/intel/providers"
	"prism/internal/intel/store"
	"prism/internal/service"
	"prism/internal/state"
)

// WP09 §4/§5.4 API surface: the data source settings, the unlock check toggles
// and the budget visibility. Everything runs through httptest against a
// throw-away intel.db and state.db; no test touches the network.

// testIntelKey is the credential of these tests. No response body may contain
// it (R6).
const testIntelKey = "sk-live-api-9f3a-do-not-leak"

// intelCheckSourceForTest resolves the persisted per-check toggle, exactly the
// way the running application does (provider_id "check:<id>").
type intelCheckSourceForTest struct{ engine *state.StateEngine }

func (s intelCheckSourceForTest) CheckEnabled(checkID string) (bool, bool) {
	if s.engine == nil {
		return false, false
	}
	rows, err := s.engine.ListIntelProviderSettings()
	if err != nil {
		return false, false
	}
	for _, row := range rows {
		if row.ProviderID == checks.CheckSettingID(checkID) {
			return row.Enabled, true
		}
	}
	return false, false
}

// wireIntelSettingsForTest builds the intel service with its provider settings
// surface, its checks engine and a user rule directory holding one broken file.
func wireIntelSettingsForTest(t *testing.T, cp *service.ControlPlaneService) (*intel.Service, *providers.Registry, *checks.Engine) {
	t.Helper()

	intelStore, err := store.Open(filepath.Join(t.TempDir(), "state", store.FileName))
	if err != nil {
		t.Fatalf("open intel store: %v", err)
	}
	t.Cleanup(func() { _ = intelStore.Close() })

	registry := providers.NewRegistry()
	providers.RegisterBuiltins(registry, providers.BuiltinConfig{Now: time.Now})
	settings := providers.NewSettingsService(registry, cp.Engine, nil, time.Now)

	userDir := filepath.Join(t.TempDir(), "checks.d")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("create checks.d: %v", err)
	}
	broken := filepath.Join(userDir, "broken.yaml")
	if err := os.WriteFile(broken, []byte("id: broken\nversion: 0\nname: Broken\ncategory: ai\n"), 0o644); err != nil {
		t.Fatalf("write broken rule: %v", err)
	}

	engine := checks.NewEngine(checks.Options{
		UserDir:       userDir,
		EnabledSource: intelCheckSourceForTest{engine: cp.Engine},
		Logf:          func(string, ...any) {},
	})

	svc, err := intel.NewService(intel.Options{
		Store: intelStore,
		Scope: jobs.ScopeResolverFunc(func(context.Context, jobs.Scope) ([]string, error) {
			return nil, nil
		}),
		ProviderSettings: settings,
		CheckEngine:      engine,
		Logf:             func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("intel.NewService: %v", err)
	}
	t.Cleanup(svc.Stop)
	cp.Intel = svc
	return svc, registry, engine
}

func intelProviderItem(t *testing.T, body map[string]any, id string) map[string]any {
	t.Helper()
	raw, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items = %T, want []any (body=%v)", body["items"], body)
	}
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("item = %T, want map", entry)
		}
		if item["id"] == id {
			return item
		}
	}
	t.Fatalf("provider %q missing from the list", id)
	return nil
}

func intelCheckItem(t *testing.T, body map[string]any, id string) map[string]any {
	t.Helper()
	raw, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items = %T, want []any", body["items"])
	}
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("item = %T, want map", entry)
		}
		if item["id"] == id {
			return item
		}
	}
	t.Fatalf("check %q missing from the list", id)
	return nil
}

// TestIntelProvidersAPI_NeverReturnsTheStoredKey is the R6 guard of the API:
// the raw credential string must be absent from every providers response, while
// the running registry really holds it.
func TestIntelProvidersAPI_NeverReturnsTheStoredKey(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	svc, registry, _ := wireIntelSettingsForTest(t, cp)

	rec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/intel/providers/proxycheck", map[string]any{
		"enabled":     true,
		"api_key":     testIntelKey,
		"daily_limit": 900,
		"qps":         1,
		"ttl":         "24h",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), testIntelKey) {
		t.Fatal("the update response leaked the credential")
	}
	body := decodeJSONMap(t, rec)
	if body["has_key"] != true {
		t.Fatalf("has_key = %v, want true", body["has_key"])
	}
	// The credential was really stored: the live registry carries it.
	live, ok := registry.Setting("proxycheck")
	if !ok || live.APIKey != testIntelKey {
		t.Fatal("the patch did not reach the running registry")
	}

	ctx := context.Background()
	now := time.Now().UTC()
	if err := svc.Store().UpsertProviderState(ctx, store.ProviderState{
		Provider:       "proxycheck",
		Day:            store.DayString(now),
		Used:           900,
		Paused:         true,
		ErrorCode:      "PROVIDER_AUTH",
		BlockedUntilNs: now.Add(time.Hour).UnixNano(),
	}); err != nil {
		t.Fatalf("upsert provider state: %v", err)
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/intel/providers", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), testIntelKey) {
		t.Fatal("the providers list leaked the credential")
	}
	if cacheControl := rec.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
	}
	list := decodeJSONMap(t, rec)
	if list["total"].(float64) != float64(len(list["items"].([]any))) {
		t.Fatalf("total does not match the item count: %v", list["total"])
	}
	item := intelProviderItem(t, list, "proxycheck")
	if item["has_key"] != true {
		t.Fatalf("has_key = %v, want true", item["has_key"])
	}
	if item["category"] != "online-ip" || item["direct"] != true || item["via_node"] != false {
		t.Fatalf("category/direct/via_node = %v/%v/%v", item["category"], item["direct"], item["via_node"])
	}
	if item["daily_limit"].(float64) != 900 || item["qps"].(float64) != 1 {
		t.Fatalf("effective settings wrong: %v/%v", item["daily_limit"], item["qps"])
	}
	if _, ok := item["api_key"]; ok {
		t.Fatal("the response carries an api_key field")
	}
	usage, ok := item["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage = %v", item["usage"])
	}
	if usage["used"].(float64) != 900 || usage["exhausted"] != true || usage["remaining"].(float64) != 0 {
		t.Fatalf("budget visibility wrong: %v", usage)
	}
	if usage["paused"] != true || usage["error_code"] != "PROVIDER_AUTH" {
		t.Fatalf("pause visibility wrong: %v", usage)
	}
	if usage["blocked_until_ns"].(float64) != float64(now.Add(time.Hour).UnixNano()) {
		t.Fatalf("blocked_until_ns = %v", usage["blocked_until_ns"])
	}
	if usage["next_allowed_at_ns"].(float64) < float64(now.Add(59*time.Minute).UnixNano()) {
		t.Fatalf("next_allowed_at_ns = %v", usage["next_allowed_at_ns"])
	}

	// The status endpoint shares the state and must not leak either.
	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/intel/status", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), testIntelKey) {
		t.Fatal("the intel status response leaked the credential")
	}
}

// TestIntelProvidersAPIPatchAppliesWithoutRestart proves the live wiring: the
// running registry is rebuilt by the PATCH, with no restart.
func TestIntelProvidersAPIPatchAppliesWithoutRestart(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	_, registry, _ := wireIntelSettingsForTest(t, cp)

	if _, ok := registry.Online("proxycheck"); ok {
		t.Fatal("proxycheck must not be live before it is configured")
	}
	rec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/intel/providers/proxycheck", map[string]any{
		"enabled":     true,
		"api_key":     testIntelKey,
		"daily_limit": 4000,
		"qps":         4,
		"config":      map[string]any{"url": "https://mirror.example.com/check"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := registry.Online("proxycheck"); !ok {
		t.Fatal("the enabled provider has no live implementation")
	}
	live, _ := registry.Setting("proxycheck")
	if live.DailyLimit != 4000 || live.QPS != 4 || live.ConfigString("url") != "https://mirror.example.com/check" {
		t.Fatalf("the running registry did not pick the patch up: limit=%d qps=%v url=%s",
			live.DailyLimit, live.QPS, live.ConfigString("url"))
	}
	if live.CredentialID() == "" {
		t.Fatal("a configured provider needs a credential fingerprint")
	}

	// Disabling takes the provider out of the running queue without a restart.
	rec = doJSONRequest(t, srv, http.MethodPatch, "/api/v1/intel/providers/proxycheck", map[string]any{"enabled": false}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := registry.Online("proxycheck"); ok {
		t.Fatal("a disabled provider stayed live")
	}
	disabled := decodeJSONMap(t, rec)
	if disabled["enabled"] != false || disabled["runnable"] != false {
		t.Fatalf("disabled state = %v/%v", disabled["enabled"], disabled["runnable"])
	}

	// A DNSBL zone edit reaches the live instance through its config.
	rec = doJSONRequest(t, srv, http.MethodPatch, "/api/v1/intel/providers/dnsbl", map[string]any{
		"config": map[string]any{"zones": []string{"zen.spamhaus.org", "bl.spamcop.net"}},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("dnsbl patch status = %d, body=%s", rec.Code, rec.Body.String())
	}
	dnsbl, ok := registry.Setting("dnsbl")
	if !ok || len(dnsbl.ConfigStrings("zones")) != 2 {
		t.Fatalf("dnsbl zones not applied: %+v", dnsbl.Config)
	}
}

// TestIntelProvidersAPIValidation covers the documented rejections.
func TestIntelProvidersAPIValidation(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	wireIntelSettingsForTest(t, cp)

	cases := []struct {
		name   string
		path   string
		body   any
		status int
		code   string
	}{
		{
			name: "unknown provider", path: "/api/v1/intel/providers/nope",
			body: map[string]any{"enabled": true}, status: http.StatusNotFound, code: "NOT_FOUND",
		},
		{
			name: "negative qps", path: "/api/v1/intel/providers/proxycheck",
			body: map[string]any{"qps": -1}, status: http.StatusBadRequest, code: "INVALID_ARGUMENT",
		},
		{
			name: "limit above the vendor cap", path: "/api/v1/intel/providers/proxycheck",
			body: map[string]any{"daily_limit": 2_000_000}, status: http.StatusBadRequest, code: "INVALID_ARGUMENT",
		},
		{
			name: "unknown field", path: "/api/v1/intel/providers/proxycheck",
			body: map[string]any{"nope": 1}, status: http.StatusBadRequest, code: "INVALID_ARGUMENT",
		},
		{
			name: "bad zone", path: "/api/v1/intel/providers/dnsbl",
			body:   map[string]any{"config": map[string]any{"zones": []string{"not a zone"}}},
			status: http.StatusBadRequest, code: "INVALID_ARGUMENT",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doJSONRequest(t, srv, http.MethodPatch, tc.path, tc.body, true)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.status, rec.Body.String())
			}
			body := decodeJSONMap(t, rec)
			errObject, ok := body["error"].(map[string]any)
			if !ok {
				t.Fatalf("error envelope missing: %s", rec.Body.String())
			}
			if errObject["code"] != tc.code {
				t.Fatalf("code = %v, want %s", errObject["code"], tc.code)
			}
			if message, _ := errObject["message"].(string); strings.Contains(message, testIntelKey) {
				t.Fatal("the error message repeated the credential")
			}
		})
	}

	// A rejected credential must not be echoed anywhere.
	rec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/intel/providers/proxycheck",
		map[string]any{"api_key": testIntelKey + "\u0000"}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), testIntelKey) {
		t.Fatal("the validation error repeated the credential")
	}
}

// TestIntelChecksAPIListsTogglesAndReportsLoadErrors covers §5.4 end to end.
func TestIntelChecksAPIListsTogglesAndReportsLoadErrors(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	_, _, engine := wireIntelSettingsForTest(t, cp)

	rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/intel/checks", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeJSONMap(t, rec)
	items, ok := body["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("items = %v", body["items"])
	}
	if body["total"].(float64) != float64(len(items)) {
		t.Fatalf("total = %v, items = %d", body["total"], len(items))
	}
	if body["limit"].(float64) == 0 {
		t.Fatalf("limit = %v", body["limit"])
	}
	chatgpt := intelCheckItem(t, body, "chatgpt")
	if chatgpt["source"] != "builtin" || chatgpt["category"] == "" || chatgpt["ttl"] == "" || chatgpt["timeout"] == "" {
		t.Fatalf("builtin metadata incomplete: %v", chatgpt)
	}
	if chatgpt["enabled"] != true || chatgpt["enabled_override"] != false {
		t.Fatalf("initial toggle state wrong: %v/%v", chatgpt["enabled"], chatgpt["enabled_override"])
	}
	if _, ok := chatgpt["path"]; ok {
		t.Fatalf("a builtin rule must not report a file path: %v", chatgpt["path"])
	}

	loadErrors, ok := body["load_errors"].([]any)
	if !ok || len(loadErrors) != 1 {
		t.Fatalf("load_errors = %v", body["load_errors"])
	}
	loadError := loadErrors[0].(map[string]any)
	if !strings.Contains(loadError["path"].(string), "broken.yaml") {
		t.Fatalf("load error path = %v", loadError["path"])
	}
	if !strings.Contains(loadError["error"].(string), "version") {
		t.Fatalf("load error message = %v", loadError["error"])
	}

	// The toggle is persisted under "check:<id>" and applies to the engine.
	rec = doJSONRequest(t, srv, http.MethodPatch, "/api/v1/intel/checks/chatgpt", map[string]any{"enabled": false}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body=%s", rec.Code, rec.Body.String())
	}
	toggled := decodeJSONMap(t, rec)
	if toggled["enabled"] != false || toggled["enabled_override"] != true {
		t.Fatalf("toggle response = %v/%v", toggled["enabled"], toggled["enabled_override"])
	}
	live := false
	for _, info := range engine.Rules() {
		if info.ID == "chatgpt" {
			live = info.Enabled
		}
	}
	if live {
		t.Fatal("the running rule engine did not pick the toggle up")
	}
	rows, err := cp.Engine.ListIntelProviderSettings()
	if err != nil {
		t.Fatalf("list settings: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.ProviderID == checks.CheckSettingID("chatgpt") {
			found = true
			if row.Enabled {
				t.Fatalf("persisted toggle = %+v", row)
			}
		}
	}
	if !found {
		t.Fatal(`the toggle was not persisted as "check:chatgpt"`)
	}

	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/intel/checks?limit=3&offset=1", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("paged status = %d", rec.Code)
	}
	paged := decodeJSONMap(t, rec)
	if paged["limit"].(float64) != 3 || paged["offset"].(float64) != 1 {
		t.Fatalf("paging envelope = %v/%v", paged["limit"], paged["offset"])
	}
	if len(paged["items"].([]any)) > 3 {
		t.Fatalf("limit ignored: %d items", len(paged["items"].([]any)))
	}
	if strings.Contains(rec.Body.String(), testIntelKey) {
		t.Fatal("the checks list leaked a credential")
	}
}

// TestIntelChecksAPIToggleValidation covers the rejected toggles.
func TestIntelChecksAPIToggleValidation(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	wireIntelSettingsForTest(t, cp)

	rec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/intel/checks/nope", map[string]any{"enabled": true}, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown check status = %d, body=%s", rec.Code, rec.Body.String())
	}
	rec = doJSONRequest(t, srv, http.MethodPatch, "/api/v1/intel/checks/chatgpt", map[string]any{}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing enabled status = %d, body=%s", rec.Code, rec.Body.String())
	}
	rec = doJSONRequest(t, srv, http.MethodPatch, "/api/v1/intel/checks/chatgpt", map[string]any{"ttl": "1h"}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

// TestIntelProvidersAPIResumeClearsThePause covers the WP09 §4 resume action.
func TestIntelProvidersAPIResumeClearsThePause(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	svc, _, _ := wireIntelSettingsForTest(t, cp)

	ctx := context.Background()
	now := time.Now().UTC()
	if err := svc.Store().UpsertProviderState(ctx, store.ProviderState{
		Provider: "proxycheck", Day: store.DayString(now), Used: 3,
		Paused: true, ErrorCode: "PROVIDER_AUTH", BlockedUntilNs: now.Add(time.Hour).UnixNano(),
	}); err != nil {
		t.Fatalf("upsert provider state: %v", err)
	}

	rec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/intel/providers/proxycheck/actions/resume", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume status = %d, body=%s", rec.Code, rec.Body.String())
	}
	usage, ok := decodeJSONMap(t, rec)["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage missing from the resume response")
	}
	if usage["paused"] != false || usage["blocked_until_ns"].(float64) != 0 || usage["error_code"] != "" {
		t.Fatalf("resume did not clear the pause: %v", usage)
	}
	if usage["used"].(float64) != 3 {
		t.Fatalf("resume must keep today's usage: %v", usage)
	}

	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/intel/providers/nope/actions/resume", nil, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

// TestIntelSettingsAPIRequiresAdminAuth keeps the new endpoints behind the admin
// token (R7).
func TestIntelSettingsAPIRequiresAdminAuth(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	wireIntelSettingsForTest(t, cp)

	for _, call := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/v1/intel/providers", nil},
		{http.MethodPatch, "/api/v1/intel/providers/proxycheck", map[string]any{"enabled": true}},
		{http.MethodGet, "/api/v1/intel/checks", nil},
		{http.MethodPatch, "/api/v1/intel/checks/chatgpt", map[string]any{"enabled": false}},
		{http.MethodPost, "/api/v1/intel/providers/proxycheck/actions/resume", nil},
	} {
		rec := doJSONRequest(t, srv, call.method, call.path, call.body, false)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without a token = %d, want 401", call.method, call.path, rec.Code)
		}
	}
}

// TestIntelSettingsAPIWithoutIntelWiring keeps the degraded answer honest.
func TestIntelSettingsAPIWithoutIntelWiring(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	for _, path := range []string{"/api/v1/intel/providers", "/api/v1/intel/checks"} {
		rec := doJSONRequest(t, srv, http.MethodGet, path, nil, true)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s = %d, want 409 (body=%s)", path, rec.Code, rec.Body.String())
		}
	}
	rec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/intel/providers/proxycheck", map[string]any{"enabled": true}, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("patch = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
}
