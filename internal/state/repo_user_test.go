package state

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"prism/internal/model"
)

func mustInsertUser(t *testing.T, repo *StateRepo, id, username string) model.User {
	t.Helper()
	now := time.Now().UnixNano()
	user := model.User{
		ID:           id,
		Username:     username,
		PasswordHash: "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$hash",
		Role:         model.UserRoleUser,
		Status:       model.UserStatusActive,
		CreatedAtNs:  now,
		UpdatedAtNs:  now,
	}
	if err := repo.InsertUser(user); err != nil {
		t.Fatalf("InsertUser(%s): %v", username, err)
	}
	return user
}

// --- users ---

func TestStateRepo_UserRoundTrip(t *testing.T) {
	repo := newTestStateRepo(t)
	inserted := mustInsertUser(t, repo, "u-1", "alice")

	got, err := repo.GetUser("u-1")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Username != "alice" {
		t.Errorf("username: got %q, want %q", got.Username, "alice")
	}
	if got.Role != model.UserRoleUser {
		t.Errorf("role: got %q, want %q", got.Role, model.UserRoleUser)
	}
	if got.PasswordHash != inserted.PasswordHash {
		t.Errorf("password hash did not round-trip")
	}

	byName, err := repo.GetUserByName("alice")
	if err != nil {
		t.Fatalf("GetUserByName: %v", err)
	}
	if byName.ID != "u-1" {
		t.Errorf("GetUserByName id: got %q, want %q", byName.ID, "u-1")
	}

	if _, err := repo.GetUser("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetUser on a missing row: got %v, want ErrNotFound", err)
	}
	if _, err := repo.GetUserByName("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetUserByName on a missing row: got %v, want ErrNotFound", err)
	}
}

func TestStateRepo_UserDuplicateUsernameIsConflict(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-1", "alice")

	now := time.Now().UnixNano()
	err := repo.InsertUser(model.User{
		ID: "u-2", Username: "alice", PasswordHash: "x",
		Role: model.UserRoleUser, Status: model.UserStatusActive,
		CreatedAtNs: now, UpdatedAtNs: now,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate username: got %v, want ErrConflict", err)
	}
}

func TestStateRepo_ListAndCountUsers(t *testing.T) {
	repo := newTestStateRepo(t)

	count, err := repo.CountUsers()
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if count != 0 {
		t.Fatalf("empty database: got %d users, want 0", count)
	}

	mustInsertUser(t, repo, "u-2", "bob")
	mustInsertUser(t, repo, "u-1", "alice")

	users, err := repo.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("got %d users, want 2", len(users))
	}
	if users[0].Username != "alice" || users[1].Username != "bob" {
		t.Errorf("users must be ordered by username, got %q then %q", users[0].Username, users[1].Username)
	}

	count, err = repo.CountUsers()
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if count != 2 {
		t.Errorf("got %d users, want 2", count)
	}
}

func TestStateRepo_DeleteUserCascadesButKeepsUsage(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-1", "alice")
	now := time.Now().UnixNano()

	if err := repo.UpsertPlan(model.Plan{
		ID: "p-1", Name: "Starter", CreatedAtNs: now, UpdatedAtNs: now,
	}); err != nil {
		t.Fatalf("UpsertPlan: %v", err)
	}
	if err := repo.UpsertUserSubscription(model.UserSubscription{
		ID: "s-1", UserID: "u-1", PlanID: "p-1", StartsAtNs: now,
		ExpiresAtNs: now + int64(30*24*time.Hour), PeriodStartNs: now,
		Status: model.SubscriptionStatusActive,
	}); err != nil {
		t.Fatalf("UpsertUserSubscription: %v", err)
	}
	if err := repo.InsertAccessKey(model.AccessKey{
		ID: "k-1", UserID: "u-1", SecretHash: []byte{1, 2, 3},
		Scope: model.KeyScopeProxy, Status: model.KeyStatusActive, CreatedAtNs: now,
	}); err != nil {
		t.Fatalf("InsertAccessKey: %v", err)
	}
	if err := repo.InsertSession(model.Session{
		IDHash: "h-1", UserID: "u-1", CreatedAtNs: now,
		ExpiresAtNs: now + int64(time.Hour),
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	if err := repo.UpsertUsageHourly([]model.UsageRow{{
		KeyID: "k-1", UserID: "u-1", PlatformID: "plat-a",
		TSNs: now, IngressBytes: 10, EgressBytes: 20,
	}}); err != nil {
		t.Fatalf("UpsertUsageHourly: %v", err)
	}

	if err := repo.DeleteUser("u-1"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if err := repo.DeleteUser("u-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: got %v, want ErrNotFound", err)
	}

	// Everything keyed by the user cascades.
	if _, err := repo.GetUserSubscription("s-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("subscription must cascade: got %v, want ErrNotFound", err)
	}
	if _, err := repo.GetAccessKey("k-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("access key must cascade: got %v, want ErrNotFound", err)
	}
	if _, err := repo.GetSession("h-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("session must cascade: got %v, want ErrNotFound", err)
	}

	// The plan itself survives, and usage has no foreign key on purpose.
	if _, err := repo.GetPlan("p-1"); err != nil {
		t.Errorf("the plan must survive a user deletion: %v", err)
	}
	usage, err := repo.ListUsageByUser("u-1", 0, now+1)
	if err != nil {
		t.Fatalf("ListUsageByUser: %v", err)
	}
	if len(usage) != 1 || usage[0].EgressBytes != 20 {
		t.Errorf("usage must outlive the user: got %+v", usage)
	}
}

// --- plans ---

func TestStateRepo_PlanRoundTrip(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	plan := model.Plan{
		ID:              "p-1",
		Name:            "Starter",
		Platforms:       []string{"plat-a", "plat-b"},
		IPTiers:         []string{"residential:excellent", "mobile:clean"},
		TrafficBytes:    10 << 30,
		Period:          "30d",
		MaxConcurrent:   50,
		MaxRPS:          20,
		MaxBandwidthBps: 1 << 20,
		MaxKeys:         3,
		DurationDays:    30,
		Status:          "active",
		CreatedAtNs:     now,
		UpdatedAtNs:     now,
	}
	if err := repo.UpsertPlan(plan); err != nil {
		t.Fatalf("UpsertPlan: %v", err)
	}

	got, err := repo.GetPlan("p-1")
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Name != "Starter" {
		t.Errorf("name: got %q, want %q", got.Name, "Starter")
	}
	if len(got.Platforms) != 2 || got.Platforms[0] != "plat-a" || got.Platforms[1] != "plat-b" {
		t.Errorf("platforms did not round-trip: %v", got.Platforms)
	}
	if len(got.IPTiers) != 2 || got.IPTiers[1] != "mobile:clean" {
		t.Errorf("ip tiers did not round-trip: %v", got.IPTiers)
	}
	if got.TrafficBytes != 10<<30 {
		t.Errorf("traffic_bytes: got %d, want %d", got.TrafficBytes, int64(10<<30))
	}
	if got.MaxBandwidthBps != 1<<20 {
		t.Errorf("max_bandwidth_bps: got %d, want %d", got.MaxBandwidthBps, int64(1<<20))
	}
	if got.MaxKeys != 3 || got.DurationDays != 30 {
		t.Errorf("quota fields did not round-trip: %+v", got)
	}

	// Update in place, keeping the id.
	plan.Name = "Starter Plus"
	plan.MaxKeys = 5
	plan.UpdatedAtNs = time.Now().UnixNano()
	if err := repo.UpsertPlan(plan); err != nil {
		t.Fatalf("UpsertPlan update: %v", err)
	}
	got, err = repo.GetPlan("p-1")
	if err != nil {
		t.Fatalf("GetPlan after update: %v", err)
	}
	if got.Name != "Starter Plus" || got.MaxKeys != 5 {
		t.Errorf("update did not apply: %+v", got)
	}

	if _, err := repo.GetPlan("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetPlan on a missing row: got %v, want ErrNotFound", err)
	}
}

func TestStateRepo_PlanNameIsUnique(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	base := model.Plan{ID: "p-1", Name: "Shared", CreatedAtNs: now, UpdatedAtNs: now}
	if err := repo.UpsertPlan(base); err != nil {
		t.Fatalf("UpsertPlan: %v", err)
	}

	other := model.Plan{ID: "p-2", Name: "Shared", CreatedAtNs: now, UpdatedAtNs: now}
	if err := repo.UpsertPlan(other); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate plan name: got %v, want ErrConflict", err)
	}
}

