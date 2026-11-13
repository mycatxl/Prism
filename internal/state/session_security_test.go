package state

import (
	"errors"
	"testing"

	"prism/internal/model"
)

func TestSessionCommitRejectsCredentialsChangedDuringLogin(t *testing.T) {
	for _, change := range []string{"password", "factor", "disabled", "reenabled"} {
		t.Run(change, func(t *testing.T) {
			repo := newTestStateRepo(t)
			verified := mustInsertUser(t, repo, "user", "alice")
			old := model.Session{IDHash: "before", UserID: verified.ID, ExpiresAtNs: 100}
			if ok, err := repo.InsertVerifiedSession(old, verified); err != nil || !ok {
				t.Fatalf("initial session %v %v", ok, err)
			}
			var err error
			switch change {
			case "password":
				err = repo.UpdateUserPassword(verified.ID, "new-password-hash", 1)
			case "factor":
				err = repo.UpdateVerifiedTOTP(verified, []byte("new-factor"), 1)
			case "reenabled":
				if err := repo.UpdateUserStatus(verified.ID, model.UserStatusDisabled, 1); err != nil {
					t.Fatal(err)
				}
				err = repo.UpdateUserStatus(verified.ID, model.UserStatusActive, 2)
			case "disabled":
				err = repo.UpdateUserStatus(verified.ID, model.UserStatusDisabled, 1)
			}
			if err != nil {
				t.Fatal(err)
			}
			late := old
			late.IDHash = "after"
			if ok, err := repo.InsertVerifiedSession(late, verified); err != nil || ok {
				t.Fatalf("obsolete credentials created session: %v %v", ok, err)
			}
			if _, err := repo.GetSession(late.IDHash); !errors.Is(err, ErrNotFound) {
				t.Fatal("late session persisted")
			}
			if _, err := repo.GetSession(old.IDHash); !errors.Is(err, ErrNotFound) {
				t.Fatal("security change retained an old session")
			}
		})
	}
}
