-- Reverse of 000020_integrity_fixes.
--
-- The backfill at the top of the up migration is the one thing that cannot be
-- reverted: which of a user's several active rows was demoted is not recorded
-- anywhere, and re-activating rows nobody verified is worse than leaving them
-- expired. Dropping the index is enough to give the old shape back.
DROP TRIGGER IF EXISTS trg_invites_clear_plan;
DROP INDEX IF EXISTS idx_sessions_expires;
DROP INDEX IF EXISTS ux_user_subscriptions_active;
