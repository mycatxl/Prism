package keys

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"prism/internal/model"
)

// fakeStateReader is a stand-in for the state repository.
type fakeStateReader struct {
	keys          []model.AccessKey
	users         []model.User
	subscriptions []model.UserSubscription
	err           error
}

func (f fakeStateReader) ListActiveAccessKeys() ([]model.AccessKey, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.keys, nil
}

func (f fakeStateReader) ListUsers() ([]model.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.users, nil
}

func (f fakeStateReader) ListUserSubscriptions() ([]model.UserSubscription, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.subscriptions, nil
}

func planSnapshotJSON(t *testing.T, plan model.Plan) string {
	t.Helper()
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	return string(raw)
}

func TestLoadFromStatePublishesKeys(t *testing.T) {
	reader := fakeStateReader{
		keys:  []model.AccessKey{testKey("k-1", "u-1")},
		users: []model.User{testUser("u-1")},
	}

	store, err := LoadFromState(reader, nil)
	if err != nil {
		t.Fatalf("LoadFromState: %v", err)
	}
	if store.Len() != 1 {
		t.Fatalf("store holds %d keys, want 1", store.Len())
	}
	if _, ok := store.Get("k-1"); !ok {
		t.Fatal("key k-1 is not published")
	}
}

// The plan comes from the subscription's stored snapshot, not from the plans
// table: that snapshot is what the quota was computed from when the
// subscription was opened, and it stays readable after the plan is edited.
func TestLoadFromStateCarriesThePlanSnapshot(t *testing.T) {
	expiresAt := time.Now().Add(24 * time.Hour).UnixNano()
	reader := fakeStateReader{
		keys:  []model.AccessKey{testKey("k-1", "u-1")},
		users: []model.User{testUser("u-1")},
		subscriptions: []model.UserSubscription{{
			ID:     "sub-1",
			UserID: "u-1",
			PlanSnapshotJSON: planSnapshotJSON(t, model.Plan{
				Platforms:       []string{"plat-a"},
				IPTiers:         []string{"residential"},
				TrafficBytes:    1 << 30,
				MaxConcurrent:   8,
				MaxRPS:          50,
				MaxBandwidthBps: 100 << 20,
				Status:          model.SubscriptionStatusActive,
			}),
			ExpiresAtNs: expiresAt,
			Status:      model.SubscriptionStatusActive,
		}},
	}

	store, err := LoadFromState(reader, nil)
	if err != nil {
		t.Fatalf("LoadFromState: %v", err)
	}
	state, ok := store.Get("k-1")
	if !ok {
		t.Fatal("key k-1 is not published")
	}
	plan := state.Plan
	if len(plan.Platforms) != 1 || plan.Platforms[0] != "plat-a" {
		t.Fatalf("plan platforms = %v", plan.Platforms)
	}
	if len(plan.IPTiers) != 1 || plan.IPTiers[0] != "residential" {
		t.Fatalf("plan ip tiers = %v", plan.IPTiers)
	}
	if plan.TrafficBytes != 1<<30 || plan.MaxConcurrent != 8 || plan.MaxRPS != 50 || plan.MaxBandwidthBps != 100<<20 {
		t.Fatalf("plan quotas = %+v", plan)
	}
	if plan.ExpiresAtNs != expiresAt {
		t.Fatalf("plan expiry = %d, want %d", plan.ExpiresAtNs, expiresAt)
	}
	// The status is the plan's own when the subscription is active: the
	// snapshot is the plan as it was sold, and a plan the operator has since
	// disabled stays disabled through a restart.
	if plan.Status != model.SubscriptionStatusActive {
		t.Fatalf("plan status = %q", plan.Status)
	}
	if !state.AllowsPlatform("plat-a") {
		t.Fatal("the key does not allow the platform its plan names")
	}
	if state.AllowsPlatform("plat-b") {
		t.Fatal("the key allows a platform its plan does not name")
	}
}

