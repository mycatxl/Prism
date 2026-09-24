# Backup and Restore

Prism backs up and restores its databases through two subcommands of `bin/prism`:

```bash
prism backup  --out DIR              # online snapshot, safe while Prism serves
prism restore --from DIR [--force]   # verified restore, requires a stopped service
```

`scripts/prism-backup.sh` is a thin wrapper over both: it adds one timestamped
backup directory per run, a retention policy (`--keep`) and the `list` / `clean`
helpers. The tar-based flow that packaged a live state directory (`-d`, `-o`,
`-c`, `-b`, `-n`, `*.tar.gz`) no longer exists.

## What a backup contains

`prism backup` snapshots every database with SQLite `VACUUM INTO` over an
independent read-only connection, so a running service is not disturbed:

| File | Read from | Content |
|------|-----------|---------|
| `state.db` | `PRISM_STATE_DIR` (default `./.local/state`) | platforms, subscriptions, endpoints, account header rules, system settings, audit log |
| `intel.db` | `PRISM_STATE_DIR` | included only when the file exists; the intel subsystem that would create it is not implemented yet, so it is normally absent |
| `cache.db` | `PRISM_CACHE_DIR` (default `./.local/cache`) | rebuildable cache |
| `manifest.json` | written by `prism backup` | name, size and sha256 of every file, timestamp and build info |

Properties that always hold:

- the output directory is created with mode `0700` (and re-chmodded to `0700` if
  it already existed); every database and the manifest are written with mode
  `0600`;
- a database that does not exist is reported as `Skipping <name> (not present)`
  and is not an error; when none of the three exists the command fails with
  `backup: no databases found in <state-dir> and <cache-dir>`;
- a file that already exists in the output directory is replaced
  (`Replacing existing <name>`);
- `.env` and every other file are deliberately excluded, so a backup never
  contains the admin or proxy token;
- the rolling request logs (`request_logs-<unix_ms>.db` in `PRISM_LOG_DIR`) are
  not part of a backup; they are observability data, not state.

`manifest.json` looks like this (`build_tags` is omitted when the binary was
built without tags, and `version`/`git_commit`/`build_time` are `dev`/`unknown`
for a plain `go build` that did not use the Makefile):

```json
{
  "version": "dev",
  "git_commit": "dfeda1d",
  "build_time": "2026-09-24T10:15:00Z",
  "build_tags": ["with_quic", "with_grpc"],
  "created_at": "2026-09-24T10:15:00Z",
  "files": [
    { "name": "state.db", "size": 1234567, "sha256": "5d41402abc4b2a76b9719d911017c592..." },
    { "name": "cache.db", "size": 234567, "sha256": "7d793037a0760186574b0282f2f435e7..." }
  ]
}
```

Only `state.db`, `cache.db` and `intel.db` are allowed in the manifest; a restore
rejects any other name.

## Prerequisites

- a compiled binary at `<deploy-dir>/bin/prism` — build it with `make build`
  (web UI + backend) or `make backend` (backend only);
- a `.env` in the working directory of the command (the deployment directory),
  because `prism backup` and `prism restore` load `./.env` first and derive
  `PRISM_STATE_DIR`, `PRISM_CACHE_DIR`, `PRISM_LISTEN_ADDRESS` and `PRISM_PORT`
  from it. Create it with `./bin/prism init` or `sudo ./scripts/deploy.sh`;
- read access to the state and cache directories and write access to the backup
  directory. Under the systemd deployment those directories belong to the `prism`
  user, so run the command as `root` or as that user — for example
  `cd /opt/prism && sudo -u prism ./bin/prism backup --out /var/backups/prism/20260924T101500Z`
  with an output directory the `prism` user can write to.

## Create a backup

Direct subcommand — the output directory is required and is used as given:

```bash
cd /opt/prism
mkdir -p backups
./bin/prism backup --out ./backups/20260924T101500Z
```

Output of a successful run:

```
Wrote /opt/prism/backups/20260924T101500Z/state.db (1234567 bytes, sha256 5d41...)
Wrote /opt/prism/backups/20260924T101500Z/cache.db (234567 bytes, sha256 7d79...)
Skipping intel.db (not present)
Wrote /opt/prism/backups/20260924T101500Z/manifest.json
Backup complete: 2 database(s) written to ./backups/20260924T101500Z
```

With the wrapper, the timestamp is chosen for you
(`<out-dir>/YYYYMMDDThhmmssZ` in UTC) and old backups are pruned in the same run:

