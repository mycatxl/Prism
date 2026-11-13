package state

import (
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/model"
)

// RecoverAdministrator creates a distinct administrator and its audit record in
// one transaction. Existing credentials, TOTP watermarks and customer state are
// untouched. The caller must own the state directory's exclusive offline lock.
func (r *StateRepo) RecoverAdministrator(user model.User, reason string) error {
	if user.ID == "" || strings.TrimSpace(reason) == "" || user.Role != model.UserRoleAdmin || user.Status != model.UserStatusActive || user.PasswordHash == "" {
		return fmt.Errorf("invalid offline recovery")
	}
	detail, _ := json.Marshal(map[string]string{"reason": reason, "method": "new administrator; existing factors and replay watermarks preserved"})
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`INSERT INTO users(id,username,password_hash,role,status,created_at_ns,updated_at_ns) VALUES(?,?,?,?,?,?,?)`, user.ID, user.Username, user.PasswordHash, user.Role, user.Status, user.CreatedAtNs, user.UpdatedAtNs); err != nil {
		return fmt.Errorf("recovery administrator could not be created; choose an unused name")
	}
	if _, err = tx.Exec(`INSERT INTO audit_log(at_ns,actor,user_id,remote_addr,action,target,detail_json) VALUES(?,'offline-owner',?,'local','offline.recover_admin',?,?)`, user.CreatedAtNs, user.ID, user.ID, string(detail)); err != nil {
		return err
	}
	return tx.Commit()
}
