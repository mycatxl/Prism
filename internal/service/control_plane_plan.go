package service

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"prism/internal/model"
	"prism/internal/platform"
	"prism/internal/state"
)

// Plans are the templates access is sold under: which platforms and IP tiers a
// user may reach, the quotas that apply, and how long a subscription bought from
// the plan lasts.
//
// Two rules shape everything here, and one exception to the first.
//
// A subscription carries a snapshot of the plan it was sold under, so editing a
// plan never silently changes what an existing user bought - unless the operator
// asks for that with apply_to_existing, which rewrites the snapshot of every
// active subscription on the plan and republishes its keys.
//
// The exception is the status. Disabling a plan is the emergency stop: it reaches
// the users already on the plan immediately and unconditionally, because the data
// plane decides from the snapshot inside each subscription and refusing to sell
// the plan would leave everyone who already bought it running. That path rewrites
// only the status - the other fields stay what each user bought - and re-enabling
// the plan does not lift the stop by itself: only apply_to_existing does.

// Plan statuses. They mirror model.Plan.Status and are validated at the edge so
// an unknown value cannot be stored and then read back as "not active".
const (
	planStatusActive   = "active"
	planStatusDisabled = "disabled"
)

// PlanInput is the accepted body of a plan write.
type PlanInput struct {
	Name            string   `json:"name"`
	Platforms       []string `json:"platforms"`
	IPTiers         []string `json:"ip_tiers"`
	TrafficBytes    int64    `json:"traffic_bytes"`
	Period          string   `json:"period"`
	MaxConcurrent   int      `json:"max_concurrent"`
	MaxRPS          int      `json:"max_rps"`
	MaxBandwidthBps int64    `json:"max_bandwidth_bps"`
	MaxKeys         int      `json:"max_keys"`
	DurationDays    int      `json:"duration_days"`
	PriceMicro      int64    `json:"price_micro"`
	Status          string   `json:"status"`
}

// PlanView is a plan as the console sees it: the definition plus how many users
// are on it right now, which is the number an operator needs before editing or
// deleting one.
type PlanView struct {
	model.Plan
	ActiveUserCount int `json:"active_user_count"`
}

// planPatchAllowedFields are the fields PATCH /admin/plans/{id} accepts. It is
// the PlanInput set plus the one instruction that is not part of the plan.
var planPatchAllowedFields = map[string]bool{
	"name":              true,
	"platforms":         true,
	"ip_tiers":          true,
	"traffic_bytes":     true,
	"period":            true,
	"max_concurrent":    true,
	"max_rps":           true,
	"max_bandwidth_bps": true,
	"max_keys":          true,
	"duration_days":     true,
	"price_micro":       true,
	"status":            true,
	"apply_to_existing": true,
}

// ListPlans returns every plan with the number of users currently on it.
func (s *ControlPlaneService) ListPlans() ([]PlanView, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("no state engine", nil)
	}
	plans, err := s.Engine.ListPlans()
	if err != nil {
		return nil, internal("list plans", err)
	}
	counts, err := s.activeUserCounts()
	if err != nil {
		return nil, err
	}

	views := make([]PlanView, 0, len(plans))
	for _, plan := range plans {
		views = append(views, PlanView{Plan: plan, ActiveUserCount: counts[plan.ID]})
	}
	return views, nil
}

// GetPlan returns one plan.
func (s *ControlPlaneService) GetPlan(id string) (PlanView, error) {
	if s == nil || s.Engine == nil {
		return PlanView{}, internal("no state engine", nil)
	}
	plan, err := s.Engine.GetPlan(id)
	if err != nil {
		return PlanView{}, mapPlanLookupError(err)
	}
	counts, err := s.activeUserCounts()
	if err != nil {
		return PlanView{}, err
	}
	return PlanView{Plan: *plan, ActiveUserCount: counts[plan.ID]}, nil
}

