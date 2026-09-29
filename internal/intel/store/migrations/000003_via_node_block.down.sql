-- SQLite has no DROP COLUMN before 3.35, so the table is rebuilt.
CREATE TABLE provider_node_state_old (
    provider           TEXT NOT NULL,
    node_hash          TEXT NOT NULL,
    day                TEXT NOT NULL,
    used               INTEGER NOT NULL DEFAULT 0,
    next_request_at_ns INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (provider, node_hash)
);
INSERT INTO provider_node_state_old (provider, node_hash, day, used, next_request_at_ns)
    SELECT provider, node_hash, day, used, next_request_at_ns FROM provider_node_state;
DROP TABLE provider_node_state;
ALTER TABLE provider_node_state_old RENAME TO provider_node_state;
CREATE INDEX idx_provider_node_state_day ON provider_node_state(day);
