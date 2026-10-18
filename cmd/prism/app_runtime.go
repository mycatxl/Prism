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
	"prism/internal/auth"
	"prism/internal/buildinfo"
	"prism/internal/config"
	"prism/internal/geoip"
	"prism/internal/intel"
	"prism/internal/intel/providers"
	"prism/internal/intel/store"
	"prism/internal/keys"
	"prism/internal/metrics"
	"prism/internal/model"
	"prism/internal/netutil"
	"prism/internal/node"
	"prism/internal/proxy"
	"prism/internal/requestlog"
	"prism/internal/routing"
	"prism/internal/safego"
	"prism/internal/service"
	"prism/internal/state"
	"prism/internal/tron"
	"prism/internal/usage"
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
	//: the metrics retention loop and the policy it applies.
	stopMetricsPruner func()
	metricsRetention  metrics.RetentionPolicy
	//: the usage summary and prune loop over state.db.
	stopUsageRetention func()
	//: the usage alert scanner over state.db.
	stopUsageAlerts func()
	//: the subscription expiry sweep over state.db.
	stopSubscriptionSweeper func()
	//: the automatic renewal sweep over state.db.
	stopRenewalSweeper func()
	//: the on-chain deposit listener and amount sweeper.
	stopDepositScanner func()
	//: the authoritative intel store, its in-memory projection and the job
	// executor.
	stateEngine *state.StateEngine
	intelStore  *store.Store
	intelSvc    *intel.Service
	// the background refresher of the offline databases.
	geoDBs *providers.GeoManager
	// keyPepper is the key pepper every access-key secret is verified against.
	// It is nil when no pepper is configured, which disables access-key
	// authentication and leaves the transitional global proxy token as the
	// only credential - the behaviour of a deployment that has not been
	// migrated yet.
	keyPepper *auth.Pepper
	// usageCounters is the live per-key traffic view the data plane writes to.
	// It is the only accounting structure the proxy touches; persistence
	// happens in the flusher.
	usageCounters *usage.Counters
	// usageSessions holds each key's live tunnels, so a revoked key can be hung
	// up on rather than only refused its next request.
	usageSessions *usage.ConnRegistry
	// stopUsageAccounting cancels the flusher and the enforcer.
	stopUsageAccounting func()
	// stopSessionPruner stops the sweep that deletes expired console
	// sessions from state.db.
	stopSessionPruner func()
}

// run assembles and runs the Prism service.
//
// Assembly order:
//
//  1. Load.env, then call config.LoadEnvConfig (RESIN_* fallbacks are).
//
//  2. state.PersistenceBootstrap(stateDir, cacheDir) returns the engine.
//
//  3. Open the intel store.
//
//  3. Open intel.db.
//
//  4. loadRuntimeConfig(engine, envCfg).
//
//  5. Create the GeoIP service (upstream implementation; adds offline DBs).
//
//  6. Create the node builder: outbound.NewSingboxBuilderWithConfig for now,
//     replaces it with outbound.NewSingboxRuntime.
//
//  7. Create the topology objects: topology.NewGlobalNodePool (GeoLookup wired,
//     QualityLookup stays nil until), SubscriptionManager,
//     SubscriptionScheduler and EphemeralCleaner.
//
//  8. Create probe.NewProbeManager and register SetOnProbeEvent (metrics);
//     attaches SetOnEgressObserved.
//
//  9. Wire pool.SetOnNodeAdded/SetOnNodeRemoved like upstream.
//
//  10. Create routing.NewRouter, routing.NewLeaseCleaner and the Prism-only
//     routing.NewScheduledRotator (started and stopped with the router).
//
//  11. Create metrics, request log and state.NewCacheFlushWorker.
//
//  12. Create the intel job executor.
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

	// 2. Take the data directory lock before opening anything in it. Two
	// instances sharing a state directory do not share their in-memory routing
	// state, leases or quota counters, so they would silently hand out
	// different exit IPs and account for traffic separately. Refusing the
	// second instance is the only honest answer.
	dirLock, err := state.AcquireDirLock(envCfg.StateDir)
	if err != nil {
		return fmt.Errorf("data directory lock: %w", err)
	}
	// Released on the way out of run(), after the shutdown sequence has stopped
	// everything that touches the directory.
	defer func() {
		if err := dirLock.Release(); err != nil {
			log.Printf("Data directory unlock error: %v", err)
		}
	}()
	log.Printf("Data directory locked: %s", dirLock.Path())

	// 3. Open state.db and cache.db and return the shared engine.
	engine, dbCloser, err := state.PersistenceBootstrap(envCfg.StateDir, envCfg.CacheDir)
	if err != nil {
		return fmt.Errorf("persistence bootstrap: %w", err)
	}
	log.Println("Persistence bootstrap complete")

	// 3. Open the intel store: intel.db is a separate SQLite file with its own
	// open path, pragmas and 0600 file mode.
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

