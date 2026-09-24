# 从 Resin 迁移到 Prism

本文档对应 WP05（Resin 兼容层）。Resin 用户可以原地换用 Prism：环境变量、请求头、错误响应头都保留上游写法，数据用 `prism import-resin` 一次性导入。行为差异只允许出现在下面的偏差清单 X1–X4 中。

## 1. 数据导入：`prism import-resin`

```text
prism import-resin --from-state DIR --from-cache DIR [--from-log DIR] [--force]
```

- `--from-state DIR`：Resin 的 `state.db` 所在目录（必需）。
- `--from-cache DIR`：Resin 的 `cache.db` 所在目录（必需）。
- `--from-log DIR`：可选的请求日志目录，里面的 `request_logs-<unix_ms>.db` 会一并导入；导入时会把上游的 `resin_error` 列改名为 `prism_error`。
- `--force`：目标数据库已存在时必须显式传入；已有的文件会先改名为 `*.pre-import-<时间戳>` 保留。

行为：

1. 检测到 Prism 实例仍在运行（pid 文件存活或监听端口可连接）时直接拒绝执行，判断方式与 `prism restore` 完全一致。
2. 用 `VACUUM INTO` 把 Resin 的 `state.db` / `cache.db` 拷贝到 `PRISM_STATE_DIR` / `PRISM_CACHE_DIR`（请求日志拷贝到 `PRISM_LOG_DIR`），随后自动执行 Prism 迁移（state 10–14 号、cache 2 号）。
3. 平台、订阅、节点（`nodes_static`，含原始 `RawOptions`，节点哈希不变）、租约、接入点全部保留，最后打印各项数量摘要。

### 上游 Docker 默认路径示例

Resin 官方镜像把数据放在 `/var/lib/resin`（`state.db`）和 `/var/cache/resin`（`cache.db`）。先把 Resin 容器停掉，再让 Prism 镜像读取这两个卷：

```sh
docker run --rm \
    -v resin-data:/var/lib/resin:ro \
    -v resin-cache:/var/cache/resin:ro \
    -v prism-state:/var/lib/prism \
    -v prism-cache:/var/cache/prism \
    -e PRISM_STATE_DIR=/var/lib/prism \
    -e PRISM_CACHE_DIR=/var/cache/prism \
    prism:latest import-resin --from-state /var/lib/resin --from-cache /var/cache/resin
```

宿主机上的等价写法（例如把 Resin 的目录挂载到了 `./resin`）：

```sh
PRISM_STATE_DIR=./.local/state PRISM_CACHE_DIR=./.local/cache \
  prism import-resin --from-state ./resin/state --from-cache ./resin/cache --from-log ./resin/logs
```

导入完成后按 `prism run` 启动即可；`prism check-config` 可以先确认配置解析结果。

## 2. 偏差清单 X1–X4

| 偏差 | Prism 行为 |
|---|---|
| X1 | 默认要求 `PRISM_ADMIN_TOKEN` / `PRISM_PROXY_TOKEN` 长度至少 16 个字符；`PRISM_ENFORCE_STRONG_TOKENS=false` 时恢复 Resin 行为：弱令牌只通过 `/api/v1/system/config/env` 的 `admin_token_weak` / `proxy_token_weak` 标记。zxcvbn 弱令牌判定永远只做标记，不会拒绝启动。 |
| X2 | 空令牌表示关闭该范围鉴权，但必须显式设置 `PRISM_ALLOW_EMPTY_ADMIN_TOKEN=true` 或 `PRISM_ALLOW_EMPTY_PROXY_TOKEN=true`，并且 `PRISM_LISTEN_ADDRESS` 是回环地址，或者额外设置 `PRISM_ALLOW_INSECURE_LISTEN=true`。 |
| X3 | `RESIN_<X>` 环境变量仍然生效，但 `PRISM_<X>` 优先，并且每个变量只提示一次 `config: RESIN_<X> is deprecated, use PRISM_<X>`。`PRISM_QUALITY_*` 没有上游对应变量，因此不做兜底。 |
| X4 | 请求头同时接受 `X-Prism-Account` 和 `X-Resin-Account`（同时出现时以 Prism 头为准），两者都会在向上游转发前剥离；所有错误响应同时携带 `X-Prism-Error` 和 `X-Resin-Error`，取值相同。`Proxy-Authenticate` 的 realm 使用 `Prism`。 |

