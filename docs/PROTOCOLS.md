# Prism Protocol Support Matrix

**中文摘要**：本文件是从代码与测试反推出来的协议支持矩阵。运行时内核只有 sing-box `1.14.2`；mihomo 按 `docs/ENGINE_DECISIONS.md` 的 D-1 被否决，
`with_mihomo` 只保留构建标签接缝。第 2–6 节列出实际可导入并构建的 outbound / endpoint 类型、
可识别的分享链接 scheme 与订阅文件格式，第 7 节列出**不支持**的 Clash/Surge 类型及其原因码，
第 8 节说明解析报告与 `auto_intel` 如何到达 API 与 UI，第 10 节列出仍然存在的限制
（naive/tor/tailscale、离线数据库、上游 sing-box 竞态、缺少离线协议端到端测试）。

This document is the protocol support surface of the current tree. It is derived from
the code and from the tests that exercise it — not from the plan documents, whose tables
are older than the implementation. Where reality and the plan disagree, this file follows
reality and says so explicitly.

How to reproduce every claim here:

```sh
# 1. The live capability surface (admin token required):
curl -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  http://127.0.0.1:2260/api/v1/system/capabilities
# -> {"engines":[…],"build_tags":[…],"share_link_schemes":[…],"file_formats":[…]}
#    internal/api/handler_capabilities.go

# 2. The protocol matrix: parses every documented input, builds it with the real
#    sing-box runtime and dials an unreachable target where the case requires it.
go test -tags "with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy" \
  -run TestProtocolMatrix -count=1 -v ./internal/outbound/

# 3. The version/build-tag stamp a released binary carries:
./bin/prism version
```

Code citations name the file and the symbol. Test citations name the test.

## 1. Engines and build variants

| Engine | Version | Compiled into this tree? | What a user sees otherwise |
|---|---|---|---|
| sing-box | 1.14.2 (`internal/node/capabilities.go`, `SingboxVersion`) | yes, always | — |
| mihomo | 1.19.31 (`MihomoVersion`), declared only | no: `go.mod` has no `github.com/metacubex/mihomo`; the only tagged file is `internal/node/mihomo_built.go` (a `const bool`) | a mihomo node document fails at build time with `ENGINE_NOT_BUILT:<type> requires the with_mihomo build` (`internal/outbound/singbox_runtime.go`, `SingboxRuntime.Build`) |

Decision D-1 (`docs/ENGINE_DECISIONS.md`) rejects mihomo as a runtime kernel. The
consequences a user notices:

- The parser keeps recognising the mihomo-only input types and reports them instead of
  dropping them silently (§7), but no input can produce a working mihomo node.
- Every build path compiles with one tag set (`with_quic with_grpc with_utls with_wireguard
  with_gvisor with_openvpn with_openconnect http2legacy`): the `Makefile` (`TAGS`), the
  root `Dockerfile` (`ARG TAGS`), `.github/workflows/release.yml` (`TAGS`) and
  `.github/Dockerfile.release` (which copies the binary `release.yml` produced, so it inherits
  the same set). `with_mihomo` is passed by none of them, and there is a single release
  variant — no reduced tag set is shipped.
- The capability flag cannot lie even if the tag is reintroduced:
  `internal/node/capabilities.go` reports mihomo as built only when the engine runtime layer
  registered itself (`node.RegisterEngineRuntime`), and only `internal/outbound/singbox_runtime.go`
  registers — `SingboxRuntime.Build` rejects mihomo documents with `ENGINE_NOT_BUILT` and no mihomo
  runtime exists. `internal/outbound` `TestEngineCapabilitiesMatchRuntimeBehaviour` asserts the
  report and the real build behaviour agree for the tags the binary was compiled with (it also
  passes with `-tags …with_mihomo`), and `internal/api`
  `TestSystemCapabilities_NeverClaimsAnUnbuiltEngine` asserts the same on the wire.
  `fallback_types` remains the honest mihomo signal: what mihomo *would* cover, not what this
  binary can do.
- `prism version` prints `BuildTags:` from `-X prism/internal/buildinfo.Tags=…`
  (`internal/buildinfo/buildinfo.go`). All four build paths now set that flag, so a released
  binary prints the same `BuildTags` as `make backend`. `release.yml` and the `Dockerfile`
  convert the space-separated tag list to the comma-separated value the linker needs
  (`TAGS_CSV` / `tr`), because `-ldflags` itself is split on spaces.

`TestProtocolMatrixCapabilities` asserts that every type advertised in
`node.SingboxOutboundTypes()` and `node.EndpointTypes()` really exists in the embedded
sing-box registries, so the lists in §4/§5 cannot drift away from the kernel silently.

## 2. Subscription file formats

`node.FileFormats()` (`internal/node/capabilities.go`) and
`internal/subscription/parser.go` (`parseSubscriptionContent`, `parseJSONSubscription`,
`parseClashYAMLSubscription`, `parseOpenVPNSubscription`, `parseSurgeProxySubscription`,
`parseURILineSubscription`):

| Format | How it is supplied | Recognition |
|---|---|---|
| `singbox-json` | `{"outbounds":[…]}` and/or `{"endpoints":[…]}` | object with either key; endpoints and detour chains are resolved over the whole document |
| `clash-json` | `{"proxies":[…]}` | `proxies` array, Clash field names |
| `clash-yaml` | `proxies:` / `Proxy:` / `proxy:` list | YAML body detected by `looksLikeClashYAML` |
| `surge` | `[Proxy]` section lines, `[WireGuard <name>]` sections | line-based; `ss, vmess, vmess-aead, trojan, socks5, http, https, vless, wireguard/wg, hysteria, hysteria2/hy2, tuic, ssh, snell` |
| `uri-lines` | one share link per line, `#` comments allowed | see §3 |
| `base64` | the whole payload base64-wrapped | tried only after the raw payload was not recognised (`tryDecodeBase64ToText`) |
| `plain-proxy-lines` | `1.2.3.4:8080` or `1.2.3.4:8080:user:pass` | imported as an `http` outbound (`parseHTTPProxyIPPort[UserPass]`) |
| `ovpn` | a raw OpenVPN client profile | `node.LooksLikeOpenVPN`; credentials must be inline or come from the bundle |
| `prism-openvpn-bundle` | JSON `{"prism_openvpn_bundle":1,"profiles":[{"name":…,"ovpn":…,"username":…,"password":…}]}` | bundle version 1, profile count bounded (`internal/subscription/openvpn.go`) |

