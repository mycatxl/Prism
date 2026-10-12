-- Reverses 000017_tenant.up.sql.
--
-- Dropping the audit_log.user_id column is safe on the SQLite version this tree
-- ships (3.51.2 through modernc.org/sqlite, verified live), which is well past
-- the 3.35 floor for ALTER TABLE ... DROP COLUMN.
DROP INDEX IF EXISTS idx_audit_log_user;
ALTER TABLE audit_log DROP COLUMN user_id;

DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS usage_daily;
DROP TABLE IF EXISTS usage_hourly;
DROP TABLE IF EXISTS access_keys;
DROP TABLE IF EXISTS user_subscriptions;
DROP TABLE IF EXISTS plans;
DROP TABLE IF EXISTS users;
