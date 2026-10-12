-- Multi-tenant data layer: users, plans, user subscriptions, access keys and
-- usage. New tables only -- platforms, subscriptions (the node feed table),
-- endpoints, account_header_rules and intel_provider_settings are untouched.
-- audit_log is extended in place: it has existed since 000014.
--
-- "user_subscriptions" is named that way on purpose: "subscriptions" is
-- already the node feed table created by 000001_state_base.up.sql.
--
-- Timestamps are integer nanoseconds with an _ns suffix, matching the rest of
-- the schema. Byte columns are INTEGER, also matching the existing tables.
-- IDs are UUIDv7 rendered as text.

CREATE TABLE IF NOT EXISTS users (
    id              TEXT PRIMARY KEY,
    username        TEXT NOT NULL UNIQUE,
    password_hash   TEXT NOT NULL,
    role            TEXT NOT NULL CHECK (role IN ('admin', 'user')),
    status          TEXT NOT NULL CHECK (status IN ('active', 'disabled')),
    totp_secret_enc BLOB,
    created_at_ns   INTEGER NOT NULL,
    updated_at_ns   INTEGER NOT NULL,
    last_login_ns   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS plans (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    platforms_json    TEXT NOT NULL DEFAULT '[]',
    ip_tiers_json     TEXT NOT NULL DEFAULT '[]',
    traffic_bytes     INTEGER NOT NULL DEFAULT 0,
    period            TEXT NOT NULL DEFAULT '30d',
    max_concurrent    INTEGER NOT NULL DEFAULT 0,
    max_rps           INTEGER NOT NULL DEFAULT 0,
    max_bandwidth_bps INTEGER,
    max_keys          INTEGER NOT NULL DEFAULT 0,
    duration_days     INTEGER NOT NULL DEFAULT 30,
    status            TEXT NOT NULL DEFAULT 'active',
    created_at_ns     INTEGER NOT NULL,
    updated_at_ns     INTEGER NOT NULL
);

-- plan_id is nullable and cleared when the plan is deleted, while
-- plan_snapshot_json keeps the terms the subscription was opened under, so the
-- quota of an existing subscription stays computable after a plan is removed.
CREATE TABLE IF NOT EXISTS user_subscriptions (
    id                 TEXT PRIMARY KEY,
    user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan_id            TEXT REFERENCES plans(id) ON DELETE SET NULL,
    plan_snapshot_json TEXT NOT NULL DEFAULT '{}',
    starts_at_ns       INTEGER NOT NULL,
    expires_at_ns      INTEGER NOT NULL,
    period_start_ns    INTEGER NOT NULL,
    period_used_bytes  INTEGER NOT NULL DEFAULT 0,
    status             TEXT NOT NULL CHECK (status IN ('active', 'expired', 'suspended'))
);

-- scope=admin keys are the automation credential for the management API; they
-- are only accepted for a user whose role is admin.
CREATE TABLE IF NOT EXISTS access_keys (
    id                TEXT PRIMARY KEY,
    user_id           TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name              TEXT NOT NULL DEFAULT '',
    secret_hash       BLOB NOT NULL,
    platforms_json    TEXT,
    ip_allowlist_json TEXT,
    scope             TEXT NOT NULL DEFAULT 'proxy' CHECK (scope IN ('proxy', 'admin')),
    status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    expires_at_ns     INTEGER NOT NULL DEFAULT 0,
    last_used_ns      INTEGER NOT NULL DEFAULT 0,
    created_at_ns     INTEGER NOT NULL
);

-- user_id is denormalized here and deliberately has no foreign key: usage must
-- survive the deletion of a key or a user so the operator can still audit it.
CREATE TABLE IF NOT EXISTS usage_hourly (
    key_id        TEXT NOT NULL,
    user_id       TEXT NOT NULL,
    platform_id   TEXT NOT NULL,
    hour_ts_ns    INTEGER NOT NULL,
    ingress_bytes INTEGER NOT NULL DEFAULT 0,
    egress_bytes  INTEGER NOT NULL DEFAULT 0,
    requests      INTEGER NOT NULL DEFAULT 0,
    errors        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (key_id, platform_id, hour_ts_ns)
);

-- Hourly rows older than the retention window are summarized into this table
-- and then deleted from usage_hourly.
CREATE TABLE IF NOT EXISTS usage_daily (
    key_id        TEXT NOT NULL,
    user_id       TEXT NOT NULL,
    platform_id   TEXT NOT NULL,
    day_ts_ns     INTEGER NOT NULL,
    ingress_bytes INTEGER NOT NULL DEFAULT 0,
    egress_bytes  INTEGER NOT NULL DEFAULT 0,
    requests      INTEGER NOT NULL DEFAULT 0,
    errors        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (key_id, platform_id, day_ts_ns)
);

-- Manual top-up and renewal records. Payment is out of scope; amount_minor and
-- currency exist so the column semantics are fixed before anything writes them.
CREATE TABLE IF NOT EXISTS orders (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan_id       TEXT REFERENCES plans(id) ON DELETE SET NULL,
    amount_minor  INTEGER NOT NULL DEFAULT 0,
    currency      TEXT NOT NULL DEFAULT '',
    note          TEXT NOT NULL DEFAULT '',
    created_by    TEXT NOT NULL DEFAULT '',
    created_at_ns INTEGER NOT NULL
);

-- Console sessions. Only the hash of the session ID is stored, never the ID.
CREATE TABLE IF NOT EXISTS sessions (
    id_hash       TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at_ns INTEGER NOT NULL,
    expires_at_ns INTEGER NOT NULL,
    ip            TEXT NOT NULL DEFAULT '',
    ua            TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_user_subscriptions_user ON user_subscriptions(user_id, status);
CREATE INDEX IF NOT EXISTS idx_access_keys_user        ON access_keys(user_id, status);
CREATE INDEX IF NOT EXISTS idx_usage_hourly_hour       ON usage_hourly(hour_ts_ns);
CREATE INDEX IF NOT EXISTS idx_usage_hourly_user_hour  ON usage_hourly(user_id, hour_ts_ns);
CREATE INDEX IF NOT EXISTS idx_usage_daily_day         ON usage_daily(day_ts_ns);
CREATE INDEX IF NOT EXISTS idx_sessions_user           ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_orders_user             ON orders(user_id);

-- audit_log already exists; this only adds the user dimension. actor stays and
-- keeps holding the actor identity string, so rows written before this
-- migration remain readable.
ALTER TABLE audit_log ADD COLUMN user_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_audit_log_user ON audit_log(user_id);