## 3. 覆盖测试

| 能力 | 测试 |
|---|---|
| X3 环境变量兜底 | `internal/config` `TestLookupEnvFallsBackToResinPrefix`、`TestLookupEnvPrefersPrismPrefix`、`TestLookupEnvWarnsOncePerVariable`、`TestLookupEnvDoesNotWarnForPrismVariable`、`TestQualityVariablesHaveNoResinFallback` |
| X1 强令牌策略 | `internal/config` `TestEnforceStrongTokensOffRestoresUpstreamBehaviour` |
| X2 空令牌语义 | `internal/config` `TestEmptyTokenRequiresOptInAndLoopback`、`TestEmptyAdminTokenRequiresItsOwnOptIn` |
| X4 请求头与错误头 | `internal/proxy` `TestResolveReverseAccountAcceptsBothAccountHeaders`、`TestReverseProxyAccountHeaderCompat`、`TestWriteProxyErrorCarriesResinHeader`、`TestReverseProxyErrorCarriesResinHeader`；`cmd/prism` `TestEndpointInboundMux_CompatErrorHeaders` |
| `prism import-resin` | `cmd/prism` `TestImportResinMigratesAndReportsSummary`、`TestImportResinRequiresForceAndMovesExistingDatabasesAside`、`TestImportResinRefusesWhileServiceIsRunning`、`TestImportResinImportsOptionalRequestLogs`、`TestImportResinRequestLogsRequireForceWhenTargetExists`、`TestImportResinCommandImportsIntoConfiguredDirectories`、`TestImportResinRejectsMissingSourceDatabases` |

## 4. Resin 能力对照表（WP05 §6）

上游 `9b8ef8e` 的 93 个测试文件（`cmd/resin` 与 `internal/*`，`internal/publicsource` 除外）已全部移植，例外见文末「未随本次移植落地」。下表按 §6 的能力清单列出对应的测试名。