Limits and errors:

- Whole payload limited to 32 MiB (`MaxSubscriptionBytes`); over it the subscription fails
  with `subscription: input exceeds … bytes; split large subscriptions into smaller sources`.
- A Clash YAML body is scanned once before it reaches the YAML decoder
  (`internal/subscription/yaml_guard.go`, `scanYAMLLimits`). Two parser-side limits apply, and
  the scan stops at the first breach:

  - nesting depth ≤ 100 (`MaxYAMLNestingDepth`) — a real Clash/sing-box document needs ~10
    levels, and the guard exists so a nesting bomb is refused with a reason instead of burning a
    deep recursive parse;
  - at most 20000 keys in one mapping (`MaxYAMLMappingKeys`) — yaml.v3's decoder checks every new
    key against the keys it already decoded, so one mapping with n keys costs O(n²) time: 50000
    keys ≈ 4.6 s, 100000 ≈ 24 s, 200000 ≈ 131 s of CPU for a body of a few megabytes.

  A body that crosses either limit fails with
  `subscription: clash yaml nesting depth exceeds 100 …` or
  `subscription: a clash yaml mapping holds more than 20000 keys …`, and the parse report carries
  one skip entry with reason `DEPTH_EXCEEDED` or `COMPLEXITY_EXCEEDED` and a detail naming the
  limit, which is what the subscription API then shows. The limits sit behind the 32 MiB cap and
  do not depend on `PRISM_RESOURCE_FETCH_MAX_BYTES` (raising that ceiling does not lift them).
  The JSON path needs no equivalent guard: `encoding/json` caps structural nesting at 10000 and
  answers with `exceeded max depth` instead of recursing, and it stays linear for a mapping with
  a very large number of keys.
- A payload from which nothing is recognised fails with
  `subscription: unsupported format or no supported nodes found`.
- An OpenVPN profile is refused with reason `INVALID` and a detail such as
  `dev tap: only tun devices are supported`, `ca: file path is not supported; inline the
  material as <ca>…</ca>`, `credentials required`, or
  `tls mode requires an inline <ca> block or a peer-fingerprint`
  (`internal/node/openvpn.go`, `openVPNErrorf` call sites; matrix cases `ovpn-reject-dev-tap`,
  `ovpn-reject-file-path`, `ovpn-reject-missing-credentials`).

## 3. Share-link schemes

`node.ShareLinkSchemes()` lists the schemes the parser recognises; the mapping to a node
type is in `parseURILineSubscription` and `internal/subscription/share_links.go`.

| Scheme | Becomes | Notes |
|---|---|---|
| `vmess://`, `vmess1://` | `vmess` | standard, Shadowrocket and Quantumult payloads |
| `vless://` | `vless` | Reality/Vision/ws/grpc/httpupgrade; **xhttp/splithttp and non-`none` `encryption` are refused** (§7) |
| `trojan://` | `trojan` | ws/grpc/httpupgrade accepted |
| `ss://` | `shadowsocks` | AEAD, SS-2022, SIP002 with `plugin=obfs-local` / `v2ray-plugin` (matrix cases `ss-sip002-*`); a `shadow-tls` plugin is only turned into a chain on the Clash path (§6) |
| `ssd://` | several `shadowsocks` | base64 ShadowSocksDroid document (`parseSSDURI`) |
| `hysteria://` | `hysteria` | v1 URI |
| `hysteria2://`, `hy2://` | `hysteria2` | salamander obfs, `mport` port hopping |
| `tuic://` | `tuic` | congestion control, UDP relay mode, ALPN |
| `anytls://` | `anytls` | uTLS fingerprint |
| `ssh://` | `ssh` | user/password, TCP port |
| `wireguard://`, `wg://` | `wireguard` **endpoint** | `parseWireGuardShareURI` |
| `socks://`, `socks5://`, `socks5h://` | `socks` | falls back to `parseProxyURI` |
| `http://`, `https://` | `http` | `https://` enables TLS; `https://t.me/…` is treated as a Telegram link |
| `tg://socks`, `tg://http`, `https://t.me/socks`, `https://t.me/http`, `https://t.me/https` | `socks` / `http` | Telegram proxy links |
| `netch://` | `vmess` / `socks` / `http` / `trojan` | Netch share links (`parseNetchURI`) |
| `ssr://` | — | line is recognised and reported: `ENGINE_NOT_BUILT`, detail `ssr` (`deferredShareLinkSchemes`) |
| `mierus://` | — | `ENGINE_NOT_BUILT`, detail `mieru` |

A recognised scheme whose payload cannot be converted produces reason `INVALID` with
`unparsable share link` / `unparsable vless share link` / `unparsable hysteria share link`
(`shareLinkSkip`). Ports and query keys are allow-listed (`parseRequiredURIPort`,
`hasOnlyAllowedQueryKeys`), so a link with an unexpected shape is reported, not guessed.

## 4. sing-box outbound types that are imported and built

Advertised list: `internal/node/capabilities.go`, `SingboxOutboundTypes()`.
Build proof: `internal/outbound/protocol_matrix_test.go`, `TestProtocolMatrix` (case names in
brackets). Every row below builds successfully today.

