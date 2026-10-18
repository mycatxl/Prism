package service

import (
	"fmt"
	"strings"
	"testing"

	"prism/internal/config"
)

// TestPatchRuntimeConfig_RefusesARetentionWindowThatWouldOverflow pins the upper
// bound on the three retention windows.
//
// Each is turned into a nanosecond offset and subtracted from the current time.
// 106752 days already overflows int64, so the offset is negative and the cutoff
// lands in the future, where the prune it drives removes every row instead of the
// expired ones. The patch has to be refused rather than clamped, because an
// operator who typed the value needs to know it did not take.
func TestPatchRuntimeConfig_RefusesARetentionWindowThatWouldOverflow(t *testing.T) {
	for _, field := range []string{
		"audit_retention_days",
		"usage_retention_days",
		"alert_retention_days",
	} {
		for _, days := range []int{config.MaxRetentionDays + 1, 106752, 200000} {
			t.Run(fmt.Sprintf("%s=%d", field, days), func(t *testing.T) {
				h := newPatchHarness(t)
				before := cloneRuntimeConfig(h.runtimeCfg.Load())

				_, err := h.cp.PatchRuntimeConfig([]byte(fmt.Sprintf(`{"%s":%d}`, field, days)))
				if err == nil {
					t.Fatalf("patch %s=%d was accepted, want a validation error", field, days)
				}
				if !strings.Contains(err.Error(), field) {
					t.Fatalf("error %q does not name %s", err, field)
				}
				if after := cloneRuntimeConfig(h.runtimeCfg.Load()); after.AuditRetentionDays != before.AuditRetentionDays ||
					after.UsageRetentionDays != before.UsageRetentionDays ||
					after.AlertRetentionDays != before.AlertRetentionDays {
					t.Fatalf("a refused patch changed the in-memory retention windows: before=%+v after=%+v", before, after)
				}
			})
		}
	}
}

// TestPatchRuntimeConfig_AcceptsTheRetentionCeiling checks the bound is a ceiling
// and not an off-by-one: the value itself has to be accepted.
func TestPatchRuntimeConfig_AcceptsTheRetentionCeiling(t *testing.T) {
	for _, field := range []string{
		"audit_retention_days",
		"usage_retention_days",
		"alert_retention_days",
	} {
		t.Run(field, func(t *testing.T) {
			h := newPatchHarness(t)
			cfg, err := h.cp.PatchRuntimeConfig([]byte(fmt.Sprintf(`{"%s":%d}`, field, config.MaxRetentionDays)))
			if err != nil {
				t.Fatalf("patch %s=%d was refused: %v", field, config.MaxRetentionDays, err)
			}
			switch field {
			case "audit_retention_days":
				if cfg.AuditRetentionDays != config.MaxRetentionDays {
					t.Fatalf("audit_retention_days = %d, want %d", cfg.AuditRetentionDays, config.MaxRetentionDays)
				}
			case "usage_retention_days":
				if cfg.UsageRetentionDays != config.MaxRetentionDays {
					t.Fatalf("usage_retention_days = %d, want %d", cfg.UsageRetentionDays, config.MaxRetentionDays)
				}
			case "alert_retention_days":
				if cfg.AlertRetentionDays != config.MaxRetentionDays {
					t.Fatalf("alert_retention_days = %d, want %d", cfg.AlertRetentionDays, config.MaxRetentionDays)
				}
			}
		})
	}
}
