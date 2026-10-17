-- The orders table was a placeholder for manual top-ups: it had no code behind
-- it, no writes, and no reads. Balances and the ledger replace it, and a table
-- nothing touches is a table that drifts from the schema it claims to describe.
DROP TABLE IF EXISTS orders;
