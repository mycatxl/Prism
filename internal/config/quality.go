package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// QualityConfig is the part of the quality pipeline that reads Prism-only
// environment variables.
//
// PRISM_QUALITY_ENABLED and PRISM_QUALITY_WORKERS used to be parsed here. They
// never reached the running pipeline: the switches that actually decide whether
// the intel jobs run are the runtime settings intel_enabled and
// intel_node_workers. A value set in the environment therefore did nothing while
// `prism check-config` printed it as if it did. They are no longer read, and they
// are deprecated with a warning where the provider settings are seeded - the same
// treatment PRISM_QUALITY_QUEUE_SIZE already gets.
type QualityConfig struct {
	APIKey      string
	AbuseAPIKey string
	DailyLimit  int
	QueueSize   int
	Timeout     time.Duration
	CacheTTL    time.Duration
}

func LoadQualityConfig() (QualityConfig, error) {
	cfg := QualityConfig{
		APIKey:      strings.TrimSpace(qualityEnv("PRISM_QUALITY_API_KEY")),
		AbuseAPIKey: strings.TrimSpace(qualityEnv("PRISM_ABUSEIPDB_API_KEY")),
		DailyLimit:  80,
		QueueSize:   4096,
		Timeout:     10 * time.Second,
		CacheTTL:    24 * time.Hour,
	}
	if cfg.APIKey != "" {
		cfg.DailyLimit = 900
	}
	for _, setting := range []struct {
		name     string
		value    *int
		min, max int
	}{
		{"PRISM_QUALITY_DAILY_LIMIT", &cfg.DailyLimit, 1, 900},
	} {
		if raw := qualityEnv(setting.name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < setting.min || value > setting.max {
				return cfg, fmt.Errorf("%s must be between %d and %d", setting.name, setting.min, setting.max)
			}
			*setting.value = value
		}
	}
	// PRISM_QUALITY_ENABLED, PRISM_QUALITY_WORKERS and PRISM_QUALITY_QUEUE_SIZE
	// are deprecated and ignored: intel_enabled decides whether the quality jobs
	// run, and intel_node_workers sizes them. They are deliberately NOT validated
	// here - rejecting a value the process never uses would turn a harmless
	// leftover variable from an older deployment into a startup failure. The
	// deprecation warning is emitted where the provider settings are seeded
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
