DROP INDEX IF EXISTS ux_balance_ledger_deposit;
CREATE UNIQUE INDEX IF NOT EXISTS ux_balance_ledger_ref
	ON balance_ledger(reason, ref_id) WHERE ref_id <> '';
