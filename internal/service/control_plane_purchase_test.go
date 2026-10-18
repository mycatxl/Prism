package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/tron"
)

// Buying, renewing and the renewal switch.

// newPurchaseTestUser creates a user and returns its id.
func newPurchaseTestUser(t *testing.T, cp *ControlPlaneService, nowNs int64) string {
	t.Helper()
	account, err := cp.CreateAccount("buyer", "buyer-password-1234", model.UserRoleUser, nowNs)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return account.ID
}

func creditUser(t *testing.T, cp *ControlPlaneService, userID string, amount int64, nowNs int64) {
	t.Helper()
	id, err := newID("bl_")
	if err != nil {
		t.Fatalf("ledger id: %v", err)
	}
	if _, err := cp.Engine.ApplyBalanceDelta(model.BalanceEntry{
		ID: id, UserID: userID, DeltaMicro: amount,
		Reason: model.BalanceReasonAdminAdjust, Note: "seed", Actor: "test",
		CreatedAtNs: nowNs,
	}); err != nil {
		t.Fatalf("credit: %v", err)
	}
}

func mustPlan(t *testing.T, cp *ControlPlaneService, in PlanInput, nowNs int64) model.Plan {
	t.Helper()
	view, err := cp.CreatePlan(in, nowNs)
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	return view.Plan
}

func pricedPlanInput(name string, priceMicro int64) PlanInput {
	return PlanInput{
		Name:         name,
		TrafficBytes: 10 * 1024 * 1024,
		Period:       "30d",
		MaxKeys:      2,
		DurationDays: 30,
		PriceMicro:   priceMicro,
	}
}

// A plan with no price is not for sale, and the difference between "free" and
// "not for sale" is the whole reason the field is checked rather than assumed.
func TestSelfPlans_ListsOnlyPricedActivePlans(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	mustPlan(t, cp, pricedPlanInput("For sale", 5_000_000), nowNs)
	mustPlan(t, cp, pricedPlanInput("Granted by hand", 0), nowNs)
	disabled := pricedPlanInput("Switched off", 7_000_000)
	disabled.Status = planStatusDisabled
	mustPlan(t, cp, disabled, nowNs)

	plans, err := cp.SelfPlans()
	if err != nil {
		t.Fatalf("SelfPlans: %v", err)
	}
	if len(plans) != 1 || plans[0].Name != "For sale" {
		t.Fatalf("only the priced active plan should be listed: got %+v", plans)
	}
	if plans[0].PriceMicro != 5_000_000 {
		t.Fatalf("price should be carried: got %d", plans[0].PriceMicro)
	}
}

// The happy path: the balance goes down by exactly the price, the subscription
// exists, and the ledger says what it was for.
func TestSelfPurchase_ChargesAndPutsThePlanInForce(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	plan := mustPlan(t, cp, pricedPlanInput("Monthly", 5_000_000), nowNs)
	creditUser(t, cp, userID, 12_000_000, nowNs)

	result, err := cp.SelfPurchase(userID, PurchaseInput{PlanID: plan.ID, RequestID: "req-1"}, nowNs)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if result.ChargedMicro != 5_000_000 {
		t.Fatalf("charged: got %d, want 5000000", result.ChargedMicro)
	}
	if result.BalanceMicro != 7_000_000 {
		t.Fatalf("balance: got %d, want 7000000", result.BalanceMicro)
	}
	if result.Replayed {
		t.Fatal("a first purchase is not a replay")
	}
	if result.Subscription.PlanID != plan.ID {
		t.Fatalf("subscription plan: got %q", result.Subscription.PlanID)
	}
	if result.Subscription.Status != model.SubscriptionStatusActive {
		t.Fatalf("subscription status: got %q", result.Subscription.Status)
	}

	ledger, err := cp.Engine.ListBalanceLedger(userID, 0)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	var purchase *model.BalanceEntry
	for i := range ledger {
		if ledger[i].Reason == model.BalanceReasonPurchase {
			purchase = &ledger[i]
		}
	}
	if purchase == nil {
		t.Fatal("a purchase row should be in the ledger")
	}
	if purchase.DeltaMicro != -5_000_000 || purchase.RefID != plan.ID || purchase.RequestID != "req-1" {
		t.Fatalf("purchase row: %+v", purchase)
	}
}

