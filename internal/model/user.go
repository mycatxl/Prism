package model

// User roles. A "user" reaches the data plane through access keys and the
// console through its own account; an "admin" additionally manages users,
// plans and the rest of the control plane.
const (
	UserRoleAdmin = "admin"
	UserRoleUser  = "user"
)

// User statuses. A disabled user is refused on every path, including the keys
// that still exist under it.
const (
	UserStatusActive   = "active"
	UserStatusDisabled = "disabled"
)

// Access key scopes. A "proxy" key authenticates the data plane; an "admin" key
// is an automation credential for the management API and is only accepted for
// a user whose role is admin.
const (
	KeyScopeProxy = "proxy"
	KeyScopeAdmin = "admin"
)

// Access key statuses.
const (
	KeyStatusActive   = "active"
	KeyStatusDisabled = "disabled"
)

// UserSubscription statuses.
const (
	SubscriptionStatusActive    = "active"
	SubscriptionStatusExpired   = "expired"
	SubscriptionStatusSuspended = "suspended"
)

// User is a console account.
//
// PasswordHash holds an argon2id hash in PHC string form and is never
// serialized. TOTPSecretEnc is the AES-GCM ciphertext of an optional TOTP
// secret, keyed by a subkey derived from the key pepper; it is empty when the
// account has no second factor. TOTPLastUsedNs is the start of the step whose
// code that factor last spent, and is 0 for an account that has never used one:
// it is what makes a code single-use, and the login path advances it with a
// compare and set rather than writing back a row it read.
type User struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	PasswordHash  string `json:"-"`
	Role          string `json:"role"`
	Status        string `json:"status"`
	TOTPSecretEnc []byte `json:"-"`
	CreatedAtNs   int64  `json:"created_at_ns"`
	UpdatedAtNs   int64  `json:"updated_at_ns"`
	LastLoginNs   int64  `json:"last_login_ns"`
	// TOTPLastUsedNs is not part of a user write. AdvanceTOTPLastUsed is the
	// only writer, and it moves the value forward only.
	TOTPLastUsedNs int64 `json:"-"`
}

// Plan is an admin-defined template: which platforms and IP tiers a user may
// reach, and the quotas that apply.
//
// Platforms and IPTiers are stored as JSON arrays. TrafficBytes is the
// per-period byte budget counting both directions, and 0 means unlimited.
// MaxConcurrent, MaxRPS, MaxKeys and DurationDays of 0 mean unlimited except
// for DurationDays, whose 0 value is not used by the console.
type Plan struct {
	ID              string   `json:"id"`
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
	Status          string   `json:"status"`
	CreatedAtNs     int64    `json:"created_at_ns"`
	UpdatedAtNs     int64    `json:"updated_at_ns"`
}

// UserSubscription binds one user to one plan for a time window.
//
// PlanSnapshotJSON holds the plan as it was when the subscription was opened,
// so the quota stays computable after the plan itself is edited or deleted.
// PlanID is cleared when the plan is deleted and may therefore be empty on an
// otherwise valid subscription.
type UserSubscription struct {
	ID               string `json:"id"`
	UserID           string `json:"user_id"`
	PlanID           string `json:"plan_id"`
	PlanSnapshotJSON string `json:"plan_snapshot_json"`
	StartsAtNs       int64  `json:"starts_at_ns"`
	ExpiresAtNs      int64  `json:"expires_at_ns"`
	PeriodStartNs    int64  `json:"period_start_ns"`
	PeriodUsedBytes  int64  `json:"period_used_bytes"`
	Status           string `json:"status"`
}

// AccessKey is a proxy or admin credential.
//
// Only SecretHash — the HMAC of the secret under a subkey derived from the key
// pepper — is stored; the plaintext is shown once at creation. ID is the public
// half of the credential, rendered to operators as pk_<id>_<secret>.
// Platforms, when non-empty, narrows the plan's platform set; IPAllowlist, when
// non-empty, restricts the source addresses the key may be used from.
type AccessKey struct {
	ID          string   `json:"id"`
	UserID      string   `json:"user_id"`
	Name        string   `json:"name"`
	SecretHash  []byte   `json:"-"`
	Platforms   []string `json:"platforms"`
	IPAllowlist []string `json:"ip_allowlist"`
	Scope       string   `json:"scope"`
	Status      string   `json:"status"`
	ExpiresAtNs int64    `json:"expires_at_ns"`
	LastUsedNs  int64    `json:"last_used_ns"`
	CreatedAtNs int64    `json:"created_at_ns"`
}

// UsageRow is one aggregation row: either an hour of traffic for a key on a
// platform (usage_hourly) or a summarized day of it (usage_daily).
//
// UserID is denormalized on purpose: usage outlives the key and the user it
// belonged to, so the operator can still account for traffic after either is
// deleted. There is no foreign key on this table for that reason.
type UsageRow struct {
	KeyID        string `json:"key_id"`
	UserID       string `json:"user_id"`
	PlatformID   string `json:"platform_id"`
	TSNs         int64  `json:"ts_ns"`
	IngressBytes int64  `json:"ingress_bytes"`
	EgressBytes  int64  `json:"egress_bytes"`
	Requests     int64  `json:"requests"`
	Errors       int64  `json:"errors"`
}

// Order is a manual top-up or renewal record. Payment is out of scope; the
// amount is recorded in minor units with an explicit currency so the column
// semantics are fixed before anything writes them.
type Order struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	PlanID      string `json:"plan_id"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Note        string `json:"note"`
	CreatedBy   string `json:"created_by"`
	CreatedAtNs int64  `json:"created_at_ns"`
}

// Session is a console login session. Only the hash of the session ID is
// stored, never the ID itself, so a database read cannot be replayed as a
// login.
type Session struct {
	IDHash      string `json:"id_hash"`
	UserID      string `json:"user_id"`
	CreatedAtNs int64  `json:"created_at_ns"`
	ExpiresAtNs int64  `json:"expires_at_ns"`
	IP          string `json:"ip"`
	UA          string `json:"ua"`
}

// Invite statuses. A disabled invite is refused even if it has uses left, which
// is how an operator revokes a leaked code without deleting the record of it.
const (
	InviteStatusActive   = "active"
	InviteStatusDisabled = "disabled"
)

// Invite is a registration invitation.
//
// It bounds self-service sign-up in two ways at once: MaxUses says how many
// accounts may be created with the code (0 means unlimited), and PlanID says
// what they start on. UsedCount is incremented by a conditional UPDATE, so the
// last use cannot be taken twice. ExpiresAtNs of 0 never expires.
type Invite struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	PlanID      string `json:"plan_id"`
	MaxUses     int    `json:"max_uses"`
	UsedCount   int    `json:"used_count"`
	ExpiresAtNs int64  `json:"expires_at_ns"`
	Note        string `json:"note"`
	Status      string `json:"status"`
	CreatedBy   string `json:"created_by"`
	CreatedAtNs int64  `json:"created_at_ns"`
	UpdatedAtNs int64  `json:"updated_at_ns"`
}
