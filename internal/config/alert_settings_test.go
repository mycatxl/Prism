package config

import (
	"reflect"
	"testing"
)

// TestLoadEnvConfig_AlertDefaults pins the shipped usage-alert settings: an
// installation that never sets PRISM_ALERT_* reports an account from a third of
// its quota inside an hour and keeps the report for 90 days.
func TestLoadEnvConfig_AlertDefaults(t *testing.T) {
	unsetEnvForConfigTest(t, "PRISM_ALERT_USAGE_RATIO", "PRISM_ALERT_WINDOW_MINUTES", "PRISM_ALERT_RETENTION_DAYS")
	setEnvs(t, requiredEnvs())

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, "AlertUsageRatio", cfg.AlertUsageRatio, DefaultAlertUsageRatio)
	assertEqual(t, "AlertWindowMinutes", cfg.AlertWindowMinutes, DefaultAlertWindowMinutes)
	assertEqual(t, "AlertRetentionDays", cfg.AlertRetentionDays, DefaultAlertRetentionDays)
}

// TestLoadEnvConfig_AlertOverrides pins that the three variables actually reach
// the configuration instead of always answering the shipped values.
func TestLoadEnvConfig_AlertOverrides(t *testing.T) {
	envs := requiredEnvs()
	envs["PRISM_ALERT_USAGE_RATIO"] = "0.75"
	envs["PRISM_ALERT_WINDOW_MINUTES"] = "15"
	envs["PRISM_ALERT_RETENTION_DAYS"] = "7"
	setEnvs(t, envs)

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, "AlertUsageRatio", cfg.AlertUsageRatio, 0.75)
	assertEqual(t, "AlertWindowMinutes", cfg.AlertWindowMinutes, 15)
	assertEqual(t, "AlertRetentionDays", cfg.AlertRetentionDays, 7)
}

// TestLoadEnvConfig_AlertInvalidValues pins that an unusable alert setting
// refuses the whole configuration and that the error names the variable it came
// from, which is the only way an operator finds the offending line.
func TestLoadEnvConfig_AlertInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		raw     string
		wantErr string
	}{
		{name: "ratio zero", key: "PRISM_ALERT_USAGE_RATIO", raw: "0", wantErr: "PRISM_ALERT_USAGE_RATIO"},
		{name: "ratio above one", key: "PRISM_ALERT_USAGE_RATIO", raw: "1.5", wantErr: "PRISM_ALERT_USAGE_RATIO"},
		{name: "ratio negative", key: "PRISM_ALERT_USAGE_RATIO", raw: "-0.3", wantErr: "PRISM_ALERT_USAGE_RATIO"},
		{name: "ratio not a number", key: "PRISM_ALERT_USAGE_RATIO", raw: "half", wantErr: "PRISM_ALERT_USAGE_RATIO"},
		{name: "window zero", key: "PRISM_ALERT_WINDOW_MINUTES", raw: "0", wantErr: "PRISM_ALERT_WINDOW_MINUTES"},
		{name: "window not an integer", key: "PRISM_ALERT_WINDOW_MINUTES", raw: "60m", wantErr: "PRISM_ALERT_WINDOW_MINUTES"},
		{name: "retention zero", key: "PRISM_ALERT_RETENTION_DAYS", raw: "0", wantErr: "PRISM_ALERT_RETENTION_DAYS"},
		{name: "retention negative", key: "PRISM_ALERT_RETENTION_DAYS", raw: "-90", wantErr: "PRISM_ALERT_RETENTION_DAYS"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			envs := requiredEnvs()
			envs[tc.key] = tc.raw
			setEnvs(t, envs)

			_, err := LoadEnvConfig()
			if err == nil {
				t.Fatalf("%s=%q was accepted, want an error naming the variable", tc.key, tc.raw)
			}
			assertContains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestAlertRatioAndWindowFallbacks pins the fallback helpers a config row
// persisted before the settings existed decodes through: an unusable value
// answers the shipped one rather than disabling the scanner.
func TestAlertRatioAndWindowFallbacks(t *testing.T) {
	for _, configured := range []float64{0, -0.5, 1.5, 2} {
		assertEqual(t, "AlertRatio fallback", AlertRatio(configured), DefaultAlertUsageRatio)
	}
	assertEqual(t, "AlertRatio exact one", AlertRatio(1), 1.0)
	assertEqual(t, "AlertRatio configured", AlertRatio(0.5), 0.5)

	for _, configured := range []int{0, -10} {
		assertEqual(t, "AlertWindow fallback", AlertWindow(configured), DefaultAlertWindowMinutes)
	}
	assertEqual(t, "AlertWindow configured", AlertWindow(15), 15)
}

// TestNewDefaultRuntimeConfig_AlertSettings pins that a brand-new runtime
// config carries the shipped alert settings instead of zeroes, which the
// scanner would otherwise read as "use the shipped value" anyway but which the
// settings page would render as empty inputs.
func TestNewDefaultRuntimeConfig_AlertSettings(t *testing.T) {
	cfg := NewDefaultRuntimeConfig()
	if !reflect.DeepEqual(
		[]float64{cfg.AlertUsageRatio, float64(cfg.AlertWindowMinutes), float64(cfg.AlertRetentionDays)},
		[]float64{DefaultAlertUsageRatio, DefaultAlertWindowMinutes, DefaultAlertRetentionDays},
	) {
		t.Fatalf(
			"alert settings = %g/%d/%d, want %g/%d/%d",
			cfg.AlertUsageRatio, cfg.AlertWindowMinutes, cfg.AlertRetentionDays,
			DefaultAlertUsageRatio, DefaultAlertWindowMinutes, DefaultAlertRetentionDays,
		)
	}
}
