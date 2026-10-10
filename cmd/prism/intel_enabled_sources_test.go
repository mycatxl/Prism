package main

import (
	"testing"
	"time"

	"prism/internal/intel/assess"
	"prism/internal/intel/providers"
)

// intelEnabledSources must fold an alias variant onto its scoring source when
// building the coverage denominator. The scoring layer does that folding in
// assess.lookup (assess.ProviderAliases), so a via-node variant that answered
// puts its weight into the numerator. If the denominator kept ignoring the
// variant, coverage would be truncated at 1.0 and confidence, the `incomplete`
// verdict and min_confidence admission would all be overstated.
// docs/INTEL.md §9 recorded that inconsistency as undecided.
func TestIntelEnabledSourcesResolvesAliasVariant(t *testing.T) {
	now := func() time.Time { return time.Unix(0, 0).UTC() }

	newRegistry := func(hostEnabled, variantEnabled bool) *providers.Registry {
		registry := providers.NewRegistry()
		providers.RegisterBuiltins(registry, providers.BuiltinConfig{Now: now})

		settings := make([]providers.Setting, 0, 2)
		for _, id := range []string{assess.SourceProxycheck, assess.SourceProxycheckNode} {
			spec, ok := registry.Spec(id)
			if !ok {
				t.Fatalf("provider %s is not registered", id)
			}
			enabled := hostEnabled
			if id == assess.SourceProxycheckNode {
				enabled = variantEnabled
			}
			settings = append(settings, providers.Setting{
				Spec:    spec,
				Enabled: enabled,
				Source:  "test",
			})
		}
		registry.Apply(settings)
		return registry
	}

	t.Run("variant alone keeps the source in the denominator", func(t *testing.T) {
		enabled := intelEnabledSources(newRegistry(false, true))
		if !enabled[assess.SourceProxycheck] {
			t.Fatalf("proxycheck must count as enabled when its alias variant is: %#v", enabled)
		}
	})

	t.Run("both paths disabled drops the source", func(t *testing.T) {
		enabled := intelEnabledSources(newRegistry(false, false))
		if enabled[assess.SourceProxycheck] {
			t.Fatalf("proxycheck must not count as enabled when no path is usable: %#v", enabled)
		}
	})

	t.Run("host path alone keeps the source", func(t *testing.T) {
		enabled := intelEnabledSources(newRegistry(true, false))
		if !enabled[assess.SourceProxycheck] {
			t.Fatalf("proxycheck must count as enabled when the host path is: %#v", enabled)
		}
	})

	t.Run("nil registry keeps the permissive default", func(t *testing.T) {
		enabled := intelEnabledSources(nil)
		for _, id := range assess.ScoringSources() {
			if !enabled[id] {
				t.Fatalf("source %s must default to enabled without a registry", id)
			}
		}
	})
}
