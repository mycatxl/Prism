package state

import (
	"errors"
	"testing"

	"prism/internal/model"
)

// Tests for the follow-up review's user and subscription writes: the
// single-column writers that replaced the whole-row write-back, the replay
// ledger reset that a second-factor rotation needs, and the rows-affected
// question the two subscription writers answered in opposite directions.

// --- F4: the console's account writes are single-column writes ---

// mustInsertReviewSubscription writes one active subscription for a user.
func mustInsertReviewSubscription(t *testing.T, repo *StateRepo, userID, planID string) model.UserSubscription {
	t.Helper()
	if err := repo.UpsertPlan(model.Plan{ID: planID, Name: "Review", Status: "active", DurationDays: 30, CreatedAtNs: 1, UpdatedAtNs: 1}); err != nil {
		t.Fatalf("UpsertPlan(%s): %v", planID, err)
	}
	sub := model.UserSubscription{
		ID: "us-" + userID, UserID: userID, PlanID: planID,
		PlanSnapshotJSON: `{"id":"` + planID + `","name":"Review","status":"active"}`,
		StartsAtNs:       10, ExpiresAtNs: 5_000, PeriodStartNs: 10, PeriodUsedBytes: 7,
		Status: model.SubscriptionStatusActive,
	}
	if err := repo.UpsertUserSubscription(sub); err != nil {
		t.Fatalf("UpsertUserSubscription(%s): %v", userID, err)
	}
	return sub
}

// TestUserSingleColumnWriters_KeepTheOtherColumns is the property that replaced
// the whole-row write-back: an administrator changing a password and another one
// disabling the account must both survive, whichever write lands second.
//
// UpdateUserPassword is handed only the hash and the account id, so it cannot
// carry a status the caller read tens of milliseconds earlier - which is what
// the argon2id hash it was produced by takes.
func TestUserSingleColumnWriters_KeepTheOtherColumns(t *testing.T) {
	repo := newTestStateRepo(t)
	user := mustInsertUser(t, repo, "u-cols", "cols")

	if err := repo.UpdateUserStatus(user.ID, model.UserStatusDisabled, 1_111); err != nil {
		t.Fatalf("UpdateUserStatus: %v", err)
	}
	if err := repo.UpdateUserRole(user.ID, model.UserRoleAdmin, 1_112); err != nil {
		t.Fatalf("UpdateUserRole: %v", err)
	}
	if err := repo.UpdateUserPassword(user.ID, "$argon2id$new", 1_113); err != nil {
		t.Fatalf("UpdateUserPassword: %v", err)
	}
	if err := repo.UpdateUserTOTPSecret(user.ID, []byte("sealed"), 1_114); err != nil {
		t.Fatalf("UpdateUserTOTPSecret: %v", err)
	}

	stored, err := repo.GetUser(user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if stored.Status != model.UserStatusDisabled {
		t.Errorf("status after a password write: got %q, want %q", stored.Status, model.UserStatusDisabled)
	}
	if stored.Role != model.UserRoleAdmin {
		t.Errorf("role after a password write: got %q, want %q", stored.Role, model.UserRoleAdmin)
	}
	if stored.PasswordHash != "$argon2id$new" {
		t.Errorf("password hash: got %q, want the value written", stored.PasswordHash)
	}
	if string(stored.TOTPSecretEnc) != "sealed" {
		t.Errorf("second factor: got %q, want %q", stored.TOTPSecretEnc, "sealed")
	}
	if stored.Username != "cols" {
		t.Errorf("username: got %q, want %q (a password write must not rename)", stored.Username, "cols")
	}
	if stored.UpdatedAtNs != 1_114 {
		t.Errorf("updated_at_ns: got %d, want %d", stored.UpdatedAtNs, int64(1_114))
	}

	// Clearing the second factor writes a NULL, and the row still round-trips.
	if err := repo.UpdateUserTOTPSecret(user.ID, nil, 1_115); err != nil {
		t.Fatalf("UpdateUserTOTPSecret (clear): %v", err)
	}
	stored, err = repo.GetUser(user.ID)
	if err != nil {
		t.Fatalf("GetUser after clearing the second factor: %v", err)
	}
	if len(stored.TOTPSecretEnc) != 0 {
		t.Errorf("second factor after clearing: got %q, want empty", stored.TOTPSecretEnc)
	}

	// Every one of them reports a missing account the same way.
	for name, err := range map[string]error{
		"password": repo.UpdateUserPassword("u-missing", "x", 1),
		"status":   repo.UpdateUserStatus("u-missing", model.UserStatusActive, 1),
		"role":     repo.UpdateUserRole("u-missing", model.UserRoleUser, 1),
		"totp":     repo.UpdateUserTOTPSecret("u-missing", nil, 1),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("writing the %s of a missing account: got %v, want ErrNotFound", name, err)
		}
	}
}

// --- F5: a rotated second factor gets its step back ---

