package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"prism/internal/auth"
	"prism/internal/model"
)

// Account errors. They name what is wrong with a request about an account, as
// opposed to what is wrong with a credential, which is the auth package's
// business.
var (
	// ErrPasswordTooShort is returned when a new password does not meet the
	// policy.
	ErrPasswordTooShort = fmt.Errorf("service: the password must be at least %d characters", MinPasswordLength)
	// ErrPasswordUnchanged is returned when a change would leave the password
	// as it was, which usually means the caller sent the wrong field.
	ErrPasswordUnchanged = errors.New("service: the new password is the same as the current one")
	// ErrCurrentPasswordWrong is returned when a password change presents the
	// wrong current password.
	ErrCurrentPasswordWrong = errors.New("service: the current password is wrong")
	// ErrTOTPAlreadyEnrolled is returned when an account that already has a
	// second factor is asked to enrol another one. Removing it first is a
	// deliberate act, not something to do by accident.
	ErrTOTPAlreadyEnrolled = errors.New("service: the account already has a second factor")
	// ErrTOTPNotEnrolled is returned when a second factor is removed from an
	// account that has none.
	ErrTOTPNotEnrolled = errors.New("service: the account has no second factor")
	// ErrTOTPCodeInvalid is returned when the confirming code does not match
	// the secret being enrolled.
	ErrTOTPCodeInvalid = errors.New("service: the second factor code does not match the secret")
	// ErrNoPepper is returned when a second factor cannot be stored or read
	// because no pepper is configured.
	ErrNoPepper = errors.New("service: no pepper is configured, so secrets cannot be sealed")
	// ErrLastAdmin is returned when an operation would leave no usable
	// administrator, which would make the console unreachable.
	ErrLastAdmin = errors.New("service: this would leave no active administrator")
	// ErrUsernameRequired is returned when an account write carries no
	// username. The name is the one identifier a login has to have, so there
	// is no default for it.
	ErrUsernameRequired = errors.New("service: the username must not be empty")
	// ErrUnknownRole is returned when a role is not one the console knows.
	// It is wrapped with the offending value so the message names it.
	ErrUnknownRole = errors.New("service: unknown role")
	// ErrUnknownStatus is returned when an account status is not one the
	// console knows.
	ErrUnknownStatus = errors.New("service: unknown status")
)

// MinPasswordLength is the shortest password the console accepts.
//
// Length rather than a character-class rule: composition requirements push people
// towards "Password1!" and a password manager's twenty random characters satisfy
// a length rule and nothing else. Twelve is the shortest length that is
// comfortably beyond online guessing at the rate this login endpoint allows.
const MinPasswordLength = 12

// Account is an account as the console sees it. It deliberately has no password
// hash and no sealed second factor: neither is ever rendered, so neither is ever
// in a response body.
type Account struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	HasTOTP     bool   `json:"has_totp"`
	CreatedAtNs int64  `json:"created_at_ns"`
	UpdatedAtNs int64  `json:"updated_at_ns"`
	LastLoginNs int64  `json:"last_login_ns"`
}

func accountFromModel(u model.User) Account {
	return Account{
		ID:          u.ID,
		Username:    u.Username,
		Role:        u.Role,
		Status:      u.Status,
		HasTOTP:     len(u.TOTPSecretEnc) > 0,
		CreatedAtNs: u.CreatedAtNs,
		UpdatedAtNs: u.UpdatedAtNs,
		LastLoginNs: u.LastLoginNs,
	}
}

// ErrAccountNotFound is returned when an account does not exist.
var ErrAccountNotFound = errors.New("service: account not found")

// GetAccount returns one account.
func (s *ControlPlaneService) GetAccount(id string) (Account, error) {
	if s == nil || s.Engine == nil {
		return Account{}, errors.New("service: no state engine")
	}
	user, err := s.Engine.GetUser(id)
	if err != nil || user == nil {
		return Account{}, ErrAccountNotFound
	}
	return accountFromModel(*user), nil
}

// ListAccounts returns every account, newest first.
func (s *ControlPlaneService) ListAccounts() ([]Account, error) {
	if s == nil || s.Engine == nil {
		return nil, errors.New("service: no state engine")
	}
	users, err := s.Engine.ListUsers()
	if err != nil {
		return nil, err
	}
	accounts := make([]Account, 0, len(users))
	for _, user := range users {
		accounts = append(accounts, accountFromModel(user))
	}
	return accounts, nil
}