// A user whose subscription is not active must not be left with no plan: the
// zero snapshot means "no restriction at all", which is the opposite of what a
// suspension does. The row is carried with its status and the key is refused.
// The same store holds an active user, so the test proves the refusal is about
// the subscription and not about the load.
func TestLoadFromStateRefusesAKeyWithoutAnActiveSubscription(t *testing.T) {
	activeExpiry := time.Now().Add(24 * time.Hour).UnixNano()
	suspendedExpiry := time.Now().Add(48 * time.Hour).UnixNano()
	reader := fakeStateReader{
		keys: []model.AccessKey{
			testKey("k-active", "u-active"),
			testKey("k-suspended", "u-suspended"),
			testKey("k-expired", "u-expired"),
			testKey("k-noplan", "u-noplan"),
		},
		users: []model.User{
			testUser("u-active"), testUser("u-suspended"),
			testUser("u-expired"), testUser("u-noplan"),
		},
		subscriptions: []model.UserSubscription{
			{
				ID:               "sub-active",
				UserID:           "u-active",
				PlanSnapshotJSON: planSnapshotJSON(t, model.Plan{Platforms: []string{"plat-a"}, Status: model.SubscriptionStatusActive}),
				ExpiresAtNs:      activeExpiry,
				Status:           model.SubscriptionStatusActive,
			},
			{
				ID:               "sub-suspended",
				UserID:           "u-suspended",
				PlanSnapshotJSON: planSnapshotJSON(t, model.Plan{Platforms: []string{"plat-a"}, Status: model.SubscriptionStatusActive}),
				ExpiresAtNs:      suspendedExpiry,
				Status:           model.SubscriptionStatusSuspended,
			},
			{
				ID:               "sub-expired",
				UserID:           "u-expired",
				PlanSnapshotJSON: planSnapshotJSON(t, model.Plan{Platforms: []string{"plat-a"}, Status: model.SubscriptionStatusActive}),
				ExpiresAtNs:      activeExpiry,
				Status:           model.SubscriptionStatusExpired,
			},
		},
	}

	store, err := LoadFromState(reader, nil)
	if err != nil {
		t.Fatalf("LoadFromState: %v", err)
	}

	now := time.Now().UnixNano()
	active, ok := store.Get("k-active")
	if !ok {
		t.Fatal("the active user's key is not published")
	}
	if !active.Usable(now) {
		t.Fatalf("an active subscription must leave the key usable: %+v", active.Plan)
	}
	if active.Plan.Status != model.SubscriptionStatusActive {
		t.Fatalf("plan status = %q, want %q", active.Plan.Status, model.SubscriptionStatusActive)
	}

	for _, keyID := range []string{"k-suspended", "k-expired"} {
		state, ok := store.Get(keyID)
		if !ok {
			t.Fatalf("key %s is not published: the key itself is fine, only its subscription is not", keyID)
		}
		if state.Usable(now) {
			t.Fatalf("key %s is usable although its subscription is %q: %+v", keyID, state.Plan.Status, state.Plan)
		}
		if state.Plan.Status == "" {
			t.Fatalf("key %s got the zero snapshot: a subscription that exists must never read as \"no plan\"", keyID)
		}
		if state.AllowsPlatform("any-platform") {
			t.Fatalf("key %s allows a platform no plan of it names", keyID)
		}
	}

	// A user with no subscription row at all keeps the documented zero
	// snapshot: an account from before plans existed must not lose its keys on
	// the next restart.
	noplan, ok := store.Get("k-noplan")
	if !ok {
		t.Fatal("a key of a user without subscriptions is not published")
	}
	if !noplan.Usable(now) {
		t.Fatalf("a user with no subscription row keeps the zero snapshot: %+v", noplan.Plan)
	}
}

