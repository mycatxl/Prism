# Prism

[English](README.md) | [中文](README.zh-CN.md)

---

Prism is a single-port proxy router and control plane. It imports proxy nodes
from subscriptions, verifies their real egress, routes client traffic through
per-platform rules with sticky leases, and exposes a management API plus an
embedded web UI on the same listener.

### Features

- **Node pool from subscriptions**: remote URLs and pasted content, parsed from
  share links, Clash, sing-box and Surge formats; deduplicated by node hash.
- **Intelligent routing**: platform rules (regex, region, subscription scope),
  P2C scheduling weighted by measured latency, sticky leases with TTL, and an
  optional scheduled rotation interval per platform.
- **Health and egress verification**: latency probes, passive TLS handshake
  sampling, egress IP/country detection through the node itself, GeoIP database
  updates, circuit breaking and recovery.
- **Single-port ingress**: the primary listener serves the HTTP forward proxy,
  CONNECT, SOCKS5, the URL reverse proxy, the management API and the web UI.
  An optional independent management listener (`PRISM_ADMIN_LISTEN`) serves
  `/ui`, `/api` and `/healthz` only.
- **Metrics and logs**: realtime, historical and snapshot metrics, bounded
  request logs with an SQLite rolling store.
- **Online backup and verified restore**: `prism backup` / `prism restore`.

### Requirements

- Go 1.27 or later (`go.mod` declares `go 1.27.0`).
- Linux with systemd for the service installer (optional).
- Node.js and npm to build the web UI (`make web`).

### Quick start

```bash
# 1. Clone
git clone https://github.com/mycatxl/Prism.git
cd Prism

# 2. Build (web UI + backend). "make backend" skips the UI build.
make build

# 3. Generate .env with fresh admin and proxy tokens (mode 0600)
./bin/prism init

# 4. Install and start the systemd service (creates the prism user,
#    keeps an existing .env, never stops another process)
sudo ./scripts/deploy.sh

# 5. Open the web UI and authenticate with PRISM_ADMIN_TOKEN from .env
#    http://127.0.0.1:2260/ui/
```

`sudo ./scripts/deploy.sh --listen 127.0.0.1 --port 2260 --state-dir /var/lib/prism/state`
rewrites only the matching `PRISM_*` keys after backing the file up.
`--no-service` prepares `.env` and the data directories without installing a
unit, and `--dry-run` prints what would happen without changing anything.
Without a compiled `bin/prism` the script stops and tells you to run
`make build`.

No systemd? Run `./bin/prism run` from the directory that contains `.env`.

Docker instead of systemd:

```bash
cp docker-compose.yml.example docker-compose.yml

# The example passes ${PRISM_ADMIN_TOKEN} and ${PRISM_PROXY_TOKEN} into the
# container and reads them from ./.env — the file `prism init` writes.
./bin/prism init            # or set both tokens in .env by hand

docker compose up -d        # then open http://<host>:2260/ui/
```

