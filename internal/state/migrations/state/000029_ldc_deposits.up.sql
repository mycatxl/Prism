-- Linux.do Credit (EasyPay) payment intents.
--
-- The callback carries an out_trade_no but no trusted account identity. This
-- row is written before Prism returns the gateway redirect, giving the callback
-- a durable mapping to the account and the exact amount it may credit.
CREATE TABLE IF NOT EXISTS ldc_deposits (
    out_trade_no  TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL,
    amount_micro  INTEGER NOT NULL,
    status        TEXT NOT NULL,
    trade_no      TEXT NOT NULL DEFAULT '',
    created_at_ns INTEGER NOT NULL,
    paid_at_ns    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_ldc_deposits_user
    ON ldc_deposits(user_id, created_at_ns);
CREATE UNIQUE INDEX IF NOT EXISTS ux_ldc_deposits_trade_no
    ON ldc_deposits(trade_no) WHERE trade_no <> '';
