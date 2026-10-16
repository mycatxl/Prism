package service

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"prism/internal/config"
	"prism/internal/model"
)

// Tests for the audited control-plane defects: the key allowance, the invite
// registration path, the account write mapping, the last-administrator rules,
// the usage limit, the plan emergency stop and the one-active-subscription rule.
//
// They are written against the service, not against the helpers, because every
// one of them was a rule the service stated and then did not keep.

// requireServiceError asserts that err is a ServiceError with the given code and
// returns it, so a test can look at the message too.
func requireServiceError(t *testing.T, err error, wantCode string) *ServiceError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a %s error, got nil", wantCode)
	}
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected a *ServiceError with code %s, got %T: %v", wantCode, err, err)
	}
	if svcErr.Code != wantCode {
		t.Fatalf("expected code %s, got %s: %v", wantCode, svcErr.Code, svcErr)
	}
	return svcErr
}

// createTestPlan makes an active plan with the given name.
func createTestPlan(t *testing.T, cp *ControlPlaneService, name string, nowNs int64) PlanView {
	t.Helper()
	plan, err := cp.CreatePlan(PlanInput{Name: name, DurationDays: 30, Status: "active"}, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan(%s): %v", name, err)
	}
	return plan
}

// requireNoAccount asserts that no account holds the given username.
func requireNoAccount(t *testing.T, cp *ControlPlaneService, username string) {
	t.Helper()
	users, err := cp.Engine.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	for _, user := range users {
		if user.Username == username {
			t.Fatalf("expected no account named %q, found %s", username, user.ID)
		}
	}
}

// requireInviteUsedCount asserts how many uses a code has spent.
func requireInviteUsedCount(t *testing.T, cp *ControlPlaneService, id string, want int) {
	t.Helper()
	invite, err := cp.Engine.GetInvite(id)
	if err != nil {
		t.Fatalf("GetInvite(%s): %v", id, err)
	}
	if invite.UsedCount != want {
		t.Fatalf("expected invite %s used_count %d, got %d", id, want, invite.UsedCount)
	}
}

// subscriptionSnapshot decodes the plan snapshot of one subscription.
func subscriptionSnapshot(t *testing.T, cp *ControlPlaneService, id string) model.Plan {
	t.Helper()
	row, err := cp.Engine.GetUserSubscription(id)
	if err != nil {
		t.Fatalf("GetUserSubscription(%s): %v", id, err)
	}
	plan, err := planFromSnapshotJSON(row.PlanSnapshotJSON)
	if err != nil {
		t.Fatalf("planFromSnapshotJSON(%s): %v", id, err)
	}
	return plan
}

