package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"prism/internal/api"
	"prism/internal/buildinfo"
	"prism/internal/config"
	"prism/internal/geoip"
	"prism/internal/intel"
	"prism/internal/intel/providers"
	"prism/internal/intel/store"
	"prism/internal/metrics"
	"prism/internal/netutil"
	"prism/internal/node"
	"prism/internal/proxy"
	"prism/internal/requestlog"
	"prism/internal/routing"
	"prism/internal/service"
	"prism/internal/state"
)

type prismApp struct {
	envCfg          *config.EnvConfig
	runtimeCfg      *atomic.Pointer[config.RuntimeConfig]
	accountMatcher  *proxy.AccountMatcherRuntime
	geoSvc          *geoip.Service
	topoRuntime     *topologyRuntime
	flushWorker     *state.CacheFlushWorker
	metricsDB       *metrics.MetricsRepo
	metricsManager  *metrics.Manager
	requestlogRepo  *requestlog.Repo
	requestlogSvc   *requestlog.Service
	endpointManager *endpointRuntimeManager
	transportPool   *proxy.OutboundTransportPool
	apiHandler      http.Handler
	adminListener   *adminListener
	stopAuditPruner func()

	// WP08: the authoritative intel store, its in-memory projection and the job
	// executor.
	stateEngine *state.StateEngine
	intelStore  *store.Store
	intelSvc    *intel.Service
	// WP09 §3: the background refresher of the offline databases.
	geoDBs *providers.GeoManager
}

// run assembles and runs the Prism service.
//
// Assembly order (WP03 §4):
//
//  1. Load .env, then call config.LoadEnvConfig() (RESIN_* fallbacks are WP05).
//
//  2. state.PersistenceBootstrap(stateDir, cacheDir) returns the engine.
//
//  3. Open the intel store (WP08; skipped until it exists).
//
//  3. Open intel.db (WP08; the authoritative detection store).
//
//  4. loadRuntimeConfig(engine).
//
//  5. Create the GeoIP service (upstream implementation; WP09 adds offline DBs).
//
//  6. Create the node builder: outbound.NewSingboxBuilderWithConfig for now,
//     WP06 replaces it with outbound.NewSingboxRuntime.
//
//  7. Create the topology objects: topology.NewGlobalNodePool (GeoLookup wired,
//     QualityLookup stays nil until WP10), SubscriptionManager,
//     SubscriptionScheduler and EphemeralCleaner.
//
//  8. Create probe.NewProbeManager and register SetOnProbeEvent (metrics);
//     WP08 attaches SetOnEgressObserved.
//
//  9. Wire pool.SetOnNodeAdded/SetOnNodeRemoved like upstream.
//
//  10. Create routing.NewRouter, routing.NewLeaseCleaner and the Prism-only
//     routing.NewScheduledRotator (started and stopped with the router).
//
//  11. Create metrics, request log and state.NewCacheFlushWorker.
//
//  12. Create the intel job executor (WP08; skipped until it exists).
//
//  13. Create service.ControlPlaneService, api.NewServer,
//     api.NewTokenActionHandler, the inbound mux/demux, the endpoint runtime
//     and the optional admin listener.
//
//  14. Start the background services and wait for a signal.
func run() error {
	// 1. Load .env first: existing process environment variables win.
	if err := loadDotenvFile(envFileName); err != nil {
		return err
	}
	envCfg, err := config.LoadEnvConfig()
	if err != nil {
		return err
	}

	// 2. Open state.db and cache.db and return the shared engine.
	engine, dbCloser, err := state.PersistenceBootstrap(envCfg.StateDir, envCfg.CacheDir)
	if err != nil {
		return fmt.Errorf("persistence bootstrap: %w", err)
	}
	log.Println("Persistence bootstrap complete")

	// 3. Open the intel store: intel.db is a separate SQLite file with its own
	// open path, pragmas and 0600 file mode (WP08 §2).
	intelStore, err := store.Open(filepath.Join(envCfg.StateDir, store.FileName))
	if err != nil {
		_ = dbCloser.Close()
		return fmt.Errorf("open intel store: %w", err)
	}
	log.Println("Intel store ready")

	// 4.-12. Assemble runtime config, GeoIP, topology, routing, observability and
	// the control plane in the documented order.
	app, err := newPrismApp(envCfg, engine, intelStore)
	if err != nil {
		_ = intelStore.Close()
		_ = dbCloser.Close()
		return err
	}

	// Record the pid so `prism restore` can detect a live instance even when its
	// listen port is unreachable (best effort).
	removePidFile := writePIDFile(envCfg.StateDir)
	defer removePidFile()

	// 13./14. Start the listeners and wait for SIGINT/SIGTERM or a serve error.
	serverErrCh, err := app.startServers()
	if err != nil {
		app.shutdown()
		_ = dbCloser.Close()
		return err
	}
	runtimeErr := waitForShutdown(serverErrCh)

	app.shutdown()

	if err := app.closeIntel(); err != nil {
		log.Printf("Intel store close error: %v", err)
	}
	if err := dbCloser.Close(); err != nil {
		log.Printf("Persistence close error: %v", err)
	}
	if runtimeErr != nil {
		return fmt.Errorf("runtime server error: %w", runtimeErr)
	}
	return nil
}