// An unset platform list must not become the empty-string JSON value, and an
// empty one must be stored as an empty array.
func TestStateRepo_PlanEmptyAndUnsetSlices(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	if err := repo.UpsertPlan(model.Plan{
		ID: "p-unset", Name: "Unset", CreatedAtNs: now, UpdatedAtNs: now,
	}); err != nil {
		t.Fatalf("UpsertPlan with nil slices: %v", err)
	}
	got, err := repo.GetPlan("p-unset")
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Platforms == nil || len(got.Platforms) != 0 {
		t.Errorf("nil platforms must round-trip as empty, got %#v", got.Platforms)
	}

	if err := repo.UpsertPlan(model.Plan{
		ID: "p-empty", Name: "Empty", Platforms: []string{}, IPTiers: []string{},
		CreatedAtNs: now, UpdatedAtNs: now,
	}); err != nil {
		t.Fatalf("UpsertPlan with empty slices: %v", err)
	}
	got, err = repo.GetPlan("p-empty")
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Platforms == nil || len(got.Platforms) != 0 {
		t.Errorf("empty platforms must round-trip as empty, got %#v", got.Platforms)
	}
}

func TestStateRepo_ListPlansOrdersByName(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	plans, err := repo.ListPlans()
	if err != nil {
		t.Fatalf("ListPlans on an empty table: %v", err)
	}
	if len(plans) != 0 {
		t.Fatalf("empty table: got %d plans, want 0", len(plans))
	}

	for _, p := range []model.Plan{
		{ID: "p-2", Name: "Standard", CreatedAtNs: now, UpdatedAtNs: now},
		{ID: "p-1", Name: "Basic", CreatedAtNs: now, UpdatedAtNs: now},
	} {
		if err := repo.UpsertPlan(p); err != nil {
			t.Fatalf("UpsertPlan(%s): %v", p.ID, err)
		}
	}

	plans, err = repo.ListPlans()
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("got %d plans, want 2", len(plans))
	}
	if plans[0].ID != "p-1" || plans[1].ID != "p-2" {
		t.Errorf("plans must be ordered by name, got %q then %q", plans[0].Name, plans[1].Name)
	}
}

func TestStateRepo_DeletePlan(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	if err := repo.UpsertPlan(model.Plan{ID: "p-1", Name: "Gone", CreatedAtNs: now, UpdatedAtNs: now}); err != nil {
		t.Fatalf("UpsertPlan: %v", err)
	}
	if err := repo.DeletePlan("p-1"); err != nil {
		t.Fatalf("DeletePlan: %v", err)
	}
	if err := repo.DeletePlan("p-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice: got %v, want ErrNotFound", err)
	}
}

// --- user subscriptions ---

