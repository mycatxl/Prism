-- Multi-tenant data model (plan v2 §3). Additive only.

CREATE TABLE IF NOT EXISTS users (
    id               TEXT PRIMARY KEY,
    username         TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash    TEXT NOT NULL DEFAULT '',
    role             TEXT NOT NULL CHECK (role IN ('admin', 'user')),
    status           TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    totp_secret_enc  BLOB,
    created_at_ns    INTEGER NOT NULL,
    updated_at_ns    INTEGER NOT NULL,
    last_login_at_ns INTEGER NOT NULL DEFAULT 0
);

-- Built-in admin. Legacy PRISM_PROXY_TOKEN traffic and pre-upgrade sticky
-- leases belong to it. Password is set by `prism init` or first login.
INSERT OR IGNORE INTO users (id, username, password_hash, role, status, created_at_ns, updated_at_ns)
VALUES ('u_admin', 'admin', '', 'admin', 'active', 0, 0);

CREATE TABLE IF NOT EXISTS plans (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    platforms_json    TEXT NOT NULL DEFAULT '[]',
    ip_tiers_json     TEXT NOT NULL DEFAULT '[]',
    traffic_bytes     INTEGER NOT NULL DEFAULT 0,
    period            TEXT NOT NULL DEFAULT '30d' CHECK (period IN ('30d', 'month', 'week', 'once')),
    max_concurrent    INTEGER NOT NULL DEFAULT 0,
    max_rps           INTEGER NOT NULL DEFAULT 0,
    max_bandwidth_bps INTEGER NOT NULL DEFAULT 0,
    max_keys          INTEGER NOT NULL DEFAULT 0,
    duration_days     INTEGER NOT NULL DEFAULT 0,
    status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_at_ns     INTEGER NOT NULL,
    updated_at_ns     INTEGER NOT NULL
);

-- "subscriptions" already names proxy subscription sources, hence the prefix.
CREATE TABLE IF NOT EXISTS user_subscriptions (
    id                 TEXT PRIMARY KEY,
    user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan_id            TEXT NOT NULL REFERENCES plans(id),
    plan_snapshot_json TEXT NOT NULL,
    starts_at_ns       INTEGER NOT NULL,
    expires_at_ns      INTEGER NOT NULL,
    period_start_ns    INTEGER NOT NULL,
    period_used_bytes  INTEGER NOT NULL DEFAULT 0,
    status             TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'expired', 'suspended')),
    created_at_ns      INTEGER NOT NULL,
    updated_at_ns      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_user_status ON user_subscriptions(user_id, status);
-- At most one active subscription per user.
CREATE UNIQUE INDEX IF NOT EXISTS uq_user_subscriptions_active
    ON user_subscriptions(user_id) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS access_keys (
    id                TEXT PRIMARY KEY,
    user_id           TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name              TEXT NOT NULL DEFAULT '',
    secret_hash       BLOB NOT NULL,
    platforms_json    TEXT,
    ip_allowlist_json TEXT,
    status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    expires_at_ns     INTEGER NOT NULL DEFAULT 0,
    last_used_at_ns   INTEGER NOT NULL DEFAULT 0,
    created_at_ns     INTEGER NOT NULL,
    updated_at_ns     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_access_keys_user ON access_keys(user_id);

-- Only hours with traffic get a row.
CREATE TABLE IF NOT EXISTS usage_hourly (
    key_id        TEXT NOT NULL,
    user_id       TEXT NOT NULL,
    platform_id   TEXT NOT NULL,
    hour_ts       INTEGER NOT NULL,
    ingress_bytes INTEGER NOT NULL DEFAULT 0,
    egress_bytes  INTEGER NOT NULL DEFAULT 0,
    requests      INTEGER NOT NULL DEFAULT 0,
    errors        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (key_id, platform_id, hour_ts)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_usage_hourly_hour ON usage_hourly(hour_ts);
CREATE INDEX IF NOT EXISTS idx_usage_hourly_user_hour ON usage_hourly(user_id, hour_ts);

CREATE TABLE IF NOT EXISTS usage_daily (
    key_id        TEXT NOT NULL,
    user_id       TEXT NOT NULL,
    platform_id   TEXT NOT NULL,
    day_ts        INTEGER NOT NULL,
    ingress_bytes INTEGER NOT NULL DEFAULT 0,
    egress_bytes  INTEGER NOT NULL DEFAULT 0,
    requests      INTEGER NOT NULL DEFAULT 0,
    errors        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (key_id, platform_id, day_ts)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_usage_daily_day ON usage_daily(day_ts);
CREATE INDEX IF NOT EXISTS idx_usage_daily_user_day ON usage_daily(user_id, day_ts);

CREATE TABLE IF NOT EXISTS invites (
    code_hash     TEXT PRIMARY KEY,
    plan_id       TEXT REFERENCES plans(id) ON DELETE SET NULL,
    max_uses      INTEGER NOT NULL DEFAULT 1,
    used_count    INTEGER NOT NULL DEFAULT 0,
    expires_at_ns INTEGER NOT NULL DEFAULT 0,
    created_by    TEXT NOT NULL DEFAULT '',
    created_at_ns INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS orders (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan_id       TEXT REFERENCES plans(id) ON DELETE SET NULL,
    amount        TEXT NOT NULL DEFAULT '',
    note          TEXT NOT NULL DEFAULT '',
    created_by    TEXT NOT NULL DEFAULT '',
    created_at_ns INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_orders_user ON orders(user_id, created_at_ns);

-- Panel sessions. Only a hash of the session ID is stored.
CREATE TABLE IF NOT EXISTS user_sessions (
    id_hash       TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at_ns INTEGER NOT NULL,
    expires_at_ns INTEGER NOT NULL,
    ip            TEXT NOT NULL DEFAULT '',
    ua            TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_user_sessions_user ON user_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_user_sessions_expires ON user_sessions(expires_at_ns);

-- audit_log (000014) gains structured actor/target columns.
ALTER TABLE audit_log ADD COLUMN actor_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN target_type TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN target_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_audit_log_actor ON audit_log(actor_user_id, at_ns);
