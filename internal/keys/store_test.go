package keys

import (
	"net/netip"
	"sync"
	"testing"
	"time"

	"prism/internal/model"
)

func testUser(id string) model.User {
	return model.User{
		ID:       id,
		Username: id,
		Role:     model.UserRoleUser,
		Status:   model.UserStatusActive,
	}
}

func testKey(id, userID string) model.AccessKey {
	return model.AccessKey{
		ID:         id,
		UserID:     userID,
		SecretHash: []byte{0x01, 0x02},
		Scope:      model.KeyScopeProxy,
		Status:     model.KeyStatusActive,
	}
}

func TestStore_LoadAndGet(t *testing.T) {
	store := NewStore()
	users := map[string]model.User{"u-1": testUser("u-1")}
	plans := map[string]PlanSnapshot{
		"u-1": {
			Platforms:    []string{"plat-a"},
			TrafficBytes: 1000,
			Status:       model.SubscriptionStatusActive,
			ExpiresAtNs:  time.Now().Add(time.Hour).UnixNano(),
		},
	}

	if err := store.Load([]model.AccessKey{testKey("k-1", "u-1")}, users, plans); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if store.Len() != 1 {
		t.Fatalf("store holds %d keys, want 1", store.Len())
	}

	state, ok := store.Get("k-1")
	if !ok {
		t.Fatal("key k-1 is not in the store")
	}
	if state.UserID != "u-1" {
		t.Errorf("user id: got %q, want %q", state.UserID, "u-1")
	}
	if len(state.SecretHash) != 2 || state.SecretHash[0] != 0x01 {
		t.Errorf("secret hash was not copied: %v", state.SecretHash)
	}
	if state.Plan.TrafficBytes != 1000 {
		t.Errorf("plan snapshot: got %+v", state.Plan)
	}

	if _, ok := store.Get("missing"); ok {
		t.Error("an unknown key must not resolve")
	}
}

// A key whose user is missing cannot be authorized and must not be published.
func TestStore_LoadSkipsKeysWithoutAUser(t *testing.T) {
	store := NewStore()
	users := map[string]model.User{"u-1": testUser("u-1")}

	if err := store.Load([]model.AccessKey{
		testKey("k-1", "u-1"),
		testKey("k-orphan", "u-gone"),
	}, users, nil); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if store.Len() != 1 {
		t.Fatalf("store holds %d keys, want 1 (the orphan is skipped)", store.Len())
	}
	if _, ok := store.Get("k-orphan"); ok {
		t.Error("a key without a user must not be published")
	}
}

// Load replaces the whole set: a key that is no longer in the rows disappears.
func TestStore_LoadReplacesEverything(t *testing.T) {
	store := NewStore()
	users := map[string]model.User{"u-1": testUser("u-1")}

	if err := store.Load([]model.AccessKey{
		testKey("k-1", "u-1"),
		testKey("k-2", "u-1"),
	}, users, nil); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := store.Load([]model.AccessKey{testKey("k-2", "u-1")}, users, nil); err != nil {
		t.Fatalf("Load second time: %v", err)
	}

	if store.Len() != 1 {
		t.Fatalf("store holds %d keys, want 1", store.Len())
	}
	if _, ok := store.Get("k-1"); ok {
		t.Error("a key dropped by the reload must not resolve")
	}
	if _, ok := store.Get("k-2"); !ok {
		t.Error("the surviving key must still resolve")
	}
}

// The published snapshot must not alias the slices it was built from, or a
// later edit of the source row would silently change what the data plane reads.
func TestStore_LoadCopiesSlices(t *testing.T) {
	store := NewStore()
	users := map[string]model.User{"u-1": testUser("u-1")}

	key := testKey("k-1", "u-1")
	key.Platforms = []string{"plat-a"}
	plan := PlanSnapshot{
		Platforms: []string{"plat-plan"},
		IPTiers:   []string{"residential:excellent"},
		Status:    model.SubscriptionStatusActive,
	}
	if err := store.Load([]model.AccessKey{key}, users, map[string]PlanSnapshot{"u-1": plan}); err != nil {
		t.Fatalf("Load: %v", err)
	}

	state, _ := store.Get("k-1")
	key.Platforms[0] = "mutated"
	plan.Platforms[0] = "mutated"
	plan.IPTiers[0] = "mutated"

	if state.Platforms[0] != "plat-a" {
		t.Errorf("key platforms aliased the source row: %v", state.Platforms)
	}
	if state.Plan.Platforms[0] != "plat-plan" {
		t.Errorf("plan platforms aliased the source row: %v", state.Plan.Platforms)
	}
	if state.Plan.IPTiers[0] != "residential:excellent" {
		t.Errorf("plan ip tiers aliased the source row: %v", state.Plan.IPTiers)
	}
}