// writePIDFile records the current process id in stateDir/prism.pid and returns
// a cleanup function. A failure is not fatal: the port probe used by
// `prism restore` still detects a running service.
func writePIDFile(stateDir string) func() {
	if strings.TrimSpace(stateDir) == "" {
		return func() {}
	}
	path := filepath.Join(stateDir, pidFileName)
	if err := writeFile0600(path, strconv.Itoa(os.Getpid())+"\n", false); err != nil {
		log.Printf("Warning: write %s: %v", path, err)
		return func() {}
	}
	return func() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("Warning: remove %s: %v", path, err)
		}
	}
}

func loadDotenvFile(path string) error {
	if err := godotenv.Load(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("load %s: %w", path, err)
	}
	return nil
}

// newPrismApp builds every long-lived runtime object in the WP03 §4 order. The
// numbered steps below match the assembly list documented on run().
func newPrismApp(envCfg *config.EnvConfig, engine *state.StateEngine, intelStore *store.Store) (*prismApp, error) {
	app := &prismApp{
		envCfg:      envCfg,
		runtimeCfg:  &atomic.Pointer[config.RuntimeConfig]{},
		stateEngine: engine,
		intelStore:  intelStore,
	}
	// 4. Runtime configuration (persisted values, defaults otherwise).
	app.runtimeCfg.Store(loadRuntimeConfig(engine))
	if err := ensureDefaultAccountHeaderRule(engine); err != nil {
		return nil, err
	}
	app.accountMatcher = buildAccountMatcher(engine)

	// 5.-10. GeoIP, node builder, topology, probes and routing incl. the
	// scheduled rotator.
	retryDL, err := app.initTopologyRuntime(engine)
	if err != nil {
		return nil, err
	}
	app.wireRetryDownloader(retryDL)

	if err := app.bootstrapFromPersistence(engine); err != nil {
		return nil, err
	}
	// 11. Observability: metrics, request log and the cache flush worker (the
	// worker itself is created while bootstrapping persistence).
	if err := app.initObservability(); err != nil {
		return nil, err
	}
	// 12. Intel job executor: the store, the projection and the batch worker pool
	// (WP08).
	if err := app.initIntelJobs(engine, intelStore); err != nil {
		return nil, err
	}
	// 13. Control plane, API server, mux/demux and the endpoint runtime.
	if err := app.buildNetworkServers(engine); err != nil {
		return nil, err
	}

	// 14. Background services start after assembly; run() then waits for a
	// signal (or a serve error) before shutting everything down again.

	app.startBackgroundServices()
	return app, nil
}

// closeIntel releases the intel.db handle. It runs after shutdown() so the job
// executor and the egress observer have already stopped writing.
func (a *prismApp) closeIntel() error {
	if a == nil || a.intelStore == nil {
		return nil
	}
	err := a.intelStore.Close()
	a.intelStore = nil
	return err
}

