-- The leases primary key gains user_id.
--
-- The account is what a client sends, so it is only unique within one user:
-- two tenants that both call their account "default" would otherwise share a
-- single sticky lease, and sharing an egress IP across tenants is exactly what
-- the tenant boundary has to prevent.
--
-- The table is rebuilt because SQLite cannot change a primary key in place.
-- Existing rows are carried over with an empty owner, which is their exact
-- owner rather than a guess: before this migration there was no user dimension,
-- so every stored lease belonged to the single-tenant path.
ALTER TABLE leases RENAME TO leases__account_only;

CREATE TABLE leases (
	platform_id      TEXT NOT NULL,
	user_id          TEXT NOT NULL DEFAULT '',
	account          TEXT NOT NULL,
	node_hash        TEXT NOT NULL,
	egress_ip        TEXT NOT NULL DEFAULT '',
	created_at_ns    INTEGER NOT NULL DEFAULT 0,
	expiry_ns        INTEGER NOT NULL,
	last_accessed_ns INTEGER NOT NULL,
	PRIMARY KEY (platform_id, user_id, account)
);

INSERT INTO leases (platform_id, user_id, account, node_hash, egress_ip, created_at_ns, expiry_ns, last_accessed_ns)
SELECT platform_id, '', account, node_hash, egress_ip, created_at_ns, expiry_ns, last_accessed_ns
FROM leases__account_only;

DROP TABLE leases__account_only;