func TestStore_UpsertAndDelete(t *testing.T) {
	store := NewStore()
	users := map[string]model.User{"u-1": testUser("u-1")}
	if err := store.Load([]model.AccessKey{testKey("k-1", "u-1")}, users, nil); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Upsert publishes a new key.
	store.Upsert(newKeyState(testKey("k-2", "u-1"), testUser("u-1"), PlanSnapshot{}))
	if store.Len() != 2 {
		t.Fatalf("store holds %d keys, want 2", store.Len())
	}

	// Upsert replaces an existing one.
	disabled := testKey("k-1", "u-1")
	disabled.Status = model.KeyStatusDisabled
	store.Upsert(newKeyState(disabled, testUser("u-1"), PlanSnapshot{}))
	state, _ := store.Get("k-1")
	if state.Status != model.KeyStatusDisabled {
		t.Errorf("upsert did not replace the key: %+v", state)
	}

	// Delete revokes it.
	store.Delete("k-1")
	if _, ok := store.Get("k-1"); ok {
		t.Error("a deleted key must not resolve")
	}
	if store.Len() != 1 {
		t.Errorf("store holds %d keys, want 1", store.Len())
	}

	// Deleting something that is not there is not an error.
	store.Delete("missing")
}

// --- eligibility ---

func TestKeyState_Usable(t *testing.T) {
	now := time.Now().UnixNano()
	active := testUser("u-1")

	base := func() *KeyState {
		return newKeyState(testKey("k-1", "u-1"), active, PlanSnapshot{
			Status:      model.SubscriptionStatusActive,
			ExpiresAtNs: now + int64(time.Hour),
		})
	}

	if !base().Usable(now) {
		t.Error("an active key with an active plan must be usable")
	}

	cases := []struct {
		name   string
		mutate func(*KeyState)
	}{
		{"disabled key", func(s *KeyState) { s.Status = model.KeyStatusDisabled }},
		{"expired key", func(s *KeyState) { s.ExpiresAtNs = now - 1 }},
		{"key expiring now", func(s *KeyState) { s.ExpiresAtNs = now }},
		{"disabled user", func(s *KeyState) { s.UserStatus = model.UserStatusDisabled }},
		{"suspended plan", func(s *KeyState) { s.Plan.Status = model.SubscriptionStatusSuspended }},
		{"expired plan", func(s *KeyState) { s.Plan.ExpiresAtNs = now - 1 }},
	}
	for _, tc := range cases {
		state := base()
		tc.mutate(state)
		if state.Usable(now) {
			t.Errorf("%s: must not be usable", tc.name)
		}
	}

	// A key with no expiry and no plan is usable: the zero values mean
	// "unrestricted", not "expired".
	bare := newKeyState(testKey("k-2", "u-1"), active, PlanSnapshot{})
	if !bare.Usable(now) {
		t.Error("a key without a plan or expiry must be usable")
	}

	// A nil state is never usable.
	var nilState *KeyState
	if nilState.Usable(now) {
		t.Error("a nil key state must not be usable")
	}
}

func TestKeyState_AllowsPlatform(t *testing.T) {
	// The key's own list wins over the plan's.
	withKeyList := newKeyState(testKey("k-1", "u-1"), testUser("u-1"), PlanSnapshot{
		Platforms: []string{"plat-plan"},
	})
	withKeyList.Platforms = []string{"plat-key"}

	if !withKeyList.AllowsPlatform("plat-key") {
		t.Error("the key's own platform must be allowed")
	}
	if withKeyList.AllowsPlatform("plat-plan") {
		t.Error("the plan's platform must not be allowed when the key narrows it")
	}

	// With no key list, the plan's applies.
	planOnly := newKeyState(testKey("k-2", "u-1"), testUser("u-1"), PlanSnapshot{
		Platforms: []string{"plat-plan"},
	})
	if !planOnly.AllowsPlatform("plat-plan") {
		t.Error("the plan's platform must be allowed")
	}
	if planOnly.AllowsPlatform("plat-other") {
		t.Error("an unlisted platform must be refused")
	}

	// Neither set means unrestricted.
	open := newKeyState(testKey("k-3", "u-1"), testUser("u-1"), PlanSnapshot{})
	if !open.AllowsPlatform("anything") {
		t.Error("with no restriction every platform must be allowed")
	}
	if !open.Plan.AllowsAllPlatforms() {
		t.Error("AllowsAllPlatforms must report true for an empty set")
	}

	// The effective set reflects which one applies.
	if got := withKeyList.EffectivePlatforms(); len(got) != 1 || got[0] != "plat-key" {
		t.Errorf("effective platforms: got %v, want [plat-key]", got)
	}
	if got := planOnly.EffectivePlatforms(); len(got) != 1 || got[0] != "plat-plan" {
		t.Errorf("effective platforms: got %v, want [plat-plan]", got)
	}

	var nilState *KeyState
	if nilState.AllowsPlatform("anything") {
		t.Error("a nil key state must not allow a platform")
	}
	if nilState.EffectivePlatforms() != nil {
		t.Error("a nil key state has no effective platforms")
	}
}

