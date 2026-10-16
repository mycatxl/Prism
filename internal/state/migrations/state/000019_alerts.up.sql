-- Usage alerts.
--
-- One row is one report that a single account ran ahead of its plan: it moved
-- more than a configured share of its quota inside a short window. The usage
-- scanner writes them and an operator reads and acknowledges them.
--
-- The measured numbers are copied onto the row instead of being recomputed from
-- the plan when the alert is read: the plan can be edited or deleted afterwards
-- and the alert has to keep saying what it saw. There is no foreign key for that
-- reason, and user_id stays readable after the account is gone.
--
-- acknowledged_at_ns of 0 means "still open", which is what the console's unread
-- list filters on. Acknowledging is a conditional UPDATE on that value, so two
-- operators clicking at the same moment cannot both claim to have read it.
--
-- kind is a text discriminator rather than a boolean so a second detector does
-- not need a schema change.
CREATE TABLE IF NOT EXISTS alerts (
	id                 TEXT PRIMARY KEY,
	kind               TEXT NOT NULL,
	user_id            TEXT NOT NULL DEFAULT '',
	plan_id            TEXT NOT NULL DEFAULT '',
	subscription_id    TEXT NOT NULL DEFAULT '',
	window_minutes     INTEGER NOT NULL DEFAULT 0,
	threshold_ratio    REAL NOT NULL DEFAULT 0,
	used_bytes         INTEGER NOT NULL DEFAULT 0,
	quota_bytes        INTEGER NOT NULL DEFAULT 0,
	detected_at_ns     INTEGER NOT NULL,
	acknowledged_at_ns INTEGER NOT NULL DEFAULT 0,
	note               TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_alerts_detected_at ON alerts(detected_at_ns);
CREATE INDEX IF NOT EXISTS idx_alerts_user ON alerts(user_id);