| Resin 能力 | 覆盖测试（来源） |
|---|---|
| 单端口：UI、API、HTTP 正向代理、反向代理、SOCKS5 | `cmd/prism` `TestInboundMux_PriorityForwardConnect`、`TestInboundMux_PriorityForwardAbsoluteURI`、`TestInboundDemux_RoutesHTTPToHTTPServer`、`TestInboundDemux_RoutesSocks5ByFirstByte`、`TestInboundDemux_PreservesHTTPFirstByteAfterPeek`、`TestInboundDemux_IdleConnectionDoesNotBlockSubsequentAccepts` |
| 多接入点热添加和能力开关 | `cmd/prism` `TestEndpointRuntimeManager_RemoveReleasesPortBeforeReturn`、`TestRestorePersistedEndpoints_SkipsDisabledListeners`、`TestEndpointInboundMux_AppliesCapabilities`、`TestEndpointInboundMux_InjectsProxyAuthInfoPolicy`；`internal/api` `TestHandleListEndpointsPagination`、`TestHandleCreateDisabledEndpoint`、`TestHandleEndpointEnableAndDisableWithPatch`；`internal/service` `TestControlPlaneEndpoints_CRUDAndDefaultProtection`、`TestControlPlaneEndpoints_ListenerFailureRollsBackPersistence`、`TestControlPlaneEndpoints_EnableAndDisableWithPatch`、`TestControlPlaneEndpoints_ManagementOnlyDefaultsProxyProtocolsOff` |
| `Platform.Account:Token` 身份、Default 平台 | `internal/proxy` `TestParseV1PlatformAccountIdentity`、`TestParseForwardCredentialV1`、`TestParseForwardCredentialV1WhenAuthDisabled`、`TestReverseParsePath_V1_AcceptsIdentityWithoutColon`、`TestReverseParsePath_Valid`、`TestResolveDefaultPlatform`；`internal/routing` `TestRouteRequest_DefaultPlatform`、`TestRouteRequest_DefaultPlatformRequiresWellKnownID` |
| 粘性租约、同 IP 切换、租约清理、IP 负载 | `internal/routing` `TestStickyLease_CreateAndHit`、`TestStickyLease_Expiry`、`TestRestoreLeases`、`TestLeaseCleaner_SweepExpired`、`TestLeaseCleaner_SweepExpiresLeaseDeterministically`、`TestChooseSameIPRotationCandidate_PicksLowestLatency`、`TestRouteRequest_SameIPRotationMissRecreatesLease`、`TestIPLoadStatsSnapshot`、`TestRouterSnapshotIPLoad`、`TestDeleteLease_EmitsLeaseRemoveWithLifetimeFields` |
| P2C 加按域名延迟、三种分配策略 | `internal/routing` `TestP2C_FavorsIdleIP`、`TestCompareLatencies_ComparableTargetDomain`、`TestCompareLatencies_IncomparableFallsBackToZero`、`TestRandomRoute_EmptyView`、`TestRandomRoute_SingleNode`、`TestRandomRoute_MultipleNodes`、`TestRouteRequest_ConcurrentSafety` |
| 正则 ANY/MUST/MUST_NOT、地区过滤、筛选预览 | `internal/platform` `TestPlatform_EvaluateNode_RegexFilter`、`TestPlatform_EvaluateNode_RegionFilter`、`TestPlatform_EvaluateNode_RegionFilter_ExcludeOnlyUnknownRegion`、`TestMatchRegionFilter`、`TestPlatform_FullRebuild_ClearsOld`、`TestPlatform_NotifyDirty_AddRemove`；`internal/service` `TestPreviewFilter_RegexRulesAnyMustAndMustNot`、`TestPreviewFilter_RegionMixedIncludeExclude`、`TestPreviewFilter_RegionNegation` |
| 被动熔断、主动恢复、平台关闭熔断 | `internal/topology` `TestRecordResult_CircuitBreak`、`TestRecordResult_Recovery`、`TestRecordResult_MaxConsecutiveFailuresPulled`、`TestRecordResult_CircuitBreak_RemovesFromView`、`TestRecordPassiveResult_DisabledPlatformSkipsFailures`、`TestRecordPassiveResult_EnabledPlatformCountsFailures` |
| 订阅：远程和本地、增量存活、临时订阅与驱逐、熔断节点清理 | `internal/topology` `TestScheduler_UpdateSubscription_Success`、`TestScheduler_UpdateSubscription_DownloadViaHTTPServer`、`TestScheduler_UpdateSubscription_LocalSubscription_SuccessWithoutFetcher`、`TestScheduler_UpdateSubscription_IncrementalAliveModeKeepsHealthyOldNodes`、`TestScheduler_UpdateSubscription_IncrementalAliveModeRemovesUnhealthyOldNodes`、`TestEphemeralCleaner_ConfirmedEviction`、`TestEphemeralCleaner_TOCTOU_RecoveryBetweenScans`；`internal/service` `TestCleanupSubscriptionCircuitOpenNodes_RemovesCircuitAndOutboundFailureNodes`；`internal/api` `TestAPIContract_SubscriptionRefreshAction_E2ELocalSource`、`TestAPIContract_SubscriptionRefreshAction_E2EHTTPSource`、`TestAPIContract_SubscriptionCleanupAction_E2E`、`TestMajorFlow_E2E_LocalProxyAndSubscriptionProvider` |
| 请求头提取规则（含 URL 前缀）、miss action、固定账号头 | `internal/proxy` `TestAccountMatcher_LongestPrefix`、`TestAccountMatcher_WildcardFallback`、`TestAccountMatcher_MatchWithPrefix`、`TestExtractAccountFromHeaders_Ordered`、`TestBuildAccountMatcher_UsesSharedRulePrefixNormalization`、`TestReverseProxy_ResolveReverseProxyAccount_BehaviorFixedHeader`、`TestReverseProxy_ResolveReverseProxyAccount_BehaviorAccountHeaderRule`、`TestReverseProxy_ResolveReverseProxyAccount_XResinAccountHeaderCompat`、`TestFixedAccountHeadersForPlatform`、`TestShouldRejectReverseProxyAccountExtractionFailure`、`TestEffectiveEmptyAccountBehavior_DefaultsToRandom`、`TestReverseProxy_AccountExtraction_WithMatcher`；`internal/platform` `TestReverseProxyMissActionIsValid`、`TestNormalizeReverseProxyMissAction`、`TestNormalizeFixedAccountHeaders_MultiLineCanonicalAndDedup`、`TestParseAllocationPolicy`；`internal/service` `TestResolveAccountHeaderRule_UsesEscapedPathSegments`、`TestUpsertAccountHeaderRule_NormalizesHostPrefix`、`TestDeleteAccountHeaderRule_RejectsFallbackRule` |
| WebSocket 反代、bypass 直连 | `internal/proxy` `TestReverseProxy_E2EWebSocketUpgrade_WithDetailCapture`、`TestForwardProxy_E2EHTTPBypassDialsDirect`、`TestReverseProxy_E2EHTTPBypassDialsDirect`、`TestForwardProxy_CONNECTTunnelSemantics`、`TestSocks5Inbound_CONNECTBypassDialsDirect`、`TestTargetBypassMatcher_MatchesCommonNoProxyRules`、`TestTargetBypassMatcher_EmptyRulesDoesNotMatch` |
| 请求日志（payload 捕获）、指标（实时、历史、快照） | `internal/requestlog` `TestRepo_InsertListGetPayloads`、`TestService_FlushesByBatchSize`、`TestRepo_ListCursorPagination`、`TestRepo_CleanupRetainsConfiguredFileCount`；`internal/metrics` `TestTakeSample_NormalizesThroughputToBPS`、`TestTakeConnectionsSample_UsesWindowMaxActiveConnections`、`TestFlushBucket_RetainsPendingTaskUntilRepoRecovers`、`TestQueryHistoryTraffic_AdvancesStaleBucketWithoutBucketLoop`；`internal/api` `TestMetricsHandlers_SnapshotNodePool_IncludesHealthyEgressIPCount`、`TestMetricsHandlers_HistoryTraffic_MergesPersistedAndCurrentBucket`、`TestMetricsHandlers_RealtimeStepSecondsMatchMetricIntervals`；`internal/proxy` `TestReverseProxy_E2ECapturesDetailPayloads` |
| GeoIP 自动更新和查询 | `internal/geoip` `TestGeoIP_ReloadReader`、`TestGeoIP_ConcurrentLookupDuringReload`、`TestUpdateNow_DownloadVerifyReload`、`TestUpdateNow_SHA256Mismatch_NoReplace`、`TestGeoIPStart_MissingDBTriggersBackgroundUpdate`、`TestGeoIPStop_WaitsInFlightUpdateAndClearsReader` |
| 持久化与重启恢复 | `internal/state` `TestEngine_StrongPersist_ConfigSurvivesRestart`、`TestEngine_StrongPersist_PlatformSurvivesRestart`、`TestEngine_WeakPersist_CacheDataSurvivesRestart`、`TestEngine_WeakPersist_FlushAndLoad`、`TestMigrateStateDB_UpgradesLegacyPlatformsColumns`、`TestStateRepo_ConcurrentWrites`；`cmd/prism` `TestBootstrapRestart_RecoversTopologyAndStickyLease`、`TestBootstrapRestart_RecoversObservabilityPersistence` |
| token-action | `internal/api` `TestTokenActionInheritLease_Success`、`TestTokenActionInheritLease_RejectsUnknownFields`、`TestTokenActionInheritLease_ParentMissingOrExpiredReturnsNotFound`、`TestTokenActionInheritLease_InvalidArguments`；`cmd/prism` `TestInboundMux_RoutesTokenAPINamespaceToTokenActionHandler`、`TestInboundMux_RoutesTokenInheritLeaseAction`、`TestInboundMux_EmptyProxyToken_RoutesNonActionTokenNamespaceToTokenAction` |