// CreatePlan defines a new plan.
func (s *ControlPlaneService) CreatePlan(in PlanInput, nowNs int64) (PlanView, error) {
	if s == nil || s.Engine == nil {
		return PlanView{}, internal("no state engine", nil)
	}
	plan, svcErr := planFromInput(in, nowNs)
	if svcErr != nil {
		return PlanView{}, svcErr
	}
	if err := s.Engine.UpsertPlan(plan); err != nil {
		return PlanView{}, mapPlanWriteError(err)
	}
	return PlanView{Plan: plan, ActiveUserCount: 0}, nil
}

// UpdatePlan applies a patch to a plan.
//
// apply_to_existing is the operator's decision about users already on the plan:
// without it the change applies to future subscriptions only, because a
// subscription keeps the snapshot it was sold under. With it, every active
// subscription on the plan is re-snapshotted and its keys are republished, so
// the change reaches the data plane on the next request rather than on the next
// restart. It is a PATCH field rather than a query parameter so that "what I am
// changing" and "who it reaches" travel together.
//
// status is the one field that does not wait for that switch. Setting it to
// disabled kills the plan for its current users at once (stopSubscriptionsOnPlan),
// which is what makes disabling a plan usable as an emergency stop; setting it
// back to active does not undo those stops, because the users who were cut off
// are the operator's decision to make again with apply_to_existing.
func (s *ControlPlaneService) UpdatePlan(id string, patchJSON json.RawMessage, nowNs int64) (PlanView, error) {
	if s == nil || s.Engine == nil {
		return PlanView{}, internal("no state engine", nil)
	}
	patch, svcErr := parseMergePatch(patchJSON)
	if svcErr != nil {
		return PlanView{}, svcErr
	}
	if svcErr := patch.validateFields(planPatchAllowedFields, func(field string) string {
		return "unknown field: " + field
	}); svcErr != nil {
		return PlanView{}, svcErr
	}

	existing, err := s.Engine.GetPlan(id)
	if err != nil {
		return PlanView{}, mapPlanLookupError(err)
	}
	updated := *existing
	applyToExisting := false

	if value, ok, svcErr := patch.optionalNonEmptyString("name"); svcErr != nil {
		return PlanView{}, svcErr
	} else if ok {
		updated.Name = value
	}
	if value, ok, svcErr := patch.optionalStringSlice("platforms"); svcErr != nil {
		return PlanView{}, svcErr
	} else if ok {
		updated.Platforms = value
	}
	if value, ok, svcErr := patch.optionalStringSlice("ip_tiers"); svcErr != nil {
		return PlanView{}, svcErr
	} else if ok {
		updated.IPTiers = value
	}
	if value, ok, svcErr := patch.optionalInt("traffic_bytes"); svcErr != nil {
		return PlanView{}, svcErr
	} else if ok {
		updated.TrafficBytes = int64(value)
	}
	if value, ok, svcErr := patch.optionalString("period"); svcErr != nil {
		return PlanView{}, svcErr
	} else if ok {
		updated.Period = normalizePlanPeriod(value)
	}
	if value, ok, svcErr := patch.optionalInt("max_concurrent"); svcErr != nil {
		return PlanView{}, svcErr
	} else if ok {
		updated.MaxConcurrent = value
	}
	if value, ok, svcErr := patch.optionalInt("max_rps"); svcErr != nil {
		return PlanView{}, svcErr
	} else if ok {
		updated.MaxRPS = value
	}
	if value, ok, svcErr := patch.optionalInt("max_bandwidth_bps"); svcErr != nil {
		return PlanView{}, svcErr
	} else if ok {
		updated.MaxBandwidthBps = int64(value)
	}
	if value, ok, svcErr := patch.optionalInt("max_keys"); svcErr != nil {
		return PlanView{}, svcErr
	} else if ok {
		updated.MaxKeys = value
	}
	if value, ok, svcErr := patch.optionalInt("duration_days"); svcErr != nil {
		return PlanView{}, svcErr
	} else if ok {
		updated.DurationDays = value
	}
	if value, ok, svcErr := patch.optionalInt("price_micro"); svcErr != nil {
		return PlanView{}, svcErr
	} else if ok {
		updated.PriceMicro = int64(value)
	}
	if value, ok, svcErr := patch.optionalString("status"); svcErr != nil {
		return PlanView{}, svcErr
	} else if ok {
		updated.Status = value
	}
	if value, ok, svcErr := patch.optionalBool("apply_to_existing"); svcErr != nil {
		return PlanView{}, svcErr
	} else if ok {
		applyToExisting = value
	}

	updated.ID = existing.ID
	updated.CreatedAtNs = existing.CreatedAtNs
	updated.UpdatedAtNs = nowNs
	if svcErr := validatePlan(updated); svcErr != nil {
		return PlanView{}, svcErr
	}
	if err := s.Engine.UpsertPlan(updated); err != nil {
		return PlanView{}, mapPlanWriteError(err)
	}

	counts, err := s.activeUserCounts()
	if err != nil {
		return PlanView{}, err
	}

	// A plan that is left disabled stops the users already on it, whether or
	// not apply_to_existing was asked for: disabling a plan is the emergency
	// stop, not a statement about future sales. The check is on the resulting
	// status rather than on the transition, so a stop interrupted half way - a
	// write failed after the plan row was saved - is finished by sending the
	// same PATCH again instead of being skipped as already done.
	if updated.Status == planStatusDisabled {
		if _, err := s.stopSubscriptionsOnPlan(updated); err != nil {
			return PlanView{}, err
		}
	}
	if applyToExisting {
		touched, err := s.applyPlanToExisting(updated)
		if err != nil {
			return PlanView{}, err
		}
		return PlanView{Plan: updated, ActiveUserCount: touched}, nil
	}
	return PlanView{Plan: updated, ActiveUserCount: counts[updated.ID]}, nil
}