| Type | How a user supplies it | Matrix case |
|---|---|---|
| `shadowsocks` | `ss://` link, Clash `ss`, Surge `ss`, sing-box JSON | `ss-aead`, `ss-2022-*`, `ss-sip002-obfs-plugin`, `ss-sip002-v2ray-plugin` |
| `vmess` | `vmess://`, Clash `vmess`, Surge `vmess`, sing-box JSON | `vmess-ws-tls`, `vmess-grpc` |
| `vless` | `vless://`, Clash `vless`, Surge `vless`, sing-box JSON | `vless-reality-vision`, `vless-ws`, `vless-grpc`, `vless-httpupgrade` |
| `trojan` | `trojan://`, Clash, Surge, sing-box JSON | `trojan-ws` |
| `hysteria` (v1) | `hysteria://`, Clash `hysteria`, Surge, sing-box JSON | `hysteria-v1-uri`, `hysteria-v1-clash` |
| `hysteria2` | `hysteria2://`, `hy2://`, Clash `hysteria2`/`hy2`, Surge | `hysteria2-salamander`, `hysteria2-port-hopping` |
| `tuic` | `tuic://`, Clash `tuic`, Surge, sing-box JSON | `tuic-uri`, `tuic-clash` |
| `anytls` | `anytls://`, Clash `anytls`, sing-box JSON | `anytls-uri`, `anytls-clash` |
| `ssh` | `ssh://`, Clash `ssh`, Surge `ssh` | `ssh-uri`, `ssh-clash` |
| `snell` **v4 only** | Clash `snell` with `version: 4`, Surge `snell, … version=4` | `snell-clash-v4`, `snell-surge-v4` |
| `shadowtls` | sing-box JSON, or as the front hop of a chain (§6) | `chain-singbox-json` (as chain dependency) |
| `socks` | `socks://`/`socks5://`/`socks5h://`, Clash `socks/socks4/socks4a/socks5`, Surge `socks5` | `socks5-uri` |
| `http` | `http://`, `https://`, Clash `http`, Surge `http`/`https`, plain `ip:port[:user:pass]` | `http-uri`, `https-uri`, `plain-ip-port-user-pass` |

Two more outbound types are accepted by the parser but are **not** part of the advertised
list (`internal/subscription/parser.go`, `supportedOutboundTypes`):

- `wireguard` in the legacy outbound shape (form A). It is imported as-is and converted to a
  wireguard **endpoint** at build time so the node hash stays stable
  (`internal/node/envelope.go`, `ParseNodeDoc`; matrix case `wireguard-legacy-outbound`).
- `naive` and `tor` — see §10.1 and §10.2. Neither is built or verified today.

sing-box infrastructure objects (`direct`, `block`, `bridge`, `selector`, `urltest`, `dns`,
`openvpn-server`) are never nodes and are ignored **without** a report entry: the guard in
`internal/subscription/report.go` (`skipDeferredOrUnknown`) checks
`singboxInfrastructureTypes[normalized]` directly. Pinned by `internal/subscription`
`TestSkipDeferredOrUnknown_InfrastructureIsSilent` and
`TestParseWithReport_InfrastructureOutboundsAreNotReported`.

## 5. Endpoint types

| Endpoint type | How a user supplies it | State |
|---|---|---|
| `wireguard` | sing-box `endpoints` array, Clash `wireguard`/`wg` proxy, Surge `[WireGuard x]` + `wireguard` line, `wireguard://`/`wg://` link, or a legacy wireguard outbound | built; five sources covered by `wireguard-legacy-outbound`, `wireguard-endpoint-array`, `wireguard-clash`, `wireguard-surge`, `wireguard-uri` |
| `openvpn-client` | `.ovpn` profile, `prism-openvpn-bundle`, or sing-box `endpoints` entry | built; matrix cases `ovpn-*` (tls-crypt, tls-auth key-direction, multiple random remotes, tcp-client, static key, verify-x509-name/peer-fingerprint, inline credentials, bundle) |
| `openconnect` | sing-box `endpoints` entry | carried verbatim into the runtime (`wrapEndpointObject`); no `.ovpn`-style converter and no matrix case |
| `tailscale` | sing-box `endpoints` entry | **not built** — reported as `ENGINE_NOT_BUILT`, detail `tailscale` (`parseSingboxEndpointItem`); an endpoint envelope fails at build with `Tailscale is not included in this build, rebuild with -tags with_tailscale` (sing-box `include/tailscale_stub.go`; matrix case `tailscale-endpoint-not-built`) |
| `openvpn-server` | sing-box `endpoints` entry | never imported: Prism dials out, it never accepts clients |

`node.EndpointTypes()` advertises exactly `openconnect`, `openvpn-client`, `wireguard`;
`tailscale` is deliberately absent.

## 6. Detour chains (ShadowTLS-style stacks)

Three input shapes are converted into a chain node (form B envelope, `kind: "chain"`):

- sing-box JSON with `"detour": "<tag>"` pointing at another object of the same document;
- Clash `ss` with `plugin: shadow-tls`;
- Clash `dialer-proxy` referencing another proxy of the same document.

Bounds and refusals: at most 3 dependencies (`node.MaxChainDeps`); a cycle is refused with
reason `INVALID`, detail `detour cycle`; an unresolvable reference is reported instead of
being guessed; a dangling `detour` is stripped from a standalone object
(`internal/subscription/chains.go`). The matrix builds the chains and dials an unreachable
target to prove the failure is a transport failure and not `outbound detour not found`
(`assertDetourResolves`; cases `chain-singbox-json`, `chain-clash-shadow-tls-plugin`,
`chain-clash-dialer-proxy`).

## 7. Input types that are not supported

`internal/node/capabilities.go` (`MihomoFallbackTypes`), `internal/subscription/report.go`
(`deferredProtocolTypes`) and `internal/subscription/skips.go` (`clashProxySkip`) define what
Prism recognises but cannot represent. This is the mihomo-rejected list of D-1.

Reason codes (`internal/node/reasons.go`): `UNSUPPORTED_PROTOCOL`, `UNSUPPORTED_FEATURE`,
`ENGINE_NOT_BUILT`, `INVALID`.