// TestKeyAllowance_NoSubscriptionRowsRefusesNewKeys pins the rule that closed the
// unlimited hole: an account with no subscription row at all used to read as the
// zero snapshot - no quota, no expiry, no key limit - so a key minted here was a
// key with no limit at all. "No plan in force" is now a refusal to mint.
//
// The accounts that already held keys before they were on a plan are covered by
// migration 000021 (they arrive here with a legacy-unlimited row), so this test
// also asserts the message names the missing plan instead of a key limit.
func TestKeyAllowance_NoSubscriptionRowsRefusesNewKeys(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	user, err := cp.CreateTenantUser(TenantUserInput{
		Username: "no_subscription_rows",
		Password: "password-123456",
		Role:     "user",
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser: %v", err)
	}

	_, _, err = cp.CreateAccessKey(user.ID, AccessKeyInput{Name: "legacy-key"}, nowNs)
	svcErr := requireServiceError(t, err, "CONFLICT")
	if !strings.Contains(svcErr.Message, "no plan is in force") {
		t.Fatalf("expected the refusal to name the missing plan, got %q", svcErr.Message)
	}
}

// TestKeyAllowance_EndedSubscriptionRefusesNewKeys is the other half: rows exist
// and none of them is active, so a new key would either be refused by the data
// plane at once or - minted as the zero snapshot - hand out unlimited access the
// subscription no longer pays for.
func TestKeyAllowance_EndedSubscriptionRefusesNewKeys(t *testing.T) {
	cp, keyStore, _, nowNs := newTenantTestService(t)

	plan, err := cp.CreatePlan(PlanInput{Name: "Ended Plan", MaxKeys: 5, DurationDays: 30, Status: "active"}, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	user, err := cp.CreateTenantUser(TenantUserInput{
		Username:     "ended_user",
		Password:     "password-123456",
		Role:         "user",
		PlanID:       plan.ID,
		DurationDays: 30,
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser: %v", err)
	}

	first, _, err := cp.CreateAccessKey(user.ID, AccessKeyInput{Name: "while-active"}, nowNs)
	if err != nil {
		t.Fatalf("CreateAccessKey while the subscription is active: %v", err)
	}
	if state, ok := keyStore.Get(first.ID); !ok || !state.Usable(nowNs) {
		t.Fatal("expected the key to work while the subscription is active")
	}

	if _, err := cp.UpdateTenantSubscription(user.Subscription.ID, []byte(`{"status":"expired"}`), nowNs); err != nil {
		t.Fatalf("UpdateTenantSubscription(expired): %v", err)
	}

	if state, ok := keyStore.Get(first.ID); !ok || state.Usable(nowNs) {
		t.Fatal("expected the key to stop working once the subscription expired")
	}

	_, _, err = cp.CreateAccessKey(user.ID, AccessKeyInput{Name: "after-expiry"}, nowNs)
	svcErr := requireServiceError(t, err, "CONFLICT")
	if !strings.Contains(svcErr.Message, "no subscription in force") {
		t.Fatalf("expected the refusal to name the missing subscription, got %q", svcErr.Message)
	}
}

// TestKeyAllowance_ConcurrentCreationsRespectTheLimit drives the check-then-act
// pair from several goroutines at once: with max_keys=1 exactly one creation may
// win, and the loser has to be refused rather than inserted.
func TestKeyAllowance_ConcurrentCreationsRespectTheLimit(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	plan, err := cp.CreatePlan(PlanInput{Name: "Single Key Plan", MaxKeys: 1, DurationDays: 30, Status: "active"}, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	user, err := cp.CreateTenantUser(TenantUserInput{
		Username:     "racing_user",
		Password:     "password-123456",
		Role:         "user",
		PlanID:       plan.ID,
		DurationDays: 30,
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser: %v", err)
	}

	const attempts = 4
	results := make([]error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, results[i] = cp.CreateAccessKey(user.ID, AccessKeyInput{Name: fmt.Sprintf("race-%d", i)}, nowNs)
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for i, err := range results {
		if err == nil {
			succeeded++
			continue
		}
		svcErr := requireServiceError(t, err, "CONFLICT")
		if !strings.Contains(svcErr.Message, "at most 1 active keys") {
			t.Fatalf("attempt %d: expected the refusal to name the plan limit, got %q", i, svcErr.Message)
		}
	}
	if succeeded != 1 {
		t.Fatalf("expected exactly one of %d concurrent creations to succeed, got %d", attempts, succeeded)
	}

	rows, err := cp.Engine.ListAccessKeysByUser(user.ID)
	if err != nil {
		t.Fatalf("ListAccessKeysByUser: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one stored key after %d concurrent creations, got %d", attempts, len(rows))
	}
}

// TestInvite_RefusesCodesThatCouldNotGrantTheirPlan covers both halves of the
// half-finished registration: a code cannot be minted for a plan that is
// disabled, a code whose plan has since been disabled is refused before it is
// spent or an account exists, and a code that still names a plan nobody can
// resolve is refused the same way - so the retry is never a 409 about an
// account the first answer claimed had failed. A code whose plan row is simply
// deleted no longer names it (the state layer clears invites.plan_id), which
// leaves a usable code that grants nothing; that is asserted here as well.
func TestInvite_RefusesCodesThatCouldNotGrantTheirPlan(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	plan := createTestPlan(t, cp, "Invite Hardening Plan", nowNs)
	doomed := createTestPlan(t, cp, "Soon Deleted Plan", nowNs)

	codeForDisabledPlan, err := cp.CreateInvite(InviteInput{PlanID: plan.ID, MaxUses: 3}, "admin", nowNs)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	codeForDeletedPlan, err := cp.CreateInvite(InviteInput{PlanID: doomed.ID, MaxUses: 1}, "admin", nowNs)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if _, err := cp.UpdatePlan(plan.ID, []byte(`{"status":"disabled"}`), nowNs); err != nil {
		t.Fatalf("UpdatePlan(disabled): %v", err)
	}
	if err := cp.DeletePlan(doomed.ID); err != nil {
		t.Fatalf("DeletePlan: %v", err)
	}

	// 1. No new code for a plan that cannot be granted.
	_, err = cp.CreateInvite(InviteInput{PlanID: plan.ID}, "admin", nowNs)
	svcErr := requireServiceError(t, err, "INVALID_ARGUMENT")
	if !strings.Contains(svcErr.Message, "plan_id") {
		t.Fatalf("expected the refusal to name plan_id, got %q", svcErr.Message)
	}

	// 2. A code minted while the plan was on sale, used after it was disabled.
	_, err = cp.RegisterTenantUser(config.RegistrationInvite, codeForDisabledPlan.Code, "invite_disabled_plan", "password-123456", nowNs)
	_ = requireServiceError(t, err, "INVALID_ARGUMENT")
	requireNoAccount(t, cp, "invite_disabled_plan")
	requireInviteUsedCount(t, cp, codeForDisabledPlan.ID, 0)

	// 3. A code whose plan row was deleted. The state layer clears
	//    invites.plan_id when the plan goes away, so the code is still a
	//    genuine code: it admits the account and grants nothing, which is what
	//    000018_invites promises for exactly this case.
	deletedPlanAccount, err := cp.RegisterTenantUser(config.RegistrationInvite, codeForDeletedPlan.Code, "invite_deleted_plan", "password-123456", nowNs)
	if err != nil {
		t.Fatalf("RegisterTenantUser with a code whose plan was deleted: %v", err)
	}
	requireInviteUsedCount(t, cp, codeForDeletedPlan.ID, 1)
	subs, err := cp.Engine.ListUserSubscriptions()
	if err != nil {
		t.Fatalf("ListUserSubscriptions: %v", err)
	}
	for _, sub := range subs {
		if sub.UserID == deletedPlanAccount.ID {
			t.Fatalf("expected an account whose code lost its plan to be on no subscription, got %s on plan %s", sub.ID, sub.PlanID)
		}
	}

	// 3b. A stale code that still names a plan nothing can resolve - what a row
	//     written before the state layer started clearing plan_id looks like.
	//     It is refused whole: no use spent and no account, so no caller can
	//     ever be told the registration failed and then meet a 409 on the
	//     retry.
	stale := model.Invite{
		ID:          "inv_stale_plan_code",
		Code:        "STALE-PLAN-CODE",
		PlanID:      "plan_that_was_never_there",
		MaxUses:     1,
		Status:      model.InviteStatusActive,
		CreatedBy:   "admin",
		CreatedAtNs: nowNs,
		UpdatedAtNs: nowNs,
	}
	if err := cp.Engine.InsertInvite(stale); err != nil {
		t.Fatalf("InsertInvite: %v", err)
	}
	_, err = cp.RegisterTenantUser(config.RegistrationInvite, stale.Code, "invite_stale_plan", "password-123456", nowNs)
	_ = requireServiceError(t, err, "INVALID_ARGUMENT")
	requireNoAccount(t, cp, "invite_stale_plan")
	requireInviteUsedCount(t, cp, stale.ID, 0)

	// 4. A code whose plan is still on sale registers and grants the plan.
	live := createTestPlan(t, cp, "Live Invite Plan", nowNs)
	liveCode, err := cp.CreateInvite(InviteInput{PlanID: live.ID, MaxUses: 1}, "admin", nowNs)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	registered, err := cp.RegisterTenantUser(config.RegistrationInvite, liveCode.Code, "invite_ok", "password-123456", nowNs)
	if err != nil {
		t.Fatalf("RegisterTenantUser with a usable code: %v", err)
	}
	requireInviteUsedCount(t, cp, liveCode.ID, 1)
	sub, err := cp.Engine.GetActiveUserSubscription(registered.ID)
	if err != nil {
		t.Fatalf("GetActiveUserSubscription: %v", err)
	}
	if sub.PlanID != live.ID {
		t.Fatalf("expected the granted subscription to be on plan %s, got %s", live.ID, sub.PlanID)
	}
}

// TestTenantUserWriteErrorsAreClientErrors holds the mapping every console write
// depends on: a duplicate username is a conflict, a short password or a role
// that is not a role is an invalid argument - none of them is a server fault.
func TestTenantUserWriteErrorsAreClientErrors(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	existing, err := cp.CreateTenantUser(TenantUserInput{
		Username: "duplicate_user",
		Password: "password-123456",
		Role:     "user",
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser: %v", err)
	}

	cases := []struct {
		name  string
		input TenantUserInput
		want  string
	}{
		{"duplicate username", TenantUserInput{Username: "duplicate_user", Password: "password-123456", Role: "user"}, "CONFLICT"},
		{"short password", TenantUserInput{Username: "short_password_user", Password: "short", Role: "user"}, "INVALID_ARGUMENT"},
		{"unknown role", TenantUserInput{Username: "unknown_role_user", Password: "password-123456", Role: "superuser"}, "INVALID_ARGUMENT"},
		{"empty username", TenantUserInput{Username: "   ", Password: "password-123456", Role: "user"}, "INVALID_ARGUMENT"},
	}
	for _, tc := range cases {
		_, err := cp.CreateTenantUser(tc.input, nowNs)
		_ = requireServiceError(t, err, tc.want)
	}

	// The single-field setters share the mapping.
	_ = requireServiceError(t, cp.SetTenantUserPassword(existing.ID, "short", nowNs), "INVALID_ARGUMENT")
	_, err = cp.SetTenantUserRole(existing.ID, "superuser", nowNs)
	_ = requireServiceError(t, err, "INVALID_ARGUMENT")
	_, err = cp.SetTenantUserStatus("usr_missing", model.UserStatusActive, nowNs)
	_ = requireServiceError(t, err, "NOT_FOUND")
}

// TestDeleteTenantUserRefusesTheLastAdministrator keeps the console reachable:
// the only active administrator cannot be deleted, which is the same rule that
// already guards disabling and demoting one.
func TestDeleteTenantUserRefusesTheLastAdministrator(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	solo, err := cp.CreateTenantUser(TenantUserInput{
		Username: "solo_admin",
		Password: "password-123456",
		Role:     "admin",
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser: %v", err)
	}

	svcErr := requireServiceError(t, cp.DeleteTenantUser(solo.ID), "FORBIDDEN")
	if !strings.Contains(svcErr.Message, "no active administrator") {
		t.Fatalf("expected the refusal to name the last administrator, got %q", svcErr.Message)
	}
	if _, err := cp.Engine.GetUser(solo.ID); err != nil {
		t.Fatalf("the refused deletion must leave the account in place: %v", err)
	}

	if _, err := cp.CreateTenantUser(TenantUserInput{
		Username: "second_admin",
		Password: "password-123456",
		Role:     "admin",
	}, nowNs); err != nil {
		t.Fatalf("CreateTenantUser(second admin): %v", err)
	}

	if err := cp.DeleteTenantUser(solo.ID); err != nil {
		t.Fatalf("deleting an administrator while another active one exists must succeed: %v", err)
	}
	if _, err := cp.Engine.GetUser(solo.ID); err == nil {
		t.Fatal("expected the account to be gone")
	}
}

// TestUpdateTenantUserAppliesThePatchOrNothing pins the atomic replacement for
// the three-field PATCH: a request that could be refused is refused before
// anything is written, and a valid one applies as a whole.
func TestUpdateTenantUserAppliesThePatchOrNothing(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	admin, err := cp.CreateTenantUser(TenantUserInput{
		Username: "patch_admin",
		Password: "password-123456",
		Role:     "admin",
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser: %v", err)
	}

	// 1. A field the request cannot use refuses the whole patch.
	badRole := "superuser"
	_, err = cp.UpdateTenantUser(admin.ID, TenantUserPatch{Role: &badRole}, nowNs)
	svcErr := requireServiceError(t, err, "INVALID_ARGUMENT")
	if !strings.Contains(svcErr.Message, "no fields were changed") {
		t.Fatalf("expected the refusal to say nothing was applied, got %q", svcErr.Message)
	}

	// 2. The last active administrator cannot disable and demote itself in one
	//    request, and the refusal leaves both fields as they were.
	disabled := model.UserStatusDisabled
	userRole := model.UserRoleUser
	_, err = cp.UpdateTenantUser(admin.ID, TenantUserPatch{Status: &disabled, Role: &userRole}, nowNs)
	svcErr = requireServiceError(t, err, "FORBIDDEN")
	if !strings.Contains(svcErr.Message, "no fields were changed") {
		t.Fatalf("expected the refusal to say nothing was applied, got %q", svcErr.Message)
	}
	after, err := cp.Engine.GetUser(admin.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if after.Status != model.UserStatusActive || after.Role != model.UserRoleAdmin {
		t.Fatalf("the refused patch must leave the account untouched, got status=%s role=%s", after.Status, after.Role)
	}

	// An empty patch is refused before anything else happens.
	_, err = cp.UpdateTenantUser(admin.ID, TenantUserPatch{}, nowNs)
	_ = requireServiceError(t, err, "INVALID_ARGUMENT")

	// 3. With a second administrator in place the same patch applies as a whole.
	if _, err := cp.CreateTenantUser(TenantUserInput{
		Username: "patch_admin_second",
		Password: "password-123456",
		Role:     "admin",
	}, nowNs); err != nil {
		t.Fatalf("CreateTenantUser(second admin): %v", err)
	}
	view, err := cp.UpdateTenantUser(admin.ID, TenantUserPatch{Status: &disabled, Role: &userRole}, nowNs)
	if err != nil {
		t.Fatalf("UpdateTenantUser: %v", err)
	}
	if view.Status != model.UserStatusDisabled || view.Role != model.UserRoleUser {
		t.Fatalf("expected both fields to be applied, got status=%s role=%s", view.Status, view.Role)
	}
}

// TestUsageByUserClampsTheLimit makes the bound the function documents real: the
// top-consumers view returns a page, never the whole table, and the parse of
// "limit" is not a promise about how many rows will be sent.
func TestUsageByUserClampsTheLimit(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	const seeded = maxUserUsageLimit + 5
	rows := make([]model.UsageRow, 0, seeded)
	for i := 0; i < seeded; i++ {
		rows = append(rows, model.UsageRow{
			KeyID:        fmt.Sprintf("k_clamp_%04d", i),
			UserID:       fmt.Sprintf("usr_clamp_%04d", i),
			TSNs:         nowNs - 1000,
			IngressBytes: int64(i + 1),
		})
	}
	if err := cp.Engine.UpsertUsageDaily(rows); err != nil {
		t.Fatalf("UpsertUsageDaily: %v", err)
	}

	if got := clampUsageUserLimit(0); got != defaultUserUsageLimit {
		t.Fatalf("expected no limit to mean the default %d, got %d", defaultUserUsageLimit, got)
	}
	if got := clampUsageUserLimit(100000); got != maxUserUsageLimit {
		t.Fatalf("expected an oversized limit to be capped at %d, got %d", maxUserUsageLimit, got)
	}
	if got := clampUsageUserLimit(5); got != 5 {
		t.Fatalf("expected a limit within the bounds to be kept, got %d", got)
	}

	unlimited, err := cp.UsageByUser(0, 0, 0, nowNs)
	if err != nil {
		t.Fatalf("UsageByUser(limit 0): %v", err)
	}
	if len(unlimited) != defaultUserUsageLimit {
		t.Fatalf("expected %d rows for an omitted limit, got %d", defaultUserUsageLimit, len(unlimited))
	}

	capped, err := cp.UsageByUser(0, 0, 100000, nowNs)
	if err != nil {
		t.Fatalf("UsageByUser(limit 100000): %v", err)
	}
	if len(capped) != maxUserUsageLimit {
		t.Fatalf("expected %d rows for an oversized limit, got %d", maxUserUsageLimit, len(capped))
	}

	top, err := cp.UsageByUser(0, 0, 5, nowNs)
	if err != nil {
		t.Fatalf("UsageByUser(limit 5): %v", err)
	}
	if len(top) != 5 {
		t.Fatalf("expected 5 rows, got %d", len(top))
	}
	if top[0].TotalBytes != int64(seeded) {
		t.Fatalf("expected the heaviest account first, got %d bytes", top[0].TotalBytes)
	}
}

// TestDisablingAPlanStopsTheUsersOnIt holds the emergency stop: status=disabled
// reaches the subscriptions already on the plan (and their keys) without
// apply_to_existing, while an ordinary edit still waits for it.
func TestDisablingAPlanStopsTheUsersOnIt(t *testing.T) {
	cp, keyStore, _, nowNs := newTenantTestService(t)

	plan, err := cp.CreatePlan(PlanInput{Name: "Emergency Stop Plan", TrafficBytes: 12345, MaxKeys: 3, DurationDays: 30, Status: "active"}, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	user, err := cp.CreateTenantUser(TenantUserInput{
		Username:     "stopped_user",
		Password:     "password-123456",
		Role:         "user",
		PlanID:       plan.ID,
		DurationDays: 30,
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser: %v", err)
	}
	key, _, err := cp.CreateAccessKey(user.ID, AccessKeyInput{Name: "stop-key"}, nowNs)
	if err != nil {
		t.Fatalf("CreateAccessKey: %v", err)
	}
	if state, ok := keyStore.Get(key.ID); !ok || !state.Usable(nowNs) {
		t.Fatal("expected the key to work before the plan is disabled")
	}

	// 1. An ordinary edit without apply_to_existing leaves the snapshot alone.
	if _, err := cp.UpdatePlan(plan.ID, []byte(`{"name":"Emergency Stop Plan v2","traffic_bytes":99999}`), nowNs); err != nil {
		t.Fatalf("UpdatePlan(name/traffic_bytes): %v", err)
	}
	snapshot := subscriptionSnapshot(t, cp, user.Subscription.ID)
	if snapshot.Status != planStatusActive || snapshot.TrafficBytes != 12345 {
		t.Fatalf("an ordinary edit must not reach existing subscriptions, got %+v", snapshot)
	}

	// 2. Disabling the plan reaches them whether or not it was asked for, and
	//    rewrites nothing but the status.
	if _, err := cp.UpdatePlan(plan.ID, []byte(`{"status":"disabled"}`), nowNs); err != nil {
		t.Fatalf("UpdatePlan(disabled): %v", err)
	}
	snapshot = subscriptionSnapshot(t, cp, user.Subscription.ID)
	if snapshot.Status != planStatusDisabled {
		t.Fatalf("expected the subscription snapshot to be stopped, got %+v", snapshot)
	}
	if snapshot.TrafficBytes != 12345 {
		t.Fatalf("the emergency stop must only rewrite the status, got %+v", snapshot)
	}
	if state, ok := keyStore.Get(key.ID); !ok || state.Usable(nowNs) {
		t.Fatal("expected the key to be unusable once the plan is disabled")
	}
}

// TestSubscriptionActivationLeavesOneActiveRow locks the "last activation wins"
// rule for both doors: assigning a plan and patching an old subscription back to
// active each end whatever else was running.
func TestSubscriptionActivationLeavesOneActiveRow(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	firstPlan := createTestPlan(t, cp, "First Plan", nowNs)
	secondPlan := createTestPlan(t, cp, "Second Plan", nowNs)

	user, err := cp.CreateTenantUser(TenantUserInput{
		Username:     "sub_switch_user",
		Password:     "password-123456",
		Role:         "user",
		PlanID:       firstPlan.ID,
		DurationDays: 30,
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser: %v", err)
	}
	firstID := user.Subscription.ID

	activeRows := func() []model.UserSubscription {
		t.Helper()
		rows, err := cp.Engine.ListUserSubscriptions()
		if err != nil {
			t.Fatalf("ListUserSubscriptions: %v", err)
		}
		var active []model.UserSubscription
		for _, row := range rows {
			if row.UserID == user.ID && row.Status == model.SubscriptionStatusActive {
				active = append(active, row)
			}
		}
		return active
	}

	second, err := cp.AssignSubscription(user.ID, secondPlan.ID, 10, nowNs)
	if err != nil {
		t.Fatalf("AssignSubscription: %v", err)
	}
	if active := activeRows(); len(active) != 1 || active[0].ID != second.ID {
		t.Fatalf("expected only the new subscription to be active, got %+v", active)
	}

	// Patching the older row back to active is an activation too.
	if _, err := cp.UpdateTenantSubscription(firstID, []byte(`{"status":"active"}`), nowNs); err != nil {
		t.Fatalf("UpdateTenantSubscription(active): %v", err)
	}
	active := activeRows()
	if len(active) != 1 {
		t.Fatalf("expected exactly one active row after re-activating the old subscription, got %d", len(active))
	}
	if active[0].ID != firstID {
		t.Fatalf("expected the last activation (%s) to win, got %s", firstID, active[0].ID)
	}
	secondView, err := cp.GetTenantSubscription(second.ID)
	if err != nil {
		t.Fatalf("GetTenantSubscription: %v", err)
	}
	if secondView.Status != model.SubscriptionStatusExpired {
		t.Fatalf("expected the superseded subscription to be expired, got %s", secondView.Status)
	}
}
