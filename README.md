# Prism

Prism is a single-port proxy router and multi-tenant control plane. It manages upstream nodes, sticky per-account leases, HTTP, SOCKS5, reverse-proxy and subscription access from one service.

## Quick start

### Local

```bash
make build
./bin/prism init --admin-username admin
./bin/prism
```

`prism init` creates the first administrator, password pepper and proxy token. The generated password is printed once. The management panel is served at `/ui/` on the configured listener.

### Docker

```bash
cp docker-compose.yml.example docker-compose.yml
docker compose run --rm prism prism init --dir /tmp --admin-username admin
docker compose up -d
```

Run `init` once before the first `up`. The state volume contains the database, pepper and accounts. The container does not create an administrator automatically.

## Build and verify

```bash
make build       # frontend bundle and binary
make verify      # vet, tests, race, protocol and frontend checks
make test-ui     # Chromium UI checks
make smoke       # end-to-end binary smoke test
make billing     # commercial flow over real HTTP
```

## Configuration

- Use `PRISM_*` variables. `.env.example` and `deploy/backend.env.example` list the supported settings.
- Run `./bin/prism check-config` to inspect effective configuration with secrets masked.
- The default network policy blocks private destinations and common SMTP ports. Disable those guards only on a trusted network.

## Security

- Bind the service to loopback for local administration. For remote access, terminate TLS at a reverse proxy and configure `PRISM_TRUSTED_PROXIES`.
- Browser sessions use an HttpOnly session cookie and CSRF protection. Automation uses an admin access key.
- The control plane enforces administrator and tenant boundaries on the server. The frontend is not an authorization boundary.

## Layout

```text
cmd/prism/          process entrypoint and runtime assembly
internal/api/       HTTP handlers and the embedded web console
internal/node/      node documents and engine capabilities
internal/proxy/     forward, CONNECT and reverse-proxy paths
internal/routing/   platform routing, leases and rotation
internal/state/     SQLite state, cache and migrations
deploy/ docker/      deployment helpers
```

The web console lives in `internal/api/web` and is embedded into the Go binary during `make build`.

## Documentation

- [`docs/MULTI_TENANT.md`](docs/MULTI_TENANT.md): roles, registration, plans, keys, usage, billing and operations.
- [`docs/specs/`](docs/specs/): architecture and implementation decisions.

## Repository hygiene

Published changes are limited to source code, tests, build configuration, this README, licenses and approved operator documentation. Never commit passwords, tokens, private keys, `.env` files, logs, databases, screenshots, generated bundles or temporary audit artifacts.

## License

GPL-3.0-or-later. Code derived from Resin keeps its MIT notice in [`LICENSES/Resin-MIT.txt`](LICENSES/Resin-MIT.txt); see [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) for provenance.

## Contributing

Run `make verify`, `make test-ui` and `make smoke` before opening a pull request. Keep changes scoped, review the diff for sensitive data, and update the relevant `docs/` contract when behavior or an architectural boundary changes.