// DeletePlan removes a plan. Subscriptions that were sold from it keep their
// snapshot and their window; the database clears their plan_id, so the plan
// itself can go without ending anyone's access mid-period.
func (s *ControlPlaneService) DeletePlan(id string) error {
	if s == nil || s.Engine == nil {
		return internal("no state engine", nil)
	}
	if _, err := s.Engine.GetPlan(id); err != nil {
		return mapPlanLookupError(err)
	}
	if err := s.Engine.DeletePlan(id); err != nil {
		return mapPlanLookupError(err)
	}
	return nil
}

// applyPlanToExisting rewrites the snapshot of every active subscription on the
// plan and republishes its keys. It returns how many subscriptions were
// affected.
func (s *ControlPlaneService) applyPlanToExisting(plan model.Plan) (int, error) {
	subs, err := s.Engine.ListActiveUserSubscriptions()
	if err != nil {
		return 0, internal("list active subscriptions", err)
	}

	snapshot, err := planSnapshotJSON(plan)
	if err != nil {
		return 0, internal("encode plan snapshot", err)
	}

	affected := 0
	for _, sub := range subs {
		if sub.PlanID != plan.ID {
			continue
		}
		updated := sub
		updated.PlanSnapshotJSON = snapshot
		if err := s.Engine.UpdateUserSubscriptionTerms(updated); err != nil {
			return affected, mapSubscriptionWriteError("update subscription snapshot", err)
		}
		if err := s.republishUserKeys(updated.UserID); err != nil {
			return affected, err
		}
		affected++
	}
	return affected, nil
}

