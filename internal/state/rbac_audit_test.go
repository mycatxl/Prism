package state

import (
	"errors"
	"testing"

	"prism/internal/model"
)

func TestRoleChangeRevokesSessionsAtomically(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			r := newTestStateRepo(t)
			u := mustInsertUser(t, r, "u", "alice")
			for _, id := range []string{"browser-one", "browser-two"} {
				if err := r.InsertSession(model.Session{IDHash: id, UserID: u.ID, ExpiresAtNs: 100}); err != nil {
					t.Fatal(err)
				}
			}
			if fail {
				if _, err := r.db.Exec(`CREATE TRIGGER refuse_revoke BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT,'storage failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			err := r.UpdateUserRole(u.ID, model.UserRoleAdmin, 2)
			if (err != nil) != fail {
				t.Fatalf("change error=%v, injected failure=%v", err, fail)
			}
			after, err := r.GetUser(u.ID)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := r.ListSessionsByUser(u.ID)
			if err != nil {
				t.Fatal(err)
			}
			if fail && (after.Role != u.Role || len(rows) != 2) {
				t.Fatal("partial role change escaped rollback")
			}
			if !fail && (after.Role != model.UserRoleAdmin || len(rows) != 0) {
				t.Fatal("role change retained existing sessions")
			}
		})
	}
}

func TestPasswordChangeRejectsObsoleteAuthorization(t *testing.T) {
	for _, change := range []string{"password", "role", "status", "factor"} {
		t.Run(change, func(t *testing.T) {
			r := newTestStateRepo(t)
			verified := mustInsertUser(t, r, "u", "alice")
			var err error
			switch change {
			case "password":
				err = r.UpdateUserPassword(verified.ID, "operator-reset", verified.UpdatedAtNs)
			case "role":
				err = r.UpdateUserRole(verified.ID, model.UserRoleAdmin, verified.UpdatedAtNs)
			case "status":
				err = r.UpdateUserStatus(verified.ID, model.UserStatusDisabled, verified.UpdatedAtNs)
			case "factor":
				err = r.UpdateVerifiedTOTP(verified, []byte("factor"), verified.UpdatedAtNs)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := r.GetUser(verified.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.UpdateVerifiedPassword(verified, "stale-request", 3); !errors.Is(err, ErrConflict) {
				t.Fatalf("obsolete authorization accepted: %v", err)
			}
			after, err := r.GetUser(verified.ID)
			if err != nil || after.PasswordHash != before.PasswordHash {
				t.Fatalf("concurrent security write overwritten: %v", err)
			}
		})
	}
}
