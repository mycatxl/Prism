package service

import (
	"errors"
	"log"
	"strings"
	"unicode"

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
//
// A code is a promise that registration grants its plan, so a plan that cannot
// be granted is refused here rather than when a person tries to use the code: an
// invite to a disabled plan would spend one of its uses on an account that gets
// nothing.
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
	if in.ExpiresInDays > maxExpiryDays {
		return InviteView{}, invalidArg("expires_in_days: must not exceed 36500 (100 years)")
	}

	planName := ""
	if planID := strings.TrimSpace(in.PlanID); planID != "" {
		plan, err := s.Engine.GetPlan(planID)
		if err != nil {
			return InviteView{}, mapPlanLookupError(err)
		}
		if plan.Status != planStatusActive {
			return InviteView{}, invalidArg("plan_id: the plan is disabled")
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

// checkInvitePlan verifies that registration with a code would be able to grant
// what the code promises.
//
// The read consumes nothing and creates nothing, which is the point: it runs
// before the code is claimed and before the account exists, so a code whose plan
// has been deleted or disabled is refused as a whole rather than spending a use
// and leaving an account behind.
//
// A code that exists but cannot be used - one an operator disabled, one past
// its date, one whose uses are spent - is refused here as well, and with the
// message a code that never existed gets: the registration path must not be a
// way to tell the states of codes apart, and the caller can do nothing
// different in any of the three cases. The two plan failures get their own
// message - the code itself is genuine, and the person holding it needs to know
// the plan, not the code, is the problem - while an unknown code is reported
// exactly the way the claim reports it, so a code that was spent and one that
// never existed still read the same.
//
// Nothing here touches the database until the string at least has the shape of a
// code this service mints (validInviteCodeShape), which is what makes it safe to
// run before a password is hashed: every refusal this function can produce costs
// one indexed read at most, never an argon2id hash.
func (s *ControlPlaneService) checkInvitePlan(code string, nowNs int64) *ServiceError {
	if !validInviteCodeShape(code) {
		return invalidArg("invite_code: the code is not valid")
	}
	invite, err := s.Engine.GetInviteByCode(code)
	if errors.Is(err, state.ErrNotFound) {
		return invalidArg("invite_code: the code is not valid")
	}
	if err != nil {
		return internal("read invite", err)
	}
	// A real code that is not usable. It reads exactly like a code that is not
	// there: inviteUsable is the same predicate the console renders the list
	// with (inviteView), so what this refuses is precisely what the console
	// already reports as disabled.
	if !inviteUsable(*invite, nowNs) {
		return invalidArg("invite_code: the code is not valid")
	}
	if invite.PlanID == "" {
		// A code with no plan grants an account and nothing else; that is
		// still a usable code.
		return nil
	}
	plan, err := s.Engine.GetPlan(invite.PlanID)
	if errors.Is(err, state.ErrNotFound) {
		return invalidArg("invite_code: the plan this code grants no longer exists")
	}
	if err != nil {
		return internal("read plan", err)
	}
	if plan.Status != planStatusActive {
		return invalidArg("invite_code: the plan this code grants is disabled")
	}
	return nil
}

// RegisterTenantUser creates a self-service account.
//
// The mode decides what is required: off refuses everything, invite requires a
// code and grants what the code's plan grants, open needs no code and grants
// nothing. The returned user is the account; signing it in is the caller's
// business, because that is the auth service's job and it owns the cookies.
//
// With an invite, everything that can be refused before anything is written is
// refused first, and before the password is hashed: a code that is missing, that
// cannot be one of ours, that exists but is disabled, expired or used up, whose
// plan no longer exists or whose plan is off sale costs a shape check or one
// indexed read, never an argon2id hash. This endpoint
// is public, so the ordering is part of its defence. A failure to grant the
// subscription after the account has been committed is not reported as a failed
// registration - the account is real and the code is spent, so the answer says the
// account was created and the missing subscription is logged for the operator.
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

	// Everything about an invitation that can be settled without writing
	// anything is settled before the password is hashed. Hashing is the expensive
	// step - argon2id, deliberately so - and this endpoint is reachable without
	// an identity, so a caller sending random codes must not be able to make the
	// server do that work. The refusals are the ones this path produces a few
	// lines later: a code that cannot be one of ours, one that does not exist
	// and one that exists but is not usable (disabled, expired, used up) all
	// read the same on purpose, so nothing about the answer changes - and none
	// of them can make the server hash a password.
	code := ""
	if mode == config.RegistrationInvite {
		code = strings.TrimSpace(inviteCode)
		if code == "" {
			return nil, invalidArg("invite_code: required")
		}
		// Before the code is spent and before the account exists. Refusing after
		// the claim would leave an account on no plan and a spent use behind for
		// a registration the caller reads as failed - and the retry, with the
		// account already there, would be a duplicate-name conflict.
		if svcErr := s.checkInvitePlan(code, nowNs); svcErr != nil {
			return nil, svcErr
		}
	}

	hash, err := s.hashPassword(password)
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
			// The account and the claim are already committed, so this is not
			// a failed registration: the account exists and the code is spent,
			// and reporting the error would both contradict the commit and
			// make the retry fail for a different reason. The subscription is
			// the part that is missing, and that is what the log says.
			log.Printf("[service] account %s was created with invite %s but the subscription on plan %s was not granted: %v",
				user.ID, invite.ID, invite.PlanID, err)
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
