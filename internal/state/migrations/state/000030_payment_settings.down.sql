-- A downgrade cannot safely represent pending orders in multiple currencies.
-- Refuse it when those rows exist rather than reinterpret TRX as USDT.
CREATE TEMP TABLE payment_downgrade_guard (ok INTEGER CHECK(ok = 1));
INSERT INTO payment_downgrade_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM deposit_intents WHERE currency <> 'usdt' OR status <> 'paid') OR EXISTS(SELECT 1 FROM ldc_deposits WHERE status <> 'paid') THEN 0 ELSE 1 END;
DROP TABLE payment_downgrade_guard;
DROP TABLE chain_scan_state;
ALTER TABLE deposit_intents DROP COLUMN reconciled_at_ns;
DROP INDEX ux_deposit_intents_amount;
ALTER TABLE deposit_intents DROP COLUMN currency;
ALTER TABLE deposit_intents DROP COLUMN address;
ALTER TABLE deposit_intents DROP COLUMN contract;
ALTER TABLE deposit_intents DROP COLUMN rate;
ALTER TABLE deposit_intents DROP COLUMN credit_micro;
CREATE UNIQUE INDEX ux_deposit_intents_amount ON deposit_intents(amount_micro) WHERE status IN ('pending','unpaid');
ALTER TABLE ldc_deposits DROP COLUMN payment_micro;
ALTER TABLE ldc_deposits DROP COLUMN rate;
ALTER TABLE ldc_deposits DROP COLUMN merchant_pid;
ALTER TABLE ldc_deposits DROP COLUMN verification_key;
