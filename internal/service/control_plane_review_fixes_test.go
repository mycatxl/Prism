package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"prism/internal/auth"
	"prism/internal/config"
	"prism/internal/model"
)

// Tests for the follow-up review's seven findings, at the service layer: what a
// registration with an unusable invite costs, what an assignment without a plan
// would create, what a password write does to a concurrent status write, what a
// second-factor rotation does to the replay ledger, and what the account lock
// covers.

// --- F1: an invite that cannot work costs no hash ---------------------------

// TestRegisterTenantUser_UnusableCodesCostNoHash pins the ordering the public
// registration path promises: everything that can be refused before the password
// is hashed is refused first.
//
// The three codes below are real ones an operator created, so the shape check
// and the lookup both pass, and the refusals used to arrive only after the
// argon2id hash - the expensive step - had been paid. They must all be refused
// with the message an unknown code gets, and the message is asserted to be the
// same, because the endpoint must not become a way to tell the states of codes
// apart.
func TestRegisterTenantUser_UnusableCodesCostNoHash(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	plan := createTestPlan(t, cp, "Review plan", nowNs)

	hashes := 0
	cp.HashPassword = func(string) (string, error) {
		hashes++
		return "$argon2id$counted-once", nil
	}

	invite := func(code string, mutate func(*model.Invite)) model.Invite {
		t.Helper()
		row := model.Invite{
			ID:          "inv_review_" + code,
			Code:        code,
			PlanID:      plan.ID,
			MaxUses:     1,
			Status:      model.InviteStatusActive,
			CreatedBy:   "review",
			CreatedAtNs: nowNs,
			UpdatedAtNs: nowNs,
		}
		mutate(&row)
		if err := cp.Engine.InsertInvite(row); err != nil {
			t.Fatalf("InsertInvite(%s): %v", code, err)
		}
		return row
	}

	disabled := invite("ABCD-2345", func(row *model.Invite) { row.Status = model.InviteStatusDisabled })
	expired := invite("EFGH-6789", func(row *model.Invite) { row.ExpiresAtNs = nowNs - 1 })
	exhausted := invite("JKMN-2345", func(row *model.Invite) {
		row.MaxUses = 1
		row.UsedCount = 1
	})

	codes := []struct {
		name string
		code string
	}{
		{"disabled", disabled.Code},
		{"expired", expired.Code},
		{"exhausted", exhausted.Code},
		// The case the previous tests covered: a code that cannot be one of
		// ours. It is kept here so the four messages are compared with each
		// other rather than with a constant.
		{"unknown", "2345-6789"},
	}
	messages := make(map[string]string, len(codes))
	for _, tc := range codes {
		_, err := cp.RegisterTenantUser(config.RegistrationInvite, tc.code, "review-victim", "review-victim-password", nowNs)
		svcErr := requireServiceError(t, err, "INVALID_ARGUMENT")
		messages[tc.name] = svcErr.Message
		if svcErr.Message != "invite_code: the code is not valid" {
			t.Errorf("%s code: message %q, want %q", tc.name, svcErr.Message, "invite_code: the code is not valid")
		}
	}
	for _, other := range []string{"disabled", "expired", "exhausted"} {
		if messages[other] != messages["unknown"] {
			t.Errorf("a %s code reads as %q while an unknown one reads as %q: the states are distinguishable",
				other, messages[other], messages["unknown"])
		}
	}

	// The point of the ordering: none of the four refusals paid for a hash.
	if hashes != 0 {
		t.Errorf("the refusals cost %d argon2id hashes, want 0: the code must be refused before the password is hashed", hashes)
	}
	// And none of them spent a use or created an account: the records read
	// back exactly as the fixtures left them.
	requireNoAccount(t, cp, "review-victim")
	wantUses := map[string]int{"disabled": 0, "expired": 0, "exhausted": 1}
	for name, code := range map[string]string{"disabled": disabled.Code, "expired": expired.Code, "exhausted": exhausted.Code} {
		row, err := cp.Engine.GetInviteByCode(code)
		if err != nil {
			t.Fatalf("GetInviteByCode(%s) after the attempt: %v", name, err)
		}
		if row.UsedCount != wantUses[name] {
			t.Errorf("the %s code after the attempt: used_count %d, want %d (a refusal must not spend a use)",
				name, row.UsedCount, wantUses[name])
		}
	}
}

