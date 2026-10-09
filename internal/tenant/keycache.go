package tenant

import (
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
)

// ErrAuthFailed is the single error returned for unknown keys, wrong secrets,
// disabled keys/users, expired keys and inactive subscriptions, so callers
// cannot distinguish them (feeds the existing auth-failure rate limiter).
var ErrAuthFailed = errors.New("tenant: authentication failed")

// UserState is shared by all keys of one user. The data plane only reads
// atomics; control-plane updates swap them.
type UserState struct {
	ID string

	admin  atomic.Bool
	active atomic.Bool                  // user status == active
	sub    atomic.Pointer[Subscription] // active subscription, nil = none

	// Concurrent is the user's open-connection count (P3 quota checks).
	Concurrent atomic.Int32
}

// Active reports whether the user is enabled.
func (u *UserState) Active() bool { return u.active.Load() }

// Subscription returns the active subscription, or nil.
func (u *UserState) Subscription() *Subscription { return u.sub.Load() }

// Unrestricted reports whether limits do not apply (admins).
func (u *UserState) Unrestricted() bool { return u.admin.Load() }

// Role returns the user's role.
func (u *UserState) Role() Role {
	if u.admin.Load() {
		return RoleAdmin
	}
	return RoleUser
}

// KeyState is the in-memory view of one access key. It is immutable except
// for the atomics; a key edit replaces the whole KeyState.
type KeyState struct {
	ID          string
	Scope       string // ScopeProxy or ScopeAdmin
	User        *UserState
	secretHash  []byte
	expiresAtNs int64
	platforms   map[string]struct{} // nil = no key-level restriction
	ipAllowlist []netip.Prefix      // nil = any source

	active     atomic.Bool
	lastUsedNs atomic.Int64

	// Concurrent is the key's open-connection count (P3 quota checks).
	Concurrent atomic.Int32
}

// AllowsPlatform reports whether platformID is in plan ∩ key platforms.
// Admin users are only limited by the key restriction.
func (k *KeyState) AllowsPlatform(platformID string) bool {
	if k.platforms != nil {
		if _, ok := k.platforms[platformID]; !ok {
			return false
		}
	}
	if k.User.Unrestricted() {
		return true
	}
	sub := k.User.Subscription()
	if sub == nil {
		return false
	}
	for _, p := range sub.Snapshot.Platforms {
		if p == platformID || p == "*" {
			return true
		}
	}
	return false
}

// AllowsSource reports whether the client address passes the key's source-IP
// allowlist.
func (k *KeyState) AllowsSource(addr netip.Addr) bool {
	if k.ipAllowlist == nil {
		return true
	}
	addr = addr.Unmap()
	for _, p := range k.ipAllowlist {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// LastUsedNs returns the last successful authentication time.
func (k *KeyState) LastUsedNs() int64 { return k.lastUsedNs.Load() }

// KeyCache holds every access key in memory so data-plane authentication
// never touches the database. Reads take a read lock on a map lookup;
// writes happen only on control-plane changes.
type KeyCache struct {
	hmacKey []byte

	mu    sync.RWMutex
	keys  map[string]*KeyState
	users map[string]*UserState
}

// NewKeyCache returns an empty cache; hmacKey is Keys.KeyHMAC.
func NewKeyCache(hmacKey []byte) *KeyCache {
	return &KeyCache{
		hmacKey: hmacKey,
		keys:    make(map[string]*KeyState),
		users:   make(map[string]*UserState),
	}
}

// Load replaces the cache contents with records (from Store.LoadAuthSnapshot).
func (c *KeyCache) Load(records []AuthRecord) error {
	keys := make(map[string]*KeyState, len(records))
	users := make(map[string]*UserState)
	for i := range records {
		ks, err := buildKeyState(&records[i], users)
		if err != nil {
			return err
		}
		keys[ks.ID] = ks
	}
	c.mu.Lock()
	c.keys, c.users = keys, users
	c.mu.Unlock()
	return nil
}

// Put inserts or replaces one key (after create/update). The owner's user
// state is refreshed from rec as well.
func (c *KeyCache) Put(rec AuthRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ks, err := buildKeyState(&rec, c.users)
	if err != nil {
		return err
	}
	c.keys[ks.ID] = ks
	return nil
}

// Remove drops a key. It returns the removed state so callers can close its
// live connections.
func (c *KeyCache) Remove(keyID string) *KeyState {
	c.mu.Lock()
	defer c.mu.Unlock()
	ks := c.keys[keyID]
	delete(c.keys, keyID)
	return ks
}

// SetUserActive flips a user's status for all of its keys at once.
func (c *KeyCache) SetUserActive(userID string, active bool) {
	c.mu.RLock()
	u := c.users[userID]
	c.mu.RUnlock()
	if u != nil {
		u.active.Store(active)
	}
}

// SetSubscription replaces a user's active subscription (nil = none).
func (c *KeyCache) SetSubscription(userID string, sub *Subscription) {
	c.mu.RLock()
	u := c.users[userID]
	c.mu.RUnlock()
	if u != nil {
		u.sub.Store(cloneSub(sub))
	}
}

// RemoveUser drops every key of a user and returns them.
func (c *KeyCache) RemoveUser(userID string) []*KeyState {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*KeyState
	for id, ks := range c.keys {
		if ks.User.ID == userID {
			out = append(out, ks)
			delete(c.keys, id)
		}
	}
	delete(c.users, userID)
	return out
}

// User returns the cached state of a user, or nil.
func (c *KeyCache) User(userID string) *UserState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.users[userID]
}

// UserKeys returns the cached keys of a user.
func (c *KeyCache) UserKeys(userID string) []*KeyState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []*KeyState
	for _, ks := range c.keys {
		if ks.User.ID == userID {
			out = append(out, ks)
		}
	}
	return out
}