func (a *prismApp) initTopologyRuntime(engine *state.StateEngine) (*netutil.RetryDownloader, error) {
	// 5. GeoIP service (upstream implementation; WP09 adds the offline DBs).
	direct := newDirectDownloader(a.envCfg)
	retryDL := &netutil.RetryDownloader{Direct: direct}
	a.geoSvc = newGeoIPService(a.envCfg.CacheDir, a.envCfg.GeoIPUpdateSchedule, retryDL)

	// 6.-9. Node builder (outbound.NewSingboxBuilderWithConfig; WP06 swaps in
	// outbound.NewSingboxRuntime), node pool, probe manager and the
	// SetOnNodeAdded/SetOnNodeRemoved wiring.
	topoRuntime, err := newTopologyRuntime(
		engine,
		a.envCfg,
		a.runtimeCfg,
		a.geoSvc,
		retryDL,
		a.onProbeConnectionLifecycle,
		func(hash node.Hash) {
			if a.transportPool != nil {
				a.transportPool.Evict(hash)
			}
		},
	)
	if err != nil {
		return nil, fmt.Errorf("topology runtime: %w", err)
	}
	a.topoRuntime = topoRuntime

	// 10. Router, lease cleaner and the Prism-only scheduled rotator. The
	// rotator sweeps every 7±3s, deletes over-age leases and emits LeaseRemove
	// events; it is started and stopped together with the router.
	log.Println("OutboundManager initialized with lifecycle callbacks")
	a.topoRuntime.router = routing.NewRouter(routing.RouterConfig{
		Pool: a.topoRuntime.pool,
		Authorities: func() []string {
			return runtimeConfigSnapshot(a.runtimeCfg).LatencyAuthorities
		},
		P2CWindow: func() time.Duration {
			return time.Duration(runtimeConfigSnapshot(a.runtimeCfg).P2CLatencyWindow)
		},
		NodeTagResolver: a.topoRuntime.pool.ResolveNodeDisplayTag,
		// Lease events are emitted synchronously on routing paths.
		// Keep this callback lightweight and non-blocking.
		OnLeaseEvent: func(e routing.LeaseEvent) {
			switch e.Type {
			case routing.LeaseCreate, routing.LeaseTouch, routing.LeaseReplace:
				engine.MarkLease(e.PlatformID, e.Account)
			case routing.LeaseRemove, routing.LeaseExpire:
				engine.MarkLeaseDelete(e.PlatformID, e.Account)
			}
			a.onLeaseEventForMetrics(e)
		},
	})
	a.topoRuntime.leaseCleaner = routing.NewLeaseCleaner(a.topoRuntime.router)
	a.topoRuntime.rotator = routing.NewScheduledRotator(a.topoRuntime.router, a.topoRuntime.pool)
	log.Println("Router, LeaseCleaner and ScheduledRotator initialized")
	return retryDL, nil
}

func (a *prismApp) onProbeConnectionLifecycle(op netutil.ConnLifecycleOp) {
	if a == nil || a.metricsManager == nil {
		return
	}
	switch op {
	case netutil.ConnLifecycleOpen:
		a.metricsManager.OnConnectionLifecycle(proxy.ConnectionOutbound, proxy.ConnectionOpen)
	case netutil.ConnLifecycleClose:
		a.metricsManager.OnConnectionLifecycle(proxy.ConnectionOutbound, proxy.ConnectionClose)
	}
}

func (a *prismApp) onLeaseEventForMetrics(e routing.LeaseEvent) {
	if a == nil || a.metricsManager == nil {
		return
	}

	op := metrics.LeaseOpTouch
	switch e.Type {
	case routing.LeaseCreate:
		op = metrics.LeaseOpCreate
	case routing.LeaseReplace:
		op = metrics.LeaseOpReplace
	case routing.LeaseRemove:
		op = metrics.LeaseOpRemove
	case routing.LeaseExpire:
		op = metrics.LeaseOpExpire
	}

	lifetimeNs := int64(0)
	if e.CreatedAtNs > 0 && op.HasLifetimeSample() {
		lifetimeNs = time.Now().UnixNano() - e.CreatedAtNs
	}

	a.metricsManager.OnLeaseEvent(metrics.LeaseMetricEvent{
		PlatformID: e.PlatformID,
		Op:         op,
		LifetimeNs: lifetimeNs,
	})
}

func (a *prismApp) wireRetryDownloader(retryDL *netutil.RetryDownloader) {
	// Phase 5: Complete RetryDownloader wiring (now that Pool + OutboundManager exist).
	retryDL.NodePicker = a.downloadNodePicker()
	retryDL.ProxyFetch = func(ctx context.Context, hash node.Hash, url string) ([]byte, error) {
		body, _, err := a.topoRuntime.outboundMgr.FetchWithUserAgent(ctx, hash, url, currentDownloadUserAgent())
		return body, err
	}
	log.Println("RetryDownloader wiring complete")
}