// --- F2: an assignment without a plan is refused ----------------------------

// TestAssignSubscription_RefusesAnEmptyPlan is the hole the review found: an
// empty plan_id used to be accepted and written as an active subscription
// carrying the zero snapshot.
//
// The zero snapshot is not "a smaller plan". keys.Store only refuses a snapshot
// whose status is set, and a quota of zero means unlimited, so the row was a
// licence to mint keys with no limit at all. The read side cannot tell that row
// from a real one, which is why the refusal has to be at the write.
func TestAssignSubscription_RefusesAnEmptyPlan(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)
	plan := createTestPlan(t, cp, "Review plan", nowNs)

	account, err := cp.CreateAccount("review-empty-plan", "review-empty-plan-password", model.UserRoleUser, nowNs)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	_, err = cp.AssignSubscription(account.ID, "   ", 30, nowNs)
	svcErr := requireServiceError(t, err, "INVALID_ARGUMENT")
	if svcErr.Message != "plan_id: required" {
		t.Errorf("message: got %q, want %q", svcErr.Message, "plan_id: required")
	}

	rows, err := cp.Engine.ListUserSubscriptions()
	if err != nil {
		t.Fatalf("ListUserSubscriptions: %v", err)
	}
	for _, row := range rows {
		if row.UserID == account.ID {
			t.Fatalf("the refused assignment left a subscription behind: %+v", row)
		}
	}

	// A real plan still works, so the refusal above is about the missing value
	// and not about the endpoint.
	view, err := cp.AssignSubscription(account.ID, plan.ID, 30, nowNs)
	if err != nil {
		t.Fatalf("AssignSubscription with a real plan: %v", err)
	}
	if view.PlanID != plan.ID || view.Status != model.SubscriptionStatusActive {
		t.Fatalf("the accepted assignment: got plan %q status %q", view.PlanID, view.Status)
	}

	// And the state the refusal exists to prevent, written directly for the
	// read side to look at: a legacy or hand-edited active row with the zero
	// snapshot is read as unlimited by maxKeysForPlan and checkKeyAllowance.
	// If either of those ever starts refusing the zero snapshot, this test and
	// the comment on AssignSubscription need revisiting - that is the point of
	// asserting it here.
	legacy, err := cp.CreateAccount("review-zero-plan", "review-zero-plan-password", model.UserRoleUser, nowNs)
	if err != nil {
		t.Fatalf("CreateAccount(zero-snapshot fixture): %v", err)
	}
	zero := model.UserSubscription{
		ID:               "us_review_zero",
		UserID:           legacy.ID,
		PlanSnapshotJSON: `{"status":"","traffic_bytes":0,"max_keys":0,"platforms":[]}`,
		StartsAtNs:       nowNs,
		ExpiresAtNs:      nowNs + int64(24*time.Hour),
		PeriodStartNs:    nowNs,
		Status:           model.SubscriptionStatusActive,
	}
	if err := cp.Engine.UpsertUserSubscription(zero); err != nil {
		t.Fatalf("UpsertUserSubscription(zero snapshot): %v", err)
	}
	limit, planErr := cp.maxKeysForPlan(legacy.ID)
	if planErr != nil {
		t.Fatalf("maxKeysForPlan on the zero snapshot: %v", planErr)
	}
	if limit != 0 {
		t.Fatalf("the zero snapshot reports a key limit of %d; the read side now tells it apart, so the fix's reasoning needs a second look", limit)
	}
	stored, err := cp.Engine.GetUser(legacy.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if svcErr := cp.checkKeyAllowance(stored); svcErr != nil {
		t.Fatalf("checkKeyAllowance now refuses the zero snapshot (%v); the read side changed and the fix's reasoning needs a second look", svcErr)
	}
}

// --- F4: a password write leaves the other columns alone --------------------

