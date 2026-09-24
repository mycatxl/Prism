package main

import (
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/state"
)

// TestSubscriptionAutoIntelReachesBootstrapAndPipeline covers WP08 §3.6 from the
// application side: the persisted auto_intel flag is restored onto the runtime
// subscription by bootstrapTopology and is the value the intel pipeline reads
// through prismApp.intelSubscriptionAutoIntel.
//
// The API side (POST/PATCH /api/v1/subscriptions writing that column) is pinned
// by internal/api TestAPIContract_SubscriptionAutoIntel_RoundTripsAndReachesTheStore.
func TestSubscriptionAutoIntelReachesBootstrapAndPipeline(t *testing.T) {
	engine, closer, err := state.PersistenceBootstrap(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	now := time.Now().UnixNano()
	for _, sub := range []model.Subscription{
		{
			ID:               "sub-intel-off",
			Name:             "IntelOff",
			SourceType:       "local",
			Content:          "127.0.0.1:8080",
			UpdateIntervalNs: int64(time.Hour),
			Enabled:          true,
			AutoIntel:        false,
			CreatedAtNs:      now,
			UpdatedAtNs:      now,
		},
		{
			ID:               "sub-intel-on",
			Name:             "IntelOn",
			SourceType:       "local",
			Content:          "127.0.0.1:8081",
			UpdateIntervalNs: int64(time.Hour),
			Enabled:          true,
			AutoIntel:        true,
			CreatedAtNs:      now,
			UpdatedAtNs:      now,
		},
	} {
		if err := engine.UpsertSubscription(sub); err != nil {
			t.Fatalf("UpsertSubscription(%s): %v", sub.ID, err)
		}
	}

	app := &prismApp{stateEngine: engine}

	// The pipeline lookup reads the persisted flag.
	if got, ok := app.intelSubscriptionAutoIntel("sub-intel-off"); !ok || got {
		t.Errorf("intelSubscriptionAutoIntel(sub-intel-off): got (%v, %v), want (false, true)", got, ok)
	}
	if got, ok := app.intelSubscriptionAutoIntel("sub-intel-on"); !ok || !got {
		t.Errorf("intelSubscriptionAutoIntel(sub-intel-on): got (%v, %v), want (true, true)", got, ok)
	}
	if _, ok := app.intelSubscriptionAutoIntel("sub-missing"); ok {
		t.Error("intelSubscriptionAutoIntel(sub-missing): got ok=true, want false")
	}

	// bootstrapTopology restores the flag onto the runtime subscription, so the
	// API response reports the same value the pipeline uses.
	subManager, pool := newBootstrapTestRuntime(config.NewDefaultRuntimeConfig())
	if err := bootstrapTopology(engine, subManager, pool, newDefaultPlatformEnvConfig()); err != nil {
		t.Fatalf("bootstrapTopology: %v", err)
	}
	for id, want := range map[string]bool{"sub-intel-off": false, "sub-intel-on": true} {
		sub := subManager.Lookup(id)
		if sub == nil {
			t.Fatalf("subscription %s missing after bootstrapTopology", id)
		}
		if got := sub.AutoIntel(); got != want {
			t.Errorf("runtime auto_intel for %s: got %v, want %v", id, got, want)
		}
	}
}
