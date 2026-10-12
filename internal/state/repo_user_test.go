package state

import (
	"errors"
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

func TestStateRepo_UpdateUser(t *testing.T) {
	repo := newTestStateRepo(t)
	user := mustInsertUser(t, repo, "u-1", "alice")
	mustInsertUser(t, repo, "u-2", "bob")

	user.Username = "alice-renamed"
	user.Status = model.UserStatusDisabled
	user.UpdatedAtNs = time.Now().UnixNano()
	if err := repo.UpdateUser(user); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}

	got, err := repo.GetUser("u-1")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Username != "alice-renamed" {
		t.Errorf("username: got %q, want %q", got.Username, "alice-renamed")
	}
	if got.Status != model.UserStatusDisabled {
		t.Errorf("status: got %q, want %q", got.Status, model.UserStatusDisabled)
	}

	// Renaming onto another user's name is a conflict.
	user.Username = "bob"
	if err := repo.UpdateUser(user); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename onto a taken name: got %v, want ErrConflict", err)
	}

	// Updating a missing row reports not found.
	user.ID = "missing"
	user.Username = "nobody"
	if err := repo.UpdateUser(user); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update a missing row: got %v, want ErrNotFound", err)
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
	if err := repo.InsertOrder(model.Order{
		ID: "o-1", UserID: "u-1", PlanID: "p-1", CreatedAtNs: now,
	}); err != nil {
		t.Fatalf("InsertOrder: %v", err)
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
	if orders, err := repo.ListOrdersByUser("u-1"); err != nil || len(orders) != 0 {
		t.Errorf("orders must cascade: got %d rows, err %v", len(orders), err)
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

func TestStateRepo_SumUsageHourlyBeforeBucketsByDay(t *testing.T) {
	repo := newTestStateRepo(t)
	const day = int64(24 * time.Hour)

	// Two hours on the same UTC day, one on the next, one recent enough to be
	// left alone.
	rows := []model.UsageRow{
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: 0, IngressBytes: 10, Requests: 1},
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: day - int64(time.Hour),
			IngressBytes: 20, Requests: 2},
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: day + int64(time.Hour),
			IngressBytes: 40, Requests: 4},
		{KeyID: "k-2", UserID: "u-2", PlatformID: "p-1", TSNs: 0, IngressBytes: 5, Requests: 1},
	}
	if err := repo.UpsertUsageHourly(rows); err != nil {
		t.Fatalf("UpsertUsageHourly: %v", err)
	}

	// Cut off at the end of the first day: only the two hour rows of day 0
	// qualify.
	aggregated, err := repo.SumUsageHourlyBefore(day)
	if err != nil {
		t.Fatalf("SumUsageHourlyBefore: %v", err)
	}
	if len(aggregated) != 2 {
		t.Fatalf("got %d aggregated rows, want 2 (one per key on day 0)", len(aggregated))
	}

	byKey := map[string]model.UsageRow{}
	for _, row := range aggregated {
		if row.TSNs != 0 {
			t.Errorf("day bucket: got %d, want 0", row.TSNs)
		}
		byKey[row.KeyID] = row
	}
	if got := byKey["k-1"]; got.IngressBytes != 30 || got.Requests != 3 {
		t.Errorf("k-1 day 0: ingress=%d requests=%d, want 30/3", got.IngressBytes, got.Requests)
	}
	if got := byKey["k-2"]; got.IngressBytes != 5 || got.Requests != 1 {
		t.Errorf("k-2 day 0: ingress=%d requests=%d, want 5/1", got.IngressBytes, got.Requests)
	}
}

