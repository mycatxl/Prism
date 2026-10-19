# Prism

Prism is a single-port proxy router with a management interface. It keeps a pool
of upstream nodes, hands them out through sticky per-account leases, and exposes
HTTP, SOCKS5, reverse-proxy and subscription endpoints on one listener.

This repository ships **code, this README and the operator guide in
[`docs/MULTI_TENANT.md`](docs/MULTI_TENANT.md)**. Design notes, plans, audits
and research do not live here: `.gitignore` refuses them and the CI build does
not depend on any of them.

## Build

```bash
make build      # frontend bundle + ./bin/prism
make backend    # ./bin/prism (embeds the frontend bundle it builds first)
make verify     # vet + tests + race + protocol matrix + frontend checks
make smoke      # end-to-end run of the built binary
```

The build tags are one set everywhere (Makefile, Dockerfile, release workflow):

```
with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy
```

`sing-box` is the only outbound engine. The `with_mihomo` seam is kept in the
tree so the decision can be revisited, but the tag is part of no build: passing
it would make `GET /api/v1/system/capabilities` report mihomo as built while
every mihomo node still fails with `ENGINE_NOT_BUILT`.

## Run

```bash
./bin/prism init       # creates the administrator account, the pepper and a proxy token
./bin/prism check-config
./bin/prism
```

`prism init` creates the administrator (`admin`; the password comes from
`--admin-password`, or is generated and printed once), the password pepper in
`PRISM_STATE_DIR` and a random proxy token. It refuses to run against a store
that already holds a user, so an existing installation is never overwritten:
starting over means backing up and removing the data directory. `--force`
overwrites the generated `.env` only, moving the old one to
`.env.bak.<timestamp>`. There is no `PRISM_ADMIN_TOKEN` - the console keeps its
session in the `prism_session` HttpOnly cookie and echoes `prism_csrf` in
`X-Prism-CSRF` on writes; automation uses an admin access key instead.

The panel is served at `/ui/`. The example environment binds to `127.0.0.1` by
default. Prism has no TLS listener of its own: terminate TLS in a reverse proxy
and list that proxy in `PRISM_TRUSTED_PROXIES`, which is what makes Prism read
its `X-Forwarded-Proto: https`. Without that entry the request is plaintext HTTP
from Prism's point of view, and the session cookie is issued only for a loopback
plaintext request - a remote panel over a proxy that is not listed cannot log in.

