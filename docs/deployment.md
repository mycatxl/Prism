# Prism Deployment Guide

This guide covers the two supported deployment paths: a compiled `bin/prism`
managed by systemd through `scripts/deploy.sh`, and the container image
described under [Docker](#docker).

## Requirements

- Linux with systemd for the service installer (optional — Prism also runs in the
  foreground without systemd).
- Go 1.26 or later and Node.js/npm to build; `make backend` skips the web UI.
- A deployment directory that contains the binary at `<deploy-dir>/bin/prism` and
  receives the `.env` configuration file.
- Root only for installing the systemd unit (`--no-service` needs no privileges).

## Quick start

```bash
cd /opt/prism                        # deployment directory (repository root works)

make build                           # web UI + backend -> bin/prism
# make backend                       # backend only, also -> bin/prism

./bin/prism init                     # writes ./.env (mode 0600) with fresh tokens
sudo ./scripts/deploy.sh             # prepares .env, data dirs and the systemd unit
```

Then open the web UI on the address the service listens on — by default
`http://127.0.0.1:2260/ui/` — and authenticate with `PRISM_ADMIN_TOKEN` from
`<deploy-dir>/.env`.

`prism init` is the only writer of `.env`: it generates a 64-character random hex
admin and proxy token, prints the **admin token exactly once** and refuses to
touch an existing file unless `--force` is passed (the old file is moved to
`.env.bak.<timestamp>` first).

```bash
./bin/prism version        # version, git commit, build time, build tags
./bin/prism check-config   # effective configuration with every secret masked
```

## deploy.sh

```
Usage: deploy.sh [options]

  --dir <path>           Deployment directory (default: the repository root, i.e.
                         the parent of scripts/). Must contain bin/prism and
                         receives .env.
  --listen <address>     Set PRISM_LISTEN_ADDRESS (e.g. 127.0.0.1 or 0.0.0.0).
  --port <port>          Set PRISM_PORT (default after init: 2260).
  --state-dir <path>     Set PRISM_STATE_DIR (relative paths resolve against the
                         deployment directory).
  --cache-dir <path>     Set PRISM_CACHE_DIR.
  --log-dir <path>       Set PRISM_LOG_DIR.
  --no-service           Only prepare .env and the data directories; do not
                         install the systemd unit.
  --dry-run              Print what would be done without changing anything.
  -h, --help             Show the help message.
```

Only the five override options exist. There is **no** `--admin-token`,
`--log-level` or `--dns-upstreams` flag, and the script does not read `PORT`,
`STATE_DIR` or `ADMIN_TOKEN` from the environment: every setting lives in
`<deploy-dir>/.env` as a `PRISM_*` key. To change something the script cannot
rewrite, edit `.env` (or re-run `prism init --force` for fresh tokens) and
restart the service.

Examples:

```bash
# prepare everything and install the unit with defaults
sudo ./scripts/deploy.sh

# listen on all interfaces, keep the databases under /var/lib/prism
sudo ./scripts/deploy.sh --listen 0.0.0.0 --port 2260 --state-dir /var/lib/prism/state

# prepare .env and directories only (no root, no systemd)
./scripts/deploy.sh --dir /opt/prism --no-service
```

### What the script does

1. **Checks the deployment directory.** It must exist and must not contain
   whitespace. `.env` is `<dir>/.env`.
2. **Checks the privileges** (only when the unit is installed): installing the
   systemd unit requires root. Without `systemctl` it warns and skips the unit.
3. **Checks the binary.** `<dir>/bin/prism` must exist *and* be executable,
   otherwise the script stops with a hint to run `make build`.
4. **Generates `.env` when it is missing** by running
   `(cd <dir> && ./bin/prism init --dir <dir>)`, forcing the file to mode `0600`
   afterwards. The standard output of `prism init` (which contains the admin
   token) is discarded and redacted on failure; the script itself never prints a
   token. **An existing `.env` is never regenerated or overwritten.**
5. **Resolves the effective configuration** from `.env`:
   `PRISM_LISTEN_ADDRESS` (default `127.0.0.1`), `PRISM_PORT` (default `2260`),
   `PRISM_STATE_DIR` (default `./.local/state`), `PRISM_CACHE_DIR`
   (default `./.local/cache`) and `PRISM_LOG_DIR` (default `./.local/logs`).
   An invalid `PRISM_PORT` aborts the run.
6. **Requires the port to be free.** An occupied port is a hard error: the script
   prints the owning socket (`ss`, `lsof` or `netstat`) and refuses to continue.
   It never stops or kills another process.
7. **Applies the overrides.** Each `--listen/--port/--state-dir/--cache-dir/
   --log-dir` rewrites only the matching `PRISM_*` line with `awk`; every other
   line of `.env` is preserved. Before the first change the file is copied to
   `<.env>.bak.<timestamp>` (mode `0600`). Values containing whitespace are
   written double-quoted; quotes, backslashes and `#` are rejected.
8. **Creates the data directories** (`PRISM_STATE_DIR`, `PRISM_CACHE_DIR`,
   `PRISM_LOG_DIR`) when they do not exist.
9. **Installs the systemd unit** (unless `--no-service`): creates the system user
   `prism` (`useradd --system --user-group --home-dir <dir> --shell
   /usr/sbin/nologin`), makes it the owner of the data directories and of `.env`
   (mode `0600`), writes `/etc/systemd/system/prism.service` (mode `0644`), then
   runs `systemctl daemon-reload`, `systemctl enable prism` and
   `systemctl restart prism`. A failing start is reported as an error with the
   commands to inspect it.
10. **Prints a summary**: the `.env` path, the effective listen address and port,
    the data directories, the unit path, its state, the log command and the
    backup command.

### The generated unit

```ini
[Unit]
Description=Prism proxy and control plane
Documentation=https://github.com/mycatxl/Prism
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=prism
Group=prism
WorkingDirectory=<deploy-dir>
ExecStart=<deploy-dir>/bin/prism run
Restart=on-failure
RestartSec=5
SyslogIdentifier=prism
StandardOutput=journal
StandardError=journal

# Hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=true
ReadWritePaths=<state-dir> <cache-dir> <log-dir>

[Install]
WantedBy=multi-user.target
```

`WorkingDirectory=<deploy-dir>` is what makes the service load
`<deploy-dir>/.env`; `ExecStart` uses the explicit `run` subcommand. Because the
unit sets `ProtectSystem=strict`, only the three directories listed in
`ReadWritePaths` are writable for the service. If you change `PRISM_STATE_DIR`,
`PRISM_CACHE_DIR` or `PRISM_LOG_DIR` later, re-run `sudo ./scripts/deploy.sh` so
the unit is regenerated with the new paths.

## Docker

Prism also runs as a container, and the shape of the application keeps that
deployment small:

- **One port carries everything.** The single listener
  `PRISM_LISTEN_ADDRESS`:`PRISM_PORT` (default `127.0.0.1:2260`) serves the Web
  UI (`/ui/`), the API (`/api/v1/`), the HTTP and SOCKS5 forward proxy, the
  `/<token>/...` reverse proxy and `/sub/{token}`. A container therefore
  publishes exactly one port. `PRISM_ADMIN_LISTEN` is disabled by default and
  must stay on loopback when it is enabled.
- **All state is files.** `state.db`, `intel.db`, `cache.db`, `metrics.db`, the
  request-log shards and the rolling logs live in the three data directories, so
  mounting them is the complete persistence story.
- **No external services.** There is no Redis, no PostgreSQL, no message broker
  and no Kubernetes operator to install: the datastores are SQLite files inside
  those directories. A Compose file, a plain `docker run` or a Pod with three
  volumes is all the orchestration this needs.

### Container files in this repository

| File | Purpose |
|------|---------|
| `Dockerfile` | Full multi-stage build: `node:22-alpine` builds the web UI, `golang:1.26-alpine` compiles `cmd/prism` with the full tag set, and the runtime stage (`alpine:3.21`) installs `ca-certificates`, `tzdata` and `su-exec` and places the binary at `/usr/local/bin/prism`. |
| `.github/Dockerfile.release` | Runtime-only image used by the release workflow: it copies the pre-built `linux/amd64` and `linux/arm64` binaries, which already embed the web UI, onto the same runtime stage. |
| `docker/entrypoint.sh` | Container entrypoint: prepares and chowns the data directories, then drops privileges to the `prism` user. |
| `docker-compose.yml.example` | Example service: `ghcr.io/mycatxl/prism:latest`, port `2260:2260`, three named volumes and a `/healthz` healthcheck. |

### Compose quick start

```bash
cp docker-compose.yml.example docker-compose.yml

# The example passes ${PRISM_ADMIN_TOKEN} and ${PRISM_PROXY_TOKEN} into the
# container and `docker compose` interpolates them from ./.env — exactly the
# file `prism init` writes (mode 0600, two 64-character hex tokens).
./bin/prism init
# ...or create ./.env yourself:
#   PRISM_ADMIN_TOKEN=<at least 16 characters>
#   PRISM_PROXY_TOKEN=<at least 16 characters>

docker compose up -d
docker compose ps               # State: running, then (healthy)
docker compose logs -f prism
```

Then open `http://<host>:2260/ui/` and authenticate with the admin token from
`.env`. `docker compose` uses `.env` only to substitute the variables the Compose
file references; the container configuration is the service's `environment:`
block, so the other keys (`PRISM_LISTEN_ADDRESS`, the data directories) do not
reach the container from that file.

The token rules are the same as for any other deployment:

- `PRISM_ADMIN_TOKEN` and `PRISM_PROXY_TOKEN` must both be non-empty, and with
  the default `PRISM_ENFORCE_STRONG_TOKENS=true` each must be at least 16
  characters.
- `PRISM_PROXY_TOKEN` must not contain any of `.:|/\@?#%~` or whitespace, and
  must not be one of the reserved words `api`, `healthz` or `ui`.
- Empty tokens are an explicit opt-in only: `PRISM_ALLOW_EMPTY_ADMIN_TOKEN=true`
  and/or `PRISM_ALLOW_EMPTY_PROXY_TOKEN=true`, and Prism then refuses to start
  with a non-loopback `PRISM_LISTEN_ADDRESS` (or `PRISM_ADMIN_LISTEN`) unless
  `PRISM_ALLOW_INSECURE_LISTEN=true` is set as well. A published container port
  is not loopback, so configure both tokens instead of using that mode.

### Data directories, volumes and the container user

| Volume in the example | Container path | Variable | Contents |
|-----------------------|----------------|----------|----------|
| `prism_state` | `/var/lib/prism` | `PRISM_STATE_DIR` | `state.db`, `intel.db`, `prism.pid` |
| `prism_cache` | `/var/cache/prism` | `PRISM_CACHE_DIR` | `cache.db` and other rebuildable data (GeoIP downloads) |
| `prism_log` | `/var/log/prism` | `PRISM_LOG_DIR` | `metrics.db`, request-log shards and the rolling logs |

The runtime stage creates the system user and group `prism` (`adduser -S -G
prism -h /var/lib/prism prism`), creates those three directories and chowns them
to `prism:prism`. The entrypoint starts as root, runs `mkdir -p` and `chown -R
prism:prism` over the three directories (skip the chown with
`PRISM_SKIP_CHOWN=1`), and then `exec`s `su-exec prism:prism
/usr/local/bin/prism`, so Prism itself never runs as root. A bind mount that the
chown cannot fix fails fast with `fatal: <label> directory is not writable:
<dir>` before Prism starts.

Set the three directory variables explicitly in the container. The entrypoint
reads `PRISM_STATE_DIR`, `PRISM_CACHE_DIR` and `PRISM_LOG_DIR` and falls back to
the in-image paths above, but those fallbacks are shell variables: they are not
exported into the Prism process, and the image sets no working directory. Without
them in the environment Prism uses its own relative defaults (`./.local/state`,
`./.local/cache`, `./.local/logs`) resolved against `/`, so the data would not
land in the mounted volumes. `docker-compose.yml.example` does not set them yet,
so add them to the service:

```yaml
    environment:
      PRISM_ADMIN_TOKEN: ${PRISM_ADMIN_TOKEN}
      PRISM_PROXY_TOKEN: ${PRISM_PROXY_TOKEN}
      PRISM_LISTEN_ADDRESS: 0.0.0.0
      PRISM_PORT: 2260
      PRISM_STATE_DIR: /var/lib/prism
      PRISM_CACHE_DIR: /var/cache/prism
      PRISM_LOG_DIR: /var/log/prism
```

`PRISM_LISTEN_ADDRESS: 0.0.0.0` is what makes the published port reachable; keep
a firewall rule or a reverse proxy in front of it (see
[Reverse proxy and TLS](#reverse-proxy-and-tls)). There is no `.env` inside the
image (`.dockerignore` excludes it) and `prism` loads `./.env` relative to its
working directory, so configure the container through environment variables only.
`docker compose run --rm prism /usr/local/bin/prism check-config` prints the
effective configuration with every secret masked.

### Health and verifying a start

```bash
docker compose ps                                                      # (healthy)
curl -sS http://127.0.0.1:2260/healthz                                 # no token
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:2260/ui/    # 200 = UI served
docker compose exec prism /usr/local/bin/prism version
docker compose exec prism /usr/local/bin/prism check-config
```

The example healthcheck runs `wget -qO- http://127.0.0.1:2260/healthz >/dev/null
|| exit 1` every 30 s with a 5 s timeout, three retries and a 20 s start period,
so `(healthy)` in `docker compose ps` is the quickest confirmation that the
listener is up. `/healthz` needs no token. `/ui/` answers `503` when the frontend
is missing: the `Dockerfile` builds it and the release image embeds it, so a
`503` points at the build. `prism version` prints version, git commit, build time
and build tags (the release image carries the full tag set; mihomo is never
compiled in).

### Upgrading, backup and restore

```bash
# snapshot the databases into the state volume (safe while the container runs)
docker compose exec prism /usr/local/bin/prism backup --out /var/lib/prism/backups

# move to a new image
docker compose pull              # or: docker compose build --pull
docker compose up -d             # recreates the container, keeps the volumes
docker compose ps
```

Images are published as `ghcr.io/mycatxl/prism` for `linux/amd64` and
`linux/arm64` by the release workflow (`release.yml` builds
`.github/Dockerfile.release` and tags `v<version>`, `<major>.<minor>` and
`latest` for non-prereleases). Pin a version tag instead of `latest` so an
upgrade is a deliberate step; the databases are migrated on start.

`prism backup --out DIR` copies `state.db`, `cache.db` and `intel.db` with
`VACUUM INTO` over read-only connections, so it needs no maintenance window, and
databases that do not exist yet are skipped. A backup never contains `.env`, so
keep a copy of the host `.env` (it holds the tokens). Write the snapshot into a
volume as above, or copy it out:

```bash
docker compose cp prism:/var/lib/prism/backups ./prism-backups
```

`prism restore --from DIR [--force]` refuses to touch a live instance, so stop
the container first and run the same binary through Compose:

```bash
docker compose stop prism
docker compose run --rm prism /usr/local/bin/prism restore --from /var/lib/prism/backups/<timestamp>
docker compose up -d
```

See [backup-restore.md](backup-restore.md) for retention and restore details.
Migrating from Resin uses the same pattern: mount the upstream data directories
read-only and import them in a one-off container —
`docker compose run --rm prism /usr/local/bin/prism import-resin --from-state
/var/lib/resin --from-cache /var/cache/resin`
([MIGRATION_FROM_RESIN.md](MIGRATION_FROM_RESIN.md) has the complete example).

### Not built or verified in this environment

`Dockerfile`, `.github/Dockerfile.release`, `docker/entrypoint.sh` and
`docker-compose.yml.example` are part of the repository, but the environment this
guide was written in has no Docker daemon: **no image was built and no container
was started or tested.** Build it once yourself before relying on it
(`docker compose build`, or `docker build -t prism .`) and confirm that the
container reaches `(healthy)`.

## Configuration

All settings come from `<deploy-dir>/.env` (a real process environment variable
takes precedence over the file) or from the environment of the service. The
relevant deployment keys:

| Variable | Default | Purpose |
|----------|---------|---------|
| `PRISM_ADMIN_TOKEN` | — (required) | Bearer token for `/api/v1/*`; generated by `prism init` |
| `PRISM_PROXY_TOKEN` | — (required) | Token in proxy credentials and reverse-proxy paths; generated by `prism init` |
| `PRISM_LISTEN_ADDRESS` | `127.0.0.1` | Primary listener host (UI, API, HTTP/SOCKS5 proxy, reverse proxy) |
| `PRISM_PORT` | `2260` | Primary listener port |
| `PRISM_ADMIN_LISTEN` | *(disabled)* | Optional `host:port` management-only listener serving `/ui`, `/api` and `/healthz` |
| `PRISM_STATE_DIR` | `./.local/state` | `state.db` (and `intel.db` when it exists) |
| `PRISM_CACHE_DIR` | `./.local/cache` | `cache.db` and other rebuildable data |
| `PRISM_LOG_DIR` | `./.local/logs` | Rolling logs and request-log shards |

Relative paths resolve against the working directory of the process, which for
the unit is the deployment directory. `PRISM_ADMIN_TOKEN` and
`PRISM_PROXY_TOKEN` must both be non-empty (at least 16 characters with the
default `PRISM_ENFORCE_STRONG_TOKENS=true`), or Prism refuses to start;
`./bin/prism check-config` reports the effective values and masks every secret.
A wider list of variables is documented in `README.md` and `.env.example`.

## Service management

```bash
sudo systemctl start prism        # start
sudo systemctl stop prism         # stop
sudo systemctl restart prism      # restart
sudo systemctl status prism       # status
sudo systemctl enable prism       # start on boot
sudo systemctl disable prism      # do not start on boot
```

Logs:

```bash
sudo journalctl -u prism -f       # follow
sudo journalctl -u prism -n 100   # last 100 lines
sudo journalctl -u prism -b       # since boot
```

While the service runs it also writes its pid to
`<PRISM_STATE_DIR>/prism.pid`; `prism restore` uses that file (and the listen
port) to detect a live instance.

## Accessing the interface

- Web UI: `http://<PRISM_LISTEN_ADDRESS>:<PRISM_PORT>/ui/` — with the defaults
  `http://127.0.0.1:2260/ui/`. If the frontend was not built the UI answers
  `503`; build it with `make web` (or `make build`).
- REST API: `http://<PRISM_LISTEN_ADDRESS>:<PRISM_PORT>/api/v1/`, always with
  `Authorization: Bearer <PRISM_ADMIN_TOKEN>`.
- Liveness: `GET /healthz` needs no token.

The tokens are stored in `<deploy-dir>/.env` (mode `0600`, owned by the `prism`
service user). There is no `<state-dir>/.admin_token` file in this version. To
use the token from a shell:

```bash
TOKEN=$(sudo grep -m1 '^PRISM_ADMIN_TOKEN=' /opt/prism/.env | cut -d= -f2-)
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:2260/api/v1/system/info
```

Do not paste that token into shared logs or bug reports.

## Post-deployment steps

All calls below need the admin token header; platform and subscription IDs are
generated by the server.

```bash
TOKEN=$(sudo grep -m1 '^PRISM_ADMIN_TOKEN=' /opt/prism/.env | cut -d= -f2-)
BASE=http://127.0.0.1:2260
```

### 1. Create a platform

```bash
curl -X POST "$BASE/api/v1/platforms" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"My Platform","sticky_ttl":"1h","scheduled_rotation_enabled":true,"scheduled_rotation_interval":"1h"}'
```

### 2. Add nodes through a subscription

Nodes are never created directly — there is no `POST /api/v1/nodes`. Add a
subscription (remote URL or pasted content), then refresh it:

```bash
curl -X POST "$BASE/api/v1/subscriptions" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"my-sub","source_type":"remote","url":"https://example.com/sub","enabled":true}'

curl -X POST "$BASE/api/v1/subscriptions/<id>/actions/refresh" \
  -H "Authorization: Bearer $TOKEN"

curl -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/nodes?limit=20"
```

### 3. Add an inbound endpoint

```bash
curl -X POST "$BASE/api/v1/endpoints" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"port":2261,"enabled":true,"allow_proxy":true,"allow_http_forward":true,"allow_socks5":true,"allow_http_reverse":false,"allow_management":false}'
```

### 4. Schedule backups

```cron
# Every day at 02:00, keep the newest 7 snapshots
0 2 * * * /opt/prism/scripts/prism-backup.sh backup --out /var/backups/prism --keep 7 >> /var/log/prism-backup.log 2>&1
```

Backups are taken online (`prism backup` uses `VACUUM INTO`), so this needs no
maintenance window. See [backup-restore.md](backup-restore.md) for restore and
retention details.

## Monitoring

```bash
# liveness, no token
curl http://127.0.0.1:2260/healthz

# service and build information
curl -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/system/info"
curl -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/system/config/env"

# metrics: realtime, history and snapshots
curl -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/metrics/realtime/throughput"
curl -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/metrics/history/traffic"
curl -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/metrics/snapshots/node-pool"
```

The full route table is `internal/api/server.go`; `README.md` lists the most
used endpoints. There is **no Prometheus exporter** in this version — no
`/metrics` and no `/api/v1/metrics/prometheus` endpoint — and no OpenAPI document
or `/ui/docs` page. Scrape the JSON endpoints above or use `journalctl` for
service-level monitoring.

### Metrics retention

Three environment variables bound how much metric data Prism keeps, and all three
are published by `GET /api/v1/system/config/env` and shown in the WebUI's system
configuration page:

| Variable | Default | Bounds |
| --- | --- | --- |
| `PRISM_METRIC_THROUGHPUT_RETENTION_SECONDS` | 3600 | the realtime throughput ring, and the `metric_traffic_bucket` / `metric_request_bucket` / `metric_access_latency_bucket` / `metric_probe_bucket` / `metric_node_pool_bucket` history |
| `PRISM_METRIC_CONNECTIONS_RETENTION_SECONDS` | 18000 | the realtime connections ring (connections have no persisted table) |
| `PRISM_METRIC_LEASES_RETENTION_SECONDS` | 18000 | the realtime leases ring, and `metric_lease_lifetime_bucket` |

`metrics.db` is pruned in bounded batches every 5 minutes, so a pass never holds
the SQLite write lock for long. The history and realtime endpoints clamp `from`
to the matching window, so a request can never scan more history than the
retention keeps: asking for 24 hours while
`PRISM_METRIC_THROUGHPUT_RETENTION_SECONDS=3600` returns the retained hour, not
an error. Raise the value if you want a longer dashboard history — the values
cost memory for the rings and disk for `metrics.db`.

## Reverse proxy and TLS

Prism itself speaks plain HTTP; there is no TLS listener and no HSTS. Keep
`PRISM_LISTEN_ADDRESS` on loopback and terminate TLS in a reverse proxy in front
of it (also documented in [SECURITY.md](SECURITY.md)).

Nginx:

```nginx
server {
    listen 443 ssl http2;
    server_name prism.example.com;

    ssl_certificate     /etc/ssl/certs/prism.crt;
    ssl_certificate_key /etc/ssl/private/prism.key;

    location / {
        proxy_pass http://127.0.0.1:2260;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Caddy:

```caddyfile
prism.example.com {
    reverse_proxy 127.0.0.1:2260
}
```

If the proxy is on another host and you want the API to honour
`X-Forwarded-For`, add its address to `PRISM_TRUSTED_PROXIES`; otherwise the
client IP of the authentication failure limiter is the proxy address.

Firewall example:

```bash
sudo ufw allow from 10.0.0.0/8 to any port 2260   # or only 443 when a proxy terminates TLS
```

## Updating Prism

```bash
# 1. take a snapshot
/opt/prism/scripts/prism-backup.sh backup --out /var/backups/prism --keep 0

# 2. stop the service
sudo systemctl stop prism

# 3. update the sources and rebuild the binary
cd /opt/prism
git pull
make build

# 4. start the service
sudo systemctl start prism
sudo systemctl status prism
```

`deploy.sh` refuses to configure a directory without `bin/prism`, so the binary
must be rebuilt in place. Re-running `sudo ./scripts/deploy.sh` after a change to
the data directories regenerates the unit; it keeps the existing `.env` and only
rewrites the keys you pass as options.

## Troubleshooting

### Port already in use

```bash
ss -ltnp 'sport = :2260'          # or: lsof -i :2260 / netstat -ltnp | grep 2260
systemctl restart prism           # when the Prism unit owns it
sudo ./scripts/deploy.sh --port 2261
```

The script never kills another process: free the port yourself first.

### `Prism binary not found` / `Prism binary is not executable`

```bash
make build            # or make backend
ls -l bin/prism
```

### Installing the unit fails

```bash
sudo ./scripts/deploy.sh          # the unit install needs root
./scripts/deploy.sh --no-service  # prepares .env and data dirs only
```

### Service will not start

```bash
sudo journalctl -u prism -n 50
cd /opt/prism && ./bin/prism check-config   # validates .env, masks secrets
cat /etc/systemd/system/prism.service       # check paths and user
sudo systemctl daemon-reload
sudo systemctl restart prism
```

A common cause is a `.env` whose data directories are not in the unit's
`ReadWritePaths`, or a missing/invalid token (`./bin/prism check-config` exits 1
and prints the validation errors).

### Web UI or API unreachable

```bash
systemctl status prism
ss -ltnp 'sport = :2260'                       # is the port listening?
curl -v http://127.0.0.1:2260/healthz
curl -v http://127.0.0.1:2260/ui/              # 503 = frontend not built
```

The defaults bind to `127.0.0.1` only. To reach the instance from another host,
either use `--listen 0.0.0.0` (and firewall the port) or put a reverse proxy in
front of it.

### Authentication failed

```bash
TOKEN=$(sudo grep -m1 '^PRISM_ADMIN_TOKEN=' /opt/prism/.env | cut -d= -f2-)   # do not share the value
curl -v -H "Authorization: Bearer $TOKEN" http://127.0.0.1:2260/api/v1/system/info
```

`401` means the token is wrong or missing; repeated failures from one IP are
answered with `429` plus `Retry-After` for five minutes (see
[SECURITY.md](SECURITY.md)). Change the tokens with
`./bin/prism init --force` and a service restart.

## Uninstalling

```bash
sudo systemctl stop prism
sudo systemctl disable prism
sudo rm /etc/systemd/system/prism.service
sudo systemctl daemon-reload
```

Then remove the deployment directory. Keep the data directories when they live
outside it (absolute `PRISM_STATE_DIR` / `PRISM_CACHE_DIR` / `PRISM_LOG_DIR`
paths), and keep a backup if you may come back:

```bash
mv /opt/prism/backups ~/prism-backups
# PRISM_STATE_DIR/CACHE_DIR/LOG_DIR are separate directories when they are absolute
rm -rf /opt/prism
```

The `prism` system user (`useradd --system` by the deploy script) is left in
place; remove it with `sudo userdel prism` if it is no longer needed.

## Not implemented in this version

- TLS listener and HSTS — terminate TLS in a reverse proxy.
- Container manifests — `Dockerfile`, `docker/entrypoint.sh`,
  `docker-compose.yml.example` and `.github/Dockerfile.release` ship with the
  repository, but they were never built or run, because the environment this
  guide was written in has no Docker daemon. See [Docker](#docker) for the caveats
  and build the image once yourself before using it.
- Prometheus exporter (`/metrics`) — scrape the JSON API instead.
- OpenAPI document and the `/ui/docs` page; the route table is
  `internal/api/server.go` and the main endpoints are listed in `README.md`.

`prism import-resin` is **not** on this list: it is implemented and imports an
upstream Resin `state.db`/`cache.db` (plus the optional request-log databases)
with
`prism import-resin --from-state DIR --from-cache DIR [--from-log DIR] [--force]`;
[MIGRATION_FROM_RESIN.md](MIGRATION_FROM_RESIN.md) is the authoritative document
for it — usage, the deviation list and a container example.