// CreateAccount adds an account.
//
// It is what `prism init` and the admin console both call, so the policy lives
// here rather than being restated at each entry point.
func (s *ControlPlaneService) CreateAccount(username, password, role string, nowNs int64) (Account, error) {
	if s == nil || s.Engine == nil {
		return Account{}, errors.New("service: no state engine")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return Account{}, ErrUsernameRequired
	}
	if role != model.UserRoleAdmin && role != model.UserRoleUser {
		return Account{}, fmt.Errorf("%w %q", ErrUnknownRole, role)
	}
	if err := checkPasswordPolicy(password); err != nil {
		return Account{}, err
	}
	hash, err := s.hashPassword(password)
	if err != nil {
		return Account{}, err
	}
	id, err := newAccountID()
	if err != nil {
		return Account{}, err
	}
	user := model.User{
		ID:           id,
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		Status:       model.UserStatusActive,
		CreatedAtNs:  nowNs,
		UpdatedAtNs:  nowNs,
	}
	if err := s.Engine.InsertUser(user); err != nil {
		return Account{}, err
	}
	return accountFromModel(user), nil
}

// ChangeAccountPassword replaces an account's password after checking the
// current one.
//
// The account's existing sessions are the caller's business: this changes the
// credential, and the caller revokes what it needs to. Splitting those two would
// make it possible to change a password and forget the revocation, so the API
// handler does both and this stays a single, testable operation.
//
// The read, the check against the hash it carries and the write are one locked
// operation (userWriteMu): a status or role change landing in between would
// otherwise be decided against a row that is about to be a different one.
func (s *ControlPlaneService) ChangeAccountPassword(userID, currentPassword, newPassword string, nowNs int64) error {
	if s == nil || s.Engine == nil {
		return errors.New("service: no state engine")
	}
	if err := checkPasswordPolicy(newPassword); err != nil {
		return err
	}
	if currentPassword == newPassword {
		return ErrPasswordUnchanged
	}
	s.userWriteMu.Lock()
	defer s.userWriteMu.Unlock()

	user, err := s.Engine.GetUser(userID)
	if err != nil || user == nil {
		return ErrAccountNotFound
	}
	if err := auth.VerifyPassword(user.PasswordHash, currentPassword); err != nil {
		return ErrCurrentPasswordWrong
	}
	return s.setPassword(user, newPassword, nowNs)
}

// SetAccountPassword replaces an account's password without asking for the
// current one.
//
// It exists for the paths where the caller has already established authority by
// other means: the initial administrator an operator creates from the server
// shell, and an administrator resetting another account. Both are audited; the
// console's own change flow uses ChangeAccountPassword instead.
//
// The write is still serialised with every other account write (userWriteMu): a
// reset is a write to a row another administrator may be writing at the same
// moment.
func (s *ControlPlaneService) SetAccountPassword(userID, newPassword string, nowNs int64) error {
	if s == nil || s.Engine == nil {
		return errors.New("service: no state engine")
	}
	if err := checkPasswordPolicy(newPassword); err != nil {
		return err
	}
	s.userWriteMu.Lock()
	defer s.userWriteMu.Unlock()

	user, err := s.Engine.GetUser(userID)
	if err != nil || user == nil {
		return ErrAccountNotFound
	}
	return s.setPassword(user, newPassword, nowNs)
}

// setPassword hashes the new password and writes it, and only it, into the
// account's row.
//
// The caller holds userWriteMu. The write is one column
// (state.UpdateUserPassword) rather than the whole row: the caller read the row
// before hashing, and hashing is tens of milliseconds of argon2id, so a
// whole-row write-back would silently revert every other column written during
// that window - the one outcome a password reset, in particular one made
// because the password leaked, must never have.
func (s *ControlPlaneService) setPassword(user *model.User, newPassword string, nowNs int64) error {
	hash, err := s.hashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.Engine.UpdateUserPassword(user.ID, hash, nowNs)
}

