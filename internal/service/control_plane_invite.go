package service

import (
	"errors"
	"strings"
	"unicode"

	"prism/internal/auth"
	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/state"
)

// Invitations bound self-service registration.
//
// A code does two jobs at once: it decides whether an account may be created at
// all, and it decides what that account starts on. The claim is a conditional
// UPDATE in the repository, so a code with one use left cannot admit two
// accounts even when two registrations arrive together.

// InviteInput is the accepted body of an invite creation. MaxUses of 0 means
// unlimited; ExpiresInDays of 0 never expires.
type InviteInput struct {
	PlanID        string `json:"plan_id"`
	MaxUses       int    `json:"max_uses"`
	ExpiresInDays int    `json:"expires_in_days"`
	Note          string `json:"note"`
}

// InviteView is an invite as the console sees it.
//
// Status is the stored value only while the invite is still usable: once it is
// exhausted or past its date, the read reports it as disabled, so an operator
// reading the list sees what the registration path will do rather than having to
// recompute it.
type InviteView struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	PlanID      string `json:"plan_id"`
	PlanName    string `json:"plan_name"`
	MaxUses     int    `json:"max_uses"`
	UsedCount   int    `json:"used_count"`
	Status      string `json:"status"`
	ExpiresAtNs int64  `json:"expires_at_ns"`
	Note        string `json:"note"`
	CreatedAtNs int64  `json:"created_at_ns"`
}

// CreateInvite mints a code.
func (s *ControlPlaneService) CreateInvite(in InviteInput, createdBy string, nowNs int64) (InviteView, error) {
	if s == nil || s.Engine == nil {
		return InviteView{}, internal("no state engine", nil)
	}
	if in.MaxUses < 0 {
		return InviteView{}, invalidArg("max_uses: must not be negative")
	}
	if in.ExpiresInDays < 0 {
		return InviteView{}, invalidArg("expires_in_days: must not be negative")
	}

	planName := ""
	if planID := strings.TrimSpace(in.PlanID); planID != "" {
		plan, err := s.Engine.GetPlan(planID)
		if err != nil {
			return InviteView{}, mapPlanLookupError(err)
		}
		planName = plan.Name
	}

	id, err := newID("iv_")
	if err != nil {
		return InviteView{}, internal("generate invite id", err)
	}
	code, err := newInviteCode()
	if err != nil {
		return InviteView{}, internal("generate invite code", err)
	}

	row := model.Invite{
		ID:          id,
		Code:        code,
		PlanID:      strings.TrimSpace(in.PlanID),
		MaxUses:     in.MaxUses,
		UsedCount:   0,
		ExpiresAtNs: expiryFromDays(in.ExpiresInDays, nowNs),
		Note:        strings.TrimSpace(in.Note),
		Status:      model.InviteStatusActive,
		CreatedBy:   createdBy,
		CreatedAtNs: nowNs,
		UpdatedAtNs: nowNs,
	}
	if err := s.Engine.InsertInvite(row); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return InviteView{}, conflict("that invite code already exists")
		}
		return InviteView{}, internal("insert invite", err)
	}
	return inviteView(row, planName, nowNs), nil
}

// ListInvites returns invites, newest first, with the total before paging.
func (s *ControlPlaneService) ListInvites(limit, offset int, nowNs int64) ([]InviteView, int, error) {
	if s == nil || s.Engine == nil {
		return nil, 0, internal("no state engine", nil)
	}
	rows, err := s.Engine.ListInvites()
	if err != nil {
		return nil, 0, internal("list invites", err)
	}
	plans, err := s.planDirectory()
	if err != nil {
		return nil, 0, err
	}

	views := make([]InviteView, 0, len(rows))
	for _, row := range rows {
		name := ""
		if plan, ok := plans[row.PlanID]; ok {
			name = plan.Name
		}
		views = append(views, inviteView(row, name, nowNs))
	}
	total := len(views)
	return pageSlice(views, limit, offset), total, nil
}

// DeleteInvite removes a code. Registration with a removed code is refused, and
// the accounts it already admitted are untouched.
func (s *ControlPlaneService) DeleteInvite(id string) error {
	if s == nil || s.Engine == nil {
		return internal("no state engine", nil)
	}
	if _, err := s.Engine.GetInvite(id); err != nil {
		return mapInviteLookupError(err)
	}
	if err := s.Engine.DeleteInvite(id); err != nil {
		return mapInviteLookupError(err)
	}
	return nil
}

