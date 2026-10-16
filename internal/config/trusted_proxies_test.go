package config

import (
	"reflect"
	"testing"
)

// PRISM_TRUSTED_PROXIES is the list of peers whose forwarded headers Prism
// believes. A CIDR that covers every source address turns the list into "trust
// anyone", which hands X-Forwarded-For (the rate-limit key) and
// X-Forwarded-Proto (the HTTPS decision) to whoever is calling.

// TestLoadEnvConfig_TrustedProxiesRejectCatchAllPrefixes pins that a prefix
// covering every source address is refused instead of silently trusted.
func TestLoadEnvConfig_TrustedProxiesRejectCatchAllPrefixes(t *testing.T) {
	for _, value := range []string{"0.0.0.0/0", "::/0"} {
		t.Run(value, func(t *testing.T) {
			unsetEnvForConfigTest(t, "PRISM_TRUSTED_PROXIES", "RESIN_TRUSTED_PROXIES")
			envs := requiredEnvs()
			envs["PRISM_TRUSTED_PROXIES"] = value
			setEnvs(t, envs)

			_, err := LoadEnvConfig()
			if err == nil {
				t.Fatalf("LoadEnvConfig accepted %s", value)
			}
			assertContains(t, err.Error(), "PRISM_TRUSTED_PROXIES")
			assertContains(t, err.Error(), value)
		})
	}
}

// TestLoadEnvConfig_TrustedProxiesAcceptRealValues pins that the ordinary
// spellings keep working: single addresses, normal prefixes and mixed lists,
// in both address families.
func TestLoadEnvConfig_TrustedProxiesAcceptRealValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  []string
	}{
		{"loopback address", "127.0.0.1", []string{"127.0.0.1"}},
		{"private prefix", "10.0.0.0/8", []string{"10.0.0.0/8"}},
		{"unique local prefix", "fd00::/8", []string{"fd00::/8"}},
		{"single host prefix", "127.0.0.1/32", []string{"127.0.0.1/32"}},
		{"mixed list", "127.0.0.1,10.0.0.0/8,fd00::/8", []string{"127.0.0.1", "10.0.0.0/8", "fd00::/8"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetEnvForConfigTest(t, "PRISM_TRUSTED_PROXIES", "RESIN_TRUSTED_PROXIES")
			envs := requiredEnvs()
			envs["PRISM_TRUSTED_PROXIES"] = tc.value
			setEnvs(t, envs)

			cfg, err := LoadEnvConfig()
			if err != nil {
				t.Fatalf("LoadEnvConfig(%q): %v", tc.value, err)
			}
			if !reflect.DeepEqual(cfg.TrustedProxies, tc.want) {
				t.Fatalf("TrustedProxies = %v, want %v", cfg.TrustedProxies, tc.want)
			}
		})
	}
}
