-- Roll back to the account-only lease key.
--
-- Rows whose owner is empty are carried back as they were. A lease that
-- belongs to a named user is dropped: the old schema cannot express its owner,
-- and collapsing it onto the account would hand that tenant's egress IP to the
-- single-tenant path. Such a lease is simply drawn again on the next request.
ALTER TABLE leases RENAME TO leases__with_owner;

CREATE TABLE leases (
	platform_id      TEXT NOT NULL,
	account          TEXT NOT NULL,
	node_hash        TEXT NOT NULL,
	egress_ip        TEXT NOT NULL DEFAULT '',
	created_at_ns    INTEGER NOT NULL DEFAULT 0,
	expiry_ns        INTEGER NOT NULL,
	last_accessed_ns INTEGER NOT NULL,
	PRIMARY KEY (platform_id, account)
);

INSERT INTO leases (platform_id, account, node_hash, egress_ip, created_at_ns, expiry_ns, last_accessed_ns)
SELECT platform_id, account, node_hash, egress_ip, created_at_ns, expiry_ns, last_accessed_ns
FROM leases__with_owner
WHERE user_id = '';

DROP TABLE leases__with_owner;