// stopSubscriptionsOnPlan forces the snapshots of the plan's active
// subscriptions to status disabled, and republishes the keys of each affected
// user. It returns how many subscriptions were stopped.
//
// Disabling a plan is the emergency stop, so this path does not ask for
// apply_to_existing and does not touch anything else in the snapshot: the plan's
// fields are still what each user bought, and it is only the status - which is
// what Usable reads - that has to change everywhere at once. Re-enabling a plan
// leaves these stopped snapshots alone, because lifting a stop is a decision
// about the users it cut off, and that decision is apply_to_existing.
func (s *ControlPlaneService) stopSubscriptionsOnPlan(plan model.Plan) (int, error) {
	subs, err := s.Engine.ListActiveUserSubscriptions()
	if err != nil {
		return 0, internal("list active subscriptions", err)
	}

	stopped := 0
	for _, sub := range subs {
		if sub.PlanID != plan.ID {
			continue
		}
		snapshot, err := disabledPlanSnapshot(sub.PlanSnapshotJSON, plan)
		if err != nil {
			return stopped, internal("encode plan snapshot", err)
		}
		updated := sub
		updated.PlanSnapshotJSON = snapshot
		if err := s.Engine.UpdateUserSubscriptionTerms(updated); err != nil {
			return stopped, mapSubscriptionWriteError("stop subscription", err)
		}
		if err := s.republishUserKeys(updated.UserID); err != nil {
			return stopped, err
		}
		stopped++
	}
	return stopped, nil
}

// disabledPlanSnapshot returns a stored subscription snapshot with its status
// rewritten to disabled.
//
// A snapshot that cannot be decoded yields the plan as it stands with the status
// disabled: a snapshot that never parsed has nothing left to preserve, and
// leaving it in place would leave the subscription's keys running - which is the
// one outcome this path exists to prevent.
func disabledPlanSnapshot(raw string, plan model.Plan) (string, error) {
	if stored, err := planFromSnapshotJSON(raw); err == nil {
		plan = stored
	}
	plan.Status = planStatusDisabled
	return planSnapshotJSON(plan)
}

// activeUserCounts counts the users holding an active subscription, per plan.
func (s *ControlPlaneService) activeUserCounts() (map[string]int, error) {
	subs, err := s.Engine.ListActiveUserSubscriptions()
	if err != nil {
		return nil, internal("list active subscriptions", err)
	}
	counts := make(map[string]int, len(subs))
	for _, sub := range subs {
		if sub.PlanID == "" {
			continue
		}
		counts[sub.PlanID]++
	}
	return counts, nil
}

// planFromInput validates a create body and turns it into a row.
func planFromInput(in PlanInput, nowNs int64) (model.Plan, *ServiceError) {
	id, err := newID("pl_")
	if err != nil {
		return model.Plan{}, internal("generate plan id", err)
	}
	plan := model.Plan{
		ID:              id,
		Name:            strings.TrimSpace(in.Name),
		Platforms:       normalizeStringList(in.Platforms),
		IPTiers:         normalizeStringList(in.IPTiers),
		TrafficBytes:    in.TrafficBytes,
		Period:          normalizePlanPeriod(in.Period),
		MaxConcurrent:   in.MaxConcurrent,
		MaxRPS:          in.MaxRPS,
		MaxBandwidthBps: in.MaxBandwidthBps,
		MaxKeys:         in.MaxKeys,
		DurationDays:    in.DurationDays,
		PriceMicro:      in.PriceMicro,
		Status:          strings.TrimSpace(in.Status),
		CreatedAtNs:     nowNs,
		UpdatedAtNs:     nowNs,
	}
	if svcErr := validatePlan(plan); svcErr != nil {
		return model.Plan{}, svcErr
	}
	plan.Period = defaultPlanPeriod(plan.Period)
	plan.Status = defaultPlanStatus(plan.Status)
	return plan, nil
}

