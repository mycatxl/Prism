-- Balance.
--
-- Money is an int64 count of micro-units: one micro is 1e-6 USDT, which is the
-- token's own precision, so nothing is lost and nothing is a float. The display
-- layer divides by 1e6; no arithmetic happens anywhere else.
--
-- user_balance is a cache and balance_ledger is the truth. Every change writes
-- one ledger row and updates the balance in the same transaction, and the row
-- records the balance as it stood afterwards - that is what makes a balance that
-- disagrees with its own history visible instead of silent.
--
-- The two unique indexes are the two kinds of idempotency this needs:
--   (user_id, request_id)  a client retrying a purchase is settled once
--   (reason, ref_id)       one on-chain transaction is credited once
-- request_id is empty for changes nobody can retry, and ref_id is empty for
-- changes with no external handle, so the empty string is exempt from both:
-- SQLite treats NULLs as distinct but would collapse many empty strings into one
-- conflict, which is why the partial index filters them out.
--
-- user_balance cascades with the account: a deleted user has no balance to keep.
-- balance_ledger does not. An account can be deleted; its money movements must
-- still be answerable afterwards.
CREATE TABLE IF NOT EXISTS user_balance (
	user_id        TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	balance_micro  INTEGER NOT NULL DEFAULT 0,
	updated_at_ns  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS balance_ledger (
	id                  TEXT PRIMARY KEY,
	user_id             TEXT NOT NULL,
	delta_micro         INTEGER NOT NULL,
	balance_after_micro INTEGER NOT NULL,
	reason              TEXT NOT NULL,
	ref_id              TEXT NOT NULL DEFAULT '',
	request_id          TEXT NOT NULL DEFAULT '',
	note                TEXT NOT NULL DEFAULT '',
	actor               TEXT NOT NULL DEFAULT '',
	created_at_ns       INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_balance_ledger_user
	ON balance_ledger(user_id, created_at_ns);
CREATE UNIQUE INDEX IF NOT EXISTS ux_balance_ledger_request
	ON balance_ledger(user_id, request_id) WHERE request_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS ux_balance_ledger_ref
	ON balance_ledger(reason, ref_id) WHERE ref_id <> '';
