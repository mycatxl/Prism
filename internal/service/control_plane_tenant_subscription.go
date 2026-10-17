package service

import (
	"encoding/json"
	"strings"

	"prism/internal/keys"
	"prism/internal/model"
)

// A subscription is what actually grants access: it binds one user to one plan
// and carries the snapshot of that plan as it was sold.
//
// Three rules that are easy to get wrong, and are therefore stated once here. A
// user has at most one active subscription - "last activation wins" - so
// assigning a new one, or patching an old one back to active, expires whatever
// else was running instead of stacking windows. A subscription keeps the plan
// snapshot it was created with, so editing a plan does not silently change what
// an existing user bought - only the plan's own "apply to existing" switch does
// that. And "the subscription a user is on" always means the one
// currentSubscription returns: the active row if there is one, otherwise the
// newest of the ended ones (keys.DecidingSubscription), which is the same read
// the restart loader and the key publish path make.

// SubscriptionView is a subscription as the console sees it, with the names of
// the user and the plan resolved so a table needs no second request.
type SubscriptionView struct {
	ID              string `json:"id"`
	UserID          string `json:"user_id"`
	Username        string `json:"username"`
	PlanID          string `json:"plan_id"`
	PlanName        string `json:"plan_name"`
	Status          string `json:"status"`
	StartsAtNs      int64  `json:"starts_at_ns"`
	ExpiresAtNs     int64  `json:"expires_at_ns"`
	PeriodStartNs   int64  `json:"period_start_ns"`
	PeriodUsedBytes int64  `json:"period_used_bytes"`
	TrafficBytes    int64  `json:"traffic_bytes"`
	// AutoRenew is the customer's own switch. It is read from the subscription
	// row rather than from the snapshot, so rewriting the snapshot does not
	// change what it says.
	AutoRenew bool `json:"auto_renew"`
	// PeriodIndex counts the periods this subscription has been extended for,
	// and PriceMicro is what one period costs - the snapshot's price, which is
	// what a renewal charges.
	PeriodIndex int   `json:"period_index"`
	PriceMicro  int64 `json:"price_micro"`
	// RemainingBytes is null when the plan does not limit traffic, which is a
	// different answer from "no bytes left".
	RemainingBytes *int64 `json:"remaining_bytes"`
}

// TenantSubscriptionFilter narrows a subscription list.
type TenantSubscriptionFilter struct {
	UserID string
	Status string
	Limit  int
	Offset int
}

// tenantSubscriptionPatchAllowedFields are the fields this PATCH accepts.
var tenantSubscriptionPatchAllowedFields = map[string]bool{
	"status":        true,
	"extend_days":   true,
	"expires_at_ns": true,
}

