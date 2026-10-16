-- Reverse of 000021_subscription_backfill_and_totp_replay.
--
-- The rows are matched by the exact prefix this migration writes rather than by
-- their contents: a row an operator has since edited - extended, suspended, put
-- on a real plan - still came from here and still has to go when the migration
-- is rolled back, and no subscription an operator created can carry that id.
--
-- substr rather than LIKE: '_' is a single-character wildcard in LIKE, so every
-- id of the same length with the same shape would match too.
DELETE FROM user_subscriptions WHERE substr(id, 1, 20) = 'us_legacy_unlimited_';

ALTER TABLE users DROP COLUMN totp_last_used_ns;
