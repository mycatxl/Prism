package proxy

import (
	"testing"
	"time"

	"prism/internal/node"
	"prism/internal/platform"
	"prism/internal/routing"
)

type renamedPlatformPool struct {
	routing.PoolAccessor
	lookups int
	other   *platform.Platform
}

func (p *renamedPlatformPool) GetPlatformByName(name string) (*platform.Platform, bool) {
	p.lookups++
	if p.lookups > 1 {
		return p.other, true
	}
	return p.PoolAccessor.GetPlatformByName(name)
}

func TestRoutedPlatformAuthorizationSurvivesNameReassignment(t *testing.T) {
	env := newProxyE2EEnv(t)
	other := platform.NewPlatform("forbidden", "other", nil, nil)
	other.StickyTTLNs = int64(time.Hour)
	env.pool.RegisterPlatform(other)
	env.pool.NotifyNodeDirty(node.HashFromRawOptions([]byte(`{"type":"stub","server":"127.0.0.1","server_port":1}`)))
	pool := &renamedPlatformPool{PoolAccessor: env.pool, other: other}
	router := routing.NewRouter(routing.RouterConfig{
		Pool:        pool,
		Authorities: func() []string { return []string{"example.com"} },
		P2CWindow:   func() time.Duration { return 10 * time.Minute },
	})
	routed, err := resolveRoutedOutbound(router, env.pool, "plat", "shared", "example.com", platformRestrictedKey("plat-id"))
	if err != nil {
		t.Fatalf("authorized route failed: %v", err)
	}
	if routed.Route.PlatformID != "plat-id" {
		t.Fatalf("authorized plat-id but routed %q", routed.Route.PlatformID)
	}
	router.RangeLeases(other.ID, func(userID, account string, _ routing.Lease) bool {
		t.Errorf("forbidden platform acquired lease for %s/%s", userID, account)
		return true
	})
}
