package state

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"strings"

	"prism/internal/model"
)

// UpdateVerifiedPassword binds a password change to the credentials that
// authorized it. Another writer's reset must win over an obsolete password.
func (r *StateRepo) UpdateVerifiedPassword(verified model.User, hash string, atNs int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanUser(tx.QueryRow("SELECT "+userColumns+" FROM users WHERE id=?", verified.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	passwordMatches := subtle.ConstantTimeCompare([]byte(current.PasswordHash), []byte(verified.PasswordHash))
	factorMatches := subtle.ConstantTimeCompare(current.TOTPSecretEnc, verified.TOTPSecretEnc)
	if current.Status != verified.Status || current.Role != verified.Role || current.UpdatedAtNs != verified.UpdatedAtNs || passwordMatches&factorMatches != 1 {
		return ErrConflict
	}
	if _, err := tx.Exec("UPDATE users SET password_hash=?,updated_at_ns=? WHERE id=?", hash, atNs, verified.ID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM sessions WHERE user_id=?", verified.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// AdvanceVerifiedTOTPLastUsed binds replay consumption to the credentials that
// verified the code. A login racing a factor replacement must not consume the
// replacement's freshly reset ledger before its stale session is refused.
func (r *StateRepo) AdvanceVerifiedTOTPLastUsed(verified model.User, stepNs int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanUser(tx.QueryRow("SELECT "+userColumns+" FROM users WHERE id=?", verified.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	passwordMatches := subtle.ConstantTimeCompare([]byte(current.PasswordHash), []byte(verified.PasswordHash))
	factorMatches := subtle.ConstantTimeCompare(current.TOTPSecretEnc, verified.TOTPSecretEnc)
	if current.Status != model.UserStatusActive || current.Role != verified.Role || current.UpdatedAtNs != verified.UpdatedAtNs || len(current.TOTPSecretEnc) == 0 || passwordMatches&factorMatches != 1 {
		return false, nil
	}
	result, err := tx.Exec("UPDATE users SET totp_last_used_ns=? WHERE id=? AND totp_last_used_ns<?", stepNs, verified.ID, stepNs)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n > 0, nil
}

// InsertVerifiedSession commits a login only while the credentials it verified
// are still current. The transaction prevents a password reset or factor change
// between the comparison and insertion from reviving an obsolete login.
func (r *StateRepo) InsertVerifiedSession(s model.Session, verified model.User) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanUser(tx.QueryRow("SELECT "+userColumns+" FROM users WHERE id = ?", s.UserID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	passwordMatches := subtle.ConstantTimeCompare([]byte(current.PasswordHash), []byte(verified.PasswordHash))
	factorMatches := subtle.ConstantTimeCompare(current.TOTPSecretEnc, verified.TOTPSecretEnc)
	if s.UserID != verified.ID || current.Status != model.UserStatusActive || current.UpdatedAtNs != verified.UpdatedAtNs || current.Role != verified.Role || passwordMatches&factorMatches != 1 {
		return false, nil
	}
	_, err = tx.Exec(`INSERT INTO sessions (id_hash, user_id, created_at_ns, expires_at_ns, ip, ua)
		VALUES (?, ?, ?, ?, ?, ?)`, s.IDHash, s.UserID, s.CreatedAtNs, s.ExpiresAtNs, s.IP, s.UA)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// UpdateVerifiedTOTP atomically replaces the factor and its replay ledger. A
// change also revokes sessions. A concurrent credential change invalidates the
// authorization snapshot rather than allowing an old password to remove MFA.
func (r *StateRepo) UpdateVerifiedTOTP(verified model.User, sealed []byte, atNs int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanUser(tx.QueryRow("SELECT "+userColumns+" FROM users WHERE id=?", verified.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current.Status != model.UserStatusActive || current.UpdatedAtNs != verified.UpdatedAtNs || subtle.ConstantTimeCompare([]byte(current.PasswordHash), []byte(verified.PasswordHash)) != 1 || subtle.ConstantTimeCompare(current.TOTPSecretEnc, verified.TOTPSecretEnc) != 1 {
		return ErrConflict
	}
	if _, err := tx.Exec("UPDATE users SET totp_secret_enc=?,totp_last_used_ns=0,updated_at_ns=? WHERE id=?", sealed, atNs, verified.ID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM sessions WHERE user_id=?", verified.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateUserSecurityPatch commits an administrator's account changes and
// revocation together, so failure cannot leave a partially applied PATCH.
func (r *StateRepo) UpdateUserSecurityPatch(id string, status, role, passwordHash *string, atNs int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	columns := []string{"updated_at_ns=?"}
	args := []any{atNs}
	for _, v := range []struct {
		column string
		value  *string
	}{{"status", status}, {"role", role}, {"password_hash", passwordHash}} {
		if v.value != nil {
			columns = append(columns, v.column+"=?")
			args = append(args, *v.value)
		}
	}
	args = append(args, id)
	result, err := tx.Exec("UPDATE users SET "+strings.Join(columns, ",")+" WHERE id=?", args...)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec("DELETE FROM sessions WHERE user_id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}