| Input | Reason | Detail a user sees |
|---|---|---|
| Clash/Surge `ssr` | `ENGINE_NOT_BUILT` | `ssr` |
| `ssr://` share link | `ENGINE_NOT_BUILT` | `ssr` |
| Clash `mieru`, `mierus://` link | `ENGINE_NOT_BUILT` | `mieru` |
| Clash `masque` / `trusttunnel` / `sudoku` / `shadowquic` / `easytier` / `zerotier` / `gost-relay` | `ENGINE_NOT_BUILT` | the type name |
| VLESS/VMess `network: xhttp` or `splithttp` (Clash) and `type=xhttp` (link) | `ENGINE_NOT_BUILT` | `vless(xhttp)` / `vmess(xhttp)` |
| VLESS `encryption` other than `none` | `ENGINE_NOT_BUILT` | `vless(encryption)` |
| Snell with a version other than 4 (or no version) | `ENGINE_NOT_BUILT` | `snell v3 (sing-box supports v4 and v6)` / `snell unspecified (…)` |
| Clash `wireguard` with `amnezia-wg-option(s)` | `UNSUPPORTED_FEATURE` | `amnezia-wg-option` |
| Clash `ss` with `plugin: restls` / `kcptun` / `gost-plugin` | `ENGINE_NOT_BUILT` | `ss(restls)`, `ss(kcptun)`, `ss(gost-plugin)` |
| Clash/sing-box `network: kcp` (mKCP) | `UNSUPPORTED_FEATURE` | `network kcp (mKCP)` |
| Surge `ssr` line | `ENGINE_NOT_BUILT` | `ssr` |
| sing-box `type: openvpn` (outbound) | `ENGINE_NOT_BUILT` | `openvpn (use a .ovpn profile instead)` |
| tailscale endpoint | `ENGINE_NOT_BUILT` | `tailscale` |
| Any other unknown type | `UNSUPPORTED_PROTOCOL` | `type "<x>" is not importable by Prism` |
| Recognised convertible type with missing data or an unrepresentable transport | `INVALID` | `missing required fields or unsupported transport` |

Two gaps in that reporting, both relevant when reading a subscription:

- **Surge**: `shadowtls`, `naive` and any unlisted Surge protocol are dropped with *no*
  report entry (`internal/subscription/parser.go`, the `case "shadowtls", "naive":` and
  `default:` branches of `parseSurgeProxyLine` both return `seen=false`).
- **Parse-report coverage**: the report caps at 500 entries (`MaxSkippedNodes`) and counts the
  rest in `stats.skipped_overflow`.

A `naive` outbound is *not* reported at parse time: it parses and is imported, then fails to
build (§10.1).

## 8. Where the outcome is actually visible

- The parser returns `ParseResult{Skipped: []SkippedNode{…}}` with the reason strings above
  (`internal/subscription/report.go`). Reason strings are asserted by `internal/subscription`
  `TestSkipDeferredOrUnknown_InfrastructureIsSilent`,
  `TestParseWithReport_InfrastructureOutboundsAreNotReported` and
  `TestSummarizeParseResult_*`; the per-protocol tests still only check that a node is not
  imported (for example `TestParseGeneralSubscription_ClashJSON_TUICWithoutUUIDIsSkipped`).
- **The report reaches the user.** The refresh path calls `subscription.ParseWithReport`
  (`internal/topology/subscription_scheduler.go`, `UpdateSubscription` → `recordParseReport`),
  which keeps the report:
  - the grouped summary is stored on the runtime subscription
    (`Subscription.SetParseSummary`) and is part of every subscription response as
    `parse_report` (list and get);
  - the full bounded report (at most `MaxSkippedNodes` = 500 records, plus
    `stats.skipped_overflow`) is persisted through
    `StateRepo.SetSubscriptionParseReport` — the scheduler's `OnParseReport` hook, wired in
    `cmd/prism/main.go` — and served by `GET /api/v1/subscriptions/{id}/parse-report`
    (`internal/api/handler_subscription.go`, `HandleGetSubscriptionParseReport`);
  - every parse also logs one line with `reason=count` pairs
    (`subscription.SortedSkipSummary`), which never contains node names;
  - the WebUI subscription view renders both (summary plus the full list on demand,
    `internal/api/web/src/features/subscriptions/SubscriptionPage.tsx`).
- A node that parses but cannot be built *is* visible as well: the node exists with
  `has_outbound: false` and `last_error` set (`GET /api/v1/nodes`; service view
  `internal/service/control_plane_platform.go`, `internal/service/control_plane_subscription.go`).
  That is where the sing-box stub messages of §10.1 surface.
- A whole-subscription failure (oversized payload, unknown format, no nodes) is returned as the
  error of the subscription create/refresh request or recorded as the subscription's failure
  state. The parse report of a failed parse is still recorded before the failure is applied.
- The per-subscription `auto_intel` flag (default `true`) is settable through the API:
  `POST /api/v1/subscriptions` and `PATCH /api/v1/subscriptions/{id}` accept `auto_intel`
  (`CreateSubscriptionRequest.AutoIntel`, `subscriptionPatchAllowedFields`), the value is
  persisted (`subscriptions.auto_intel`), reported back in `SubscriptionResponse.auto_intel`
  and exposed as a switch on the subscription page. The intel pipeline reads it from the store
  (`prismApp.intelSubscriptionAutoIntel` in `cmd/prism/intel_runtime.go` →
  `Engine.ListSubscriptions`), pinned by `cmd/prism`
  `TestSubscriptionAutoIntelReachesBootstrapAndPipeline`. The system-wide `intel_enabled`
  setting still switches the whole batch off.

Bounds of the parse-report surface: 500 skip records per report, 16 reason buckets and 5 sample
names per bucket (names capped at 80 runes) in the summary, 64 KiB per stored report. Every
over-limit case is counted explicitly (`skipped_overflow`, `reasons_overflow`,
`samples_truncated`, `truncated`/`original_bytes`) rather than silently dropped.