Container deployment uses `Dockerfile` and `docker-compose.yml.example`, which documents the volume layout, the container user and the health check. A tagged release publishes a multi-arch image to [`ghcr.io/mycatxl/prism`](https://github.com/mycatxl/Prism/pkgs/container/prism); `docker-compose.yml.example` pins the release it deploys.

**Initialise the store before the first `up`.** The image's entrypoint only creates the data directories and drops to the `prism` user: it never creates an account, and self-registration is off unless `PRISM_REGISTRATION` says otherwise (`off` is the default), so a container that was simply started has no way in. `prism init` is the only entry point that creates the administrator and the password pepper, and it refuses to run against a store that already holds a user - which is why the compose file does not run it as a service. Run it once, against the same volumes the service uses:

```bash
docker compose run --rm prism prism init --dir /tmp --admin-username admin
```

It prints the generated administrator password once; copy it. `--dir /tmp` keeps the generated `.env` inside that throw-away container: the account and the pepper land in the `prism_state` volume, and the settings the service reads come from the `environment:` block of the compose file. Pass `--admin-password` to choose the password instead. Then start the service and sign in at `/ui/` as that administrator:

```bash
docker compose up -d
```

A second `init` fails by design; starting over means removing the `prism_state` volume, which holds the store, the pepper and every account.

## Configuration

Every setting is a `PRISM_*` variable; `.env.example` and
`deploy/backend.env.example` document all of them with the value that applies
when the line is absent. `./bin/prism check-config` prints the effective
configuration with every secret masked, one row per setting, and the last column
says where the value came from: `env` (process environment or `./.env`),
`legacy` (the deprecated `RESIN_` spelling), `empty` (set but empty, so the
default applies) or `default`. A test keeps that table and both example files
in step with every variable a non-test file reads.

Several switches are on by default: `PRISM_DENY_PRIVATE_NODES`,
`PRISM_DIRECT_DENY_PRIVATE`, `PRISM_BLOCKED_DESTINATION_PORTS=25,465,587` and
proxy-entry failure limiting (`PRISM_PROXY_AUTH_FAIL_LIMIT=30`). The first two
refuse loopback, private, link-local, CGNAT, reserved and cloud-metadata targets
on every local dial path; the port list independently refuses the SMTP ports, so
an exposed deployment is not an outbound mail relay. Turn them off only on a
trusted network.

## API

The management API lives under `/api/v1/`. `GET /api/v1/system/capabilities`
reports the engines, protocols and build tags this binary actually has, so a
client never has to guess what a build can do. The console authenticates with a
username, a password and an optional TOTP code, and keeps the session in a
HttpOnly cookie; automation sends an admin access key instead:
`Authorization: Bearer <admin-access-key>`.
The deployment is multi-tenant: an administrator keeps the nodes, plans and
tenant accounts, while each tenant only sees its own subscription, keys and
usage under `/api/v1/me/*`. Roles, registration modes, plan snapshots, key
revocation and the retention windows are documented in
[`docs/MULTI_TENANT.md`](docs/MULTI_TENANT.md). That guide also covers the abuse-protection defaults and the usage-anomaly alerts: an account that spends a third of its plan quota inside an hour is reported once (`PRISM_ALERT_USAGE_RATIO`, `PRISM_ALERT_WINDOW_MINUTES`, `PRISM_ALERT_RETENTION_DAYS`). The three are hot-changeable in the console, where a change is in force at the next scan - at most a minute later, not instantly.

The commercial path is part of the same API: a tenant reads a balance (`GET /api/v1/me/balance`), tops it up by redeeming a code an administrator minted on `/admin/redeem-codes` (`POST /api/v1/me/redeem`) or by an on-chain USDT deposit that needs `PRISM_USDT_ADDRESS` set, then spends the balance on a plan (`GET /api/v1/me/plans`, `POST /api/v1/me/purchases`). `make billing` walks that path end-to-end over real HTTP against a fake TronGrid, and CI runs it on every push.

## Layout

```
cmd/prism/          # entrypoint, listeners, runtime assembly
internal/api/       # HTTP handlers and the web console (internal/api/web)
internal/intel/     # node intelligence: providers, checks, jobs, store
internal/node/      # node documents, hashing, engine capabilities
internal/outbound/  # sing-box runtime and protocol matrix
internal/proxy/     # forward proxy, CONNECT and the reverse-proxy path
internal/routing/   # platform routing, leases and rotation
internal/state/     # SQLite state/cache engines and migrations
internal/subscription/  # subscription parsers (links, Clash, sing-box, Surge)
internal/topology/  # subscriptions, nodes and platform relations
deploy/ docker/ scripts/  # deployment helpers
```

## Acknowledgements

Prism started from [Resin](https://github.com/Resinat/Resin) (commit `9b8ef8e`,
MIT, © Resinat and contributors). Its proxy pool, sticky leases, scheduling,
subscription and probe design are the foundation this project grew from — many
thanks to the Resin authors. The original MIT notice is kept in
[`LICENSES/Resin-MIT.txt`](LICENSES/Resin-MIT.txt); see
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) for the full provenance.
Thanks also to [sing-box](https://github.com/SagerNet/sing-box), the engine
under every outbound.

## License

GPL-3.0-or-later — see the `LICENSE` file for the full text. Code derived from
Resin keeps its MIT notice (`LICENSES/Resin-MIT.txt`).

## Contributing

1. Fork the repository.
2. Create a feature branch.
3. Run `make verify` and `bash scripts/smoke.sh` before opening a pull request.
4. Keep it to code and the documentation under `docs/`: this repository does not carry design notes, plans or audits.

## Support

- GitHub Issues: https://github.com/mycatxl/Prism/issues
