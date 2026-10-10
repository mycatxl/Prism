package api

import (
	"net/http"
	"sync/atomic"
	"time"

	"prism/internal/config"
	"prism/internal/metrics"
	"prism/internal/requestlog"
	"prism/internal/service"
)

// Connection-level bounds of every Prism HTTP listener (WP04 §4.6).
//
// ReadHeaderTimeout bounds how long a client may take to send its request
// headers, which is what stops a slowloris-style hold on a connection.
// IdleTimeout bounds an idle keep-alive connection between requests.
// MaxHeaderBytes caps the header block; 1 MiB is the net/http default, stated
// here so the bound is visible next to the request-body limit.
//
// WriteTimeout is deliberately not set: GET /api/v1/intel/jobs/{id}/events is a
// long-lived SSE stream that writes keep-alive frames every sseKeepAlive and is
// meant to stay open for the whole life of a job. A write deadline would cut
// every progress stream short. ReadTimeout is unset for the same reason it is
// redundant here: ReadHeaderTimeout already bounds the header phase, and a
// whole-request deadline would also bound a streaming response.
//
// These values are exported because the listeners that are actually served live
// in cmd/prism: the inbound demux server on the proxy/management port and the
// optional PRISM_ADMIN_LISTEN listener. Both must build their http.Server
// through NewListenerServer so the bounds cannot drift apart.
const (
	ReadHeaderTimeout = 10 * time.Second
	IdleTimeout       = 120 * time.Second
	MaxHeaderBytes    = 1 << 20
)

// NewListenerServer builds the http.Server every Prism listener serves.
//
// It is the single place the connection bounds above are applied, so a new
// listener cannot be added with them missing (a bare &http.Server{Handler: h}
// leaves ReadHeaderTimeout zero, which lets one client hold a connection open
// forever with a half-sent header).
//
// WriteTimeout and ReadTimeout stay unset on purpose: see the constant block.
func NewListenerServer(handler http.Handler) *http.Server {
	if handler == nil {
		handler = http.NotFoundHandler()
	}
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: ReadHeaderTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    MaxHeaderBytes,
	}
}

// Server wires the Prism API routes into one handler.
//
// It owns the mux only. The listeners are created and served by cmd/prism
// (see NewListenerServer), so there is exactly one set of connection bounds in
// the process.
type Server struct {
	mux *http.ServeMux
}

// NewServer creates a new API server wired with all routes.
// cp may be nil if the control plane is not yet initialized.
//
// port is part of the constructor contract only: nothing is bound here, so the
// value is not read. The listener is created and served by cmd/prism.
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
	panelEgress PanelEgressProvider,
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
		panelEgress,
	)
}

// NewServerWithAddress creates a new API server with an explicit listen address.
// panelEgress may be nil: the panel egress fields stay empty.
//
// listenAddress and port are part of the constructor contract only: this value
// only ever produces the mux, and cmd/prism binds the address itself (through
// NewListenerServer and the endpoint runtime). Passing them here is harmless but
// does not configure anything.
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
	panelEgress PanelEgressProvider,
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
	mux.Handle("/ui/", newWebUIHandler(adminToken != ""))

	// Authenticated routes
	authed := http.NewServeMux()
	authed.Handle("GET /api/v1/system/info", HandleSystemInfo(systemInfo, panelEgress))
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
		authed.Handle("POST /api/v1/platforms/preview-scope", HandlePreviewPlatformScope(cp))
		authed.Handle("GET /api/v1/platforms/node-facets", HandlePlatformNodeFacets(cp))
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
		authed.Handle("POST /api/v1/platforms/{id}/leases/{account}/actions/rotate", HandleRotateLease(cp))
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

	// Management write auditing (WP04 §4.7). The request-body limit wraps the
	// audit middleware, so the raw body its payload peek reads is already bounded
	// by apiMaxBodyBytes before the peek's own 64 KiB cap applies (G-12).
	var authedHandler http.Handler = authed
	if cp != nil && cp.Engine != nil {
		authed.Handle("GET /api/v1/audit-logs", HandleListAuditLogs(cp.Engine))
		authedHandler = AuditMiddleware(cp.Engine, adminToken, apiMaxBodyBytes, authed)
	}
	authedHandler = RequestBodyLimitMiddleware(apiMaxBodyBytes, authedHandler)

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

	// Connection hygiene belongs to the listener, not to this value: cmd/prism
	// builds every served http.Server through NewListenerServer, which is the
	// single place ReadHeaderTimeout/IdleTimeout/MaxHeaderBytes are applied.
	return &Server{mux: mux}
}

// Handler returns the underlying http.Handler for testing.
func (s *Server) Handler() http.Handler {
	return s.mux
}