func TestStateRepo_UserSubscriptionRoundTrip(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-1", "alice")
	now := time.Now().UnixNano()

	// plan_id carries a foreign key, so the plan has to exist first.
	if err := repo.UpsertPlan(model.Plan{
		ID: "p-1", Name: "Starter", CreatedAtNs: now, UpdatedAtNs: now,
	}); err != nil {
		t.Fatalf("UpsertPlan: %v", err)
	}

	sub := model.UserSubscription{
		ID:               "s-1",
		UserID:           "u-1",
		PlanID:           "p-1",
		PlanSnapshotJSON: `{"traffic_bytes":100}`,
		StartsAtNs:       now,
		ExpiresAtNs:      now + int64(30*24*time.Hour),
		PeriodStartNs:    now,
		PeriodUsedBytes:  0,
		Status:           model.SubscriptionStatusActive,
	}
	if err := repo.UpsertUserSubscription(sub); err != nil {
		t.Fatalf("UpsertUserSubscription: %v", err)
	}

	got, err := repo.GetUserSubscription("s-1")
	if err != nil {
		t.Fatalf("GetUserSubscription: %v", err)
	}
	if got.PlanID != "p-1" || got.PlanSnapshotJSON != `{"traffic_bytes":100}` {
		t.Errorf("subscription did not round-trip: %+v", got)
	}

	active, err := repo.GetActiveUserSubscription("u-1")
	if err != nil {
		t.Fatalf("GetActiveUserSubscription: %v", err)
	}
	if active.ID != "s-1" {
		t.Errorf("active subscription id: got %q, want %q", active.ID, "s-1")
	}

	// A plan-less subscription is valid: plan_id may be cleared by a plan
	// deletion while the snapshot keeps the terms.
	sub.PlanID = ""
	sub.ID = "s-2"
	sub.Status = model.SubscriptionStatusSuspended
	if err := repo.UpsertUserSubscription(sub); err != nil {
		t.Fatalf("UpsertUserSubscription without a plan: %v", err)
	}
	got, err = repo.GetUserSubscription("s-2")
	if err != nil {
		t.Fatalf("GetUserSubscription: %v", err)
	}
	if got.PlanID != "" {
		t.Errorf("plan_id must stay empty, got %q", got.PlanID)
	}
	if got.PlanSnapshotJSON != `{"traffic_bytes":100}` {
		t.Errorf("snapshot must survive a cleared plan_id, got %q", got.PlanSnapshotJSON)
	}

	// Only the active one is returned for the user.
	if _, err := repo.GetActiveUserSubscription("nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetActiveUserSubscription for an unknown user: got %v, want ErrNotFound", err)
	}
}

func TestStateRepo_ListUserSubscriptions(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-2", "bob")
	mustInsertUser(t, repo, "u-1", "alice")
	now := time.Now().UnixNano()

	subs, err := repo.ListUserSubscriptions()
	if err != nil {
		t.Fatalf("ListUserSubscriptions on an empty table: %v", err)
	}
	if len(subs) != 0 {
		t.Fatalf("empty table: got %d subscriptions, want 0", len(subs))
	}

	for _, s := range []model.UserSubscription{
		{ID: "s-b", UserID: "u-2", StartsAtNs: now, ExpiresAtNs: now + 1,
			PeriodStartNs: now, Status: model.SubscriptionStatusActive},
		{ID: "s-a", UserID: "u-1", StartsAtNs: now, ExpiresAtNs: now + 1,
			PeriodStartNs: now, Status: model.SubscriptionStatusSuspended},
	} {
		if err := repo.UpsertUserSubscription(s); err != nil {
			t.Fatalf("UpsertUserSubscription(%s): %v", s.ID, err)
		}
	}

	subs, err = repo.ListUserSubscriptions()
	if err != nil {
		t.Fatalf("ListUserSubscriptions: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("got %d subscriptions, want 2", len(subs))
	}
	if subs[0].UserID != "u-1" || subs[1].UserID != "u-2" {
		t.Errorf("subscriptions must be ordered by user, got %q then %q", subs[0].UserID, subs[1].UserID)
	}
	// Every status is listed, not only the active ones.
	if subs[0].Status != model.SubscriptionStatusSuspended {
		t.Errorf("status: got %q, want %q", subs[0].Status, model.SubscriptionStatusSuspended)
	}
}

func TestStateRepo_AddSubscriptionUsedBytes(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-1", "alice")
	now := time.Now().UnixNano()

	if err := repo.UpsertUserSubscription(model.UserSubscription{
		ID: "s-1", UserID: "u-1", StartsAtNs: now, ExpiresAtNs: now + 1,
		PeriodStartNs: now, Status: model.SubscriptionStatusActive,
	}); err != nil {
		t.Fatalf("UpsertUserSubscription: %v", err)
	}

	if err := repo.AddSubscriptionUsedBytes("s-1", 500); err != nil {
		t.Fatalf("AddSubscriptionUsedBytes: %v", err)
	}
	if err := repo.AddSubscriptionUsedBytes("s-1", 250); err != nil {
		t.Fatalf("AddSubscriptionUsedBytes: %v", err)
	}

	got, err := repo.GetUserSubscription("s-1")
	if err != nil {
		t.Fatalf("GetUserSubscription: %v", err)
	}
	if got.PeriodUsedBytes != 750 {
		t.Errorf("period_used_bytes: got %d, want 750", got.PeriodUsedBytes)
	}

	// The counter never goes negative.
	if err := repo.AddSubscriptionUsedBytes("s-1", -10000); err != nil {
		t.Fatalf("AddSubscriptionUsedBytes negative: %v", err)
	}
	got, err = repo.GetUserSubscription("s-1")
	if err != nil {
		t.Fatalf("GetUserSubscription: %v", err)
	}
	if got.PeriodUsedBytes != 0 {
		t.Errorf("period_used_bytes must clamp at zero, got %d", got.PeriodUsedBytes)
	}

	if err := repo.AddSubscriptionUsedBytes("missing", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("AddSubscriptionUsedBytes on a missing row: got %v, want ErrNotFound", err)
	}
}

// --- access keys ---

func TestStateRepo_AccessKeyRoundTrip(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-1", "alice")
	now := time.Now().UnixNano()

	key := model.AccessKey{
		ID:          "k-1",
		UserID:      "u-1",
		Name:        "laptop",
		SecretHash:  []byte{0x01, 0x02, 0x03, 0x04},
		Platforms:   []string{"plat-a"},
		IPAllowlist: []string{"203.0.113.0/24"},
		Scope:       model.KeyScopeProxy,
		Status:      model.KeyStatusActive,
		CreatedAtNs: now,
	}
	if err := repo.InsertAccessKey(key); err != nil {
		t.Fatalf("InsertAccessKey: %v", err)
	}

	got, err := repo.GetAccessKey("k-1")
	if err != nil {
		t.Fatalf("GetAccessKey: %v", err)
	}
	if got.Name != "laptop" || got.UserID != "u-1" {
		t.Errorf("key did not round-trip: %+v", got)
	}
	if len(got.SecretHash) != 4 || got.SecretHash[0] != 0x01 {
		t.Errorf("secret hash did not round-trip: %v", got.SecretHash)
	}
	if len(got.Platforms) != 1 || got.Platforms[0] != "plat-a" {
		t.Errorf("platforms did not round-trip: %v", got.Platforms)
	}
	if len(got.IPAllowlist) != 1 || got.IPAllowlist[0] != "203.0.113.0/24" {
		t.Errorf("ip allowlist did not round-trip: %v", got.IPAllowlist)
	}

	// A key with neither restriction set must come back unset, which is what
	// "the plan decides" reads as.
	unrestricted := model.AccessKey{
		ID: "k-2", UserID: "u-1", SecretHash: []byte{0x09}, CreatedAtNs: now,
	}
	if err := repo.InsertAccessKey(unrestricted); err != nil {
		t.Fatalf("InsertAccessKey unrestricted: %v", err)
	}
	got, err = repo.GetAccessKey("k-2")
	if err != nil {
		t.Fatalf("GetAccessKey: %v", err)
	}
	if got.Platforms != nil {
		t.Errorf("unset platforms must stay nil, got %#v", got.Platforms)
	}
	if got.IPAllowlist != nil {
		t.Errorf("unset ip allowlist must stay nil, got %#v", got.IPAllowlist)
	}
	if got.Scope != model.KeyScopeProxy || got.Status != model.KeyStatusActive {
		t.Errorf("defaults must apply: scope=%q status=%q", got.Scope, got.Status)
	}

	if _, err := repo.GetAccessKey("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAccessKey on a missing row: got %v, want ErrNotFound", err)
	}
}