```bash
./scripts/prism-backup.sh backup                       # -> <deploy-dir>/backups/<timestamp>
./scripts/prism-backup.sh backup --out /var/backups/prism --keep 7
```

The wrapper refuses to run when `<deploy-dir>/bin/prism` is missing or not
executable, when `<deploy-dir>/.env` does not exist, or when the target directory
already exists. `--dry-run` prints the underlying `prism backup` command without
running it.

## Restore a backup

A restore is a two-step operation, because `prism restore` refuses to touch a
live installation:

```bash
# 1. stop the service
sudo systemctl stop prism

# 2. restore (--force is required to replace existing databases)
cd /opt/prism
./bin/prism restore --from /var/backups/prism/20260924T101500Z --force

# or through the wrapper, which is only a convenience layer:
./scripts/prism-backup.sh restore --from backups/20260924T101500Z --force

# 3. start the service again
sudo systemctl start prism
```

What `prism restore` does, in order:

1. **Refuses while an instance is active.** It reads `PRISM_STATE_DIR/prism.pid`
   and, when that file holds a pid that is still alive, aborts with
   `restore: refusing to run while a Prism instance is active (<pid file> holds
   live pid <n>); stop it first`. It also dials
   `PRISM_LISTEN_ADDRESS:PRISM_PORT` with a 500 ms timeout and refuses when the
   port accepts connections. `--force` never overrides this check.
2. **Verifies the backup before modifying anything.** It parses
   `<from-dir>/manifest.json`, requires at least one entry
   (`restore: manifest lists no files`), and checks the size and sha256 of every
   listed file against the manifest:
   `restore: <name> size mismatch: manifest=<n> actual=<n>` or
   `restore: <name> sha256 mismatch: manifest=<sha> actual=<sha>`.
3. **Requires `--force` to overwrite.** Without it, the command aborts with
   `restore: <path> already exists; pass --force to replace it (existing files
   are renamed to *.pre-restore-<timestamp>)` when a target database is present.
4. **Moves existing files aside, then copies the backup into place.** For
   `state.db`, `intel.db` (into `PRISM_STATE_DIR`) and `cache.db` (into
   `PRISM_CACHE_DIR`) it renames an existing `<file>`, `<file>-wal` and
   `<file>-shm` to `<file>.pre-restore-<timestamp>` and copies the verified file
   with mode `0600`. The directories are recreated with mode `0755` when they do
   not exist.

The manifest is not merged with the current configuration: only the three
databases are restored. `.env` is never part of a backup and must be kept
separately — `prism restore` itself needs a valid `.env` (or `PRISM_ADMIN_TOKEN` /
`PRISM_PROXY_TOKEN` in the process environment), otherwise it stops with a
configuration error before touching anything.

After a successful restore the `*.pre-restore-<timestamp>` files hold the
previous databases. Verify the instance (`curl http://127.0.0.1:2260/healthz`)
and delete the leftovers afterwards if you do not need them.

## Test a restore without touching production

`prism restore` targets the directories of the `.env` it loads, but `./.env` is
loaded only for variables that are not already set in the process environment,
so a scratch restore can be done like this:

```bash
cd /opt/prism
mkdir -p ./state-test ./cache-test

# PRISM_PORT points at a port nothing listens on: without that, the running
# production instance would make the command refuse.
PRISM_STATE_DIR=./state-test PRISM_CACHE_DIR=./cache-test PRISM_PORT=22699 \
  ./bin/prism restore --from ./backups/20260924T101500Z
```

`--force` is only needed when the test directories already contain databases.
Inspect the restored files, then remove the test directories.

## Wrapper commands and options

```
./scripts/prism-backup.sh <backup|restore|list|clean> [options]
```

| Option | Applies to | Description | Default |
|--------|-----------|-------------|---------|
| `--dir <path>` | all | Deployment directory that contains `.env` and `bin/prism` | parent directory of `scripts/` |
| `-o`, `--out`, `--output <path>` | backup, list, clean | Directory holding one `<timestamp>` subdirectory per backup; relative paths resolve against `--dir` | `<deploy-dir>/backups` |
| `-k`, `--keep <n>` | backup, clean | Keep the newest `n` backups; `0` keeps all of them | `10` |
| `-b`, `--from`, `--backup <path>` | restore | Backup directory to restore from (must contain `manifest.json`) | required |
| `-f`, `--force` | restore | Pass `--force` through to `prism restore` | disabled |
| `--dry-run` | backup, restore | Print the `prism backup` / `prism restore` command instead of running it | disabled |
| `-h`, `--help` | — | Print the usage text | — |