// A subscription that is active on a plan the operator has since disabled is
// refused as well: the status is carried from the plan the subscription was
// sold under, which is what makes disabling a plan an emergency stop that
// survives a restart.
func TestLoadFromStateCarriesADisabledPlanStatus(t *testing.T) {
	reader := fakeStateReader{
		keys:  []model.AccessKey{testKey("k-1", "u-1")},
		users: []model.User{testUser("u-1")},
		subscriptions: []model.UserSubscription{{
			ID:               "sub-1",
			UserID:           "u-1",
			PlanSnapshotJSON: planSnapshotJSON(t, model.Plan{Status: "disabled"}),
			ExpiresAtNs:      time.Now().Add(time.Hour).UnixNano(),
			Status:           model.SubscriptionStatusActive,
		}},
	}

	store, err := LoadFromState(reader, nil)
	if err != nil {
		t.Fatalf("LoadFromState: %v", err)
	}
	state, ok := store.Get("k-1")
	if !ok {
		t.Fatal("key k-1 is not published")
	}
	if state.Plan.Status != "disabled" {
		t.Fatalf("plan status = %q, want the plan's own disabled status", state.Plan.Status)
	}
	if state.Usable(time.Now().UnixNano()) {
		t.Fatal("a key on a disabled plan must not be usable after a restart")
	}
}

// The newest expiry wins when a user somehow has two active rows, which is the
// reading that cannot lock a user out of a subscription they just renewed.
func TestLoadFromStateKeepsTheLaterActiveSubscription(t *testing.T) {
	older := time.Now().Add(1 * time.Hour).UnixNano()
	newer := time.Now().Add(48 * time.Hour).UnixNano()

	reader := fakeStateReader{
		keys:  []model.AccessKey{testKey("k-1", "u-1")},
		users: []model.User{testUser("u-1")},
		subscriptions: []model.UserSubscription{
			{
				ID: "sub-old", UserID: "u-1",
				PlanSnapshotJSON: planSnapshotJSON(t, model.Plan{MaxRPS: 10}),
				ExpiresAtNs:      older,
				Status:           model.SubscriptionStatusActive,
			},
			{
				ID: "sub-new", UserID: "u-1",
				PlanSnapshotJSON: planSnapshotJSON(t, model.Plan{MaxRPS: 99}),
				ExpiresAtNs:      newer,
				Status:           model.SubscriptionStatusActive,
			},
		},
	}

	store, err := LoadFromState(reader, nil)
	if err != nil {
		t.Fatalf("LoadFromState: %v", err)
	}
	state, _ := store.Get("k-1")
	if state.Plan.MaxRPS != 99 {
		t.Fatalf("plan MaxRPS = %d, want 99 (the later subscription)", state.Plan.MaxRPS)
	}
	if state.Plan.ExpiresAtNs != newer {
		t.Fatalf("plan expiry = %d, want %d", state.Plan.ExpiresAtNs, newer)
	}
}

// An active subscription decides over an ended one even when the ended row
// expires later: a user who has just been renewed must not stay refused because
// the row that was suspended carries a longer window.
func TestLoadFromStateLetsTheActiveSubscriptionDecide(t *testing.T) {
	activeExpiry := time.Now().Add(24 * time.Hour).UnixNano()
	suspendedExpiry := time.Now().Add(240 * time.Hour).UnixNano()

	reader := fakeStateReader{
		keys:  []model.AccessKey{testKey("k-1", "u-1")},
		users: []model.User{testUser("u-1")},
		subscriptions: []model.UserSubscription{
			{
				ID: "sub-suspended", UserID: "u-1",
				PlanSnapshotJSON: planSnapshotJSON(t, model.Plan{MaxRPS: 10}),
				ExpiresAtNs:      suspendedExpiry,
				Status:           model.SubscriptionStatusSuspended,
			},
			{
				ID: "sub-active", UserID: "u-1",
				PlanSnapshotJSON: planSnapshotJSON(t, model.Plan{MaxRPS: 99}),
				ExpiresAtNs:      activeExpiry,
				Status:           model.SubscriptionStatusActive,
			},
		},
	}

	store, err := LoadFromState(reader, nil)
	if err != nil {
		t.Fatalf("LoadFromState: %v", err)
	}
	state, ok := store.Get("k-1")
	if !ok {
		t.Fatal("key k-1 is not published")
	}
	if state.Plan.MaxRPS != 99 || state.Plan.ExpiresAtNs != activeExpiry {
		t.Fatalf("plan = %+v, want the active subscription's plan and window", state.Plan)
	}
	if !state.Usable(time.Now().UnixNano()) {
		t.Fatalf("the active subscription must leave the key usable: %+v", state.Plan)
	}
}