// TestResetTOTPLastUsed_LetsTheNewSecretUseTheSameStep is the state half of the
// second-factor rotation: the ledger is monotone, so a fresh secret enrolled
// inside a step the account already spent would refuse its first code as a
// replay.
func TestResetTOTPLastUsed_LetsTheNewSecretUseTheSameStep(t *testing.T) {
	repo := newTestStateRepo(t)
	user := mustInsertUser(t, repo, "u-rotate", "rotate")

	const step = int64(1_700_000_000_000_000_000) / 30_000_000_000 * 30_000_000_000

	accepted, err := repo.AdvanceTOTPLastUsed(user.ID, step)
	if err != nil {
		t.Fatalf("AdvanceTOTPLastUsed: %v", err)
	}
	if !accepted {
		t.Fatal("the first use of the step was refused")
	}
	// The ledger only moves forward: the same step cannot be spent twice.
	accepted, err = repo.AdvanceTOTPLastUsed(user.ID, step)
	if err != nil {
		t.Fatalf("AdvanceTOTPLastUsed (again): %v", err)
	}
	if accepted {
		t.Fatal("the same step was accepted twice")
	}

	if err := repo.ResetTOTPLastUsed(user.ID); err != nil {
		t.Fatalf("ResetTOTPLastUsed: %v", err)
	}
	stored, err := repo.GetUser(user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if stored.TOTPLastUsedNs != 0 {
		t.Fatalf("totp_last_used_ns after the reset: got %d, want 0", stored.TOTPLastUsedNs)
	}

	// The step the old secret spent is spendable again by the new one.
	accepted, err = repo.AdvanceTOTPLastUsed(user.ID, step)
	if err != nil {
		t.Fatalf("AdvanceTOTPLastUsed after the reset: %v", err)
	}
	if !accepted {
		t.Error("the step was still counted as spent after the ledger was cleared")
	}

	if err := repo.ResetTOTPLastUsed("u-missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("resetting the ledger of a missing account: got %v, want ErrNotFound", err)
	}
}

// TestResetTOTPLastUsed_AlreadyZeroIsASuccess covers the case the clearing is
// called in: an account that never signed in with a second factor enrols one,
// and its ledger already reads zero.
func TestResetTOTPLastUsed_AlreadyZeroIsASuccess(t *testing.T) {
	repo := newTestStateRepo(t)
	user := mustInsertUser(t, repo, "u-fresh", "fresh")

	if err := repo.ResetTOTPLastUsed(user.ID); err != nil {
		t.Fatalf("clearing the ledger of an account that never spent a step: %v", err)
	}
	if err := repo.ResetTOTPLastUsed(user.ID); err != nil {
		t.Errorf("clearing it a second time: %v", err)
	}
	if err := repo.ResetTOTPLastUsed("u-missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("clearing the ledger of a missing account: got %v, want ErrNotFound", err)
	}
}

// --- F7: what SQLite's rows-affected means for the two subscription writers ---

// TestUpdateUserSubscriptionTerms_UnchangedTermsAreNotAMissingRow settles the
// question the two writers answered in opposite directions.
//
// The console's PATCH {"status":"active"} on a row that is already active writes
// the values the row already holds. If SQLite reported zero rows for that, this
// write would be indistinguishable from "there is no such row", and a legal
// request would come back as a 404.
func TestUpdateUserSubscriptionTerms_UnchangedTermsAreNotAMissingRow(t *testing.T) {
	repo := newTestStateRepo(t)
	mustInsertUser(t, repo, "u-terms", "terms")
	sub := mustInsertReviewSubscription(t, repo, "u-terms", "p-terms")

	// The same terms, written twice.
	if err := repo.UpdateUserSubscriptionTerms(sub); err != nil {
		t.Fatalf("rewriting a row with the terms it already holds: %v", err)
	}

	stored, err := repo.GetUserSubscription(sub.ID)
	if err != nil {
		t.Fatalf("GetUserSubscription: %v", err)
	}
	if stored.Status != model.SubscriptionStatusActive || stored.PlanID != sub.PlanID {
		t.Errorf("the row after an unchanged write: got status %q plan %q, want active on %s",
			stored.Status, stored.PlanID, sub.PlanID)
	}
	// The counter is not a term, and no terms write may touch it.
	if stored.PeriodUsedBytes != 7 {
		t.Errorf("period_used_bytes after a terms write: got %d, want 7", stored.PeriodUsedBytes)
	}

	// A row that really is absent is still ErrNotFound.
	missing := sub
	missing.ID = "us-missing"
	if err := repo.UpdateUserSubscriptionTerms(missing); !errors.Is(err, ErrNotFound) {
		t.Errorf("rewriting a missing row: got %v, want ErrNotFound", err)
	}

	// And the second writer agrees: revoking twice is not an error, and only a
	// row that is really absent is ErrNotFound.
	if err := repo.RevokeUserSubscription(sub.ID); err != nil {
		t.Fatalf("RevokeUserSubscription: %v", err)
	}
	if err := repo.RevokeUserSubscription(sub.ID); err != nil {
		t.Errorf("revoking an already suspended row: %v", err)
	}
	if err := repo.RevokeUserSubscription("us-missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking a missing row: got %v, want ErrNotFound", err)
	}
}