// AssignSubscription opens a subscription for a user, replacing whatever they
// had.
//
// durationDays of 0 takes the plan's own duration. The previous active
// subscription is expired rather than deleted, because the history of who was
// on what is worth keeping.
//
// A plan_id is required, and an empty one is refused here rather than turned
// into a subscription. The row this used to write carried the zero snapshot -
// no status, no quotas, no platform list - and a zero snapshot is not a smaller
// subscription on the read side, it is the largest one there is: keys.Store
// only refuses a snapshot whose status is set, and a traffic quota of zero
// means unlimited (planFromSnapshotJSON, maxKeysForPlan, checkKeyAllowance). An
// active row "on nothing" would therefore grant unlimited keys with no plan
// limit at all, which is the exact state an account with no plan is supposed to
// be refused in (control_plane_accesskey.go).
func (s *ControlPlaneService) AssignSubscription(userID, planID string, durationDays int, nowNs int64) (SubscriptionView, error) {
	if s == nil || s.Engine == nil {
		return SubscriptionView{}, internal("no state engine", nil)
	}
	if durationDays < 0 {
		return SubscriptionView{}, invalidArg("duration_days: must not be negative")
	}
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return SubscriptionView{}, invalidArg("plan_id: required")
	}
	if _, err := s.Engine.GetUser(userID); err != nil {
		return SubscriptionView{}, mapUserLookupError(err)
	}

	plan, err := s.Engine.GetPlan(planID)
	if err != nil {
		return SubscriptionView{}, mapPlanLookupError(err)
	}
	if plan.Status == planStatusDisabled {
		return SubscriptionView{}, invalidArg("plan_id: the plan is disabled")
	}

	days := durationDays
	if days == 0 {
		days = plan.DurationDays
	}
	if days <= 0 {
		return SubscriptionView{}, invalidArg("duration_days: a plan without a duration must be given one")
	}

	snapshot, err := planSnapshotJSON(*plan)
	if err != nil {
		return SubscriptionView{}, internal("encode plan snapshot", err)
	}
	id, err := newID("us_")
	if err != nil {
		return SubscriptionView{}, internal("generate subscription id", err)
	}

	if err := s.expireActiveSubscriptions(userID, nowNs); err != nil {
		return SubscriptionView{}, err
	}

	row := model.UserSubscription{
		ID:               id,
		UserID:           userID,
		PlanID:           plan.ID,
		PlanSnapshotJSON: snapshot,
		StartsAtNs:       nowNs,
		ExpiresAtNs:      expiryFromDays(days, nowNs),
		PeriodStartNs:    nowNs,
		PeriodUsedBytes:  0,
		Status:           model.SubscriptionStatusActive,
	}
	if err := s.Engine.UpsertUserSubscription(row); err != nil {
		// The insert itself can lose the race the unique index guards: a
		// concurrent assign that slips in behind the expire above trips
		// ux_user_subscriptions_active, and that is a conflict to report, not a
		// server fault.
		return SubscriptionView{}, mapSubscriptionWriteError("write subscription", err)
	}
	// The new plan is in force from the next request, not from the next
	// restart: the keys of this user are republished with the new snapshot.
	if err := s.republishUserKeys(userID); err != nil {
		return SubscriptionView{}, err
	}

	views, err := s.subscriptionViews([]model.UserSubscription{row})
	if err != nil {
		return SubscriptionView{}, err
	}
	return views[0], nil
}

// ListTenantSubscriptions returns subscriptions, newest period first, with the
// total that matches the filter for paging.
func (s *ControlPlaneService) ListTenantSubscriptions(filter TenantSubscriptionFilter) ([]SubscriptionView, int, error) {
	if s == nil || s.Engine == nil {
		return nil, 0, internal("no state engine", nil)
	}
	rows, err := s.Engine.ListUserSubscriptions()
	if err != nil {
		return nil, 0, internal("list subscriptions", err)
	}

	matched := make([]model.UserSubscription, 0, len(rows))
	for _, row := range rows {
		if filter.UserID != "" && row.UserID != filter.UserID {
			continue
		}
		if filter.Status != "" && row.Status != filter.Status {
			continue
		}
		matched = append(matched, row)
	}
	views, err := s.subscriptionViews(matched)
	if err != nil {
		return nil, 0, err
	}

	total := len(views)
	views = pageSlice(views, filter.Limit, filter.Offset)
	return views, total, nil
}

// GetTenantSubscription returns one subscription.
func (s *ControlPlaneService) GetTenantSubscription(id string) (SubscriptionView, error) {
	if s == nil || s.Engine == nil {
		return SubscriptionView{}, internal("no state engine", nil)
	}
	row, err := s.Engine.GetUserSubscription(id)
	if err != nil {
		return SubscriptionView{}, mapSubscriptionLookupError(err)
	}
	views, err := s.subscriptionViews([]model.UserSubscription{*row})
	if err != nil {
		return SubscriptionView{}, err
	}
	return views[0], nil
}