// downloadNodePicker selects a node for a download whose direct attempt
// failed: the RetryDownloader then borrows a healthy node instead of the host
// network. WP09 §3 reuses it for the offline databases.
func (a *prismApp) downloadNodePicker() func(target string) (node.Hash, error) {
	return func(target string) (node.Hash, error) {
		if a == nil || a.topoRuntime == nil || a.topoRuntime.router == nil {
			return node.Zero, errors.New("router is not available")
		}
		res, err := a.topoRuntime.router.RouteRequest("", "", target)
		if err != nil {
			return node.Zero, err
		}
		return res.NodeHash, nil
	}
}

func (a *prismApp) bootstrapFromPersistence(engine *state.StateEngine) error {
	// Phase 6: Bootstrap topology data from persistence.
	if err := bootstrapTopology(engine, a.topoRuntime.subManager, a.topoRuntime.pool, a.envCfg); err != nil {
		return err
	}

	// Phase 6.1: Bootstrap nodes (steps 3-6: static, subscription_nodes, dynamic, latency).
	if err := bootstrapNodes(
		engine,
		a.topoRuntime.pool,
		a.topoRuntime.subManager,
		a.topoRuntime.outboundMgr,
		a.envCfg,
		runtimeConfigSnapshot(a.runtimeCfg).LatencyAuthorities,
	); err != nil {
		return err
	}

	// GeoIP moved to step 8 batch 1 (after lease restore, per DESIGN.md).

	// Phase 8.1: Rebuild platform views BEFORE lease restore.
	// DESIGN.md requires step 6 (rebuild) before step 7 (leases).
	a.topoRuntime.pool.RebuildAllPlatforms()
	log.Println("Platform rebuild complete")

	// Phase 9: Restore leases (AFTER rebuild so platform views are populated).
	leases, err := engine.LoadAllLeases()
	if err != nil {
		log.Printf("Warning: load leases: %v", err)
	} else if len(leases) > 0 {
		a.topoRuntime.router.RestoreLeases(leases)
		log.Printf("Restored %d leases from cache.db", len(leases))
	}

	flushReaders := newFlushReaders(a.topoRuntime.pool, a.topoRuntime.subManager, a.topoRuntime.router)
	a.flushWorker = state.NewCacheFlushWorker(
		engine,
		flushReaders,
		func() int { return runtimeConfigSnapshot(a.runtimeCfg).CacheFlushDirtyThreshold },
		func() time.Duration { return time.Duration(runtimeConfigSnapshot(a.runtimeCfg).CacheFlushInterval) },
		5*time.Second, // check tick
	)
	return nil
}

func (a *prismApp) initObservability() error {
	// Phase 10: Initialize observability services.
	requestLogCfg := deriveRequestLogRuntimeSettings(a.envCfg)
	metricsCfg := deriveMetricsManagerSettings(a.envCfg)

	metricsDB, err := metrics.NewMetricsRepo(filepath.Join(a.envCfg.LogDir, "metrics.db"))
	if err != nil {
		return fmt.Errorf("metrics DB: %w", err)
	}
	a.metricsDB = metricsDB

	a.metricsManager = metrics.NewManager(metrics.ManagerConfig{
		Repo:                        a.metricsDB,
		LatencyBinMs:                metricsCfg.LatencyBinMs,
		LatencyOverflowMs:           metricsCfg.LatencyOverflowMs,
		BucketSeconds:               metricsCfg.BucketSeconds,
		ThroughputRealtimeCapacity:  metricsCfg.ThroughputRealtimeCapacity,
		ThroughputIntervalSec:       metricsCfg.ThroughputIntervalSec,
		ConnectionsRealtimeCapacity: metricsCfg.ConnectionsRealtimeCapacity,
		ConnectionsIntervalSec:      metricsCfg.ConnectionsIntervalSec,
		LeasesRealtimeCapacity:      metricsCfg.LeasesRealtimeCapacity,
		LeasesIntervalSec:           metricsCfg.LeasesIntervalSec,
		RuntimeStats: &runtimeStatsAdapter{
			pool:   a.topoRuntime.pool,
			router: a.topoRuntime.router,
			authorities: func() []string {
				return runtimeConfigSnapshot(a.runtimeCfg).LatencyAuthorities
			},
		},
	})

	a.requestlogRepo = requestlog.NewRepo(
		a.envCfg.LogDir,
		requestLogCfg.DBMaxBytes,
		requestLogCfg.DBRetainCount,
	)
	if err := a.requestlogRepo.Open(); err != nil {
		return fmt.Errorf("requestlog repo open: %w", err)
	}
	a.requestlogSvc = requestlog.NewService(requestlog.ServiceConfig{
		Repo:          a.requestlogRepo,
		QueueSize:     requestLogCfg.QueueSize,
		FlushBatch:    requestLogCfg.FlushBatch,
		FlushInterval: requestLogCfg.FlushInterval,
	})
	return nil
}

