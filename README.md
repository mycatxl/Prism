[English](README.md) | [简体中文](README_zh.md)

# Prism

**Release target: v0.5.0** · Container image: `ghcr.io/mycatxl/prism:0.5.0`

Prism is a single-port proxy router with a multi-tenant control plane. It brings upstream nodes, platform routing, sticky account leases, HTTP/HTTPS forwarding, SOCKS5 and reverse-proxy access into one service, with an embedded web console for operators and tenants.

## Features

- **Multi-tenant access:** administrators manage users, plans, subscriptions and keys; tenants see their own plan, keys, usage and leases. Server-side authorization enforces these boundaries.
- **AccessKey authentication:** each proxy key belongs to a user and can restrict platforms, source IPs and expiry. Complete credentials appear once on creation; storage retains a pepper-protected verification hash. Browser sessions use HttpOnly cookies, CSRF protection and optional TOTP.
- **Centered-dialog WebUI:** Chinese and English interfaces provide separate operator and tenant workspaces. Create, edit, inspect and confirm operations use centered dialogs.
- **Clear node metrics:** inventory, healthy nodes, unique healthy egress IPs and each platform's routable pool have separate counts. Subscription-refresh diagnostics and probe counters are tracked separately from proxy request and traffic metrics.
- **Evidence-based purity:** valid IPPure evidence for the current egress IPv4 determines `purity = 100 - fraudScore`. Other providers supply network facts and risk indicators. Missing, failed or expired scores remain unrated with an explanation; a measured zero is a valid score.
- **Sticky routing and rotation:** leases are isolated by platform, user and account. Requests renew the idle TTL within a seven-day lifetime cap. Failover can retain the same egress IP; manual rotation releases the lease for the next request.
- **Offline administrator recovery:** `recover-admin` creates a new administrator and audit record after the service is stopped, preserving existing accounts, factors, subscriptions and usage.

The Default platform sets no minimum purity score, while still requiring current valid scoring and the other routing checks. Pool health counts do not imply that every node passes a platform's admission policy. Subscription enablement and probe results still affect node health and routing.

## Quick start

### Local build

Requires Go **1.27.0+**, Node.js **22.12.0+**, npm and make. The build installs frontend dependencies, bundles the console and embeds it in the Go binary.

```bash
make build VERSION=v0.5.0
./bin/prism init --admin-username admin
./bin/prism run
```

Open `http://127.0.0.1:2260/ui/`. Initialization creates the first administrator, databases, key pepper and private `.env`. Store the generated password shown once on the local console. Registration is off by default. Run `init` only for a new installation; an existing user database is refused.

### Docker Compose

Requires Docker with Compose. On a **new deployment**, prepare the example and a private host `.env` (OpenSSL generates the required legacy configuration token):

```bash
cp docker-compose.yml.example docker-compose.yml
(umask 077; set -C; printf 'PRISM_PROXY_TOKEN=%s\n' "$(openssl rand -hex 32)" > .env)
```

Choose the image source:

- **Build this checkout:** keep `build:` and run `docker compose build`.
- **Use the published release:** remove the entire `build:` block, keep `image: ghcr.io/mycatxl/prism:0.5.0`, and run `docker compose pull` once that release is published.

Then initialize the shared state volume once and start the service:

```bash
docker compose run --rm prism prism init --dir /tmp --admin-username admin
docker compose up -d
```

Save the generated administrator password and sign in at `http://127.0.0.1:2260/ui/`. The container entrypoint prepares directories and runs as the `prism` user; it does not create an administrator automatically. The initialization container's `/tmp/.env` is temporary, so the host `.env` supplies the running service's configuration token. This token remains required by startup validation and cannot authenticate proxy traffic.

The example binds host port 2260 to loopback and persists state, cache and logs in named volumes. Git release tag `v0.5.0` produces container tag `0.5.0`, without the `v` prefix.

### Prepare proxy access

