// Package tenant holds the multi-tenant domain model (users, plans, user
// subscriptions, access keys, usage), the storage interface implemented by
// internal/state, the in-memory credential cache used by the data plane, and
// the key pepper.
package tenant

import (
	"errors"

	"prism/internal/model"
)

// Re-exported so callers need not import model for these.
const (
	BuiltinAdminUserID = model.BuiltinAdminUserID
	LegacyKeyID        = model.LegacyKeyID
)

var (
	ErrNotFound = errors.New("tenant: not found")
	// ErrConflict reports a unique-constraint violation (username, plan name,
	// a second active subscription, ...).
	ErrConflict = errors.New("tenant: conflict")
	// ErrInviteUnavailable means the invite is unknown, expired or used up.
	ErrInviteUnavailable = errors.New("tenant: invite unavailable")
)

type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// Status values shared by users, plans and access keys.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled" // users, access keys
	StatusArchived = "archived" // plans
)

// Subscription status values.
const (
	SubActive    = "active"
	SubExpired   = "expired"
	SubSuspended = "suspended"
)

// Plan period values.
const (
	Period30d   = "30d"
	PeriodMonth = "month"
	PeriodWeek  = "week"
	PeriodOnce  = "once"
)

type User struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	PasswordHash  string `json:"-"`
	Role          Role   `json:"role"`
	Status        string `json:"status"`
	TOTPSecretEnc []byte `json:"-"`
	CreatedAtNs   int64  `json:"created_at_ns"`
	UpdatedAtNs   int64  `json:"updated_at_ns"`
	LastLoginAtNs int64  `json:"last_login_at_ns"`
}

// PlanLimits is the part of a plan copied into a subscription snapshot.
type PlanLimits struct {
	Platforms       []string `json:"platforms"`
	IPTiers         []string `json:"ip_tiers"`      // "residential:A", "datacenter:*", "unknown:*"
	TrafficBytes    int64    `json:"traffic_bytes"` // per period, ingress+egress; 0 = unlimited
	Period          string   `json:"period"`
	MaxConcurrent   int      `json:"max_concurrent"`    // 0 = unlimited
	MaxRPS          int      `json:"max_rps"`           // 0 = unlimited
	MaxBandwidthBps int64    `json:"max_bandwidth_bps"` // 0 = unlimited
	MaxKeys         int      `json:"max_keys"`          // 0 = unlimited
	DurationDays    int      `json:"duration_days"`
}

type Plan struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	PlanLimits
	Status      string `json:"status"`
	CreatedAtNs int64  `json:"created_at_ns"`
	UpdatedAtNs int64  `json:"updated_at_ns"`
}

// Subscription is one user × plan activation (table user_subscriptions).
type Subscription struct {
	ID              string     `json:"id"`
	UserID          string     `json:"user_id"`
	PlanID          string     `json:"plan_id"`
	Snapshot        PlanLimits `json:"plan_snapshot"`
	StartsAtNs      int64      `json:"starts_at_ns"`
	ExpiresAtNs     int64      `json:"expires_at_ns"`
	PeriodStartNs   int64      `json:"period_start_ns"`
	PeriodUsedBytes int64      `json:"period_used_bytes"`
	Status          string     `json:"status"`
	CreatedAtNs     int64      `json:"created_at_ns"`
	UpdatedAtNs     int64      `json:"updated_at_ns"`
}

type AccessKey struct {
	ID          string   `json:"id"` // public, "pk_..."
	UserID      string   `json:"user_id"`
	Name        string   `json:"name"`
	SecretHash  []byte   `json:"-"`
	Platforms   []string `json:"platforms,omitempty"`    // nil = no extra restriction
	IPAllowlist []string `json:"ip_allowlist,omitempty"` // nil = any source
	Status      string   `json:"status"`
	ExpiresAtNs int64    `json:"expires_at_ns"` // 0 = never
	LastUsedNs  int64    `json:"last_used_at_ns"`
	CreatedAtNs int64    `json:"created_at_ns"`
	UpdatedAtNs int64    `json:"updated_at_ns"`
}

// KeyPatch updates mutable access-key fields; nil leaves a field unchanged.
type KeyPatch struct {
	Name        *string
	Platforms   *[]string // pointer to nil slice clears the restriction
	IPAllowlist *[]string
	Status      *string
	ExpiresAtNs *int64
}

// UserPatch updates mutable user fields; nil leaves a field unchanged.
type UserPatch struct {
	Username     *string
	PasswordHash *string
	Role         *Role
	Status       *string
	// TOTPSecretEnc: pointer to nil clears TOTP.
	TOTPSecretEnc *[]byte
}

// UsageDelta is one (key, platform, hour) increment from the data plane.
type UsageDelta struct {
	KeyID        string
	UserID       string
	PlatformID   string
	HourTs       int64 // unix seconds, truncated to the hour
	IngressBytes int64
	EgressBytes  int64
	Requests     int64
	Errors       int64
}

// UsageRow is one aggregated bucket returned by QueryUsage.
type UsageRow struct {
	KeyID        string `json:"key_id,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	PlatformID   string `json:"platform_id,omitempty"`
	BucketTs     int64  `json:"bucket_ts"` // unix seconds
	IngressBytes int64  `json:"ingress_bytes"`
	EgressBytes  int64  `json:"egress_bytes"`
	Requests     int64  `json:"requests"`
	Errors       int64  `json:"errors"`
}

// UsageFilter selects usage rows. Hourly rows are used inside the hourly
// retention window and daily rows outside it; the repo merges both.
type UsageFilter struct {
	UserID     string // required for user-role callers
	KeyID      string
	PlatformID string
	FromTs     int64 // inclusive, unix seconds
	ToTs       int64 // exclusive, unix seconds; 0 = now
	Daily      bool  // bucket by day instead of hour
	GroupBy    UsageGroup
}

type UsageGroup int

const (
	GroupNone UsageGroup = iota
	GroupKey
	GroupUser
	GroupPlatform
)

type Invite struct {
	CodeHash    string `json:"-"`
	PlanID      string `json:"plan_id,omitempty"`
	MaxUses     int    `json:"max_uses"`
	UsedCount   int    `json:"used_count"`
	ExpiresAtNs int64  `json:"expires_at_ns"`
	CreatedBy   string `json:"created_by"`
	CreatedAtNs int64  `json:"created_at_ns"`
}

type Order struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	PlanID      string `json:"plan_id,omitempty"`
	Amount      string `json:"amount"`
	Note        string `json:"note"`
	CreatedBy   string `json:"created_by"`
	CreatedAtNs int64  `json:"created_at_ns"`
}

// Session is a panel login. IDHash is the hash of the cookie value; the raw
// session ID is never stored.
type Session struct {
	IDHash      string `json:"-"`
	UserID      string `json:"user_id"`
	CreatedAtNs int64  `json:"created_at_ns"`
	ExpiresAtNs int64  `json:"expires_at_ns"`
	IP          string `json:"ip"`
	UA          string `json:"ua"`
}

// AuthRecord is one row of the credential snapshot loaded into KeyCache:
// a key joined with its owner and the owner's active subscription (if any).
type AuthRecord struct {
	Key          AccessKey
	UserStatus   string
	UserRole     Role
	Subscription *Subscription
}
