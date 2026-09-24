package providers

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/model"
)

// WP09 §4: the provider settings surface. Everything here is local: no test
// touches the network, and no test may ever print a stored credential.

const testRawKey = "sk-live-5f4c0d-do-not-leak-me"

// memorySettingsStore is the in-memory SettingsStore of these tests.
type memorySettingsStore struct {
	mu   sync.Mutex
	rows map[string]model.IntelProviderSetting
}

func newMemorySettingsStore(rows ...model.IntelProviderSetting) *memorySettingsStore {
	store := &memorySettingsStore{rows: map[string]model.IntelProviderSetting{}}
	for _, row := range rows {
		store.rows[row.ProviderID] = row
	}
	return store
}

func (m *memorySettingsStore) ListIntelProviderSettings() ([]model.IntelProviderSetting, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.IntelProviderSetting, 0, len(m.rows))
	for _, row := range m.rows {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProviderID < out[j].ProviderID })
	return out, nil
}

func (m *memorySettingsStore) UpsertIntelProviderSetting(row model.IntelProviderSetting) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[row.ProviderID] = row
	return nil
}

func (m *memorySettingsStore) row(id string) (model.IntelProviderSetting, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	return row, ok
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC) }
}

func newTestSettingsService(t *testing.T, store SettingsStore) (*Registry, *SettingsService) {
	t.Helper()
	registry := NewRegistry()
	RegisterBuiltins(registry, BuiltinConfig{Now: fixedClock()})
	settings := NewSettingsService(registry, store, nil, fixedClock())
	if err := settings.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	return registry, settings
}

func ptr[T any](value T) *T { return &value }

// TestSettingsAPIStatusesNeverExposeTheKey is the R6 guard of the settings
// surface: the raw credential string must be absent from every response body.
func TestSettingsAPIStatusesNeverExposeTheKey(t *testing.T) {
	store := newMemorySettingsStore(
		model.IntelProviderSetting{
			ProviderID: "proxycheck", Enabled: true, APIKey: testRawKey,
			DailyLimit: 900, QPS: 1, ConfigJSON: `{}`, UpdatedAtNs: 7,
		},
		model.IntelProviderSetting{
			ProviderID: "maxmind_geolite2", Enabled: true,
			ConfigJSON: `{"account_id":"12345","license_key":"` + testRawKey + `"}`,
		},
	)
	_, settings := newTestSettingsService(t, store)

	statuses, err := settings.Statuses(nil)
	if err != nil {
		t.Fatalf("statuses: %v", err)
	}
	encoded, err := json.Marshal(map[string]any{"items": statuses})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), testRawKey) {
		t.Fatal("the settings list leaked a credential")
	}

	for _, status := range statuses {
		switch status.ID {
		case "proxycheck":
			if !status.HasKey || status.Source != "state" {
				t.Fatalf("proxycheck status: %+v", status)
			}
		case "maxmind_geolite2":
			if !status.HasKey {
				t.Fatalf("a keyed config must report has_key: %+v", status)
			}
			// Both account_id and license_key are declared credential fields, so
			// neither value is ever returned; only has_key is.
			for _, field := range []string{"account_id", "license_key"} {
				if _, ok := status.Config[field]; ok {
					t.Fatalf("credential field %s returned in the config view: %+v", field, status.Config)
				}
			}
			if len(status.CredentialFields) != 2 {
				t.Fatalf("credential field names must be reported: %+v", status.CredentialFields)
			}
		}
	}

	single, err := settings.Status("proxycheck", nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	singleJSON, _ := json.Marshal(single)
	if strings.Contains(string(singleJSON), testRawKey) {
		t.Fatal("one provider leaked a credential")
	}
}

