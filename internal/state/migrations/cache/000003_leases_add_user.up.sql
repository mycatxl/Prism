-- Sticky leases are keyed by (user_id, platform_id, account). Existing leases
-- move to the built-in admin so their egress IPs survive the upgrade.
CREATE TABLE leases__new (
	user_id          TEXT NOT NULL DEFAULT 'u_admin',
	platform_id      TEXT NOT NULL,
	account          TEXT NOT NULL,
	node_hash        TEXT NOT NULL,
	egress_ip        TEXT NOT NULL DEFAULT '',
	created_at_ns    INTEGER NOT NULL DEFAULT 0,
	expiry_ns        INTEGER NOT NULL,
	last_accessed_ns INTEGER NOT NULL,
	PRIMARY KEY (user_id, platform_id, account)
);

INSERT INTO leases__new (user_id, platform_id, account, node_hash, egress_ip, created_at_ns, expiry_ns, last_accessed_ns)
SELECT 'u_admin', platform_id, account, node_hash, egress_ip, created_at_ns, expiry_ns, last_accessed_ns FROM leases;

DROP TABLE leases;
ALTER TABLE leases__new RENAME TO leases;
