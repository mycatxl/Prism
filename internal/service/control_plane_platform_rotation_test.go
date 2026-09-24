package service

import (
	"net/netip"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/platform"
	"prism/internal/state"
	"prism/internal/subscription"
	"prism/internal/testutil"
	"prism/internal/topology"
)

// WP10 §3 regression guard: the scheduled rotator reads
// scheduled_rotation_enabled / scheduled_rotation_interval / from the runtime
// pool objects only, so toRuntime must carry all three (plus
// rotation_avoid_previous_ip) instead of dropping them.
func TestPlatformConfigToRuntime_PreservesScheduledRotationFields(t *testing.T) {
	cfg := platformConfig{
		Name:                             "rotation-platform",
		StickyTTLNs:                      int64(time.Hour),
		ReverseProxyMissAction:           string(platform.ReverseProxyMissActionTreatAsEmpty),
		ReverseProxyEmptyAccountBehavior: string(platform.ReverseProxyEmptyAccountBehaviorRandom),
		AllocationPolicy:                 string(platform.AllocationPolicyBalanced),
		ScheduledRotationEnabled:         true,
		ScheduledRotationIntervalNs:      int64(15 * time.Minute),
		RotationAvoidPreviousIP:          true,
	}

	plat, err := cfg.toRuntime("plat-rotation")
	if err != nil {
		t.Fatalf("toRuntime: %v", err)
	}
	if !plat.ScheduledRotationEnabled {
		t.Fatal("runtime platform lost scheduled_rotation_enabled")
	}
	if got := plat.GetScheduledRotationInterval(); got != 15*time.Minute {
		t.Fatalf("runtime scheduled rotation interval = %v, want 15m", got)
	}
	if !plat.RotationAvoidPreviousIP {
		t.Fatal("runtime platform lost rotation_avoid_previous_ip")
	}
}

// The persisted → config → runtime path (startup and read-modify-write flows)
// must preserve the same fields.
func TestPlatformConfigFromModelToRuntime_PreservesScheduledRotationFields(t *testing.T) {
	mp := model.Platform{
		ID:                               "plat-rotation",
		Name:                             "rotation-platform",
		StickyTTLNs:                      int64(2 * time.Hour),
		RegexFilters:                     []string{"^us-"},
		RegionFilters:                    []string{"us"},
		ReverseProxyMissAction:           string(platform.ReverseProxyMissActionTreatAsEmpty),
		ReverseProxyEmptyAccountBehavior: string(platform.ReverseProxyEmptyAccountBehaviorRandom),
		AllocationPolicy:                 string(platform.AllocationPolicyBalanced),
		ScheduledRotationEnabled:         true,
		ScheduledRotationIntervalNs:      int64(45 * time.Minute),
		RotationAvoidPreviousIP:          true,
	}

	plat, err := platformConfigFromModel(mp).toRuntime(mp.ID)
	if err != nil {
		t.Fatalf("toRuntime: %v", err)
	}
	if !plat.ScheduledRotationEnabled {
		t.Fatal("runtime platform lost scheduled_rotation_enabled")
	}
	if got := plat.GetScheduledRotationInterval(); got != 45*time.Minute {
		t.Fatalf("runtime scheduled rotation interval = %v, want 45m", got)
	}
	if !plat.RotationAvoidPreviousIP {
		t.Fatal("runtime platform lost rotation_avoid_previous_ip")
	}
}

// End-to-end guard for the original bug: creating a platform with scheduled
// rotation enabled must publish a pool object the rotator can act on.
func TestCreatePlatform_PublishesScheduledRotationFieldsToPool(t *testing.T) {
	dir := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(
		filepath.Join(dir, "state"),
		filepath.Join(dir, "cache"),
	)
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	subMgr := topology.NewSubscriptionManager()
	sub := subscription.NewSubscription("sub-1", "sub", "https://example.com/sub", true, false)
	subMgr.Register(sub)
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})

	raw := []byte(`{"type":"ss","server":"1.1.1.1","port":443}`)
	hash := node.HashFromRawOptions(raw)
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"seed"}})
	entry := node.NewNodeEntry(hash, raw, time.Now(), 16)
	entry.AddSubscriptionID(sub.ID)
	entry.SetEgressIP(netip.MustParseAddr("1.2.3.4"))
	entry.LatencyTable.LoadEntry("cloudflare.com", node.DomainLatencyStats{
		Ewma:        50 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	pool.LoadNodeFromBootstrap(entry)

	runtimeCfg := &atomic.Pointer[config.RuntimeConfig]{}
	runtimeCfg.Store(config.NewDefaultRuntimeConfig())

	cp := &ControlPlaneService{
		Engine:     engine,
		Pool:       pool,
		SubMgr:     subMgr,
		RuntimeCfg: runtimeCfg,
		EnvCfg: &config.EnvConfig{
			DefaultPlatformStickyTTL:              30 * time.Minute,
			DefaultPlatformRegexFilters:           []string{},
			DefaultPlatformRegionFilters:          []string{},
			DefaultPlatformReverseProxyMissAction: "TREAT_AS_EMPTY",
			DefaultPlatformAllocationPolicy:       "BALANCED",
		},
	}

	name := "rotation-platform"
	enabled := true
	avoid := true
	interval := "1h"
	created, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:                      &name,
		ScheduledRotationEnabled:  &enabled,
		ScheduledRotationInterval: &interval,
		RotationAvoidPreviousIP:   &avoid,
	})
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}

	plat, ok := pool.GetPlatform(created.ID)
	if !ok {
		t.Fatalf("platform %s was not registered in pool", created.ID)
	}
	if !plat.ScheduledRotationEnabled {
		t.Fatal("pool platform lost scheduled_rotation_enabled (scheduled rotator would sweep nothing)")
	}
	if got := plat.GetScheduledRotationInterval(); got != time.Hour {
		t.Fatalf("pool scheduled rotation interval = %v, want 1h", got)
	}
	if !plat.RotationAvoidPreviousIP {
		t.Fatal("pool platform lost rotation_avoid_previous_ip")
	}
}