// A key must never be stored without a secret hash.
func TestStateRepo_AccessKeyRequiresSecretHash(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-1", "alice")

	err := repo.InsertAccessKey(model.AccessKey{
		ID: "k-1", UserID: "u-1", CreatedAtNs: time.Now().UnixNano(),
	})
	if err == nil {
		t.Fatal("inserting a key without a secret hash must fail")
	}
}

func TestStateRepo_ListAndDeleteAccessKeys(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-1", "alice")
	mustInsertUser(t, repo, "u-2", "bob")
	now := time.Now().UnixNano()

	for _, key := range []model.AccessKey{
		{ID: "k-1", UserID: "u-1", SecretHash: []byte{1}, CreatedAtNs: now},
		{ID: "k-2", UserID: "u-1", SecretHash: []byte{2}, CreatedAtNs: now + 1},
		{ID: "k-3", UserID: "u-2", SecretHash: []byte{3}, CreatedAtNs: now + 2,
			Status: model.KeyStatusDisabled},
	} {
		if err := repo.InsertAccessKey(key); err != nil {
			t.Fatalf("InsertAccessKey(%s): %v", key.ID, err)
		}
	}

	mine, err := repo.ListAccessKeysByUser("u-1")
	if err != nil {
		t.Fatalf("ListAccessKeysByUser: %v", err)
	}
	if len(mine) != 2 {
		t.Fatalf("alice has %d keys, want 2", len(mine))
	}
	if mine[0].ID != "k-1" || mine[1].ID != "k-2" {
		t.Errorf("keys must be ordered by creation time, got %q then %q", mine[0].ID, mine[1].ID)
	}

	active, err := repo.ListActiveAccessKeys()
	if err != nil {
		t.Fatalf("ListActiveAccessKeys: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("got %d active keys, want 2 (the disabled one is excluded)", len(active))
	}
	for _, key := range active {
		if key.Status != model.KeyStatusActive {
			t.Errorf("ListActiveAccessKeys returned a %q key", key.Status)
		}
	}

	if err := repo.DeleteAccessKey("k-1"); err != nil {
		t.Fatalf("DeleteAccessKey: %v", err)
	}
	if err := repo.DeleteAccessKey("k-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice: got %v, want ErrNotFound", err)
	}
}

func TestStateRepo_UpdateAccessKeyRotatesSecret(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-1", "alice")
	now := time.Now().UnixNano()

	key := model.AccessKey{
		ID: "k-1", UserID: "u-1", SecretHash: []byte{0xAA}, CreatedAtNs: now,
	}
	if err := repo.InsertAccessKey(key); err != nil {
		t.Fatalf("InsertAccessKey: %v", err)
	}

	key.SecretHash = []byte{0xBB, 0xCC}
	key.Status = model.KeyStatusDisabled
	key.LastUsedNs = now + 1
	if err := repo.UpdateAccessKey(key); err != nil {
		t.Fatalf("UpdateAccessKey: %v", err)
	}

	got, err := repo.GetAccessKey("k-1")
	if err != nil {
		t.Fatalf("GetAccessKey: %v", err)
	}
	if len(got.SecretHash) != 2 || got.SecretHash[0] != 0xBB {
		t.Errorf("secret hash was not rotated: %v", got.SecretHash)
	}
	if got.Status != model.KeyStatusDisabled {
		t.Errorf("status: got %q, want %q", got.Status, model.KeyStatusDisabled)
	}
	if got.LastUsedNs != now+1 {
		t.Errorf("last_used_ns: got %d, want %d", got.LastUsedNs, now+1)
	}

	key.ID = "missing"
	if err := repo.UpdateAccessKey(key); !errors.Is(err, ErrNotFound) {
		t.Errorf("updating a missing row: got %v, want ErrNotFound", err)
	}
}

