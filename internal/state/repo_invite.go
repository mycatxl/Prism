package state

import (
	"database/sql"
	"errors"
	"fmt"

	"prism/internal/model"
)

// StateRepo methods for the invites table created by 000018_invites.
//
// Conventions match repo_user.go: writes take r.mu, a missing row comes back as
// ErrNotFound and a duplicate code as ErrConflict.
//
// The redeem methods are the interesting half. A use is claimed with one
// conditional UPDATE rather than a read followed by a write, so two
// registrations arriving at the same moment cannot both take the last use: the
// second UPDATE matches no row and is refused.

const inviteColumns = `id, code, plan_id, max_uses, used_count, expires_at_ns,
	note, status, created_by, created_at_ns, updated_at_ns`

// InsertInvite persists a new invite. A duplicate code returns ErrConflict.
func (r *StateRepo) InsertInvite(inv model.Invite) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
		INSERT INTO invites (id, code, plan_id, max_uses, used_count, expires_at_ns,
		                     note, status, created_by, created_at_ns, updated_at_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, inv.ID, inv.Code, inv.PlanID, inv.MaxUses, inv.UsedCount, inv.ExpiresAtNs,
		inv.Note, inv.Status, inv.CreatedBy, inv.CreatedAtNs, inv.UpdatedAtNs)
	if isSQLiteUniqueConstraint(err) {
		return fmt.Errorf("%w: invite code already exists", ErrConflict)
	}
	return err
}

// GetInvite returns one invite by id.
func (r *StateRepo) GetInvite(id string) (*model.Invite, error) {
	row := r.db.QueryRow("SELECT "+inviteColumns+" FROM invites WHERE id = ?", id)
	inv, err := scanInvite(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get invite: %w", err)
	}
	return &inv, nil
}

// GetInviteByCode returns one invite by its code. The code is what a registrant
// presents, so this is the lookup the registration path makes.
func (r *StateRepo) GetInviteByCode(code string) (*model.Invite, error) {
	row := r.db.QueryRow("SELECT "+inviteColumns+" FROM invites WHERE code = ?", code)
	inv, err := scanInvite(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get invite by code: %w", err)
	}
	return &inv, nil
}

// ListInvites returns every invite, newest first.
func (r *StateRepo) ListInvites() ([]model.Invite, error) {
	rows, err := r.db.Query("SELECT " + inviteColumns + " FROM invites ORDER BY created_at_ns DESC, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []model.Invite{}
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, inv)
	}
	return result, rows.Err()
}

// DeleteInvite removes an invite.
func (r *StateRepo) DeleteInvite(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM invites WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RedeemInviteByCode claims one use of the invite identified by code and
// returns the row afterwards.
//
// The claim is a single conditional UPDATE: it matches only while the invite is
// active, unexpired and has a use left, so concurrent callers cannot
// over-redeem. No row matched means the invite is unknown, revoked, expired or
// used up - the caller decides which by reading it, because the refusal message
// differs and reading it here would be a second, racy check.
func (r *StateRepo) RedeemInviteByCode(code string, nowNs int64) (*model.Invite, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(`
		UPDATE invites SET used_count = used_count + 1, updated_at_ns = ?
		WHERE code = ?
		  AND status = ?
		  AND (expires_at_ns = 0 OR expires_at_ns > ?)
		  AND (max_uses = 0 OR used_count < max_uses)
	`, nowNs, code, model.InviteStatusActive, nowNs)
	if err != nil {
		return nil, fmt.Errorf("redeem invite: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return r.getInviteByCodeLocked(code)
}

// RedeemInviteAndInsertUser claims one use of the invite and inserts the account
// in the same transaction.
//
// Registration is the one flow where burning a use without creating an account
// is worse than the reverse: an operator watching invite usage would see a use
// consumed by nobody. Doing both in one transaction means a failed insert rolls
// the claim back. The returned invite is the row as it stands after the claim.
func (r *StateRepo) RedeemInviteAndInsertUser(code string, nowNs int64, u model.User) (*model.Invite, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin registration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.Exec(`
		UPDATE invites SET used_count = used_count + 1, updated_at_ns = ?
		WHERE code = ?
		  AND status = ?
		  AND (expires_at_ns = 0 OR expires_at_ns > ?)
		  AND (max_uses = 0 OR used_count < max_uses)
	`, nowNs, code, model.InviteStatusActive, nowNs)
	if err != nil {
		return nil, fmt.Errorf("redeem invite: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}

	_, err = tx.Exec(`
		INSERT INTO users (id, username, password_hash, role, status, totp_secret_enc,
		                   created_at_ns, updated_at_ns, last_login_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, u.ID, u.Username, u.PasswordHash, u.Role, u.Status, u.TOTPSecretEnc,
		u.CreatedAtNs, u.UpdatedAtNs, u.LastLoginNs)
	if isSQLiteUniqueConstraint(err) {
		return nil, fmt.Errorf("%w: username already exists", ErrConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}

	row := tx.QueryRow("SELECT "+inviteColumns+" FROM invites WHERE code = ?", code)
	inv, err := scanInvite(row)
	if err != nil {
		return nil, fmt.Errorf("read redeemed invite: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit registration: %w", err)
	}
	return &inv, nil
}

// getInviteByCodeLocked reads an invite while r.mu is already held.
func (r *StateRepo) getInviteByCodeLocked(code string) (*model.Invite, error) {
	row := r.db.QueryRow("SELECT "+inviteColumns+" FROM invites WHERE code = ?", code)
	inv, err := scanInvite(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read redeemed invite: %w", err)
	}
	return &inv, nil
}

func scanInvite(row interface{ Scan(...any) error }) (model.Invite, error) {
	var inv model.Invite
	err := row.Scan(&inv.ID, &inv.Code, &inv.PlanID, &inv.MaxUses, &inv.UsedCount,
		&inv.ExpiresAtNs, &inv.Note, &inv.Status, &inv.CreatedBy,
		&inv.CreatedAtNs, &inv.UpdatedAtNs)
	return inv, err
}