The example publishes `2260:2260`, keeps its databases and logs in three named
volumes, and pins a release tag of the image: the moving `latest` tag only
appears once a non-prerelease is published, so a pinned tag is what pulls today.
Requirements, volume paths, the container user, health checks and upgrades are in
the [Docker section of docs/deployment.md](docs/deployment.md#docker).

### Managing a running instance

```bash
sudo systemctl status prism     # state
sudo journalctl -u prism -f     # logs
sudo systemctl restart prism    # restart
```

### Configuration

Configuration comes from `./.env` (loaded first, never overriding the process
environment) or from real environment variables. `./bin/prism check-config`
prints the effective values with every secret masked.

| Variable | Default | Purpose |
|---|---|---|
| `PRISM_ADMIN_TOKEN` | — (required) | Bearer token for `/api/v1/*` |
| `PRISM_PROXY_TOKEN` | — (required) | Token in proxy credentials and reverse-proxy paths |
| `PRISM_AUTH_VERSION` | `v1` | Identity format: `Platform.Account:Token` |
| `PRISM_LISTEN_ADDRESS` | `127.0.0.1` | Primary listener host |
| `PRISM_PORT` | `2260` | Primary listener port (UI, API, proxy, reverse proxy) |
| `PRISM_ADMIN_LISTEN` | *(disabled)* | Optional `host:port` for a management-only listener |
| `PRISM_STATE_DIR` | `./.local/state` | Databases (`state.db`, `intel.db`) |
| `PRISM_CACHE_DIR` | `./.local/cache` | Rebuildable cache (`cache.db`, GeoIP files) |
| `PRISM_LOG_DIR` | `./.local/logs` | Rolling logs |
| `PRISM_API_MAX_BODY_BYTES` | `1048576` | Request body limit for `/api/` |
| `PRISM_PROBE_CONCURRENCY` | `32` | Parallel probe workers |
| `PRISM_PROBE_TIMEOUT` | `15s` | Per-probe timeout |
| `PRISM_DEFAULT_PLATFORM_STICKY_TTL` | `168h` | Default sticky lease lifetime |
| `PRISM_GEOIP_UPDATE_SCHEDULE` | `0 7 * * *` | GeoIP update cron expression |
| `PRISM_RESOURCE_FETCH_MAX_BYTES` | `33554432` | Download ceiling after decompression |
| `PRISM_RESOURCE_FETCH_ALLOW_PRIVATE` | `false` | Let subscription and GeoIP downloads (and their redirects) reach loopback, private or cloud-metadata addresses. Off by default to block SSRF; the check runs on the resolved IP at connect time |
| `PRISM_ENFORCE_STRONG_TOKENS` | `true` | Reject tokens shorter than 16 characters |
| `PRISM_ALLOW_EMPTY_ADMIN_TOKEN` / `PRISM_ALLOW_EMPTY_PROXY_TOKEN` | `false` | Explicitly disable one authentication scope. Every listener — `PRISM_LISTEN_ADDRESS` **and** `PRISM_ADMIN_LISTEN` — must then stay on loopback unless `PRISM_ALLOW_INSECURE_LISTEN=true` |
| `PRISM_QUALITY_ENABLED`, `PRISM_QUALITY_API_KEY`, `PRISM_ABUSEIPDB_API_KEY` | *(off / unset)* | Optional IP-quality providers |
| `PRISM_TRUSTED_PROXIES` | *(empty)* | CIDR list whose `X-Forwarded-For` is trusted for client-IP limiting |
| `PRISM_PROXY_AUTH_FAIL_LIMIT` | `30` | Failed proxy authentications per minute per IP. Covers the HTTP forward proxy and CONNECT (`Proxy-Authorization`), the reverse-proxy path token and the SOCKS5 username/password check; `0` disables proxy-entry limiting. `/api/*` keeps its own limiter |
| `PRISM_DIRECT_DENY_PRIVATE` | `true` | Refuse loopback, private, link-local, CGNAT, reserved and cloud-metadata targets on every local dial path (reverse-proxy bypass, forward HTTP, CONNECT, SOCKS5). On by default; set `false` to allow local direct dials to any address |
| `PRISM_DENY_PRIVATE_NODES` | `true` | Refuse a **node** whose own `server` names loopback, the LAN, a link-local address or a cloud metadata endpoint. A name that is not a literal is resolved, so a public-looking hostname whose DNS answer is private (`127.0.0.1.nip.io`, `sslip.io`, `xip.io`, …) is refused as well; a name that resolves only into a transparent proxy's fake-IP pool (`198.18.0.0/15`) is allowed, because on such a machine that answer says nothing about the target. On by default; set `false` if a deployment deliberately routes through a private node |

### Usage

All management calls need `Authorization: Bearer $PRISM_ADMIN_TOKEN`.

Create a platform (platform IDs are generated by the server):

```bash
curl -X POST http://127.0.0.1:2260/api/v1/platforms \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"My Platform","sticky_ttl":"1h","scheduled_rotation_enabled":true}'
```

Add nodes through a subscription (a node is never created directly):

```bash
curl -X POST http://127.0.0.1:2260/api/v1/subscriptions \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"my-sub","source_type":"remote","url":"https://example.com/sub","enabled":true}'
```

Refresh it and inspect the pool:

```bash
curl -X POST http://127.0.0.1:2260/api/v1/subscriptions/<id>/actions/refresh \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN"
curl -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" "http://127.0.0.1:2260/api/v1/nodes?limit=20"
```

Expose a second inbound port:

```bash
curl -X POST http://127.0.0.1:2260/api/v1/endpoints \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"port":2261,"enabled":true,"allow_proxy":true,"allow_http_forward":true,"allow_socks5":true,"allow_http_reverse":false,"allow_management":false}'
```

Route client traffic (proxy credentials use the layout
`Platform.Account:PRISM_PROXY_TOKEN`, with the account optional):

```bash
# HTTP forward proxy
curl -x "http://Default:$PRISM_PROXY_TOKEN@127.0.0.1:2260" https://example.com/

# SOCKS5
curl --proxy "socks5h://Default:$PRISM_PROXY_TOKEN@127.0.0.1:2260" https://example.com/

# URL reverse proxy: /<token>/<Platform>.<Account>/<http|https>/<host>/<path>
curl "http://127.0.0.1:2260/$PRISM_PROXY_TOKEN/Default/http/example.com/"
```

### What you can do today

These surfaces landed after the first revision of this file. Protocol-level detail, the list of
unsupported types and the known limitations are in [docs/PROTOCOLS.md](docs/PROTOCOLS.md).

**Import a subscription and let the intel batch run.** Every subscription carries an `auto_intel`
flag that defaults to true (the database default, and the value rows created by older revisions are
migrated to), so a refresh queues one intel job for the nodes it imported. It is settable per
subscription with `auto_intel` on `POST /api/v1/subscriptions` and
`PATCH /api/v1/subscriptions/{id}`, is reported back in the subscription response, and has a switch
in the subscription view of the WebUI; the batch is additionally switched off system-wide with
`intel_enabled=false`. With the default
runtime settings (`intel_enabled=true`, `intel_auto_checks=false`) the job runs
`egress → offline evidence → online evidence → via-node lookups → assessment`
(`internal/intel/jobs/jobs.go`), writes into `intel.db`, and resumes at the next step after a
restart. Turning `intel_auto_checks` on adds the unlock checks to the same job.

The `via-node` lookups carry no quota of Prism's own: the request leaves through the tested node, so
the vendor's own per-address limit is the only gate that applies, and a `429` cools down that one node
for at most 15 minutes instead of pausing the whole data source. Concurrency comes from the node pool
(`intel_node_workers`, 100 by default), each worker on a different node, so a large inventory is walked
in parallel rather than at a shared one-request-per-second pace. The pipeline still parks an item
instead of blocking a worker when a gate does close, and a parked item backs off exponentially (30s
doubling to a 6h ceiling, spread by a per-node jitter) rather than retrying at the gate's own
one-second pace. That matters operationally - a stalled job used to hold one of the two
`max_running_jobs` slots forever and block every later job. `docs/INTEL.md` §2.3 and §7.3 have the
numbers.

```bash
curl -X POST http://127.0.0.1:2260/api/v1/subscriptions \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"my-sub","source_type":"remote","url":"https://example.com/sub","enabled":true}'

# manual batch: by subscription, platform, filter or explicit node hashes
curl -X POST http://127.0.0.1:2260/api/v1/intel/jobs \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"kind":"full","scope":{"platform_ids":["<platform-id>"]}}'

# live progress (Server-Sent Events)
curl -N -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  http://127.0.0.1:2260/api/v1/intel/jobs/<job-id>/events
```

Per-node results are on `GET /api/v1/intel/nodes/{hash}`, per-IP evidence on
`GET /api/v1/intel/ip/{ip}`, and `POST /api/v1/intel/jobs/{id}/actions/retry-failed` retries the
failures of a finished job.

**Every drop is explained.** A subscription refresh keeps a parse report: nodes the parser refuses
are grouped by reason with a count and a bounded sample of names. The grouped summary is part of
every subscription response (`parse_report`) and the exact list of the last parse (at most 500
records, plus a `skipped_overflow` count) is on
`GET /api/v1/subscriptions/{id}/parse-report`; the WebUI subscription view shows both. Reasons are
the stable codes from `internal/node/reasons.go` (`ENGINE_NOT_BUILT`, `UNSUPPORTED_PROTOCOL`,
`INVALID`, …) and are listed per protocol in [docs/PROTOCOLS.md](docs/PROTOCOLS.md) §7.

**Purity score.** Purity is `100 - provider risk` (`internal/quality/model.go`, `PurityScore`).
It is a labelled display inversion, not a calibrated probability or a second provider score, and a
node without evidence stays `unknown`/`pending`. Bands: `excellent` (≥95), `clean` (≥90), `fair`
(≥80), `mixed` (≥60), `poor` (<60).

```bash
curl -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  "http://127.0.0.1:2260/api/v1/nodes?purity_band=clean&purity_min=90&limit=50"
```

The same filter vocabulary — `purity_band`, `purity_min`, `purity_max`, `ip_type`, `verdict`,
`confidence_min`, `native`, `asn`, `country`, `checks` — also drives the
platform quality policy, which is fail-closed: a policy that requires a score does not admit a node
without an assessment.


**Data sources and unlock checks.** These are built-in and run automatically; they are not a page you
operate. The API (`GET|PATCH /api/v1/intel/providers`,
`POST /api/v1/intel/providers/{id}/actions/refresh`, `GET|PATCH /api/v1/intel/checks`) reports each
source's enabled/runnable state, key presence, daily budget and offline database age. Offline
databases are not bundled: DB-IP Lite, MaxMind GeoLite2 and IPinfo Lite need one download into
`$PRISM_CACHE_DIR/geo` before they contribute evidence — until then the source answers
`PROVIDER_UNAVAILABLE` (`… database is not installed`) and no ASN/geo evidence is recorded.
Built-in unlock checks: `chatgpt`, `claude`, `gemini`, `google_captcha`, `netflix`, `smtp25`,
`tiktok`, `youtube_premium`.

### Key endpoints

- `GET /healthz` — liveness (no token required).
- `GET /ui/` — web interface; if the frontend was not built it answers `503`.
- `GET /api/v1/system/info|config|config/env`, `PATCH /api/v1/system/config`.
- `GET|POST /api/v1/platforms`, `GET|PATCH|DELETE /api/v1/platforms/{id}`,
  `POST /api/v1/platforms/{id}/actions/reset-to-default`,
  `POST /api/v1/platforms/{id}/actions/rebuild-routable-view`.
- `GET|POST /api/v1/subscriptions`,
  `GET|PATCH|DELETE /api/v1/subscriptions/{id}`,
  `POST /api/v1/subscriptions/{id}/actions/refresh`,
  `POST /api/v1/subscriptions/{id}/actions/cleanup-circuit-open-nodes`.
- `GET /api/v1/nodes`, `GET /api/v1/nodes/{hash}`,
  `POST /api/v1/nodes/{hash}/actions/probe-egress|probe-latency|probe-quality|review-ippure`.
- `GET|POST /api/v1/endpoints`, `GET|PATCH|DELETE /api/v1/endpoints/{id}`.
- `GET|DELETE /api/v1/platforms/{id}/leases`,
  `GET|DELETE /api/v1/platforms/{id}/leases/{account}`,
  `GET /api/v1/platforms/{id}/ip-load`.
- `GET /api/v1/quality/status|assessments|ip/{ip}`,
  `POST /api/v1/quality/ip/{ip}/actions/probe`.
- `GET|PUT|DELETE /api/v1/account-header-rules[/{prefix...}]`,
  `POST /api/v1/account-header-rules:resolve`.
- `GET /api/v1/geoip/status|lookup`, `POST /api/v1/geoip/actions/update-now`.
- `GET /api/v1/request-logs[/{log_id}[/payloads]]`.
- `GET /api/v1/metrics/realtime/{throughput,connections,leases}`,
  `GET /api/v1/metrics/history/{traffic,requests,access-latency,probes,node-pool,lease-lifetime}`,
  `GET /api/v1/metrics/snapshots/{node-pool,platform-node-pool,node-latency-distribution}`.
- `GET|POST /api/v1/intel/jobs`, `GET /api/v1/intel/jobs/{id}[/items|/events]`,
  `POST /api/v1/intel/jobs/{id}/actions/cancel|retry-failed`, `GET /api/v1/intel/status|nodes/{hash}|ip/{ip}`.
- `GET /api/v1/intel/providers`, `PATCH /api/v1/intel/providers/{id}`,
  `POST /api/v1/intel/providers/{id}/actions/refresh|resume`, `GET /api/v1/intel/checks`,
  `PATCH /api/v1/intel/checks/{id}`.
- `GET /api/v1/nodes/export` — admin download of the filtered node pool as `singbox`, `mihomo`,
  `v2rayn`, `uri`, `csv` or `json` (at most 5000 nodes per request; counts in the
  `X-Prism-Export-Exported` / `-Skipped` / `-Truncated` headers). The nodes page has an Export menu.

There is no OpenAPI document or `/ui/docs` page in this repository yet; the
route table lives in `internal/api/server.go`.

### Backup and restore

```bash
# Snapshot state.db, cache.db and intel.db into backups/<timestamp>/
./scripts/prism-backup.sh backup

# Keep only the newest 7 snapshots
./scripts/prism-backup.sh backup --keep 7

# List them
./scripts/prism-backup.sh list

# Restore (stop the service first: restore refuses to touch a live instance)
sudo systemctl stop prism
./scripts/prism-backup.sh restore --from backups/20260924T101500Z --force
sudo systemctl start prism
```

Both subcommands delegate to `./bin/prism backup --out DIR` and
`./bin/prism restore --from DIR`: the backup is a `VACUUM INTO` snapshot of each
database plus a `manifest.json` with size and sha256 per file, and the restore
verifies that manifest before replacing anything. `.env` is never part of a
backup.

### Development

```bash
make backend          # Go binary only -> bin/prism
make build            # web UI + backend
make test             # go test ./cmd/... ./internal/...
make verify           # vet + tests + race + protocol matrix
make protocol-matrix  # table-driven protocol build matrix
bash scripts/smoke.sh # end-to-end smoke test against a throw-away environment
```

### Project structure

```text
Prism/
├── cmd/prism/                # entry point, subcommands, listeners, inbound mux
├── internal/
│   ├── api/                  # REST handlers, middleware; api/web/ holds the React UI
│   ├── buildinfo/            # version, commit, build time, build tags
│   ├── config/               # PRISM_* environment configuration
│   ├── export/               # admin node export (sing-box, mihomo, v2rayN, URI, CSV/JSON)
│   ├── geoip/                # country database and lookup
│   ├── intel/                # providers, checks, purity, evidence store and jobs
│   ├── metrics/              # realtime, history and snapshot metrics
│   ├── model/                # shared domain models (platform, lease, audit, ...)
│   ├── netutil/              # downloader, retry helpers
│   ├── node/                 # node records and hashes
│   ├── outbound/             # sing-box node adapters
│   ├── platform/             # routable platform views and filters
│   ├── probe/                # latency and egress probes
│   ├── proxy/                # HTTP/SOCKS5/reverse entry points and forwarding
│   ├── quality/              # quality evidence and assessment
│   ├── requestlog/           # bounded request log storage
│   ├── routing/              # P2C, leases, scheduled rotation
│   ├── scanloop/             # periodic maintenance loops
│   ├── service/              # control-plane use cases
│   ├── state/                # SQLite state/cache engines and migrations
│   ├── subscription/         # subscription parsers (links, Clash, sing-box, Surge)
│   ├── testutil/             # shared test helpers
│   └── topology/             # subscriptions, nodes and platform relations
├── deploy/                   # backend.env.example for deployments
├── docker/                   # container entrypoint
├── docs/                     # design, deployment, security and API documents
├── scripts/                  # deploy.sh, prism-backup.sh, smoke.sh
├── Dockerfile, docker-compose.yml.example
└── Makefile
```

### Security

Implemented and test-verified controls are documented in
[docs/SECURITY.md](docs/SECURITY.md): admin API authentication with per-IP
failure limiting (`429` plus `Retry-After`, `X-Forwarded-For` honoured only from
`PRISM_TRUSTED_PROXIES`), management-listener isolation, audit logging of
management writes with 90-day retention, verified online backups and restores,
and the `PRISM_DIRECT_DENY_PRIVATE` policy (on by default), which refuses loopback,
private, link-local, CGNAT, reserved and cloud-metadata targets on **every**
local dial path (reverse-proxy bypass, forward HTTP proxy, CONNECT and SOCKS5).
The same document lists the known limitations — there is no TLS listener
(terminate TLS in front of Prism). Proxy-entry failure limiting (30/min/IP),
the direct-target policy and the node-target policy are on by default; turn
them off only on a trusted network.

### Acknowledgements

Thanks to [sing-box](https://github.com/SagerNet/sing-box), the engine under
every outbound, and to Claude, which pair-programmed much of Prism with the
maintainer.

### License

GPL-3.0-or-later — see the `LICENSE` file for the full text. Third-party notices are in
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).

### Contributing

1. Fork the repository.
2. Create a feature branch.
3. Run `make verify` and `bash scripts/smoke.sh` before opening a pull request.
4. Describe behaviour changes in `docs/`.

### Support

- GitHub Issues: https://github.com/mycatxl/Prism/issues
- Documentation: https://github.com/mycatxl/Prism/tree/main/docs
