ALTER TABLE deposit_intents ADD COLUMN currency TEXT NOT NULL DEFAULT 'usdt';
ALTER TABLE deposit_intents ADD COLUMN address TEXT NOT NULL DEFAULT '';
ALTER TABLE deposit_intents ADD COLUMN contract TEXT NOT NULL DEFAULT '';
ALTER TABLE deposit_intents ADD COLUMN rate REAL NOT NULL DEFAULT 1;
ALTER TABLE deposit_intents ADD COLUMN credit_micro INTEGER NOT NULL DEFAULT 0;
UPDATE deposit_intents SET credit_micro = amount_micro;
DROP INDEX ux_deposit_intents_amount;
CREATE UNIQUE INDEX ux_deposit_intents_amount ON deposit_intents(currency, address, contract, amount_micro) WHERE status IN ('pending','unpaid');
ALTER TABLE ldc_deposits ADD COLUMN payment_micro INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ldc_deposits ADD COLUMN rate REAL NOT NULL DEFAULT 1;
ALTER TABLE ldc_deposits ADD COLUMN merchant_pid TEXT NOT NULL DEFAULT '';
ALTER TABLE ldc_deposits ADD COLUMN verification_key TEXT NOT NULL DEFAULT '';
UPDATE ldc_deposits SET payment_micro = amount_micro;

ALTER TABLE deposit_intents ADD COLUMN reconciled_at_ns INTEGER NOT NULL DEFAULT 0;
CREATE TABLE chain_scan_state (name TEXT PRIMARY KEY, since_ms INTEGER NOT NULL, until_ms INTEGER NOT NULL, fingerprint TEXT NOT NULL);
