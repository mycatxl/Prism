-- Invitations.
--
-- An invite is how a self-service registration is bounded: the code decides
-- whether an account may be created at all (PRISM_REGISTRATION=invite), which
-- plan it starts on, and how many times it may be redeemed. used_count is
-- incremented by a conditional UPDATE so two concurrent registrations cannot
-- both take the last use.
--
-- plan_id is cleared rather than cascaded when a plan is deleted: an invite
-- whose plan is gone still admits an account, it just grants no subscription.
CREATE TABLE IF NOT EXISTS invites (
	id            TEXT PRIMARY KEY,
	code          TEXT NOT NULL UNIQUE,
	plan_id       TEXT NOT NULL DEFAULT '',
	max_uses      INTEGER NOT NULL DEFAULT 1,
	used_count    INTEGER NOT NULL DEFAULT 0,
	expires_at_ns INTEGER NOT NULL DEFAULT 0,
	note          TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL DEFAULT 'active',
	created_by    TEXT NOT NULL DEFAULT '',
	created_at_ns INTEGER NOT NULL,
	updated_at_ns INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_invites_code ON invites(code);
