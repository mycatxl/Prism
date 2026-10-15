package service

import (
	"encoding/json"
	"errors"
	"strings"

	"prism/internal/model"
	"prism/internal/state"
)

// A subscription is what actually grants access: it binds one user to one plan
// and carries the snapshot of that plan as it was sold.
//
// Two rules that are easy to get wrong, and are therefore stated once here. A
// user has at most one active subscription, so assigning a new one expires the
// previous one instead of stacking windows. And a subscription keeps the plan
// snapshot it was created with, so editing a plan does not silently change what
// an existing user bought - only the plan's own "apply to existing" switch does
// that.

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
// subscription is expired rather than deleted, because the history of who was on
// what is worth keeping.
func (s *ControlPlaneService) AssignSubscription(userID, planID string, durationDays int, nowNs int64) (SubscriptionView, error) {
	if s == nil || s.Engine == nil {
		return SubscriptionView{}, internal("no state engine", nil)
	}
	if durationDays < 0 {
		return SubscriptionView{}, invalidArg("duration_days: must not be negative")
	}
	if _, err := s.Engine.GetUser(userID); err != nil {
		return SubscriptionView{}, mapUserLookupError(err)
	}

	var plan model.Plan
	if strings.TrimSpace(planID) != "" {
		row, err := s.Engine.GetPlan(planID)
		if err != nil {
			return SubscriptionView{}, mapPlanLookupError(err)
		}
		plan = *row
		if plan.Status == planStatusDisabled {
			return SubscriptionView{}, invalidArg("plan_id: the plan is disabled")
		}
	}

	days := durationDays
	if days == 0 {
		days = plan.DurationDays
	}
	if days <= 0 {
		return SubscriptionView{}, invalidArg("duration_days: a plan without a duration must be given one")
	}

	snapshot, err := planSnapshotJSON(plan)
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
		return SubscriptionView{}, internal("write subscription", err)
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

	if err := s.Engine.UpsertUserSubscription(updated); err != nil {
		return SubscriptionView{}, internal("write subscription", err)
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

// DeleteTenantSubscription removes a subscription row. The user's keys are
// republished afterwards, so access ends with the row rather than with the next
// restart.
func (s *ControlPlaneService) DeleteTenantSubscription(id string) error {
	if s == nil || s.Engine == nil {
		return internal("no state engine", nil)
	}
	row, err := s.Engine.GetUserSubscription(id)
	if err != nil {
		return mapSubscriptionLookupError(err)
	}
	if err := s.Engine.DeleteUserSubscription(id); err != nil {
		return mapSubscriptionLookupError(err)
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
		if err := s.Engine.UpsertUserSubscription(updated); err != nil {
			return internal("expire previous subscription", err)
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

// activeSubscriptionFor returns the user's active subscription, or nil.
func (s *ControlPlaneService) activeSubscriptionFor(userID string) (*model.UserSubscription, error) {
	row, err := s.Engine.GetActiveUserSubscription(userID)
	if errors.Is(err, state.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, internal("read active subscription", err)
	}
	return row, nil
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
