package model

// Multi-tenant identifiers that migrations and code both rely on.
const (
	// BuiltinAdminUserID is created by state migration 000017. Legacy
	// PRISM_PROXY_TOKEN traffic and pre-upgrade sticky leases belong to it.
	BuiltinAdminUserID = "u_admin"
	// LegacyKeyID is recorded as key_id for PRISM_PROXY_TOKEN traffic.
	LegacyKeyID = "legacy"
)