// --- usage ---

func TestStateRepo_UpsertUsageHourlySums(t *testing.T) {
	repo := newTestStateRepo(t)
	const hour = int64(time.Hour)

	first := []model.UsageRow{
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: hour,
			IngressBytes: 100, EgressBytes: 200, Requests: 1},
	}
	if err := repo.UpsertUsageHourly(first); err != nil {
		t.Fatalf("UpsertUsageHourly: %v", err)
	}
	// A second flush for the same hour must add, not replace.
	second := []model.UsageRow{
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: hour,
			IngressBytes: 50, EgressBytes: 60, Requests: 2, Errors: 1},
	}
	if err := repo.UpsertUsageHourly(second); err != nil {
		t.Fatalf("UpsertUsageHourly: %v", err)
	}

	var ingress, egress, requests, errs int64
	if err := repo.db.QueryRow(`
		SELECT ingress_bytes, egress_bytes, requests, errors FROM usage_hourly
		WHERE key_id = 'k-1' AND platform_id = 'p-1' AND hour_ts_ns = ?
	`, hour).Scan(&ingress, &egress, &requests, &errs); err != nil {
		t.Fatalf("read usage row: %v", err)
	}
	if ingress != 150 || egress != 260 {
		t.Errorf("bytes must be summed: ingress=%d egress=%d, want 150/260", ingress, egress)
	}
	if requests != 3 || errs != 1 {
		t.Errorf("counters must be summed: requests=%d errors=%d, want 3/1", requests, errs)
	}

	// An empty batch is a no-op, not an error: the flush path calls it on a
	// timer.
	if err := repo.UpsertUsageHourly(nil); err != nil {
		t.Errorf("empty batch must be a no-op, got %v", err)
	}
}

func TestStateRepo_ListUsageByUserReadsBothTables(t *testing.T) {
	repo := newTestStateRepo(t)
	const day = int64(24 * time.Hour)

	// Day 0 is summarized (daily), day 1 is still recent (hourly), day 2 is out
	// of the requested window.
	if err := repo.UpsertUsageHourly([]model.UsageRow{
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: 0,
			IngressBytes: 10, EgressBytes: 1, Requests: 1, Errors: 1},
		{KeyID: "k-2", UserID: "u-2", PlatformID: "p-1", TSNs: 0, IngressBytes: 99},
	}); err != nil {
		t.Fatalf("UpsertUsageHourly: %v", err)
	}
	if _, err := repo.SummarizeUsageHourlyBefore(day); err != nil {
		t.Fatalf("SummarizeUsageHourlyBefore: %v", err)
	}
	if err := repo.UpsertUsageHourly([]model.UsageRow{
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: day, IngressBytes: 20, EgressBytes: 2, Requests: 2},
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: day + int64(time.Hour), IngressBytes: 5, EgressBytes: 3, Requests: 3},
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-2", TSNs: day, IngressBytes: 7, EgressBytes: 4, Requests: 4},
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: 2 * day, IngressBytes: 1000},
	}); err != nil {
		t.Fatalf("UpsertUsageHourly: %v", err)
	}

	// The window covers days 0 and 1 only, and another user's row must not leak.
	got, err := repo.ListUsageByUser("u-1", 0, 2*day)
	if err != nil {
		t.Fatalf("ListUsageByUser: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3 (one per key/platform/day)", len(got))
	}

	type bucket struct {
		key, platform string
		day           int64
	}
	byBucket := map[bucket]model.UsageRow{}
	for _, row := range got {
		byBucket[bucket{row.KeyID, row.PlatformID, row.TSNs}] = row
	}
	if row := byBucket[bucket{"k-1", "p-1", 0}]; row.IngressBytes != 10 || row.Requests != 1 || row.Errors != 1 {
		t.Errorf("summarized day 0 must come back from usage_daily: %+v", row)
	}
	if row := byBucket[bucket{"k-1", "p-1", day}]; row.IngressBytes != 25 || row.EgressBytes != 5 || row.Requests != 5 {
		t.Errorf("day 1 must sum its two hourly rows: %+v", row)
	}
	if row := byBucket[bucket{"k-1", "p-2", day}]; row.IngressBytes != 7 {
		t.Errorf("day 1 must stay split per platform: %+v", row)
	}

	// Newest first, and the out-of-window hour is excluded.
	if got[0].TSNs != day {
		t.Errorf("rows must be newest first, got day %d", got[0].TSNs)
	}
	for _, row := range got {
		if row.UserID != "u-1" {
			t.Errorf("another user's usage leaked into the result: %+v", row)
		}
		if row.TSNs >= 2*day {
			t.Errorf("a row outside the window was returned: %+v", row)
		}
	}
}

// --- sessions ---

func TestStateRepo_SessionRoundTrip(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-1", "alice")
	now := time.Now().UnixNano()

	session := model.Session{
		IDHash: "hash-1", UserID: "u-1", CreatedAtNs: now,
		ExpiresAtNs: now + int64(12*time.Hour), IP: "203.0.113.7", UA: "Mozilla/5.0",
	}
	if err := repo.InsertSession(session); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	got, err := repo.GetSession("hash-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.UserID != "u-1" || got.IP != "203.0.113.7" || got.UA != "Mozilla/5.0" {
		t.Errorf("session did not round-trip: %+v", got)
	}

	if err := repo.DeleteSession("hash-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := repo.GetSession("hash-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSession after delete: got %v, want ErrNotFound", err)
	}
	if err := repo.DeleteSession("hash-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteSession twice: got %v, want ErrNotFound", err)
	}
}