// UpdateTenantSubscription applies a patch: a status change, an extension, or an
// explicit expiry.
//
// extend_days counts from whichever is later, now or the current expiry, so
// extending an expired subscription restarts it from today rather than from a
// date in the past. Setting status to suspended or expired stops the user's keys
// on their next request.
//
// Patching a subscription back to active is an activation, not an edit of a
// window: whatever else the user had running is expired first, because two
// active rows would leave "which plan am I on" to table order. The same rule
// AssignSubscription follows.
//
// The write is a terms write (UpdateUserSubscriptionTerms), not a whole-row one:
// period_used_bytes has a concurrent writer - the usage flush worker adds to it -
// and writing the row back as it looked when it was read would silently give away
// every byte counted in between, permanently, because nothing recomputes the
// counter from the usage tables.
func (s *ControlPlaneService) UpdateTenantSubscription(id string, patchJSON json.RawMessage, nowNs int64) (SubscriptionView, error) {
	if s == nil || s.Engine == nil {
		return SubscriptionView{}, internal("no state engine", nil)
	}
	patch, svcErr := parseMergePatch(patchJSON)
	if svcErr != nil {
		return SubscriptionView{}, svcErr
	}
	if svcErr := patch.validateFields(tenantSubscriptionPatchAllowedFields, func(field string) string {
		return "unknown field: " + field
	}); svcErr != nil {
		return SubscriptionView{}, svcErr
	}

	row, err := s.Engine.GetUserSubscription(id)
	if err != nil {
		return SubscriptionView{}, mapSubscriptionLookupError(err)
	}
	updated := *row

	if value, ok, svcErr := patch.optionalString("status"); svcErr != nil {
		return SubscriptionView{}, svcErr
	} else if ok {
		switch value {
		case model.SubscriptionStatusActive, model.SubscriptionStatusExpired, model.SubscriptionStatusSuspended:
			updated.Status = value
		default:
			return SubscriptionView{}, invalidArg("status: must be active, expired or suspended")
		}
	}
	if value, ok, svcErr := patch.optionalInt("extend_days"); svcErr != nil {
		return SubscriptionView{}, svcErr
	} else if ok {
		if value <= 0 {
			return SubscriptionView{}, invalidArg("extend_days: must be greater than zero")
		}
		from := nowNs
		if updated.ExpiresAtNs > from {
			from = updated.ExpiresAtNs
		}
		updated.ExpiresAtNs = expiryFromDays(value, from)
	}
	if value, ok, svcErr := patch.optionalInt("expires_at_ns"); svcErr != nil {
		return SubscriptionView{}, svcErr
	} else if ok {
		if value < 0 {
			return SubscriptionView{}, invalidArg("expires_at_ns: must not be negative")
		}
		updated.ExpiresAtNs = int64(value)
	}
	// Bring the row back to active only after ending whatever else is running.
	// Doing it the other way round would leave two active rows whenever the
	// patch itself arrived second.
	reactivating := updated.Status == model.SubscriptionStatusActive && row.Status != model.SubscriptionStatusActive
	if reactivating {
		if err := s.expireActiveSubscriptions(updated.UserID, nowNs); err != nil {
			return SubscriptionView{}, err
		}
	}

	if err := s.Engine.UpdateUserSubscriptionTerms(updated); err != nil {
		return SubscriptionView{}, mapSubscriptionWriteError("write subscription", err)
	}
	if err := s.republishUserKeys(updated.UserID); err != nil {
		return SubscriptionView{}, err
	}

	views, err := s.subscriptionViews([]model.UserSubscription{updated})
	if err != nil {
		return SubscriptionView{}, err
	}
	return views[0], nil
}

// DeleteTenantSubscription ends a subscription: DELETE /admin/subscriptions/{id}.
//
// It no longer deletes the row. A user with no subscription row at all is the
// state the loader and the publish path read as the zero snapshot, which means no
// quota, no expiry and no restriction - unlimited. Removing the last row of an
// account would therefore have handed it more access than it had, not less, and a
// key minted afterwards looked like a key on no plan at all. The row is revoked
// instead (state.RevokeUserSubscription) and the user's keys are republished, so
// access ends with the decision rather than with the next restart.
func (s *ControlPlaneService) DeleteTenantSubscription(id string) error {
	if s == nil || s.Engine == nil {
		return internal("no state engine", nil)
	}
	row, err := s.Engine.GetUserSubscription(id)
	if err != nil {
		return mapSubscriptionLookupError(err)
	}
	if err := s.Engine.RevokeUserSubscription(row.ID); err != nil {
		return mapSubscriptionWriteError("revoke subscription", err)
	}
	return s.republishUserKeys(row.UserID)
}

// expireActiveSubscriptions ends whatever the user has running now.
func (s *ControlPlaneService) expireActiveSubscriptions(userID string, nowNs int64) error {
	rows, err := s.Engine.ListUserSubscriptions()
	if err != nil {
		return internal("list subscriptions", err)
	}
	for _, row := range rows {
		if row.UserID != userID || row.Status != model.SubscriptionStatusActive {
			continue
		}
		updated := row
		updated.Status = model.SubscriptionStatusExpired
		if err := s.Engine.UpdateUserSubscriptionTerms(updated); err != nil {
			return mapSubscriptionWriteError("expire previous subscription", err)
		}
	}
	return nil
}