### Prism 新增能力的测试

| 能力 | 测试 |
|---|---|
| 平台质量准入 | `internal/platform` `TestPlatform_EvaluateNode_QualityPolicy_MinScore`、`TestPlatform_EvaluateNode_QualityPolicy_IPTypes`、`TestPlatform_EvaluateNode_QualityPolicy_ExcludeHighRisk`、`TestPlatform_EvaluateNode_QualityPolicy_ExcludeTor`、`TestPlatform_EvaluateNode_QualityPolicy_Empty`、`TestPlatform_EvaluateNode_QualityPolicy_NoAssessment` |
| 质量模型与评估 | `internal/quality` `TestAssessmentUsesIPPureScoreAndIndependentRiskEvidence`、`TestQueuedInspectionStateRemainsPending`、`TestPublicTorEvidenceAddsReviewWithoutChangingIPPure`、`TestPurityDisplayBoundariesAndUnknown`、`TestNetworkTypePreservesAllocationMeaning`；`internal/model` `TestQualityPolicyIsEmpty`、`TestQualityPolicyUnmarshalLegacyKeys`、`TestQualityPolicyJSONRoundTrip`、`TestQualityPolicyMarshalWritesOnlyCurrentKeys` |
| 容量回归 | `internal/platform` `TestPlatformCapacity_FullRebuildLargePool`、`TestPlatformCapacity_NotifyDirtyThroughput`、`TestPlatformCapacity_ViewRangeThroughput`；`internal/routing` `TestRouterCapacity_ConcurrentRouteRequests`、`TestRouterCapacity_LeaseManagement`、`TestRouterCapacity_IPLoadTracking`；`internal/topology` `TestPoolCapacity_BulkImport`、`TestPoolCapacity_LiveNodeOutboundCreation`、`TestPoolCapacity_ConcurrentAccess` |
| 定时轮换 | `internal/routing` `TestScheduledRotator_Basic`、`TestScheduledRotator_NoRotationWhenDisabled`、`TestScheduledRotator_OnlyRotatesOldLeases`、`TestScheduledRotator_StopGracefully`、`TestScheduledRotator_ParallelPlatformProcessing` |
| 资源安全与日志脱敏 | `internal/netutil` `TestResourceLimitsApplyToChunkedAndDecompressedResponses`、`TestOutboundResourceResponseIsBounded`、`TestResourceErrorsDoNotExposeSubscriptionCredentials`、`TestResourceRedirectDoesNotForwardOriginCredentials`；`internal/proxy` `TestRequestLogTargetURLPrivacy`、`TestRequestLogHeaderPrivacyBeforeTruncation`、`TestRequestLogUpstreamErrorURLPrivacy`、`TestReverseProxy_E2ELogPrivacyPreservesForwardingAndLease` |

