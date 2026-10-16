package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"prism/internal/auth"
	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/state"
)

// Tests for the closeout batch: the subscription terms writes that must not
// clobber a concurrently counted usage total, the revocation that replaced the
// subscription delete, and the invite screening that has to happen before the
// public registration path spends an argon2id hash.

// --- A. Subscription writes are terms writes ---

// requireSubscriptionUsage asserts the period_used_bytes counter of one row.
func requireSubscriptionUsage(t *testing.T, cp *ControlPlaneService, id string, want int64) {
	t.Helper()
	row, err := cp.Engine.GetUserSubscription(id)
	if err != nil {
		t.Fatalf("GetUserSubscription(%s): %v", id, err)
	}
	if row.PeriodUsedBytes != want {
		t.Fatalf("period_used_bytes of %s: got %d, want %d", id, row.PeriodUsedBytes, want)
	}
}

// createUserOnPlan puts one account on a plan and returns it.
func createUserOnPlan(t *testing.T, cp *ControlPlaneService, username, planID string, nowNs int64) AdminUserDetail {
	t.Helper()
	user, err := cp.CreateTenantUser(TenantUserInput{
		Username:     username,
		Password:     "password-123456",
		Role:         "user",
		PlanID:       planID,
		DurationDays: 30,
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser(%s): %v", username, err)
	}
	return user
}

// TestUpdateTenantSubscription_TermsWriteKeepsUsageCounters drives the console
// PATCH against a row whose usage counter has moved since it was read.
//
// period_used_bytes has a second writer - the usage flush worker adds to it -
// so a patch that wrote the whole row back as it was read would hand back every
// byte counted in between, permanently, because nothing recomputes the counter
// from the usage tables. The three patches below are the three writes the console
// can make: an extension, a suspension and a reactivation.
func TestUpdateTenantSubscription_TermsWriteKeepsUsageCounters(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	plan := createTestPlan(t, cp, "Terms Plan", nowNs)
	user := createUserOnPlan(t, cp, "terms_user", plan.ID, nowNs)
	subID := user.Subscription.ID

	if err := cp.Engine.AddSubscriptionUsedBytes(subID, 4096); err != nil {
		t.Fatalf("AddSubscriptionUsedBytes: %v", err)
	}
	requireSubscriptionUsage(t, cp, subID, 4096)

	// 1. extend_days rewrites the window only.
	if _, err := cp.UpdateTenantSubscription(subID, []byte(`{"extend_days":5}`), nowNs); err != nil {
		t.Fatalf("UpdateTenantSubscription(extend_days): %v", err)
	}
	requireSubscriptionUsage(t, cp, subID, 4096)

	// 2. Suspending stops the keys; it must not stop the counter.
	if _, err := cp.UpdateTenantSubscription(subID, []byte(`{"status":"suspended"}`), nowNs); err != nil {
		t.Fatalf("UpdateTenantSubscription(suspended): %v", err)
	}
	requireSubscriptionUsage(t, cp, subID, 4096)

	// 3. Reactivating expires whatever else runs and brings this row back.
	if _, err := cp.UpdateTenantSubscription(subID, []byte(`{"status":"active"}`), nowNs); err != nil {
		t.Fatalf("UpdateTenantSubscription(active): %v", err)
	}
	requireSubscriptionUsage(t, cp, subID, 4096)
}

// TestSubscriptionWrites_PreserveUsageCounters covers the three write paths that
// do not come from a PATCH: assigning another plan (which expires the previous
// row), editing a plan with apply_to_existing (which rewrites the snapshot of
// every active subscription) and disabling a plan (the emergency stop).
func TestSubscriptionWrites_PreserveUsageCounters(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	planA := createTestPlan(t, cp, "Closeout Plan A", nowNs)
	planB := createTestPlan(t, cp, "Closeout Plan B", nowNs)
	user := createUserOnPlan(t, cp, "closeout_user", planA.ID, nowNs)
	firstSubID := user.Subscription.ID

	if err := cp.Engine.AddSubscriptionUsedBytes(firstSubID, 8192); err != nil {
		t.Fatalf("AddSubscriptionUsedBytes: %v", err)
	}

	// Assigning the second plan expires the first row through
	// expireActiveSubscriptions.
	second, err := cp.AssignSubscription(user.ID, planB.ID, 30, nowNs)
	if err != nil {
		t.Fatalf("AssignSubscription: %v", err)
	}
	expired, err := cp.Engine.GetUserSubscription(firstSubID)
	if err != nil {
		t.Fatalf("GetUserSubscription(%s): %v", firstSubID, err)
	}
	if expired.Status != model.SubscriptionStatusExpired {
		t.Fatalf("expected the previous subscription to be expired, got %s", expired.Status)
	}
	requireSubscriptionUsage(t, cp, firstSubID, 8192)

	if err := cp.Engine.AddSubscriptionUsedBytes(second.ID, 512); err != nil {
		t.Fatalf("AddSubscriptionUsedBytes(second): %v", err)
	}

	// apply_to_existing rewrites the snapshot of the new active subscription.
	if _, err := cp.UpdatePlan(planB.ID, []byte(`{"max_concurrent":7,"apply_to_existing":true}`), nowNs); err != nil {
		t.Fatalf("UpdatePlan(apply_to_existing): %v", err)
	}
	requireSubscriptionUsage(t, cp, second.ID, 512)
	row, err := cp.Engine.GetUserSubscription(second.ID)
	if err != nil {
		t.Fatalf("GetUserSubscription(%s): %v", second.ID, err)
	}
	snapshot, err := planFromSnapshotJSON(row.PlanSnapshotJSON)
	if err != nil {
		t.Fatalf("planFromSnapshotJSON: %v", err)
	}
	if snapshot.MaxConcurrent != 7 {
		t.Fatalf("expected the refreshed snapshot to carry max_concurrent 7, got %d", snapshot.MaxConcurrent)
	}

	// Disabling the plan is the emergency stop: the snapshot status is forced
	// down, and the counter still is not the snapshot's business.
	if _, err := cp.UpdatePlan(planB.ID, []byte(`{"status":"disabled"}`), nowNs); err != nil {
		t.Fatalf("UpdatePlan(disabled): %v", err)
	}
	requireSubscriptionUsage(t, cp, second.ID, 512)
	row, err = cp.Engine.GetUserSubscription(second.ID)
	if err != nil {
		t.Fatalf("GetUserSubscription(%s) after the stop: %v", second.ID, err)
	}
	snapshot, err = planFromSnapshotJSON(row.PlanSnapshotJSON)
	if err != nil {
		t.Fatalf("planFromSnapshotJSON after the stop: %v", err)
	}
	if snapshot.Status != planStatusDisabled {
		t.Fatalf("expected the stopped snapshot status %s, got %s", planStatusDisabled, snapshot.Status)
	}
	if row.Status != model.SubscriptionStatusActive {
		t.Fatalf("the stop belongs in the snapshot, not in the row status: got %s", row.Status)
	}
}

// TestMapSubscriptionWriteError pins the answer a client gets when a
// subscription write hits the one-active-row unique index: a conflict the
// operator can act on, not a server fault.
func TestMapSubscriptionWriteError(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		want    string
		message string
	}{
		{
			name:    "conflict",
			err:     state.ErrConflict,
			want:    "CONFLICT",
			message: "the user already has an active subscription",
		},
		{
			name:    "wrapped conflict",
			err:     fmt.Errorf("activate subscription: %w", state.ErrConflict),
			want:    "CONFLICT",
			message: "the user already has an active subscription",
		},
		{
			name:    "missing row",
			err:     state.ErrNotFound,
			want:    "NOT_FOUND",
			message: "subscription not found",
		},
		{
			name: "server fault",
			err:  errors.New("disk went away"),
			want: "INTERNAL",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svcErr := mapSubscriptionWriteError("write subscription", tc.err)
			if svcErr.Code != tc.want {
				t.Fatalf("expected code %s, got %s: %v", tc.want, svcErr.Code, svcErr)
			}
			if tc.message != "" && svcErr.Message != tc.message {
				t.Fatalf("expected message %q, got %q", tc.message, svcErr.Message)
			}
		})
	}
}