func (a *prismApp) startBackgroundServices() {
	// --- Step 8 Batch 1: CacheFlushWorker + GeoIP + MetricsManager ---
	a.flushWorker.Start()
	log.Println("Cache flush worker started")

	startGeoIPService(a.geoSvc)
	log.Println("GeoIP service started (batch 1)")

	a.metricsManager.Start()
	log.Println("Metrics manager started (batch 1)")

	// --- Step 8 Batch 2: ProbeManager, RequestLog, LeaseCleaner, EphemeralCleaner ---
	a.topoRuntime.probeMgr.SetOnProbeEvent(func(kind string) {
		a.metricsManager.OnProbeEvent(metrics.ProbeEvent{Kind: metrics.ProbeKind(kind)})
	})

	a.topoRuntime.probeMgr.Start()
	a.topoRuntime.probeMgr.Start()
	log.Println("Probe manager started (batch 2)")

	// WP08 step 12: the egress observer must be attached after the probe manager
	// is running, and the job executor starts with the projection loaded.
	if err := a.startIntel(); err != nil {
		log.Printf("Warning: intel service start: %v", err)
	}

	a.requestlogSvc.Start()
	log.Println("Request log service started (batch 2)")

	// The scheduled rotator shares the lease cleaner's lifecycle: it removes
	// over-age leases for platforms with scheduled rotation enabled.
	a.topoRuntime.rotator.Start()
	log.Println("Scheduled rotator started (batch 2)")

	a.topoRuntime.leaseCleaner.Start()
	log.Println("Lease cleaner started (batch 2)")

	a.topoRuntime.ephemeralCleaner.Start()
	log.Println("Ephemeral cleaner started (batch 2)")

	// --- Step 8 Batch 3: Subscription scheduler (force full refresh on start) ---
	a.topoRuntime.scheduler.Start()
	a.topoRuntime.scheduler.ForceRefreshAllAsync()
	log.Println("Subscription scheduler started; forced full refresh running in background (batch 3)")
}

