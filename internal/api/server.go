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

// Connection-level bounds of every Prism HTTP listener.
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
	// RequestBodyTimeout bounds the body phase of a request that carries one.
	// ReadHeaderTimeout only covers the headers, and ReadTimeout cannot be used
	// here because it also bounds a streaming response (the SSE endpoint); this
	// deadline is armed per request and only when a body is present.
	RequestBodyTimeout = 60 * time.Second
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

// AuthDeps is the authentication surface the management API mounts.
//
// Identity is mandatory for management routes. Without it the server still
// builds its public health and UI routes, but every /api/ request fails closed.
type AuthDeps struct {
	Identity  *Identity
	Handlers  *AuthHandlers
	Issuer    string
	Blocklist *AuthFailureLimiter
}

// NewServer creates a new API server wired with all routes.
func NewServer(
	port int,
	systemInfo service.SystemInfo,
	runtimeCfg *atomic.Pointer[config.RuntimeConfig],
	envCfg *config.EnvConfig,
	cp *service.ControlPlaneService,
	apiMaxBodyBytes int64,
	requestlogRepo *requestlog.Repo,
	metricsManager *metrics.Manager,
	panelEgress PanelEgressProvider,
	authDeps *AuthDeps,
) *Server {
	return NewServerWithAddress(
		"",
		port,
		systemInfo,
		runtimeCfg,
		envCfg,
		cp,
		apiMaxBodyBytes,
		requestlogRepo,
		metricsManager,
		panelEgress,
		authDeps,
	)
}

