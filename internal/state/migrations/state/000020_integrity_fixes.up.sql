-- Integrity fixes for the tenant tables.
--
-- Each statement closes a hole that until now lived only in code or in a
-- comment. They are cheap on a healthy database (the backfill matches nothing)
-- and they make the invariants they describe enforceable rather than hoped for.

-- 1. Backfill before 2. "A user has at most one active subscription" was a rule
--    of the service layer only: idx_user_subscriptions_user (000017) is not
--    unique, so two concurrent AssignSubscription calls could each expire the
--    running subscription and insert their own, leaving the user with two
--    active rows.
--
--    The subscription that stays is the one an operator would have kept: the
--    latest expiry, then the latest start, then the smallest id, so the outcome
--    is deterministic. The others are marked 'expired' instead of deleted: they
--    are history, and the traffic recorded against them stays accountable.
UPDATE user_subscriptions
SET status = 'expired'
WHERE status = 'active'
  AND id NOT IN (
      SELECT id
      FROM (
          SELECT id,
                 ROW_NUMBER() OVER (
                     PARTITION BY user_id
                     ORDER BY expires_at_ns DESC, starts_at_ns DESC, id ASC
                 ) AS keep_rank
          FROM user_subscriptions
          WHERE status = 'active'
      )
      WHERE keep_rank = 1
  );

-- 2. Enforce it. The index is partial because only the active row has to be
--    unique: a user accumulates retired subscriptions over time and those may
--    repeat a user_id without limit. A violation now comes back as a unique
--    constraint error from the insert, which is what the repository maps to
--    ErrConflict.
CREATE UNIQUE INDEX IF NOT EXISTS ux_user_subscriptions_active
    ON user_subscriptions(user_id)
    WHERE status = 'active';

-- 3. Session cleanup deletes on expires_at_ns and only idx_sessions_user
--    (user_id) existed, so the delete was a full scan of every session row -
--    including the quantity of live sessions - on every pass.
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at_ns);

-- 4. invites.plan_id is cleared when its plan is deleted. 000018 states that in
--    a comment and the invite repository writes an empty string for "no plan",
--    but nothing enforced it, so deleting a plan left invites pointing at an id
--    that no longer exists. plan_id is TEXT NOT NULL DEFAULT '' (000018), so
--    the empty string is what "cleared" means here: there is no NULL to write.
--
--    A trigger rather than a foreign key: adding REFERENCES plans(id) ON DELETE
--    SET NULL to an existing table means rebuilding it, which SQLite only does
--    through a copy-and-rename over a table that is live while the migration
--    runs.
CREATE TRIGGER IF NOT EXISTS trg_invites_clear_plan
AFTER DELETE ON plans
BEGIN
    UPDATE invites SET plan_id = '' WHERE plan_id = OLD.id;
END;
