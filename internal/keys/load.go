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
// The subscription snapshot is taken per user from the subscription that
// decides that user's access (DecidingSubscription): the active one if there is
// one, otherwise the newest of the ended ones. A suspended or expired
// subscription is carried as such and refuses the key; only a user with no
// subscription row at all gets the zero snapshot, which means "no plan": no
// platform restriction beyond the key's own, no quota, no expiry.
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
// Which row decides a user's access is settled by DecidingSubscription, whose
// rule is stated there. The snapshot is then built by the same function the
// console publishes with (SnapshotForPlan), so a restart and a change made in
// the console cannot disagree about whether a user may connect; Usable() refuses
// a subscription whose status is not active. A row whose snapshot cannot be
// decoded is skipped and reported rather than guessed at, and only a user with
// no readable row at all keeps the zero snapshot.
func planSnapshots(subscriptions []model.UserSubscription, logf func(format string, args ...any)) map[string]PlanSnapshot {
	plans := make(map[string]model.Plan, len(subscriptions))
	byUser := make(map[string][]model.UserSubscription, len(subscriptions))
	for _, sub := range subscriptions {
		var plan model.Plan
		if raw := strings.TrimSpace(sub.PlanSnapshotJSON); raw != "" {
			if err := json.Unmarshal([]byte(raw), &plan); err != nil {
				// A snapshot that cannot be read must not silently become an
				// unlimited plan, so the row is skipped and reported. The user
				// keeps the global-token path, which is unrestricted but also
				// not what a paying user should silently get.
				if logf != nil {
					logf("Access keys: subscription %s has an unreadable plan snapshot, skipped: %v", sub.ID, err)
				}
				continue
			}
		}
		plans[sub.ID] = plan
		byUser[sub.UserID] = append(byUser[sub.UserID], sub)
	}

	snapshots := make(map[string]PlanSnapshot, len(byUser))
	for userID, rows := range byUser {
		sub := DecidingSubscription(rows)
		if sub == nil {
			continue
		}
		plan := plans[sub.ID]
		snapshots[userID] = SnapshotForPlan(sub, &plan)
	}
	return snapshots
}

// DecidingSubscription returns the row that decides a user's access out of that
// user's subscription rows, or nil when there are none.
//
// An active row decides first, whatever the others say: an active subscription
// is what grants access, and a user who has just been put on a new plan must not
// stay refused because an older, already-ended row happens to expire later. Only
// when nothing is active do the ended rows decide, and then the newest expiry
// wins - that is the subscription an operator last acted on, and carrying it
// (with its own status) is what keeps a suspension or an expiry in force instead
// of silently handing the user the zero snapshot.
//
// It is exported because the console (internal/service) must read the same rule:
// a restart and a change made in the console cannot be allowed to disagree about
// whether a user may connect.
func DecidingSubscription(subs []model.UserSubscription) *model.UserSubscription {
	var best *model.UserSubscription
	for i := range subs {
		candidate := &subs[i]
		if best == nil {
			best = candidate
			continue
		}
		candidateActive := candidate.Status == model.SubscriptionStatusActive
		bestActive := best.Status == model.SubscriptionStatusActive
		if candidateActive != bestActive {
			if candidateActive {
				best = candidate
			}
			continue
		}
		if newerSubscription(candidate, best) {
			best = candidate
		}
	}
	return best
}

// newerSubscription orders two subscriptions of the same standing: the newest
// expiry wins, then the latest start, then the larger id, so the answer does not
// depend on the order the rows came back in.
func newerSubscription(a, b *model.UserSubscription) bool {
	if a.ExpiresAtNs != b.ExpiresAtNs {
		return a.ExpiresAtNs > b.ExpiresAtNs
	}
	if a.StartsAtNs != b.StartsAtNs {
		return a.StartsAtNs > b.StartsAtNs
	}
	return a.ID > b.ID
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
