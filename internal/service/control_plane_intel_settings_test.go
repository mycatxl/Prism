package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prism/internal/intel"
	"prism/internal/intel/checks"
	"prism/internal/intel/jobs"
	"prism/internal/intel/providers"
	"prism/internal/intel/store"
	"prism/internal/model"
	"prism/internal/state"
)

// WP09 §4/§5.4 settings surface of internal/service. internal/api covers the
// HTTP status mapping of handler_intel_settings_test.go; these tests cover the
// branches that only exist below the handler: the sentinel→ServiceError
// mapping, the budget synthesis (remaining = -1), the "no store"/"no engine"
// conflicts and the R6 promise that a stored credential is never returned.

// testIntelSettingsKey is the credential of these tests. No value returned by
// the service may ever contain it.
const testIntelSettingsKey = "sk-test-fake-provider-credential"

func newStateEngineForTest(t *testing.T) (*state.StateEngine, func()) {
	t.Helper()
	dir := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(
		filepath.Join(dir, "state"),
		filepath.Join(dir, "cache"),
	)
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	return engine, func() { _ = closer.Close() }
}

// newIntelServiceWithSurfaces opens a throw-away intel.db and wires the given
// settings/checks surfaces into it. It mirrors the api-side fixture but stays
// inside the service package, so no network and no shared state.
func newIntelServiceWithSurfaces(t *testing.T, settings *providers.SettingsService, checkEngine *checks.Engine) *intel.Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state", store.FileName))
	if err != nil {
		t.Fatalf("open intel store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc, err := intel.NewService(intel.Options{
		Store: st,
		Scope: jobs.ScopeResolverFunc(func(context.Context, jobs.Scope) ([]string, error) {
			return nil, nil
		}),
		ProviderSettings: settings,
		CheckEngine:      checkEngine,
		Logf:             func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("intel.NewService: %v", err)
	}
	t.Cleanup(svc.Stop)
	return svc
}

// newIntelSettingsForTest builds the provider settings surface on top of the
// given state engine (the settings store).
func newIntelSettingsForTest(t *testing.T, engine *state.StateEngine) (*intel.Service, *providers.Registry, *providers.SettingsService) {
	t.Helper()
	registry := providers.NewRegistry()
	providers.RegisterBuiltins(registry, providers.BuiltinConfig{Now: time.Now})
	settings := providers.NewSettingsService(registry, engine, nil, time.Now)
	return newIntelServiceWithSurfaces(t, settings, nil), registry, settings
}

func newIntelChecksForTest(t *testing.T) (*intel.Service, *checks.Engine) {
	t.Helper()
	checkEngine := checks.NewEngine(checks.Options{Logf: func(string, ...any) {}})
	return newIntelServiceWithSurfaces(t, nil, checkEngine), checkEngine
}

func findProviderStatus(statuses []providers.ProviderStatus, id string) (providers.ProviderStatus, bool) {
	for _, status := range statuses {
		if status.ID == id {
			return status, true
		}
	}
	return providers.ProviderStatus{}, false
}

func TestMapIntelSettingsError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		want    string
		message string
	}{
		{name: "nil stays nil", err: nil, want: ""},
		{name: "unknown provider", err: providers.ErrUnknownProvider, want: "NOT_FOUND", message: "provider not found"},
		{name: "wrapped unknown provider", err: fmt.Errorf("%w: proxycheck", providers.ErrUnknownProvider), want: "NOT_FOUND", message: "provider not found"},
		{name: "invalid setting", err: fmt.Errorf("%w: qps: must be <= 100", providers.ErrInvalidSetting), want: "INVALID_ARGUMENT", message: "invalid provider setting: qps: must be <= 100"},
		{name: "settings unavailable", err: providers.ErrSettingsUnavailable, want: "CONFLICT", message: "intel provider settings are not available"},
		{name: "no downloadable database", err: fmt.Errorf("%w: proxycheck", providers.ErrGeoUnknownProvider), want: "CONFLICT", message: "this data source has no downloadable database"},
		{name: "no database refresher", err: fmt.Errorf("%w: maxmind_geolite2", providers.ErrGeoUnavailable), want: "CONFLICT", message: "offline database downloads are not available"},
		{name: "anything else is internal", err: errors.New("boom"), want: "INTERNAL", message: "intel provider settings"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapped := mapIntelSettingsError(tt.err)
			if tt.want == "" {
				if mapped != nil {
					t.Fatalf("mapIntelSettingsError(nil) = %v, want nil", mapped)
				}
				return
			}
			if !isServiceErrorCode(mapped, tt.want) {
				t.Fatalf("mapIntelSettingsError(%v) = %v, want code %s", tt.err, mapped, tt.want)
			}
			var svcErr *ServiceError
			if !errors.As(mapped, &svcErr) {
				t.Fatalf("mapIntelSettingsError(%v) = %T, want *ServiceError", tt.err, mapped)
			}
			if svcErr.Message != tt.message {
				t.Errorf("message = %q, want %q", svcErr.Message, tt.message)
			}
			// A validation error must never echo a credential.
			if strings.Contains(svcErr.Message, testIntelSettingsKey) {
				t.Fatal("the mapped error repeated the credential")
			}
		})
	}
}

