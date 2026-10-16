package service

import (
	"encoding/json"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"prism/internal/keys"
	"prism/internal/model"
	"prism/internal/state"
)

// Access keys are what a user's client presents: one credential per device or
// deployment, each narrowing the plan's platform set and optionally the source
// addresses it may be used from.
//
// Everything that changes a key, a subscription or an account ends in
// republishUserKeys. The data plane reads key state from memory, so without that
// step a revocation would only take effect at the next restart; with it, the
// next request is refused.

// AccessKeyInput is the accepted body of a key creation. ExpiresInDays is a
// duration rather than an absolute time because a key is minted for "30 more
// days from now", and a client cannot be trusted to do that arithmetic in its
// own clock.
type AccessKeyInput struct {
	Name          string   `json:"name"`
	Platforms     []string `json:"platforms"`
	IPAllowlist   []string `json:"ip_allowlist"`
	ExpiresInDays int      `json:"expires_in_days"`
}

// AccessKeyView is a key as the console sees it. The secret is not in it and
// never can be: only its hash is stored, so the plaintext exists exactly once,
// in the response to the call that created it. The hint is the public half,
// which is enough to tell two keys apart in a list.
type AccessKeyView struct {
	ID             string   `json:"id"`
	UserID         string   `json:"user_id"`
	Name           string   `json:"name"`
	Scope          string   `json:"scope"`
	Status         string   `json:"status"`
	Platforms      []string `json:"platforms"`
	IPAllowlist    []string `json:"ip_allowlist"`
	ExpiresAtNs    int64    `json:"expires_at_ns"`
	LastUsedNs     int64    `json:"last_used_ns"`
	CreatedAtNs    int64    `json:"created_at_ns"`
	CredentialHint string   `json:"credential_hint"`
}

// accessKeyPatchAllowedFields are the fields PATCH accepts on a key.
var accessKeyPatchAllowedFields = map[string]bool{
	"name":         true,
	"platforms":    true,
	"ip_allowlist": true,
	"status":       true,
}

func accessKeyView(key model.AccessKey) AccessKeyView {
	return AccessKeyView{
		ID:             key.ID,
		UserID:         key.UserID,
		Name:           key.Name,
		Scope:          key.Scope,
		Status:         key.Status,
		Platforms:      append([]string{}, key.Platforms...),
		IPAllowlist:    append([]string{}, key.IPAllowlist...),
		ExpiresAtNs:    key.ExpiresAtNs,
		LastUsedNs:     key.LastUsedNs,
		CreatedAtNs:    key.CreatedAtNs,
		CredentialHint: keys.CredentialPrefix + key.ID + "_…",
	}
}

// ListAccessKeys returns one user's keys, newest first.
func (s *ControlPlaneService) ListAccessKeys(userID string) ([]AccessKeyView, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("no state engine", nil)
	}
	if _, err := s.Engine.GetUser(userID); err != nil {
		return nil, mapUserLookupError(err)
	}
	rows, err := s.Engine.ListAccessKeysByUser(userID)
	if err != nil {
		return nil, internal("list access keys", err)
	}
	views := make([]AccessKeyView, 0, len(rows))
	for _, row := range rows {
		views = append(views, accessKeyView(row))
	}
	return views, nil
}

