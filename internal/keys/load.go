package keys

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"prism/internal/model"
)

// StateReader is the persistence surface the loader needs.
//
// It is deliberately narrow: the loader reads three tables once at startup and
// is the only thing in this package that touches a database, which keeps the
// store itself pure and the wiring testable with a fake.
type StateReader interface {
	ListActiveAccessKeys() ([]model.AccessKey, error)
	ListUsers() ([]model.User, error)
	ListUserSubscriptions() ([]model.UserSubscription, error)
}

// LoadFromState builds a store from the database.
//
// A missing pepper is not a failure here: without it every key check fails and
// the data plane falls back to the transitional global token, which is the
// behaviour a deployment that has not been migrated yet must keep. The caller
// reports that degradation; this function only reports what it could not read.
//
// The subscription snapshot is taken per user from their active subscription.
// A user with no active subscription gets the zero snapshot, which means "no
// plan": no platform restriction beyond the key's own, no quota, no expiry.
func LoadFromState(reader StateReader, logf func(format string, args ...any)) (*Store, error) {
	if reader == nil {
		return nil, fmt.Errorf("keys: nil state reader")
	}

	accessKeys, err := reader.ListActiveAccessKeys()
	if err != nil {
		return nil, fmt.Errorf("keys: list access keys: %w", err)
	}
	users, err := reader.ListUsers()
	if err != nil {
		return nil, fmt.Errorf("keys: list users: %w", err)
	}
	subscriptions, err := reader.ListUserSubscriptions()
	if err != nil {
		return nil, fmt.Errorf("keys: list subscriptions: %w", err)
	}

	userByID := make(map[string]model.User, len(users))
	for _, user := range users {
		userByID[user.ID] = user
	}

	store := NewStore()
	if err := store.Load(accessKeys, userByID, planSnapshots(subscriptions, logf)); err != nil {
		return nil, err
	}
	return store, nil
}

// planSnapshots reduces the subscription rows to one snapshot per user.
//
// The plan itself is read from the subscription's stored snapshot rather than
// from the plans table: that snapshot is what the quota was computed from when
// the subscription was opened, and it is what stays computable after the plan
// is edited or deleted.
//
// Only an active subscription is carried: a suspended or expired one must not
// grant anything, and the key state's own check would refuse it anyway, but
// leaving it out here means the data plane never even holds the numbers. When a
// user somehow has several active rows the newest expiry wins, which is the
// reading that cannot lock a user out of a subscription they just renewed.
func planSnapshots(subscriptions []model.UserSubscription, logf func(format string, args ...any)) map[string]PlanSnapshot {
	snapshots := make(map[string]PlanSnapshot)
	expiry := make(map[string]int64)

	for _, sub := range subscriptions {
		if sub.Status != model.SubscriptionStatusActive {
			continue
		}
		if previous, ok := expiry[sub.UserID]; ok && previous >= sub.ExpiresAtNs {
			continue
		}

		var plan model.Plan
		raw := strings.TrimSpace(sub.PlanSnapshotJSON)
		if raw == "" {
			// A subscription without a snapshot still grants what the row
			// itself carries: the window. The quota is then unknown, which is
			// the zero value, and zero means unlimited.
			plan = model.Plan{}
		} else if err := json.Unmarshal([]byte(raw), &plan); err != nil {
			// A snapshot that cannot be read must not silently become an
			// unlimited plan, so the subscription is skipped and reported.
			// The user keeps the global-token path, which is unrestricted but
			// also not what a paying user should silently get.
			if logf != nil {
				logf("Access keys: subscription %s has an unreadable plan snapshot, skipped: %v", sub.ID, err)
			}
			continue
		}

		snapshots[sub.UserID] = PlanSnapshot{
			Platforms:       append([]string(nil), plan.Platforms...),
			IPTiers:         append([]string(nil), plan.IPTiers...),
			TrafficBytes:    plan.TrafficBytes,
			MaxConcurrent:   plan.MaxConcurrent,
			MaxRPS:          plan.MaxRPS,
			MaxBandwidthBps: plan.MaxBandwidthBps,
			ExpiresAtNs:     sub.ExpiresAtNs,
			Status:          sub.Status,
		}
		expiry[sub.UserID] = sub.ExpiresAtNs
	}
	return snapshots
}

// LogSummary reports what was published, so an operator can see at a glance
// whether the keys they created are actually live in the running process.
func LogSummary(store *Store, logf func(format string, args ...any)) {
	if logf == nil {
		logf = log.Printf
	}
	if store == nil {
		logf("Access keys: none loaded")
		return
	}
	logf("Access keys: %d loaded", store.Len())
}
