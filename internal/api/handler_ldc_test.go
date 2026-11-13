package api

import (
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
)

const apiLDCTestKey = "api-ldc-test-secret"

func TestLDCEndpointsInitiateAndAcceptPublicCallback(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	env.cp.EnvCfg.LDCPID = "10001"
	env.cp.EnvCfg.LDCKey = apiLDCTestKey
	env.cp.EnvCfg.LDCGatewayURL = "https://payments.example/epay"
	env.cp.EnvCfg.LDCNotifyURL = "https://prism.example/api/v1/public/ldc/notify"
	env.cp.EnvCfg.LDCReturnURL = "https://prism.example/ui/me/deposit"
	env.cp.InitializeLDC()

	// The info route is authenticated and reports the live client state.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/ldc/info", nil)
	req.Header.Set("Authorization", "Bearer "+tenantE2EAdminKey)
	rec := httptest.NewRecorder()
	env.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("LDC info status: got %d, body=%s", rec.Code, rec.Body.String())
	}
	info := decodeJSONMap(t, rec)
	if enabled, _ := info["enabled"].(bool); !enabled {
		t.Fatalf("LDC info: got disabled state %v", info)
	}

	session := env.session()
	session.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	rec = session.do(http.MethodPost, "/api/v1/me/ldc/pay", map[string]any{"points": 12.5})
	if rec.Code != http.StatusCreated {
		t.Fatalf("LDC pay status: got %d, body=%s", rec.Code, rec.Body.String())
	}
	payment := decodeJSONMap(t, rec)
	redirect, _ := payment["redirect_url"].(string)
	outTradeNo, _ := payment["out_trade_no"].(string)
	if redirect == "" || outTradeNo == "" {
		t.Fatalf("LDC pay response missing fields: %v", payment)
	}

	parsed, err := url.Parse(redirect)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	callback := map[string]string{
		"pid":          parsed.Query().Get("pid"),
		"trade_status": "TRADE_SUCCESS",
		"out_trade_no": outTradeNo,
		"trade_no":     "api-gateway-trade",
		"money":        parsed.Query().Get("money"),
		"sign_type":    "MD5",
	}
	callback = signedAPILDCCallback(apiLDCTestKey, callback)

	// The provider callback has no session, CSRF token, or bearer credential.
	callbackValues := url.Values{}
	for name, value := range callback {
		callbackValues.Set(name, value)
	}
	callbackURL := "/api/v1/public/ldc/notify?" + callbackValues.Encode()
	req = httptest.NewRequest(http.MethodGet, callbackURL, nil)
	rec = httptest.NewRecorder()
	env.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "success" {
		t.Fatalf("LDC GET callback: status=%d body=%q", rec.Code, rec.Body.String())
	}

	// POST form callbacks are accepted too, and the replay must be harmless.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/public/ldc/notify", strings.NewReader(callbackValues.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	env.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "success" {
		t.Fatalf("LDC POST replay: status=%d body=%q", rec.Code, rec.Body.String())
	}

	balance, err := env.cp.Engine.GetBalance(env.adminID)
	if err != nil {
		t.Fatalf("read credited balance: %v", err)
	}
	if balance != 12_500_000 {
		t.Fatalf("credited balance = %d, want 12500000", balance)
	}
}

func TestLDCEndpointsRequireAuthenticationForSelfService(t *testing.T) {
	env := newTenantE2EEnv(t, "off")
	env.cp.EnvCfg.LDCPID = "10001"
	env.cp.EnvCfg.LDCKey = apiLDCTestKey
	env.cp.InitializeLDC()

	for _, tc := range []struct {
		method string
		path   string
		body   io.Reader
	}{
		{http.MethodGet, "/api/v1/me/ldc/info", nil},
		{http.MethodPost, "/api/v1/me/ldc/pay", strings.NewReader(`{"points":1}`)},
	} {
		req := httptest.NewRequest(tc.method, tc.path, tc.body)
		if tc.body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		env.server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without auth: got %d, body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func signedAPILDCCallback(key string, params map[string]string) map[string]string {
	copyParams := make(map[string]string, len(params)+1)
	for name, value := range params {
		copyParams[name] = value
	}
	keys := make([]string, 0, len(copyParams))
	for name, value := range copyParams {
		if name == "sign" || name == "sign_type" || value == "" {
			continue
		}
		keys = append(keys, name)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, name := range keys {
		parts = append(parts, name+"="+copyParams[name])
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + key))
	copyParams["sign"] = hex.EncodeToString(sum[:])
	return copyParams
}
