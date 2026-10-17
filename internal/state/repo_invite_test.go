package state

import (
	"errors"
	"testing"
	"time"

	"prism/internal/model"
)

func TestInviteRepo_CRUD(t *testing.T) {
	repo := newTestStateRepo(t)
	nowNs := time.Now().UnixNano()

	inv := model.Invite{
		ID:          "iv_test_01",
		Code:        "TEST-CODE",
		PlanID:      "plan_basic",
		MaxUses:     5,
		UsedCount:   0,
		ExpiresAtNs: nowNs + int64(7*24*time.Hour),
		Note:        "test invite note",
		Status:      model.InviteStatusActive,
		CreatedBy:   "admin",
		CreatedAtNs: nowNs,
		UpdatedAtNs: nowNs,
	}

	// 1. InsertInvite
	if err := repo.InsertInvite(inv); err != nil {
		t.Fatalf("InsertInvite failed: %v", err)
	}

	// Duplicate code returns ErrConflict
	dup := inv
	dup.ID = "iv_test_02"
	if err := repo.InsertInvite(dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("InsertInvite duplicate code: got %v, want ErrConflict", err)
	}

	// 2. GetInviteByCode
	gotByCode, err := repo.GetInviteByCode("TEST-CODE")
	if err != nil {
		t.Fatalf("GetInviteByCode failed: %v", err)
	}
	if gotByCode.ID != inv.ID || gotByCode.Code != inv.Code || gotByCode.PlanID != inv.PlanID ||
		gotByCode.MaxUses != inv.MaxUses || gotByCode.UsedCount != inv.UsedCount ||
		gotByCode.ExpiresAtNs != inv.ExpiresAtNs || gotByCode.Note != inv.Note ||
		gotByCode.Status != inv.Status || gotByCode.CreatedBy != inv.CreatedBy {
		t.Fatalf("GetInviteByCode mismatched: got %+v, want %+v", gotByCode, inv)
	}

	// GetInvite by ID
	gotByID, err := repo.GetInvite(inv.ID)
	if err != nil {
		t.Fatalf("GetInvite failed: %v", err)
	}
	if gotByID.Code != inv.Code {
		t.Fatalf("GetInvite code mismatched: got %s, want %s", gotByID.Code, inv.Code)
	}

	// ListInvites
	list, err := repo.ListInvites()
	if err != nil {
		t.Fatalf("ListInvites failed: %v", err)
	}
	if len(list) != 1 || list[0].ID != inv.ID {
		t.Fatalf("ListInvites unexpected result: got %+v", list)
	}

	// 3. DeleteInvite
	if err := repo.DeleteInvite(inv.ID); err != nil {
		t.Fatalf("DeleteInvite failed: %v", err)
	}

	if _, err := repo.GetInviteByCode("TEST-CODE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetInviteByCode after delete: got %v, want ErrNotFound", err)
	}

	if _, err := repo.GetInvite(inv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetInvite after delete: got %v, want ErrNotFound", err)
	}

	if err := repo.DeleteInvite(inv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteInvite on non-existent row: got %v, want ErrNotFound", err)
	}
}

func TestInviteRepo_RedeemInviteByCode(t *testing.T) {
	repo := newTestStateRepo(t)
	pinnedNow := int64(1700000000_000000000)

	t.Run("succeeds once for max_uses 1 then fails", func(t *testing.T) {
		inv := model.Invite{
			ID:          "iv_max1",
			Code:        "MAX1-CODE",
			MaxUses:     1,
			UsedCount:   0,
			ExpiresAtNs: 0, // never expires
			Status:      model.InviteStatusActive,
			CreatedAtNs: pinnedNow,
			UpdatedAtNs: pinnedNow,
		}
		if err := repo.InsertInvite(inv); err != nil {
			t.Fatalf("InsertInvite: %v", err)
		}

		// First redemption must succeed
		redeemed, err := repo.RedeemInviteByCode("MAX1-CODE", pinnedNow+10)
		if err != nil {
			t.Fatalf("RedeemInviteByCode first attempt failed: %v", err)
		}
		if redeemed.UsedCount != 1 {
			t.Fatalf("expected used_count 1, got %d", redeemed.UsedCount)
		}

		// Second redemption must fail because max_uses reached
		if _, err := repo.RedeemInviteByCode("MAX1-CODE", pinnedNow+20); !errors.Is(err, ErrNotFound) {
			t.Fatalf("RedeemInviteByCode second attempt: got %v, want ErrNotFound", err)
		}
	})

	t.Run("refuses expired invite", func(t *testing.T) {
		inv := model.Invite{
			ID:          "iv_expired",
			Code:        "EXPR-CODE",
			MaxUses:     10,
			UsedCount:   0,
			ExpiresAtNs: pinnedNow - 1000, // already expired
			Status:      model.InviteStatusActive,
			CreatedAtNs: pinnedNow - 2000,
			UpdatedAtNs: pinnedNow - 2000,
		}
		if err := repo.InsertInvite(inv); err != nil {
			t.Fatalf("InsertInvite: %v", err)
		}

		if _, err := repo.RedeemInviteByCode("EXPR-CODE", pinnedNow); !errors.Is(err, ErrNotFound) {
			t.Fatalf("RedeemInviteByCode on expired: got %v, want ErrNotFound", err)
		}
	})

	t.Run("refuses disabled invite", func(t *testing.T) {
		inv := model.Invite{
			ID:          "iv_disabled",
			Code:        "DISA-CODE",
			MaxUses:     10,
			UsedCount:   0,
			ExpiresAtNs: 0,
			Status:      model.InviteStatusDisabled,
			CreatedAtNs: pinnedNow,
			UpdatedAtNs: pinnedNow,
		}
		if err := repo.InsertInvite(inv); err != nil {
			t.Fatalf("InsertInvite: %v", err)
		}

		if _, err := repo.RedeemInviteByCode("DISA-CODE", pinnedNow); !errors.Is(err, ErrNotFound) {
			t.Fatalf("RedeemInviteByCode on disabled: got %v, want ErrNotFound", err)
		}
	})
}

