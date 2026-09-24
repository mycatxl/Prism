# Prism Deployment Guide

This guide covers the supported deployment path: a compiled `bin/prism` managed by
systemd through `scripts/deploy.sh`.

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
- Prometheus exporter (`/metrics`) — scrape the JSON API instead.
- `prism import-resin` — the subcommand exists but exits with
  `prism import-resin is not available yet (WP05)` and status 1.
- OpenAPI document and the `/ui/docs` page; the route table is
  `internal/api/server.go` and the main endpoints are listed in `README.md`.