// --- B. Deleting a subscription revokes it ---

// TestDeleteTenantSubscription_RevokesAndTakesTheKeysOutOfUse is the security
// half of the closeout: a delete that removed the last subscription row would
// leave the account on the zero snapshot - no quota, no expiry, no key limit -
// and it would be handed more access than it had, not less. The row is revoked
// instead, and the account cannot be minted a replacement key.
func TestDeleteTenantSubscription_RevokesAndTakesTheKeysOutOfUse(t *testing.T) {
	cp, keyStore, _, nowNs := newTenantTestService(t)

	plan := createTestPlan(t, cp, "Revoke Plan", nowNs)
	user := createUserOnPlan(t, cp, "revoke_user", plan.ID, nowNs)
	subID := user.Subscription.ID

	key, _, err := cp.CreateAccessKey(user.ID, AccessKeyInput{Name: "live-key"}, nowNs)
	if err != nil {
		t.Fatalf("CreateAccessKey: %v", err)
	}
	if live, ok := keyStore.Get(key.ID); !ok || !live.Usable(nowNs) {
		t.Fatal("expected the key to work while the subscription is active")
	}

	if err := cp.Engine.AddSubscriptionUsedBytes(subID, 1024); err != nil {
		t.Fatalf("AddSubscriptionUsedBytes: %v", err)
	}

	if err := cp.DeleteTenantSubscription(subID); err != nil {
		t.Fatalf("DeleteTenantSubscription: %v", err)
	}

	row, err := cp.Engine.GetUserSubscription(subID)
	if err != nil {
		t.Fatalf("the subscription row must survive the delete: %v", err)
	}
	if row.Status != model.SubscriptionStatusSuspended {
		t.Fatalf("expected the revoked row status %s, got %s", model.SubscriptionStatusSuspended, row.Status)
	}
	requireSubscriptionUsage(t, cp, subID, 1024)

	if live, ok := keyStore.Get(key.ID); !ok || live.Usable(nowNs) {
		t.Fatal("expected the revoked subscription to take the key out of use")
	}

	// A revoked account is not an unlimited one: nothing can be minted here.
	_, _, err = cp.CreateAccessKey(user.ID, AccessKeyInput{Name: "after-revoke"}, nowNs)
	svcErr := requireServiceError(t, err, "CONFLICT")
	if !strings.Contains(svcErr.Message, "no subscription in force") {
		t.Fatalf("expected the refusal to name the missing subscription, got %q", svcErr.Message)
	}
}