## 9. Export (removed)

Prism is a relay gateway: node export (`GET /api/v1/nodes/export`), export profiles and the public
`/sub/{token}` subscription output were removed. Subscription *import* (§1–§8) is unchanged.

## 10. Known limitations

Read this before filing a bug; every item is either a deliberate decision or a verified gap.

### 10.1 `naive` is imported and then fails to build

sing-box has no `with_naive_outbound` tag in this build, and `naive` is in the parser's
`supportedOutboundTypes`, so a naive node appears in the pool and then cannot be built.
The user-visible text is the sing-box stub message:
`naive outbound is not included in this build, rebuild with -tags with_naive_outbound`
(`internal/node/naive_outbound_stub.go` in sing-box 1.14.2; matrix case `naive-not-built`
asserts the `not included in this build` substring).
Note: the parse report does not carry an `ENGINE_NOT_BUILT:naive` entry — a `naive` node is
only refused when its outbound is actually built, which is what the message above reports.

### 10.2 `tor`: no claim

`tor` is accepted by the parser (`supportedOutboundTypes`) and sing-box 1.14.2 registers the
`tor` outbound unconditionally (`include/registry.go` imports `protocol/tor`, the file has no
build tag). It is absent from `SingboxOutboundTypes()`, from the protocol matrix and from the
end-to-end entry-point tests. Whether a tor node dials successfully depends on reaching the
Tor network. Treat tor support as unverified.

### 10.3 `tailscale` is not built

No `with_tailscale` tag. Parsed as `ENGINE_NOT_BUILT`/`tailscale`; an envelope-shaped
tailscale endpoint fails at build with the sing-box stub message.

### 10.4 Offline databases need a download first

`dbip_lite` (DB-IP Lite city + ASN), `maxmind_geolite2` (GeoLite2 city + ASN, requires an
account id + license key) and `ipinfo_lite` are offline `.mmdb` files under
`$PRISM_CACHE_DIR/geo` (`internal/intel/providers/geo_db.go`, `GeoDBSources`). Before a file
lands:

- the provider returns `PROVIDER_UNAVAILABLE` with `<name> database is not installed`
  (`internal/intel/providers/offline.go`), and no ASN/geo evidence is recorded for that node;
- `GET /api/v1/intel/providers` reports `database.installed=false` / `ready=false` with a
  `reason` and a stable `error_code` (`GEO_NOT_READY` when the credential is missing, …) on
  the data-source settings page; `POST /api/v1/intel/providers/{id}/actions/refresh`
  downloads now and returns a per-file outcome (`refreshed` / `up-to-date` / `skipped` /
  `failed`);
- a source the user never configured stays `skipped`, never an error.

`country.mmdb` for the `geo_country` provider is a separate updater
(`internal/geoip/geoip.go`, `POST /api/v1/geoip/actions/update-now`); it answers
`PROVIDER_UNAVAILABLE` with `country.mmdb is not available` until then.

### 10.5 Upstream sing-box data race (fixed upstream in v1.14.2)

sing-box v1.12.21 through v1.14.1 wrote `NetworkManager.started` as a plain `bool`
(`route/network.go:220`) while the netlink callback goroutine read it (`route/network.go:574`),
so exercising the embedded runtime with the real route/netlink monitor under the Go race detector
produced a `WARNING: DATA RACE` inside sing-box's own `route/network.go` — upstream code, not Prism.

v1.14.2 rewrote `NetworkManager` onto `startedCtx`/`startedCancel` plus explicit mutexes
(`stateAccess`, `environmentUpdateAccess`, `interfaceUpdateAccess`, `resetRunAccess`,
`powerUpdateAccess`), and the plain field no longer exists. Prism is locked to v1.14.2 and
`make verify` (which includes `go test -race`) is race-clean on it: zero `DATA RACE` and zero
`FAIL` lines in the run.

The test-only no-op interface monitor (`SingboxBuilderConfig.QuietInterfaceMonitor`) is still
installed, but no longer as a race workaround — the tests simply want no live netlink monitor.
Decision D-3 in `docs/ENGINE_DECISIONS.md` records the history and the closure. A NEW race report
naming `github.com/sagernet/sing-box/route` is therefore a regression to investigate, not the known
upstream finding.

### 10.6 No remaining parse-report gap (kept for the record)

This section used to state that the parse report and the `auto_intel` flag were produced but not
exposed. Both are now reachable: `parse_report` on every subscription response,
`GET /api/v1/subscriptions/{id}/parse-report` for the full bounded list, the per-parse
`reason=count` log line and the WebUI subscription view (§8). There is no known gap left here;
the bounded behaviour (500 records, 16 buckets, 5 samples, 64 KiB) is part of §8.

### 10.7 Offline protocol end-to-end suite

`internal/e2e/protocols_test.go` stands up a real sing-box instance serving each protocol's
inbound on a loopback port, points a Prism-built outbound at it, and reads a body back from a
loopback HTTP server through the tunnel. Nothing outside the process is contacted, so this
proves "traffic flows through this node to a target" per protocol rather than only
"the node can be constructed". Its companion:

- `TestProtocolMatrix` — parses and *builds* every protocol, dials an unreachable target for
  chain cases (`internal/outbound/protocol_matrix_test.go`);
- entry-point end-to-end tests (`internal/api/major_flow_e2e_test.go`,
  `internal/api/subscription_refresh_e2e_test.go`, `internal/api/subscription_cleanup_e2e_test.go`,
  `internal/proxy/e2e_test.go`, `internal/proxy/e2e_subscription_test.go`);
- the offline intel pipeline test (`internal/intel/pipeline_e2e_test.go`).

Protocols whose inbound needs a peer this suite cannot synthesise (a real WireGuard peer, an
OpenVPN/OpenConnect server) are covered by the matrix only, and §10.1/§10.2 still apply to the
protocols that are not built at all.

### 10.8 Release-path caveats