func TestPlanSnapshot_ExpiresAt(t *testing.T) {
	if got := (PlanSnapshot{}).ExpiresAt(); !got.IsZero() {
		t.Errorf("no expiry must be the zero time, got %v", got)
	}
	want := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	got := PlanSnapshot{ExpiresAtNs: want.UnixNano()}.ExpiresAt()
	if !got.Equal(want) {
		t.Errorf("expiry: got %v, want %v", got, want)
	}
}

// --- concurrency ---

// Readers must never see a torn store while keys are being refreshed: the
// data plane reads on every connection while the management API writes.
func TestStore_ConcurrentReadAndWrite(t *testing.T) {
	store := NewStore()
	users := map[string]model.User{"u-1": testUser("u-1")}
	if err := store.Load([]model.AccessKey{testKey("k-1", "u-1")}, users, nil); err != nil {
		t.Fatalf("Load: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if state, ok := store.Get("k-1"); ok {
					_ = state.Usable(time.Now().UnixNano())
					_ = state.AllowsPlatform("plat-a")
				}
				store.Len()
			}
		}()
	}

	// Writers.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				key := testKey("k-1", "u-1")
				key.Name = "writer"
				store.Upsert(newKeyState(key, testUser("u-1"), PlanSnapshot{}))
				store.Delete("k-tmp")
				store.Upsert(newKeyState(testKey("k-tmp", "u-1"), testUser("u-1"), PlanSnapshot{}))
			}
		}(i)
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	// A reload racing with the writers must leave a consistent store.
	if err := store.Load([]model.AccessKey{testKey("k-1", "u-1")}, users, nil); err != nil {
		t.Fatalf("Load after the race: %v", err)
	}
	if store.Len() != 1 {
		t.Errorf("store holds %d keys after a full reload, want 1", store.Len())
	}
}

func TestStore_Range(t *testing.T) {
	store := NewStore()
	users := map[string]model.User{"u-1": testUser("u-1")}
	if err := store.Load([]model.AccessKey{
		testKey("k-1", "u-1"),
		testKey("k-2", "u-1"),
		testKey("k-3", "u-1"),
	}, users, nil); err != nil {
		t.Fatalf("Load: %v", err)
	}

	seen := map[string]bool{}
	store.Range(func(state *KeyState) bool {
		seen[state.KeyID] = true
		return true
	})
	if len(seen) != 3 {
		t.Errorf("Range visited %d keys, want 3", len(seen))
	}

	// Returning false stops the walk early.
	count := 0
	store.Range(func(*KeyState) bool {
		count++
		return false
	})
	if count != 1 {
		t.Errorf("Range must stop on false, visited %d", count)
	}
}

func TestKeyState_AllowsIP(t *testing.T) {
	now := time.Now().UnixNano()
	build := func(allowlist []string) *KeyState {
		key := testKey("k-1", "u-1")
		key.IPAllowlist = allowlist
		return newKeyState(key, testUser("u-1"), PlanSnapshot{
			Status:      model.SubscriptionStatusActive,
			ExpiresAtNs: now + int64(time.Hour),
		})
	}

	open := build(nil)
	if !open.AllowsIP(netip.MustParseAddr("192.0.2.33")) {
		t.Error("a key without an allowlist must accept any address")
	}
	if !open.AllowsIP(netip.Addr{}) {
		t.Error("a key without an allowlist must not be refused for an unparsable address")
	}

	pinned := build([]string{" 10.0.0.0/8 ", "192.0.2.7", "2001:db8::/32"})
	cases := []struct {
		name string
		addr netip.Addr
		want bool
	}{
		{"inside a prefix", netip.MustParseAddr("10.9.8.7"), true},
		{"a listed address", netip.MustParseAddr("192.0.2.7"), true},
		{"inside an IPv6 prefix", netip.MustParseAddr("2001:db8::1"), true},
		{"another address", netip.MustParseAddr("192.0.2.8"), false},
		{"the zero address", netip.Addr{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pinned.AllowsIP(tc.addr); got != tc.want {
				t.Fatalf("AllowsIP(%v) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}

	// A list the service accepted but this parser cannot read must still
	// restrict: the key is closed, not opened.
	garbled := build([]string{"not-an-address"})
	if garbled.AllowsIP(netip.MustParseAddr("10.0.0.1")) {
		t.Error("an unparsable allowlist must deny rather than allow everything")
	}

	var nilState *KeyState
	if nilState.AllowsIP(netip.MustParseAddr("10.0.0.1")) {
		t.Error("a nil key state must deny")
	}
}
