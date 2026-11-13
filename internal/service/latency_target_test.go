package service

import (
	"strings"
	"testing"
)

func TestLatencyTargetRequiresHTTPSMeasurement(t *testing.T) {
	cfg := newDefaultCfg()
	cfg.LatencyTestURL = "http://example.test/probe"
	err := validateRuntimeConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatal("HTTP latency target must explain the required HTTPS measurement")
	}
	cfg.LatencyTestURL = "https://example.test/probe"
	cfg.LatencyAuthorities = []string{"example.test"}
	if err := validateRuntimeConfig(cfg); err != nil {
		t.Fatal(err)
	}
}
