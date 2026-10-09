package tenant

import "prism/internal/model"

// Store is the multi-tenant persistence API. internal/state implements it on
// state.db. Methods that take a userID scope the operation to that owner and
// return ErrNotFound for rows owned by someone else (IDOR-safe by default).
type Store interface {
	// Users.
	CreateUser(u User) error
	GetUser(id string) (*User, error)
	GetUserByUsername(username string) (*User, error)
	ListUsers() ([]User, error)
	UpdateUser(id string, p UserPatch, nowNs int64) error
	TouchUserLogin(id string, nowNs int64) error
	// DeleteUser cascades to keys, subscriptions, sessions and orders.
	// The built-in admin cannot be deleted.
	DeleteUser(id string) error

	// Plans.
	CreatePlan(p Plan) error
	UpdatePlan(p Plan) error
	GetPlan(id string) (*Plan, error)
	ListPlans() ([]Plan, error)
	// CountActiveSubscribers reports active subscriptions per plan ID.
	CountActiveSubscribers() (map[string]int, error)

	// Subscriptions. Activate expires any active subscription of the same
	// user and inserts s in one transaction.
	ActivateSubscription(s Subscription) error
	GetActiveSubscription(userID string) (*Subscription, error)
	ListSubscriptions(userID string) ([]Subscription, error)
	SetSubscriptionStatus(id, status string, nowNs int64) error
	ExtendSubscription(id string, expiresAtNs, nowNs int64) error
	// SyncPlanSnapshot rewrites the snapshot of every active subscription of
	// planID ("sync to existing users").
	SyncPlanSnapshot(planID string, limits PlanLimits, nowNs int64) (int64, error)
	// ResetPeriod starts a new quota period.
	ResetPeriod(id string, periodStartNs, nowNs int64) error
	// ExpireDue marks active subscriptions with expires_at <= nowNs expired
	// and returns their user IDs.
	ExpireDue(nowNs int64) ([]string, error)

	// Access keys.
	CreateKey(k AccessKey) error
	GetKey(userID, keyID string) (*AccessKey, error)
	ListKeys(userID string) ([]AccessKey, error)
	CountKeys(userID string) (int, error)
	UpdateKey(userID, keyID string, p KeyPatch, nowNs int64) error
	DeleteKey(userID, keyID string) error
	// TouchKeysUsed batch-updates last_used_at_ns.
	TouchKeysUsed(lastUsed map[string]int64) error
	// LoadAuthSnapshot returns every key with its owner state and active
	// subscription, for KeyCache.Load.
	LoadAuthSnapshot() ([]AuthRecord, error)
	// LoadAuthRecord returns one key's record (KeyCache refresh).
	LoadAuthRecord(keyID string) (*AuthRecord, error)

	// Usage. FlushUsage upserts deltas into usage_hourly and adds
	// periodUsed[subscriptionID] to user_subscriptions.period_used_bytes in a
	// single transaction. Zero deltas are skipped (no empty rows).
	FlushUsage(deltas []UsageDelta, periodUsed map[string]int64) error
	// RollupUsage folds usage_hourly rows older than cutoffTs into usage_daily
	// and deletes them, in one transaction.
	RollupUsage(cutoffTs int64) (int64, error)
	PruneUsageDaily(olderThanTs int64) (int64, error)
	QueryUsage(f UsageFilter) ([]UsageRow, error)

	// Invites. RedeemInvite atomically increments used_count when the invite
	// is valid at nowNs, else returns ErrInviteUnavailable.
	CreateInvite(inv Invite) error
	RedeemInvite(codeHash string, nowNs int64) (*Invite, error)
	ListInvites() ([]Invite, error)
	DeleteInvite(codeHash string) error

	// Orders.
	CreateOrder(o Order) error
	ListOrders(userID string) ([]Order, error)

	// Sessions.
	CreateSession(s Session) error
	// GetSession returns ErrNotFound when missing or expired at nowNs.
	GetSession(idHash string, nowNs int64) (*Session, error)
	ExtendSession(idHash string, expiresAtNs int64) error
	DeleteSession(idHash string) error
	DeleteUserSessions(userID string) (int64, error)
	ListUserSessions(userID string, nowNs int64) ([]Session, error)
	PruneSessions(nowNs int64) (int64, error)

	// Audit (shared audit_log table).
	AppendAudit(e model.AuditEntry) error
	ListAuditByActor(actorUserID string, beforeID int64, limit int) ([]model.AuditEntry, error)
}