func (a *prismApp) buildNetworkServers(engine *state.StateEngine) error {
	startedAt := time.Now().UTC()
	systemInfo := service.SystemInfo{
		Version:   buildinfo.Version,
		GitCommit: buildinfo.GitCommit,
		BuildTime: buildinfo.BuildTime,
		BuildTags: buildinfo.TagList(),
		StartedAt: startedAt,
	}

	cpService := &service.ControlPlaneService{
		RuntimeCfg:     a.runtimeCfg,
		EnvCfg:         a.envCfg,
		Engine:         engine,
		Pool:           a.topoRuntime.pool,
		SubMgr:         a.topoRuntime.subManager,
		Scheduler:      a.topoRuntime.scheduler,
		Router:         a.topoRuntime.router,
		ProbeMgr:       a.topoRuntime.probeMgr,
		GeoIP:          a.geoSvc,
		MatcherRuntime: a.accountMatcher,
		Intel:          a.intelSvc,
	}

	// WP04 §4.7: audit retention runs once a day (90 days, at most 100000 rows).
	a.stopAuditPruner = api.StartAuditPruner(engine, 24*time.Hour)

	apiSrv := api.NewServerWithAddress(
		a.envCfg.ListenAddress,
		a.envCfg.ProxyPort,
		a.envCfg.AdminToken,
		systemInfo,
		a.runtimeCfg,
		a.envCfg,
		cpService,
		int64(a.envCfg.APIMaxBodyBytes),
		a.requestlogRepo,
		a.metricsManager,
	)
	// The optional admin listener (PRISM_ADMIN_LISTEN) reuses this handler but
	// only exposes the management paths, never a proxy protocol.
	a.apiHandler = apiSrv.Handler()
	tokenActionHandler := api.NewTokenActionHandler(
		a.envCfg.ProxyToken,
		cpService,
		int64(a.envCfg.APIMaxBodyBytes),
	)

	proxyEvents := a.buildProxyEvents()
	outboundTransportCfg := proxy.OutboundTransportConfig{
		MaxIdleConns:        a.envCfg.ProxyTransportMaxIdleConns,
		MaxIdleConnsPerHost: a.envCfg.ProxyTransportMaxIdleConnsPerHost,
		IdleConnTimeout:     a.envCfg.ProxyTransportIdleConnTimeout,
	}
	if a.transportPool == nil {
		a.transportPool = proxy.NewOutboundTransportPool(outboundTransportCfg)
	}

	// WP04 §4.6: proxy-entry (407/403) failure limiting is opt-in and disabled
	// by default (PRISM_PROXY_AUTH_FAIL_LIMIT=0 keeps Resin behaviour).
	var proxyAuthGuard proxy.AuthFailureGuard
	if a.envCfg.ProxyAuthFailLimit > 0 {
		proxyAuthGuard = api.NewAuthFailureLimiter(
			a.envCfg.ProxyAuthFailLimit,
			time.Minute,
			api.AuthFailureBlockDuration,
			a.envCfg.TrustedProxies,
		)
	}

	forwardProxy := proxy.NewForwardProxy(proxy.ForwardProxyConfig{
		ProxyToken:        a.envCfg.ProxyToken,
		Router:            a.topoRuntime.router,
		Pool:              a.topoRuntime.pool,
		Health:            a.topoRuntime.pool,
		Events:            proxyEvents,
		MetricsSink:       a.metricsManager,
		OutboundTransport: outboundTransportCfg,
		TransportPool:     a.transportPool,
		ProxyBypassRules:  a.envCfg.ProxyBypassRules,
		DirectDenyPrivate: a.envCfg.DirectDenyPrivate,
		AuthGuard:         proxyAuthGuard,
	})

	reverseProxy := proxy.NewReverseProxy(proxy.ReverseProxyConfig{
		ProxyToken:        a.envCfg.ProxyToken,
		Router:            a.topoRuntime.router,
		Pool:              a.topoRuntime.pool,
		PlatformLookup:    a.topoRuntime.pool,
		Health:            a.topoRuntime.pool,
		Matcher:           a.accountMatcher,
		Events:            proxyEvents,
		MetricsSink:       a.metricsManager,
		OutboundTransport: outboundTransportCfg,
		TransportPool:     a.transportPool,
		ProxyBypassRules:  a.envCfg.ProxyBypassRules,
		DirectDenyPrivate: a.envCfg.DirectDenyPrivate,
		AuthGuard:         proxyAuthGuard,
	})
	socks5Inbound := proxy.NewSocks5Inbound(proxy.Socks5InboundConfig{
		ProxyToken:        a.envCfg.ProxyToken,
		Router:            a.topoRuntime.router,
		Pool:              a.topoRuntime.pool,
		Health:            a.topoRuntime.pool,
		Events:            proxyEvents,
		MetricsSink:       a.metricsManager,
		ProxyBypassRules:  a.envCfg.ProxyBypassRules,
		DirectDenyPrivate: a.envCfg.DirectDenyPrivate,
	})
	// WP04 §4.6: the SOCKS5 username/password check happens on a raw TCP
	// session, so the mux cannot meter it with an http.ResponseWriter; the
	// guard observes the RFC 1929 reply instead (see proxy_auth_guard.go). A nil
	// guard (the default, PRISM_PROXY_AUTH_FAIL_LIMIT=0) leaves the inbound
	// untouched.
	var socks5Handler inboundConnHandler = socks5Inbound
	if proxyAuthGuard != nil {
		socks5Handler = newSocks5AuthGuard(proxyAuthGuard, socks5Inbound)
	}

	endpointManager := newEndpointRuntimeManager(
		a.envCfg.ListenAddress,
		a.envCfg.ProxyToken,
		forwardProxy,
		reverseProxy,
		apiSrv.Handler(),
		tokenActionHandler,
		socks5Handler,
		proxyAuthGuard,
		a.metricsManager,
	)
	cpService.EndpointRuntime = endpointManager
	defaultEndpoint := service.NewDefaultEndpoint(a.envCfg.ProxyPort)
	if err := endpointManager.ApplyEndpoint(defaultEndpoint); err != nil {
		return fmt.Errorf("default endpoint listen: %w", err)
	}
	if err := restorePersistedEndpoints(engine, endpointManager); err != nil {
		endpointManager.RemoveEndpoint(service.DefaultEndpointID)
		return fmt.Errorf("load endpoints: %w", err)
	}
	a.endpointManager = endpointManager

	return nil
}