func TestInviteRepo_RedeemInviteAndInsertUser_Atomicity(t *testing.T) {
	repo := newTestStateRepo(t)
	pinnedNow := int64(1700000000_000000000)

	// Pre-insert user "alice"
	existingUser := model.User{
		ID:           "u_alice",
		Username:     "alice",
		PasswordHash: "dummy-hash",
		Role:         model.UserRoleUser,
		Status:       model.UserStatusActive,
		CreatedAtNs:  pinnedNow,
		UpdatedAtNs:  pinnedNow,
	}
	if err := repo.InsertUser(existingUser); err != nil {
		t.Fatalf("InsertUser(alice): %v", err)
	}

	// Insert invite with max_uses = 1
	inv := model.Invite{
		ID:          "iv_atomic",
		Code:        "ATOM-CODE",
		PlanID:      "plan_p1",
		MaxUses:     1,
		UsedCount:   0,
		ExpiresAtNs: 0,
		Status:      model.InviteStatusActive,
		CreatedAtNs: pinnedNow,
		UpdatedAtNs: pinnedNow,
	}
	if err := repo.InsertInvite(inv); err != nil {
		t.Fatalf("InsertInvite: %v", err)
	}

	// Attempt registration with duplicate username "alice"
	conflictingUser := model.User{
		ID:           "u_alice_duplicate",
		Username:     "alice",
		PasswordHash: "another-hash",
		Role:         model.UserRoleUser,
		Status:       model.UserStatusActive,
		CreatedAtNs:  pinnedNow + 1,
		UpdatedAtNs:  pinnedNow + 1,
	}
	_, err := repo.RedeemInviteAndInsertUser("ATOM-CODE", pinnedNow+1, conflictingUser)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("RedeemInviteAndInsertUser on duplicate username: got %v, want ErrConflict", err)
	}

	// Assert no new user row was created for "u_alice_duplicate"
	if _, err := repo.GetUser("u_alice_duplicate"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUser(u_alice_duplicate) should not exist, got: %v", err)
	}

	// Assert the invite's used_count was rolled back and is STILL 0
	fetchedInv, err := repo.GetInviteByCode("ATOM-CODE")
	if err != nil {
		t.Fatalf("GetInviteByCode: %v", err)
	}
	if fetchedInv.UsedCount != 0 {
		t.Fatalf("invite used_count expected 0 after rollback, got %d", fetchedInv.UsedCount)
	}

	// Now register a new user "bob" using the same invite - must succeed
	bobUser := model.User{
		ID:           "u_bob",
		Username:     "bob",
		PasswordHash: "bob-hash",
		Role:         model.UserRoleUser,
		Status:       model.UserStatusActive,
		CreatedAtNs:  pinnedNow + 2,
		UpdatedAtNs:  pinnedNow + 2,
	}
	redeemedInv, err := repo.RedeemInviteAndInsertUser("ATOM-CODE", pinnedNow+2, bobUser)
	if err != nil {
		t.Fatalf("RedeemInviteAndInsertUser(bob) failed: %v", err)
	}
	if redeemedInv.UsedCount != 1 {
		t.Fatalf("redeemed invite used_count expected 1, got %d", redeemedInv.UsedCount)
	}

	// Check that bob exists in users table
	gotBob, err := repo.GetUser("u_bob")
	if err != nil {
		t.Fatalf("GetUser(u_bob): %v", err)
	}
	if gotBob.Username != "bob" {
		t.Fatalf("GetUser(u_bob) username expected bob, got %s", gotBob.Username)
	}

	// Verify that now the invite is exhausted and cannot be used again
	charlieUser := model.User{
		ID:           "u_charlie",
		Username:     "charlie",
		PasswordHash: "charlie-hash",
		Role:         model.UserRoleUser,
		Status:       model.UserStatusActive,
		CreatedAtNs:  pinnedNow + 3,
		UpdatedAtNs:  pinnedNow + 3,
	}
	if _, err := repo.RedeemInviteAndInsertUser("ATOM-CODE", pinnedNow+3, charlieUser); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RedeemInviteAndInsertUser after exhausted: got %v, want ErrNotFound", err)
	}
	if _, err := repo.GetUser("u_charlie"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("charlie user row should not exist, got %v", err)
	}
}
