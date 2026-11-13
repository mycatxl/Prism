package state

import (
	"testing"
	"time"

	"prism/internal/auth"
	"prism/internal/model"
)

type rotatingFactorStore struct {
	*StateRepo
	rotate func()
}

func (s *rotatingFactorStore) AdvanceVerifiedTOTPLastUsed(verified model.User, stepNs int64) (bool, error) {
	if s.rotate != nil {
		rotate := s.rotate
		s.rotate = nil
		rotate()
	}
	return s.StateRepo.AdvanceVerifiedTOTPLastUsed(verified, stepNs)
}

func TestStaleLoginCannotSpendReplacementFactorStep(t *testing.T) {
	repo := newTestStateRepo(t)
	pepper, err := auth.NewPepper(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("round-two-password")
	if err != nil {
		t.Fatal(err)
	}
	oldSecret, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	newSecret, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := auth.EncryptTOTPSecretForUser(pepper, "alice", oldSecret)
	if err != nil {
		t.Fatal(err)
	}
	user := model.User{ID: "alice", Username: "alice", PasswordHash: hash, Status: model.UserStatusActive, Role: model.UserRoleUser, TOTPSecretEnc: sealed, UpdatedAtNs: 1}
	if err := repo.InsertUser(user); err != nil {
		t.Fatal(err)
	}
	store := &rotatingFactorStore{StateRepo: repo, rotate: func() {
		sealed, err := auth.EncryptTOTPSecretForUser(pepper, user.ID, newSecret)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.UpdateVerifiedTOTP(user, sealed, 2); err != nil {
			t.Fatal(err)
		}
	}}
	now := time.Unix(1_700_000_000, 0)
	svc := &auth.Service{Store: store, Pepper: pepper, Now: func() int64 { return now.UnixNano() }}
	oldCode, err := auth.TOTPCode(oldSecret, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login("alice", "round-two-password", oldCode, "", "", ""); err == nil {
		t.Fatal("stale factor opened a session")
	}
	after, err := repo.GetUser(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.TOTPLastUsedNs != 0 {
		t.Error("stale login spent the replacement factor's replay step")
	}
	newCode, err := auth.TOTPCode(newSecret, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login("alice", "round-two-password", newCode, "", "", ""); err != nil {
		t.Fatalf("replacement factor login refused: %v", err)
	}
}
