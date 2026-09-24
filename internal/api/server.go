package api

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"

	"prism/internal/config"
	"prism/internal/metrics"
	"prism/internal/requestlog"
	"prism/internal/service"
)

// Server wraps the HTTP server and mux for the Prism API.
type Server struct {
	httpServer *http.Server
	mux        *http.ServeMux
}

// NewServer creates a new API server wired with all routes.
// cp may be nil if the control plane is not yet initialized.
func NewServer(
	port int,
	adminToken string,
	systemInfo service.SystemInfo,
	runtimeCfg *atomic.Pointer[config.RuntimeConfig],
	envCfg *config.EnvConfig,
	cp *service.ControlPlaneService,
	apiMaxBodyBytes int64,
	requestlogRepo *requestlog.Repo,
	metricsManager *metrics.Manager,
) *Server {
	return NewServerWithAddress(
		"",
		port,
		adminToken,
		systemInfo,
		runtimeCfg,
		envCfg,
		cp,
		apiMaxBodyBytes,
		requestlogRepo,
		metricsManager,
	)
}

// NewServerWithAddress creates a new API server with an explicit listen address.
func NewServerWithAddress(
	listenAddress string,
	port int,
	adminToken string,
	systemInfo service.SystemInfo,
	runtimeCfg *atomic.Pointer[config.RuntimeConfig],
	envCfg *config.EnvConfig,
	cp *service.ControlPlaneService,
	apiMaxBodyBytes int64,
	requestlogRepo *requestlog.Repo,
	metricsManager *metrics.Manager,
) *Server {
	mux := http.NewServeMux()

	// Public (no auth)
	mux.Handle("GET /healthz", HandleHealthz())

	// WebUI routes (no auth - served under /ui/).
	//
	// newWebUIHandler inspects the full "/ui/" prefixed path itself, so it must
	// be registered without http.StripPrefix.
	mux.Handle("/", newRootRedirectHandler())
	mux.Handle("/ui", newUIRootRedirectHandler())
	mux.Handle("/ui/", newWebUIHandler())

	// Authenticated routes
	authed := http.NewServeMux()
	authed.Handle("GET /api/v1/system/info", HandleSystemInfo(systemInfo))
	authed.Handle("GET /api/v1/system/config", HandleSystemConfig(runtimeCfg))
	authed.Handle("GET /api/v1/system/config/default", HandleSystemDefaultConfig())
	authed.Handle("GET /api/v1/system/config/env", HandleSystemEnvConfig(envCfg))
	authed.Handle("GET /api/v1/system/capabilities", HandleSystemCapabilities())

	if cp != nil {
		// System config mutations.
		authed.Handle("PATCH /api/v1/system/config", HandlePatchSystemConfig(cp))

		// Platforms.
		authed.Handle("GET /api/v1/platforms", HandleListPlatforms(cp))
		authed.Handle("POST /api/v1/platforms", HandleCreatePlatform(cp))
		authed.Handle("POST /api/v1/platforms/preview-filter", HandlePreviewFilter(cp))
		authed.Handle("GET /api/v1/platforms/{id}", HandleGetPlatform(cp))
		authed.Handle("PATCH /api/v1/platforms/{id}", HandleUpdatePlatform(cp))
		authed.Handle("DELETE /api/v1/platforms/{id}", HandleDeletePlatform(cp))
		authed.Handle("POST /api/v1/platforms/{id}/actions/reset-to-default", HandleResetPlatform(cp))
		authed.Handle("POST /api/v1/platforms/{id}/actions/rebuild-routable-view", HandleRebuildPlatform(cp))

		authed.Handle("GET /api/v1/platforms/{id}/nodes/{hash}/explain", HandleExplainPlatformNode(cp))
		// Endpoints.
		authed.Handle("GET /api/v1/endpoints", HandleListEndpoints(cp))
		authed.Handle("POST /api/v1/endpoints", HandleCreateEndpoint(cp))
		authed.Handle("GET /api/v1/endpoints/{id}", HandleGetEndpoint(cp))
		authed.Handle("PATCH /api/v1/endpoints/{id}", HandleUpdateEndpoint(cp))
		authed.Handle("DELETE /api/v1/endpoints/{id}", HandleDeleteEndpoint(cp))

		// Leases (under platforms).
		authed.Handle("GET /api/v1/platforms/{id}/leases", HandleListLeases(cp))
		authed.Handle("DELETE /api/v1/platforms/{id}/leases", HandleDeleteAllLeases(cp))
		authed.Handle("GET /api/v1/platforms/{id}/leases/{account}", HandleGetLease(cp))
		authed.Handle("DELETE /api/v1/platforms/{id}/leases/{account}", HandleDeleteLease(cp))
		authed.Handle("GET /api/v1/platforms/{id}/ip-load", HandleIPLoad(cp))

		// Subscriptions.
		authed.Handle("GET /api/v1/subscriptions", HandleListSubscriptions(cp))
		authed.Handle("POST /api/v1/subscriptions", HandleCreateSubscription(cp))
		authed.Handle("GET /api/v1/subscriptions/{id}", HandleGetSubscription(cp))
		authed.Handle("PATCH /api/v1/subscriptions/{id}", HandleUpdateSubscription(cp))
		authed.Handle("DELETE /api/v1/subscriptions/{id}", HandleDeleteSubscription(cp))
		authed.Handle("POST /api/v1/subscriptions/{id}/actions/refresh", HandleRefreshSubscription(cp))
		authed.Handle("POST /api/v1/subscriptions/{id}/actions/cleanup-circuit-open-nodes", HandleCleanupSubscriptionCircuitOpenNodes(cp))
		authed.Handle("GET /api/v1/subscriptions/{id}/parse-report", HandleGetSubscriptionParseReport(cp))

		// Account header rules.
		authed.Handle("GET /api/v1/account-header-rules", HandleListRules(cp))
		// Canonical route (DESIGN.md): url_prefix comes from path parameter only.
		authed.Handle("PUT /api/v1/account-header-rules/{prefix...}", HandleUpsertRule(cp))
		authed.Handle("POST /api/v1/account-header-rules:resolve", HandleResolveRule(cp))
		authed.Handle("DELETE /api/v1/account-header-rules/{prefix...}", HandleDeleteRule(cp))

		// Nodes.
		authed.Handle("GET /api/v1/nodes", HandleListNodes(cp))
		authed.Handle("GET /api/v1/nodes/export", HandleExportNodes(cp))
		authed.Handle("GET /api/v1/nodes/{hash}", HandleGetNode(cp))
		authed.Handle("POST /api/v1/nodes/{hash}/actions/probe-egress", HandleProbeEgress(cp))
		authed.Handle("POST /api/v1/nodes/{hash}/actions/probe-latency", HandleProbeLatency(cp))
		authed.Handle("POST /api/v1/nodes/{hash}/actions/probe-quality", HandleProbeQuality(cp))
		authed.Handle("POST /api/v1/nodes/{hash}/actions/review-ippure", HandleReviewIPPure(cp))
		authed.Handle("GET /api/v1/quality/status", HandleQualityStatus(cp))
		authed.Handle("GET /api/v1/quality/assessments", HandleQualityList(cp))
		authed.Handle("GET /api/v1/quality/ip/{ip}", HandleQualityIP(cp))
		authed.Handle("POST /api/v1/quality/ip/{ip}/actions/probe", HandleQualityProbeIP(cp))

		// Intel store and batch jobs (WP08 §7). The SSE stream is registered
		// outside this mux so it can also accept ?access_token=.
		authed.Handle("POST /api/v1/intel/jobs", HandleIntelCreateJob(cp))
		authed.Handle("GET /api/v1/intel/jobs", HandleIntelListJobs(cp))
		authed.Handle("GET /api/v1/intel/jobs/{id}", HandleIntelGetJob(cp))
		authed.Handle("GET /api/v1/intel/jobs/{id}/items", HandleIntelListJobItems(cp))
		authed.Handle("POST /api/v1/intel/jobs/{id}/actions/cancel", HandleIntelCancelJob(cp))
		authed.Handle("POST /api/v1/intel/jobs/{id}/actions/retry-failed", HandleIntelRetryFailedJob(cp))
		authed.Handle("GET /api/v1/intel/status", HandleIntelStatus(cp))
		authed.Handle("GET /api/v1/intel/nodes/{hash}", HandleIntelNode(cp))
		authed.Handle("GET /api/v1/intel/ip/{ip}", HandleIntelIP(cp))
		// Data source and unlock check settings (WP09 §4/§5.4).
		authed.Handle("GET /api/v1/intel/providers", HandleIntelListProviders(cp))
		authed.Handle("PATCH /api/v1/intel/providers/{id}", HandleIntelUpdateProvider(cp))
		authed.Handle("POST /api/v1/intel/providers/{id}/actions/resume", HandleIntelResumeProvider(cp))
		authed.Handle("POST /api/v1/intel/providers/{id}/actions/refresh", HandleIntelRefreshProvider(cp))
		authed.Handle("GET /api/v1/intel/checks", HandleIntelListChecks(cp))
		authed.Handle("PATCH /api/v1/intel/checks/{id}", HandleIntelUpdateCheck(cp))

		// GeoIP.
		authed.Handle("GET /api/v1/geoip/status", HandleGeoIPStatus(cp))
		authed.Handle("GET /api/v1/geoip/lookup", HandleGeoIPLookup(cp))
		authed.Handle("POST /api/v1/geoip/lookup", HandleGeoIPLookupPost(cp))
		authed.Handle("POST /api/v1/geoip/actions/update-now", HandleGeoIPUpdate(cp))
		// Export profiles and the public subscription they mint (WP11 §4.2/§4.3).
		authed.Handle("GET /api/v1/export-profiles", HandleListExportProfiles(cp))
		authed.Handle("POST /api/v1/export-profiles", HandleCreateExportProfile(cp))
		authed.Handle("GET /api/v1/export-profiles/{id}", HandleGetExportProfile(cp))
		authed.Handle("PATCH /api/v1/export-profiles/{id}", HandleUpdateExportProfile(cp))
		authed.Handle("DELETE /api/v1/export-profiles/{id}", HandleDeleteExportProfile(cp))
		authed.Handle("POST /api/v1/export-profiles/{id}/actions/rotate-token", HandleRotateExportProfileToken(cp))
	}
	// Public subscription endpoints (WP11 §4.3). They are registered on the main
	// listener without the admin token: the token in the path is the credential.
	if cp != nil {
		subscriptionHandler := NewSubscriptionHandler(cp)
		mux.Handle("/sub/", subscriptionHandler)
		mux.Handle("GET /sub/{token}", subscriptionHandler)
	}

	// Request log endpoints (always registered if repo is available).
	if requestlogRepo != nil {
		authed.Handle("GET /api/v1/request-logs", HandleListRequestLogs(requestlogRepo))
		authed.Handle("GET /api/v1/request-logs/{log_id}", HandleGetRequestLog(requestlogRepo))
		authed.Handle("GET /api/v1/request-logs/{log_id}/payloads", HandleGetRequestLogPayloads(requestlogRepo))
	}

	// Metrics endpoints.
	if metricsManager != nil {
		// Realtime (ring buffer).
		authed.Handle("GET /api/v1/metrics/realtime/throughput", HandleRealtimeThroughput(metricsManager))
		authed.Handle("GET /api/v1/metrics/realtime/connections", HandleRealtimeConnections(metricsManager))
		authed.Handle("GET /api/v1/metrics/realtime/leases", HandleRealtimeLeases(metricsManager))

		// History (metrics.db bucket).
		authed.Handle("GET /api/v1/metrics/history/traffic", HandleHistoryTraffic(metricsManager))
		authed.Handle("GET /api/v1/metrics/history/requests", HandleHistoryRequests(metricsManager))
		authed.Handle("GET /api/v1/metrics/history/access-latency", HandleHistoryAccessLatency(metricsManager))
		authed.Handle("GET /api/v1/metrics/history/probes", HandleHistoryProbes(metricsManager))
		authed.Handle("GET /api/v1/metrics/history/node-pool", HandleHistoryNodePool(metricsManager))
		authed.Handle("GET /api/v1/metrics/history/lease-lifetime", HandleHistoryLeaseLifetime(metricsManager))

		// Snapshots (realtime computed).
		authed.Handle("GET /api/v1/metrics/snapshots/node-pool", HandleSnapshotNodePool(metricsManager))
		authed.Handle("GET /api/v1/metrics/snapshots/platform-node-pool", HandleSnapshotPlatformNodePool(metricsManager))
		authed.Handle("GET /api/v1/metrics/snapshots/node-latency-distribution", HandleSnapshotNodeLatencyDistribution(metricsManager))
	}

	limitedAuthed := RequestBodyLimitMiddleware(apiMaxBodyBytes, authed)

	// Management write auditing (WP04 §4.7). The audit middleware stays inside
	// the request-body limit so its payload peek cannot read unbounded data.
	var authedHandler http.Handler = limitedAuthed
	if cp != nil && cp.Engine != nil {
		authed.Handle("GET /api/v1/audit-logs", HandleListAuditLogs(cp.Engine))
		authedHandler = AuditMiddleware(cp.Engine, adminToken, apiMaxBodyBytes, limitedAuthed)
	}

	// Login failure limiting (WP04 §4.6). Only failed authentications count and
	// X-Forwarded-For is honoured solely for PRISM_TRUSTED_PROXIES.
	var trustedProxies []string
	if envCfg != nil {
		trustedProxies = envCfg.TrustedProxies
	}
	authLimiter := NewAuthFailureLimiter(
		authFailuresPerWindow,
		authFailureWindow,
		authFailureBlock,
		trustedProxies,
	)
	mux.Handle("GET /api/v1/intel/jobs/{id}/events", HandleIntelJobEvents(adminToken, authLimiter, cp))
	mux.Handle("/api/", AuthMiddleware(adminToken, authLimiter, authedHandler))

	srv := &http.Server{
		Addr:    net.JoinHostPort(listenAddress, strconv.Itoa(port)),
		Handler: mux,
	}

	return &Server{
		httpServer: srv,
		mux:        mux,
	}
}

// ListenAndServe starts the HTTP server. It blocks until the server stops.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Handler returns the underlying http.Handler for testing.
func (s *Server) Handler() http.Handler {
	return s.mux
}
