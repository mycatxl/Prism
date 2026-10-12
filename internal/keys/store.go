// Package keys holds the in-memory view of the active access keys.
//
// The data plane reads a key on every new connection, so this store is built
// once at startup from the database and refreshed in place when the management
// API changes a key. Nothing here touches the database: the caller owns
// persistence and calls Upsert or Delete after a successful write.
//
// Reads are lock-free. The store is an xsync.Map, so a refresh that replaces
// one key does not block readers of any other key, and the values are treated
// as immutable once published.
package keys

import (
	"fmt"
	"time"

	"github.com/puzpuzpuz/xsync/v4"

	"prism/internal/model"
)

// PlanSnapshot is the part of a plan that decides whether a request is allowed
// and how much of the quota is left. It is copied into the key state so the
// data plane never has to look a plan up.
//
// The zero value means "no plan": the key then has no platform restriction
// beyond its own, no quota and no expiry.
type PlanSnapshot struct {
	Platforms       []string
	IPTiers         []string
	TrafficBytes    int64
	MaxConcurrent   int
	MaxRPS          int
	MaxBandwidthBps int64
	ExpiresAtNs     int64
	Status          string
}

// AllowsAllPlatforms reports whether the snapshot restricts platforms at all.
func (p PlanSnapshot) AllowsAllPlatforms() bool {
	return len(p.Platforms) == 0
}

// ExpiresAt returns the plan expiry as a time, or the zero time when the plan
// does not expire.
func (p PlanSnapshot) ExpiresAt() time.Time {
	if p.ExpiresAtNs <= 0 {
		return time.Time{}
	}
	return time.Unix(0, p.ExpiresAtNs)
}

// KeyState is the immutable per-key snapshot the data plane reads.
//
// UserStatus and PlanSnapshot are copied in rather than looked up per request,
// which is what keeps the hot path free of database access. Platforms is the
// key's own restriction; when it is empty the plan's set applies instead.
type KeyState struct {
	KeyID       string
	UserID      string
	SecretHash  []byte
	Scope       string
	Status      string
	ExpiresAtNs int64
	Platforms   []string
	UserStatus  string
	UserRole    string
	Plan        PlanSnapshot
}

// Usable reports whether the key may be used at all: the key itself must be
// active and unexpired, the user must be active, and the plan must be active
// and unexpired. Per-request checks (platform, quota, concurrency) build on
// this.
func (k *KeyState) Usable(nowNs int64) bool {
	if k == nil {
		return false
	}
	if k.Status != model.KeyStatusActive {
		return false
	}
	if k.ExpiresAtNs > 0 && nowNs >= k.ExpiresAtNs {
		return false
	}
	if k.UserStatus != model.UserStatusActive {
		return false
	}
	if k.Plan.Status != "" && k.Plan.Status != model.SubscriptionStatusActive {
		return false
	}
	if k.Plan.ExpiresAtNs > 0 && nowNs >= k.Plan.ExpiresAtNs {
		return false
	}
	return true
}

// EffectivePlatforms returns the platform restriction that applies to this key:
// the key's own list when it sets one, otherwise the plan's. An empty result
// means the key is not restricted by either.
func (k *KeyState) EffectivePlatforms() []string {
	if k == nil {
		return nil
	}
	if len(k.Platforms) > 0 {
		return k.Platforms
	}
	return k.Plan.Platforms
}

// AllowsPlatform reports whether the key may reach the named platform. An empty
// effective set allows every platform.
//
// A nil state allows nothing: a key that cannot be resolved must fail closed
// rather than fall through to an unrestricted path.
func (k *KeyState) AllowsPlatform(platformID string) bool {
	if k == nil {
		return false
	}
	effective := k.EffectivePlatforms()
	if len(effective) == 0 {
		return true
	}
	for _, allowed := range effective {
		if allowed == platformID {
			return true
		}
	}
	return false
}

// Store holds every active key.
type Store struct {
	keys *xsync.Map[string, *KeyState]
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{keys: xsync.NewMap[string, *KeyState]()}
}

// Load replaces the whole store from the database rows.
//
// keys are the active access keys, users maps user id to user, and plans maps
// user id to that user's active subscription snapshot. A key whose user is
// missing is skipped rather than published: a key without an owner cannot be
// authorized.
func (s *Store) Load(
	accessKeys []model.AccessKey,
	users map[string]model.User,
	plans map[string]PlanSnapshot,
) error {
	if s == nil {
		return fmt.Errorf("keys: nil store")
	}

	next := xsync.NewMap[string, *KeyState]()
	for _, key := range accessKeys {
		user, ok := users[key.UserID]
		if !ok {
			continue
		}
		next.Store(key.ID, newKeyState(key, user, plans[key.UserID]))
	}
	s.keys = next
	return nil
}

// newKeyState builds the immutable snapshot for one key.
//
// The slices are copied so a later mutation of the source row cannot change
// what the data plane reads.
func newKeyState(key model.AccessKey, user model.User, plan PlanSnapshot) *KeyState {
	state := &KeyState{
		KeyID:       key.ID,
		UserID:      key.UserID,
		SecretHash:  append([]byte(nil), key.SecretHash...),
		Scope:       key.Scope,
		Status:      key.Status,
		ExpiresAtNs: key.ExpiresAtNs,
		UserStatus:  user.Status,
		UserRole:    user.Role,
		Plan: PlanSnapshot{
			TrafficBytes:    plan.TrafficBytes,
			MaxConcurrent:   plan.MaxConcurrent,
			MaxRPS:          plan.MaxRPS,
			MaxBandwidthBps: plan.MaxBandwidthBps,
			ExpiresAtNs:     plan.ExpiresAtNs,
			Status:          plan.Status,
			Platforms:       append([]string(nil), plan.Platforms...),
			IPTiers:         append([]string(nil), plan.IPTiers...),
		},
	}
	if len(key.Platforms) > 0 {
		state.Platforms = append([]string(nil), key.Platforms...)
	}
	return state
}

// Get returns the key state for a key id.
func (s *Store) Get(keyID string) (*KeyState, bool) {
	if s == nil || s.keys == nil {
		return nil, false
	}
	return s.keys.Load(keyID)
}

// Upsert publishes one key, replacing any previous state for it.
func (s *Store) Upsert(state *KeyState) {
	if s == nil || s.keys == nil || state == nil {
		return
	}
	s.keys.Store(state.KeyID, state)
}

// Delete removes one key. A revoked key stops being resolvable immediately.
func (s *Store) Delete(keyID string) {
	if s == nil || s.keys == nil {
		return
	}
	s.keys.Delete(keyID)
}

// Len reports how many keys are published.
func (s *Store) Len() int {
	if s == nil || s.keys == nil {
		return 0
	}
	return s.keys.Size()
}

// Range calls fn for every published key. Returning false stops the walk.
func (s *Store) Range(fn func(state *KeyState) bool) {
	if s == nil || s.keys == nil || fn == nil {
		return
	}
	s.keys.Range(func(_ string, state *KeyState) bool {
		return fn(state)
	})
}
