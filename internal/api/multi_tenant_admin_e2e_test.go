package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/auth"
	"prism/internal/config"
	"prism/internal/keys"
	"prism/internal/model"
	"prism/internal/service"
	"prism/internal/state"
	"prism/internal/usage"
)

// This file is the acceptance walk of the multi-tenant phase, driven through the
// real HTTP stack: the real session service, the real cookie and CSRF rules, the
// real role gate and the real service layer over a live state engine. Nothing is
// faked, so a green run means the console's own request shapes work.

const (
	tenantE2EAdminUser     = "root"
	tenantE2EAdminPassword = "root-password-1234"
	tenantE2ETenantUser    = "tenant-one"
	tenantE2ETenantPass    = "tenant-one-password"
	tenantE2EPlanName      = "starter"
	// tenantE2EAdminKey is the automation credential the identity middleware
	// accepts in this walk. It resolves to the same account as the console
	// sign-in, which is what makes the two kinds of administrator comparable:
	// same role, same account, different kind of credential.
	tenantE2EAdminKey = "pk_tenant_e2e_admin"
)

type tenantE2EEnv struct {
	t      *testing.T
	server *Server
	cp     *service.ControlPlaneService
	keys   *keys.Store
	// identity is the same value the server mounted, kept so a test can ask
	// what /ui/session.json answers without going through a browser.
	identity *Identity
	// adminID is the account the automation key resolves to.
	adminID string
}

// newTenantE2EEnv builds the leanest deployment that still answers every
// endpoint this walk touches: state engine, tenant service with its key store
// and usage counters, real session service, and the console routes.
func newTenantE2EEnv(t *testing.T, mode config.RegistrationMode) *tenantE2EEnv {
	t.Helper()

	root := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(filepath.Join(root, "state"), filepath.Join(root, "cache"))
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	raw, err := auth.GeneratePepper()
	if err != nil {
		t.Fatalf("GeneratePepper: %v", err)
	}
	pepper, err := auth.NewPepper(raw)
	if err != nil {
		t.Fatalf("NewPepper: %v", err)
	}

	runtimeCfg := &atomic.Pointer[config.RuntimeConfig]{}
	runtimeCfg.Store(config.NewDefaultRuntimeConfig())

	keyStore := keys.NewStore()
	counters := usage.NewCounters()
	cp := &service.ControlPlaneService{
		Engine:     engine,
		RuntimeCfg: runtimeCfg,
		EnvCfg: &config.EnvConfig{
			DefaultPlatformStickyTTL:              30 * time.Minute,
			DefaultPlatformRegexFilters:           []string{},
			DefaultPlatformRegionFilters:          []string{},
			DefaultPlatformReverseProxyMissAction: "TREAT_AS_EMPTY",
			DefaultPlatformAllocationPolicy:       "BALANCED",
		},
		Pepper:  pepper,
		Keys:    keyStore,
		Usage:   counters,
		Conns:   usage.NewConnRegistry(),
		Limiter: usage.NewLimiter(counters),
	}
	admin, err := cp.CreateAccount(tenantE2EAdminUser, tenantE2EAdminPassword, model.UserRoleAdmin, time.Now().UnixNano())
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	sessions := &auth.Service{Store: engine, Pepper: pepper, Logf: func(string, ...any) {}}
	// The automation credential resolves to the same account as the console
	// sign-in, so a test can hold role and account constant and vary only the
	// kind of credential the request arrived with.
	identity := &Identity{
		Sessions: sessions,
		AdminKeys: fakeAdminKeys{
			credential: tenantE2EAdminKey,
			identity:   AdminKeyIdentity{KeyID: "k-tenant-e2e", UserID: admin.ID, Role: model.UserRoleAdmin},
		},
		Logf: func(string, ...any) {},
	}
	authDeps := &AuthDeps{
		Issuer:    "Prism",
		Blocklist: NewLoginBlocklist(nil),
		Identity:  identity,
		Handlers: &AuthHandlers{
			Sessions:         sessions,
			Accounts:         cp,
			Registration:     cp,
			RegistrationMode: func() config.RegistrationMode { return mode },
			ClientKey:        func(*http.Request) string { return "tenant-e2e" },
			Logf:             func(string, ...any) {},
		},
	}

	srv := NewServer(0, service.SystemInfo{
		Version:   "1.0.0-test",
		GitCommit: "tenant-e2e",
		BuildTime: "2026-03-01T00:00:00Z",
		StartedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}, runtimeCfg, cp.EnvCfg, cp, 1<<20, nil, nil, nil, authDeps)

	return &tenantE2EEnv{t: t, server: srv, cp: cp, keys: keyStore, identity: identity, adminID: admin.ID}
}