// The same request_id is answered, not charged twice. This is what a client
// that retried a timed-out request gets.
func TestSelfPurchase_ReplayChargesOnce(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	plan := mustPlan(t, cp, pricedPlanInput("Monthly", 5_000_000), nowNs)
	creditUser(t, cp, userID, 12_000_000, nowNs)

	first, err := cp.SelfPurchase(userID, PurchaseInput{PlanID: plan.ID, RequestID: "req-1"}, nowNs)
	if err != nil {
		t.Fatalf("first purchase: %v", err)
	}
	second, err := cp.SelfPurchase(userID, PurchaseInput{PlanID: plan.ID, RequestID: "req-1"}, nowNs+1)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	if !second.Replayed {
		t.Fatal("the second call should be reported as a replay")
	}
	if second.ChargedMicro != 0 {
		t.Fatalf("a replay charges nothing: got %d", second.ChargedMicro)
	}
	if second.BalanceMicro != first.BalanceMicro {
		t.Fatalf("balance moved on a replay: %d then %d", first.BalanceMicro, second.BalanceMicro)
	}

	// Only one subscription, and only one purchase row.
	subs, err := cp.Engine.ListUserSubscriptions()
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("a replay must not open a second subscription: got %d", len(subs))
	}
	ledger, err := cp.Engine.ListBalanceLedger(userID, 0)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	purchases := 0
	for _, entry := range ledger {
		if entry.Reason == model.BalanceReasonPurchase {
			purchases++
		}
	}
	if purchases != 1 {
		t.Fatalf("a replay must not write a second ledger row: got %d", purchases)
	}
}

