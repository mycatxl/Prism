-- WP09 §3.2 step 4 follow-up: per-node budgets for the via-node data sources.
--
-- A via-node lookup leaves through the node under test, so the vendor sees the
-- node's own address and its anonymous quota belongs to that address, not to
-- the Prism host. Keeping the counters in the provider-wide provider_state row
-- collapsed the whole inventory onto one gate (ippure was effectively "500
-- lookups per day for all 287 nodes" instead of "500 per node").
--
-- provider_state keeps provider-wide meaning: paused/blocked_until_ns and
-- next_request_at_ns stay the global QPS safety valve that protects the vendor
-- from the entire inventory arriving at once. The per-node daily counters live
-- here.
CREATE TABLE provider_node_state (
    provider           TEXT NOT NULL,
    node_hash          TEXT NOT NULL,
    day                TEXT NOT NULL,
    used               INTEGER NOT NULL DEFAULT 0,
    next_request_at_ns INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (provider, node_hash)
);
CREATE INDEX idx_provider_node_state_day ON provider_node_state(day);