- `docs/release-notes/<tag>.md` is used as the release body when the file exists
  (`release.yml` looks for one named after the tag); when it does not, the workflow falls
  back to generated notes.
- Released binaries and the container image print the same `BuildTags` as `make backend`:
  `release.yml` converts its tag list to the comma-separated `-X prism/internal/buildinfo.Tags`
  value and the root `Dockerfile` does the same (`tr ' ' ','`). Verified locally by building with
  the workflow's exact `-ldflags` string and running `prism version` (§1).
- No release path passes `with_mihomo` any more, and the capability API is gated on a runtime
  registration, so `GET /api/v1/system/capabilities` reports `mihomo.built=false` in every
  artifact (§1).
- The Docker image build is not verified in this environment (Docker is unavailable); the
  Dockerfiles were checked by inspection and every path they copy exists in the tree.

### 10.9 mihomo-only and other unrepresentable protocols

`ssr`, `mieru`, `masque`, `trusttunnel`, `sudoku`, `shadowquic`, `gost-relay`, `easytier`,
`zerotier`, Snell v1–v3, VLESS XHTTP, VLESS `encryption`, AmneziaWG, `ss(restls|kcptun|gost-plugin)`
and the tailscale endpoint remain unsupported by decision D-1; §7 lists the exact reason
strings.

### 10.10 Not ported from Resin / still absent

- The public-source collector **is** ported now: `internal/publicsource` (10 test files) and
  `cmd/public-source-sync` (2) landed afterwards, so the earlier "no Prism equivalent" note no
  longer applies. Two `internal/config` cases (`TestLoadEnvConfig_PublicSourceOverrides`,
  `TestLoadEnvConfig_PublicSourceRequiresSourcesWhenEnabled`) stay unported on purpose: Prism keeps
  `PUBLIC_SOURCE_*` in the companion binary's own config instead of `internal/config`
  (`docs/MIGRATION_FROM_RESIN.md`).
- The WireGuard test cases of the upstream protocol table were deliberately replaced by the
  endpoint forms of §5 (sing-box 1.14 removed the WireGuard outbound).
- The `internal/inspection` test files (`ippure`, `manager`, `provider`, `tor_registry`) and
  `internal/api`'s `handler_ippure_test` are not in the tree and never will be: WP08 §8 deleted the
  whole `internal/inspection` package (manager, providers, `tor_registry`) and moved its two
  responsibilities onto intel.db and the control plane
  (`internal/service/control_plane_quality.go`, `internal/api/handler_quality.go`). Those tests
  exercised an implementation that no longer exists. Their behaviour is covered against the new
  implementation by `internal/service/control_plane_quality_test.go` and
  `internal/api/quality_status_test.go`.
  An earlier revision explained the gap with "no type in this tree implements `inspection.Store`";
  that described a tree that still contained the package and is now obsolete.
  `docs/MIGRATION_FROM_RESIN.md` carried the same stale paragraph and is corrected too.

### 10.11 Upstream sing data race: `CachedConn` (NOT fixed upstream, and reachable from our
outbound)

`github.com/sagernet/sing/common/bufio.CachedConn` guards its `taken` field with an `atomic.Bool`
but reads and writes `c.buffer` with **no synchronisation at all**: `Close()`
(`common/bufio/cache.go:85`) reads it, nils it and calls `DecRef`/`Release`, while `Read()`
(`common/bufio/cache.go:47`) does the same on its error path. A reader and a closer running
concurrently is therefore a data race under the Go memory model — upstream code, not Prism.

Where it shows up: `make verify`'s `test-race` stage, on `internal/e2e`'s
`TestOfflineProtocolRoundTrip/shadowtls-v3-shadowsocks-chain`, as `WARNING: DATA RACE` followed by
`race detected during execution of test`. It is **probabilistic**: the shadowtls case passed 40
consecutive `-race` runs locally and the whole `internal/e2e` package passed 10, while CI hit it
once in six consecutive pushes. The same commit's `make test` (no `-race`) passes.

Read the stack carefully: it *does* name `sing-box/route.(*ConnectionManager).connectionCopy`, but
only as the caller that closes the connection. **The racing field is `CachedConn.c.buffer` in
`sing/common/bufio`**, so this is not a §10.5 regression — §10.5 is about `NetworkManager.started`
in `sing-box/route/network.go`, which v1.14.2 fixed.