// Len returns the number of cached keys.
func (c *KeyCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.keys)
}

// Authenticate verifies "pk_<id>_<secret>" at nowNs. It checks, in order:
// key exists, secret matches (constant time), admin-scoped keys belong to an
// admin, key active and unexpired, user active, and for non-admin users an active, unexpired subscription. Every
// failure returns ErrAuthFailed. Scope, platform and source checks are left
// to the caller (KeyState.Scope, AllowsPlatform, AllowsSource) so each entry
// point can return its own error.
func (c *KeyCache) Authenticate(credential string, nowNs int64) (*KeyState, error) {
	id, secret, ok := ParseKey(credential)
	if !ok {
		return nil, ErrAuthFailed
	}
	c.mu.RLock()
	ks := c.keys[id]
	c.mu.RUnlock()
	if ks == nil {
		// Burn the same HMAC time as a real check.
		_ = VerifySecret(c.hmacKey, secret, nil)
		return nil, ErrAuthFailed
	}
	if !VerifySecret(c.hmacKey, secret, ks.secretHash) {
		return nil, ErrAuthFailed
	}
	if ks.Scope == ScopeAdmin && !ks.User.Unrestricted() {
		return nil, ErrAuthFailed // admin keys die with the admin role
	}
	if !ks.active.Load() || (ks.expiresAtNs > 0 && nowNs >= ks.expiresAtNs) || !ks.User.Active() {
		return nil, ErrAuthFailed
	}
	if !ks.User.Unrestricted() {
		sub := ks.User.Subscription()
		if sub == nil || sub.Status != SubActive || nowNs >= sub.ExpiresAtNs {
			return nil, ErrAuthFailed
		}
	}
	ks.lastUsedNs.Store(nowNs)
	return ks, nil
}

// DrainLastUsed returns keyID → last-used time for keys used since the
// previous call, for Store.TouchKeysUsed.
func (c *KeyCache) DrainLastUsed(sinceNs int64) map[string]int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]int64)
	for id, ks := range c.keys {
		if t := ks.lastUsedNs.Load(); t > sinceNs {
			out[id] = t
		}
	}
	return out
}

func buildKeyState(rec *AuthRecord, users map[string]*UserState) (*KeyState, error) {
	k := &rec.Key
	u := users[k.UserID]
	if u == nil {
		u = &UserState{ID: k.UserID}
		users[k.UserID] = u
	}
	u.admin.Store(rec.UserRole == RoleAdmin)
	u.active.Store(rec.UserStatus == StatusActive)
	u.sub.Store(cloneSub(rec.Subscription))

	ks := &KeyState{
		ID:          k.ID,
		Scope:       k.Scope,
		User:        u,
		secretHash:  append([]byte(nil), k.SecretHash...),
		expiresAtNs: k.ExpiresAtNs,
	}
	if ks.Scope == "" {
		ks.Scope = ScopeProxy
	}
	if k.Platforms != nil {
		ks.platforms = make(map[string]struct{}, len(k.Platforms))
		for _, p := range k.Platforms {
			ks.platforms[p] = struct{}{}
		}
	}
	if k.IPAllowlist != nil {
		ks.ipAllowlist = make([]netip.Prefix, 0, len(k.IPAllowlist))
		for _, raw := range k.IPAllowlist {
			p, err := ParseSourcePrefix(raw)
			if err != nil {
				return nil, err
			}
			ks.ipAllowlist = append(ks.ipAllowlist, p)
		}
	}
	ks.active.Store(k.Status == StatusActive)
	ks.lastUsedNs.Store(k.LastUsedNs)
	return ks, nil
}

// ParseSourcePrefix accepts "1.2.3.4", "1.2.3.0/24" or an IPv6 equivalent.
func ParseSourcePrefix(raw string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(raw); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func cloneSub(s *Subscription) *Subscription {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}