// TestDeleteTenantSubscription_UnknownIDReportsNotFound keeps the delete's
// answer for a row that never existed.
func TestDeleteTenantSubscription_UnknownIDReportsNotFound(t *testing.T) {
	cp, _, _, _ := newTenantTestService(t)

	err := cp.DeleteTenantSubscription("us_missing")
	_ = requireServiceError(t, err, "NOT_FOUND")
}

// --- D. The public registration path refuses codes before it hashes ---

// TestRegisterTenantUser_RefusesBadInviteCodesBeforeHashing checks both halves of
// the pre-screening: the refusals are the ones the path already produced, and
// they are produced without an argon2id hash.
//
// The cost check is a comparison rather than a count because hashing is a
// package function with no seam to count through. The margin is the whole point:
// a refusual costs a length check on the malformed shapes and one indexed read on
// the well-formed one, so fifteen of them have to fit inside a single hash - they
// do, by orders of magnitude, and if the order ever regressed every one of those
// fifteen would cost a hash of its own.
func TestRegisterTenantUser_RefusesBadInviteCodesBeforeHashing(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	const password = "password-123456"
	cases := []struct {
		name    string
		code    string
		message string
	}{
		{"missing", "", "invite_code: required"},
		{"no shape at all", "nope", "invite_code: the code is not valid"},
		{"right length, wrong alphabet", "0000-0000", "invite_code: the code is not valid"},
		{"well formed but unknown", "2345-6789", "invite_code: the code is not valid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cp.RegisterTenantUser(config.RegistrationInvite, tc.code, "victim", password, nowNs)
			svcErr := requireServiceError(t, err, "INVALID_ARGUMENT")
			if svcErr.Message != tc.message {
				t.Fatalf("expected message %q, got %q", tc.message, svcErr.Message)
			}
		})
	}
	requireNoAccount(t, cp, "victim")

	// Warm the allocator up before the measurement so the first call's page
	// faults are not counted as either cost.
	if _, err := auth.HashPassword(password); err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	start := time.Now()
	if _, err := auth.HashPassword(password); err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	hashDur := time.Since(start)

	const attempts = 15
	start = time.Now()
	for i := 0; i < attempts; i++ {
		_, err := cp.RegisterTenantUser(config.RegistrationInvite, "0000-0000", fmt.Sprintf("victim-%d", i), password, nowNs)
		if err == nil {
			t.Fatal("expected the malformed invite code to be refused")
		}
	}
	elapsed := time.Since(start)
	if elapsed >= hashDur {
		t.Fatalf("%d refusals took %v while one argon2id hash takes %v: the code is being hashed before it is checked",
			attempts, elapsed, hashDur)
	}
	t.Logf("%d refusals in %v against one argon2id hash in %v", attempts, elapsed, hashDur)
}

// TestRegisterTenantUser_RedeemsARealCode keeps the other side of the same path
// honest: the screening must not have moved the point at which a good code is
// spent, and a spent code still refuses.
func TestRegisterTenantUser_RedeemsARealCode(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	plan := createTestPlan(t, cp, "Invite Closeout Plan", nowNs)
	invite, err := cp.CreateInvite(InviteInput{
		PlanID:        plan.ID,
		MaxUses:       1,
		ExpiresInDays: 7,
	}, "admin", nowNs)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	user, err := cp.RegisterTenantUser(config.RegistrationInvite, invite.Code, "invited_user", "password-123456", nowNs)
	if err != nil {
		t.Fatalf("RegisterTenantUser: %v", err)
	}
	requireInviteUsedCount(t, cp, invite.ID, 1)

	rows, err := cp.Engine.ListUserSubscriptions()
	if err != nil {
		t.Fatalf("ListUserSubscriptions: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.UserID != user.ID {
			continue
		}
		found = true
		if row.PlanID != plan.ID {
			t.Fatalf("expected the granted subscription on plan %s, got %s", plan.ID, row.PlanID)
		}
		if row.Status != model.SubscriptionStatusActive {
			t.Fatalf("expected an active subscription, got %s", row.Status)
		}
	}
	if !found {
		t.Fatal("the invitation granted no subscription")
	}

	// The code is spent: the same code with another name is refused, and no
	// second account is created.
	_, err = cp.RegisterTenantUser(config.RegistrationInvite, invite.Code, "second_user", "password-123456", nowNs)
	_ = requireServiceError(t, err, "INVALID_ARGUMENT")
	requireNoAccount(t, cp, "second_user")
}