Upstream status rechecked 2026-09-28: `dev` (the default branch) and `main` both still carry the
unsynchronised `Close()`/`Read()`. There is no `master` branch - the earlier note saying "dev, main
and master were all checked" was wrong; the branches are `dev`, `main`, `stable` and some
`renovate/*`. Rechecked 2026-09-30: a request for `master` still answers 200, because GitHub
redirects a branch's old name after a rename, and the bytes it returns are `dev`'s
(`sha256 b14f264a...`, identical to `dev` and different from `main`'s `0ecef13b...`); a genuinely
missing branch answers 404. Do not read that 200 as a third branch. Prism pins
`sing v0.9.6-0.20260922013354-87c33f17688f`, chosen by `sing-box v1.14.2`'s
require. `main`'s `common/bufio/cache.go` is **byte-identical** to our pin (blob `94423887b2`); `dev`
differs only in the UDP `CachedPacketConn` refactor from `9822d61a` (author 2026-09-10, committer
2026-09-27 - upstream rebased it), and `CachedConn`/`CachedReader` are identical on both. Do not
mistake the older fix for this one: `b8eed517` (2026-01-17) is titled "Fix race between
**ReadCached** and Close" and only makes `ReadCached()` mutually exclusive with `Close()` through
`taken.CompareAndSwap`; the race we hit is **`Read()` vs `Close()`**, and `Read()` never touches
`taken`.

Where it can happen (rewritten 2026-09-28; both earlier versions of this paragraph were wrong - the
first said the window was reachable in production, the second said Prism's data path never creates a
`CachedConn`; the truth is that it does).

*Inbound side* (Prism never creates one here): `sing-box/route/route.go:148/164` (only when the
first-packet `buffers` are non-empty), `sing/protocol/http`, `sing/protocol/socks`,
`sing-shadowsocks/shadowaead_2022/relay.go:193`, and the `sing-box/transport/*` servers.

*Outbound side* (**Prism's `adapter.Outbound` does create one**):

- `sing/protocol/http/client.go:138` - `(*http.Client).DialContext` wraps the connection in a
  `CachedConn` when the response left buffered bytes. That client is what sing-box's `http` outbound
  builds (`sing-box/protocol/http/outbound.go:44`), and Prism accepts `http`/`https` nodes
  (`internal/subscription/parser.go:608/980/1530/2321`; protocol matrix cases `http-uri`,
  `https-uri`).
- `sing-box/transport/v2raywebsocket/client.go:117` - same wrap after a ws upgrade
  (`sing-box/transport/v2ray/transport.go:57`). That is the `transport: ws` path of vmess/vless, and
  Prism keeps `net=ws` from subscriptions (`internal/subscription/parser.go:950/1129/3151`).
- `sing-box/transport/v2rayhttpupgrade/client.go:113` - the same for httpupgrade
  (`transport/v2ray/transport.go:64`; `internal/subscription/parser.go:1161`).

(Two further creation points are server-side and not reachable from Prism:
`sing-box/transport/trojan/mux.go:67` and `transport/trojan/service.go:121`.)

Reproduced in production code on 2026-09-28 (WSL, `-race`): a stub proxy answers CONNECT with
`200 Connection Established` **plus** server-first bytes in one write (what a proxy in front of a
server-first protocol does), and a node built by **Prism's own**
`outbound.NewSingboxBuilderWithConfig` + `Build()` - `{"type":"http",...}` - dials it. The dial result
is a `*bufio.CachedConn`; reading and closing it concurrently (the shape of Prism's tunnel pump,
`internal/proxy/tunnel.go:220` and `:214`) makes `-race` report `(*CachedConn).Read` at
`cache.go:40` against `(*CachedConn).Close` at `cache.go:86` - the same field and method pair CI caught
in `internal/e2e`.

CI caught it on the shadowtls case first because those cases stand up a real sing-box instance
**inside the test process** (`protocols_test.go`'s `startPeerFixture` calls `box.New` +
`instance.Start`) to act as the peer, and that instance's *inbound* creates the `CachedConn`
(`sing-shadowtls/service.go:285`, the v3 branch). `-race` only reports races inside one process, so
the first sighting was there - but the first sighting is not the only reachable site.

The complete CI stack (run `36326553534`, job `108640324612`, commit `b506888`) is:
write side `(*CachedConn).Read` `cache.go:47` <- `shadowaead.(*Reader).Read` `shadowaead/aead.go:142`
<- `shadowaead.(*serverConn).Read` `shadowaead/service.go:150` <- `bufio.copyExtendedWithPool` <-
`route.(*ConnectionManager).connectionCopy` `route/conn.go:274`; read side `(*CachedConn).Close`
`cache.go:85` <- `shadowaead.(*serverConn).Close` <- `bufio/deadline.(*Conn).Close` <-
`connectionCopy` `route/conn.go:286`. Both goroutines come from `ConnectionManager.NewConnection`
(`route/conn.go:152/153`), reached via `sing-shadowtls/service.go:285` ->
`sing-box/protocol/shadowtls/inbound.go:140` -> `route.RouteConnectionEx` ->
`protocol/shadowsocks/inbound.go:111` -> `shadowaead.Service.newConnection` ->
`adapter.legacyUpstreamHandlerWrapper.NewConnection` -> `shadowsocks.(*Inbound).newConnection`
`inbound.go:134` -> `route.RouteConnection` `route.go:45`.

**Method A - run the peer sing-box out of process - was prototyped and rejected.** A helper
(`internal/e2e/peerserver`) that runs one instance from a JSON config, exec'd by the fixture and
awaited on a `READY` line, works: all 15 round-trip cases passed under `-race`, six rounds of
`-count=5` were clean, each round ~8s. But it does not fix the race, for two reasons:

1. The outbound-side `CachedConn` is created by **Prism's own outbound**, inside the test process, so
   `-race` still sees it. Moving the peer only moves the inbound half.
2. The inbound half cannot be moved either. `include.Context` is imported by
   `internal/outbound/singbox_runtime.go:16`, which is production code (`box.New` registers every
   protocol), and the fixture's `newBuilder` uses it. Measured: with the fixture exec'ing the helper,
   the test binary's dependency set did not shrink at all (917 -> 917 packages;
   `sing-box/protocol/{shadowsocks,shadowtls,socks,http,trojan}`, `sing-box/route`, `sing-shadowtls`,
   `sing-shadowsocks/shadowaead` and `sing/common/bufio` all still linked). Moving that too would mean
   not exercising Prism's outbound at all, which is the coverage the suite exists for.

Config-level workarounds (different cipher, different protocol, dropping ss-2022 or shadowtls) all
change what the suite proves and are therefore rejected outright.

So: the `-race` gate stays as it is, nothing is skipped and no allow-list is added. This is upstream
code; the fix belongs upstream, and the prepared patch (plus a deterministic reproducer) is recorded
in D-5 of `docs/ENGINE_DECISIONS.md`. Re-evaluate when upstream fixes the field, or when the hit rate
rises.

**Rechecked 2026-09-30, two additions.**

*It reproduces with no Prism code linked.* A standalone `main` that imports only
`sing/common/bufio` and `sing/common/buf` - no Prism package at all - builds the value the report
names (`bufio.NewCachedConn`), then reads it and closes it from two goroutines. Under `-race` it
prints 8 reports naming exactly this field and method pair: `(*CachedConn).Close` `cache.go:85` (read)
against `(*CachedConn).Read` `cache.go:47` (write), plus the `cache.go:40` variant. That removes the
last place the attribution could have gone wrong: nothing in the reproducer is ours, so the race
cannot be ours either. (`CachedConn.Read` does not touch `taken`; only `Close` and `ReadCached` do,
which is why the older upstream fix does not cover it.)

*It is pre-existing, not introduced by the lint cleanup.* Between the last commit whose CI was green
before that work (`535ca96`) and HEAD:

- `go.mod`'s `sagernet/sing` and `sagernet/sing-box` requirements are unchanged, and the `go.sum`
  lines for `sagernet/sing` are byte-identical.
- The same holds against the released `v0.1.0-rc2` (`64100a9`), so the race is present in the
  published tag as well.
- The only change under `internal/e2e/` in that range is the removal of the `offlineCase.udp` field,
  which was never assigned or read - it cannot affect scheduling.
- The `-race` stage was green on `535ca96` (CI run `36701455439`) and the failure first appeared on
  the run for HEAD, which is the signature of a probabilistic upstream race rather than a
  regression.

Local stress on `535ca96` itself (4 rounds x `-count=400` at `GOMAXPROCS=2`) did not reproduce it -
at roughly one hit per 100 runs, four rounds is not enough to expect one - but the dependency and
diff evidence above does not depend on that sampling.

### 10.12 The e2e fixture reused a loopback port (fixed, and not the race above)

Same test case, different failure, and worth separating: `shadowtls-v3-shadowsocks-chain` can also
fail with

```
start inbound/shadowtls[stls-in]: listen tcp 127.0.0.1:44417: bind: address already in use
```

That one was **ours**. `internal/e2e/protocols_test.go`'s `freeLoopbackPort` reserves a port by
binding `127.0.0.1:0`, reading the number and closing the listener again. The kernel is free to hand
the same number straight back on the next call, and this case reserves two ports back to back
(`ssPort`, `shadowTLSPort`) and then binds both inside **one** sing-box instance, so the second bind
can collide with the first. Measured before the fix: one failure in 3000 runs of that case
(`-count=3000`, no `-race`).

The fix keeps a bounded window (`recentPortWindow`, 64 entries) of recently handed-out ports in
`recentPorts` and draws another port when the kernel repeats itself. The window update lives in the
pure function `portWindowAppend`, pinned by `TestPortWindowAppend`, so the behaviour is tested
without depending on the kernel actually repeating a port. After the fix the same 3000 runs are
clean.

Bounded is the load-bearing word. The first version remembered every port for the lifetime of the
process; under `-count=3000` the suite reserves thousands of ports and, because a closed port is
handed out again immediately, the draw loop then never found an unused one - 212 failures, all
`no unused loopback port after 128 attempts`. A sliding window is sufficient because only the
reservations one fixture makes in a row have to differ.

Do not confuse this with §10.11. `address already in use` is a fixture defect and is fixed; a
`WARNING: DATA RACE` naming `(*CachedConn).Read`/`Close` in `sing/common/bufio` is upstream code and
is not.

## 11. Which test pins which claim

| Claim | Test |
|---|---|
| Advertised outbound/endpoint types exist in the sing-box registries | `internal/outbound` `TestProtocolMatrixCapabilities` |
| Every §4 row can be parsed *and* built | `internal/outbound` `TestProtocolMatrix` |
| Chain nodes dial past detour resolution | `TestProtocolMatrix` cases `chain-*` (`assertDetourResolves`) |
| `.ovpn` variants are accepted / refused with a reason | `TestProtocolMatrix` cases `ovpn-*` + fixtures in `internal/subscription/testdata/openvpn/` |
| Deferred/rejected types are not imported (per protocol the matrix cases only assert non-import; the reason *codes* are asserted by the report tests) | `TestProtocolMatrix` cases `ssr-clash-deferred`, `mieru-clash-deferred`, `wireguard-amnezia-deferred`, `tailscale-endpoint-not-built`, `snell-clash-v3-deferred`, `vless-xhttp-deferred`, `vless-encryption-deferred`, `mihomo-proxy-envelope`, `invalid-uri` (`notParsed`), and `internal/subscription` `TestParseGeneralSubscription_*IsSkipped` cases |
| Config-level sing-box objects (`direct`/`block`/`selector`/`urltest`/`dns`/`bridge`/`openvpn-server`) are ignored without a report entry, deferred/unknown types keep their reason | `internal/subscription` `TestSkipDeferredOrUnknown_InfrastructureIsSilent`, `TestParseWithReport_InfrastructureOutboundsAreNotReported` |
| The parse report reaches the API: reason counts match the input, the summary is on the subscription, the full list is on `/parse-report` | `internal/api` `TestAPIContract_SubscriptionParseReport_ExposesDroppedNodes`; summary bounds/redaction in `internal/subscription` `TestSummarizeParseResult_*`, `TestSortedSkipSummary_IsReasonCountsOnly` |
| `auto_intel` is settable through the API, reported back and observed by the intel pipeline | `internal/api` `TestAPIContract_SubscriptionAutoIntel_RoundTripsAndReachesTheStore` (API → store), `cmd/prism` `TestSubscriptionAutoIntelReachesBootstrapAndPipeline` (store → pipeline lookup → runtime) |
| A capability is only reported when the runtime can deliver it (mihomo stays `built:false`, also with `-tags with_mihomo`) | `internal/outbound` `TestEngineCapabilitiesMatchRuntimeBehaviour`, `internal/api` `TestSystemCapabilities_NeverClaimsAnUnbuiltEngine` |
| `naive` fails only at build time | `TestProtocolMatrix` case `naive-not-built` |
| Share links, Clash, Surge and sing-box JSON conversion detail | `internal/subscription` `TestParseGeneralSubscription_*` (parser, links, plugins, transports) |
| Offline database state and refresh | `internal/intel/providers` `geo_db_test.go`, `internal/api` `handler_intel_geo_test.go` |
| Intel pipeline (egress → offline → online → via-node → checks → assess) | `internal/intel` `pipeline_e2e_test.go`, `internal/intel/jobs` `jobs_test.go` |