1. Import upstream nodes and wait for current egress, positive latency, valid scoring and applicable privacy checks.
2. Create a plan with access to the desired platforms, then assign a subscription to the user. Invite-based registration can grant its associated plan; open registration alone grants none.
3. Issue a **proxy-scope AccessKey** from the user's key page or through administrator user management. Save the complete `pk_<id>_<secret>` credential when it is shown once.

## Protocols and routing

The default listener serves HTTP forward proxy, HTTPS via CONNECT, authenticated SOCKS5 TCP CONNECT, reverse HTTP/HTTPS forwarding, the API and `/ui/` on the same port.

| Entrance | Client configuration |
| --- | --- |
| HTTP proxy / CONNECT | Basic proxy authentication: username `Platform.Account`, password the complete proxy AccessKey |
| SOCKS5 | Username/password authentication: username `Platform.Account`, password the complete proxy AccessKey |
| Reverse proxy | Path `/<ACCESS_KEY>/Platform.Account/https/example.com/path` on the Prism listener |

`Platform` selects a permitted routing platform; `Account` identifies a sticky lease within that tenant and platform. An empty account permits random routing, subject to the platform's reverse-proxy account rules. The same account name in different tenants does not share a lease. Access also depends on enabled account/key state, expiry, platform permissions, IP allowlists and quota. Traffic quota is shared across a user's keys; current rate, concurrency and bandwidth limits apply per key.

SOCKS5 supports TCP CONNECT only. The embedded **sing-box** engine supports upstream protocols including Shadowsocks, Trojan, VMess, VLESS, AnyTLS, Hysteria/Hysteria2 and TUIC, plus WireGuard, OpenVPN and OpenConnect endpoints. Import formats include Clash, sing-box, Surge, share links, Base64 subscriptions and OpenVPN configurations. Import recognition does not guarantee runtime or privacy-policy eligibility: inspect the capability report and import results. Mihomo fallback protocols and VLESS xhttp are not built into this release.

## Deployment architecture

```text
Browser / API client / proxy client
                |
     Optional TLS terminator
                |
     Prism listener (default :2260)
        |                       |
  Embedded WebUI + API     HTTP / SOCKS5 / reverse
        |                       |
  Users, plans, keys      Authentication + platform policy
        |                       |
  SQLite state/cache/intel   Sticky lease -> sing-box -> upstream
```

The TLS terminator is typically used for remote web/API access and HTTP reverse forwarding. Direct HTTP-proxy and SOCKS5 clients must reach a listener that accepts their protocol.

One Prism process owns each state directory. SQLite stores accounts, subscriptions, usage and audit data alongside cache and intelligence databases; key snapshots and connection registries live in that process. A directory lock prevents two processes opening the same state directory. Shared-state clustering, cross-instance revocation and quota synchronization are outside v0.5.0.

Additional endpoints can enable selected proxy protocols. `PRISM_ADMIN_LISTEN=host:port` adds a management-only listener for `/ui/`, `/api/` and `/healthz`; the primary listener continues to expose management routes.

## Configuration and security

- Use `PRISM_*` variables. [`.env.example`](.env.example) and [`deploy/backend.env.example`](deploy/backend.env.example) document deployment settings. Initialize and run against the same storage paths, keeping the generated `.env` private.
- Inspect effective configuration with `./bin/prism check-config`; secrets are masked. The console can change supported runtime settings without a restart.
- For remote web access, terminate TLS at a reverse proxy and restrict `PRISM_TRUSTED_PROXIES` to that proxy. Browser sessions require HTTPS or local loopback HTTP. Restrict listener exposure to the intended clients.
- Automation can authenticate management reads and supported operations with an admin-scope AccessKey. Credential issuance, payment settings and privacy-admission changes require an administrator browser session with CSRF validation. Admin keys and browser sessions do not replace proxy keys.
- Default network guards reject private/metadata targets and common SMTP ports on local direct paths. Node admission separately checks forbidden node addresses, known own-asset exits and configured privacy rules.
- Optional anonymity echo measurement is off by default. An unmeasured path carries no anonymity guarantee. Confirmed identity leaks trigger permanent deletion; ordinary health failures are retried and low purity is classified under platform policy.

