-- On-chain deposits.
--
-- One fixed receiving address, one unique amount per intent. A user asks to
-- deposit N USDT; the system hands back an amount that nobody else is holding
-- right now - N plus one micro-unit, then two, and so on - and opens a window in
-- which a transfer of exactly that amount is credited to that user.
--
-- Why a unique amount instead of a memo or a per-user address: a bare TRC20
-- transfer carries no field we can put an identifier in, and deriving addresses
-- would mean holding keys on the server. The amount is the only channel left,
-- and the partial unique index below is what makes it an allocator: insert the
-- candidate, and if the database says the amount is taken, take the next one.
--
-- Two windows, both 15 minutes:
--   expires_at_ns  the payment window shown as a countdown
--   hold_until_ns  expires_at_ns + 15m, the end of the amount's occupancy
-- The second one exists because a transfer made at the last second of the
-- payment window is only seen on chain a minute or two later. Crediting is
-- decided by hold_until_ns, never by expires_at_ns.
--
-- status:
--   pending   inside the payment window, amount held
--   unpaid    window over, amount still held for the silent tail
--   paid      credited, terminal, amount released
--   released  occupancy over, amount released
--
-- Only pending and unpaid hold the amount. A paid intent must not keep holding
-- it: the amount is a claim on "waiting to be paid", not a receipt, and keeping
-- it would shrink the pool with every deposit.
--
-- chain_cursor is the listener's watermark. TronGrid's TRC20 history endpoint
-- filters by min_timestamp and has no block-height parameter, so the watermark
-- is a millisecond timestamp: the largest block_timestamp already handled.
CREATE TABLE IF NOT EXISTS deposit_intents (
	id            TEXT PRIMARY KEY,
	user_id       TEXT NOT NULL,
	amount_micro  INTEGER NOT NULL,
	status        TEXT NOT NULL,
	tx_id         TEXT NOT NULL DEFAULT '',
	created_at_ns INTEGER NOT NULL,
	expires_at_ns INTEGER NOT NULL,
	hold_until_ns INTEGER NOT NULL,
	paid_at_ns    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_deposit_intents_user
	ON deposit_intents(user_id, created_at_ns);
CREATE INDEX IF NOT EXISTS idx_deposit_intents_hold
	ON deposit_intents(status, hold_until_ns);
CREATE UNIQUE INDEX IF NOT EXISTS ux_deposit_intents_amount
	ON deposit_intents(amount_micro) WHERE status IN ('pending', 'unpaid');

CREATE TABLE IF NOT EXISTS chain_cursor (
	name         TEXT PRIMARY KEY,
	watermark_ms INTEGER NOT NULL DEFAULT 0,
	updated_at_ns INTEGER NOT NULL
);