// CreateAccessKey mints a key for one user and returns it with the only copy of
// its plaintext credential.
//
// The secret is hashed with the same pepper the data plane verifies against, so
// the value returned here cannot be recovered from the database afterwards. A
// deployment without a pepper cannot mint keys at all - without it, a stored
// hash could not be verified, and a key that cannot be verified is worse than
// no key.
func (s *ControlPlaneService) CreateAccessKey(userID string, in AccessKeyInput, nowNs int64) (AccessKeyView, string, error) {
	if s == nil || s.Engine == nil {
		return AccessKeyView{}, "", internal("no state engine", nil)
	}
	if s.Pepper == nil {
		return AccessKeyView{}, "", internal("no pepper is configured, so access keys cannot be minted", ErrNoPepper)
	}

	user, err := s.Engine.GetUser(userID)
	if err != nil {
		return AccessKeyView{}, "", mapUserLookupError(err)
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		return AccessKeyView{}, "", invalidArg("name: must not be empty")
	}
	allowlist, svcErr := normalizeIPAllowlist(in.IPAllowlist)
	if svcErr != nil {
		return AccessKeyView{}, "", svcErr
	}
	if in.ExpiresInDays < 0 {
		return AccessKeyView{}, "", invalidArg("expires_in_days: must not be negative")
	}

	// The allowance is checked inside insertAccessKeyWithinAllowance, in the
	// same critical section as the insert itself: reading the count here and
	// inserting later would let two creations pass the same check.
	id, secret, err := keys.GenerateCredential()
	if err != nil {
		return AccessKeyView{}, "", internal("generate credential", err)
	}

	row := model.AccessKey{
		ID:          id,
		UserID:      userID,
		Name:        name,
		SecretHash:  s.Pepper.HashSecret(secret),
		Platforms:   normalizeStringList(in.Platforms),
		IPAllowlist: allowlist,
		Scope:       model.KeyScopeProxy,
		Status:      model.KeyStatusActive,
		CreatedAtNs: nowNs,
	}
	if in.ExpiresInDays > 0 {
		row.ExpiresAtNs = nowNs + int64(in.ExpiresInDays)*int64(24*time.Hour)
	}

	if svcErr := s.insertAccessKeyWithinAllowance(*user, row); svcErr != nil {
		return AccessKeyView{}, "", svcErr
	}
	if err := s.publishKey(row, *user); err != nil {
		return AccessKeyView{}, "", err
	}
	return accessKeyView(row), keys.FormatCredential(id, secret), nil
}

// UpdateAccessKey applies a patch to one of a user's keys.
//
// The ownership check is the user id in the path, not a field in the body: a key
// is addressed as /admin/users/{id}/keys/{key_id} (or /me/keys/{key_id}), so a
// caller can only ever act on keys of the user it named.
func (s *ControlPlaneService) UpdateAccessKey(userID, keyID string, patchJSON json.RawMessage, nowNs int64) (AccessKeyView, error) {
	if s == nil || s.Engine == nil {
		return AccessKeyView{}, internal("no state engine", nil)
	}
	patch, svcErr := parseMergePatch(patchJSON)
	if svcErr != nil {
		return AccessKeyView{}, svcErr
	}
	if svcErr := patch.validateFields(accessKeyPatchAllowedFields, func(field string) string {
		return "unknown field: " + field
	}); svcErr != nil {
		return AccessKeyView{}, svcErr
	}

	row, err := s.lookupUserKey(userID, keyID)
	if err != nil {
		return AccessKeyView{}, err
	}
	user, err := s.Engine.GetUser(userID)
	if err != nil {
		return AccessKeyView{}, mapUserLookupError(err)
	}

	if value, ok, svcErr := patch.optionalNonEmptyString("name"); svcErr != nil {
		return AccessKeyView{}, svcErr
	} else if ok {
		row.Name = value
	}
	if value, ok, svcErr := patch.optionalStringSlice("platforms"); svcErr != nil {
		return AccessKeyView{}, svcErr
	} else if ok {
		row.Platforms = normalizeStringList(value)
	}
	if value, ok, svcErr := patch.optionalStringSlice("ip_allowlist"); svcErr != nil {
		return AccessKeyView{}, svcErr
	} else if ok {
		normalized, svcErr := normalizeIPAllowlist(value)
		if svcErr != nil {
			return AccessKeyView{}, svcErr
		}
		row.IPAllowlist = normalized
	}
	if value, ok, svcErr := patch.optionalString("status"); svcErr != nil {
		return AccessKeyView{}, svcErr
	} else if ok {
		if value != model.KeyStatusActive && value != model.KeyStatusDisabled {
			return AccessKeyView{}, invalidArg("status: must be active or disabled")
		}
		row.Status = value
	}

	if err := s.Engine.UpdateAccessKey(row); err != nil {
		return AccessKeyView{}, internal("update access key", err)
	}
	if err := s.publishKey(row, *user); err != nil {
		return AccessKeyView{}, err
	}
	return accessKeyView(row), nil
}