// Buying twice with different request_ids is two purchases, and the same plan
// may be bought again: the ledger's deposit-only uniqueness must not get in the
// way of an ordinary repeat.
func TestSelfPurchase_SamePlanTwiceIsTwoCharges(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	plan := mustPlan(t, cp, pricedPlanInput("Monthly", 5_000_000), nowNs)
	creditUser(t, cp, userID, 12_000_000, nowNs)

	if _, err := cp.SelfPurchase(userID, PurchaseInput{PlanID: plan.ID, RequestID: "req-1"}, nowNs); err != nil {
		t.Fatalf("first purchase: %v", err)
	}
	second, err := cp.SelfPurchase(userID, PurchaseInput{PlanID: plan.ID, RequestID: "req-2"}, nowNs+1)
	if err != nil {
		t.Fatalf("second purchase: %v", err)
	}
	if second.Replayed || second.ChargedMicro != 5_000_000 {
		t.Fatalf("a second request_id is a second charge: %+v", second)
	}
	if second.BalanceMicro != 2_000_000 {
		t.Fatalf("balance: got %d, want 2000000", second.BalanceMicro)
	}

	// The first subscription was ended, not stacked: one active row.
	active := 0
	subs, err := cp.Engine.ListUserSubscriptions()
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	for _, sub := range subs {
		if sub.Status == model.SubscriptionStatusActive {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("exactly one subscription may be active: got %d", active)
	}
}

// A purchase the balance cannot cover is refused and changes nothing.
func TestSelfPurchase_RefusesWhenShortOfFunds(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	plan := mustPlan(t, cp, pricedPlanInput("Monthly", 5_000_000), nowNs)
	creditUser(t, cp, userID, 1_000_000, nowNs)

	_, err := cp.SelfPurchase(userID, PurchaseInput{PlanID: plan.ID, RequestID: "req-1"}, nowNs)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != "INVALID_ARGUMENT" {
		t.Fatalf("expected INVALID_ARGUMENT, got %v", err)
	}

	if balance, err := cp.Engine.GetBalance(userID); err != nil || balance != 1_000_000 {
		t.Fatalf("balance must be untouched: got %d, %v", balance, err)
	}
	subs, err := cp.Engine.ListUserSubscriptions()
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	if len(subs) != 0 {
		t.Fatalf("a refused purchase must not leave a subscription: got %d", len(subs))
	}
}

// A plan that is not for sale cannot be bought, and the refusal is the same one
// an operator's own assign would give for a disabled plan.
func TestSelfPurchase_RefusesUnpricedAndDisabledPlans(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	creditUser(t, cp, userID, 100_000_000, nowNs)

	free := mustPlan(t, cp, pricedPlanInput("Granted", 0), nowNs)
	if _, err := cp.SelfPurchase(userID, PurchaseInput{PlanID: free.ID}, nowNs); err == nil {
		t.Fatal("an unpriced plan must not be purchasable")
	}

	disabled := pricedPlanInput("Off", 5_000_000)
	disabled.Status = planStatusDisabled
	off := mustPlan(t, cp, disabled, nowNs)
	if _, err := cp.SelfPurchase(userID, PurchaseInput{PlanID: off.ID}, nowNs); err == nil {
		t.Fatal("a disabled plan must not be purchasable")
	}
}

// Renewing by hand, from the customer's own button rather than the sweep.

// buyAndRenew sets a user up on a plan and returns the subscription id.
func buyAndRenew(t *testing.T, cp *ControlPlaneService, userID string, nowNs int64, price int64) (model.Plan, string) {
	t.Helper()
	plan := mustPlan(t, cp, pricedPlanInput("Monthly", price), nowNs)
	creditUser(t, cp, userID, price*3, nowNs)
	result, err := cp.SelfPurchase(userID, PurchaseInput{PlanID: plan.ID, RequestID: "buy-1"}, nowNs)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	return plan, result.Subscription.ID
}

// The happy path: one period longer, charged exactly once, and the ledger names
// the subscription it was for.
func TestSelfRenew_ExtendsAndCharges(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	plan, subID := buyAndRenew(t, cp, userID, nowNs, 5_000_000)

	before, err := cp.Engine.GetUserSubscription(subID)
	if err != nil {
		t.Fatalf("read subscription: %v", err)
	}

	result, err := cp.SelfRenew(userID, RenewInput{SubscriptionID: subID, RequestID: "renew-1"}, nowNs)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if result.ChargedMicro != 5_000_000 {
		t.Fatalf("charged: got %d, want 5000000", result.ChargedMicro)
	}
	if result.Replayed {
		t.Fatal("a first renewal is not a replay")
	}
	// Seeded with three periods, one spent buying, one spent here.
	if result.BalanceMicro != 5_000_000 {
		t.Fatalf("balance: got %d, want 5000000", result.BalanceMicro)
	}
	if result.Subscription.ExpiresAtNs <= before.ExpiresAtNs {
		t.Fatalf("expiry should move forward: %d -> %d", before.ExpiresAtNs, result.Subscription.ExpiresAtNs)
	}
	if result.Subscription.PeriodIndex != before.PeriodIndex+1 {
		t.Fatalf("period index: got %d, want %d", result.Subscription.PeriodIndex, before.PeriodIndex+1)
	}

	ledger, err := cp.Engine.ListBalanceLedger(userID, 0)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	var renew *model.BalanceEntry
	for i := range ledger {
		if ledger[i].Reason == model.BalanceReasonRenew {
			renew = &ledger[i]
		}
	}
	if renew == nil {
		t.Fatal("a renewal row should be in the ledger")
	}
	if renew.DeltaMicro != -5_000_000 || renew.RefID != subID || renew.RequestID != "renew-1" {
		t.Fatalf("renewal row: %+v", renew)
	}
	if renew.RefID != plan.ID && renew.RefID != subID {
		t.Fatalf("renewal should name what it paid for: %+v", renew)
	}
}

// Pressing renew twice with the same key answers the first call instead of
// charging a second period. This is what a retried request gets.
func TestSelfRenew_ReplayChargesOnce(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	_, subID := buyAndRenew(t, cp, userID, nowNs, 5_000_000)

	first, err := cp.SelfRenew(userID, RenewInput{SubscriptionID: subID, RequestID: "renew-1"}, nowNs)
	if err != nil {
		t.Fatalf("first renew: %v", err)
	}
	second, err := cp.SelfRenew(userID, RenewInput{SubscriptionID: subID, RequestID: "renew-1"}, nowNs)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !second.Replayed {
		t.Fatal("the second call with the same request_id is a replay")
	}
	if second.ChargedMicro != 0 {
		t.Fatalf("a replay charges nothing: got %d", second.ChargedMicro)
	}
	if second.BalanceMicro != first.BalanceMicro {
		t.Fatalf("a replay leaves the balance alone: %d -> %d", first.BalanceMicro, second.BalanceMicro)
	}
	if second.Subscription.ExpiresAtNs != first.Subscription.ExpiresAtNs {
		t.Fatal("a replay must not extend the subscription again")
	}
}

// A different key is a second period, and that really is a second charge.
func TestSelfRenew_NewRequestIsAnotherPeriod(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	_, subID := buyAndRenew(t, cp, userID, nowNs, 5_000_000)

	first, err := cp.SelfRenew(userID, RenewInput{SubscriptionID: subID, RequestID: "renew-1"}, nowNs)
	if err != nil {
		t.Fatalf("first renew: %v", err)
	}
	second, err := cp.SelfRenew(userID, RenewInput{SubscriptionID: subID, RequestID: "renew-2"}, nowNs)
	if err != nil {
		t.Fatalf("second renew: %v", err)
	}
	if second.Replayed {
		t.Fatal("a new key is not a replay")
	}
	if second.ChargedMicro != 5_000_000 {
		t.Fatalf("charged: got %d", second.ChargedMicro)
	}
	if second.Subscription.ExpiresAtNs <= first.Subscription.ExpiresAtNs {
		t.Fatal("a second period should extend past the first")
	}
}

// Short of funds is refused, and nothing moves.
func TestSelfRenew_RefusesWhenShortOfFunds(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	_, subID := buyAndRenew(t, cp, userID, nowNs, 5_000_000)

	// Spend the seeded balance down to nothing with an operator adjustment.
	balance, err := cp.Engine.GetBalance(userID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	creditUser(t, cp, userID, -balance, nowNs)

	before, err := cp.Engine.GetUserSubscription(subID)
	if err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	_, err = cp.SelfRenew(userID, RenewInput{SubscriptionID: subID, RequestID: "renew-1"}, nowNs)
	if err == nil {
		t.Fatal("a renewal without funds must be refused")
	}
	if se, ok := err.(*ServiceError); !ok || se.Code != "INVALID_ARGUMENT" {
		t.Fatalf("expected INVALID_ARGUMENT, got %v", err)
	}
	after, err := cp.Engine.GetUserSubscription(subID)
	if err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	if after.ExpiresAtNs != before.ExpiresAtNs || after.PeriodIndex != before.PeriodIndex {
		t.Fatal("a refused renewal must leave the subscription alone")
	}
	if got, err := cp.Engine.GetBalance(userID); err != nil || got != 0 {
		t.Fatalf("a refused renewal must not move money: %d, %v", got, err)
	}
}

// Somebody else's subscription id matches nothing, so a learnt id is useless.
func TestSelfRenew_RefusesAnotherUsersSubscription(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	ownerID := newPurchaseTestUser(t, cp, nowNs)
	_, subID := buyAndRenew(t, cp, ownerID, nowNs, 5_000_000)

	intruder, err := cp.CreateAccount("intruder", "intruder-password-1234", model.UserRoleUser, nowNs)
	if err != nil {
		t.Fatalf("create intruder: %v", err)
	}
	creditUser(t, cp, intruder.ID, 20_000_000, nowNs)

	_, err = cp.SelfRenew(intruder.ID, RenewInput{SubscriptionID: subID, RequestID: "renew-1"}, nowNs)
	if err == nil {
		t.Fatal("renewing somebody else's subscription must be refused")
	}
	if se, ok := err.(*ServiceError); !ok || se.Code != "NOT_FOUND" {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}
}

// The snapshot price is the contract: a plan repriced afterwards does not
// silently reprice what was already sold.
func TestSelfRenew_ChargesTheSnapshotPrice(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	plan, subID := buyAndRenew(t, cp, userID, nowNs, 5_000_000)

	// Reprice the plan without applying it to existing subscriptions.
	patch, err := json.Marshal(map[string]any{"price_micro": 9_000_000})
	if err != nil {
		t.Fatalf("encode patch: %v", err)
	}
	if _, err := cp.UpdatePlan(plan.ID, patch, nowNs); err != nil {
		t.Fatalf("reprice: %v", err)
	}

	result, err := cp.SelfRenew(userID, RenewInput{SubscriptionID: subID, RequestID: "renew-1"}, nowNs)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if result.ChargedMicro != 5_000_000 {
		t.Fatalf("the snapshot price wins: got %d, want 5000000", result.ChargedMicro)
	}
}

// The renewal switch is the customer's, and it survives a plan edit that
// rewrites the snapshot - which is the reason it is not in the snapshot.
func TestSetAutoRenew_SurvivesApplyToExisting(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	plan := mustPlan(t, cp, pricedPlanInput("Monthly", 5_000_000), nowNs)
	creditUser(t, cp, userID, 12_000_000, nowNs)

	purchased, err := cp.SelfPurchase(userID, PurchaseInput{PlanID: plan.ID}, nowNs)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	subID := purchased.Subscription.ID

	view, err := cp.SetAutoRenew(userID, subID, true)
	if err != nil {
		t.Fatalf("set auto renew: %v", err)
	}
	if !view.AutoRenew {
		t.Fatal("the view should report the switch as on")
	}

	// An operator pushes a new plan version onto everybody on it.
	if _, err := cp.UpdatePlan(plan.ID, []byte(`{"traffic_bytes": 20000000, "apply_to_existing": true}`), nowNs+1); err != nil {
		t.Fatalf("apply to existing: %v", err)
	}

	row, err := cp.Engine.GetUserSubscription(subID)
	if err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	if !row.AutoRenew {
		t.Fatal("apply_to_existing must not flip the customer's renewal switch")
	}
	if !strings.Contains(row.PlanSnapshotJSON, "20000000") {
		t.Fatalf("the snapshot should have been rewritten: %s", row.PlanSnapshotJSON)
	}
}

// Renewing charges the snapshot price and extends by the plan's duration, and a
// second pass does not charge again because the period index moved.
func TestRenewDueSubscriptions_ChargesOncePerPeriod(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	plan := mustPlan(t, cp, pricedPlanInput("Monthly", 5_000_000), nowNs)
	creditUser(t, cp, userID, 12_000_000, nowNs)

	purchased, err := cp.SelfPurchase(userID, PurchaseInput{PlanID: plan.ID}, nowNs)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if _, err := cp.SetAutoRenew(userID, purchased.Subscription.ID, true); err != nil {
		t.Fatalf("set auto renew: %v", err)
	}

	// Not yet inside the lead time: nothing to do.
	if result, err := cp.RenewDueSubscriptions(nowNs); err != nil {
		t.Fatalf("sweep: %v", err)
	} else if result.Renewed != 0 {
		t.Fatalf("nothing should renew 30 days out: %+v", result)
	}

	// Inside the lead time: one charge, one extension.
	dueAt := purchased.Subscription.ExpiresAtNs - int64(24*time.Hour)
	result, err := cp.RenewDueSubscriptions(dueAt)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Renewed != 1 {
		t.Fatalf("one subscription should renew: %+v", result)
	}

	row, err := cp.Engine.GetUserSubscription(purchased.Subscription.ID)
	if err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	if row.PeriodIndex != 1 {
		t.Fatalf("period index: got %d, want 1", row.PeriodIndex)
	}
	if row.ExpiresAtNs <= purchased.Subscription.ExpiresAtNs {
		t.Fatalf("expiry should move forward: %d then %d", purchased.Subscription.ExpiresAtNs, row.ExpiresAtNs)
	}
	// 12 charged in, 5 spent on the purchase, 5 on the renewal.
	if balance, err := cp.Engine.GetBalance(userID); err != nil || balance != 2_000_000 {
		t.Fatalf("one renewal is one charge: got %d, %v", balance, err)
	}

	// The same moment again: the index has moved, so nothing matches.
	again, err := cp.RenewDueSubscriptions(dueAt)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if again.Renewed != 0 {
		t.Fatalf("a renewed subscription must not renew again: %+v", again)
	}
	if balance, err := cp.Engine.GetBalance(userID); err != nil || balance != 2_000_000 {
		t.Fatalf("no second charge: got %d, %v", balance, err)
	}
}

// A subscription that opted in but cannot afford the next period is reported,
// not silently skipped and not extended on credit.
func TestRenewDueSubscriptions_ReportsShortOfFunds(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	plan := mustPlan(t, cp, pricedPlanInput("Monthly", 5_000_000), nowNs)
	creditUser(t, cp, userID, 5_000_000, nowNs)

	purchased, err := cp.SelfPurchase(userID, PurchaseInput{PlanID: plan.ID}, nowNs)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if _, err := cp.SetAutoRenew(userID, purchased.Subscription.ID, true); err != nil {
		t.Fatalf("set auto renew: %v", err)
	}

	dueAt := purchased.Subscription.ExpiresAtNs - int64(time.Hour)
	result, err := cp.RenewDueSubscriptions(dueAt)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Renewed != 0 || result.ShortOfFunds != 1 {
		t.Fatalf("expected one shortfall and no renewal: %+v", result)
	}
	if balance, err := cp.Engine.GetBalance(userID); err != nil || balance != 0 {
		t.Fatalf("balance must be untouched: got %d, %v", balance, err)
	}
	row, err := cp.Engine.GetUserSubscription(purchased.Subscription.ID)
	if err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	if row.PeriodIndex != 0 {
		t.Fatalf("a refused renewal must not move the index: got %d", row.PeriodIndex)
	}
}

// A snapshot with no price in it is the pre-000024 case: the live plan's price
// applies once and is written back, so the next period is priced from the
// snapshot like any other.
func TestRenewDueSubscriptions_PricesAnUnpricedSnapshotOnce(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	plan := mustPlan(t, cp, pricedPlanInput("Monthly", 5_000_000), nowNs)
	creditUser(t, cp, userID, 12_000_000, nowNs)

	// A subscription opened the way 000017 opened them: no price in the
	// snapshot at all.
	legacy := model.Plan{
		ID: plan.ID, Name: plan.Name, TrafficBytes: plan.TrafficBytes,
		Period: plan.Period, MaxKeys: plan.MaxKeys, DurationDays: plan.DurationDays,
		Status: planStatusActive,
	}
	snapshot, err := planSnapshotJSON(legacy)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	sub := model.UserSubscription{
		ID: "us-legacy", UserID: userID, PlanID: plan.ID, PlanSnapshotJSON: snapshot,
		StartsAtNs: nowNs, ExpiresAtNs: expiryFromDays(30, nowNs),
		PeriodStartNs: nowNs, AutoRenew: true,
		Status: model.SubscriptionStatusActive,
	}
	if err := cp.Engine.UpsertUserSubscription(sub); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	dueAt := sub.ExpiresAtNs - int64(time.Hour)
	result, err := cp.RenewDueSubscriptions(dueAt)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Renewed != 1 {
		t.Fatalf("the legacy subscription should renew: %+v", result)
	}
	if balance, err := cp.Engine.GetBalance(userID); err != nil || balance != 7_000_000 {
		t.Fatalf("the live price should be charged: got %d, %v", balance, err)
	}

	row, err := cp.Engine.GetUserSubscription("us-legacy")
	if err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	if !strings.Contains(row.PlanSnapshotJSON, "5000000") {
		t.Fatalf("the price should have been written back: %s", row.PlanSnapshotJSON)
	}
}

// The four steps only pay for themselves when money that arrives on chain can be
// spent, and the next period is paid for out of the same balance. This walks the
// whole way through the service layer with a fake chain: a transfer credits the
// balance, the balance buys a plan, and the renewal that follows charges the same
// ledger. Anything that drifts between the three shows up as a balance that does
// not reconcile.
func TestChainDepositFundsPurchaseAndRenewal(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	userID := createDepositUser(t, cp, "chain-wallet")
	plan := mustPlan(t, cp, pricedPlanInput("Chain monthly", 5_000_000), nowNs)

	intent, err := cp.SelfDeposit(userID, 12_000_000, nowNs)
	if err != nil {
		t.Fatalf("open deposit: %v", err)
	}

	// The chain reports exactly what the page told the user to send.
	blockMs := (intent.CreatedAtNs + 1_000_000_000) / 1_000_000
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":[{"transaction_id":"tx-funding","block_timestamp":%d,`+
			`"from":"TPayer","to":%q,"type":"Transfer","value":"%d",`+
			`"token_info":{"address":%q,"decimals":6}}]}`,
			blockMs, testUSDTAddress, intent.AmountMicro, config.DefaultUSDTContract)
	}))
	t.Cleanup(srv.Close)
	cp.Tron = tron.NewClient(srv.URL, "", cp.EnvCfg.USDTContract)

	credited, err := cp.ScanDeposits(context.Background(), nowNs)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if credited != 1 {
		t.Fatalf("credited: got %d, want 1", credited)
	}
	balance, err := cp.Engine.GetBalance(userID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != intent.AmountMicro {
		t.Fatalf("balance after deposit: got %d, want %d", balance, intent.AmountMicro)
	}

	// The balance buys the plan.
	bought, err := cp.SelfPurchase(userID, PurchaseInput{PlanID: plan.ID, RequestID: "req-chain-1"}, nowNs)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if bought.ChargedMicro != plan.PriceMicro {
		t.Fatalf("charged: got %d, want %d", bought.ChargedMicro, plan.PriceMicro)
	}
	if bought.BalanceMicro != intent.AmountMicro-plan.PriceMicro {
		t.Fatalf("balance after purchase: got %d, want %d", bought.BalanceMicro, intent.AmountMicro-plan.PriceMicro)
	}

	// Opting in, then letting the period run out, charges the next one.
	if _, err := cp.SetAutoRenew(userID, bought.Subscription.ID, true); err != nil {
		t.Fatalf("auto renew on: %v", err)
	}
	dueAt := bought.Subscription.ExpiresAtNs + int64(time.Second)
	renewed, err := cp.RenewDueSubscriptions(dueAt)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if renewed.Renewed != 1 {
		t.Fatalf("renewed: got %d, want 1 (%+v)", renewed.Renewed, renewed)
	}

	finalBalance, err := cp.Engine.GetBalance(userID)
	if err != nil {
		t.Fatalf("final balance: %v", err)
	}
	if want := intent.AmountMicro - 2*plan.PriceMicro; finalBalance != want {
		t.Fatalf("final balance: got %d, want %d", finalBalance, want)
	}

	// Every micro-unit that moved is accounted for in the ledger.
	ledger, err := cp.Engine.ListBalanceLedger(userID, 0)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	var sum int64
	reasons := map[string]int{}
	for _, entry := range ledger {
		sum += entry.DeltaMicro
		reasons[entry.Reason]++
	}
	if sum != finalBalance {
		t.Fatalf("ledger sum: got %d, balance %d", sum, finalBalance)
	}
	if reasons[model.BalanceReasonDeposit] != 1 {
		t.Fatalf("deposit rows: got %d, want 1 (%v)", reasons[model.BalanceReasonDeposit], reasons)
	}
	if reasons[model.BalanceReasonPurchase] != 1 {
		t.Fatalf("purchase rows: got %d, want 1 (%v)", reasons[model.BalanceReasonPurchase], reasons)
	}
	if reasons[model.BalanceReasonRenew] != 1 {
		t.Fatalf("renewal rows: got %d, want 1 (%v)", reasons[model.BalanceReasonRenew], reasons)
	}
}