`prism backup` and `prism restore` themselves only accept:

```
prism backup  --out DIR            # required
prism restore --from DIR [--force] # DIR is required
```

`prism backup` runs as `(cd <deploy-dir> && ./bin/prism backup --out <target>)`,
so the deployment `.env` is used exactly as the service uses it.

### list

```bash
./scripts/prism-backup.sh list --out /var/backups/prism
```

```
[INFO] Backups in /var/backups/prism:
  1. 20260924T101500Z  (2.4M, created 2026-09-24T10:15:00Z)
  2. 20260923T020000Z  (2.3M, created 2026-09-23T02:00:00Z)
[INFO] Total backups: 2
```

Only directories whose name matches the timestamp pattern
(`[0-9]*T*Z`) are listed and pruned; a warning is printed when the output
directory does not exist.

### clean

```bash
./scripts/prism-backup.sh clean --out /var/backups/prism --keep 5
```

`clean` removes the same set of old timestamped directories that
`backup --keep N` prunes after a successful run.

## Automation

```cron
# Every day at 02:00: snapshot into /var/backups/prism and keep the newest 7
0 2 * * * /opt/prism/scripts/prism-backup.sh backup --out /var/backups/prism --keep 7 >> /var/log/prism-backup.log 2>&1
```

`prism backup` is safe while the service is serving, so no downtime is required
for the snapshot. A restore always requires the service to be stopped.

Before upgrading Prism, changing platform configuration or moving to another
host, take one extra snapshot instead of relying on retention:

```bash
./scripts/prism-backup.sh backup --out /var/backups/prism --keep 0
```

Copy the whole backup directory (databases plus `manifest.json`) and the `.env`
to the new host; both are needed, and `manifest.json` must travel with the
databases or the restore will refuse to run. `prism import-resin` — the tool that
would import an existing Resin `state.db` — is not implemented yet: the
subcommand prints `prism import-resin is not available yet (WP05)` and exits with
status 1.

## Troubleshooting

| Message | Cause and fix |
|---------|----------------|
| `backup: --out DIR is required` | The output directory is mandatory; pass `--out ./backups/<timestamp>`. |
| `backup: no databases found in <state-dir> and <cache-dir>` | `PRISM_STATE_DIR` / `PRISM_CACHE_DIR` point at the wrong directories, or Prism never ran there. Check them with `./bin/prism check-config`. |
| `backup: state and cache directories must be configured` | `PRISM_STATE_DIR` or `PRISM_CACHE_DIR` is empty in the effective configuration. |
| `[ERROR] Backup target already exists: <dir>/<timestamp>` | The wrapper creates one directory per second; wait a second and retry, or use `--out` with a different parent directory. |
| `restore: refusing to run while a Prism instance is active (...)` | A live instance owns `<state-dir>/prism.pid` or answers on `PRISM_LISTEN_ADDRESS:PRISM_PORT`. Stop it (`sudo systemctl stop prism`); `--force` does not bypass this check. |
| `restore: <path> already exists; pass --force to replace it` | Add `--force`; the current databases are renamed to `*.pre-restore-<timestamp>` first. |
| `restore: <name> sha256 mismatch: manifest=... actual=...` | The backup was modified or truncated. Restore a different snapshot. |
| `restore: unknown file "<name>" in manifest` | The directory is not a backup produced by `prism backup`. |
| `restore: read manifest <dir>/manifest.json: open <dir>/manifest.json: no such file or directory` | `--from` does not point at a backup directory (for example, at `<out-dir>` instead of `<out-dir>/<timestamp>`). |
| `[ERROR] Prism binary not found or not executable: <dir>/bin/prism` | Build it with `make build` or `make backend`, or pass the right `--dir`. |
| `[ERROR] No .env in <dir>` | Create one with `./bin/prism init --dir <dir>` or `sudo ./scripts/deploy.sh --dir <dir>`. |
| `[ERROR] Deployment directory does not exist: <dir>` | `--dir` must point at an existing directory. |
| `[ERROR] --keep must be a non-negative integer, got: <value>` | `--keep` takes a number; `0` means "keep everything". |
| `[ERROR] <flag> requires a value` | A wrapper flag was passed without its argument. |
| `Permission denied` | Make the wrapper executable (`chmod +x scripts/prism-backup.sh`) and check that the user can read the databases and write the backup directory (`ls -la "$PRISM_STATE_DIR" "$PRISM_CACHE_DIR"`). |

`prism version` prints the version, git commit, build time and build tags of the
binary; comparing them with the values in a backup's `manifest.json` tells you
which build produced it.