// validatePlan enforces the rules a plan has to satisfy whatever route created
// it. Cross-field rules only: the per-field shapes are checked by the patch
// helpers before this runs.
func validatePlan(plan model.Plan) *ServiceError {
	if strings.TrimSpace(plan.Name) == "" {
		return invalidArg("name: must not be empty")
	}
	if plan.DurationDays <= 0 {
		return invalidArg("duration_days: must be greater than zero")
	}
	switch plan.Status {
	case "", planStatusActive, planStatusDisabled:
	default:
		return invalidArg("status: must be active or disabled")
	}
	if plan.TrafficBytes < 0 || plan.MaxBandwidthBps < 0 {
		return invalidArg("traffic_bytes and max_bandwidth_bps: must not be negative")
	}
	if plan.MaxConcurrent < 0 || plan.MaxRPS < 0 || plan.MaxKeys < 0 {
		return invalidArg("max_concurrent, max_rps and max_keys: must not be negative")
	}
	// A price of zero is legal and means the plan is not for sale. A negative
	// one is not a price, and one above the balance cap is a typo that would
	// otherwise be stored and then never be affordable.
	if plan.PriceMicro < 0 {
		return invalidArg("price_micro: must not be negative")
	}
	if plan.PriceMicro > maxBalanceMicro {
		return invalidArg("price_micro: the amount is out of range")
	}
	if !validPlanPeriod(plan.Period) {
		return invalidArg("period: must be month, week, day, once or a number of days such as 30d")
	}
	for i, tier := range plan.IPTiers {
		ipType, band, ok := strings.Cut(tier, ":")
		if !ok {
			return invalidArg("ip_tiers[" + strconv.Itoa(i) + "]: must be \"ip_type:purity_band\"")
		}
		// The vocabularies are the quality model's, so a tier that can never
		// match a node is refused here rather than quietly selecting nothing.
		if err := platform.ValidateNodeCriteria([]string{ipType}, []string{band}, nil, nil); err != nil {
			return invalidArg("ip_tiers[" + strconv.Itoa(i) + "]: " + err.Error())
		}
	}
	return nil
}

// defaultPlanPeriod normalizes a plan's period and fills in the default.
//
// The canonical spelling is stored rather than the one typed: the rollover that
// reads this field accepts "Month" and " month " as well as "month", so keeping
// the variant in the table would leave two spellings of the same period in it,
// and only one of them matches what the console offers.
func defaultPlanPeriod(period string) string {
	normalized := normalizePlanPeriod(period)
	if normalized == "" {
		return "30d"
	}
	return normalized
}

// normalizePlanPeriod trims and lower cases a period, matching internal/usage's
// normalizePeriod so a plan written here is read back as the period it means.
func normalizePlanPeriod(period string) string {
	return strings.ToLower(strings.TrimSpace(period))
}

// validPlanPeriod reports whether a rollover can be computed for a period.
//
// The accepted values are the ones internal/usage acts on: the named periods
// (month, week, day, once), the "<n>d" form its default uses, and the empty
// string, which this package turns into "30d" rather than storing. Anything else
// is refused at the API boundary: it would be stored happily, enforced as a
// quota and never reset, which reads to the user as a plan that took their money
// and their allowance with it.
func validPlanPeriod(period string) bool {
	normalized := normalizePlanPeriod(period)
	switch normalized {
	case "", "month", "day", "week", "once":
		return true
	}
	digits := strings.TrimSuffix(normalized, "d")
	if digits == "" || digits == normalized {
		return false
	}
	days, err := strconv.Atoi(digits)
	return err == nil && days > 0
}

func defaultPlanStatus(status string) string {
	if status == "" {
		return planStatusActive
	}
	return status
}

// normalizeStringList trims the entries and drops the empty ones, so "" and a
// missing list mean the same thing: no restriction.
func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		result = append(result, trimmed)
	}
	return result
}

func mapPlanLookupError(err error) *ServiceError {
	if errors.Is(err, state.ErrNotFound) {
		return notFound("plan not found")
	}
	return internal("read plan", err)
}

func mapPlanWriteError(err error) *ServiceError {
	if errors.Is(err, state.ErrConflict) {
		return conflict("a plan with that name already exists")
	}
	return internal("write plan", err)
}