// doAsKey performs a request with the automation credential instead of a console
// session: no cookie, no CSRF token, just the bearer header a script sends.
func (e *tenantE2EEnv) doAsKey(method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Host = "127.0.0.1:8080"
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	req.Header.Set("Authorization", "Bearer "+tenantE2EAdminKey)

	rec := httptest.NewRecorder()
	e.server.Handler().ServeHTTP(rec, req)
	return rec
}

func (e *tenantE2EEnv) session() *tenantE2ESession {
	return &tenantE2ESession{t: e.t, env: e}
}

// tenantE2ESession is a browser: it carries the session cookie and echoes the
// CSRF token on every write, exactly as the console does.
type tenantE2ESession struct {
	t       *testing.T
	env     *tenantE2EEnv
	session string
	csrf    string
}

func (s *tenantE2ESession) do(method, path string, body any) *httptest.ResponseRecorder {
	s.t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			s.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	req := httptest.NewRequest(method, path, reader)
	// Plain HTTP on loopback is what the console's cookie policy allows, and
	// httptest hands out "example.com" by default, which it refuses.
	req.Host = "127.0.0.1:8080"
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if s.session != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: s.session})
	}
	if s.csrf != "" {
		// The double-submit pair: the browser holds both, so the jar must send
		// both - the cookie is the half the server compares against.
		req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: s.csrf})
		req.Header.Set(auth.CSRFHeaderName, s.csrf)
	}

	rec := httptest.NewRecorder()
	s.env.server.Handler().ServeHTTP(rec, req)
	// A browser stores whatever a response asks it to, so every answer can
	// change what the jar carries: sign-in and sign-up both hand out the pair.
	s.captureCookies(rec)
	return rec
}
func (s *tenantE2ESession) login(username, password string) *httptest.ResponseRecorder {
	s.t.Helper()
	rec := s.do(http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": username,
		"password": password,
	})
	if rec.Code == http.StatusOK {
		s.captureCookies(rec)
	}
	return rec
}

func (s *tenantE2ESession) mustLogin(username, password string) {
	s.t.Helper()
	rec := s.login(username, password)
	tenantAssertStatus(s.t, rec, http.StatusOK)
	if s.session == "" || s.csrf == "" {
		s.t.Fatalf("login %s: session and CSRF cookies must both be set (session=%q csrf=%q)", username, s.session, s.csrf)
	}
}

func (s *tenantE2ESession) captureCookies(rec *httptest.ResponseRecorder) {
	for _, cookie := range rec.Result().Cookies() {
		switch cookie.Name {
		case auth.SessionCookieName:
			s.session = cookie.Value
		case auth.CSRFCookieName:
			s.csrf = cookie.Value
		}
	}
}

func tenantAssertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, want, rec.Body.String())
	}
}

func tenantField(t *testing.T, body map[string]any, key string) string {
	t.Helper()
	value, _ := body[key].(string)
	if value == "" {
		t.Fatalf("field %q missing or empty: body=%v", key, body)
	}
	return value
}

func tenantNested(t *testing.T, body map[string]any, key string) map[string]any {
	t.Helper()
	nested, ok := body[key].(map[string]any)
	if !ok {
		t.Fatalf("field %q is not an object: body=%v", key, body)
	}
	return nested
}

func tenantItems(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal items: %v body=%q", err, rec.Body.String())
	}
	return payload.Items
}