// newPrismApp builds every long-lived runtime object in the order. The
// numbered steps below match the assembly list documented on run().
func newPrismApp(envCfg *config.EnvConfig, engine *state.StateEngine, intelStore *store.Store) (*prismApp, error) {
	app := &prismApp{
		envCfg:      envCfg,
		runtimeCfg:  &atomic.Pointer[config.RuntimeConfig]{},
		stateEngine: engine,
		intelStore:  intelStore,
	}
	// 4. Runtime configuration (persisted values, defaults otherwise).
	app.runtimeCfg.Store(loadRuntimeConfig(engine, envCfg))
	if err := ensureDefaultAccountHeaderRule(engine); err != nil {
		return nil, err
	}
	app.accountMatcher = buildAccountMatcher(engine)

	// The key pepper is loaded before anything that verifies a credential is
	// built. Doing it later would hand the data plane a nil pepper, which fails
	// every key check - and the symptom would be "valid keys are refused", not
	// "the pepper was missing". A missing pepper is not fatal: access-key
	// authentication stays off and the transitional global proxy token keeps
	// working, which is what a deployment that has not been migrated yet needs.
	app.loadKeyPepper(envCfg.StateDir)

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
	//
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
	// 5. GeoIP service (upstream implementation; adds the offline DBs).
	direct := newDirectDownloader(a.envCfg)
	retryDL := &netutil.RetryDownloader{Direct: direct}
	a.geoSvc = newGeoIPService(a.envCfg.CacheDir, a.envCfg.GeoIPUpdateSchedule, retryDL)

	// 6.-9. Node builder (outbound.NewSingboxBuilderWithConfig; swaps in
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
				engine.MarkLease(e.PlatformID, e.UserID, e.Account)
			case routing.LeaseRemove, routing.LeaseExpire:
				engine.MarkLeaseDelete(e.PlatformID, e.UserID, e.Account)
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
// network. reuses it for the offline databases.
func (a *prismApp) downloadNodePicker() func(target string) (node.Hash, error) {
	return func(target string) (node.Hash, error) {
		if a == nil || a.topoRuntime == nil || a.topoRuntime.router == nil {
			return node.Zero, errors.New("router is not available")
		}
		res, err := a.topoRuntime.router.RouteRequest("", "", "", target)
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

	// GeoIP moved to step 8 batch 1 (after lease restore,).

	// Phase 8.1: Rebuild platform views BEFORE lease restore.
	// the design system requires step 6 (rebuild) before step 7 (leases).
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
	a.metricsRetention = metrics.RetentionPolicy{
		ThroughputSeconds:  metricsCfg.ThroughputRetentionSec,
		ConnectionsSeconds: metricsCfg.ConnectionsRetentionSec,
		LeasesSeconds:      metricsCfg.LeasesRetentionSec,
	}

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
		Retention:                   a.metricsRetention,
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
	log.Println("Probe manager started (batch 2)")

	// step 12: the egress observer must be attached after the probe manager
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

// loadKeyPepper loads the key pepper, or reports why access-key authentication
// stays off.
//
// A missing pepper is not fatal: the transitional global proxy token keeps
// working exactly as before, which is what a deployment that has not been
// migrated yet needs. It is reported at startup rather than at first use so the
// degradation is visible in the log instead of showing up as every key failing.
func (a *prismApp) loadKeyPepper(stateDir string) {
	pepper, err := auth.LoadPepper(stateDir)
	if err != nil {
		if errors.Is(err, auth.ErrPepperMissing) {
			log.Println("Access key authentication is unavailable: no key pepper configured; " +
				"the global proxy token remains the only credential")
			return
		}
		// A pepper that exists but cannot be read is a different problem, and
		// one an operator has to fix: every key would fail for a reason the
		// data plane cannot report.
		log.Printf("Access key authentication is unavailable: %v", err)
		return
	}
	a.keyPepper = pepper
	log.Println("Key pepper loaded")
}

// loadAccessKeys builds the in-memory key view the data plane reads.
//
// A failure here is not fatal for the same reason a missing pepper is not: the
// global proxy token still authenticates. It is logged loudly because it means
// the keys an operator created are not live.
func (a *prismApp) loadAccessKeys(engine *state.StateEngine) *keys.Store {
	store, err := keys.LoadFromState(engine, log.Printf)
	if err != nil {
		log.Printf("Access keys are unavailable: %v", err)
		return keys.NewStore()
	}
	keys.LogSummary(store, log.Printf)
	return store
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

	// The dashboard map draws panel -> node-region traffic, so the system info
	// endpoint reports where the panel server itself egresses from. The trace is
	// a best-effort direct HTTPS GET issued in the background: the handler reads
	// the cached snapshot and never waits for it.
	panelEgress := service.NewPanelEgressProvider()

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

	// audit retention runs once a day. The window is read from the runtime
	// config at each pass, so a change made in the console applies without a
	// restart; the row cap (100000) stays fixed.
	a.stopAuditPruner = api.StartAuditPrunerWithRetention(engine, 24*time.Hour, func() int {
		return runtimeConfigSnapshot(a.runtimeCfg).AuditRetentionDays
	})
	//: the same published retention windows bound metrics.db. The pruner
	// deletes in bounded batches, so it never holds the write lock for long.
	if a.metricsDB != nil {
		a.stopMetricsPruner = metrics.StartRetentionPruner(
			a.metricsDB, a.metricsRetention, metrics.RetentionPruneInterval)
	}

	// The access-key view is built once here and refreshed by the management
	// API when a key changes. It is the data plane's only source of key state,
	// which is what keeps the hot path free of database reads.
	accessKeys := a.loadAccessKeys(engine)
	keyVerifier := proxy.NewAccessKeyVerifier(accessKeys, a.keyPepper)

	// The usage summary runs hourly: state.db has one write connection shared
	// with the flush worker, and a summary pass is heavier than a bounded
	// delete, so it stays well clear of the metric pruner's five-minute cadence.
	a.stopUsageRetention = (&usage.Retention{
		Store: engine,
		Logf:  usage.DefaultLogf,
		// The daily window follows usage_retention_days, read at each pass so
		// the console can shorten or lengthen it without a restart.
		DailyKeepFn: func() time.Duration {
			days := config.RetentionDays(
				runtimeConfigSnapshot(a.runtimeCfg).UsageRetentionDays,
				config.DefaultUsageRetentionDays,
			)
			return time.Duration(days) * 24 * time.Hour
		},
	}).Start()
	log.Println("Usage retention started")

	// Usage alerts scan on a short cadence of their own: the detector compares a
	// one-hour window, so a crossing is reported while it is still worth acting
	// on. The ratio, window and retention come from the runtime config at each
	// pass, which is what lets the console change them without a restart.
	a.stopUsageAlerts = service.StartUsageAlertScanner(cpService, service.UsageAlertScanInterval, log.Printf)
	log.Println("Usage alerts started")

	// A subscription is closed out of the stored status once its window has
	// passed, which is the write the status-only reads need: the console's
	// active counts and the period worker read "active" and nothing else, so a
	// subscription that expired keeps being counted and keeps being rolled until
	// something says otherwise. Access itself does not wait for the sweep - the
	// key, snapshot and seed paths all compare expires_at_ns with the clock -
	// which is why the sweep can be this slow. It writes state.db, so shutdown
	// stops it while that database is still open.
	a.stopSubscriptionSweeper = service.StartSubscriptionExpirySweeper(cpService, service.SubscriptionSweepInterval, log.Printf)
	log.Println("Subscription expiry sweeper started")

	// Automatic renewal. It runs before the expiry sweep can close a window,
	// because both look at the same rows: a subscription that opted in is
	// charged and extended here, and one that did not is left for the sweep to
	// close out. Order matters only in that the renewal sweep reads the lead
	// time (7 days), which is far wider than either interval.
	//
	// It writes state.db and republishes keys, so shutdown stops it before the
	// database is closed, in the same place as the sweep above.
	a.stopRenewalSweeper = service.StartRenewalSweeper(cpService, 0, log.Printf)
	log.Println("Subscription renewal sweeper started")

	// On-chain deposits. The client only reads: the receiving address is the
	// operator's wallet and the server holds no key, so an empty address simply
	// means the feature is off and the scanner is never started.
	//
	// It writes state.db, so it is stopped with the other loops before the
	// database closes.
	if address := a.envCfg.USDTAddress; address != "" {
		cpService.Tron = tron.NewClient(a.envCfg.TronGridEndpoint, a.envCfg.TronGridAPIKey, a.envCfg.USDTContract)
		a.stopDepositScanner = service.StartDepositScanner(cpService, 0, log.Printf)
		log.Println("On-chain deposit scanner started")
	}

	// Accounting. One counter set and one session registry are shared by every
	// inbound: a key's allowance spans the protocols it uses, and so does the
	// set of tunnels that may need ending.
	//
	// Nothing here touches the database on the data path. The counters are
	// in-memory atomics, and the flusher below is the only thing that reaches
	// disk.
	a.usageCounters = usage.NewCounters()
	a.usageSessions = usage.NewConnRegistry()
	usageLimiter := usage.NewLimiter(a.usageCounters)

	// The management service reads and writes the same live objects the data
	// plane uses: the key store the proxy authorises against, the counters the
	// allowance is drawn from, and the registry of open tunnels. Handing them
	// over here is what makes an operator's change take effect on a user's next
	// request instead of at the next restart.
	cpService.Keys = accessKeys
	cpService.Usage = a.usageCounters
	cpService.Conns = a.usageSessions
	cpService.Limiter = usageLimiter
	usageQuota := &usage.Gate{Limiter: usageLimiter}
	usageMetering := &usage.Sink{Counters: a.usageCounters}

	// A refused request never reaches the request lifecycle, so it would
	// otherwise leave no trace at all: neither the request log nor the server
	// log would say which limit stopped which key. The data plane reports the
	// refusal here, with the key id and the reason only - no credential and
	// nothing that identifies the account behind the key.
	quotaRefusalLogf := func(format string, args ...any) {
		log.Printf(format, args...)
	}

	accountingCtx, cancelAccounting := context.WithCancel(context.Background())
	a.stopUsageAccounting = cancelAccounting

	// The allowance is a running total and the counters start empty, so the
	// persisted usage is loaded back before anything can be served. Without
	// this a restart would hand every user their whole allowance again, while
	// the disk rows the flusher keeps adding to would keep the old usage:
	// memory and disk would disagree for as long as the process lived.
	//
	// The read is retried, because a state.db that is momentarily busy must
	// not cost every user a second allowance. Serving from zero is the only
	// remaining option - refusing service would be worse - so the process does
	// that, says so loudly, and keeps trying in the background until the
	// persisted totals are back in memory.
	seedRetry := usageSeedRetry{attempts: 4, backoff: 250 * time.Millisecond}
	seeded, seedErr := seedUsage(a.usageCounters, engine, seedRetry, usage.DefaultLogf)
	if seedErr != nil {
		usage.DefaultLogf(
			"Usage accounting: cannot read subscriptions to restore usage after %d attempts: %v; "+
				"period usage is counted from zero for now, which hands every user a second allowance "+
				"until a re-seed succeeds - the process keeps trying every %s",
			seedRetry.attempts, seedErr, usage.DefaultFlushInterval)
		safego.Go("the usage re-seed", func() {
			reseedUsageUntilApplied(accountingCtx, a.usageCounters, engine, usage.DefaultFlushInterval, usage.DefaultLogf)
		})
	} else if seeded > 0 {
		usage.DefaultLogf("Usage accounting: restored period usage for %d subscription(s)", seeded)
	}

	go (&usage.Flusher{
		Counters: a.usageCounters,
		Store:    engine,
		Logf:     usage.DefaultLogf,
	}).Start(accountingCtx)

	// The rollover is what makes an allowance a period rather than a one-time
	// grant: without it a user who spends their month on the first day stays
	// refused forever, because nothing ever moves the period on.
	go (&usage.PeriodReset{
		Store:    engine,
		Counters: a.usageCounters,
		Logf:     usage.DefaultLogf,
	}).Start(accountingCtx)

	// The enforcer is what makes a disable mean "now" rather than "at the next
	// request". It resolves keys through the same store the data plane
	// authenticates against, so both see one answer.
	if accessKeys != nil {
		go (&usage.Enforcer{
			Registry: a.usageSessions,
			Limiter:  usageLimiter,
			Lookup: func(keyID string) *keys.KeyState {
				state, ok := accessKeys.Get(keyID)
				if !ok {
					// A key that cannot be resolved is one that was deleted, or
					// whose owner was. Answering nil is what tells the enforcer
					// to close its sessions.
					return nil
				}
				return state
			},
			GracePercent: usage.DefaultGracePercent,
			Logf:         usage.DefaultLogf,
		}).Start(accountingCtx)
	}
	log.Println("Usage accounting started")

	// Expired console sessions are swept on a slow loop of their own. Nothing
	// else ever deletes them: a login writes a row, and once the row's own
	// expiry has passed it would stay in state.db forever, which is what turns
	// a deleted user's credentials into permanent rows. The sweep belongs with
	// the accounting loops: same database, same process-owned timer.
	a.stopSessionPruner = (&usage.SessionPruner{
		Store: engine,
		Logf:  usage.DefaultLogf,
	}).Start()
	log.Println("Session pruner started")

	// Console authentication. One session service is shared by the identity
	// middleware and the auth handlers, so a login writes the row that the very
	// next request reads.
	//
	// The state engine is the session store directly: its repository already has
	// exactly the methods a session needs, and wrapping it would only add a layer
	// to keep in step.
	cpService.Pepper = a.keyPepper
	// One limiter for both paths: the handler counts the failures and the
	// middleware enforces the block, and two limiters would let an address fail
	// one without the other noticing.
	loginBlocklist := api.NewLoginBlocklist(a.envCfg.TrustedProxies)
	sessionService := &auth.Service{
		Store:     engine,
		Pepper:    a.keyPepper,
		Logf:      log.Printf,
		Blocklist: loginBlocklist,
	}
	authDeps := &api.AuthDeps{
		Issuer:    "Prism",
		Blocklist: loginBlocklist,
		Identity: &api.Identity{
			Sessions:  sessionService,
			AdminKeys: adminKeyResolver{verifier: keyVerifier},
			// Administrators must carry a second factor when the policy says
			// so. An administrator without one is admitted only far enough to
			// enrol, which is what keeps the requirement from locking everyone
			// out of a fresh install.
			RequireAdminTOTP: func() bool { return a.envCfg.RequireAdminTOTP },
			// The console reads the same policy as the register endpoint, so
			// it offers the form exactly when that endpoint would accept it.
			// The panel may override the deployment's policy at runtime; the
			// environment value is the fallback once that override is cleared.
			RegistrationMode: a.effectiveRegistrationMode,
			Logf:             log.Printf,
		},
		Handlers: &api.AuthHandlers{
			Sessions: sessionService,
			Accounts: cpService,
			// Self-service sign-up: the runtime config owns the override and
			// the environment owns the fallback, so opening or closing the
			// door is an operational decision the console can make live.
			Registration:     cpService,
			RegistrationMode: a.effectiveRegistrationMode,
			Logf:             log.Printf,
		},
	}

	apiSrv := api.NewServerWithAddress(
		a.envCfg.ListenAddress,
		a.envCfg.ProxyPort,
		systemInfo,
		a.runtimeCfg,
		a.envCfg,
		cpService,
		int64(a.envCfg.APIMaxBodyBytes),
		a.requestlogRepo,
		a.metricsManager,
		panelEgress,
		authDeps,
	)
	// The optional admin listener (PRISM_ADMIN_LISTEN) reuses this handler but
	// only exposes the management paths, never a proxy protocol.
	a.apiHandler = apiSrv.Handler()
	tokenActionHandler := api.NewTokenActionHandler(
		a.envCfg.ProxyToken,
		keyVerifier,
		cpService,
		int64(a.envCfg.APIMaxBodyBytes),
	)

	proxyEvents := a.buildProxyEvents()
	outboundTransportCfg := proxy.OutboundTransportConfig{
		MaxIdleConns:        a.envCfg.ProxyTransportMaxIdleConns,
		MaxIdleConnsPerHost: a.envCfg.ProxyTransportMaxIdleConnsPerHost,
		IdleConnTimeout:     a.envCfg.ProxyTransportIdleConnTimeout,
		// The three outbound deadlines: without them a peer that accepts the
		// connection and never answers pins the request indefinitely, because
		// the listener has no read timeout (the SSE endpoint needs it).
		ResponseHeaderTimeout: a.envCfg.ProxyTransportResponseHeaderTimeout,
		TLSHandshakeTimeout:   a.envCfg.ProxyTransportTLSHandshakeTimeout,
		DialTimeout:           a.envCfg.ProxyTransportDialTimeout,
	}
	if a.transportPool == nil {
		a.transportPool = proxy.NewOutboundTransportPool(outboundTransportCfg)
	}

	// proxy-entry (407/403) failure limiting is opt-in and disabled
	// by default (PRISM_PROXY_AUTH_FAIL_LIMIT=0 keeps Resin behaviour).
	// The proxy entry points resolve their client key with the same trusted
	// proxy list the management API uses, so a client behind a listed proxy
	// gets one bucket per client instead of one per proxy.
	proxy.SetTrustedProxies(a.envCfg.TrustedProxies)
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
		ProxyToken:              a.envCfg.ProxyToken,
		AccessKeys:              keyVerifier,
		Router:                  a.topoRuntime.router,
		Pool:                    a.topoRuntime.pool,
		Health:                  a.topoRuntime.pool,
		Events:                  proxyEvents,
		MetricsSink:             a.metricsManager,
		OutboundTransport:       outboundTransportCfg,
		TransportPool:           a.transportPool,
		ProxyBypassRules:        a.envCfg.ProxyBypassRules,
		DirectDenyPrivate:       a.envCfg.DirectDenyPrivate,
		BlockedDestinationPorts: a.envCfg.BlockedDestinationPorts,
		AuthGuard:               proxyAuthGuard,
		Metering:                usageMetering,
		Quota:                   usageQuota,
		Sessions:                a.usageSessions,
		QuotaLogf:               quotaRefusalLogf,
	})

	reverseProxy := proxy.NewReverseProxy(proxy.ReverseProxyConfig{
		ProxyToken:              a.envCfg.ProxyToken,
		AccessKeys:              keyVerifier,
		Router:                  a.topoRuntime.router,
		Pool:                    a.topoRuntime.pool,
		PlatformLookup:          a.topoRuntime.pool,
		Health:                  a.topoRuntime.pool,
		Matcher:                 a.accountMatcher,
		Events:                  proxyEvents,
		MetricsSink:             a.metricsManager,
		OutboundTransport:       outboundTransportCfg,
		TransportPool:           a.transportPool,
		ProxyBypassRules:        a.envCfg.ProxyBypassRules,
		DirectDenyPrivate:       a.envCfg.DirectDenyPrivate,
		BlockedDestinationPorts: a.envCfg.BlockedDestinationPorts,
		AuthGuard:               proxyAuthGuard,
		Metering:                usageMetering,
		Quota:                   usageQuota,
		Sessions:                a.usageSessions,
		QuotaLogf:               quotaRefusalLogf,
	})
	socks5Inbound := proxy.NewSocks5Inbound(proxy.Socks5InboundConfig{
		ProxyToken:              a.envCfg.ProxyToken,
		AccessKeys:              keyVerifier,
		Router:                  a.topoRuntime.router,
		Pool:                    a.topoRuntime.pool,
		Health:                  a.topoRuntime.pool,
		Events:                  proxyEvents,
		MetricsSink:             a.metricsManager,
		ProxyBypassRules:        a.envCfg.ProxyBypassRules,
		DirectDenyPrivate:       a.envCfg.DirectDenyPrivate,
		BlockedDestinationPorts: a.envCfg.BlockedDestinationPorts,
		DialTimeout:             a.envCfg.ProxyTransportDialTimeout,
		Metering:                usageMetering,
		Quota:                   usageQuota,
		Sessions:                a.usageSessions,
		QuotaLogf:               quotaRefusalLogf,
	})
	// the SOCKS5 username/password check happens on a raw TCP
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

// effectiveRegistrationMode answers who may sign up right now. The console's
// stored override wins when it holds a mode the service understands; otherwise
// the deployment's PRISM_REGISTRATION applies. Resolving through the runtime
// snapshot is what makes a panel change take effect on the next request instead
// of the next restart, and an unreadable deployment value fails closed to off.
func (a *prismApp) effectiveRegistrationMode() config.RegistrationMode {
	if a == nil {
		return config.RegistrationOff
	}
	// A nil env config is a construction bug rather than a policy, and the
	// answer has to be the same one a broken policy gets: closed.
	fallback := config.RegistrationOff
	if a.envCfg != nil {
		fallback = a.envCfg.Registration
	}
	return runtimeConfigSnapshot(a.runtimeCfg).EffectiveRegistrationMode(fallback)
}

// usageSeedRetry bounds the startup seed: how many times the persisted
// subscriptions are re-read, and how long the process waits between attempts.
type usageSeedRetry struct {
	attempts int
	backoff  time.Duration
}

// usageSeedSource is the read the startup seed needs: the stored subscriptions,
// whose period totals are the allowance a restart must not hand out again. A
// *state.StateEngine satisfies it; the interface exists so the startup path can
// be tested without a database.
type usageSeedSource interface {
	ListUserSubscriptions() ([]model.UserSubscription, error)
}

// seedUsage restores the persisted period usage into the running counters.
//
// It answers how many subscriptions were seeded and, if every attempt failed,
// the error of the read it could not complete. The caller decides what a
// process that has to serve without the persisted totals says about itself; the
// retry exists because a state.db that is momentarily busy must not cost every
// user a second allowance.
func seedUsage(
	counters *usage.Counters,
	source usageSeedSource,
	retry usageSeedRetry,
	logf func(format string, args ...any),
) (int, error) {
	if counters == nil || source == nil {
		return 0, nil
	}
	attempts := retry.attempts
	if attempts < 1 {
		attempts = 1
	}
	backoff := retry.backoff
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		subs, err := source.ListUserSubscriptions()
		if err == nil {
			return counters.SeedFromSubscriptions(subs, time.Now().UnixNano()), nil
		}
		lastErr = err
		if attempt == attempts {
			break
		}
		if logf != nil {
			logf("Usage accounting: cannot read subscriptions to restore usage (attempt %d/%d): %v; retrying in %s",
				attempt, attempts, err, backoff)
		}
		if backoff > 0 {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	return 0, lastErr
}

// pendingUsageSeed reports the subscriptions whose persisted period totals are
// not covered by the running counters yet.
//
// "Covered" is the water mark the flusher maintains, not the presence of an
// aggregate. A user who moved traffic between a failed startup seed and the
// re-seed already has an aggregate built from that traffic alone, while the
// persisted total it should be measured against was never loaded; skipping such
// a user - "it already has an aggregate" - leaves the quota reading less than
// the disk records, which is an allowance handed out twice, and it is the exact
// gap this loop exists to close.
//
// Comparing the persisted total against the mark is also what keeps the re-seed
// from double counting. The bytes a flush charges to the subscription stay in
// the aggregate (it is never drained) and are added to the mark in the same
// pass, so a total that has been seeded leaves the two equal - and one that the
// flusher has already charged leaves the mark ahead of the pending test, not
// behind it. Traffic that arrives after the seed sits above the mark on both
// sides. The loop therefore converges on "every user's persisted total is
// covered", whatever the aggregate holds: seeding moves the mark to the total,
// which is what makes a second pass report nothing.
//
// The filter is the one SeedFromSubscriptions applies, so a subscription it
// would skip can never keep the re-seed loop running.
func pendingUsageSeed(subs []model.UserSubscription, counters *usage.Counters, nowNs int64) []model.UserSubscription {
	if counters == nil || len(subs) == 0 {
		return nil
	}
	pending := make([]model.UserSubscription, 0, len(subs))
	for _, sub := range subs {
		if sub.UserID == "" || sub.PeriodUsedBytes <= 0 {
			continue
		}
		// Only the usage of a subscription that is still the user's current
		// one belongs in the aggregate.
		if sub.Status != model.SubscriptionStatusActive {
			continue
		}
		if sub.ExpiresAtNs > 0 && nowNs > 0 && sub.ExpiresAtNs < nowNs {
			continue
		}
		// A missing mark stands for zero: nothing of this user's total has
		// been charged by this process, so any persisted total is pending.
		persisted, _ := counters.PersistedBytes(sub.UserID)
		if sub.PeriodUsedBytes <= persisted {
			continue
		}
		pending = append(pending, sub)
	}
	return pending
}

// reseedUsageUntilApplied closes the gap a failed startup seed leaves open.
//
// It runs on the accounting context, so a shutdown stops it, and on the flush
// cadence: a subscription the flusher is writing for is one the seed should
// have covered. The loop ends as soon as nothing is missing, so it cannot fight
// the flusher or the period reset for control of an aggregate that is already
// live.
func reseedUsageUntilApplied(
	ctx context.Context,
	counters *usage.Counters,
	source usageSeedSource,
	interval time.Duration,
	logf func(format string, args ...any),
) {
	if counters == nil || source == nil {
		return
	}
	if interval <= 0 {
		interval = usage.DefaultFlushInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		subs, err := source.ListUserSubscriptions()
		if err != nil {
			if logf != nil {
				logf("Usage accounting: re-seed skipped, cannot read subscriptions: %v", err)
			}
			continue
		}
		pending := pendingUsageSeed(subs, counters, time.Now().UnixNano())
		if len(pending) == 0 {
			if logf != nil {
				logf("Usage accounting: re-seed complete, the persisted period usage is live again")
			}
			return
		}
		seeded := counters.SeedFromSubscriptions(pending, time.Now().UnixNano())
		if logf != nil {
			logf("Usage accounting: re-seeded period usage for %d of %d subscription(s)", seeded, len(pending))
		}
	}
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

// shutdownStepTimeout bounds a single shutdown step; the whole sequence has a
// 30s budget.
const shutdownStepTimeout = 10 * time.Second

// shutdown stops everything in the order:
//
//	listeners -> endpoints -> scheduled rotator -> probes -> intel executor
//	 -> subscription scheduler -> final dirty flush -> databases
//
// Every step is capped at 10s and the sequence as a whole at 30s.
func (a *prismApp) shutdown() {
	overallCtx, cancelOverall := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelOverall()

	// 0. The audit retention loop only touches state.db.
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
	// The listeners are gone, so no new session can be registered; the ones
	// still open are hung up here rather than left to the process exit. A
	// tunnel that outlives its listeners keeps its upstream connection - and
	// the accounting that connection reports - alive until the OS reaps it.
	if a.usageSessions != nil {
		if closed := a.usageSessions.CloseAll(); closed > 0 {
			log.Printf("Closed %d live proxy session(s)", closed)
		}
	}

	// 2. Event sources: the scheduled rotator goes down with the router, before
	// the lease cleaner, probes and the subscription scheduler.
	shutdownBlocking("scheduled rotator", a.topoRuntime.rotator.Stop)
	log.Println("Scheduled rotator stopped")

	shutdownBlocking("lease cleaner", a.topoRuntime.leaseCleaner.Stop)
	log.Println("Lease cleaner stopped")

	shutdownBlocking("ephemeral cleaner", a.topoRuntime.ephemeralCleaner.Stop)
	log.Println("Ephemeral cleaner stopped")

	// 3. Probes (the intel job executor joins here in).
	shutdownBlocking("probe manager", a.topoRuntime.probeMgr.Stop)
	log.Println("Probe manager stopped")

	// 3b. The intel job executor goes down with the probes: the
	// observer is detached first so no new sample is queued while draining.
	// the offline database refresher only writes cache files, so it
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

	if a.stopMetricsPruner != nil {
		a.stopMetricsPruner()
	}
	if a.stopUsageRetention != nil {
		a.stopUsageRetention()
	}
	if a.stopSessionPruner != nil {
		a.stopSessionPruner()
	}
	if a.stopUsageAlerts != nil {
		a.stopUsageAlerts()
	}
	// The sweep only writes state.db, so it stops here, while that database is
	// still open - the same reason the accounting is cancelled below.
	if a.stopSubscriptionSweeper != nil {
		a.stopSubscriptionSweeper()
	}
	// Same reason: the renewal sweep charges balances and republishes keys,
	// both of which reach state.db.
	if a.stopRenewalSweeper != nil {
		a.stopRenewalSweeper()
	}
	// And the deposit scanner credits balances and advances its cursor, so it
	// stops here too, before state.db closes.
	if a.stopDepositScanner != nil {
		a.stopDepositScanner()
	}
	// Cancelling the accounting runs the flusher's final pass, which writes to
	// state.db - so it has to happen here, while that database is still open,
	// and not with the later teardown.
	if a.stopUsageAccounting != nil {
		a.stopUsageAccounting()
	}
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
	// A component that panics while shutting down must not take the process
	// down before the remaining components are stopped: the timeout below is
	// what keeps the sequence moving, and a panic here would skip it.
	safego.Go("the "+name+" shutdown", func() {
		defer close(done)
		stop()
	})
	select {
	case <-done:
	case <-time.After(shutdownStepTimeout):
		log.Printf("%s shutdown timed out after %s", name, shutdownStepTimeout)
	}
}
