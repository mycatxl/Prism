package service

import (
	"encoding/json"
	"testing"

	"prism/internal/model"
)

func TestCatalogLimitsMatchPurchasedSnapshot(t *testing.T) {
	cp, _, _, now := newTenantTestService(t)
	input := pricedPlanInput("Finite offer", 1000000)
	input.MaxRPS = 1
	input.MaxBandwidthBps = 1024
	input.MaxConcurrent = 2
	input.MaxKeys = 3
	plan := mustPlan(t, cp, input, now)
	offers, err := cp.SelfPlans()
	if err != nil || len(offers) != 1 {
		t.Fatal("missing offer")
	}
	user := newPurchaseTestUser(t, cp, now)
	creditUser(t, cp, user, 1000000, now)
	result, err := cp.SelfPurchase(user, PurchaseInput{PlanID: plan.ID, RequestID: "offer-contract"}, now)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := cp.Engine.GetUserSubscription(result.Subscription.ID)
	if err != nil {
		t.Fatal(err)
	}
	terms, err := planFromSnapshotJSON(sub.PlanSnapshotJSON)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(offers[0])
	if err != nil {
		t.Fatal(err)
	}
	var visible map[string]any
	if err = json.Unmarshal(encoded, &visible); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]int64{"max_rps": int64(terms.MaxRPS), "max_bandwidth_bps": terms.MaxBandwidthBps, "max_concurrent": int64(terms.MaxConcurrent), "max_keys": int64(terms.MaxKeys)} {
		if visible[name] != float64(value) {
			t.Fatalf("catalog %s does not disclose the purchased limit", name)
		}
	}
}

func TestCountryLikePlatformNameWithoutMeasurementsRemainsNameOnly(t *testing.T) {
	cp := newSelfPlatformFixture(t, "US-name-only")
	platforms, err := cp.reachablePlatforms([]model.AccessKey{{Status: model.KeyStatusActive}}, model.Plan{}, 1)
	if err != nil || len(platforms) != 1 {
		t.Fatal("platform missing")
	}
	encoded, err := json.Marshal(platforms[0])
	if err != nil {
		t.Fatal(err)
	}
	var visible map[string]any
	if err = json.Unmarshal(encoded, &visible); err != nil {
		t.Fatal(err)
	}
	countries, ok := visible["measured_countries"].([]any)
	if !ok || len(countries) != 0 {
		t.Fatal("name-only platform must explicitly report no measured countries")
	}
}