func TestStateRepo_SessionCleanup(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-1", "alice")
	now := time.Now().UnixNano()

	for _, s := range []model.Session{
		{IDHash: "h-expired", UserID: "u-1", CreatedAtNs: now - 100, ExpiresAtNs: now - 1},
		{IDHash: "h-live", UserID: "u-1", CreatedAtNs: now, ExpiresAtNs: now + int64(time.Hour)},
	} {
		if err := repo.InsertSession(s); err != nil {
			t.Fatalf("InsertSession(%s): %v", s.IDHash, err)
		}
	}

	removed, err := repo.PruneSessions(now)
	if err != nil {
		t.Fatalf("PruneSessions: %v", err)
	}
	if removed != 1 {
		t.Errorf("pruned %d sessions, want 1", removed)
	}
	if _, err := repo.GetSession("h-live"); err != nil {
		t.Errorf("the live session must survive: %v", err)
	}

	// Disabling an account clears every session it holds.
	count, err := repo.DeleteSessionsByUser("u-1")
	if err != nil {
		t.Fatalf("DeleteSessionsByUser: %v", err)
	}
	if count != 1 {
		t.Errorf("deleted %d sessions, want 1", count)
	}
}

// --- usage summary in one step (000020-era fix) ---

// The summary has to be atomic, not only correct: the three-call sequence it
// replaces could leave the daily row written while the hourly rows survived, and
// the next pass would add the same traffic to the same day a second time. This
// test pins the outcome of the one-call version: the day is right after the
// first call and still right after a second one.
func TestStateRepo_SummarizeUsageHourlyBeforeIsAtomic(t *testing.T) {
	repo := newTestStateRepo(t)
	const (
		hour = int64(time.Hour)
		day  = int64(24 * time.Hour)
	)

	// Two hours of day 0 for one key, one hour of day 0 for another, one hour of
	// day 1, and one hour that is younger than the cutoff.
	if err := repo.UpsertUsageHourly([]model.UsageRow{
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: 0,
			IngressBytes: 10, EgressBytes: 1, Requests: 1, Errors: 1},
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: day - hour,
			IngressBytes: 20, EgressBytes: 2, Requests: 2},
		{KeyID: "k-2", UserID: "u-2", PlatformID: "p-1", TSNs: 0,
			IngressBytes: 5, EgressBytes: 5, Requests: 1},
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: day,
			IngressBytes: 40, EgressBytes: 4, Requests: 4},
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: day + hour,
			IngressBytes: 100, EgressBytes: 100, Requests: 10},
	}); err != nil {
		t.Fatalf("UpsertUsageHourly: %v", err)
	}

	// Everything strictly older than this is summarized; the last row stays.
	cutoff := day + hour

	removed, err := repo.SummarizeUsageHourlyBefore(cutoff)
	if err != nil {
		t.Fatalf("SummarizeUsageHourlyBefore: %v", err)
	}
	if removed != 4 {
		t.Errorf("summarized %d hourly rows, want 4", removed)
	}

	var hourlyLeft int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM usage_hourly`).Scan(&hourlyLeft); err != nil {
		t.Fatalf("count hourly rows: %v", err)
	}
	if hourlyLeft != 1 {
		t.Errorf("hourly rows left: got %d, want 1 (the one inside the window)", hourlyLeft)
	}

	readDay := func(keyID string, dayTs int64) (ingress, requests int64) {
		t.Helper()
		if err := repo.db.QueryRow(`
			SELECT ingress_bytes, requests FROM usage_daily
			WHERE key_id = ? AND platform_id = 'p-1' AND day_ts_ns = ?
		`, keyID, dayTs).Scan(&ingress, &requests); err != nil {
			t.Fatalf("read daily row %s/%d: %v", keyID, dayTs, err)
		}
		return ingress, requests
	}

	if ingress, requests := readDay("k-1", 0); ingress != 30 || requests != 3 {
		t.Errorf("k-1 day 0: ingress=%d requests=%d, want 30/3", ingress, requests)
	}
	if ingress, requests := readDay("k-2", 0); ingress != 5 || requests != 1 {
		t.Errorf("k-2 day 0: ingress=%d requests=%d, want 5/1", ingress, requests)
	}
	if ingress, requests := readDay("k-1", day); ingress != 40 || requests != 4 {
		t.Errorf("k-1 day 1: ingress=%d requests=%d, want 40/4", ingress, requests)
	}

	// A second run must find nothing to fold, and in particular must not add the
	// same traffic to the same day again: the rows it summarized are gone.
	removed, err = repo.SummarizeUsageHourlyBefore(cutoff)
	if err != nil {
		t.Fatalf("SummarizeUsageHourlyBefore (second call): %v", err)
	}
	if removed != 0 {
		t.Errorf("second run summarized %d rows, want 0", removed)
	}
	if ingress, requests := readDay("k-1", 0); ingress != 30 || requests != 3 {
		t.Errorf("k-1 day 0 after two runs: ingress=%d requests=%d, want 30/3 (no doubling)",
			ingress, requests)
	}
	if ingress, requests := readDay("k-1", day); ingress != 40 || requests != 4 {
		t.Errorf("k-1 day 1 after two runs: ingress=%d requests=%d, want 40/4", ingress, requests)
	}

	var dailyRows int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM usage_daily`).Scan(&dailyRows); err != nil {
		t.Fatalf("count daily rows: %v", err)
	}
	if dailyRows != 3 {
		t.Errorf("daily rows: got %d, want 3", dailyRows)
	}
}

// --- subscription terms without the usage counter ---

