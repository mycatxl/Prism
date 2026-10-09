package model

// Multi-tenant identifiers that migrations and code both rely on.
const (
	// BuiltinAdminUserID is the first admin, created by state migration
	// 000017 (password set by `prism init`). Pre-upgrade sticky leases
	// belong to it.
	BuiltinAdminUserID = "u_admin"
)
