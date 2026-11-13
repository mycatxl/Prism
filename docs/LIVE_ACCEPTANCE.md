# Live three-protocol acceptance

`python3 -B scripts/live_acceptance.py` prints JSON with `status: NOT_RUN` and exits 2. It does not read credentials, load deployment settings, discover instances or make requests. A passing offline test is not live acceptance. The old `smoke.sh --live-purity` remains a direct-only diagnostic that cannot pass independent-egress acceptance.

The tool tests an **already running, explicitly authorized Prism binary**. It never starts, imports, updates or restarts an instance. Run it on that instance's host, against its explicit loopback HTTP/data listener. SOCKS5 and the reverse credential path stay on the local host; do not place an HTTP reverse proxy or a shared access logger between the tool and Prism. Management reads use a separately supplied administrator session or admin-scope AccessKey. Every data request uses the subscribed tenant's proxy-scope AccessKey.

## Operator inputs

Provision the authorized encrypted upstream, routable platform, active subscribed proxy key and controlled HTTPS target under the owner's normal process. A dedicated single-node platform makes selection deterministic; the receipt rejects any request actually routed to another node. The tool does not create these resources. Use non-secret incident/change and secret-store references in the manifest; references attest the operator's authority and asset inventory, they do not independently authenticate ownership.

The controlled target must terminate valid HTTPS itself and implement `GET /check`: return `observed_address` from the TCP peer, echo the request's random `X-Prism-Privacy-Marker` as `marker`, and set `marker_survived: true`. `cmd/privacy-echo` implements this contract. A fixed response or a proxy-derived peer address is not a valid receipt. This target is the controlled acceptance target, not a requirement to enable product anonymity measurement. If Prism's optional echo measurement is enabled, the tool additionally requires this target to match the configured echo URL and checks all identity-header observations against the existing echo rules. When echo is disabled, the receipt explicitly says `identity_checked: false`.

Prepare a non-secret JSON manifest with **exactly** these fields (replace placeholders):

```json
{
  "schema": 1,
  "prism_url": "http://127.0.0.1:PORT",
  "platform": "ACCEPTANCE_PLATFORM",
  "platform_id": "PLATFORM_UUID",
  "node_hash": "NODE_HASH_32_HEX",
  "git_commit": "EXPECTED_BUILD_COMMIT",
  "expected_egress": "AUTHORIZED_EGRESS_IPV4",
  "target_url": "https://AUTHORIZED_TARGET/check",
  "own_cidrs": ["COMPLETE_OPERATOR_ASSET_CIDR"],
  "authority_ref": "change:AUTHORIZED_RUN",
  "upstream_ref": "inventory:AUTHORIZED_UPSTREAM",
  "target_ref": "inventory:CONTROLLED_TARGET",
  "asset_inventory_ref": "inventory:CURRENT_OWN_ASSETS",
  "proxy_secret_ref": "vault:PROXY_KEY_ENTRY",
  "admin_secret_ref": "vault:ADMIN_SESSION_ENTRY"
}
```

`git_commit` must exactly match the non-secret `/api/v1/system/info` build stamp (7–40 hex digits). Record the deployed binary SHA-256 and installation attestation separately under `authority_ref`; a server's build stamp is not a cryptographic measurement of its executable. The before/after instance start time must also match. Platform names supported by this client use ASCII letters, digits, `_` and `-`; the product's other platform naming forms can be tested after preparing a suitable acceptance platform. IPv4 is required because the frozen IPPure scoring contract does not score IPv6; this tool does not claim dual-stack acceptance.

Use a protected launcher/secret manager to open **distinct inherited descriptors ≥3**: one contains only the complete proxy AccessKey, another only the administrator session cookie value (or an admin AccessKey). Each may have a trailing newline. Pipes and owner-only regular files are accepted. No shell arguments, environment variables, manifest fields, report fields or shared logs should contain the values. Do not use shell tracing. Python does not promise forensic erasure of process memory.

With descriptors 3 and 4 already supplied by that launcher:

```bash
umask 077
python3 -B scripts/live_acceptance.py --execute --manifest /protected/acceptance.json \
  --proxy-key-fd 3 --admin-session-fd 4 > /protected/acceptance-receipt.json
```

