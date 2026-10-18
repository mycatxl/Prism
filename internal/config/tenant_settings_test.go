package config

import (
	"encoding/json"
	"testing"
)

// TestLoadEnvConfig_RegistrationModes covers the three accepted values and the
// default. The default matters most: an installation that has not thought about
// self-service sign-up must not be open to it.
func TestLoadEnvConfig_RegistrationModes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want RegistrationMode
	}{
		{"default", "", RegistrationOff},
		{"off", "off", RegistrationOff},
		{"invite", "invite", RegistrationInvite},
		{"open padded", "  open  ", RegistrationOpen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envs := requiredEnvs()
			envs["PRISM_REGISTRATION"] = tc.raw
			setEnvs(t, envs)

			cfg, err := LoadEnvConfig()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertEqual(t, "Registration", cfg.Registration, tc.want)
		})
	}
}

func TestLoadEnvConfig_InvalidRegistration(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_REGISTRATION"] = "banana"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected an error for an unknown PRISM_REGISTRATION")
	}
	assertContains(t, err.Error(), "PRISM_REGISTRATION")
}

func TestRegistrationModeAllowsRegistration(t *testing.T) {
	if RegistrationOff.AllowsRegistration() {
		t.Error("off must not allow a sign-up")
	}
	if !RegistrationInvite.AllowsRegistration() || !RegistrationOpen.AllowsRegistration() {
		t.Error("invite and open must allow a sign-up")
	}
	if RegistrationMode("banana").Valid() {
		t.Error("an unknown mode must not be valid")
	}
}

// TestPurityBandsNormalized pins the fallbacks: an unusable set is answered
// with the shipped thresholds rather than with a set that would change what
// every existing platform filter serves.
func TestPurityBandsNormalized(t *testing.T) {
	if got := (PurityBands{}).Normalized(); got != DefaultPurityBands() {
		t.Errorf("zero bands normalized to %+v, want the shipped bands", got)
	}
	if got := (PurityBands{Excellent: 50, Clean: 60, Fair: 70, Mixed: 80}).Normalized(); got != DefaultPurityBands() {
		t.Errorf("ascending bands normalized to %+v, want the shipped bands", got)
	}
	if got := (PurityBands{Excellent: 101, Clean: 90, Fair: 80, Mixed: 60}).Normalized(); got != DefaultPurityBands() {
		t.Errorf("out-of-range bands normalized to %+v, want the shipped bands", got)
	}
	configured := PurityBands{Excellent: 90, Clean: 80, Fair: 70, Mixed: 50}
	if got := configured.Normalized(); got != configured {
		t.Errorf("usable bands normalized to %+v, want them unchanged", got)
	}
}

func TestRetentionDays(t *testing.T) {
	cases := []struct {
		configured int
		shipped    int
		want       int
	}{
		{0, 90, 90},
		{-1, 90, 90},
		{30, 90, 30},
		{365, 90, 365},
	}
	for _, tc := range cases {
		if got := RetentionDays(tc.configured, tc.shipped); got != tc.want {
			t.Errorf("RetentionDays(%d, %d) = %d, want %d", tc.configured, tc.shipped, got, tc.want)
		}
	}
}

// TestRuntimeConfigTenantSettingsDefault asserts the shipped defaults and the
// JSON field names the console patches: a rename here would break the settings
// page without breaking the build.
func TestRuntimeConfigTenantSettingsDefault(t *testing.T) {
	cfg := NewDefaultRuntimeConfig()
	assertEqual(t, "AuditRetentionDays", cfg.AuditRetentionDays, DefaultAuditRetentionDays)
	assertEqual(t, "UsageRetentionDays", cfg.UsageRetentionDays, DefaultUsageRetentionDays)
	assertEqual(t, "PurityBands", cfg.PurityBands, DefaultPurityBands())

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, name := range []string{"audit_retention_days", "usage_retention_days", "purity_bands"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("runtime config JSON is missing %q", name)
		}
	}
}

// TestRuntimeConfigTenantSettingsFromOldRow covers the upgrade path: a config
// persisted before these settings existed decodes to zero values, and the
// consumers must treat that as "the shipped window" rather than as "keep
// nothing".
func TestRuntimeConfigTenantSettingsFromOldRow(t *testing.T) {
	const legacy = `{"request_log_enabled":true,"intel_enabled":true}`
	var cfg RuntimeConfig
	if err := json.Unmarshal([]byte(legacy), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertEqual(t, "AuditRetentionDays", RetentionDays(cfg.AuditRetentionDays, DefaultAuditRetentionDays), DefaultAuditRetentionDays)
	assertEqual(t, "UsageRetentionDays", RetentionDays(cfg.UsageRetentionDays, DefaultUsageRetentionDays), DefaultUsageRetentionDays)
	assertEqual(t, "PurityBands", cfg.PurityBands.Normalized(), DefaultPurityBands())
}

// TestEffectiveRegistrationMode covers the resolution the console and the
// register endpoint share: an override the service understands wins, an empty
// or unreadable override defers to the deployment, and an unreadable deployment
// value fails closed. Failing closed is the point - a typo in PRISM_REGISTRATION
// must never open self-service sign-up by accident.
func TestEffectiveRegistrationMode(t *testing.T) {
	cases := []struct {
		name     string
		override RegistrationMode
		fallback RegistrationMode
		want     RegistrationMode
	}{
		{"no override follows env", "", RegistrationInvite, RegistrationInvite},
		{"override wins", RegistrationOpen, RegistrationOff, RegistrationOpen},
		{"override closes an open deployment", RegistrationOff, RegistrationOpen, RegistrationOff},
		{"valid override outranks an unreadable env", RegistrationOpen, "banana", RegistrationOpen},
		{"no override and no env", "", "", RegistrationOff},
		{"unreadable env fails closed", RegistrationOpen, "banana", RegistrationOpen},
		{"both unreadable fails closed", "banana", "banana", RegistrationOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &RuntimeConfig{RegistrationMode: tc.override}
			assertEqual(t, "EffectiveRegistrationMode", cfg.EffectiveRegistrationMode(tc.fallback), tc.want)
		})
	}

	// The stored config is read on every sign-up through a pointer that may not
	// be populated yet during boot; a nil receiver must not decide the door is
	// open.
	var nilCfg *RuntimeConfig
	assertEqual(t, "nil config", nilCfg.EffectiveRegistrationMode(RegistrationOpen), RegistrationOpen)
	assertEqual(t, "nil config, no env", nilCfg.EffectiveRegistrationMode(""), RegistrationOff)
}
