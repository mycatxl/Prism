package keys

import (
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/model"
)

// The store is rebuilt from the database at startup (Load). These functions are
// the incremental half: they put one key back after the console changes it, so a
// revoked key stops working on the next request instead of on the next restart.

// Publish puts one key into the live store, replacing whatever was there.
//
// Access keys are read on the data plane's hot path, so a change made in the
// console has to reach the store rather than wait for a restart. A nil store is
// a no-op: the console still works when the data plane is not running.
func (s *Store) Publish(key model.AccessKey, user model.User, plan PlanSnapshot) {
	if s == nil {
		return
	}
	s.Upsert(newKeyState(key, user, plan))
}

// SnapshotForPlan builds the live plan snapshot for one subscription.
//
// The plan contributes the quotas and the platform set; the subscription
// contributes the window and, importantly, the status. A suspended or expired
// subscription must stop the key even though the plan it points at is still on
// sale, so the subscription's status wins whenever it is not active.
//
// A nil plan is not an error: a subscription whose plan was deleted still
// carries a window, and the zero quotas mean unlimited.
func SnapshotForPlan(sub *model.UserSubscription, plan *model.Plan) PlanSnapshot {
	var snap PlanSnapshot
	if plan != nil {
		snap.Platforms = append([]string(nil), plan.Platforms...)
		snap.IPTiers = append([]string(nil), plan.IPTiers...)
		snap.TrafficBytes = plan.TrafficBytes
		snap.MaxConcurrent = plan.MaxConcurrent
		snap.MaxRPS = plan.MaxRPS
		snap.MaxBandwidthBps = plan.MaxBandwidthBps
		snap.Status = plan.Status
	}
	if sub == nil {
		return snap
	}
	snap.ExpiresAtNs = sub.ExpiresAtNs
	if sub.Status != model.SubscriptionStatusActive {
		snap.Status = sub.Status
	}
	return snap
}

// SnapshotFromSubscription builds the live snapshot from a subscription row
// alone, reading the plan out of the snapshot the subscription carries.
//
// It is what republishing after an edit uses: the row is authoritative, and the
// plan it was sold under may since have been changed or deleted. An unreadable
// snapshot is reported rather than treated as unlimited - silently widening a
// user's quota is the one failure mode worth being loud about.
func SnapshotFromSubscription(sub *model.UserSubscription) (PlanSnapshot, error) {
	if sub == nil {
		return PlanSnapshot{}, nil
	}

	var plan model.Plan
	raw := strings.TrimSpace(sub.PlanSnapshotJSON)
	if raw == "" {
		return SnapshotForPlan(sub, nil), nil
	}
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return PlanSnapshot{}, fmt.Errorf("keys: subscription %s has an unreadable plan snapshot: %w", sub.ID, err)
	}
	return SnapshotForPlan(sub, &plan), nil
}

// MarshalPlanSnapshot renders a plan the way PlanSnapshotJSON stores it.
//
// It exists so the write path and the read path (load.go) agree on the shape
// without either holding the other's format in its head.
func MarshalPlanSnapshot(plan model.Plan) (string, error) {
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("keys: encode plan snapshot: %w", err)
	}
	return string(encoded), nil
}