// An unreadable snapshot must not silently become an unlimited plan: the
// subscription is skipped and reported instead.
func TestLoadFromStateSkipsAnUnreadableSnapshot(t *testing.T) {
	var logged []string
	logf := func(format string, args ...any) {
		logged = append(logged, format)
	}

	reader := fakeStateReader{
		keys:  []model.AccessKey{testKey("k-1", "u-1")},
		users: []model.User{testUser("u-1")},
		subscriptions: []model.UserSubscription{{
			ID: "sub-broken", UserID: "u-1",
			PlanSnapshotJSON: "{not json",
			ExpiresAtNs:      time.Now().Add(time.Hour).UnixNano(),
			Status:           model.SubscriptionStatusActive,
		}},
	}

	store, err := LoadFromState(reader, logf)
	if err != nil {
		t.Fatalf("LoadFromState: %v", err)
	}
	state, ok := store.Get("k-1")
	if !ok {
		t.Fatal("the key must still be published: it is not the broken subscription")
	}
	if state.Plan.Status != "" {
		t.Fatalf("a broken snapshot was carried: %+v", state.Plan)
	}
	if len(logged) == 0 {
		t.Fatal("the skipped subscription was not reported")
	}
}

// A subscription row with no snapshot still grants its window: the operator
// created a subscription, and refusing it would lock the user out.
func TestLoadFromStateAcceptsASubscriptionWithoutASnapshot(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour).UnixNano()
	reader := fakeStateReader{
		keys:  []model.AccessKey{testKey("k-1", "u-1")},
		users: []model.User{testUser("u-1")},
		subscriptions: []model.UserSubscription{{
			ID: "sub-1", UserID: "u-1",
			ExpiresAtNs: expiresAt,
			Status:      model.SubscriptionStatusActive,
		}},
	}

	store, err := LoadFromState(reader, nil)
	if err != nil {
		t.Fatalf("LoadFromState: %v", err)
	}
	state, _ := store.Get("k-1")
	if state.Plan.ExpiresAtNs != expiresAt {
		t.Fatalf("plan expiry = %d, want %d", state.Plan.ExpiresAtNs, expiresAt)
	}
	if state.Plan.TrafficBytes != 0 {
		t.Fatalf("an absent snapshot must leave the quota unknown (zero), got %d", state.Plan.TrafficBytes)
	}
}

func TestLoadFromStateReportsReadFailures(t *testing.T) {
	if _, err := LoadFromState(nil, nil); err == nil {
		t.Fatal("a nil reader must be reported")
	}
	if _, err := LoadFromState(fakeStateReader{err: errors.New("boom")}, nil); err == nil {
		t.Fatal("a read failure must be reported")
	}
}

func TestLogSummaryReportsTheCount(t *testing.T) {
	var got string
	LogSummary(&Store{keys: nil}, func(format string, args ...any) {
		got = format
	})
	if got == "" {
		t.Fatal("a nil-map store must still be reported")
	}

	var nilStoreReported bool
	LogSummary(nil, func(format string, args ...any) {
		nilStoreReported = true
	})
	if !nilStoreReported {
		t.Fatal("a nil store must be reported")
	}
}
