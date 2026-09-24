# Prism Protocol Support Matrix

**中文摘要**：本文件是从代码与测试反推出来的协议支持矩阵，取代 `docs/plan/06-singbox-engine-protocols.md`
中的计划表格。运行时内核只有 sing-box `1.14.2`；mihomo 按 `docs/ENGINE_DECISIONS.md` 的 D-1 被否决，
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
- `Makefile` defines `TAGS_FULL := $(TAGS_BASE)` — the lite and full variants compile with
  the same tag set today (`with_quic with_grpc with_utls with_wireguard with_gvisor
  with_openvpn with_openconnect http2legacy`).
- The tag set is identical in all four build paths: the `Makefile` (`TAGS_FULL := $(TAGS_BASE)`),
  `.github/workflows/release.yml` (`TAGS_FULL` = `TAGS_BASE`), the root `Dockerfile`
  (`ARG TAGS`, same list) and `.github/release/Dockerfile` — the published image is built from
  (`ARG TAGS`, same list) and `.github/Dockerfile.release` — the published image is built from
  that tag set. `with_mihomo` is passed by none of them.
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

## 9. Export formats and subscription links

Output (independent of the D-1 kernel decision — a mihomo *configuration file* is a
dependency-free serialisation, `internal/export/types.go`):

| Format | Content type | Notes |
|---|---|---|
| `singbox` | `application/json` | selector + urltest groups, default tags `PROXY` / `AUTO` |
| `mihomo` | `text/yaml` | Clash Meta config; chain nodes are not representable (`NOT_REPRESENTABLE:mihomo(chain)`) |
| `v2rayn` | `text/plain` | share-link list |
| `uri` | `text/plain` | plain share-link list (round-trip tested through the parser) |
| `csv`, `json` | `text/csv` / JSON | analysis columns incl. optional intel columns; never contains credentials |

Per-node skips carry `NOT_REPRESENTABLE:<format>`, `NOT_REPRESENTABLE:mihomo(chain)`,
`INVALID:node document` or `INVALID:duplicate node name`; the response reports
`exported` / `skipped` / `truncated` (one request is bounded to `export.MaxItems = 5000`).
`GET /api/v1/nodes/export` is admin-authenticated.

Export profiles (`/api/v1/export-profiles`, `POST …/{id}/actions/rotate-token`) store format,
name template, platform and a node filter with the same vocabulary as `GET /api/v1/nodes`
(`ip_type`, `purity_band`, `purity_min`, `purity_max`, `verdict`, `asn`, `country`, `checks`, …;
`internal/api/handler_export.go`). Each profile exposes a public subscription URL
`<scheme>://<host>/sub/<token>` served without the admin token (`internal/api/server.go`
registers `GET /sub/{token}` on the management handler, so the subscription URL resolves on every
listener that carries the management surface — the primary listener and `PRISM_ADMIN_LISTEN` — and
is gated by that endpoint's `allow_management` flag exactly like `/api/*`;
`cmd/prism/inbound_mux.go`, `shouldRouteControlPlane`). A profile can be disabled and its token
rotated; an unknown token, a disabled profile and a listener without management access all answer
`404`. The public endpoint stores only the token hash and has its own limiter — 60 requests per
minute per token, 120 per minute per client IP, then `429` with `Retry-After` — which is
independent of `PRISM_PROXY_AUTH_FAIL_LIMIT` (`internal/api/export_token.go`,
`internal/api/handler_subscription_token.go`).

## 10. Known limitations

Read this before filing a bug; every item is either a deliberate decision or a verified gap.

### 10.1 `naive` is imported and then fails to build

sing-box has no `with_naive_outbound` tag in this build, and `naive` is in the parser's
`supportedOutboundTypes`, so a naive node appears in the pool and then cannot be built.
The user-visible text is the sing-box stub message:
`naive outbound is not included in this build, rebuild with -tags with_naive_outbound`
(`internal/node/naive_outbound_stub.go` in sing-box 1.14.2; matrix case `naive-not-built`
asserts the `not included in this build` substring).
Note: `docs/plan/06-singbox-engine-protocols.md` expected a parse-report
`ENGINE_NOT_BUILT:naive`; the code does not do that.

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

### 10.7 No offline protocol end-to-end suite

`docs/plan/13-testing-release-docs.md` §1 asks for `internal/e2e/protocols_test.go`, which
starts sing-box in-process as a server and proxies real traffic through Prism. That package
does not exist. What exists instead:

- `TestProtocolMatrix` — parses and *builds* every protocol, dials an unreachable target for
  chain cases (`internal/outbound/protocol_matrix_test.go`);
- entry-point end-to-end tests (`internal/api/major_flow_e2e_test.go`,
  `internal/api/subscription_refresh_e2e_test.go`, `internal/api/subscription_cleanup_e2e_test.go`,
  `internal/proxy/e2e_test.go`, `internal/proxy/e2e_subscription_test.go`);
- the offline intel pipeline test (`internal/intel/pipeline_e2e_test.go`).

So "the node can be constructed" is proven; "traffic flows through this node to a target" is
only proven for the entry points, not per protocol.

### 10.8 Release-path caveats

- `docs/release-notes/<tag>.md` does not exist (no `docs/release-notes/` directory). The
  release workflow tolerates that and falls back to generated notes.
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
- The `internal/inspection` test files (`ippure`, `manager`, `provider`, `tor_registry`) and the
  `internal/api` `handler_ippure` / `handler_quality` tests listed as withheld in
  `docs/MIGRATION_FROM_RESIN.md` are still not in the tree, and the reason that document gives
  still holds: `ClaimInspection` / `LoadQualityRecords` exist only as methods of the
  `inspection.Store` interface (`internal/inspection/manager.go`) and are called from the code
  paths of `NewManager`, but **no type in this tree implements that interface** and nothing
  constructs a manager (`grep -rn 'ClaimInspection' --include='*.go' .` returns the interface
  declarations and their call sites only). A previous revision of this section claimed the
  methods were implemented; that was wrong.

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
| Public `/sub/<token>` behaviour and token rotation | `internal/api` `handler_export_test.go` (`TestSubscriptionTokenFlow`, `TestSubscriptionMihomoContentDisposition`), `export_token_test.go` (`TestExportSubscriptionLimiterPerTokenBound`, `TestExportSubscriptionLimiterPerIPBound`); the listener coverage is pinned by `cmd/prism/subscription_routing_test.go` (`TestInboundMuxRoutesSubscriptionPathToManagementHandler`, `TestInboundMuxSubscriptionPathHonoursAllowManagement`, `TestAdminListenerServesSubscriptionPath`) |
| Export formats and their skip reasons | `internal/export` (`TestExportMihomoEveryProxyIsParseableByClash`, `TestExportSkipsUnrepresentable*`, `TestExportAnalysisFormatsNeverCarryCredentials`, …) |
| Offline database state and refresh | `internal/intel/providers` `geo_db_test.go`, `internal/api` `handler_intel_geo_test.go` |
| Intel pipeline (egress → offline → online → via-node → checks → assess) | `internal/intel` `pipeline_e2e_test.go`, `internal/intel/jobs` `jobs_test.go` |