// tenantCreatePlan walks the plan endpoint the console uses.
func tenantCreatePlan(t *testing.T, admin *tenantE2ESession, maxKeys int) string {
	t.Helper()
	rec := admin.do(http.MethodPost, "/api/v1/admin/plans", map[string]any{
		"name":              tenantE2EPlanName,
		"platforms":         []string{},
		"ip_tiers":          []string{},
		"traffic_bytes":     int64(10) << 30,
		"period":            "30d",
		"max_concurrent":    5,
		"max_rps":           10,
		"max_bandwidth_bps": 0,
		"max_keys":          maxKeys,
		"duration_days":     30,
		"status":            "active",
	})
	tenantAssertStatus(t, rec, http.StatusCreated)
	return tenantField(t, decodeJSONMap(t, rec), "id")
}

// TestMultiTenantAdmin_E2E_TenantLifecycle is the phase's acceptance walk:
// create a plan, create a tenant on it, issue a key, read usage, prove a tenant
// cannot reach the management API, then disable and delete the tenant and watch
// the session and the credentials die with it.
func TestMultiTenantAdmin_E2E_TenantLifecycle(t *testing.T) {
	env := newTenantE2EEnv(t, config.RegistrationOff)

	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)

	// A fresh deployment has no plans, and the console reads the "items"
	// envelope rather than a bare array.
	rec := admin.do(http.MethodGet, "/api/v1/admin/plans", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	if items := tenantItems(t, rec); len(items) != 0 {
		t.Fatalf("plans on a fresh deployment: got %d, want 0", len(items))
	}

	planID := tenantCreatePlan(t, admin, 2)

	rec = admin.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username":      tenantE2ETenantUser,
		"password":      tenantE2ETenantPass,
		"role":          "user",
		"plan_id":       planID,
		"duration_days": 30,
	})
	tenantAssertStatus(t, rec, http.StatusCreated)
	created := decodeJSONMap(t, rec)
	userID := tenantField(t, created, "id")
	if got := tenantField(t, tenantNested(t, created, "subscription"), "plan_id"); got != planID {
		t.Fatalf("created user subscription plan: got %q, want %q", got, planID)
	}

	rec = admin.do(http.MethodPost, "/api/v1/admin/users/"+userID+"/keys", map[string]any{"name": "laptop"})
	tenantAssertStatus(t, rec, http.StatusCreated)
	issued := decodeJSONMap(t, rec)
	credential := tenantField(t, issued, "credential")
	if !strings.HasPrefix(credential, keys.CredentialPrefix) {
		t.Fatalf("credential prefix: got %q, want the %q prefix", credential, keys.CredentialPrefix)
	}
	firstKeyID := tenantField(t, tenantNested(t, issued, "key"), "id")
	if live, ok := env.keys.Get(firstKeyID); !ok || !live.Usable(time.Now().UnixNano()) {
		t.Fatalf("the issued key must be usable by the data plane (state=%v present=%v)", live, ok)
	}

	rec = admin.do(http.MethodGet, "/api/v1/admin/usage/summary", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	summary := decodeJSONMap(t, rec)
	if total, _ := summary["total_users"].(float64); total < 2 {
		t.Fatalf("usage summary total_users: got %v, want at least the administrator and the tenant", summary["total_users"])
	}
	if active, _ := summary["active_users"].(float64); active < 1 {
		t.Fatalf("usage summary active_users: got %v, want the newly subscribed tenant", summary["active_users"])
	}

	// The per-plan view: the plan this tenant rides on must appear as a bucket
	// with one subscriber, which is the number the console groups by.
	rec = admin.do(http.MethodGet, "/api/v1/admin/usage/plans?days=30", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	planRows := tenantItems(t, rec)
	if len(planRows) == 0 {
		t.Fatal("usage by plan: no rows for a deployment with one subscribed tenant")
	}
	var planSeen bool
	for _, row := range planRows {
		if row["plan_id"] != planID {
			continue
		}
		planSeen = true
		if subs, _ := row["subscribers"].(float64); subs < 1 {
			t.Fatalf("usage by plan subscribers for %s: got %v, want at least 1", planID, row["subscribers"])
		}
	}
	if !planSeen {
		t.Fatalf("usage by plan: plan %s missing from %v", planID, planRows)
	}

	// The per-account usage window the user detail page charts.
	rec = admin.do(http.MethodGet, "/api/v1/admin/users/"+userID+"/usage?days=30", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	if items := tenantItems(t, rec); len(items) == 0 {
		t.Fatal("user usage window: an account on a plan must still answer with its daily series")
	}

	// Usage alerts, end to end: the detector reads the same usage tables the
	// views do, so a bucket that crosses a third of the plan quota has to
	// produce an alert the admin API can list and acknowledge - and that a
	// tenant session cannot reach at all.
	hourTS := (time.Now().UnixNano() / int64(time.Hour)) * int64(time.Hour)
	if err := env.cp.Engine.UpsertUsageHourly([]model.UsageRow{{
		KeyID:       firstKeyID,
		UserID:      userID,
		PlatformID:  "",
		TSNs:        hourTS,
		EgressBytes: int64(4) << 30,
		Requests:    1,
	}}); err != nil {
		t.Fatalf("UpsertUsageHourly: %v", err)
	}

	stopAlerts := service.StartUsageAlertScanner(env.cp, 20*time.Millisecond, func(string, ...any) {})
	defer stopAlerts()

	var alertID string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && alertID == ""; {
		rec = admin.do(http.MethodGet, "/api/v1/admin/alerts?unacknowledged=true", nil)
		tenantAssertStatus(t, rec, http.StatusOK)
		alerts := tenantItems(t, rec)
		if len(alerts) == 0 {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		first := alerts[0]
		alertID = tenantField(t, first, "id")
		if got := tenantField(t, first, "kind"); got != model.AlertKindUsageAnomaly {
			t.Fatalf("alert kind: got %q, want %q", got, model.AlertKindUsageAnomaly)
		}
		if got := tenantField(t, first, "user_id"); got != userID {
			t.Fatalf("alert user_id: got %q, want %q", got, userID)
		}
		if got := tenantField(t, first, "plan_id"); got != planID {
			t.Fatalf("alert plan_id: got %q, want %q", got, planID)
		}
		if used, _ := first["used_bytes"].(float64); used < float64(int64(4)<<30) {
			t.Fatalf("alert used_bytes: got %v, want at least the 4 GiB the scan summed", first["used_bytes"])
		}
	}
	if alertID == "" {
		t.Fatal("usage alerts: a bucket over a third of the plan quota produced no alert within 5s")
	}

	rec = admin.do(http.MethodPost, "/api/v1/admin/alerts/"+alertID+"/actions/acknowledge", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	acked := decodeJSONMap(t, rec)
	if at, _ := acked["acknowledged_at_ns"].(float64); at <= 0 {
		t.Fatalf("acknowledged_at_ns: got %v, want a stamp", acked["acknowledged_at_ns"])
	}
	// Acknowledging twice is not an error to the operator who tapped twice, but
	// the second call names a row that is no longer open.
	rec = admin.do(http.MethodPost, "/api/v1/admin/alerts/"+alertID+"/actions/acknowledge", nil)
	tenantAssertStatus(t, rec, http.StatusNotFound)

	// A write without the double-submit token is refused even with a valid
	// session: the cookie alone is not proof the console sent the request.
	csrf := admin.csrf
	admin.csrf = ""
	rec = admin.do(http.MethodPost, "/api/v1/admin/plans", map[string]any{"name": "no-csrf"})
	tenantAssertStatus(t, rec, http.StatusForbidden)
	assertErrorCode(t, rec, "CSRF_FAILED")
	admin.csrf = csrf

	tenant := env.session()
	tenant.mustLogin(tenantE2ETenantUser, tenantE2ETenantPass)

	rec = tenant.do(http.MethodGet, "/api/v1/me/overview", nil)
	tenantAssertStatus(t, rec, http.StatusOK)

	rec = tenant.do(http.MethodGet, "/api/v1/me/keys", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	if items := tenantItems(t, rec); len(items) != 1 {
		t.Fatalf("tenant keys: got %d, want the one the operator issued", len(items))
	}

	// The role gate: a tenant session reaches its own surface and nothing
	// else, including the read-only management and instance endpoints the
	// The role gate: a tenant session reaches its own surface and nothing
	// else, including the read-only management and instance endpoints the
	// console's operator shell uses. The refusal is not-found rather than
	// forbidden, so a tenant cannot tell a management route from one that was
	// never registered.
	for _, path := range []string{"/api/v1/admin/plans", "/api/v1/admin/users", "/api/v1/admin/usage/summary", "/api/v1/admin/alerts", "/api/v1/system/info"} {
		rec = tenant.do(http.MethodGet, path, nil)
		tenantAssertStatus(t, rec, http.StatusNotFound)
		assertErrorCode(t, rec, "NOT_FOUND")
	}
	rec = tenant.do(http.MethodDelete, "/api/v1/admin/plans/"+planID, nil)
	tenantAssertStatus(t, rec, http.StatusNotFound)
	assertErrorCode(t, rec, "NOT_FOUND")

	// The tenant manages their own credentials, and the plan's key allowance
	// is what stops them.
	rec = tenant.do(http.MethodPost, "/api/v1/me/keys", map[string]any{"name": "phone"})
	tenantAssertStatus(t, rec, http.StatusCreated)
	secondKeyID := tenantField(t, tenantNested(t, decodeJSONMap(t, rec), "key"), "id")

	rec = tenant.do(http.MethodPost, "/api/v1/me/keys", map[string]any{"name": "tablet"})
	tenantAssertStatus(t, rec, http.StatusConflict)
	assertErrorCode(t, rec, "CONFLICT")

	// Disabling the account ends its sessions and takes its credentials out of
	// the data plane in the same breath.
	rec = admin.do(http.MethodPatch, "/api/v1/admin/users/"+userID, map[string]any{"status": "disabled"})
	tenantAssertStatus(t, rec, http.StatusOK)
	if got := tenantField(t, decodeJSONMap(t, rec), "status"); got != model.UserStatusDisabled {
		t.Fatalf("user status after disable: got %q, want %q", got, model.UserStatusDisabled)
	}

	rec = tenant.do(http.MethodGet, "/api/v1/me/overview", nil)
	tenantAssertStatus(t, rec, http.StatusUnauthorized)
	assertErrorCode(t, rec, "UNAUTHORIZED")

	for _, keyID := range []string{firstKeyID, secondKeyID} {
		live, ok := env.keys.Get(keyID)
		if ok && live.Usable(time.Now().UnixNano()) {
			t.Fatalf("key %s is still usable after its owner was disabled", keyID)
		}
	}

	// A disabled account cannot sign back in either.
	blocked := env.session()
	rec = blocked.login(tenantE2ETenantUser, tenantE2ETenantPass)
	if rec.Code == http.StatusOK {
		t.Fatal("a disabled account must not be able to log in")
	}

	// Deleting the account removes it, and its credentials leave the store.
	rec = admin.do(http.MethodDelete, "/api/v1/admin/users/"+userID, nil)
	tenantAssertStatus(t, rec, http.StatusNoContent)

	rec = admin.do(http.MethodGet, "/api/v1/admin/users/"+userID, nil)
	tenantAssertStatus(t, rec, http.StatusNotFound)
	assertErrorCode(t, rec, "NOT_FOUND")

	for _, keyID := range []string{firstKeyID, secondKeyID} {
		if _, ok := env.keys.Get(keyID); ok {
			t.Fatalf("key %s survived the deletion of its owner", keyID)
		}
	}

	// The plan outlives the tenant, and the usage views keep answering.
	rec = admin.do(http.MethodGet, "/api/v1/admin/plans/"+planID, nil)
	tenantAssertStatus(t, rec, http.StatusOK)

	rec = admin.do(http.MethodGet, "/api/v1/admin/usage/users?limit=10", nil)
	tenantAssertStatus(t, rec, http.StatusOK)

	rec = admin.do(http.MethodGet, "/api/v1/admin/usage/daily?days=7", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
}

// TestMultiTenantRegister_E2E_InviteCode walks the invite door end to end: the
// operator mints a code, sign-up refuses a code-less attempt, the code admits
// exactly one account and grants the plan the code carried, and a spent code is
// refused with the same answer as an unknown one.
func TestMultiTenantRegister_E2E_InviteCode(t *testing.T) {
	env := newTenantE2EEnv(t, config.RegistrationInvite)

	admin := env.session()
	admin.mustLogin(tenantE2EAdminUser, tenantE2EAdminPassword)
	planID := tenantCreatePlan(t, admin, 1)

	rec := admin.do(http.MethodPost, "/api/v1/admin/invites", map[string]any{
		"plan_id":         planID,
		"max_uses":        1,
		"expires_in_days": 7,
		"note":            "e2e",
	})
	tenantAssertStatus(t, rec, http.StatusCreated)
	code := tenantField(t, decodeJSONMap(t, rec), "code")

	// Invite mode means a code is required: without one there is nothing to
	// redeem, and the refusal is an argument error rather than a closed door.
	anon := env.session()
	rec = anon.do(http.MethodPost, "/api/v1/auth/register", map[string]any{
		"username": "invited-one",
		"password": "invited-one-password",
	})
	tenantAssertStatus(t, rec, http.StatusBadRequest)
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	// Sign-up is public: no cookie, no CSRF pair, and the account is created
	// and signed in in one step.
	rec = anon.do(http.MethodPost, "/api/v1/auth/register", map[string]any{
		"invite_code": code,
		"username":    "invited-one",
		"password":    "invited-one-password",
	})
	tenantAssertStatus(t, rec, http.StatusCreated)
	if anon.session == "" || anon.csrf == "" {
		t.Fatalf("registration must leave the new account signed in: body=%s", rec.Body.String())
	}

	rec = anon.do(http.MethodGet, "/api/v1/me/subscription", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	subscription := tenantNested(t, decodeJSONMap(t, rec), "subscription")
	if got := tenantField(t, subscription, "plan_id"); got != planID {
		t.Fatalf("invited account subscription plan: got %q, want %q", got, planID)
	}

	// A used-up code is refused exactly like an unknown one: an exhausted code
	// must not be distinguishable from a wrong one.
	spent := env.session()
	rec = spent.do(http.MethodPost, "/api/v1/auth/register", map[string]any{
		"invite_code": code,
		"username":    "invited-two",
		"password":    "invited-two-password",
	})
	tenantAssertStatus(t, rec, http.StatusBadRequest)
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	rec = spent.do(http.MethodPost, "/api/v1/auth/register", map[string]any{
		"invite_code": "ZZZZ-ZZZZ",
		"username":    "invited-two",
		"password":    "invited-two-password",
	})
	tenantAssertStatus(t, rec, http.StatusBadRequest)
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	// The operator sees the code as spent, not as available.
	rec = admin.do(http.MethodGet, "/api/v1/admin/invites", nil)
	tenantAssertStatus(t, rec, http.StatusOK)
	invites := tenantItems(t, rec)
	if len(invites) != 1 {
		t.Fatalf("invites: got %d, want the one the operator created", len(invites))
	}
	if used, _ := invites[0]["used_count"].(float64); used != 1 {
		t.Fatalf("invite used_count: got %v, want 1", invites[0]["used_count"])
	}
}

// TestMultiTenantRegister_E2E_ClosedDeployment proves the sign-up door is shut
// when the deployment never opened it, and that the refusal is explicit rather
// than a missing route.
func TestMultiTenantRegister_E2E_ClosedDeployment(t *testing.T) {
	env := newTenantE2EEnv(t, config.RegistrationOff)

	anon := env.session()
	rec := anon.do(http.MethodPost, "/api/v1/auth/register", map[string]any{
		"username": "invited-one",
		"password": "invited-one-password",
	})
	tenantAssertStatus(t, rec, http.StatusForbidden)
	assertErrorCode(t, rec, "REGISTRATION_CLOSED")

	if anon.session != "" {
		t.Fatal("a refused registration must not hand out a session")
	}
}
