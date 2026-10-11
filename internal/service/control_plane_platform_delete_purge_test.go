package service

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/platform"
	"prism/internal/routing"
	"prism/internal/state"
	"prism/internal/topology"
)

// TestDeletePlatform_PurgesRouterState covers audit 3.2 (#11): DeletePlatform
// removed the platform from the engine and the topology pool but left its
// Router.states entry (lease table + per-IP load counters) behind forever.
func TestDeletePlatform_PurgesRouterState(t *testing.T) {
	dir := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(
		filepath.Join(dir, "state"),
		filepath.Join(dir, "cache"),
	)
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	platformRow := model.Platform{
		ID:                     "plat-delete-purge",
		Name:                   "delete-purge",
		StickyTTLNs:            int64(time.Hour),
		RegexFilters:           []string{},
		RegionFilters:          []string{"us"},
		ReverseProxyMissAction: "TREAT_AS_EMPTY",
		AllocationPolicy:       "BALANCED",
		UpdatedAtNs:            time.Now().UnixNano(),
	}
	if err := engine.UpsertPlatform(platformRow); err != nil {
		t.Fatalf("UpsertPlatform: %v", err)
	}

	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              nil,
		GeoLookup:              func(netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})
	pool.RegisterPlatform(platform.NewConfiguredPlatform(
		platformRow.ID,
		platformRow.Name,
		node.TagFilter{},
		nil,
		model.QualityPolicy{},
		platformRow.StickyTTLNs,
		platformRow.ReverseProxyMissAction,
		string(platform.ReverseProxyEmptyAccountBehaviorAccountHeaderRule),
		"",
		platformRow.AllocationPolicy,
		true,
		false,
		0,
		false,
	))

	var events []routing.LeaseEvent
	router := routing.NewRouter(routing.RouterConfig{
		Pool:        pool,
		Authorities: func() []string { return []string{"cloudflare.com"} },
		P2CWindow:   func() time.Duration { return 10 * time.Minute },
		OnLeaseEvent: func(e routing.LeaseEvent) {
			events = append(events, e)
		},
	})

	cp := &ControlPlaneService{
		Engine: engine,
		Pool:   pool,
		Router: router,
	}

	now := time.Now().UnixNano()
	accounts := []string{"alice", "bob", "carol"}
	for i, account := range accounts {
		if err := router.UpsertLease(model.Lease{
			PlatformID:     platformRow.ID,
			Account:        account,
			NodeHash:       node.HashFromRawOptions([]byte(`{"id":"purge-` + account + `"}`)).Hex(),
			EgressIP:       fmt.Sprintf("203.0.113.%d", 10+i),
			CreatedAtNs:    now - int64(time.Minute),
			ExpiryNs:       now + int64(time.Hour),
			LastAccessedNs: now,
		}); err != nil {
			t.Fatalf("UpsertLease(%s): %v", account, err)
		}
	}

	if snap := router.SnapshotIPLoad(platformRow.ID); len(snap) != len(accounts) {
		t.Fatalf("precondition: ip load snapshot = %v, want %d entries", snap, len(accounts))
	}
	if !router.RangeLeases(platformRow.ID, func(string, routing.Lease) bool { return false }) {
		t.Fatal("precondition: Router has no state for the platform")
	}

	events = nil
	if err := cp.DeletePlatform(platformRow.ID); err != nil {
		t.Fatalf("DeletePlatform: %v", err)
	}

	if _, ok := pool.GetPlatform(platformRow.ID); ok {
		t.Fatalf("platform %s should be removed from pool", platformRow.ID)
	}
	if router.RangeLeases(platformRow.ID, func(string, routing.Lease) bool { return true }) {
		t.Fatal("Router.states kept the deleted platform's leases (audit 3.2 leak)")
	}
	if snap := router.SnapshotIPLoad(platformRow.ID); len(snap) != 0 {
		t.Fatalf("Router kept the deleted platform's IP load stats: %v", snap)
	}

	removed := 0
	for _, e := range events {
		if e.Type == routing.LeaseRemove && e.PlatformID == platformRow.ID {
			removed++
		}
	}
	if removed != len(accounts) {
		t.Fatalf("LeaseRemove events = %d, want %d (events=%v)", removed, len(accounts), events)
	}
}