// A renewal buys a fresh allowance. Before this was fixed, the row kept the
// period it had already spent: the expiry moved forward and the customer stayed
// refused, because nothing except the rollover worker ever zeroed the counter.
func TestSelfRenew_StartsAFreshAllowance(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	_, subID := buyAndRenew(t, cp, userID, nowNs, 5_000_000)

	before, err := cp.Engine.GetUserSubscription(subID)
	if err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	// Spend part of the allowance on disk, the way the flush worker records it.
	if err := cp.Engine.AddSubscriptionUsedBytes(subID, 999_999); err != nil {
		t.Fatalf("seed usage: %v", err)
	}

	if _, err := cp.SelfRenew(userID, RenewInput{SubscriptionID: subID, RequestID: "fresh-1"}, nowNs); err != nil {
		t.Fatalf("renew: %v", err)
	}

	after, err := cp.Engine.GetUserSubscription(subID)
	if err != nil {
		t.Fatalf("read subscription after renew: %v", err)
	}
	if after.PeriodUsedBytes != 0 {
		t.Fatalf("period usage after renewal: got %d, want 0", after.PeriodUsedBytes)
	}
	// The new window starts where the old one would have ended, so renewing
	// early costs the customer nothing.
	if after.PeriodStartNs != before.ExpiresAtNs {
		t.Fatalf("period start: got %d, want the old expiry %d", after.PeriodStartNs, before.ExpiresAtNs)
	}
}