// DeleteAccessKey removes one of a user's keys. The key leaves the live store in
// the same breath, so a deleted credential stops working immediately.
func (s *ControlPlaneService) DeleteAccessKey(userID, keyID string) error {
	if s == nil || s.Engine == nil {
		return internal("no state engine", nil)
	}
	if _, err := s.lookupUserKey(userID, keyID); err != nil {
		return err
	}
	if err := s.Engine.DeleteAccessKey(keyID); err != nil {
		return internal("delete access key", err)
	}
	if s.Keys != nil {
		s.Keys.Delete(keyID)
	}
	return nil
}

// republishUserKeys puts a user's keys back into the live store from the
// database.
//
// It is the one step every change that can affect access has to end with, which
// is why it reads the rows rather than taking them as arguments: after an
// account is disabled, a subscription is suspended or a plan is edited, the
// question is "what does this user's access look like now", and that is a
// question for the database.
func (s *ControlPlaneService) republishUserKeys(userID string) error {
	if s == nil || s.Engine == nil {
		return internal("no state engine", nil)
	}
	if s.Keys == nil {
		// The console runs without a data plane in tests and in a
		// management-only deployment; the rows are still the record.
		return nil
	}

	user, err := s.Engine.GetUser(userID)
	if err != nil {
		return mapUserLookupError(err)
	}
	rows, err := s.Engine.ListAccessKeysByUser(userID)
	if err != nil {
		return internal("list access keys", err)
	}

	snapshot, err := s.userPlanSnapshot(userID)
	if err != nil {
		return err
	}

	for _, row := range rows {
		if row.Status != model.KeyStatusActive {
			s.Keys.Delete(row.ID)
			continue
		}
		s.Keys.Publish(row, *user, snapshot)
	}
	return nil
}

// userPlanSnapshot returns the live plan snapshot for the subscription that
// decides the user's access, or the zero snapshot when the user has no
// subscription row at all.
//
// It is the decision a restart makes (keys.DecidingSubscription) and the one the
// console makes everywhere else (currentSubscription), which is what keeps a
// suspended or expired subscription from being minted a key that looks
// unlimited: the subscription's status travels into the snapshot and Usable
// refuses it, exactly as it does after a restart.
func (s *ControlPlaneService) userPlanSnapshot(userID string) (keys.PlanSnapshot, error) {
	sub, svcErr := s.currentSubscription(userID)
	if svcErr != nil {
		return keys.PlanSnapshot{}, svcErr
	}
	if sub == nil {
		return keys.PlanSnapshot{}, nil
	}
	snapshot, err := keys.SnapshotFromSubscription(sub)
	if err != nil {
		return keys.PlanSnapshot{}, internal("read plan snapshot", err)
	}
	return snapshot, nil
}

// publishKey puts one key into the live store with the user's current plan.
func (s *ControlPlaneService) publishKey(row model.AccessKey, user model.User) error {
	if s.Keys == nil {
		return nil
	}
	if row.Status != model.KeyStatusActive {
		s.Keys.Delete(row.ID)
		return nil
	}
	snapshot, err := s.userPlanSnapshot(row.UserID)
	if err != nil {
		return err
	}
	s.Keys.Publish(row, user, snapshot)
	return nil
}

// lookupUserKey reads one key, refusing one that belongs to a different user.
//
// A key id is not a secret in the sense the credential is - it is half of one -
// so this is the check that keeps an id learnt elsewhere from being used to
// disable or delete someone else's key.
func (s *ControlPlaneService) lookupUserKey(userID, keyID string) (model.AccessKey, error) {
	row, err := s.Engine.GetAccessKey(keyID)
	if errors.Is(err, state.ErrNotFound) {
		return model.AccessKey{}, notFound("access key not found")
	}
	if err != nil {
		return model.AccessKey{}, internal("read access key", err)
	}
	if row.UserID != userID {
		return model.AccessKey{}, notFound("access key not found")
	}
	return *row, nil
}

