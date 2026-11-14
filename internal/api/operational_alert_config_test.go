package api

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"prism/internal/alerting"
)

type alertTestTransport func(*http.Request) (*http.Response, error)

func (f alertTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOperationalAlertSettingsAreValidatedPersistedAndMasked(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	const token = "123456:TEST_TOKEN_FOR_LOCAL_FIXTURES_ONLY"
	body := map[string]any{"alert_telegram_enabled": true, "alert_telegram_bot_token": token, "alert_telegram_chat_id": "-1001234567", "alert_disk_free_percent": 10, "alert_cooldown_minutes": 15}
	denied := env.doAsKey(http.MethodPatch, "/api/v1/system/config", body)
	if denied.Code != http.StatusForbidden {
		t.Fatal("automation key may not change notification credentials")
	}
	rec := admin.do(http.MethodPatch, "/api/v1/system/config", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("save status = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), token) || decodeJSONMap(t, rec)["alert_telegram_bot_token_set"] != true {
		t.Fatal("save response did not mask the token")
	}
	if env.cp.RuntimeCfg.Load().AlertTelegramBotToken != token {
		t.Fatal("secret was not retained internally")
	}
	persisted, _, err := env.cp.Engine.GetSystemConfig()
	if err != nil || persisted.AlertTelegramBotToken != token {
		t.Fatal("secret was not persisted")
	}
	for _, path := range []string{"/api/v1/system/config", "/api/v1/system/config/default"} {
		get := admin.do(http.MethodGet, path, nil)
		if strings.Contains(get.Body.String(), token) {
			t.Fatal("read endpoint exposed the token")
		}
	}
	for _, patch := range []map[string]any{{"alert_telegram_chat_id": ""}, {"alert_telegram_bot_token": "bad"}, {"alert_disk_free_percent": -1}, {"alert_cooldown_minutes": 1441}, {"alert_telegram_bot_token_set": false}} {
		invalid := admin.do(http.MethodPatch, "/api/v1/system/config", patch)
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("invalid patch accepted: %s", invalid.Body.String())
		}
	}
	cleared := admin.do(http.MethodPatch, "/api/v1/system/config", map[string]any{"alert_telegram_enabled": false, "alert_telegram_bot_token": "", "alert_telegram_chat_id": ""})
	if cleared.Code != http.StatusOK || decodeJSONMap(t, cleared)["alert_telegram_bot_token_set"] != false {
		t.Fatal("explicit disabled secret clearing failed")
	}
}

func TestOperationalAlertTestRequiresSessionAndReturnsSafeDeliveryResults(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	cfg := *env.cp.RuntimeCfg.Load()
	cfg.AlertTelegramEnabled = true
	cfg.AlertTelegramBotToken = "123456:TEST_TOKEN_FOR_LOCAL_FIXTURES_ONLY"
	cfg.AlertTelegramChatID = "-1001234567"
	env.cp.RuntimeCfg.Store(&cfg)
	status, calls := 200, 0
	env.cp.AlertNotifier = alerting.NewNotifier(env.cp.RuntimeCfg.Load, &http.Client{Transport: alertTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme != "https" || r.URL.Host != "api.telegram.org" || r.Header.Get("Cookie") != "" {
			t.Error("notification request forwarded browser authority")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"ok":true,"description":"private provider diagnostic"}`)), Header: http.Header{}}, nil
	})}, nil)
	defer env.cp.AlertNotifier.Stop()
	denied := env.doAsKey(http.MethodPost, "/api/v1/system/alerts/test", map[string]any{})
	if denied.Code != http.StatusForbidden || calls != 0 {
		t.Fatal("automation key sent an external notification")
	}
	confirmed := admin.do(http.MethodPost, "/api/v1/system/alerts/test", map[string]any{})
	if confirmed.Code != http.StatusOK || calls != 1 || decodeJSONMap(t, confirmed)["delivered"] != true {
		t.Fatal("saved channel test did not confirm delivery")
	}
	status = 401
	failed := admin.do(http.MethodPost, "/api/v1/system/alerts/test", map[string]any{})
	if failed.Code != http.StatusBadGateway || strings.Contains(failed.Body.String(), cfg.AlertTelegramBotToken) || strings.Contains(failed.Body.String(), "private provider diagnostic") {
		t.Fatal("delivery error leaked provider details or did not fail")
	}
}