// SetAccountStatus enables or disables an account.
//
// Disabling the last active administrator is refused: it would leave the console
// with nobody able to use it, which is not recoverable from inside the product.
// The count and the write are one locked operation (userWriteMu), so two
// concurrent requests cannot each see the other as the one still standing.
func (s *ControlPlaneService) SetAccountStatus(id, status string, nowNs int64) error {
	if s == nil || s.Engine == nil {
		return errors.New("service: no state engine")
	}
	if status != model.UserStatusActive && status != model.UserStatusDisabled {
		return fmt.Errorf("%w %q", ErrUnknownStatus, status)
	}
	s.userWriteMu.Lock()
	defer s.userWriteMu.Unlock()

	return s.setAccountStatusLocked(id, status, nowNs)
}

// setAccountStatusLocked is SetAccountStatus without the mutex: the caller
// holds userWriteMu (SetAccountStatus, and UpdateTenantUser, which applies a
// whole patch under one lock).
func (s *ControlPlaneService) setAccountStatusLocked(id, status string, nowNs int64) error {
	user, err := s.Engine.GetUser(id)
	if err != nil || user == nil {
		return ErrAccountNotFound
	}
	if user.Status == status {
		return nil
	}
	if status == model.UserStatusDisabled {
		if err := s.refuseIfLastAdmin(user); err != nil {
			return err
		}
	}
	// A single column: a status change must not write back the password hash,
	// the role or the second factor the caller read alongside it.
	return s.Engine.UpdateUserStatus(id, status, nowNs)
}

// SetAccountRole changes an account's role.
//
// Demoting the last administrator is refused for the same reason disabling one
// is, and the count and the write are one locked operation (userWriteMu).
func (s *ControlPlaneService) SetAccountRole(id, role string, nowNs int64) error {
	if s == nil || s.Engine == nil {
		return errors.New("service: no state engine")
	}
	if role != model.UserRoleAdmin && role != model.UserRoleUser {
		return fmt.Errorf("%w %q", ErrUnknownRole, role)
	}
	s.userWriteMu.Lock()
	defer s.userWriteMu.Unlock()

	return s.setAccountRoleLocked(id, role, nowNs)
}

// setAccountRoleLocked is SetAccountRole without the mutex: the caller holds
// userWriteMu (SetAccountRole, and UpdateTenantUser, which applies a whole
// patch under one lock).
func (s *ControlPlaneService) setAccountRoleLocked(id, role string, nowNs int64) error {
	user, err := s.Engine.GetUser(id)
	if err != nil || user == nil {
		return ErrAccountNotFound
	}
	if user.Role == role {
		return nil
	}
	if user.Role == model.UserRoleAdmin && role == model.UserRoleUser {
		if err := s.refuseIfLastAdmin(user); err != nil {
			return err
		}
	}
	// A single column: a role change must not write back the password hash,
	// the status or the second factor the caller read alongside it.
	return s.Engine.UpdateUserRole(id, role, nowNs)
}

// refuseIfLastAdmin reports ErrLastAdmin when user is the only active
// administrator.
//
// The caller holds userWriteMu. What this reads is a count of the other active
// administrators taken before the write it guards, so it is only a promise
// while no other writer can change a role or a status in between: two callers
// that each count the other and then both write would leave none. Every caller
// therefore reaches it with the lock held, and the write it guards happens
// before that lock is released.
func (s *ControlPlaneService) refuseIfLastAdmin(user *model.User) error {
	if user.Role != model.UserRoleAdmin || user.Status != model.UserStatusActive {
		// Not an active administrator, so removing it cannot strand the
		// console.
		return nil
	}
	users, err := s.Engine.ListUsers()
	if err != nil {
		return err
	}
	others := 0
	for _, candidate := range users {
		if candidate.ID == user.ID {
			continue
		}
		if candidate.Role == model.UserRoleAdmin && candidate.Status == model.UserStatusActive {
			others++
		}
	}
	if others == 0 {
		return ErrLastAdmin
	}
	return nil
}

// TOTPEnrolment is what an account needs to add a second factor.
type TOTPEnrolment struct {
	// Secret is the Base32 secret to show once.
	Secret string `json:"secret"`
	// URI is the otpauth:// URI an authenticator app scans.
	URI string `json:"uri"`
}

