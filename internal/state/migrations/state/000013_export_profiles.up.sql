CREATE TABLE IF NOT EXISTS export_profiles (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    format            TEXT NOT NULL,
    token_sha256      TEXT NOT NULL UNIQUE,
    platform_id       TEXT NOT NULL DEFAULT '',
    filter_json       TEXT NOT NULL DEFAULT '{}',
    name_template     TEXT NOT NULL DEFAULT '',
    enabled           INTEGER NOT NULL DEFAULT 1,
    last_access_at_ns INTEGER NOT NULL DEFAULT 0,
    access_count      INTEGER NOT NULL DEFAULT 0,
    created_at_ns     INTEGER NOT NULL,
    updated_at_ns     INTEGER NOT NULL
);