// RedeemInvite claims one use of a code and returns the invite.
//
// It is the atomic half of registration. A code that is unknown, revoked, past
// its date or used up all produce the same refusal: telling a caller which of
// the four it was would turn this endpoint into a way to enumerate valid codes.
func (s *ControlPlaneService) RedeemInvite(code string, nowNs int64) (*model.Invite, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("no state engine", nil)
	}
	invite, err := s.Engine.RedeemInviteByCode(strings.TrimSpace(code), nowNs)
	if errors.Is(err, state.ErrNotFound) {
		return nil, invalidArg("invite_code: the code is not valid")
	}
	if err != nil {
		return nil, internal("redeem invite", err)
	}
	return invite, nil
}

// RegisterTenantUser creates a self-service account.
//
// The mode decides what is required: off refuses everything, invite requires a
// code and grants what the code's plan grants, open needs no code and grants
// nothing. The returned user is the account; signing it in is the caller's
// business, because that is the auth service's job and it owns the cookies.
func (s *ControlPlaneService) RegisterTenantUser(mode config.RegistrationMode, inviteCode, username, password string, nowNs int64) (*model.User, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("no state engine", nil)
	}
	if !mode.AllowsRegistration() {
		return nil, &ServiceError{Code: "REGISTRATION_CLOSED", Message: "self-service registration is disabled"}
	}

	username = strings.TrimSpace(username)
	if svcErr := validateTenantUsername(username); svcErr != nil {
		return nil, svcErr
	}
	if err := checkPasswordPolicy(password); err != nil {
		return nil, invalidArg(err.Error())
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, internal("hash password", err)
	}
	id, err := newAccountID()
	if err != nil {
		return nil, internal("generate account id", err)
	}
	user := model.User{
		ID:           id,
		Username:     username,
		PasswordHash: hash,
		Role:         model.UserRoleUser,
		Status:       model.UserStatusActive,
		CreatedAtNs:  nowNs,
		UpdatedAtNs:  nowNs,
	}

	if mode != config.RegistrationInvite {
		// An open deployment creates the account and nothing else: there is
		// no code, so there is nothing to grant.
		if err := s.Engine.InsertUser(user); err != nil {
			return nil, mapUserWriteError(err)
		}
		return &user, nil
	}

	code := strings.TrimSpace(inviteCode)
	if code == "" {
		return nil, invalidArg("invite_code: required")
	}
	invite, err := s.Engine.RedeemInviteAndInsertUser(code, nowNs, user)
	if errors.Is(err, state.ErrNotFound) {
		return nil, invalidArg("invite_code: the code is not valid")
	}
	if errors.Is(err, state.ErrConflict) {
		return nil, conflict("a user with that username already exists")
	}
	if err != nil {
		return nil, internal("register account", err)
	}

	if invite.PlanID != "" {
		// The code grants a subscription at the plan's own duration. A plan
		// that has since been deleted leaves plan_id empty, and the account
		// is still created - the code admitted the person, and the operator
		// can put them on a plan afterwards.
		if _, err := s.AssignSubscription(user.ID, invite.PlanID, 0, nowNs); err != nil {
			return nil, err
		}
	}
	return &user, nil
}

// inviteView renders an invite, computing the status a reader should see.
func inviteView(row model.Invite, planName string, nowNs int64) InviteView {
	view := InviteView{
		ID:          row.ID,
		Code:        row.Code,
		PlanID:      row.PlanID,
		PlanName:    planName,
		MaxUses:     row.MaxUses,
		UsedCount:   row.UsedCount,
		Status:      row.Status,
		ExpiresAtNs: row.ExpiresAtNs,
		Note:        row.Note,
		CreatedAtNs: row.CreatedAtNs,
	}
	if view.Status == model.InviteStatusActive && !inviteUsable(row, nowNs) {
		view.Status = model.InviteStatusDisabled
	}
	return view
}

// inviteUsable reports whether the registration path would accept the code.
func inviteUsable(row model.Invite, nowNs int64) bool {
	if row.Status != model.InviteStatusActive {
		return false
	}
	if row.ExpiresAtNs > 0 && nowNs >= row.ExpiresAtNs {
		return false
	}
	return row.MaxUses == 0 || row.UsedCount < row.MaxUses
}

// validateTenantUsername applies the account-name rule both the administrator's
// creation and self-service registration use.
//
// A name is what an operator reads and what a login is typed as, so it is kept
// to letters, digits and a small set of separators. Notably it may not contain
// whitespace: a name with a space in it cannot be checked reliably against a
// login form that trims.
func validateTenantUsername(username string) *ServiceError {
	if username == "" {
		return invalidArg("username: must not be empty")
	}
	if len(username) < minUsernameLength || len(username) > maxUsernameLength {
		return invalidArg("username: must be between 3 and 64 characters")
	}
	for _, r := range username {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		switch r {
		case '-', '_', '.', '@', '+':
			continue
		}
		return invalidArg("username: only letters, digits and - _ . @ + are allowed")
	}
	return nil
}

const (
	minUsernameLength = 3
	maxUsernameLength = 64
)