func TestStateRepo_UsageSummaryRoundTrip(t *testing.T) {
	repo := newTestStateRepo(t)
	const day = int64(24 * time.Hour)

	if err := repo.UpsertUsageHourly([]model.UsageRow{
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: 0, IngressBytes: 10, EgressBytes: 20, Requests: 1},
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: int64(time.Hour), IngressBytes: 5, EgressBytes: 6, Requests: 2},
	}); err != nil {
		t.Fatalf("UpsertUsageHourly: %v", err)
	}

	aggregated, err := repo.SumUsageHourlyBefore(day)
	if err != nil {
		t.Fatalf("SumUsageHourlyBefore: %v", err)
	}
	if err := repo.UpsertUsageDaily(aggregated); err != nil {
		t.Fatalf("UpsertUsageDaily: %v", err)
	}

	removed, err := repo.DeleteUsageHourlyBefore(day)
	if err != nil {
		t.Fatalf("DeleteUsageHourlyBefore: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed %d hourly rows, want 2", removed)
	}

	var hourlyLeft int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM usage_hourly`).Scan(&hourlyLeft); err != nil {
		t.Fatalf("count hourly: %v", err)
	}
	if hourlyLeft != 0 {
		t.Errorf("hourly rows left after the summary: %d, want 0", hourlyLeft)
	}

	// Running the summary twice must not double-count: the second run finds
	// nothing to aggregate and the daily row stays as it was.
	if err := repo.UpsertUsageDaily(aggregated); err != nil {
		t.Fatalf("UpsertUsageDaily second time: %v", err)
	}

	daily, err := repo.ListUsageByUser("u-1", 0, day)
	if err != nil {
		t.Fatalf("ListUsageByUser: %v", err)
	}
	if len(daily) != 1 {
		t.Fatalf("got %d daily rows, want 1", len(daily))
	}
	// The aggregation is summed into the existing row, so re-applying the same
	// batch doubles it. That is why the summary deletes the hourly rows in the
	// same pass; this asserts the summing semantics are deliberate.
	if daily[0].IngressBytes != 30 || daily[0].EgressBytes != 52 || daily[0].Requests != 6 {
		t.Errorf("daily row after two applies: %+v", daily[0])
	}

	removed, err = repo.DeleteUsageDailyBefore(day + 1)
	if err != nil {
		t.Fatalf("DeleteUsageDailyBefore: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed %d daily rows, want 1", removed)
	}
}

func TestStateRepo_ListUsageByUserReadsBothTables(t *testing.T) {
	repo := newTestStateRepo(t)
	const day = int64(24 * time.Hour)

	// Day 0 is summarized (daily), day 1 is still recent (hourly), day 2 is out
	// of the requested window.
	if err := repo.UpsertUsageDaily([]model.UsageRow{
		{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: 0,
			IngressBytes: 10, EgressBytes: 1, Requests: 1, Errors: 1},
		{KeyID: "k-2", UserID: "u-2", PlatformID: "p-1", TSNs: 0, IngressBytes: 99},
	}); err != nil {
		t.Fatalf("UpsertUsageDaily: %v", err)
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

// --- orders ---

func TestStateRepo_OrderRoundTrip(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-1", "alice")
	now := time.Now().UnixNano()

	// plan_id carries a foreign key, so the plan has to exist first.
	if err := repo.UpsertPlan(model.Plan{
		ID: "p-1", Name: "Starter", CreatedAtNs: now, UpdatedAtNs: now,
	}); err != nil {
		t.Fatalf("UpsertPlan: %v", err)
	}

	order := model.Order{
		ID: "o-1", UserID: "u-1", PlanID: "p-1",
		AmountMinor: 9900, Currency: "CNY", Note: "manual renewal",
		CreatedBy: "u-admin", CreatedAtNs: now,
	}
	if err := repo.InsertOrder(order); err != nil {
		t.Fatalf("InsertOrder: %v", err)
	}
	// An order without a plan is valid.
	if err := repo.InsertOrder(model.Order{
		ID: "o-2", UserID: "u-1", AmountMinor: 100, CreatedAtNs: now + 1,
	}); err != nil {
		t.Fatalf("InsertOrder without a plan: %v", err)
	}

	orders, err := repo.ListOrdersByUser("u-1")
	if err != nil {
		t.Fatalf("ListOrdersByUser: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("got %d orders, want 2", len(orders))
	}
	// Newest first.
	if orders[0].ID != "o-2" || orders[1].ID != "o-1" {
		t.Errorf("orders must be newest first, got %q then %q", orders[0].ID, orders[1].ID)
	}
	if orders[1].AmountMinor != 9900 || orders[1].Currency != "CNY" {
		t.Errorf("order did not round-trip: %+v", orders[1])
	}
	if orders[0].PlanID != "" {
		t.Errorf("an order without a plan must come back empty, got %q", orders[0].PlanID)
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