## Upgrade and recovery

Before upgrading to v0.5.0, take a coordinated backup of **state.db, cache.db, intel.db and pepper**, and separately preserve private `.env`, service/container and reverse-proxy settings. See the [recovery guide](docs/RECOVERY.md). Keep existing storage volumes and configuration; upgrades do not run `init` or remove volumes.

For a deployment using the published image, update its image tag to `0.5.0`, then:

```bash
docker compose pull
docker compose up -d
```

For a source deployment, rebuild with `make build VERSION=v0.5.0` and replace the service binary through the deployment's normal procedure.

If the sole administrator loses access, stop that installation and back it up. On a protected local Bash console, create a new recovery administrator using the installation's actual state/cache paths and an incident reference:

```bash
read -r -s -p 'New recovery password: ' PRISM_RECOVERY_PASSWORD
printf '\n'
printf '%s\n' "$PRISM_RECOVERY_PASSWORD" | ./bin/prism recover-admin \
  --state-dir /srv/prism/state --cache-dir /srv/prism/cache \
  --username recovery-admin --reason INC-1234 \
  --password-stdin --confirm-offline
unset PRISM_RECOVERY_PASSWORD
```

Choose an unused username and a random password of at least 12 characters; disable shell tracing while entering it. The command requires private existing database/pepper files and refuses a running installation. It preserves existing passwords, TOTP factors and replay watermarks. After local sign-in, enroll a fresh second factor and disable the lost administrator. The recovery guide also covers clock correction, full restore, unresolved usage reservations and certificate renewal.

## Build and verify

```bash
make build VERSION=v0.5.0  # frontend bundle and binary
make verify                # vet, tests, race, coverage, protocol and frontend checks
make test-ui               # Chromium UI checks; install Playwright Chromium first
make smoke                 # end-to-end binary checks in a disposable local fixture
make billing               # commercial flow over real HTTP with a local payment fixture
```

The last three commands use the built binary. Install the browser with `npx --prefix internal/api/web playwright install --with-deps chromium` before `make test-ui`.

Offline checks do not establish live egress or payment acceptance. [`docs/LIVE_ACCEPTANCE.md`](docs/LIVE_ACCEPTANCE.md) describes explicit three-protocol verification; its CLI defaults to `NOT_RUN`. `/healthz` reports liveness. Production acceptance also requires verified TLS/time, backup and recovery, sellable inventory, provider allowances and capacity measured on the target hardware with durable accounting; enabled payments require their own live acceptance.

## Repository layout

```text
cmd/prism/           process entrypoint, initialization and offline recovery
internal/api/        HTTP handlers and embedded web console
internal/node/       node documents, health and engine capabilities
internal/proxy/      forward, CONNECT, SOCKS5 and reverse-proxy paths
internal/routing/    platform routing, sticky leases and rotation
internal/intel/      measurement evidence and node assessments
internal/state/      SQLite state, cache and migrations
deploy/ docker/      deployment references and container helpers
docs/                operator and acceptance guides
```

## Documentation

- [`docs/MULTI_TENANT.md`](docs/MULTI_TENANT.md): roles, registration, plans, keys, usage, billing and operational boundaries (Chinese).
- [`docs/RECOVERY.md`](docs/RECOVERY.md): protected offline administrator recovery, backups, clock correction, usage reconciliation and certificate renewal (English).
- [`docs/LIVE_ACCEPTANCE.md`](docs/LIVE_ACCEPTANCE.md): authorized live HTTP/SOCKS5/reverse verification and evidence receipts (English).

## Contributing

Run `make verify`, `make test-ui` and `make smoke` before opening a pull request. Keep changes scoped and update relevant operator documentation when behavior changes. Publish source, tests, build configuration, both READMEs, licenses and operator documentation. Keep credentials, private keys, `.env` files, databases, logs, generated bundles and temporary audit artifacts out of commits.

## License

GPL-3.0-or-later. Code derived from Resin retains its MIT notice in [`LICENSES/Resin-MIT.txt`](LICENSES/Resin-MIT.txt); see [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) for provenance.
