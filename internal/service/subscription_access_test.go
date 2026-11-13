package service

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"prism/internal/usage"
)

func TestReactivatedSubscriptionRestoresDurableAllowance(t *testing.T) {
	cp, store, _, _ := newTenantTestService(t)
	cp.Usage = usage.NewCounters()
	now := time.Now().UnixNano()
	plan, err := cp.CreatePlan(PlanInput{Name: "reactivation", TrafficBytes: 100, DurationDays: 30, Status: "active"}, now)
	if err != nil {
		t.Fatal(err)
	}
	user, err := cp.CreateTenantUser(TenantUserInput{Username: "reactivation", Password: uuid.NewString(), Role: "user", PlanID: plan.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := cp.CreateAccessKey(user.ID, AccessKeyInput{Name: "client"}, now)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := usage.NewJournal(cp.Engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	gate := &usage.Gate{Limiter: usage.NewLimiter(cp.Usage), Journal: journal, Keys: store}
	reserve := func(want int64) func(int64) {
		t.Helper()
		live, ok := store.Get(key.ID)
		if !ok {
			t.Fatal("key missing")
		}
		got, settle := gate.ReserveTraffic(live, "", false, 100)
		if got != want {
			t.Fatalf("reserved %d, want %d", got, want)
		}
		return settle
	}
	reserve(100)(60)
	lateA := reserve(40)
	second, err := cp.AssignSubscription(user.ID, plan.ID, 30, now+1)
	if err != nil {
		t.Fatal(err)
	}
	lateB := reserve(100)
	if _, err = cp.UpdateTenantSubscription(user.Subscription.ID, []byte(`{"status":"active"}`), now+2); err != nil {
		t.Fatal(err)
	}
	reserve(0)(0) // A's original 60 bytes and 40-byte in-flight reservation remain charged.
	lateA(10)
	lateB(20) // B's completion must not debit the restored A period.
	reserve(30)(30)
	reserve(0)(0)
	for id, want := range map[string]int64{user.Subscription.ID: 100, second.ID: 20} {
		sub, err := cp.Engine.GetUserSubscription(id)
		if err != nil {
			t.Fatal(err)
		}
		if sub.PeriodUsedBytes != want {
			t.Fatalf("persisted usage %d, want %d", sub.PeriodUsedBytes, want)
		}
	}
}

func TestSelfOverviewOffersPlanPlatformsBeforeFirstKey(t *testing.T) {
	cp := newSelfPlatformFixture(t, "alpha", "bravo", "outside")
	now := time.Now().UnixNano()
	plan, err := cp.CreatePlan(PlanInput{Name: "platform-options", Platforms: []string{"alpha", "bravo"}, DurationDays: 30, Status: "active"}, now)
	if err != nil {
		t.Fatal(err)
	}
	user, err := cp.CreateTenantUser(TenantUserInput{Username: "platform-options", Password: uuid.NewString(), Role: "user", PlanID: plan.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	check := func(wantReachable []string) {
		t.Helper()
		view, err := cp.SelfOverview(user.ID, now)
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			KeyPlatforms []SelfPlatform `json:"key_platforms"`
		}
		if err = json.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		if got := platformIDs(wire.KeyPlatforms); !reflect.DeepEqual(got, []string{"alpha", "bravo"}) {
			t.Fatalf("creation options=%v", got)
		}
		if got := platformIDs(view.Platforms); !reflect.DeepEqual(got, wantReachable) {
			t.Fatalf("reachable=%v want=%v", got, wantReachable)
		}
	}
	check([]string{})
	if _, _, err = cp.CreateAccessKey(user.ID, AccessKeyInput{Name: "alpha-only", Platforms: []string{"alpha"}}, now); err != nil {
		t.Fatal(err)
	}
	check([]string{"alpha"})
}

func TestKeyCreationRejectsExpiredUnsweptSubscription(t *testing.T) {
	cp, _, _, now := newTenantTestService(t)
	plan, err := cp.CreatePlan(PlanInput{Name: "unswept", DurationDays: 1, Status: "active"}, now)
	if err != nil {
		t.Fatal(err)
	}
	user, err := cp.CreateTenantUser(TenantUserInput{Username: "unswept", Password: uuid.NewString(), Role: "user", PlanID: plan.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = cp.CreateAccessKey(user.ID, AccessKeyInput{Name: "expired"}, user.Subscription.ExpiresAtNs)
	_ = requireServiceError(t, err, "CONFLICT")
}