// subscriptionViews resolves the names a subscription row does not carry.
//
// The plan name comes from the live plan when it still exists and from the
// snapshot when it does not: a subscription whose plan was deleted must still
// render as something recognizable rather than as an empty cell.
func (s *ControlPlaneService) subscriptionViews(rows []model.UserSubscription) ([]SubscriptionView, error) {
	users, err := s.userDirectory()
	if err != nil {
		return nil, err
	}
	plans, err := s.planDirectory()
	if err != nil {
		return nil, err
	}

	views := make([]SubscriptionView, 0, len(rows))
	for _, row := range rows {
		plan, snapshotErr := planFromSnapshotJSON(row.PlanSnapshotJSON)
		if snapshotErr != nil {
			return nil, internal("read plan snapshot", snapshotErr)
		}
		name := plan.Name
		if live, ok := plans[row.PlanID]; ok && live.Name != "" {
			name = live.Name
		}
		view := SubscriptionView{
			ID:              row.ID,
			UserID:          row.UserID,
			PlanID:          row.PlanID,
			PlanName:        name,
			Status:          row.Status,
			StartsAtNs:      row.StartsAtNs,
			ExpiresAtNs:     row.ExpiresAtNs,
			PeriodStartNs:   row.PeriodStartNs,
			PeriodUsedBytes: row.PeriodUsedBytes,
			TrafficBytes:    plan.TrafficBytes,
			AutoRenew:       row.AutoRenew,
			PeriodIndex:     row.PeriodIndex,
			PriceMicro:      plan.PriceMicro,
			RemainingBytes:  remainingBytes(plan.TrafficBytes, row.PeriodUsedBytes),
		}
		if user, ok := users[row.UserID]; ok {
			view.Username = user.Username
		}
		views = append(views, view)
	}
	return views, nil
}

// userDirectory returns every user keyed by id. The console's tables need names
// next to ids, and the whole table is a single cheap read.
func (s *ControlPlaneService) userDirectory() (map[string]model.User, error) {
	rows, err := s.Engine.ListUsers()
	if err != nil {
		return nil, internal("list users", err)
	}
	directory := make(map[string]model.User, len(rows))
	for _, row := range rows {
		directory[row.ID] = row
	}
	return directory, nil
}

// planDirectory returns every plan keyed by id.
func (s *ControlPlaneService) planDirectory() (map[string]model.Plan, error) {
	rows, err := s.Engine.ListPlans()
	if err != nil {
		return nil, internal("list plans", err)
	}
	directory := make(map[string]model.Plan, len(rows))
	for _, row := range rows {
		directory[row.ID] = row
	}
	return directory, nil
}

// currentSubscription returns the row that decides a user's access, or nil when
// the user has no subscription row at all.
//
// It applies the same rule a restart does (keys.DecidingSubscription): the active
// subscription if there is one, otherwise the newest of the ended ones. Reading
// it in one place is what keeps the console, the key publish path and the
// startup loader from disagreeing about a user's plan - and it is why a
// suspended or expired subscription refuses the user's keys instead of
// vanishing and leaving them on the zero snapshot.
//
// The error is a ServiceError because every caller hands it straight to a client.
func (s *ControlPlaneService) currentSubscription(userID string) (*model.UserSubscription, *ServiceError) {
	rows, err := s.Engine.ListUserSubscriptions()
	if err != nil {
		return nil, internal("list subscriptions", err)
	}
	mine := make([]model.UserSubscription, 0, len(rows))
	for _, row := range rows {
		if row.UserID == userID {
			mine = append(mine, row)
		}
	}
	return keys.DecidingSubscription(mine), nil
}

// activeSubscriptionFor returns the user's active subscription, or nil when the
// row that decides their access is not active (or they have none). It is the
// deciding row that is tested, so "is this user subscribed" has one answer
// everywhere, and a user whose newest row is suspended is not reported as
// holding a subscription they cannot use.
func (s *ControlPlaneService) activeSubscriptionFor(userID string) (*model.UserSubscription, error) {
	sub, err := s.currentSubscription(userID)
	if err != nil {
		return nil, err
	}
	if sub == nil || sub.Status != model.SubscriptionStatusActive {
		return nil, nil
	}
	return sub, nil
}

// pageSlice applies limit and offset to an in-memory list. A limit of 0 means
// "every row", which is what the console sends when it wants the whole set.
func pageSlice[T any](rows []T, limit, offset int) []T {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(rows) {
		return []T{}
	}
	rows = rows[offset:]
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}
	return rows
}
