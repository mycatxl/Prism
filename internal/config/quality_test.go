package config

import (
	"os"
	"testing"
)

// TestLoadQualityConfig_IgnoresDeprecatedQueueSize pins the deprecation of
// PRISM_QUALITY_QUEUE_SIZE.
//
// The variable stopped being consumed when the provider queue started following
// intel_node_workers (internal/intel/providers/settings.go only emits a warning
// for it). It used to be range-validated here anyway, so a leftover value from an
// older deployment - or a typo like 99999 - aborted the whole process with
// "PRISM_QUALITY_QUEUE_SIZE must be between 1 and 4096" even though nothing reads
// it. A deprecated setting must not be able to fail startup.
func TestLoadQualityConfig_IgnoresDeprecatedQueueSize(t *testing.T) {
	for _, raw := range []string{"99999", "0", "-5", "not-a-number"} {
		t.Setenv("PRISM_QUALITY_QUEUE_SIZE", raw)
		cfg, err := LoadQualityConfig()
		if err != nil {
			t.Errorf("PRISM_QUALITY_QUEUE_SIZE=%q made LoadQualityConfig fail: %v", raw, err)
			continue
		}
		// The value must not reach the config either: the queue size is no
		// longer a quality setting.
		if cfg.QueueSize != 4096 {
			t.Errorf("PRISM_QUALITY_QUEUE_SIZE=%q changed QueueSize to %d; the variable is ignored, so the default must stay", raw, cfg.QueueSize)
		}
	}
}

// TestLoadQualityConfig_StillValidatesLiveSettings is the control: the settings
// that are actually consumed keep their bounds, while a deprecated one cannot
// abort startup with a value nothing reads.
func TestLoadQualityConfig_StillValidatesLiveSettings(t *testing.T) {
	for _, raw := range []string{"9", "0", "not-a-number"} {
		t.Setenv("PRISM_QUALITY_WORKERS", raw)
		if _, err := LoadQualityConfig(); err != nil {
			t.Errorf("PRISM_QUALITY_WORKERS=%q rejected: %v; the variable is deprecated, so it must be ignored", raw, err)
		}
	}
	t.Setenv("PRISM_QUALITY_WORKERS", "")

	t.Setenv("PRISM_QUALITY_DAILY_LIMIT", "901")
	if _, err := LoadQualityConfig(); err == nil {
		t.Error("PRISM_QUALITY_DAILY_LIMIT=901 was accepted; the live bound is 1..900")
	}
	t.Setenv("PRISM_QUALITY_DAILY_LIMIT", "80")
	if _, err := LoadQualityConfig(); err != nil {
		t.Errorf("PRISM_QUALITY_DAILY_LIMIT=80 rejected: %v", err)
	}
}

// TestLoadQualityConfig_AnonymousDailyLimitCeiling keeps the rule that the
// anonymous (keyless) allowance stays at or below 80/day.
func TestLoadQualityConfig_AnonymousDailyLimitCeiling(t *testing.T) {
	os.Unsetenv("PRISM_QUALITY_API_KEY")
	t.Setenv("PRISM_QUALITY_DAILY_LIMIT", "80")
	if _, err := LoadQualityConfig(); err != nil {
		t.Errorf("the anonymous ceiling itself must be allowed: %v", err)
	}
}