For an existing admin-scope AccessKey, replace `--admin-session-fd 4` with `--admin-key-fd 4`. The tool only reads management APIs, so a session does not need a CSRF token. It does not search for or issue either credential. An expired session or wrong scope fails closed. The complete run has a 180-second deadline (operator may set `--timeout` from 30 to 600 seconds); each network operation is bounded. A retry is a new run with new request identities, not a continuation that combines old successes.

## Evidence and verdict

The client first checks the specified build, node, current own-asset discovery, configured/current/historical asset ranges, positive latency and valid non-null IPPure evidence bound to this node and IPv4. It preserves the product's TLS-verification warning instead of converting it into a new acceptance gate. The runtime trace URL must be the shipped public URL in live mode.

For **each** of HTTP CONNECT/HTTPS, authenticated SOCKS5 CONNECT/HTTPS and reverse HTTPS forwarding, it makes three requests through Prism:

1. Public Cloudflare trace, validating the observed IP.
2. Public `https://my.ippure.com/v1/info`, requiring the same IPv4 and an integer `fraudScore` in 0–100. A real zero is valid; absent/null/boolean/out-of-range values fail. `purity = 100 - fraudScore` is recorded without changing any platform threshold.
3. The controlled target, requiring the same peer IP and this request's unpredictable challenge.

Each request has a fresh account identifier and closes its connection. Its Prism request-log row must match the time window, account, platform ID, protocol, target authority, specified node and exit, with `net_ok` and no Prism error. Missing, duplicate, stale or replayed rows fail. The cached log exit alone is insufficient: it must match the received trace/provider/target observations. The final snapshot rechecks expiry and the same instance, node and own-asset guard. Public responses are fetched during this run; a pre-existing review API success or cached provider score cannot substitute for them. HTTP redirects and environment proxies are never followed; HTTP/SOCKS origin TLS uses verifying system trust without environment CA overrides. Reverse-origin TLS is performed by Prism under its existing route and privacy policy.

The JSON receipt contains only allowlisted identifiers, times, scores, hashes and classifications, never credentials, raw URLs, request/response bodies, cookie values or upstream exception text. Response SHA-256 values associate the observed data without retaining potentially sensitive vendor payloads. Keep the receipt and the external authorization/asset/build records together. It demonstrates the listed paths and time window, not arbitrary destinations, independent operator identity, all address families or a production capacity guarantee.

| Exit | Status | Meaning |
| --- | --- | --- |
| 0 | `PASS` in `live` mode | All nine requests and their guard/log/evidence checks passed. Only the real three-protocol layer passes. |
| 1 | `FAIL` in `live` mode | An attempted run failed. Completed protocol rows remain visible; unattempted rows remain `NOT_RUN`. |
| 2 | `NOT_RUN` | Execution was not explicitly requested, or required configuration/secret input failed before the run. |
| 0/1 | `NOT_RUN` in `offline_fixture` mode | `offline_checks` reports fixture success/failure. Never live evidence. |

Failure codes distinguish missing authority/input, authentication/policy rejection, TLS/transport/timeout, own-asset/discovery, unknown/stale/unbound score, mismatched trace/provider/target, missing challenge, bad log association and instance changes. No failed category falls back to direct traffic or an invented score. `make test-live` checks these failure paths without external network. `make smoke` runs the same CLI through all three entrances of its disposable binary and local TLS node, with nine log associations and separate invalid-key rejection checks for HTTP, SOCKS5 and reverse. Prism returns HTTP 403 with `X-Prism-Error: AUTH_FAILED` for an invalid key; the client classifies this as `AUTH_REJECTED`, while unrelated 403 policy refusals remain failures. The fixture waits for its TLS self-discovery endpoint before starting Prism's one-time asset discovery. Fixture endpoint/CA overrides require explicit `--offline-fixture`; that mode cannot emit live PASS.

Production GO still separately requires current-instance TLS/trusted-proxy/time verification, backup and recovery evidence, measured target-hardware capacity with durable accounting, sellable inventory/provider allowance, and real enabled-payment acceptance. Unknown crash byte outcomes retain the existing offline reconciliation contract. The acceptance tool does not relax or close those conditions.
