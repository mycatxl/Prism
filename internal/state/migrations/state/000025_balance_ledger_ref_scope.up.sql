-- The ledger's second unique index was too broad.
--
-- 000022 declared UNIQUE (reason, ref_id) WHERE ref_id <> '', intending "one
-- on-chain transaction is credited once". Applied to every reason it also
-- refuses the second occurrence of anything that repeats a handle: the same
-- plan bought twice, the same code redeemed by two users, the same subscription
-- renewed for a second period. All of those are ordinary and all of them would
-- have failed as a conflict the service would have reported as "already
-- applied".
--
-- The rule is about deposits only, so the index now says so. A deposit's ref_id
-- is a transaction id, which is unique on chain and therefore unique here; the
-- other reasons are answered by (user_id, request_id) when the caller can retry
-- and by the row's own history when it cannot.
DROP INDEX IF EXISTS ux_balance_ledger_ref;
CREATE UNIQUE INDEX IF NOT EXISTS ux_balance_ledger_deposit
	ON balance_ledger(reason, ref_id) WHERE reason = 'deposit' AND ref_id <> '';