func TestRuleErrorPath(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "loader shape", err: errors.New("rule /etc/prism/checks.d/broken.yaml: version is required"), want: "/etc/prism/checks.d/broken.yaml"},
		{name: "loader shape with a colon in the reason", err: errors.New("rule /tmp/x.yaml: id \"a\": must equal the file name"), want: "/tmp/x.yaml"},
		{name: "no separator", err: errors.New("rule /tmp/x.yaml"), want: ""},
		{name: "no rule prefix", err: errors.New("read directory: no such file"), want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ruleErrorPath(tt.err); got != tt.want {
				t.Fatalf("ruleErrorPath(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestIntelProviderUsage(t *testing.T) {
	t.Run("nil store has no usage at all", func(t *testing.T) {
		usage, err := intelProviderUsage(context.Background(), nil)
		if err != nil || usage != nil {
			t.Fatalf("intelProviderUsage(nil) = (%v, %v), want (nil, nil)", usage, err)
		}
	})

	t.Run("recorded state is projected and unknown providers get today", func(t *testing.T) {
		st, err := store.Open(filepath.Join(t.TempDir(), "state", store.FileName))
		if err != nil {
			t.Fatalf("open intel store: %v", err)
		}
		defer func() { _ = st.Close() }()

		now := time.Now().UTC()
		if err := st.UpsertProviderState(context.Background(), store.ProviderState{
			Provider: "proxycheck", Day: store.DayString(now), Used: 11,
			NextRequestAtNs: now.Add(time.Minute).UnixNano(), Paused: true, ErrorCode: "PROVIDER_AUTH",
		}); err != nil {
			t.Fatalf("UpsertProviderState: %v", err)
		}

		usage, err := intelProviderUsage(context.Background(), st)
		if err != nil {
			t.Fatalf("intelProviderUsage: %v", err)
		}
		recorded := usage("proxycheck")
		if recorded.Day != store.DayString(now) || recorded.Used != 11 {
			t.Fatalf("recorded usage = %+v, want day=%s used=11", recorded, store.DayString(now))
		}
		if !recorded.Paused || recorded.ErrorCode != "PROVIDER_AUTH" {
			t.Fatalf("paused state = %+v", recorded)
		}
		if recorded.NextRequestAtNs != now.Add(time.Minute).UnixNano() {
			t.Fatalf("next_request_at_ns = %d", recorded.NextRequestAtNs)
		}

		// A provider without a state row reports the current day and no usage.
		unknown := usage("dnsbl")
		if unknown.Day == "" || unknown.Used != 0 || unknown.Paused {
			t.Fatalf("synthesised usage = %+v, want empty usage for today", unknown)
		}
	})

	t.Run("a broken store is reported", func(t *testing.T) {
		st, err := store.Open(filepath.Join(t.TempDir(), "state", store.FileName))
		if err != nil {
			t.Fatalf("open intel store: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("close intel store: %v", err)
		}
		if _, err := intelProviderUsage(context.Background(), st); err == nil {
			t.Fatal("intelProviderUsage must fail on a closed store")
		}
	})
}

func TestIntelProviderSettingsAvailability(t *testing.T) {
	// No intel subsystem at all.
	empty := &ControlPlaneService{}
	if _, _, err := empty.intelProviderSettings(); !isServiceErrorCode(err, "CONFLICT") {
		t.Fatalf("intelProviderSettings without intel = %v, want CONFLICT", err)
	}
	if _, err := empty.IntelProviderList(context.Background()); !isServiceErrorCode(err, "CONFLICT") {
		t.Fatalf("IntelProviderList without intel = %v, want CONFLICT", err)
	}
	if _, err := empty.IntelRefreshProvider(context.Background(), "proxycheck"); !isServiceErrorCode(err, "CONFLICT") {
		t.Fatalf("IntelRefreshProvider without intel = %v, want CONFLICT", err)
	}

	// Wired intel without the settings surface: the projection-only service.
	svc := newIntelProjectionForTest(t)
	cp := &ControlPlaneService{Intel: svc}
	if _, _, err := cp.intelProviderSettings(); !isServiceErrorCode(err, "CONFLICT") {
		t.Fatalf("intelProviderSettings without the settings surface = %v, want CONFLICT", err)
	}
	if _, err := cp.IntelResumeProvider(context.Background(), "proxycheck"); !isServiceErrorCode(err, "CONFLICT") {
		t.Fatalf("IntelResumeProvider without the settings surface = %v, want CONFLICT", err)
	}
	if _, err := cp.IntelUpdateProvider(context.Background(), "proxycheck", providers.ProviderPatch{}); !isServiceErrorCode(err, "CONFLICT") {
		t.Fatalf("IntelUpdateProvider without the settings surface = %v, want CONFLICT", err)
	}

	// Wired intel without the checks engine.
	if _, err := cp.IntelChecks(); !isServiceErrorCode(err, "CONFLICT") {
		t.Fatalf("IntelChecks without the checks engine = %v, want CONFLICT", err)
	}
	if _, err := cp.IntelUpdateCheck("chatgpt", IntelUpdateCheckRequest{Enabled: intelTestBoolPtr(true)}); !isServiceErrorCode(err, "CONFLICT") {
		t.Fatalf("IntelUpdateCheck without the checks engine = %v, want CONFLICT", err)
	}
}

// A storage failure on either side of the read path is an INTERNAL error
// instead of an empty provider list.
func TestIntelProviderListStoreFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("intel state store", func(t *testing.T) {
		engine, closeEngine := newStateEngineForTest(t)
		defer closeEngine()
		svc, _, _ := newIntelSettingsForTest(t, engine)
		cp := &ControlPlaneService{Engine: engine, Intel: svc}

		if err := svc.Store().Close(); err != nil {
			t.Fatalf("close intel store: %v", err)
		}
		if _, err := cp.IntelProviderList(ctx); !isServiceErrorCode(err, "INTERNAL") {
			t.Fatalf("IntelProviderList with a closed intel store = %v, want INTERNAL", err)
		}
		if _, err := cp.IntelUpdateProvider(ctx, "proxycheck", providers.ProviderPatch{}); !isServiceErrorCode(err, "INTERNAL") {
			t.Fatalf("IntelUpdateProvider with a closed intel store = %v, want INTERNAL", err)
		}
		if _, err := cp.IntelResumeProvider(ctx, "proxycheck"); !isServiceErrorCode(err, "INTERNAL") {
			t.Fatalf("IntelResumeProvider with a closed intel store = %v, want INTERNAL", err)
		}
	})

	t.Run("provider settings store", func(t *testing.T) {
		engine, closeEngine := newStateEngineForTest(t)
		svc, _, _ := newIntelSettingsForTest(t, engine)
		cp := &ControlPlaneService{Engine: engine, Intel: svc}

		closeEngine()
		if _, err := cp.IntelProviderList(ctx); !isServiceErrorCode(err, "INTERNAL") {
			t.Fatalf("IntelProviderList with unreadable settings = %v, want INTERNAL", err)
		}
		if _, err := cp.IntelUpdateProvider(ctx, "proxycheck", providers.ProviderPatch{}); !isServiceErrorCode(err, "INTERNAL") {
			t.Fatalf("IntelUpdateProvider with unreadable settings = %v, want INTERNAL", err)
		}
	})
}

func TestIntelProviderListExposesBudgetAndNeverTheKey(t *testing.T) {
	engine, closeEngine := newStateEngineForTest(t)
	defer closeEngine()

	svc, registry, settings := newIntelSettingsForTest(t, engine)
	cp := &ControlPlaneService{Engine: engine, Intel: svc}
	ctx := context.Background()

	if _, err := settings.Update("proxycheck", providers.ProviderPatch{
		Enabled:    intelTestBoolPtr(true),
		APIKey:     intelTestStrPtr(testIntelSettingsKey),
		DailyLimit: intelTestIntPtr(50),
	}, nil); err != nil {
		t.Fatalf("settings.Update: %v", err)
	}
	if live, ok := registry.Setting("proxycheck"); !ok || live.APIKey != testIntelSettingsKey {
		t.Fatal("the fake credential did not reach the running registry")
	}

	// A provider whose credential lives in config_json (the MaxMind profile)
	// must redact the declared credential fields too.
	const fakeAccountID = "test-account-id-0000"
	const fakeLicenseKey = "test-license-key-0000"
	if err := engine.UpsertIntelProviderSetting(model.IntelProviderSetting{
		ProviderID: "maxmind_geolite2",
		Enabled:    true,
		ConfigJSON: `{"account_id":"` + fakeAccountID + `","license_key":"` + fakeLicenseKey + `"}`,
	}); err != nil {
		t.Fatalf("UpsertIntelProviderSetting(maxmind_geolite2): %v", err)
	}
	// The running registry is what the status view prefers, so the direct row
	// needs the same reload the settings surface performs on every write.
	if err := settings.Reload(); err != nil {
		t.Fatalf("settings.Reload: %v", err)
	}
	now := time.Now().UTC()
	blockedUntil := now.Add(30 * time.Minute).UnixNano()
	if err := svc.Store().UpsertProviderState(ctx, store.ProviderState{
		Provider: "proxycheck", Day: store.DayString(now), Used: 7,
		Paused: true, ErrorCode: "PROVIDER_AUTH", BlockedUntilNs: blockedUntil,
	}); err != nil {
		t.Fatalf("UpsertProviderState: %v", err)
	}

	statuses, err := cp.IntelProviderList(ctx)
	if err != nil {
		t.Fatalf("IntelProviderList: %v", err)
	}
	proxycheck, ok := findProviderStatus(statuses, "proxycheck")
	if !ok {
		t.Fatalf("proxycheck missing from %d statuses", len(statuses))
	}
	if !proxycheck.HasKey {
		t.Fatal("has_key must be true after a credential was stored")
	}
	if len(proxycheck.CredentialFields) != 0 {
		t.Fatalf("proxycheck stores its key in api_key, not in config: %+v", proxycheck.CredentialFields)
	}
	if proxycheck.Usage.Used != 7 || proxycheck.Usage.Remaining != 43 || proxycheck.Usage.Exhausted {
		t.Fatalf("budget = %+v, want used=7 remaining=43 exhausted=false", proxycheck.Usage)
	}
	if !proxycheck.Usage.Paused || proxycheck.Usage.ErrorCode != "PROVIDER_AUTH" {
		t.Fatalf("pause visibility = %+v", proxycheck.Usage)
	}
	if proxycheck.Usage.BlockedUntilNs != blockedUntil || proxycheck.Usage.NextAllowedAtNs != blockedUntil {
		t.Fatalf("next_allowed_at_ns = %d, want the 429 cooldown %d", proxycheck.Usage.NextAllowedAtNs, blockedUntil)
	}

	// R6: the credential string must not appear anywhere in the payload.
	encoded, err := json.Marshal(statuses)
	if err != nil {
		t.Fatalf("marshal statuses: %v", err)
	}
	if strings.Contains(string(encoded), testIntelSettingsKey) {
		t.Fatal("IntelProviderList leaked the stored credential")
	}
	if strings.Contains(string(encoded), `"api_key"`) {
		t.Fatal("IntelProviderList must only report has_key, never an api_key field")
	}
	for _, value := range proxycheck.Config {
		if text, ok := value.(string); ok && strings.Contains(text, testIntelSettingsKey) {
			t.Fatal("the provider config leaked the stored credential")
		}
	}

	maxmind, ok := findProviderStatus(statuses, "maxmind_geolite2")
	if !ok {
		t.Fatal("maxmind_geolite2 missing from the provider list")
	}
	if !maxmind.HasKey {
		t.Fatalf("a provider with both credential fields set must report has_key: %+v", maxmind.Config)
	}
	if len(maxmind.CredentialFields) != 2 {
		t.Fatalf("credential field names must be announced: %+v", maxmind.CredentialFields)
	}
	for _, field := range []string{"account_id", "license_key"} {
		if _, present := maxmind.Config[field]; present {
			t.Fatalf("credential field %q is part of the returned config: %+v", field, maxmind.Config)
		}
	}
	if strings.Contains(string(encoded), fakeAccountID) || strings.Contains(string(encoded), fakeLicenseKey) {
		t.Fatal("IntelProviderList leaked a config credential")
	}

	// Exhausted: used == limit clamps remaining to 0.
	if _, err := settings.Update("proxycheck", providers.ProviderPatch{DailyLimit: intelTestIntPtr(7)}, nil); err != nil {
		t.Fatalf("settings.Update(daily_limit=7): %v", err)
	}
	statuses, err = cp.IntelProviderList(ctx)
	if err != nil {
		t.Fatalf("IntelProviderList: %v", err)
	}
	exhausted, _ := findProviderStatus(statuses, "proxycheck")
	if exhausted.Usage.Remaining != 0 || !exhausted.Usage.Exhausted {
		t.Fatalf("exhausted budget = %+v, want remaining=0 exhausted=true", exhausted.Usage)
	}

	// Unlimited (daily_limit = 0) keeps remaining = -1, so "no quota" is
	// distinguishable from "quota exhausted" even though usage is recorded.
	if _, err := settings.Update("proxycheck", providers.ProviderPatch{DailyLimit: intelTestIntPtr(0)}, nil); err != nil {
		t.Fatalf("settings.Update(daily_limit=0): %v", err)
	}
	statuses, err = cp.IntelProviderList(ctx)
	if err != nil {
		t.Fatalf("IntelProviderList: %v", err)
	}
	unlimited, _ := findProviderStatus(statuses, "proxycheck")
	if unlimited.DailyLimit != 0 {
		t.Fatalf("daily_limit = %d, want 0 (unlimited)", unlimited.DailyLimit)
	}
	if unlimited.Usage.Remaining != -1 || unlimited.Usage.Exhausted {
		t.Fatalf("unlimited budget = %+v, want remaining=-1 exhausted=false", unlimited.Usage)
	}
	if unlimited.Usage.Used != 7 {
		t.Fatalf("unlimited budget lost today's usage: %+v", unlimited.Usage)
	}

	// A provider without a state row still reports a budget window for today.
	for _, status := range statuses {
		if status.ID != "dnsbl" {
			continue
		}
		if status.Usage.Used != 0 || status.Usage.Day == "" {
			t.Fatalf("dnsbl usage = %+v, want an empty usage for the current day", status.Usage)
		}
	}
}

func TestIntelUpdateProviderMapsSettingsErrors(t *testing.T) {
	engine, closeEngine := newStateEngineForTest(t)
	defer closeEngine()
	svc, _, _ := newIntelSettingsForTest(t, engine)
	cp := &ControlPlaneService{Engine: engine, Intel: svc}
	ctx := context.Background()

	tests := []struct {
		name  string
		id    string
		patch providers.ProviderPatch
		want  string
	}{
		{name: "unknown provider", id: "nope", patch: providers.ProviderPatch{Enabled: intelTestBoolPtr(true)}, want: "NOT_FOUND"},
		{name: "invalid daily limit", id: "proxycheck", patch: providers.ProviderPatch{DailyLimit: intelTestIntPtr(-1)}, want: "INVALID_ARGUMENT"},
		{name: "limit above the vendor cap", id: "proxycheck", patch: providers.ProviderPatch{DailyLimit: intelTestIntPtr(2_000_000)}, want: "INVALID_ARGUMENT"},
		{name: "invalid qps", id: "proxycheck", patch: providers.ProviderPatch{QPS: floatPtr(-1)}, want: "INVALID_ARGUMENT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cp.IntelUpdateProvider(ctx, tt.id, tt.patch)
			if !isServiceErrorCode(err, tt.want) {
				t.Fatalf("IntelUpdateProvider(%s) = %v, want %s", tt.name, err, tt.want)
			}
			if err != nil && strings.Contains(err.Error(), testIntelSettingsKey) {
				t.Fatal("the validation error repeated a credential")
			}
		})
	}
}

func floatPtr(value float64) *float64 { return &value }

func TestIntelResumeProvider(t *testing.T) {
	engine, closeEngine := newStateEngineForTest(t)
	defer closeEngine()
	svc, _, _ := newIntelSettingsForTest(t, engine)
	cp := &ControlPlaneService{Engine: engine, Intel: svc}
	ctx := context.Background()

	if _, err := cp.IntelResumeProvider(ctx, "nope"); !isServiceErrorCode(err, "NOT_FOUND") {
		t.Fatalf("IntelResumeProvider(unknown) = %v, want NOT_FOUND", err)
	}

	now := time.Now().UTC()
	if err := svc.Store().UpsertProviderState(ctx, store.ProviderState{
		Provider: "proxycheck", Day: store.DayString(now), Used: 3,
		Paused: true, ErrorCode: "PROVIDER_AUTH", BlockedUntilNs: now.Add(time.Hour).UnixNano(),
	}); err != nil {
		t.Fatalf("UpsertProviderState: %v", err)
	}

	status, err := cp.IntelResumeProvider(ctx, " proxycheck ")
	if err != nil {
		t.Fatalf("IntelResumeProvider: %v", err)
	}
	if status.Usage.Paused || status.Usage.BlockedUntilNs != 0 || status.Usage.ErrorCode != "" {
		t.Fatalf("resume did not clear the pause: %+v", status.Usage)
	}
	if status.Usage.Used != 3 {
		t.Fatalf("resume must keep today's usage: %+v", status.Usage)
	}
}

func TestIntelRefreshProviderRequiresADownloader(t *testing.T) {
	engine, closeEngine := newStateEngineForTest(t)
	defer closeEngine()
	svc, _, _ := newIntelSettingsForTest(t, engine)
	cp := &ControlPlaneService{Engine: engine, Intel: svc}
	ctx := context.Background()

	if _, err := cp.IntelRefreshProvider(ctx, "nope"); !isServiceErrorCode(err, "NOT_FOUND") {
		t.Fatalf("IntelRefreshProvider(unknown) = %v, want NOT_FOUND", err)
	}
	// The fixture has no GeoManager attached: a known provider is a CONFLICT,
	// and the request itself is never an error for a provider without a
	// database.
	if _, err := cp.IntelRefreshProvider(ctx, "proxycheck"); !isServiceErrorCode(err, "CONFLICT") {
		t.Fatalf("IntelRefreshProvider(without a downloader) = %v, want CONFLICT", err)
	}
}

func TestIntelCheckOverrides(t *testing.T) {
	var nilService *ControlPlaneService
	if got := nilService.intelCheckOverrides(); len(got) != 0 {
		t.Fatalf("nil receiver overrides = %v, want empty", got)
	}

	if got := (&ControlPlaneService{}).intelCheckOverrides(); len(got) != 0 {
		t.Fatalf("overrides without state storage = %v, want empty", got)
	}

	engine, closeEngine := newStateEngineForTest(t)
	defer closeEngine()
	cp := &ControlPlaneService{Engine: engine}

	for _, row := range []model.IntelProviderSetting{
		{ProviderID: checks.CheckSettingID("chatgpt")},
		// A "check:" row without an id is not an override.
		{ProviderID: checks.CheckSettingID(""), Enabled: true},
		// A provider row is not a check override either.
		{ProviderID: "proxycheck", Enabled: true},
	} {
		if err := engine.UpsertIntelProviderSetting(row); err != nil {
			t.Fatalf("UpsertIntelProviderSetting(%q): %v", row.ProviderID, err)
		}
	}

	overrides := cp.intelCheckOverrides()
	if len(overrides) != 1 {
		t.Fatalf("overrides = %v, want only the chatgpt toggle", overrides)
	}
	row, ok := overrides["chatgpt"]
	if !ok || row.Enabled {
		t.Fatalf("chatgpt override = %+v, want enabled=false", row)
	}

	// A store that cannot be read degrades to "no overrides" instead of
	// failing the whole list request.
	closeEngine()
	if got := cp.intelCheckOverrides(); len(got) != 0 {
		t.Fatalf("overrides on a closed store = %v, want empty", got)
	}
}

func TestIntelChecksAndToggleStatus(t *testing.T) {
	engine, closeEngine := newStateEngineForTest(t)
	defer closeEngine()
	svc, checkEngine := newIntelChecksForTest(t)
	cp := &ControlPlaneService{Engine: engine, Intel: svc}

	checksList, err := cp.IntelChecks()
	if err != nil {
		t.Fatalf("IntelChecks: %v", err)
	}
	if len(checksList.Items) == 0 {
		t.Fatal("the built-in rule set is empty")
	}
	var before IntelCheckStatus
	for _, item := range checksList.Items {
		if item.ID == "chatgpt" {
			before = item
		}
	}
	if before.ID == "" {
		t.Fatal("the chatgpt rule is missing from the built-in set")
	}
	if before.EnabledOverride {
		t.Fatal("a rule without a persisted toggle must not report an override")
	}

	updated, err := cp.IntelUpdateCheck(" chatgpt ", IntelUpdateCheckRequest{Enabled: intelTestBoolPtr(false)})
	if err != nil {
		t.Fatalf("IntelUpdateCheck: %v", err)
	}
	if updated.ID != "chatgpt" || !updated.EnabledOverride {
		t.Fatalf("toggle response = %+v, want id=chatgpt override=true", updated)
	}
	// IntelUpdateCheck reports the effective state the engine applies on the
	// next run. This fixture wires no checks.EnabledSource, so the engine keeps
	// the rule's own default; a production build (cmd/prism) wires the source
	// and reports the persisted value here.
	engineDefault := ruleEnabled(checkEngine, "chatgpt")
	if updated.Enabled != engineDefault {
		t.Fatalf("enabled = %v, want the engine's effective state %v", updated.Enabled, engineDefault)
	}

	// The toggle is persisted as "check:<id>" and the next list reports it, so
	// the settings page and the engine agree.
	rows, err := engine.ListIntelProviderSettings()
	if err != nil {
		t.Fatalf("ListIntelProviderSettings: %v", err)
	}
	persisted := false
	for _, row := range rows {
		if row.ProviderID == checks.CheckSettingID("chatgpt") {
			persisted = true
			if row.Enabled {
				t.Fatalf("persisted toggle = %+v, want enabled=false", row)
			}
		}
	}
	if !persisted {
		t.Fatal(`the toggle was not persisted under "check:chatgpt"`)
	}

	checksList, err = cp.IntelChecks()
	if err != nil {
		t.Fatalf("IntelChecks: %v", err)
	}
	for _, item := range checksList.Items {
		if item.ID != "chatgpt" {
			if item.EnabledOverride {
				t.Errorf("rule %q must not report an override", item.ID)
			}
			continue
		}
		if !item.EnabledOverride {
			t.Errorf("chatgpt after the toggle = %+v, want override=true", item)
		}
	}

	// The engine resolves the toggle through its EnabledSource on every run;
	// this fixture wires none, so the engine keeps the rule default while the
	// service still reports the persisted override.
	if enabled := ruleEnabled(checkEngine, "chatgpt"); !enabled {
		t.Fatal("the fixture is expected to keep the rule default without an EnabledSource")
	}
}

func ruleEnabled(engine *checks.Engine, id string) bool {
	for _, info := range engine.Rules() {
		if info.ID == id {
			return info.Enabled
		}
	}
	return false
}

func TestIntelUpdateCheckWithoutStateStorage(t *testing.T) {
	svc, _ := newIntelChecksForTest(t)
	cp := &ControlPlaneService{Intel: svc}

	_, err := cp.IntelUpdateCheck("chatgpt", IntelUpdateCheckRequest{Enabled: intelTestBoolPtr(true)})
	if !isServiceErrorCode(err, "CONFLICT") {
		t.Fatalf("IntelUpdateCheck without state storage = %v, want CONFLICT", err)
	}

	// The request shape is validated before anything is persisted.
	if _, err := cp.IntelUpdateCheck("chatgpt", IntelUpdateCheckRequest{}); !isServiceErrorCode(err, "INVALID_ARGUMENT") {
		t.Fatalf("IntelUpdateCheck without enabled = %v, want INVALID_ARGUMENT", err)
	}
	if _, err := cp.IntelUpdateCheck("nope", IntelUpdateCheckRequest{Enabled: intelTestBoolPtr(true)}); !isServiceErrorCode(err, "NOT_FOUND") {
		t.Fatalf("IntelUpdateCheck(unknown rule) = %v, want NOT_FOUND", err)
	}
}

func TestIntelUpdateCheckPersistFailureIsInternal(t *testing.T) {
	engine, closeEngine := newStateEngineForTest(t)
	svc, _ := newIntelChecksForTest(t)
	cp := &ControlPlaneService{Engine: engine, Intel: svc}

	closeEngine()

	if _, err := cp.IntelUpdateCheck("chatgpt", IntelUpdateCheckRequest{Enabled: intelTestBoolPtr(false)}); !isServiceErrorCode(err, "INTERNAL") {
		t.Fatalf("IntelUpdateCheck with a broken store = %v, want INTERNAL", err)
	}
}
