# Prism Security Controls

This document replaces `SECURITY_AUDIT_COMPLETED.md`, which was deleted in WP04:
it listed controls that did not exist in the code base ("rate limiting on
authentication endpoints", "audit logging for administrative actions",
"rejects absolute paths", "all tests pass").

**Rules for this document**: it only describes behaviour that is implemented
today, and every entry names the automated test that verifies it. Controls that
exist but have no test, and known limitations, are listed separately and
explicitly — they are not security claims.

How to reproduce everything in §1:

```sh
TAGS='with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy with_mihomo'
go test -tags "$TAGS" -count=1 ./cmd/... ./internal/...
bash scripts/smoke.sh
```

Code references name the file and the symbol instead of line numbers, because
this document and the code it describes change in the same work packages.

## 1. Controls verified by an automated test

### 1.1 Admin API authentication

`/api/v1/*` is served through `AuthMiddleware`
(`internal/api/rate_limiter.go`, wired for `mux.Handle("/api/", ...)` in
`internal/api/server.go`). It requires
`Authorization: Bearer <PRISM_ADMIN_TOKEN>`, compares the token with
`crypto/subtle.ConstantTimeCompare`, and answers `401 UNAUTHORIZED` otherwise.
A missing `Authorization` header, a malformed header and a wrong token all
fail; the management routes are unreachable without the token.

Verified by:

- `scripts/smoke.sh` check `/api/v1/system/info rejects missing token` → 401
  (`scripts/smoke.sh:316-317`).
- `scripts/smoke.sh` check `/api/v1/system/info accepts the admin token` → 200
  (`scripts/smoke.sh:318-319`).
- `TestAuthRateLimit_SuccessfulRequestsAreNotCounted`
  (`internal/api/wp04_security_test.go`) exercises the successful path.

### 1.2 Admin authentication failure limiting

Failed authentications are counted per client IP; after 10 failures within one
minute the client IP is blocked for 5 minutes and every management API request
from it is answered with `429 RATE_LIMITED` and a `Retry-After` header
(`internal/api/rate_limiter.go`, `AuthFailureLimiter`). Successful requests are
not counted. The tracking table holds at most 65536 client IPs; the oldest entry
is evicted first.

The client key is the host part of `RemoteAddr`. `X-Forwarded-For` is honoured
**only** when `RemoteAddr` itself is inside `PRISM_TRUSTED_PROXIES` (a
comma-separated CIDR list, empty by default), and then the last untrusted
address in the header is used — a forged header from an untrusted peer has no
effect.

Verified by:

- `TestAuthRateLimit_BlocksAfterTenFailures`
  (`internal/api/wp04_security_test.go`).
- `TestAuthRateLimit_IgnoresForgedXForwardedFor`
  (`internal/api/wp04_security_test.go`).
- `TestAuthRateLimit_TrustsConfiguredProxyXForwardedFor`
  (`internal/api/wp04_security_test.go`).
- `TestAuthRateLimit_SuccessfulRequestsAreNotCounted`
  (`internal/api/wp04_security_test.go`).
- `TestAuthRateLimit_CapsTableSize` (`internal/api/wp04_security_test.go`).

