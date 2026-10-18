package service

import (
	"testing"

	"prism/internal/model"
	"prism/internal/platform"
)

// reachablePlatforms must answer with the union of what the user's active keys
// allow, using the same rule the data plane applies in
// keys.KeyState.AllowsPlatform: a key's own list when it has one, otherwise the
// plan's, and an empty effective list allows every known platform.
func TestReachablePlatformsUnionsTheActiveKeys(t *testing.T) {
	cp := newSelfPlatformFixture(t, "alpha", "bravo", "charlie")

	plan := model.Plan{Platforms: []string{"alpha"}}
	keys := []model.AccessKey{
		{Status: model.KeyStatusActive, Platforms: []string{"bravo"}},
		{Status: model.KeyStatusActive, Platforms: []string{"charlie"}},
	}

	got, err := cp.reachablePlatforms(keys, plan)
	if err != nil {
		t.Fatalf("reachablePlatforms: %v", err)
	}
	ids := platformIDs(got)
	if len(ids) != 2 || ids[0] != "bravo" || ids[1] != "charlie" {
		t.Fatalf("expected the union [bravo charlie], got %v", ids)
	}
}

// A key with no platform list inherits the plan's, and a revoked key
// contributes nothing at all.
func TestReachablePlatformsInheritsThePlanAndSkipsRevokedKeys(t *testing.T) {
	cp := newSelfPlatformFixture(t, "alpha", "bravo")

	plan := model.Plan{Platforms: []string{"alpha"}}
	keys := []model.AccessKey{
		{Status: model.KeyStatusActive},
		{Status: "revoked", Platforms: []string{"bravo"}},
	}

	got, err := cp.reachablePlatforms(keys, plan)
	if err != nil {
		t.Fatalf("reachablePlatforms: %v", err)
	}
	ids := platformIDs(got)
	if len(ids) != 1 || ids[0] != "alpha" {
		t.Fatalf("expected only the plan's platform alpha, got %v", ids)
	}
}

// When neither the key nor the plan names a platform, every platform the
// deployment knows is reachable - not none.
func TestReachablePlatformsFallsBackToEveryKnownPlatform(t *testing.T) {
	cp := newSelfPlatformFixture(t, "alpha", "bravo")

	got, err := cp.reachablePlatforms([]model.AccessKey{{Status: model.KeyStatusActive}}, model.Plan{})
	if err != nil {
		t.Fatalf("reachablePlatforms: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected every known platform, got %+v", got)
	}
}

// A user with no active key has nothing to connect through, even when the plan
// names platforms: the keys are what carries the credentials.
func TestReachablePlatformsIsEmptyWithoutAnActiveKey(t *testing.T) {
	cp := newSelfPlatformFixture(t, "alpha")

	got, err := cp.reachablePlatforms([]model.AccessKey{{Status: "revoked"}}, model.Plan{Platforms: []string{"alpha"}})
	if err != nil {
		t.Fatalf("reachablePlatforms: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no reachable platform without an active key, got %+v", got)
	}
}

// newSelfPlatformFixture seeds platforms straight into the store: the
// reachablePlatforms rule is what these tests pin, not the create path.
func newSelfPlatformFixture(t *testing.T, names ...string) *ControlPlaneService {
	t.Helper()
	cp, _, _, _ := newTenantTestService(t)
	for _, name := range names {
		row := model.Platform{
			ID:                     name,
			Name:                   name,
			ReverseProxyMissAction: string(platform.ReverseProxyMissActionTreatAsEmpty),
			AllocationPolicy:       string(platform.AllocationPolicyBalanced),
		}
		if err := cp.Engine.UpsertPlatform(row); err != nil {
			t.Fatalf("UpsertPlatform %q: %v", name, err)
		}
	}
	return cp
}

func platformIDs(platforms []SelfPlatform) []string {
	ids := make([]string, 0, len(platforms))
	for _, platform := range platforms {
		ids = append(ids, platform.PlatformID)
	}
	return ids
}
