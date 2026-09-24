CREATE TABLE IF NOT EXISTS intel_provider_settings (
    provider_id   TEXT PRIMARY KEY,
    enabled       INTEGER NOT NULL,
    api_key       TEXT NOT NULL DEFAULT '',
    daily_limit   INTEGER NOT NULL DEFAULT 0,
    qps           REAL NOT NULL DEFAULT 0,
    ttl_ns        INTEGER NOT NULL DEFAULT 0,
    config_json   TEXT NOT NULL DEFAULT '{}',
    updated_at_ns INTEGER NOT NULL
);
