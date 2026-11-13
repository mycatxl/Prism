# Recovery of a single Prism deployment

Use these procedures on a protected local console with filesystem-owner access. Protect the deployment directory (0700), database/pepper/settings files and backups (0600). Never put credentials in incident reports, shell history, command arguments or shared logs. Use `[REDACTED]` in reports. Prism holds no wallet private keys; recovery never imports one.

Record the incident reference, artifact digest and affected paths before starting. These commands operate on the chosen installation. Stop its service and block ingress before offline changes; do not stop a different deployment. Preserve a backup first. `init --force` is not recovery and must not be run on customer state.

## Lost session, key, password or second factor

With the password and current second factor, sign in normally. Under Account Security revoke the lost session. Under My Keys revoke a lost proxy key, then issue a replacement; revocation must be checked with the old credential. Do not print either credential. If only an automation key is lost, use a console session to replace it.

If the sole administrator cannot authenticate (lost password/factor or future TOTP watermark), use the following offline path. It creates a **new** administrator and an audit record atomically. It preserves every existing password/factor/replay watermark, customer subscription, balance, usage row, privacy fence and key. Access to the private state directory is the recovery authority; secure it separately from browser credentials. The command refuses to run while Prism holds its directory lock. There is no remote recovery endpoint.

1. Stop this installation, block ingress, take the backup described below and verify time synchronization.
2. Choose an unused administrator name. Supply a new random password through protected stdin (at least 12 characters), never as a CLI argument. On an interactive Bash console:

   ```bash
   read -r -s -p 'New recovery password: ' PRISM_RECOVERY_PASSWORD
   printf '\n'
   printf '%s\n' "$PRISM_RECOVERY_PASSWORD" | /path/to/prism recover-admin \
     --state-dir /srv/prism/state --cache-dir /srv/prism/cache \
     --username recovery-incident --reason INC-1234 \
     --password-stdin --confirm-offline
   unset PRISM_RECOVERY_PASSWORD
   ```

   Use actual paths and an incident reference without credentials. Disable shell tracing before entering secrets. A protected password manager may supply stdin instead. Success prints only that recovery was created and audited. No existing administrator is overwritten.
3. Start this installation on a local-only management boundary. Sign in as the new administrator, enroll a fresh TOTP factor in Account Security, then sign in again. Protect this independent factor separately. Disable the lost administrator and revoke its lost keys/sessions using the console. Disabling the account does not erase its replay watermark. Inspect the `offline.recover_admin` audit event. Keep the incident and operator identity in the external incident record.
4. Verify a customer active key still authenticates and a revoked key still fails; compare balances, subscription usage and deletion records to the backup. Only restore ingress under the owner's release procedure.

## Wrong time and the replay watermark

Compare the host's UTC time and the authenticator's time with a trusted time source. On a systemd host, inspect `timedatectl status` and the configured NTP service (for example `chronyc tracking` or `timedatectl timesync-status`). Restore synchronization using the host's approved NTP procedure. Check the VM/container host too. Do not change an unrelated host's clock for diagnosis.

`TOTP_INVALID` can result from skew; correcting time and using a fresh code should restore access. `TOTP_ALREADY_USED` has a different cause: Prism stores the highest accepted step for that factor. If both clocks previously ran in the future, moving them back does **not** make those steps reusable. Wait until trusted time passes the consumed step and use a fresh code, or use the independent recovery administrator/offline procedure above. **Do not clear the watermark, disable replay protection or advance clocks to log in.** A new administrator has an independent factor; the old factor remains replay-protected. The login error code and audit timestamps help diagnose the incident without revealing the TOTP secret.

## Backup and restore, including settings and pepper

Stop traffic and then stop Prism for a coordinated recovery backup. `prism backup` can snapshot databases online, but separate databases and in-flight accounting are easiest to reconcile while stopped. Set the same deployment environment or run from the protected directory containing its `.env`:

```bash
/path/to/prism backup --out /protected/backups/incident-1234
```

The manifest contains hashes for state.db, cache.db, intel.db and `pepper` (the exact filename is listed by the command). Preserve all listed files. **Separately copy the deployment `.env`, service/container configuration, reverse-proxy configuration and external secret-store references** to protected storage. The CLI intentionally does not back up `.env`. Never paste `check-config` secrets or settings into reports. If `PRISM_KEY_PEPPER` overrides the file, securely retain that exact value too. Losing or changing the pepper invalidates existing keys and encrypted second factors; generating a new pepper is not a restore.

