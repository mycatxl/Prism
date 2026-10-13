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
	// The subscription window, not the plan's own status field, decides the
	// expiry; the status carried is the subscription's.
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

// A user with no active subscription gets no plan, which means no restriction
// beyond the key's own. Carrying a suspended subscription would silently grant
// what the operator suspended.
func TestLoadFromStateIgnoresInactiveSubscriptions(t *testing.T) {
	reader := fakeStateReader{
		keys:  []model.AccessKey{testKey("k-1", "u-1")},
		users: []model.User{testUser("u-1")},
		subscriptions: []model.UserSubscription{{
			ID:               "sub-1",
			UserID:           "u-1",
			PlanSnapshotJSON: planSnapshotJSON(t, model.Plan{Platforms: []string{"plat-a"}}),
			Status:           model.SubscriptionStatusSuspended,
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
	if state.Plan.Status != "" {
		t.Fatalf("a suspended subscription was carried: %+v", state.Plan)
	}
	if !state.AllowsPlatform("any-platform") {
		t.Fatal("a key with no plan must be unrestricted by it")
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