func restorePersistedEndpoints(engine *state.StateEngine, endpointManager *endpointRuntimeManager) error {
	customEndpoints, err := engine.ListEndpoints()
	if err != nil {
		return err
	}
	for _, endpoint := range customEndpoints {
		if !endpoint.Enabled {
			continue
		}
		if err := endpointManager.ApplyEndpoint(endpoint); err != nil {
			endpointManager.RecordEndpointError(endpoint, err)
			log.Printf("Endpoint %s on port %d unavailable: %v", endpoint.ID, endpoint.Port, err)
		}
	}
	return nil
}

func (a *prismApp) buildProxyEvents() proxy.ConfigAwareEventEmitter {
	// Composite emitter: requestlog handles EmitRequestLog, metricsManager handles EmitRequestFinished.
	composite := compositeEmitter{logSvc: a.requestlogSvc, metricsMgr: a.metricsManager}
	return proxy.ConfigAwareEventEmitter{
		Base: composite,
		RequestLogEnabled: func() bool {
			return runtimeConfigSnapshot(a.runtimeCfg).RequestLogEnabled
		},
		ReverseProxyLogDetailEnabled: func() bool {
			return runtimeConfigSnapshot(a.runtimeCfg).ReverseProxyLogDetailEnabled
		},
		ReverseProxyLogReqHeadersMaxBytes: func() int {
			return runtimeConfigSnapshot(a.runtimeCfg).ReverseProxyLogReqHeadersMaxBytes
		},
		ReverseProxyLogReqBodyMaxBytes: func() int {
			return runtimeConfigSnapshot(a.runtimeCfg).ReverseProxyLogReqBodyMaxBytes
		},
		ReverseProxyLogRespHeadersMaxBytes: func() int {
			return runtimeConfigSnapshot(a.runtimeCfg).ReverseProxyLogRespHeadersMaxBytes
		},
		ReverseProxyLogRespBodyMaxBytes: func() int {
			return runtimeConfigSnapshot(a.runtimeCfg).ReverseProxyLogRespBodyMaxBytes
		},
	}
}

// startServers brings up the optional admin listener and every endpoint
// (including the default one) and returns a channel that reports fatal serve
// errors from any of them.
func (a *prismApp) startServers() (<-chan error, error) {
	// 13./14. Bind the independent management listener before the endpoints so
	// a bad PRISM_ADMIN_LISTEN fails fast.
	admin, err := startAdminListener(a.envCfg.AdminListen, a.apiHandler)
	if err != nil {
		return nil, err
	}
	a.adminListener = admin

	log.Printf(
		"Prism default endpoint starting on %s (%s)",
		formatListenAddress(a.envCfg.ListenAddress, a.envCfg.ProxyPort),
		"HTTP + SOCKS5",
	)
	return mergeErrorChannels(a.endpointManager.Start(), a.adminListener.Errors()), nil
}

func waitForShutdown(serverErrCh <-chan error) error {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	select {
	case sig := <-quit:
		log.Printf("Received signal %s, shutting down...", sig)
		return nil
	case err := <-serverErrCh:
		log.Printf("Received server runtime error (%v), shutting down...", err)
		return err
	}
}

func formatListenAddress(listenAddress string, port int) string {
	return net.JoinHostPort(listenAddress, strconv.Itoa(port))
}

func formatListenURL(listenAddress string, port int) string {
	return "http://" + formatListenAddress(listenAddress, port)
}

// shutdownStepTimeout bounds a single shutdown step; the whole sequence has a
// 30s budget (WP03 §4).
const shutdownStepTimeout = 10 * time.Second