### 未随本次移植落地

- `internal/publicsource`（10 个上游测试文件）：Prism 没有对应包。
- `internal/config` 的 `TestLoadEnvConfig_PublicSourceOverrides` 与 `TestLoadEnvConfig_PublicSourceRequiresSourcesWhenEnabled`：对应上游的 public-source 配置项（`PUBLIC_SOURCE_*`）在 Prism 中不存在，这两个用例随该功能一起不移植。
- `internal/inspection` 的 4 个 Prism 测试（`ippure_test`、`manager_test`、`provider_test`、`tor_registry_test`）与 `internal/api` 的 `handler_ippure_test`、`handler_quality_test`：仍未移植，**原来的理由依然成立**——`ClaimInspection` / `LoadQualityRecords` 只作为 `internal/inspection/manager.go` 里 `inspection.Store` 接口的方法存在并被 `NewManager` 的代码路径调用，但树里没有任何类型实现该接口，也没有任何地方构造 manager。核验记录见 `docs/PROTOCOLS.md` §10.10（该节此前误称这两个方法“已实现”，已更正）。
- WireGuard 用例（`internal/outbound` 的 `wireguard` / `wireguard-domain` 两个表项）：sing-box 1.14 已移除 WG outbound，改 endpoint 形式由 WP06 完成，本次先不保留这两个用例。
