-- Rebuild the placeholder exactly as 000017 created it, so a rollback leaves the
-- schema the previous version expects.
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