// shutdown stops everything in the WP03 §4 order:
//
//	listeners -> endpoints -> scheduled rotator -> probes -> intel executor
//	(WP08) -> subscription scheduler -> final dirty flush -> databases
//
// Every step is capped at 10s and the sequence as a whole at 30s.
func (a *prismApp) shutdown() {
	overallCtx, cancelOverall := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelOverall()

	// 0. The audit retention loop (WP04 §4.7) only touches state.db.
	if a.stopAuditPruner != nil {
		a.stopAuditPruner()
	}

	// 1. Listeners: the optional admin listener and every endpoint runtime.
	shutdownWithContext(overallCtx, "admin listener", a.adminListener.Shutdown)
	shutdownWithContext(overallCtx, "endpoints", a.endpointManager.Shutdown)
	log.Println("Prism server stopped")
	if a.transportPool != nil {
		a.transportPool.CloseAll()
		log.Println("Outbound transport pool closed")
	}

	// 2. Event sources: the scheduled rotator goes down with the router, before
	// the lease cleaner, probes and the subscription scheduler.
	shutdownBlocking("scheduled rotator", a.topoRuntime.rotator.Stop)
	log.Println("Scheduled rotator stopped")

	shutdownBlocking("lease cleaner", a.topoRuntime.leaseCleaner.Stop)
	log.Println("Lease cleaner stopped")

	shutdownBlocking("ephemeral cleaner", a.topoRuntime.ephemeralCleaner.Stop)
	log.Println("Ephemeral cleaner stopped")

	// 3. Probes (the intel job executor joins here in WP08).
	shutdownBlocking("probe manager", a.topoRuntime.probeMgr.Stop)
	log.Println("Probe manager stopped")

	// 3b. The intel job executor goes down with the probes (WP08 §4): the
	// observer is detached first so no new sample is queued while draining.
	// WP09 §3: the offline database refresher only writes cache files, so it
	// goes down before the GeoIP service without waiting for a download.
	if a.geoDBs != nil {
		a.geoDBs.Stop()
		log.Println("Intel offline database refresher stopped")
	}
	if a.intelSvc != nil {
		a.intelSvc.Stop()
		log.Println("Intel job executor stopped")
	}

	shutdownBlocking("geoip service", a.geoSvc.Stop)
	log.Println("GeoIP service stopped")

	// 4. Subscription scheduler.
	shutdownBlocking("subscription scheduler", a.topoRuntime.scheduler.Stop)
	log.Println("Subscription scheduler stopped")

	// 5. Observability sinks (flush remaining data).
	shutdownBlocking("request log service", a.requestlogSvc.Stop)
	log.Println("Request log service stopped")
	if err := a.requestlogRepo.Close(); err != nil {
		log.Printf("Request log repo close error: %v", err)
	}
	log.Println("Request log repo closed")

	shutdownBlocking("metrics manager", a.metricsManager.Stop)
	log.Println("Metrics manager stopped")
	if err := a.metricsDB.Close(); err != nil {
		log.Printf("Metrics DB close error: %v", err)
	}
	log.Println("Metrics DB closed")

	// 6. Infrastructure and the final dirty flush before the databases close in
	// run() (state.db/cache.db are owned by the caller).
	if a.topoRuntime.singboxBuilder != nil {
		if err := a.topoRuntime.singboxBuilder.Close(); err != nil {
			log.Printf("SingboxBuilder close error: %v", err)
		}
		log.Println("SingboxBuilder stopped")
	}

	shutdownBlocking("cache flush worker", a.flushWorker.Stop) // final cache flush before DB close
	log.Println("Server stopped")
}

// shutdownWithContext runs a context-aware stop function with a 10s cap.
func shutdownWithContext(parent context.Context, name string, stop func(context.Context) error) {
	if stop == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, shutdownStepTimeout)
	defer cancel()
	if err := stop(ctx); err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) {
		log.Printf("%s shutdown error: %v", name, err)
	}
}

// shutdownBlocking runs a stop function that has no context and gives up after
// 10s so a stuck component cannot delay the rest of the sequence.
func shutdownBlocking(name string, stop func()) {
	if stop == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		stop()
	}()
	select {
	case <-done:
	case <-time.After(shutdownStepTimeout):
		log.Printf("%s shutdown timed out after %s", name, shutdownStepTimeout)
	}
}
