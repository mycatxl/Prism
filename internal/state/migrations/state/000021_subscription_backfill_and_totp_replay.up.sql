-- Two closings that belong to the same release: an account that was never on a
-- plan on purpose keeps its keys, and a second factor stops being reusable.

-- 1. Single-use second factors.
--
--    A TOTP code verifies for a whole step (and one step either side, for clock
--    drift), so until now the same six digits could be replayed as often as the
--    login form was submitted - by anyone who read them over a shoulder, off a
--    screen share, or out of a log. The column records the newest step whose code
--    this account has already spent; the login path advances it with a compare
--    and set, so a code that verifies is accepted exactly once.
--
--    It is the step's start, not the wall clock at the moment of the login: a
--    code that arrived one step early under clock drift must not spend the step
--    that is about to begin.
ALTER TABLE users ADD COLUMN totp_last_used_ns INTEGER NOT NULL DEFAULT 0;

-- 2. A subscription row for every account that has keys and no subscription.
--
--    "No subscription row" is how an account that predates plans is represented,
--    and the loader and the publish path both read it as the zero snapshot: no
--    platform restriction, no quota, no expiry - unlimited. That was harmless
--    while deleting a subscription left the account with nothing, because nothing
--    could reach the state; now that a deletion has to end an account's access,
--    the same state has to be readable as what it is.
--
--    So the accounts that were already in it are given the row that says it out
--    loud: active, no expiry, no traffic quota. A user who has never had an
--    access key is deliberately left alone - they have no access to keep, and
--    giving them a row would hand the zero snapshot's meaning to someone the
--    operator has not put on a plan yet.
--
--    plan_id is NULL, not a synthetic plans row. The column is a foreign key to
--    plans(id) and foreign keys are enforced on this connection, so naming a plan
--    that does not exist would make this migration fail outright on a database
--    that has any such user; and creating one would put a plan the operator never
--    made on sale in the console and collide with the plans.name uniqueness. NULL
--    is exactly the documented state of a subscription whose plan is gone, and
--    the name the console lists comes from the snapshot (plan_name), so the row
--    still reads as "legacy-unlimited" rather than as an empty cell.
INSERT INTO user_subscriptions (id, user_id, plan_id, plan_snapshot_json, starts_at_ns,
                                expires_at_ns, period_start_ns, period_used_bytes, status)
SELECT 'us_legacy_unlimited_' || u.id,
       u.id,
       NULL,
       '{"id":"legacy-unlimited","name":"legacy-unlimited","platforms":[],"ip_tiers":[],'
       || '"traffic_bytes":0,"period":"","max_concurrent":0,"max_rps":0,"max_bandwidth_bps":0,'
       || '"max_keys":0,"duration_days":0,"status":"active","created_at_ns":0,"updated_at_ns":0}',
       CAST(strftime('%s', 'now') AS INTEGER) * 1000000000,
       0,
       CAST(strftime('%s', 'now') AS INTEGER) * 1000000000,
       0,
       'active'
FROM users u
WHERE EXISTS (SELECT 1 FROM access_keys k WHERE k.user_id = u.id)
  AND NOT EXISTS (SELECT 1 FROM user_subscriptions s WHERE s.user_id = u.id);