func TestStateRepo_UpdateUserSubscriptionTermsKeepsUsageCounters(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-1", "alice")

	const (
		periodStart = int64(1700000000_000000000)
		usedBytes   = int64(4096)
	)
	sub := model.UserSubscription{
		ID:               "us-1",
		UserID:           "u-1",
		PlanSnapshotJSON: `{"traffic_bytes":100}`,
		StartsAtNs:       periodStart,
		ExpiresAtNs:      periodStart + int64(30*24*time.Hour),
		PeriodStartNs:    periodStart,
		PeriodUsedBytes:  usedBytes,
		Status:           model.SubscriptionStatusActive,
	}
	if err := repo.UpsertUserSubscription(sub); err != nil {
		t.Fatalf("UpsertUserSubscription: %v", err)
	}

	terms := sub
	terms.Status = model.SubscriptionStatusSuspended
	if err := repo.UpdateUserSubscriptionTerms(terms); err != nil {
		t.Fatalf("UpdateUserSubscriptionTerms: %v", err)
	}

	got, err := repo.GetUserSubscription("us-1")
	if err != nil {
		t.Fatalf("GetUserSubscription: %v", err)
	}
	if got.Status != model.SubscriptionStatusSuspended {
		t.Errorf("status: got %q, want %q", got.Status, model.SubscriptionStatusSuspended)
	}
	if got.PeriodUsedBytes != usedBytes {
		t.Errorf("period_used_bytes: got %d, want %d", got.PeriodUsedBytes, usedBytes)
	}
	if got.PeriodStartNs != periodStart {
		t.Errorf("period_start_ns: got %d, want %d", got.PeriodStartNs, periodStart)
	}

	if err := repo.UpdateUserSubscriptionTerms(model.UserSubscription{ID: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("terms of a missing subscription: got %v, want ErrNotFound", err)
	}
}

// An operator's PATCH reads the row, the flush worker adds to the counter, and
// the PATCH writes the terms. UpsertUserSubscription would write the counter from
// the read - an admin edit would silently refund the bytes that landed in
// between - so the terms update must leave the column alone.
func TestStateRepo_UpdateUserSubscriptionTermsDoesNotClobberIncrement(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-1", "alice")

	sub := model.UserSubscription{
		ID:               "us-1",
		UserID:           "u-1",
		PlanSnapshotJSON: `{}`,
		StartsAtNs:       1,
		ExpiresAtNs:      2,
		PeriodStartNs:    1,
		PeriodUsedBytes:  100,
		Status:           model.SubscriptionStatusActive,
	}
	if err := repo.UpsertUserSubscription(sub); err != nil {
		t.Fatalf("UpsertUserSubscription: %v", err)
	}

	// What the operator read: the counter as it was before the flush landed.
	stale := sub

	if err := repo.AddSubscriptionUsedBytes("us-1", 900); err != nil {
		t.Fatalf("AddSubscriptionUsedBytes: %v", err)
	}

	stale.Status = model.SubscriptionStatusExpired
	if err := repo.UpdateUserSubscriptionTerms(stale); err != nil {
		t.Fatalf("UpdateUserSubscriptionTerms: %v", err)
	}

	got, err := repo.GetUserSubscription("us-1")
	if err != nil {
		t.Fatalf("GetUserSubscription: %v", err)
	}
	if got.PeriodUsedBytes != 1000 {
		t.Errorf("period_used_bytes: got %d, want 1000 (the increment must survive the edit)",
			got.PeriodUsedBytes)
	}
	if got.Status != model.SubscriptionStatusExpired {
		t.Errorf("status: got %q, want %q", got.Status, model.SubscriptionStatusExpired)
	}
}

// --- usage windows ---

// The alert scanner asks for the last few minutes while the rows it reads cover
// whole hours: a bucket whose hour overlaps the window has to count, even though
// its bucket start lies before the window.
func TestStateRepo_ListUsageDailyRangeCountsTheHourTheWindowIsIn(t *testing.T) {
	repo := newTestStateRepo(t)
	const (
		hour   = int64(time.Hour)
		minute = int64(time.Minute)
	)

	// A fixed clock, half past the hour: the previous hour bucket and the current
	// one are both real, and neither depends on when the test runs.
	now := time.Date(2026, 1, 2, 10, 30, 0, 0, time.UTC).UnixNano()
	thisHour := now - 30*minute
	lastHour := thisHour - hour

	if err := repo.UpsertUsageHourly([]model.UsageRow{
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: lastHour,
			IngressBytes: 10, Requests: 1},
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: thisHour,
			IngressBytes: 20, Requests: 2},
	}); err != nil {
		t.Fatalf("UpsertUsageHourly: %v", err)
	}

	// A one-hour window that starts inside the previous hour: both hours are part
	// of it, so both count.
	rows, err := repo.ListUsageDailyRange(now-60*minute, now)
	if err != nil {
		t.Fatalf("ListUsageDailyRange(one hour): %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("one-hour window returned %d rows, want 1 (both hours are in one day)", len(rows))
	}
	if rows[0].IngressBytes != 30 || rows[0].Requests != 3 {
		t.Errorf("one-hour window: ingress=%d requests=%d, want 30/3",
			rows[0].IngressBytes, rows[0].Requests)
	}

	// A window shorter than a bucket must not read as empty.
	rows, err = repo.ListUsageDailyRange(now-10*minute, now)
	if err != nil {
		t.Fatalf("ListUsageDailyRange(ten minutes): %v", err)
	}
	if len(rows) != 1 || rows[0].IngressBytes != 20 {
		t.Errorf("ten-minute window: got %+v, want the current hour bucket with 20 bytes", rows)
	}

	// The upper bound still keeps out everything at or after the window: both
	// hour buckets end before this one starts.
	rows, err = repo.ListUsageDailyRange(now+hour, now+2*hour)
	if err != nil {
		t.Fatalf("ListUsageDailyRange(after the rows): %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("window after the rows returned %+v, want nothing", rows)
	}
}

// --- batched retention deletes ---

// stateDeleteBatch is shrunk here so the loop has to run more than once: with a
// single statement, deleting five rows while the limit is two could only ever
// report two.
func TestStateRepo_DeleteUsageDailyBeforeBatches(t *testing.T) {
	repo := newTestStateRepo(t)
	original := stateDeleteBatch
	stateDeleteBatch = 2
	t.Cleanup(func() { stateDeleteBatch = original })

	const day = int64(24 * time.Hour)
	cutoff := 10 * day

	// Three days of traffic for three keys, plus one day inside the retention
	// window. The summary folds the three older days into the daily table.
	if err := repo.UpsertUsageHourly([]model.UsageRow{
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: 0, IngressBytes: 1},
		{KeyID: "k-2", UserID: "u-2", PlatformID: "p-1", TSNs: day, IngressBytes: 1},
		{KeyID: "k-3", UserID: "u-3", PlatformID: "p-1", TSNs: 2 * day, IngressBytes: 1},
		{KeyID: "k-4", UserID: "u-4", PlatformID: "p-1", TSNs: 11 * day, IngressBytes: 1},
	}); err != nil {
		t.Fatalf("UpsertUsageHourly: %v", err)
	}
	if _, err := repo.SummarizeUsageHourlyBefore(cutoff); err != nil {
		t.Fatalf("SummarizeUsageHourlyBefore: %v", err)
	}

	removed, err := repo.DeleteUsageDailyBefore(cutoff)
	if err != nil {
		t.Fatalf("DeleteUsageDailyBefore: %v", err)
	}
	if removed != 3 {
		t.Errorf("daily rows removed: got %d, want 3 (several batches)", removed)
	}

	// The day inside the window stays, and a second call has nothing left to do.
	var dailyLeft, hourlyLeft int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM usage_daily`).Scan(&dailyLeft); err != nil {
		t.Fatalf("count daily rows: %v", err)
	}
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM usage_hourly`).Scan(&hourlyLeft); err != nil {
		t.Fatalf("count hourly rows: %v", err)
	}
	if dailyLeft != 0 || hourlyLeft != 1 {
		t.Errorf("rows left: daily=%d hourly=%d, want 0/1", dailyLeft, hourlyLeft)
	}
	removed, err = repo.DeleteUsageDailyBefore(cutoff)
	if err != nil {
		t.Fatalf("DeleteUsageDailyBefore (second call): %v", err)
	}
	if removed != 0 {
		t.Errorf("second call removed %d rows, want 0", removed)
	}
}