// StartTOTPEnrolment returns a fresh secret for an account.
//
// The secret is not stored here. It reaches the database only after the account
// proves it can generate a code from it, which is what keeps a mistyped or
// unscanned enrolment from locking the account out: a stored secret that the
// user's app does not have makes every future login fail.
func (s *ControlPlaneService) StartTOTPEnrolment(id, issuer string) (TOTPEnrolment, error) {
	if s == nil || s.Engine == nil {
		return TOTPEnrolment{}, errors.New("service: no state engine")
	}
	user, err := s.Engine.GetUser(id)
	if err != nil || user == nil {
		return TOTPEnrolment{}, ErrAccountNotFound
	}
	if len(user.TOTPSecretEnc) > 0 {
		return TOTPEnrolment{}, ErrTOTPAlreadyEnrolled
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		return TOTPEnrolment{}, err
	}
	return TOTPEnrolment{
		Secret: secret,
		URI:    auth.TOTPURI(issuer, user.Username, secret),
	}, nil
}

// ConfirmTOTPEnrolment stores a secret after proving a code matches it.
//
// The ledger of spent steps is cleared with it (state.ResetTOTPLastUsed): the
// steps it counts were spent by codes from the secret being replaced, and the
// new secret's first code can legitimately fall inside a step the old one
// spent. Without the reset that fresh code would be refused as a replay until
// the next step came round.
func (s *ControlPlaneService) ConfirmTOTPEnrolment(id, secret, code string, nowNs int64) error {
	if s == nil || s.Engine == nil {
		return errors.New("service: no state engine")
	}
	if s.Pepper == nil {
		return ErrNoPepper
	}
	s.userWriteMu.Lock()
	defer s.userWriteMu.Unlock()

	user, err := s.Engine.GetUser(id)
	if err != nil || user == nil {
		return ErrAccountNotFound
	}
	if len(user.TOTPSecretEnc) > 0 {
		return ErrTOTPAlreadyEnrolled
	}
	// The code is checked against the secret before anything is written, so a
	// secret the user's app does not actually hold never becomes the one they
	// must present at the next login.
	if !auth.VerifyTOTP(secret, code, time.Unix(0, nowNs)) {
		return ErrTOTPCodeInvalid
	}
	sealed, err := auth.EncryptTOTPSecret(s.Pepper, secret)
	if err != nil {
		return err
	}
	// Two writes, the secret first: clearing the ledger before the new secret
	// is in place would let the old one spend a step twice, so the reset only
	// ever follows the rotation it belongs to.
	if err := s.Engine.UpdateUserTOTPSecret(id, sealed, nowNs); err != nil {
		return err
	}
	return s.Engine.ResetTOTPLastUsed(id)
}

// DisableTOTP removes an account's second factor.
//
// The replay ledger goes with it (state.ResetTOTPLastUsed): it counts steps the
// secret being removed has already spent, and a later enrolment installs a new
// secret whose first code can fall inside a step the old one spent. The ledger
// is monotone, so without the reset that genuinely fresh code would be refused
// as a replay until the next step came round.
func (s *ControlPlaneService) DisableTOTP(id string, nowNs int64) error {
	if s == nil || s.Engine == nil {
		return errors.New("service: no state engine")
	}
	s.userWriteMu.Lock()
	defer s.userWriteMu.Unlock()

	user, err := s.Engine.GetUser(id)
	if err != nil || user == nil {
		return ErrAccountNotFound
	}
	if len(user.TOTPSecretEnc) == 0 {
		return ErrTOTPNotEnrolled
	}
	// Two writes, and the order matters: the secret goes first. Clearing the
	// ledger while the old secret is still in force would let a code computed
	// from that secret spend a step twice, so the reset only ever follows the
	// removal it belongs to.
	if err := s.Engine.UpdateUserTOTPSecret(id, nil, nowNs); err != nil {
		return err
	}
	return s.Engine.ResetTOTPLastUsed(id)
}

// checkPasswordPolicy enforces the console's password rule.
func checkPasswordPolicy(password string) error {
	if len(password) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	return nil
}

// newAccountID returns a fresh account id.
//
// It is a UUIDv7 without dashes, matching the access keys: ids sort by creation
// time and the column is a plain text primary key.
func newAccountID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("service: generate account id: %w", err)
	}
	return strings.ReplaceAll(id.String(), "-", ""), nil
}