// NewServerWithAddress creates a new API server with an explicit listen address.
func NewServerWithAddress(
	listenAddress string,
	port int,
	systemInfo service.SystemInfo,
	runtimeCfg *atomic.Pointer[config.RuntimeConfig],
	envCfg *config.EnvConfig,
	cp *service.ControlPlaneService,
	apiMaxBodyBytes int64,
	requestlogRepo *requestlog.Repo,
	metricsManager *metrics.Manager,
	panelEgress PanelEgressProvider,
	authDeps *AuthDeps,
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

	// The console's own view of the caller. It resolves the session cookie
	// directly rather than through the identity middleware, because /ui/ is not
	// behind that middleware - the login page itself is served from there.
	var sessionInfo func(*http.Request) sessionInfoForWebUI
	if authDeps != nil && authDeps.Identity != nil {
		sessionInfo = authDeps.Identity.sessionInfo
	}
	mux.Handle("/ui/", newWebUIHandler(true, sessionInfo))

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
		// Canonical route: url_prefix comes from path parameter only.
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

		// Intel store and batch jobs. The SSE stream is registered
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
		// Data source and unlock check settings.
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

		// The multi-tenant administration surface.
		//
		// Everything below is the administrator's, and it is private by
		// default: the role gate lets a user-role session reach /api/v1/auth
		// and /api/v1/me and refuses the rest, so a route added here is
		// protected by existing rather than by remembering to protect it.
		authed.Handle("GET /api/v1/admin/plans", HandleAdminListPlans(cp))
		authed.Handle("POST /api/v1/admin/plans", HandleAdminCreatePlan(cp))
		authed.Handle("GET /api/v1/admin/plans/{id}", HandleAdminGetPlan(cp))
		authed.Handle("PATCH /api/v1/admin/plans/{id}", HandleAdminUpdatePlan(cp))
		authed.Handle("DELETE /api/v1/admin/plans/{id}", HandleAdminDeletePlan(cp))

		authed.Handle("GET /api/v1/admin/users", HandleAdminListUsers(cp))
		authed.Handle("POST /api/v1/admin/users", HandleAdminCreateUser(cp))
		authed.Handle("GET /api/v1/admin/users/{id}", HandleAdminGetUser(cp))
		authed.Handle("PATCH /api/v1/admin/users/{id}", HandleAdminUpdateUser(cp))
		authed.Handle("DELETE /api/v1/admin/users/{id}", HandleAdminDeleteUser(cp))
		authed.Handle("GET /api/v1/admin/users/{id}/keys", HandleAdminListUserKeys(cp))
		authed.Handle("POST /api/v1/admin/users/{id}/keys", HandleAdminCreateUserKey(cp))
		authed.Handle("PATCH /api/v1/admin/users/{id}/keys/{keyID}", HandleAdminUpdateUserKey(cp))
		authed.Handle("DELETE /api/v1/admin/users/{id}/keys/{keyID}", HandleAdminDeleteUserKey(cp))
		authed.Handle("GET /api/v1/admin/users/{id}/usage", HandleAdminGetUserUsage(cp))

		authed.Handle("GET /api/v1/admin/subscriptions", HandleAdminListSubscriptions(cp))
		authed.Handle("POST /api/v1/admin/subscriptions", HandleAdminCreateSubscription(cp))
		authed.Handle("GET /api/v1/admin/subscriptions/{id}", HandleAdminGetSubscription(cp))
		authed.Handle("PATCH /api/v1/admin/subscriptions/{id}", HandleAdminUpdateSubscription(cp))
		authed.Handle("DELETE /api/v1/admin/subscriptions/{id}", HandleAdminDeleteSubscription(cp))

		authed.Handle("GET /api/v1/admin/invites", HandleAdminListInvites(cp))
		authed.Handle("POST /api/v1/admin/invites", HandleAdminCreateInvite(cp))
		authed.Handle("DELETE /api/v1/admin/invites/{id}", HandleAdminDeleteInvite(cp))

		authed.Handle("GET /api/v1/admin/usage/summary", HandleAdminUsageSummary(cp))
		authed.Handle("GET /api/v1/admin/usage/users", HandleAdminUsageUsers(cp))
		authed.Handle("GET /api/v1/admin/usage/daily", HandleAdminUsageDaily(cp))
		authed.Handle("GET /api/v1/admin/usage/platforms", HandleAdminUsagePlatforms(cp))

		// The user's own panel. Every handler keys off the session's actor, so
		// there is no id in these paths to point at somebody else.
		authed.Handle("GET /api/v1/me/overview", HandleMeOverview(cp))
		authed.Handle("GET /api/v1/me/subscription", HandleMeSubscription(cp))
		authed.Handle("GET /api/v1/me/usage", HandleMeUsage(cp))
		authed.Handle("GET /api/v1/me/keys", HandleMeKeys(cp))
		authed.Handle("POST /api/v1/me/keys", HandleMeCreateKey(cp))
		authed.Handle("DELETE /api/v1/me/keys/{keyID}", HandleMeDeleteKey(cp))
		authed.Handle("GET /api/v1/me/security/sessions", HandleMeSessions(cp))
		authed.Handle("DELETE /api/v1/me/security/sessions/{sessionID}", HandleMeRevokeSession(cp))
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

	// Management write auditing. The request-body limit wraps the
	// audit middleware, so the raw body its payload peek reads is already bounded
	// by apiMaxBodyBytes before the peek's own 64 KiB cap applies.
	var authedHandler http.Handler = authed
	if cp != nil && cp.Engine != nil {
		authed.Handle("GET /api/v1/audit-logs", HandleListAuditLogs(cp.Engine))
		authedHandler = AuditMiddleware(cp.Engine, apiMaxBodyBytes, authed)
	}
	authedHandler = RequestBodyLimitMiddleware(apiMaxBodyBytes, authedHandler)
	// The body phase gets its own bound: ReadHeaderTimeout stops at the end of
	// the header block, and ReadTimeout has to stay unset for the SSE stream.
	authedHandler = RequestBodyDeadlineMiddleware(RequestBodyTimeout, authedHandler)

	// The role gate sits inside the identity middleware, which is what makes
	// its 403 mean "the credential is fine, the role is not". It wraps the
	// audit and body handling as well, so a request from a user-role session
	// never reaches the management handlers and is not recorded as a
	// management write.
	authedHandler = requireAdmin(authedHandler)

	// Login failure limiting. Only failed authentications count and
	// X-Forwarded-For is honoured solely for PRISM_TRUSTED_PROXIES.
	var trustedProxies []string
	if envCfg != nil {
		trustedProxies = envCfg.TrustedProxies
	}
	authLimiter := NewLoginBlocklist(trustedProxies)
	if authDeps != nil && authDeps.Blocklist != nil {
		// A caller that built the session service already created the limiter
		// the login handler counts against. Reusing it is what makes "too many
		// failed attempts" mean the same thing to both paths.
		authLimiter = authDeps.Blocklist
	}
	mux.Handle("GET /api/v1/intel/jobs/{id}/events", HandleIntelJobEvents(cp))

	// Authentication: the console's session routes, and the middleware that
	// guards everything else.
	//
	// They are registered on the authed mux even though login is reachable
	// without an identity - the identity middleware lets that one path through
	// - because a route has to exist somewhere for the pattern to match and for
	// the handler to be reachable at all.
	if authDeps != nil && authDeps.Handlers != nil {
		handlers := authDeps.Handlers
		// The limiter is created here, so the handler is told how to name a
		// client after the fact. Without it a failed login would not count
		// towards the block that the middleware enforces.
		handlers.ClientKey = authLimiter.ClientIP
		authed.Handle("POST /api/v1/auth/login", handlers.HandleLogin())
		// Sign-up is the second route reachable without an identity. It is
		// always registered: the handler refuses when the deployment has
		// registration switched off, and an endpoint that says no is better
		// than a console that cannot tell "closed" from "not built".
		authed.Handle("POST /api/v1/auth/register", handlers.HandleRegister())
		authed.Handle("POST /api/v1/auth/logout", handlers.HandleLogout())
		authed.Handle("GET /api/v1/auth/session", handlers.HandleSession())
		authed.Handle("POST /api/v1/auth/password", handlers.HandleChangePassword())
		authed.Handle("POST /api/v1/auth/totp/enrol", handlers.HandleStartTOTPEnrolment(authDeps.Issuer))
		authed.Handle("POST /api/v1/auth/totp/confirm", handlers.HandleConfirmTOTPEnrolment())
		authed.Handle("POST /api/v1/auth/totp/disable", handlers.HandleDisableTOTP())
	}
	if authDeps != nil && authDeps.Identity != nil {
		authDeps.Identity.FailureLimiter = authLimiter
	}
	if authDeps != nil && authDeps.Identity != nil {
		// The identity middleware authenticates both console sessions and admin
		// access keys, while this limiter blocks addresses with repeated failures.
		authedHandler = limiterMiddleware(authLimiter, authedHandler)
		mux.Handle("/api/", authDeps.Identity.Middleware(authedHandler))
	} else {
		// No identity is a safe failure mode: the management surface must never
		// silently fall back to a shared credential.
		mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		}))
	}

	// Connection hygiene belongs to the listener, not to this value: cmd/prism
	// builds every served http.Server through NewListenerServer, which is the
	// single place ReadHeaderTimeout/IdleTimeout/MaxHeaderBytes are applied.
	return &Server{mux: mux}
}

// Handler returns the underlying http.Handler for testing.
func (s *Server) Handler() http.Handler {
	return s.mux
}
