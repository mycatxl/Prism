CREATE TABLE privacy_deletions (
 node_hash TEXT NOT NULL, reason TEXT NOT NULL, deleted_at_ns INTEGER NOT NULL,
 subscription_id TEXT NOT NULL DEFAULT '', PRIMARY KEY(node_hash, subscription_id)
);