For restore, create a new private directory on the target machine, securely restore the deployment settings, and change only the intended storage/listener paths. Supply the original `PRISM_PROXY_TOKEN` and any pepper override through the protected environment. Do not run `init`. With no Prism process using the target:

```bash
/path/to/prism restore --from /protected/backups/incident-1234
/path/to/prism check-config
```

The restore verifies manifest hashes and refuses a running target. Start it with ingress closed. Validate login, active and revoked key behavior, balance equals ledger, current period usage, hourly history and privacy deletion fences. Restore all three databases together: cache/intel tombstones are part of privacy deletion. Replaying a deleted source must not resurrect its nodes. Unresolved byte reservations also survive backup; handle them below. A new hostname/path is a local rehearsal; an independent physical restore host remains owner acceptance.

## Crash or storage outage during metering

Prism commits a byte reservation with SQLite FULL synchronization **before** handing bytes to a peer. Completion records actual bytes and charges the original subscription period in one transaction, then removes the reservation. Request/error counters still flush asynchronously; they do not duplicate settled bytes. Durable writes are serialized with counter recovery and use SQLite's single writer; slow or blocked storage can delay other users too. Measure throughput on the owner's hardware with this accounting path before choosing capacity limits. A failed durable write refuses service. Failed settlements are retried while that process is alive, and affected access remains closed. Monitor storage errors, free space and accounting errors; `/healthz` is liveness only.

A crash between reservation and completion has an unknown byte outcome. The next start emits an unresolved-reservations diagnostic and refuses affected users with HTTP 503 `ACCOUNTING_UNAVAILABLE`; renewals or new keys cannot bypass this hold. Do not infer that a missing completion means zero bytes. Fix storage first. Stop the installation, back up, then list liabilities without secrets:

A writer panic before returning its accepted-byte count has the same unknown outcome. Prism retains that reservation and closes affected access immediately, even if the process recovers the panic. An interrupted forward upgrade handshake also retains its header reservation once writing has started: a write/flush error or panic does not establish how many header bytes reached the client. A rejection before writing refunds the unattempted handshake. Ordinary returned short writes still settle their known byte counts. Settlement failures publish the access hold before releasing the shared admission lock; a retry only reopens access after successful reconciliation. Neither rule estimates unknown bytes or removes the need for independent recovery evidence.

```bash
/path/to/prism usage-recovery --state-dir /srv/prism/state --cache-dir /srv/prism/cache
```

For each reservation, use independently retained peer/transport evidence to establish actual accepted bytes in that direction. If the exact outcome is unknown, **leave access closed** and escalate the incident; the command does not invent usage or charge a worst-case guess. When verified, settle the exact integer, no greater than reserved:

```bash
/path/to/prism usage-recovery --state-dir /srv/prism/state --cache-dir /srv/prism/cache \
  --reservation RESERVATION_ID --actual-bytes VERIFIED_INTEGER \
  --reason INC-1234-peer-evidence --confirm-offline
```

Settlement and `offline.reconcile_usage` audit insertion are atomic. Repeating a settled reservation does not debit again. No balance entry is added. Old-period bytes remain in history and do not debit a new period. Restart after reconciliation, compare byte counters/history and verify access. Never clear reservations with SQLite or restore an older backup to give spent quota back.

## Certificate expiry and renewal

Prism expects TLS termination at the owner's reverse proxy. Keep `PRISM_TRUSTED_PROXIES` restricted to that proxy. Monitor certificate expiry independently of `/healthz`. Check the served hostname, chain, expiry and UTC clock; a valid renewed certificate can appear expired with a wrong clock.

Use the certificate issuer's approved renewal process on the actual TLS terminator (for example an already configured ACME renewal job). Validate its configuration before reload, install the full chain for the same hostname, reload that terminator, and confirm a normal verifying client can sign in over HTTPS. Check secure cookies, callback reachability and the proxy route. Do not disable certificate verification or use HTTP as an expiry workaround. Prism itself usually needs no restart for a terminator certificate renewal. Upstream measurement targets also require valid certificates; after repairing one, rerun its normal probe. Real public-domain renewal and callback acceptance require the owner's domain and deployment authority.
