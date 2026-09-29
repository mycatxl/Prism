-- A vendor 429 is a fact about the address that sent the request. A via-node
-- lookup leaves through the node under test, so its rate limit belongs to that
-- node alone.
--
-- Before this migration the only cooldown column was
-- provider_state.blocked_until_ns, which is provider-wide: one 429 through
-- node-a parked every other node of the same data source, and ConsumeViaNodeBudget
-- read exactly that column. The cooldown now lives next to the node's budget.
--
-- provider_state keeps its provider-wide meaning for the keyed host-side
-- sources, where a 429 really is about the caller's own quota: one key, one
-- budget, one cooldown.
ALTER TABLE provider_node_state ADD COLUMN blocked_until_ns INTEGER NOT NULL DEFAULT 0;
ALTER TABLE provider_node_state ADD COLUMN error_code TEXT NOT NULL DEFAULT '';