// More rows than one batch of the production size, pruned by one call.
func TestStateRepo_DeleteUsageDailyBeforeRemovesMoreThanOneBatch(t *testing.T) {
	repo := newTestStateRepo(t)

	total := int(stateDeleteBatch) + 7
	hourly := make([]model.UsageRow, 0, total)
	for i := 0; i < total; i++ {
		hourly = append(hourly, model.UsageRow{
			KeyID: fmt.Sprintf("k-%d", i), UserID: "u-1", PlatformID: "p-1",
			TSNs: 0, IngressBytes: 1,
		})
	}
	if err := repo.UpsertUsageHourly(hourly); err != nil {
		t.Fatalf("UpsertUsageHourly: %v", err)
	}
	if _, err := repo.SummarizeUsageHourlyBefore(1); err != nil {
		t.Fatalf("SummarizeUsageHourlyBefore: %v", err)
	}

	removed, err := repo.DeleteUsageDailyBefore(int64(total))
	if err != nil {
		t.Fatalf("DeleteUsageDailyBefore: %v", err)
	}
	if int(removed) != total {
		t.Errorf("removed %d rows, want %d (more than one batch of %d)",
			removed, total, stateDeleteBatch)
	}

	var left int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM usage_daily`).Scan(&left); err != nil {
		t.Fatalf("count daily rows: %v", err)
	}
	if left != 0 {
		t.Errorf("daily rows left: got %d, want 0", left)
	}
}

// A batch that fails must not throw away what the earlier batches already
// removed. That partial progress is the reason the tail is not deleted in one
// statement: a single statement is all-or-nothing, so a failure deep inside it
// would leave every old row behind and the next pass would start over.
//
// The trigger pins the failure to the number of rows still in the table, so the
// test does not depend on timing.
func TestStateRepo_DeleteUsageDailyBeforeKeepsPartialProgressWhenABatchFails(t *testing.T) {
	repo := newTestStateRepo(t)
	original := stateDeleteBatch
	stateDeleteBatch = 2
	t.Cleanup(func() { stateDeleteBatch = original })

	const day = int64(24 * time.Hour)
	cutoff := 10 * day
	hourly := make([]model.UsageRow, 0, 5)
	for i := 0; i < 5; i++ {
		hourly = append(hourly, model.UsageRow{
			KeyID: fmt.Sprintf("k-%d", i), UserID: "u-1", PlatformID: "p-1", TSNs: 0, IngressBytes: 1,
		})
	}
	if err := repo.UpsertUsageHourly(hourly); err != nil {
		t.Fatalf("UpsertUsageHourly: %v", err)
	}
	if _, err := repo.SummarizeUsageHourlyBefore(cutoff); err != nil {
		t.Fatalf("SummarizeUsageHourlyBefore: %v", err)
	}

	// Abort a delete that would have to go below three rows: the first batch of
	// two succeeds, the second one fails.
	if _, err := repo.db.Exec(`CREATE TRIGGER test_abort_deep_batch
		BEFORE DELETE ON usage_daily
		WHEN (SELECT COUNT(*) FROM usage_daily) <= 3
		BEGIN SELECT RAISE(ABORT, 'test: batch too deep'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	removed, err := repo.DeleteUsageDailyBefore(cutoff)
	if err == nil {
		t.Fatalf("DeleteUsageDailyBefore: want the aborted batch to fail, got nil (removed %d)", removed)
	}
	if removed != 2 {
		t.Errorf("removed before the failure: got %d, want 2 (the first batch)", removed)
	}

	var left int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM usage_daily`).Scan(&left); err != nil {
		t.Fatalf("count daily rows: %v", err)
	}
	if left != 3 {
		t.Errorf("daily rows left after the failed batch: got %d, want 3 (one statement would have restored all 5)", left)
	}
}