The proxy entrypoints share the same limiter when `PRISM_PROXY_AUTH_FAIL_LIMIT`
is positive. It covers the reverse-proxy path token as decided by the inbound
mux (`cmd/prism/inbound_mux.go`, `shouldRejectReverseProxyByToken`: `403
AUTH_FAILED`, and `429 RATE_LIMITED` with `Retry-After` once the client is
blocked, through `writeInboundAuthFailed` / `writeInboundRateLimited`), the
SOCKS5 username/password exchange (`cmd/prism/proxy_auth_guard.go`,
`newSocks5AuthGuard`, which observes the RFC 1929 reply because a raw TCP session
has no status line; a blocked client gets the `0x05 0xFF` "no acceptable
methods" reply and a closed session), and the checks `internal/proxy` meters
itself: the forward-proxy `Proxy-Authorization` header and the CONNECT entry
(`internal/proxy/forward.go`), and the path token inside the reverse handler
(`internal/proxy/reverse.go`). The default `0` disables all of it; §3.2 lists
what the limiter does *not* cover.

Verified by:

- `TestInboundMuxReversePathTokenFailuresAreMetered`,
  `TestInboundMuxSuccessfulPathTokenIsNotCounted` and
  `TestInboundMuxPathTokenIsNotMeteredWithoutALimiter`
  (`cmd/prism/proxy_auth_guard_test.go`).
- `TestSocks5EntryMetersWrongCredentialsAndBlocks`,
  `TestSocks5AuthObserverCountsOnlyRejections` and
  `TestSocks5EntryMetersNothingWithoutALimiter` (same file).
- `TestForwardProxy_AuthFailed` and the guard cases of
  `internal/proxy/proxy_test.go` / `internal/proxy/reverse_wp05_test.go`.

### 1.3 Reverse-proxy namespace authentication

The URL reverse proxy uses the path layout `/<PRISM_PROXY_TOKEN>/...`
(`cmd/prism/inbound_mux.go`, `shouldRejectReverseProxyByToken`). A request whose
first path segment does not match the configured proxy token is answered with
`403` and the header `X-Prism-Error: AUTH_FAILED`; it never reaches the
reverse-proxy handler. Inside the reverse proxy the token is compared again in
constant time (`internal/proxy/reverse.go`, `parsePathV1`).

Verified by `TestInboundMux_RejectsReverseWhenTokenMissingOrWrong`
(`cmd/prism/inbound_mux_test.go`).

### 1.4 Endpoint capability gating

Every endpoint record carries `allow_management`, `allow_proxy`,
`allow_http_forward`, `allow_http_reverse`, `allow_socks5` and
`require_proxy_auth_info`. The inbound mux checks the capability of the matching
protocol before dispatch and answers `403 ENDPOINT_CAPABILITY_DISABLED`
otherwise (`cmd/prism/inbound_mux.go`, `newEndpointInboundMux`).

Verified by `TestEndpointInboundMux_AppliesCapabilities` and
`TestEndpointInboundMux_InjectsProxyAuthInfoPolicy`
(`cmd/prism/inbound_mux_test.go`).

### 1.5 Management listener isolation (`PRISM_ADMIN_LISTEN`)

When `PRISM_ADMIN_LISTEN=host:port` is set, a second listener serves only the
management surface — `/`, `/healthz`, `/api/*`, `/ui/*` and the public
subscription path `/sub/{token}` — and `CONNECT` plus every other path answer
`404` (`cmd/prism/admin_runtime.go`, `newAdminOnlyHandler` / `isManagementPath`).
An empty value disables the listener instead of binding one.

The management surface is gated as a whole by the endpoint's `allow_management`
flag: `GET /sub/{token}` reaches the subscription handler through the same
`shouldRouteControlPlane` case as `/api/*` and `/ui/*`
(`cmd/prism/inbound_mux.go`), so the subscription URL the API mints resolves on
the listener that minted it instead of being treated as a reverse-proxy path.

With an empty admin or proxy token the listener must stay on loopback:
`LoadEnvConfig` rejects a non-loopback `PRISM_ADMIN_LISTEN` unless
`PRISM_ALLOW_INSECURE_LISTEN=true`, because `startAdminListener` binds the value
verbatim (`internal/config/env.go`).

Verified by:

- `TestAdminOnlyHandlerRejectsProxyPaths`
  (`cmd/prism/subcommands_test.go`).
- `TestStartAdminListenerDisabledWhenAddressEmpty`
  (`cmd/prism/subcommands_test.go`).
- `TestInboundMuxRoutesSubscriptionPathToManagementHandler`,
  `TestInboundMuxSubscriptionPathHonoursAllowManagement` and
  `TestAdminListenerServesSubscriptionPath`
  (`cmd/prism/subscription_routing_test.go`).
- `TestEmptyTokenRequiresLoopbackAdminListen` and
  `TestEmptyProxyTokenRequiresLoopbackAdminListen`
  (`internal/config/env_test.go`).
- `scripts/smoke.sh` check `admin listener refuses CONNECT`
  (`scripts/smoke.sh:330-335`).

### 1.6 Audit logging of management writes

`AuditMiddleware` (`internal/api/audit.go`, wired inside the body limit in
`internal/api/server.go`) records every **successful** management write
(`POST`, `PUT`, `PATCH`, `DELETE`, including `/actions/*` routes) into the
`audit_log` table. Failed writes are not recorded because nothing changed.

Each record contains:

- `actor`: the first 8 hex characters of the sha256 of the admin token — the
  token itself is never stored (`internal/api/audit.go`, `AuditActor`);
- `remote_addr`;
- `action`: `METHOD` plus the route pattern, for example
  `PATCH /api/v1/platforms/{id}`;
- `target`: the request's path parameter values;
- `detail`: the **key names** of the request body, never the values.

`GET /api/v1/audit-logs?before_id=&limit=` exposes the log with cursor paging
(default limit 100, hard maximum 200 —
`internal/api/audit.go`, `HandleListAuditLogs`). Entries older than 90 days, and
everything beyond the newest 100000 entries, are pruned every 24 hours by
`StartAuditPruner` (`internal/api/audit.go`, started in
`cmd/prism/app_runtime.go`).

Verified by:

- `TestAuditLog_RecordsWritesOnly`
  (`internal/api/wp04_security_test.go`).
- `TestAuditLog_ListEndpoint` (`internal/api/wp04_security_test.go`).
- `TestAuditLog_RetentionPolicy` (`internal/api/wp04_security_test.go`).
- `TestStateRepo_PrismAuditAppendListAndPrune`
  (`internal/state/repo_state_prism_test.go`) covers the storage layer: append,
  descending cursor paging, age pruning and size pruning.
- `TestMigrateStateDB_PrismUpgradePreservesData`
  (`internal/state/migrate_prism_test.go`) checks that the audit table exists
  after upgrading a database created by an older revision.

### 1.7 Local direct dial target policy (`PRISM_DIRECT_DENY_PRIVATE`)

With `PRISM_DIRECT_DENY_PRIVATE=true` (default `false`) **every** local dial path
refuses a target whose address is in the denied set, because all four share one
policy object (`internal/proxy/direct_dial_guard.go`, `DirectDialGuard`):

- the reverse-proxy bypass branch — `403 DIRECT_TARGET_DENIED`
  (`X-Prism-Error`), evaluated before the request is dialled;
- the forward HTTP proxy's local direct branch — `403 DIRECT_TARGET_DENIED`;
- the CONNECT tunnel's local direct branch — `403 DIRECT_TARGET_DENIED`;
- the SOCKS5 inbound's local direct branch — reply `0x02` ("connection not
  allowed by ruleset", RFC 1928).

The denied set is: loopback (`127.0.0.0/8`, `::1/128`), every RFC 1918 range
(`10/8`, `172.16/12`, `192.168/16`), link-local (`169.254/16`, `fe80::/10`),
unspecified (`0.0.0.0/8`, `::/128`), CGNAT (`100.64.0.0/10`, which carries the
Alibaba metadata address `100.100.100.200`), benchmarking (`198.18.0.0/15`),
reserved (`240.0.0.0/4`, including the broadcast address), IPv6 unique-local
(`fc00::/7`), multicast (`ff00::/8`), NAT64 (`64:ff9b::/96`, `64:ff9b:1::/48`),
and the cloud metadata endpoints `169.254.169.254` and `fd00:ec2::254`.
IPv4-mapped IPv6 addresses (`::ffff:127.0.0.1`) are classified as the IPv4
address they route to.

Three deliberate properties:

- **one policy, one code path**: the four entrypoints call the same `CheckTarget`
  and dial through the same `dialer()`, so a path cannot be forgotten again;
- **rebinding-proof**: the target name is resolved and checked first, and the
  address the socket is actually connected to is checked again at dial time
  (`net.Dialer.ControlContext`), so a resolver that answers differently between
  the two calls cannot reach a denied address;
- **fail-closed**: a target name the guard cannot resolve is refused
  (`errDirectDialUnresolvable`) instead of being handed to the transport, which
  would resolve it again.

The switch still applies to local dial paths only: a private target reached
through a remote node is that node's network, not a local SSRF surface, and
node-routed requests keep the upstream Resin behaviour. A request that hits the
bypass rule still does not create a sticky lease.

Verified by:

- `TestReverseProxy_DirectDenyPrivate` and
  `TestReverseProxy_DirectDenyPrivateScope`
  (`internal/proxy/reverse_wp04_test.go`).
- `TestReverseProxy_DirectDenyPrivateAddressClasses`,
  `TestReverseProxy_DirectDenyPrivateFailClosedOnResolution` and
  `TestReverseProxy_DirectDenyPrivateRebindingRefusedAtDialTime`
  (`internal/proxy/direct_dial_guard_test.go`).
- `TestForwardProxy_DirectDenyPrivateBlocksLocalDial`,
  `TestForwardProxy_DirectDenyPrivateFailClosed`,
  `TestForwardProxyConnect_DirectDenyPrivateBlocksLocalDial` and
  `TestSocks5Inbound_DirectDenyPrivateBlocksLocalDial` (same file): one per
  entrypoint, each with the switch off as the Resin-behaviour control.
- `TestDirectDialGuard_AddressSet` (address classes, IPv6 and IPv4-mapped
  forms), `TestDirectDialGuard_CheckTarget` (a resolution failure is refused) and
  `TestDirectDialGuard_DirectTransportRefusesPrivateAddress` (the dial-time hook
  refuses even though no pre-dial check ran).
- `TestReverseProxy_BypassCreatesNoLease`
  (`internal/proxy/reverse_wp04_test.go`).
- `TestReverseProxy_E2ESuccess` and `TestIsValidHost`
  (`internal/proxy/reverse_wp04_test.go`) pin the restored upstream host
  validation.

### 1.8 Token material: file mode and no silent overwrite

`prism init` is the only writer of `.env`. It writes the file with mode `0600`
(`cmd/prism/subcommands.go`, `writeFile0600`), refuses to touch an existing
`.env` unless `--force` is given, and moves the old file to
`.env.bak.<timestamp>` when it is.

Verified by:

- `TestInitCommandWritesEnvWith0600` (`cmd/prism/subcommands_test.go`).
- `TestInitCommandRefusesOverwriteWithoutForce`
  (`cmd/prism/subcommands_test.go`).
- `TestInitCommandWithForceCreatesTimestampedBackup`
  (`cmd/prism/subcommands_test.go`).

### 1.9 Configuration reporting never discloses secrets

`prism check-config` prints the effective configuration with
`PRISM_ADMIN_TOKEN`, `PRISM_PROXY_TOKEN`, `PRISM_QUALITY_API_KEY` and
`PRISM_ABUSEIPDB_API_KEY` replaced by `***`
(`cmd/prism/subcommands.go`, `printEnvConfig`).

Verified by:

- `TestCheckConfigNeverPrintsSecrets` (`cmd/prism/subcommands_test.go`).
- `scripts/smoke.sh` check `check-config succeeds without printing any token`
  (`scripts/smoke.sh:454-463`).

### 1.10 Online backup is a consistent snapshot and excludes secrets

`prism backup --out DIR` copies `state.db`, `cache.db` and `intel.db` with
SQLite `VACUUM INTO` over read-only connections, so a running service is not
disturbed, and writes `manifest.json` with size and sha256 per file
(`cmd/prism/subcommands.go`, `performBackup` / `vacuumInto`). `.env` and every
other file are deliberately excluded. The output directory is mode `0700` and
every file `0600`.

Verified by:

- `TestBackupProducesConsistentDatabasesAndManifest`
  (`cmd/prism/subcommands_test.go`).
- `scripts/smoke.sh` check `online backup databases are consistent (quick_check
  ok)` (`scripts/smoke.sh:420-444`).
- `scripts/smoke.sh` check `backup does not contain .env`
  (`scripts/smoke.sh:414-418`).

### 1.11 Restore verifies the backup before it touches live data

`prism restore --from DIR` verifies the manifest and the size and sha256 of
every file before it moves anything, refuses to run while an instance is active
(live pid file or a connectable listen port), and renames existing databases to
`*.pre-restore-<timestamp>` instead of deleting them
(`cmd/prism/subcommands.go`, `restoreBackup` / `detectRunningService`).

Verified by:

- `TestRestoreRejectsTamperedBackup` (`cmd/prism/subcommands_test.go`).
- `TestRestoreSucceedsWithForceWhenNotRunning`
  (`cmd/prism/subcommands_test.go`).
- `TestRestoreRefusesWhileServiceIsRunning`
  (`cmd/prism/subcommands_test.go`).
- `scripts/smoke.sh` check `restore refuses to run while the service is active`
  (`scripts/smoke.sh:445-452`).

`scripts/prism-backup.sh` is a thin wrapper over these two subcommands; it adds
`--keep N` retention and never packages a live database with `tar`.

### 1.12 Node documents from subscriptions cannot name local files

A node document is untrusted subscription content: a sing-box JSON outbound is
stored verbatim and a Clash/Surge or share-link `ca:` is mapped onto
`tls.certificate_path`. sing-box opens such a path while building the outbound
and, when the read or the certificate parse fails, puts the file's content into
the error string, which is stored on the node and returned by the node APIs.
A path such as `/dev/zero` or a FIFO made that read unbounded as well.

`node.RejectLocalFileRefs` (`internal/node/untrusted_refs.go`) now refuses any
path-valued option (`*_path`, including `certificate_path`,
`client_certificate_path`, `client_key_path`, `key_path`, `crl_path`,
`static_key_path`, `private_key_path`) that holds a file reference instead of
inline material, wherever it appears in the document (main object, nested `tls`,
chain dependencies). It runs inside `node.ParseNodeDoc`, which every consumer
uses — the outbound builder, the export path and the subscription parse report —
so no input format can bypass it. The refusal carries reason
`UNSUPPORTED_FEATURE` and detail `<field>: file path is not supported; inline the
material` (the same rule the `.ovpn` parser already applied to `ca`/`cert`/`key`
file paths), and it never echoes the path or the content. Inline PEM material
keeps working. `OutboundManager.EnsureNodeOutbound` also caps the error text it
stores, so no engine error is reflected at unbounded length.

Verified by:

- `TestParseRefusesLocalFileRefsPerFormat` (`internal/outbound/untrusted_refs_test.go`):
  one case each for sing-box JSON, Clash and a share link, asserting the reason
  and the exact detail string.
- `TestBuildRefusesLocalFileRefWithoutReadingTheFile` (same file): a readable
  canary file is never opened and its content never appears in the error.
- `TestRejectLocalFileRefsCoversEveryPathOption` (same file): the option names,
  the nesting, list values, and the bound on the reflected detail.
- `TestParseKeepsInlineCertificateMaterial` and
  `TestEnsureNodeOutboundRecordsBoundedRefusal` (same file).

## 2. Implemented but not covered by an automated test

These behaviours exist in the code but have no test today. They are listed so
that the gap is visible; they are not guarantees.

- **Token requirements** (`internal/config/env.go`, `LoadEnvConfig`):
  `PRISM_ADMIN_TOKEN` and `PRISM_PROXY_TOKEN` must be set and non-empty unless
  `PRISM_ALLOW_EMPTY_ADMIN_TOKEN` / `PRISM_ALLOW_EMPTY_PROXY_TOKEN` are set
  explicitly. An empty token also forces **every** listener onto loopback:
  `PRISM_LISTEN_ADDRESS` and `PRISM_ADMIN_LISTEN` are both rejected when they
  are not a loopback address, unless `PRISM_ALLOW_INSECURE_LISTEN=true`
  (`TestEmptyTokenRequiresOptInAndLoopback`,
  `TestEmptyTokenRequiresLoopbackAdminListen` and
  `TestEmptyProxyTokenRequiresLoopbackAdminListen`, `internal/config/env_test.go`
  — before that check existed only `PRISM_LISTEN_ADDRESS` was gated, so an empty
  token could still expose the management plane through `PRISM_ADMIN_LISTEN`).
  `ValidateProxyTokenForV1` additionally requires 16+ characters for the proxy
  token, forbids `. : | / \ @ ? # % ~` and whitespace, and rejects the reserved
  words `api`, `healthz`, `ui`. The check is a length check — there is no
  entropy, dictionary or pattern analysis.
- **Credential fields are excluded from JSON serialization** (`json:"-"` on the
  `AdminToken` and `ProxyToken` fields of `EnvConfig`,
  `internal/config/env.go`) so a marshalled `EnvConfig` cannot leak them.
- **Request body limit**: `PRISM_API_MAX_BODY_BYTES` (default 1 MiB) is applied
  to every `/api/` request by `RequestBodyLimitMiddleware`
  (`internal/api/middleware.go`, wired in `internal/api/server.go`).
- **Data directory validation** (`internal/config/env.go`, `cleanDirPath`):
  `PRISM_STATE_DIR`, `PRISM_CACHE_DIR` and `PRISM_LOG_DIR` are rejected when any
  path segment is exactly `..` (`PRISM_STATE_DIR: must not contain '..'
  segments`), and otherwise cleaned with `filepath.Clean`. Absolute paths such
  as `/var/lib/prism` are accepted (the Docker deployment needs them).
- **`PRISM_TRUSTED_PROXIES`, `PRISM_DIRECT_DENY_PRIVATE` and
  `PRISM_PROXY_AUTH_FAIL_LIMIT` input validation** (`internal/config/env.go`,
  `LoadEnvConfig`): each trusted CIDR must parse, and the proxy failure limit
  must not be negative. The parsing itself is only exercised indirectly through
  the limiter tests in §1.2.
- **Node hash parsing** (`internal/node/hash.go`, `ParseHex`) requires exactly
  16 bytes of hex; malformed input is rejected instead of being truncated.
- **Single-writer SQLite connections**: the state, request-log and backup
  databases are opened with `SetMaxOpenConns(1)` (`internal/state/schema.go`,
  `internal/requestlog/repo.go`, `cmd/prism/subcommands.go`).
- **File modes for backups** (`cmd/prism/subcommands.go`, `performBackup`):
  `prism backup` creates its output directory with `0700` and every database and
  the manifest with `0600`.
- **Audit append failures are logged, not fatal**
  (`internal/api/audit.go`, `AuditMiddleware`): a failing audit write does not
  turn a successful mutation into an error. The trade-off (fail the request vs.
  keep serving) is deliberate but untested.
- **`scripts/deploy.sh`** never writes or echoes a token: it calls `prism init`
  without `--force` when `.env` is missing, discards the stdout that contains the
  admin token, and prints only paths. It writes `.env` backups
  (`<.env>.bak.<timestamp>`) before editing single keys.

## 3. Known limitations

### 3.1 Transport security

The listeners speak plain HTTP; there is no TLS listener and no HSTS. Terminate
TLS in a reverse proxy in front of Prism and keep `PRISM_LISTEN_ADDRESS` on
loopback unless the network path is trusted.

### 3.2 Proxy entry failure limiting is off by default

`PRISM_PROXY_AUTH_FAIL_LIMIT` defaults to `0`, which disables failure limiting
on the proxy entrypoints (407/403 responses and the SOCKS5 username/password
rejection); only the management API is protected by default (§1.2). This keeps
the upstream Resin behaviour (`cmd/prism/app_runtime.go`,
`internal/proxy/auth_guard.go`, `cmd/prism/proxy_auth_guard.go`). Set a positive
value to enable the same limiter for proxy authentication failures; §1.2 lists
the entry points it then covers.

Deliberate exceptions, so the next reader does not have to grep for them:

- **An empty proxy token has nothing to meter.** With
  `PRISM_ALLOW_EMPTY_PROXY_TOKEN=true` the proxy entrypoints do not authenticate
  at all (`internal/proxy/forward.go`, `internal/proxy/socks5.go`: a SOCKS5
  client may offer the no-auth method, which the guard therefore never counts).
  The loopback rule of §1.5 is what constrains that configuration.
- **`/sub/{token}` is not a `PRISM_PROXY_AUTH_FAIL_LIMIT` surface.** The public
  subscription endpoint answers an unknown token with `404`, but it has its own
  limiter — 60 requests per minute per token and 120 per minute per client IP,
  answered `429` — which is independent of the proxy limiter
  (`internal/api/export_token.go`, `internal/api/handler_subscription_token.go`).
- **The admin API keeps its own fixed limiter.** `/api/*` is governed by
  `AuthMiddleware` (10 failures per minute, 5-minute block, §1.2); raising
  `PRISM_PROXY_AUTH_FAIL_LIMIT` does not change it, and lowering it does not
  weaken it.

The SOCKS5 entry has no HTTP status to answer, so a blocked client receives the
RFC 1928 `0x05 0xFF` ("no acceptable methods") method-selection reply and a
closed session instead of the `429 RATE_LIMITED` the HTTP entries return.

### 3.3 Direct target policy is off by default

`PRISM_DIRECT_DENY_PRIVATE=false` is the default, so **none** of Prism's local
dial paths (the reverse-proxy bypass branch, the forward HTTP proxy, CONNECT and
SOCKS5) applies an address policy unless it is enabled (§1.7). Without it, a
client that holds the proxy token can reach loopback, private, CGNAT, reserved
and cloud-metadata addresses through those paths. Enable it whenever the proxy
token is handed to untrusted clients and the host can reach metadata services or
internal networks; node-routed requests are unaffected either way.

## 4. Reporting a problem

Security-relevant reports belong in a private GitHub Security Advisory for
`mycatxl/Prism` rather than a public issue. Include the version output of
`prism version` and, when possible, the failing test name from §1.