// TestSetAccountPassword_DoesNotRevertAConcurrentStatusChange reproduces the
// interleaving the whole-row write-back allowed: an administrator resets a
// password (read, verify, hash, write - tens of milliseconds) while another
// administrator disables the account.
//
// The hash hook runs inside setPassword, so the concurrent write lands exactly
// between this caller's read and its write, which is the window the review
// described. Both changes have to survive; with a whole-row write the disable
// was silently reverted.
func TestSetAccountPassword_DoesNotRevertAConcurrentStatusChange(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	victim, err := cp.CreateAccount("review-victim", "review-victim-password", model.UserRoleUser, nowNs)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	cp.HashPassword = func(string) (string, error) {
		if err := cp.Engine.UpdateUserStatus(victim.ID, model.UserStatusDisabled, nowNs+1); err != nil {
			t.Fatalf("the concurrent status change: %v", err)
		}
		return "$argon2id$replaced", nil
	}
	if err := cp.SetAccountPassword(victim.ID, "review-victim-password-new", nowNs+2); err != nil {
		t.Fatalf("SetAccountPassword: %v", err)
	}

	stored, err := cp.Engine.GetUser(victim.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if stored.Status != model.UserStatusDisabled {
		t.Errorf("status after the password reset: got %q, want %q (the reset must not restore what it read)",
			stored.Status, model.UserStatusDisabled)
	}
	if stored.PasswordHash != "$argon2id$replaced" {
		t.Errorf("password hash after the reset: got %q, want the value written", stored.PasswordHash)
	}
	if stored.Role != model.UserRoleUser || stored.Username != "review-victim" {
		t.Errorf("columns the reset never touched changed: role %q username %q", stored.Role, stored.Username)
	}
}

// --- F5: a rotated second factor gets its step back -------------------------

// TestSecondFactorRotation_ClearsTheReplayLedger is the service-level half of
// the ledger reset: a code from a freshly enrolled secret must be accepted even
// when it falls inside a step the previous secret already spent, which is what
// the login path asks by advancing the ledger.
func TestSecondFactorRotation_ClearsTheReplayLedger(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	account, err := cp.CreateAccount("review-rotate", "review-rotate-password", model.UserRoleUser, nowNs)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	const stepNs = int64(30 * time.Second)
	step := nowNs / stepNs * stepNs

	enrol := func(secret string, atNs int64) {
		t.Helper()
		code, err := auth.TOTPCode(secret, time.Unix(0, atNs))
		if err != nil {
			t.Fatalf("TOTPCode: %v", err)
		}
		if err := cp.ConfirmTOTPEnrolment(account.ID, secret, code, atNs); err != nil {
			t.Fatalf("ConfirmTOTPEnrolment: %v", err)
		}
	}

	first, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	enrol(first, nowNs)
	// A login with the first secret's code spends the step.
	accepted, err := cp.Engine.AdvanceTOTPLastUsed(account.ID, step)
	if err != nil {
		t.Fatalf("AdvanceTOTPLastUsed: %v", err)
	}
	if !accepted {
		t.Fatal("the first use of the step was refused")
	}

	if err := cp.DisableTOTP(account.ID, nowNs+1); err != nil {
		t.Fatalf("DisableTOTP: %v", err)
	}
	afterDisable, err := cp.Engine.GetUser(account.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if afterDisable.TOTPLastUsedNs != 0 {
		t.Errorf("totp_last_used_ns after removing the factor: got %d, want 0", afterDisable.TOTPLastUsedNs)
	}

	// The replacement secret is enrolled inside the same step the old one spent.
	second, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	enrol(second, nowNs+2)
	accepted, err = cp.Engine.AdvanceTOTPLastUsed(account.ID, step)
	if err != nil {
		t.Fatalf("AdvanceTOTPLastUsed after the rotation: %v", err)
	}
	if !accepted {
		t.Error("the fresh secret's first code was refused as a replay of a step the old secret spent")
	}
}

// --- F6: one writer at a time for a user row --------------------------------

// TestUpdateTenantUser_HoldsTheLockAcrossItsWrites pins the mutex the account
// patch takes: a competing account write must not be able to complete while the
// patch is between its read and its writes.
//
// The hash hook is the patch's slow middle step - the argon2id the lock is held
// across - so what it observes is the lock's coverage rather than a schedule:
// without the lock, the competing write finishes inside the window and this test
// fails on the hook's error.
func TestUpdateTenantUser_HoldsTheLockAcrossItsWrites(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	victim, err := cp.CreateAccount("review-locked", "review-locked-password", model.UserRoleUser, nowNs)
	if err != nil {
		t.Fatalf("CreateAccount(victim): %v", err)
	}
	other, err := cp.CreateAccount("review-other", "review-other-password", model.UserRoleUser, nowNs)
	if err != nil {
		t.Fatalf("CreateAccount(other): %v", err)
	}

	competing := make(chan error, 1)
	cp.HashPassword = func(string) (string, error) {
		go func() { competing <- cp.SetAccountStatus(other.ID, model.UserStatusDisabled, nowNs+1) }()
		select {
		case err := <-competing:
			return "", fmt.Errorf("a competing account write finished while the patch was still running: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
		return "$argon2id$patched", nil
	}

	disabled := model.UserStatusDisabled
	admin := model.UserRoleAdmin
	password := "review-locked-password-new"
	view, err := cp.UpdateTenantUser(victim.ID, TenantUserPatch{Status: &disabled, Role: &admin, Password: &password}, nowNs+2)
	if err != nil {
		t.Fatalf("UpdateTenantUser: %v", err)
	}
	if view.Status != model.UserStatusDisabled || view.Role != model.UserRoleAdmin {
		t.Fatalf("the accepted patch: got status %q role %q, want disabled/admin - the whole patch is applied", view.Status, view.Role)
	}

	// The competing write was waiting for the lock, and lands once the patch
	// has released it.
	select {
	case err := <-competing:
		if err != nil {
			t.Fatalf("the competing write: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the competing write never completed after the patch released the lock")
	}
	stored, err := cp.Engine.GetUser(other.ID)
	if err != nil {
		t.Fatalf("GetUser(other): %v", err)
	}
	if stored.Status != model.UserStatusDisabled {
		t.Errorf("the competing write's effect: got status %q, want %q", stored.Status, model.UserStatusDisabled)
	}
}

// TestConcurrentDemotions_LeaveAnActiveAdministrator is the invariant the
// last-administrator rule exists for, tested through the calls the console
// makes: two administrators demoting each other, at the same time, repeated
// enough times for the race to be reachable.
//
// Each round starts with exactly two active administrators: the survivors of
// earlier rounds are demoted first, while this round's pair can carry the count,
// so the pair really is alone. The round must then end with at least one of the
// two still an active administrator. The count is only a promise while the check
// and the write are one locked operation, which is what userWriteMu makes them.
func TestConcurrentDemotions_LeaveAnActiveAdministrator(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	const rounds = 25
	var survivor []string
	for round := 0; round < rounds; round++ {
		first, err := cp.CreateAccount(fmt.Sprintf("review-admin-a-%d", round), "review-admin-a-password", model.UserRoleAdmin, nowNs)
		if err != nil {
			t.Fatalf("CreateAccount(first, round %d): %v", round, err)
		}
		second, err := cp.CreateAccount(fmt.Sprintf("review-admin-b-%d", round), "review-admin-b-password", model.UserRoleAdmin, nowNs)
		if err != nil {
			t.Fatalf("CreateAccount(second, round %d): %v", round, err)
		}
		// An earlier round's survivor is an active administrator too, and the
		// invariant under test is about this pair alone, so it is demoted while
		// the fresh pair can still carry the console.
		for _, id := range survivor {
			if err := cp.SetAccountRole(id, model.UserRoleUser, nowNs); err != nil {
				t.Fatalf("round %d: demoting the previous round's survivor: %v", round, err)
			}
		}
		survivor = []string{first.ID, second.ID}

		start := make(chan struct{})
		results := make(chan error, 2)
		demote := func(id string) {
			<-start
			results <- cp.SetAccountRole(id, model.UserRoleUser, nowNs+1)
		}
		go demote(first.ID)
		go demote(second.ID)
		close(start)

		refusals := 0
		for i := 0; i < 2; i++ {
			switch err := <-results; {
			case err == nil:
			case errors.Is(err, ErrLastAdmin):
				refusals++
			default:
				t.Fatalf("round %d: unexpected error from a demotion: %v", round, err)
			}
		}

		activeAdmins := 0
		for _, id := range []string{first.ID, second.ID} {
			stored, err := cp.Engine.GetUser(id)
			if err != nil {
				t.Fatalf("GetUser(%s, round %d): %v", id, round, err)
			}
			if stored.Role == model.UserRoleAdmin && stored.Status == model.UserStatusActive {
				activeAdmins++
			}
		}
		if activeAdmins == 0 {
			t.Fatalf("round %d: %d of the two demotions was refused and no active administrator is left: the check and the write are not one operation",
				round, refusals)
		}
	}
}

// TestLastAdministratorRefusals_ChangeNothing pins the shape and the effect of
// the last-administrator refusal: the account methods return the bare
// ErrLastAdmin, the tenant-user methods map it through mapAccountWriteError into
// a FORBIDDEN answer for the console, and the account is left exactly as it was,
// including its username.
func TestLastAdministratorRefusals_ChangeNothing(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	solo, err := cp.CreateAccount("review-solo", "review-solo-password", model.UserRoleAdmin, nowNs)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := cp.SetAccountStatus(solo.ID, model.UserStatusDisabled, nowNs+1); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("disabling the only administrator: got %v, want ErrLastAdmin", err)
	}
	if err := cp.SetAccountRole(solo.ID, model.UserRoleUser, nowNs+1); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demoting the only administrator: got %v, want ErrLastAdmin", err)
	}
	// DeleteTenantUser answers the caller rather than returning the sentinel,
	// so what the console sees is a FORBIDDEN ServiceError either way.
	if svcErr := requireServiceError(t, cp.DeleteTenantUser(solo.ID), "FORBIDDEN"); svcErr.Message == "" {
		t.Fatal("deleting the only administrator: a FORBIDDEN answer with no message")
	}

	stored, err := cp.Engine.GetUser(solo.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if stored.Status != model.UserStatusActive || stored.Role != model.UserRoleAdmin {
		t.Fatalf("a refused last-administrator operation changed the account: status %q role %q", stored.Status, stored.Role)
	}
	if stored.Username != "review-solo" {
		t.Fatalf("a refused last-administrator operation replaced the account: username %q", stored.Username)
	}
}

// TestUpdateTenantUser_FailedHashAppliesNothing is the atomicity half of the
// patch that the last-administrator tests cannot reach: the password hash is the
// one step that can fail on its own, and it runs before the first write. A patch
// asking for a status change and a password reset that cannot produce the hash
// must leave the status and the stored hash as they were - a half-applied patch
// is exactly what the operator's next read would contradict.
func TestUpdateTenantUser_FailedHashAppliesNothing(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	account, err := cp.CreateAccount("review-halfling", "review-halfling-password", model.UserRoleUser, nowNs)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	before, err := cp.Engine.GetUser(account.ID)
	if err != nil {
		t.Fatalf("GetUser before the patch: %v", err)
	}
	cp.HashPassword = func(string) (string, error) {
		return "", fmt.Errorf("the hasher ran out of entropy")
	}

	disabled := model.UserStatusDisabled
	password := "review-halfling-password-new"
	_, err = cp.UpdateTenantUser(account.ID, TenantUserPatch{Status: &disabled, Password: &password}, nowNs+1)
	// The failure is a server fault - the hash could not be produced - and it
	// must reach the caller as one rather than as a refusal.
	if svcErr := requireServiceError(t, err, "INTERNAL"); svcErr.Message == "" {
		t.Fatal("the failed hash was reported without a message")
	}

	stored, err := cp.Engine.GetUser(account.ID)
	if err != nil {
		t.Fatalf("GetUser after the patch: %v", err)
	}
	if stored.Status != model.UserStatusActive {
		t.Errorf("status after the failed hash: got %q, want %q (the patch must not be applied in part)",
			stored.Status, model.UserStatusActive)
	}
	if stored.PasswordHash != before.PasswordHash {
		t.Error("the stored password hash changed although the hash step failed")
	}
}