// Renewing before the period ends must not move the window backwards, and the
// running aggregate has to follow the disk or the quota keeps refusing.
func TestSelfRenew_EarlyRenewalKeepsTheRemainingTime(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	plan, subID := buyAndRenew(t, cp, userID, nowNs, 5_000_000)

	before, err := cp.Engine.GetUserSubscription(subID)
	if err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	// Renew a day into a 30-day period, well before it ends.
	earlyNs := nowNs + int64(24*time.Hour)
	if _, err := cp.SelfRenew(userID, RenewInput{SubscriptionID: subID, RequestID: "early-1"}, earlyNs); err != nil {
		t.Fatalf("renew: %v", err)
	}
	after, err := cp.Engine.GetUserSubscription(subID)
	if err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	// The old expiry already covered a full period, so one more period lands
	// exactly one period past it.
	want := expiryFromDays(plan.DurationDays, before.ExpiresAtNs)
	if after.ExpiresAtNs != want {
		t.Fatalf("expiry: got %d, want %d", after.ExpiresAtNs, want)
	}
	if after.PeriodStartNs != before.ExpiresAtNs {
		t.Fatalf("period start: got %d, want the old expiry %d", after.PeriodStartNs, before.ExpiresAtNs)
	}
}

// A plan whose period is shorter than the renewal lead time is inside the
// window the moment it is bought, so a sweep that compared only the expiry with
// the lead bought the same period again on every tick. The window is capped by
// the period being bought, which is what this pins: one purchase, one renewal,
// whatever the sweep interval is.
func TestRenewDueSubscriptions_DoesNotRebuyAPlanShorterThanTheLead(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	userID := newPurchaseTestUser(t, cp, nowNs)
	in := pricedPlanInput("Daily", 2_000_000)
	in.Period = "1d"
	in.DurationDays = 1
	plan := mustPlan(t, cp, in, nowNs)
	creditUser(t, cp, userID, 10_000_000, nowNs)

	purchased, err := cp.SelfPurchase(userID, PurchaseInput{PlanID: plan.ID}, nowNs)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if _, err := cp.SetAutoRenew(userID, purchased.Subscription.ID, true); err != nil {
		t.Fatalf("set auto renew: %v", err)
	}
	if remaining := purchased.Subscription.ExpiresAtNs - nowNs; remaining >= renewalLeadTimeNs {
		t.Fatalf("a one-day plan has to start inside the lead time for this test: %d", remaining)
	}

	// Three passes, a minute apart, exactly what StartRenewalSweeper does.
	renewed := 0
	for i := 0; i < 3; i++ {
		result, err := cp.RenewDueSubscriptions(nowNs + int64(i)*int64(time.Minute))
		if err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
		renewed += result.Renewed
	}
	if renewed != 1 {
		t.Fatalf("a short plan must renew once, not once per tick: %d", renewed)
	}
	row, err := cp.Engine.GetUserSubscription(purchased.Subscription.ID)
	if err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	if row.PeriodIndex != 1 {
		t.Fatalf("period index: got %d, want 1", row.PeriodIndex)
	}
	// 10 in, 2 for the purchase, 2 for the one renewal.
	if balance, err := cp.Engine.GetBalance(userID); err != nil || balance != 6_000_000 {
		t.Fatalf("a short plan must be charged once: got %d, %v", balance, err)
	}
}

// The window is the lead time unless the period being bought is shorter.
func TestRenewalWindowNs_CapsAtThePeriod(t *testing.T) {
	day := int64(24 * time.Hour)
	if got := renewalWindowNs(renewalLeadTimeNs, 30); got != renewalLeadTimeNs {
		t.Fatalf("a monthly plan keeps the lead time: got %d", got)
	}
	if got := renewalWindowNs(renewalLeadTimeNs, 7); got != renewalLeadTimeNs {
		t.Fatalf("a seven-day plan is exactly the lead time: got %d", got)
	}
	if got := renewalWindowNs(renewalLeadTimeNs, 1); got != day {
		t.Fatalf("a one-day plan caps the window at its own period: got %d, want %d", got, day)
	}
	if got := renewalWindowNs(renewalLeadTimeNs, 0); got != renewalLeadTimeNs {
		t.Fatalf("a duration of zero keeps the lead time: got %d", got)
	}
}
