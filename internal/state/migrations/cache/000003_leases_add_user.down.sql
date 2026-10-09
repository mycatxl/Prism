-- Keeps only the built-in admin's leases; other users' leases cannot be
-- represented without user_id.
CREATE TABLE leases__old (
	platform_id      TEXT NOT NULL,
	account          TEXT NOT NULL,
	node_hash        TEXT NOT NULL,
	egress_ip        TEXT NOT NULL DEFAULT '',
	created_at_ns    INTEGER NOT NULL DEFAULT 0,
	expiry_ns        INTEGER NOT NULL,
	last_accessed_ns INTEGER NOT NULL,
	PRIMARY KEY (platform_id, account)
);

INSERT INTO leases__old (platform_id, account, node_hash, egress_ip, created_at_ns, expiry_ns, last_accessed_ns)
SELECT platform_id, account, node_hash, egress_ip, created_at_ns, expiry_ns, last_accessed_ns FROM leases WHERE user_id = 'u_admin';

DROP TABLE leases;
ALTER TABLE leases__old RENAME TO leases;