// insertAccessKeyWithinAllowance mints a key for one account with the plan's
// allowance check and the insert inside one critical section.
//
// The check reads the user's existing keys and the insert adds one; two requests
// arriving together could otherwise both read the same count and both insert,
// leaving the account over the limit its plan sets. Nothing below can refuse the
// second row - the limit lives inside the subscription's plan snapshot, not in a
// column a unique index could cover - so the pair is held together here.
// keyCreateMu only covers this process; its comment names the repository
// primitive that would make the limit hold across panels sharing one database.
func (s *ControlPlaneService) insertAccessKeyWithinAllowance(user model.User, row model.AccessKey) *ServiceError {
	s.keyCreateMu.Lock()
	defer s.keyCreateMu.Unlock()

	if svcErr := s.checkKeyAllowance(&user); svcErr != nil {
		return svcErr
	}
	if err := s.Engine.InsertAccessKey(row); err != nil {
		return internal("insert access key", err)
	}
	return nil
}

// checkKeyAllowance enforces the plan's key limit when it sets one.
//
// It is called from inside the key-creation critical section
// (insertAccessKeyWithinAllowance), never on its own: the count it reads is only
// meaningful while the insert that follows is held back.
func (s *ControlPlaneService) checkKeyAllowance(user *model.User) *ServiceError {
	if user == nil {
		return notFound("user not found")
	}
	limit, err := s.maxKeysForPlan(user.ID)
	if err != nil {
		return err
	}
	if limit <= 0 {
		return nil
	}
	existing, readErr := s.Engine.ListAccessKeysByUser(user.ID)
	if readErr != nil {
		return internal("list access keys", readErr)
	}
	active := 0
	for _, row := range existing {
		if row.Status == model.KeyStatusActive {
			active++
		}
	}
	if active >= limit {
		return conflict("the plan allows at most " + strconv.Itoa(limit) + " active keys")
	}
	return nil
}

// maxKeysForPlan reads the key limit out of the subscription that decides the
// user's access, and refuses the creation when there is no usable one.
//
// Two absences used to be told apart here, and they do not mean the same thing -
// but the answer is the same for both, and it has to be. An account whose
// subscriptions have all ended, or have been suspended, is refused because its
// snapshot is unusable on purpose. An account with no subscription row at all is
// refused because that is the state that reads as unlimited: the loader and the
// publish path fall back to the zero snapshot - no quota, no expiry, no key limit
// - so a key minted here would be a key with no limit at all, which is exactly
// the access the account is not paying for. "No plan in force" is a refusal to
// mint, not a licence to mint without one.
//
// The accounts that were minted a key before they were on any plan are not cut
// off by this: migration 000021 gives every user who already had a key and no
// subscription row an active, unlimited "legacy-unlimited" subscription, so they
// keep the access they had and arrive here with a row to read.
func (s *ControlPlaneService) maxKeysForPlan(userID string) (int, *ServiceError) {
	sub, svcErr := s.currentSubscription(userID)
	if svcErr != nil {
		return 0, svcErr
	}
	if sub == nil {
		return 0, conflict("no plan is in force on the account, so a new key cannot be created")
	}
	if sub.Status != model.SubscriptionStatusActive {
		return 0, conflict("the account has no subscription in force, so a new key cannot be created")
	}
	plan, err := planFromSnapshotJSON(sub.PlanSnapshotJSON)
	if err != nil {
		return 0, internal("read plan snapshot", err)
	}
	return plan.MaxKeys, nil
}

// normalizeIPAllowlist validates and normalizes a source-address list.
//
// Entries are addresses or CIDR prefixes. An empty list means "any address",
// which is the default because a key is usually scoped to a person rather than
// to a network.
func normalizeIPAllowlist(values []string) ([]string, *ServiceError) {
	if len(values) == 0 {
		return []string{}, nil
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for i, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(trimmed); err == nil {
			trimmed = prefix.Masked().String()
		} else if addr, err := netip.ParseAddr(trimmed); err == nil {
			trimmed = addr.String()
		} else {
			return nil, invalidArg("ip_allowlist[" + strconv.Itoa(i) + "]: not an address or CIDR prefix")
		}
		if seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		result = append(result, trimmed)
	}
	return result, nil
}
