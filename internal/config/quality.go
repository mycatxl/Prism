package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type QualityConfig struct {
	Enabled     bool
	APIKey      string
	AbuseAPIKey string
	DailyLimit  int
	Workers     int
	QueueSize   int
	Timeout     time.Duration
	CacheTTL    time.Duration
}

func LoadQualityConfig() (QualityConfig, error) {
	cfg := QualityConfig{
		Enabled: true, APIKey: strings.TrimSpace(qualityEnv("PRISM_QUALITY_API_KEY")),
		AbuseAPIKey: strings.TrimSpace(qualityEnv("PRISM_ABUSEIPDB_API_KEY")),
		DailyLimit:  80, Workers: 2, QueueSize: 4096,
		Timeout: 10 * time.Second, CacheTTL: 24 * time.Hour,
	}
	if cfg.APIKey != "" {
		cfg.DailyLimit = 900
	}
	if value := qualityEnv("PRISM_QUALITY_ENABLED"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return cfg, fmt.Errorf("PRISM_QUALITY_ENABLED must be a boolean")
		}
		cfg.Enabled = enabled
	}
	for _, setting := range []struct {
		name     string
		value    *int
		min, max int
	}{
		{"PRISM_QUALITY_DAILY_LIMIT", &cfg.DailyLimit, 1, 900},
		{"PRISM_QUALITY_WORKERS", &cfg.Workers, 1, 8},
	} {
		if raw := qualityEnv(setting.name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < setting.min || value > setting.max {
				return cfg, fmt.Errorf("%s must be between %d and %d", setting.name, setting.min, setting.max)
			}
			*setting.value = value
		}
	}
	// PRISM_QUALITY_QUEUE_SIZE is deprecated and ignored: the provider queue is
	// bounded by intel_node_workers. It is deliberately NOT validated here -
	// rejecting a value the process never uses would turn a harmless leftover
	// variable from an older deployment into a startup failure. The deprecation
	// warning is emitted where the setting is actually seeded
	// (internal/intel/providers/settings.go).
	// Anonymous requests share a provider quota by public source IP. Leave
	// headroom for other clients and never attempt to bypass that limit.
	if cfg.APIKey == "" && cfg.DailyLimit > 80 {
		return cfg, fmt.Errorf("PRISM_QUALITY_DAILY_LIMIT cannot exceed 80 without PRISM_QUALITY_API_KEY")
	}
	return cfg, nil
}

// qualityEnv reads a Prism-only quality setting.
//
// These variables have no RESIN_ fallback - they only exist in Prism - so the
// read stays on the raw environment. The origin is recorded all the same,
// because `prism check-config` prints a source for every row it shows.
func qualityEnv(name string) string {
	value, set := os.LookupEnv(name)
	// Unset is the shipped default and "set but empty" is a line that carries
	// nothing; the two are recorded as the different answers they are.
	if set {
		recordEnvOrigin(name, envSourceForValue(value, false))
	} else {
		recordEnvOrigin(name, EnvSourceDefault)
	}
	return value
}