// TestSettingsAPIUpdateAppliesToTheLiveRegistry proves that a settings change
// reaches the running data sources without a restart (WP09 §4).
func TestSettingsAPIUpdateAppliesToTheLiveRegistry(t *testing.T) {
	store := newMemorySettingsStore()
	registry, settings := newTestSettingsService(t, store)

	status, err := settings.Update("proxycheck", ProviderPatch{
		Enabled:    ptr(true),
		APIKey:     ptr(testRawKey),
		DailyLimit: ptr(5000),
		QPS:        ptr(2.0),
		TTL:        ptr("12h"),
		Config:     map[string]any{"url": "https://mirror.example.com/check"},
	}, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !status.Enabled || !status.Runnable || !status.HasKey || status.DailyLimit != 5000 || status.QPS != 2 || status.TTL != DurationString(12*time.Hour) {
		t.Fatalf("status after update: %+v", status)
	}
	live, ok := registry.Setting("proxycheck")
	if !ok {
		t.Fatal("the registry has no setting after Apply")
	}
	if !live.Enabled || !live.HasKey() || live.DailyLimit != 5000 || live.QPS != 2 || live.TTL != 12*time.Hour {
		t.Fatalf("the live registry did not pick the change up: enabled=%v has_key=%v limit=%d qps=%v ttl=%s",
			live.Enabled, live.HasKey(), live.DailyLimit, live.QPS, live.EffectiveTTL())
	}
	if live.ConfigString("url") != "https://mirror.example.com/check" {
		t.Fatalf("provider config not applied: %+v", live.Config)
	}
	if _, ok := registry.Online("proxycheck"); !ok {
		t.Fatal("the enabled provider has no live implementation")
	}
	if live.CredentialID() == "" {
		t.Fatal("a configured provider needs a credential fingerprint")
	}

	// Disabling removes the implementation from the live registry.
	if _, err := settings.Update("proxycheck", ProviderPatch{Enabled: ptr(false)}, nil); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, ok := registry.Online("proxycheck"); ok {
		t.Fatal("a disabled provider must not stay live")
	}

	// An explicit empty string clears the stored credential.
	cleared, err := settings.Update("proxycheck", ProviderPatch{APIKey: ptr("")}, nil)
	if err != nil {
		t.Fatalf("clear key: %v", err)
	}
	if cleared.HasKey {
		t.Fatalf("has_key after clearing: %+v", cleared)
	}
	row, _ := store.row("proxycheck")
	if row.APIKey != "" {
		t.Fatal("the cleared key is still stored")
	}
}

// TestSettingsAPIUpdateKeepsCredentialConfigValues guards the maxmind/ipinfo
// credential fields: redaction happens on the response, never on the write.
func TestSettingsAPIUpdateKeepsCredentialConfigValues(t *testing.T) {
	store := newMemorySettingsStore()
	_, settings := newTestSettingsService(t, store)

	status, err := settings.Update("maxmind_geolite2", ProviderPatch{
		Enabled: ptr(true),
		Config:  map[string]any{"account_id": "12345", "license_key": testRawKey},
	}, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !status.HasKey || !status.Runnable {
		t.Fatalf("a fully configured maxmind must be runnable: %+v", status)
	}
	row, ok := store.row("maxmind_geolite2")
	if !ok {
		t.Fatal("the setting was not persisted")
	}
	if !strings.Contains(row.ConfigJSON, "license_key") || !strings.Contains(row.ConfigJSON, "account_id") {
		t.Fatal("the persisted config lost a credential field")
	}
	// The raw value is stored but never returned, and never logged.
	encoded, _ := json.Marshal(status)
	if strings.Contains(string(encoded), testRawKey) {
		t.Fatal("the update response leaked a credential")
	}

	// A later patch without the credential fields keeps them.
	status, err = settings.Update("maxmind_geolite2", ProviderPatch{Enabled: ptr(false)}, nil)
	if err != nil {
		t.Fatalf("second update: %v", err)
	}
	if !status.HasKey {
		t.Fatalf("the credential fields were dropped: %+v", status)
	}
}

// TestSettingsAPIUnlimitedMarkers keeps the documented "0 = unlimited" contract
// working end to end, including after a reload from the persisted shape.
func TestSettingsAPIUnlimitedMarkers(t *testing.T) {
	store := newMemorySettingsStore()
	_, settings := newTestSettingsService(t, store)

	status, err := settings.Update("proxycheck", ProviderPatch{
		Enabled:    ptr(true),
		APIKey:     ptr(testRawKey),
		DailyLimit: ptr(0),
		QPS:        ptr(0.0),
	}, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if status.DailyLimit != 0 || status.QPS != 0 {
		t.Fatalf("0 must mean unlimited: %+v", status)
	}
	row, _ := store.row("proxycheck")
	if row.DailyLimit != -1 || row.QPS != -1 {
		t.Fatalf("the persisted unlimited marker is wrong: limit=%d qps=%v", row.DailyLimit, row.QPS)
	}

	// Reload resolves the same effective values, so a restart cannot silently
	// restore the default quota.
	if err := settings.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	reloaded, err := settings.Status("proxycheck", nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if reloaded.DailyLimit != 0 || reloaded.QPS != 0 {
		t.Fatalf("reload lost the unlimited setting: %+v", reloaded)
	}
}

// TestSettingsAPIReportsBudgetAndPause covers the "why did my batch stop"
// surface: today's usage, the 429 cooldown and the pause state.
func TestSettingsAPIReportsBudgetAndPause(t *testing.T) {
	store := newMemorySettingsStore()
	_, settings := newTestSettingsService(t, store)
	if _, err := settings.Update("proxycheck", ProviderPatch{
		Enabled: ptr(true), APIKey: ptr(testRawKey), DailyLimit: ptr(900), QPS: ptr(1.0),
	}, nil); err != nil {
		t.Fatalf("update: %v", err)
	}

	usage := UsageFunc(func(provider string) ProviderUsage {
		if provider != "proxycheck" {
			return ProviderUsage{}
		}
		return ProviderUsage{
			Day: "2026-02-03", Used: 900, NextRequestAtNs: 1000, BlockedUntilNs: 2000,
			Paused: true, ErrorCode: "PROVIDER_AUTH", Queued: 4, Failed: 1,
		}
	})
	statuses, err := settings.Statuses(usage)
	if err != nil {
		t.Fatalf("statuses: %v", err)
	}
	for _, status := range statuses {
		if status.ID != "proxycheck" {
			if status.DailyLimit == 0 && (status.Usage.Remaining != -1 || status.Usage.Exhausted) {
				t.Fatalf("an unlimited provider must report remaining=-1: %+v", status.Usage)
			}
			continue
		}
		if !status.Usage.Exhausted || status.Usage.Remaining != 0 || !status.Usage.Paused {
			t.Fatalf("exhausted provider usage: %+v", status.Usage)
		}
		if status.Usage.NextAllowedAtNs != 2000 {
			t.Fatalf("next_allowed_at must be the later of the two gates: %+v", status.Usage)
		}
		if status.Usage.Queued != 4 || status.Usage.Failed != 1 {
			t.Fatalf("queue counters: %+v", status.Usage)
		}
	}
}

// TestSettingsAPIUpdateValidation covers the rejected inputs. A validation
// message must never repeat a submitted secret.
func TestSettingsAPIUpdateValidation(t *testing.T) {
	store := newMemorySettingsStore()
	_, settings := newTestSettingsService(t, store)

	cases := []struct {
		name  string
		id    string
		patch ProviderPatch
	}{
		{name: "unknown provider", id: "nope", patch: ProviderPatch{Enabled: ptr(true)}},
		{name: "negative limit", id: "proxycheck", patch: ProviderPatch{DailyLimit: ptr(-5)}},
		{name: "limit above the vendor cap", id: "proxycheck", patch: ProviderPatch{DailyLimit: ptr(2_000_000)}},
		{name: "negative qps", id: "proxycheck", patch: ProviderPatch{QPS: ptr(-1.0)}},
		{name: "qps above the bound", id: "proxycheck", patch: ProviderPatch{QPS: ptr(float64(MaxQPS + 1))}},
		{name: "bad ttl", id: "proxycheck", patch: ProviderPatch{TTL: ptr("tomorrow")}},
		{name: "zero ttl", id: "proxycheck", patch: ProviderPatch{TTL: ptr("0s")}},
		{name: "config key shape", id: "proxycheck", patch: ProviderPatch{Config: map[string]any{"Bad Key": "x"}}},
		{name: "config value type", id: "proxycheck", patch: ProviderPatch{Config: map[string]any{"nested": map[string]any{"a": 1}}}},
		{name: "dnsbl zone", id: "dnsbl", patch: ProviderPatch{Config: map[string]any{"zones": []any{"not a zone"}}}},
		{name: "dnsbl resolver", id: "dnsbl", patch: ProviderPatch{Config: map[string]any{"resolver": "http://1.1.1.1"}}},
		{name: "mirror url", id: "proxycheck", patch: ProviderPatch{Config: map[string]any{"url": "ftp://example.com"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := settings.Update(tc.id, tc.patch, nil)
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if tc.id != "nope" && !errors.Is(err, ErrInvalidSetting) {
				t.Fatalf("error = %v, want ErrInvalidSetting", err)
			}
			if tc.id == "nope" && !errors.Is(err, ErrUnknownProvider) {
				t.Fatalf("error = %v, want ErrUnknownProvider", err)
			}
		})
	}

	// A rejected patch is never persisted.
	if _, ok := store.row("dnsbl"); ok {
		t.Fatal("a rejected patch wrote a row")
	}

	// A secret-looking api_key value must not be echoed by the error.
	_, err := settings.Update("proxycheck", ProviderPatch{APIKey: ptr(testRawKey + "\x00")}, nil)
	if err == nil {
		t.Fatal("expected a validation error for a control character")
	}
	if strings.Contains(err.Error(), testRawKey) {
		t.Fatalf("the validation error repeated the credential: %v", err)
	}
}

// TestSettingsAPIUpdateRemovesConfigKeys documents the null-delete rule.
func TestSettingsAPIUpdateRemovesConfigKeys(t *testing.T) {
	store := newMemorySettingsStore()
	_, settings := newTestSettingsService(t, store)

	if _, err := settings.Update("dnsbl", ProviderPatch{Config: map[string]any{
		"zones":    []any{"zen.spamhaus.org", "bl.spamcop.net"},
		"resolver": "udp://1.1.1.1:53",
	}}, nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	status, err := settings.Update("dnsbl", ProviderPatch{Config: map[string]any{"resolver": nil}}, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, ok := status.Config["resolver"]; ok {
		t.Fatalf("a null value must remove the key: %+v", status.Config)
	}
	if status.Config["zones"] == nil {
		t.Fatalf("an untouched key must stay: %+v", status.Config)
	}
}

// TestSettingsAPIWithoutStoreIsReadOnly covers the degraded wiring: reading
// still works, writing reports the unavailable surface.
func TestSettingsAPIWithoutStoreIsReadOnly(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry, BuiltinConfig{Now: fixedClock()})
	settings := NewSettingsService(registry, nil, nil, fixedClock())

	statuses, err := settings.Statuses(nil)
	if err != nil {
		t.Fatalf("statuses: %v", err)
	}
	if len(statuses) == 0 {
		t.Fatal("a read-only surface still lists the providers")
	}
	if _, err := settings.Update("proxycheck", ProviderPatch{Enabled: ptr(true)}, nil); !errors.Is(err, ErrSettingsUnavailable) {
		t.Fatalf("error = %v, want ErrSettingsUnavailable", err)
	}
}

// TestSettingsAPIEnvironmentIsTheInitialValue keeps the WP09 §1 precedence: the
// environment is the first value, the settings page takes over afterwards.
func TestSettingsAPIEnvironmentIsTheInitialValue(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry, BuiltinConfig{Now: fixedClock()})
	env := func(name string) string {
		if name == "PRISM_QUALITY_API_KEY" {
			return testRawKey
		}
		return ""
	}
	settings := NewSettingsService(registry, newMemorySettingsStore(), env, fixedClock())
	if err := settings.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	status, err := settings.Status("proxycheck", nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.HasKey || status.Source != "env" {
		t.Fatalf("the environment value must be the initial setting: %+v", status)
	}
	encoded, _ := json.Marshal(status)
	if strings.Contains(string(encoded), testRawKey) {
		t.Fatal("the environment credential leaked")
	}

	// Source distinguishes a spec default from an environment value: a provider
	// with a finite default limit and no environment variable stays "default".
	plain := DefaultSetting(NewAbuseIPDBProvider(AbuseIPDBOptions{}).Spec(), func(string) string { return "" })
	if plain.Source != "default" {
		t.Fatalf("source = %q, want default", plain.Source)
	}
}
