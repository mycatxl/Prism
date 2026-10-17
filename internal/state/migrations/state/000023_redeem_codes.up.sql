-- Redemption codes.
--
-- An invite answers "may this person have an account"; a redeem code answers
-- "how much money do they get". Different question, different transaction, so
-- they are different tables: registration claims a use and inserts an account,
-- redemption claims a use and adds balance, and neither should have to reason
-- about the other's conditions.
--
-- The plaintext is never stored. code_hash is an HMAC of the code under a
-- subkey of the key pepper, the same way access-key secrets are stored, so a
-- copy of this database cannot be redeemed. code_hint keeps the last four
-- symbols so an operator can tell two codes apart in a list without the codes
-- themselves being readable.
--
-- used_count is claimed by a conditional UPDATE, the same shape the invites
-- table uses: status, expiry and remaining uses are all in the WHERE clause, so
-- two redemptions arriving together cannot both take the last use.
CREATE TABLE IF NOT EXISTS redeem_codes (
	id            TEXT PRIMARY KEY,
	code_hash     BLOB NOT NULL UNIQUE,
	code_hint     TEXT NOT NULL DEFAULT '',
	amount_micro  INTEGER NOT NULL,
	max_uses      INTEGER NOT NULL DEFAULT 1,
	used_count    INTEGER NOT NULL DEFAULT 0,
	expires_at_ns INTEGER NOT NULL DEFAULT 0,
	note          TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL DEFAULT 'active',
	created_by    TEXT NOT NULL DEFAULT '',
	created_at_ns INTEGER NOT NULL,
	updated_at_ns INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_redeem_codes_status
	ON redeem_codes(status, expires_at_ns);
