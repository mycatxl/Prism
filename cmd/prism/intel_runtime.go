package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"

	"prism/internal/intel"
	"prism/internal/intel/assess"
	"prism/internal/intel/checks"
	"prism/internal/intel/egress"
	"prism/internal/intel/jobs"
	"prism/internal/intel/providers"
	"prism/internal/intel/store"
	"prism/internal/model"
	"prism/internal/netutil"
	"prism/internal/node"
	"prism/internal/state"
)

// maxIntelScopeNodes bounds the in-memory expansion of one job scope before the
// job manager rejects it (WP08 §3.1). The expansion never truncates silently:
// a scope that exceeds the bound fails with jobs.ErrTooManyNodes.
const maxIntelScopeNodes = jobs.MaxNodesPerJob

// intelGeoDirName is the directory below $PRISM_CACHE_DIR that holds the
// offline databases (WP09 §3).
const intelGeoDirName = "geo"

// intelHTTPTimeout bounds one online data source request (host side only).
const intelHTTPTimeout = 15 * time.Second

// initIntelJobs wires the intel store, the in-memory projection and the batch
// executor into the running application (assembly step 12).
func (a *prismApp) initIntelJobs(engine *state.StateEngine, intelStore *store.Store) error {
	if intelStore == nil {
		return nil
	}
	a.stateEngine = engine

	prober := &egress.Probe{
		Fetcher: a.topoRuntime.outboundMgr,
		Store:   intelStore,
		ProbeEgressSync: func(_ context.Context, hash node.Hash) error {
			if a.topoRuntime.probeMgr == nil {
				return nil
			}
			_, err := a.topoRuntime.probeMgr.ProbeEgressSync(hash)
			return err
		},
	}

	// WP09 §1/§3: the provider registry carries the effective settings (spec
	// defaults merged with state.db and, on the first start, with the legacy
	// environment variables).
	registry := a.intelProviderRegistry(engine)
	checkEngine := a.intelChecksEngine(engine)
	// WP09 §4: the settings surface wraps the same registry the pipeline steps
	// and the queue workers resolve, so a PATCH applies to the running data
	// sources without a restart.
	var settingsStore providers.SettingsStore
	if engine != nil {
		settingsStore = engine
	}
	providerSettings := providers.NewSettingsService(registry, settingsStore, os.Getenv, time.Now)

	// WP09 §3: the offline databases are downloaded by a bounded background
	// refresher. Nothing here waits for the network: a database that is missing
	// or stale keeps its provider at PROVIDER_UNAVAILABLE until the download
	// lands, and the settings page can still trigger a manual refresh.
	if geoDBs := a.intelGeoDatabases(registry); geoDBs != nil {
		providerSettings.SetDatabases(geoDBs)
		geoDBs.Start()
	}

	stepOptions := intel.StepOptions{
		Store:    intelStore,
		Registry: registry,
		Checks:   checkEngine,
		Outbound: a.intelNodeOutbound,
		Config:   a.intelJobConfig,
		Clock:    jobs.SystemClock{},
		Logf:     log.Printf,
	}
	handlers := intel.NewStepHandlers(stepOptions)
	if handlers.Checks != nil {
		handlers.Checks = a.withCheckConcurrency(checkEngine, handlers.Checks)
	}

	svc, err := intel.NewService(intel.Options{
		Store: intelStore,
		Scope: jobs.ScopeResolverFunc(a.resolveIntelScope),
		// Step 1 stays the built-in egress probe. Every other step is now a real
		// WP09/WP10 handler, so no step degrades to a no-op skip any more.
		Prober:        prober,
		Handlers:      handlers,
		Config:        a.intelJobConfig,
		ProviderSpecs: intel.QueueSpecs(registry),
		Lookup:        intel.NewOnlineLookup(stepOptions),
		// §4/§5: the settings surfaces of the data sources and the checks.
		ProviderSettings: providerSettings,
		CheckEngine:      checkEngine,
		EnabledSources: func() map[string]bool {
			return intelEnabledSources(registry)
		},
		OnAssessmentChanged: func(ips []netip.Addr) {
			if a == nil || a.topoRuntime == nil || a.topoRuntime.pool == nil {
				return
			}
			for _, ip := range ips {
				a.topoRuntime.pool.NotifyEgressIPDirty(ip)
			}
		},
		AssessmentSweepInterval: assessmentSweepInterval,
		Census:                  a,
		Logf:                    log.Printf,
	})
	if err != nil {
		return fmt.Errorf("intel service: %w", err)
	}
	// §3.6: a subscription with auto_intel=1 auto-enqueues the nodes a refresh
	// added; auto_intel=0 and intel_enabled=false never do.
	svc.EnableAutoEnqueue(intel.AutoEnqueueOptions{
		Lookup:     a.intelSubscriptionAutoIntel,
		AutoChecks: func() bool { return runtimeConfigSnapshot(a.runtimeCfg).IntelAutoChecks },
		Logf:       log.Printf,
	})
	a.intelSvc = svc
	a.wireIntelSubscriptionAutoEnqueue()

	// WP10 §2: the routing admission path reads the intel projection, so the
	// pool needs it before any platform evaluates a node.
	if a.topoRuntime != nil && a.topoRuntime.pool != nil {
		a.topoRuntime.pool.SetQualitySnapshot(svc.Snapshot())
	}
	return nil
}

// intelJobConfig reports the hot-reloadable executor configuration (§3.4).
func (a *prismApp) intelJobConfig() jobs.Config {
	if a == nil || a.runtimeCfg == nil {
		return jobs.Config{Enabled: true}.Normalize()
	}
	rc := runtimeConfigSnapshot(a.runtimeCfg)
	return jobs.Config{
		Enabled:                  rc.IntelEnabled,
		NodeWorkers:              rc.IntelNodeWorkers,
		MaxRunningJobs:           rc.IntelMaxRunningJobs,
		CheckConcurrencyPerCheck: rc.IntelCheckConcurrencyPerCheck,
	}
}

// intelProviderRegistry builds the built-in data sources and applies their
// effective settings. Legacy PRISM_* variables are written into state.db once,
// so the settings page takes over afterwards (WP09 §1).
func (a *prismApp) intelProviderRegistry(engine *state.StateEngine) *providers.Registry {
	registry := providers.NewRegistry()

	var country providers.CountryLookup
	if a != nil && a.geoSvc != nil {
		country = a.geoSvc
	}
	geoDir := a.intelGeoDir()
	client := providers.NewStrictClient(intelHTTPTimeout)
	providers.RegisterBuiltins(registry, providers.BuiltinConfig{
		GeoDir:  geoDir,
		Country: country,
		Tor:     providers.NewTorRegistry(providers.TorRegistryOptions{Client: client, Now: time.Now}),
		Client:  client,
		Timeout: intelHTTPTimeout,
		Now:     time.Now,
	})

	specs := registry.Specs()
	var rows []model.IntelProviderSetting
	if engine != nil {
		if _, warnings, err := providers.SeedSettings(engine, specs, os.Getenv, time.Now().UnixNano()); err != nil {
			log.Printf("Warning: seed intel provider settings: %v", err)
		} else {
			for _, warning := range warnings {
				log.Printf("Warning: %s", warning)
			}
		}
		settings, err := engine.ListIntelProviderSettings()
		if err != nil {
			log.Printf("Warning: list intel provider settings: %v", err)
		} else {
			rows = settings
		}
	}
	registry.Apply(providers.ResolveSettings(specs, rows, os.Getenv))
	return registry
}

// intelChecksEngine loads the unlock check rules and honours the persisted
// per-check enabled toggle (provider_id "check:<id>", WP09 §5.4).
func (a *prismApp) intelChecksEngine(engine *state.StateEngine) *checks.Engine {
	stateDir := ""
	if a != nil && a.envCfg != nil {
		stateDir = a.envCfg.StateDir
	}
	return checks.NewEngine(checks.Options{
		UserDir:             checks.UserRuleDir(stateDir),
		EnabledSource:       intelCheckEnabledSource{engine: engine},
		ConcurrencyPerCheck: a.intelJobConfig().CheckConcurrencyPerCheck,
		Logf:                log.Printf,
	})
}

// withCheckConcurrency applies intel_check_concurrency_per_check before a check
// step runs, so a settings change takes effect without a restart.
func (a *prismApp) withCheckConcurrency(engine *checks.Engine, inner func(ctx context.Context, jobID, nodeHash string) jobs.StepResult) func(ctx context.Context, jobID, nodeHash string) jobs.StepResult {
	applied := atomic.Int64{}
	if engine != nil {
		applied.Store(int64(a.intelJobConfig().CheckConcurrencyPerCheck))
	}
	return func(ctx context.Context, jobID, nodeHash string) jobs.StepResult {
		if engine != nil {
			want := int64(a.intelJobConfig().CheckConcurrencyPerCheck)
			if want > 0 && applied.Swap(want) != want {
				engine.SetConcurrencyPerCheck(int(want))
			}
		}
		return inner(ctx, jobID, nodeHash)
	}
}

// intelCheckEnabledSource resolves the persisted per-check enabled toggle.
type intelCheckEnabledSource struct {
	engine *state.StateEngine
}

// CheckEnabled implements checks.EnabledSource.
func (s intelCheckEnabledSource) CheckEnabled(checkID string) (bool, bool) {
	if s.engine == nil {
		return false, false
	}
	settings, err := s.engine.ListIntelProviderSettings()
	if err != nil {
		return false, false
	}
	wanted := checks.CheckSettingID(checkID)
	for _, row := range settings {
		if row.ProviderID == wanted {
			return row.Enabled, true
		}
	}
	return false, false
}

// intelNodeOutbound resolves a node hash to its live node outbound. Requests of
// the via-node data sources and of the unlock checks leave through it, never
// through the host network or the platform router (WP09 §1).
func (a *prismApp) intelNodeOutbound(nodeHash string) (adapter.Outbound, bool) {
	if a == nil || a.topoRuntime == nil || a.topoRuntime.pool == nil {
		return nil, false
	}
	hash, err := node.ParseHex(strings.TrimSpace(nodeHash))
	if err != nil {
		return nil, false
	}
	entry, ok := a.topoRuntime.pool.GetEntry(hash)
	if !ok || entry == nil {
		return nil, false
	}
	outbound := entry.Outbound.Load()
	if outbound == nil {
		return nil, false
	}
	return *outbound, true
}

// intelSubscriptionAutoIntel resolves the persisted auto_intel flag of one
// subscription (WP08 §3.6).
func (a *prismApp) intelSubscriptionAutoIntel(subscriptionID string) (bool, bool) {
	if a == nil || a.stateEngine == nil {
		return false, false
	}
	subscriptions, err := a.stateEngine.ListSubscriptions()
	if err != nil {
		log.Printf("Warning: list subscriptions for intel auto-enqueue: %v", err)
		return false, false
	}
	for _, sub := range subscriptions {
		if sub.ID == subscriptionID {
			return sub.AutoIntel, true
		}
	}
	return false, false
}

// wireIntelSubscriptionAutoEnqueue tells the intel service about every node a
// subscription refresh added. The hook is additive: the upstream node-added
// wiring keeps running.
func (a *prismApp) wireIntelSubscriptionAutoEnqueue() {
	if a == nil || a.intelSvc == nil || a.topoRuntime == nil || a.topoRuntime.pool == nil {
		return
	}
	pool := a.topoRuntime.pool
	pool.AddOnNodeAdded(func(hash node.Hash) {
		svc := a.intelSvc
		if svc == nil {
			return
		}
		entry, ok := pool.GetEntry(hash)
		if !ok || entry == nil {
			return
		}
		for _, subscriptionID := range entry.SubscriptionIDs() {
			svc.OfferSubscriptionNode(subscriptionID, hash.String())
		}
	})
}

// assessmentSweepInterval is the WP10 §1.8 expiry sweep period.
const assessmentSweepInterval = 5 * time.Minute

// intelEnabledSources reports the enabled data sources for the coverage
// denominator (§1.2). The effective setting decides: a data source that needs a
// missing key, or that the settings page disabled, does not count as coverage.
func intelEnabledSources(registry *providers.Registry) map[string]bool {
	enabled := make(map[string]bool)
	for _, id := range assess.ScoringSources() {
		enabled[id] = true
	}
	if registry == nil {
		return enabled
	}
	for _, setting := range registry.Settings() {
		if _, known := enabled[setting.Spec.ID]; !known {
			continue
		}
		enabled[setting.Spec.ID] = setting.Enabled && setting.Runnable()
	}
	return enabled
}

// startIntel attaches the egress observer to the probe manager and starts the
// executor. It runs after the probe manager is up so the observer receives the
// periodic samples.
func (a *prismApp) startIntel() error {
	if a.intelSvc == nil {
		return nil
	}
	if a.topoRuntime.probeMgr != nil {
		// WP08 §4: the callback only enqueues a bounded sample; a separate writer
		// goroutine batches the database writes.
		a.topoRuntime.probeMgr.SetOnEgressObserved(func(hash node.Hash, ip netip.Addr, loc string) {
			a.intelSvc.ObserveEgress(hash.String(), ip, loc)
		})
	}
	if err := a.intelSvc.Start(); err != nil {
		return fmt.Errorf("intel service start: %w", err)
	}
	log.Println("Intel store, projection and job executor started (step 12)")
	return nil
}

// KnownNodeHashes implements jobs.NodeCensus: it reports the node hashes that
// still exist in the running pool so the cleaner can drop intel.db rows of
// nodes that were deleted.
func (a *prismApp) KnownNodeHashes() map[string]struct{} {
	known := make(map[string]struct{})
	if a == nil || a.topoRuntime == nil || a.topoRuntime.pool == nil {
		return known
	}
	a.topoRuntime.pool.Range(func(hash node.Hash, _ *node.NodeEntry) bool {
		known[hash.String()] = struct{}{}
		return true
	})
	return known
}

// resolveIntelScope expands a job scope into node hashes. Every selector is a
// union (WP08 §3.1); the expansion happens server-side and it never truncates:
// a scope above jobs.MaxNodesPerJob fails with the named limit error.
func (a *prismApp) resolveIntelScope(_ context.Context, scope jobs.Scope) ([]string, error) {
	selected := make(map[string]struct{})
	add := func(hash string) {
		trimmed := strings.TrimSpace(hash)
		if trimmed == "" {
			return
		}
		selected[trimmed] = struct{}{}
	}

	if a == nil || a.topoRuntime == nil || a.topoRuntime.pool == nil {
		for _, hash := range scope.NodeHashes {
			add(hash)
		}
		if len(selected) > maxIntelScopeNodes {
			return nil, fmt.Errorf("%w: %d > %d", jobs.ErrTooManyNodes, len(selected), maxIntelScopeNodes)
		}
		return scopeHashes(selected), nil
	}

	for _, hash := range scope.NodeHashes {
		add(hash)
	}

	// An explicit filter is expanded against the running pool; an empty filter
	// only expands when scope.all is set. The walk stops one node past the
	// bound so the limit error below is always reported instead of a silent
	// truncation.
	if scope.All || len(scope.Filter) > 0 {
		filter := newNodeFilter(scope.Filter)
		a.topoRuntime.pool.Range(func(hash node.Hash, entry *node.NodeEntry) bool {
			if filter.matches(entry) {
				add(hash.String())
			}
			return len(selected) <= maxIntelScopeNodes
		})
	}

	if len(scope.SubscriptionIDs) > 0 {
		wanted := make(map[string]struct{}, len(scope.SubscriptionIDs))
		for _, id := range scope.SubscriptionIDs {
			wanted[strings.TrimSpace(id)] = struct{}{}
		}
		engine := a.stateEngine
		if engine == nil {
			return nil, fmt.Errorf("resolve subscription scope: state engine unavailable")
		}
		links, err := engine.LoadAllSubscriptionNodes()
		if err != nil {
			return nil, fmt.Errorf("resolve subscription scope: %w", err)
		}
		for _, link := range links {
			if _, ok := wanted[link.SubscriptionID]; !ok {
				continue
			}
			add(link.NodeHash)
		}
	}

	if len(scope.PlatformIDs) > 0 {
		for _, platformID := range scope.PlatformIDs {
			plat, ok := a.topoRuntime.pool.GetPlatform(strings.TrimSpace(platformID))
			if !ok {
				continue
			}
			plat.View().Range(func(hash node.Hash) bool {
				add(hash.String())
				return true
			})
		}
	}

	if len(selected) > maxIntelScopeNodes {
		return nil, fmt.Errorf("%w: %d > %d", jobs.ErrTooManyNodes, len(selected), maxIntelScopeNodes)
	}
	return scopeHashes(selected), nil
}

// scopeHashes renders the selected node hashes in a stable order.
func scopeHashes(selected map[string]struct{}) []string {
	out := make([]string, 0, len(selected))
	for hash := range selected {
		out = append(out, hash)
	}
	sort.Strings(out)
	return out
}

// nodeFilter is the subset of the GET /api/v1/nodes query parameters the job
// scope understands today. WP10 extends it with the purity filters.
type nodeFilter struct {
	protocol string
	engine   string
}

func newNodeFilter(raw map[string]string) nodeFilter {
	if len(raw) == 0 {
		return nodeFilter{}
	}
	return nodeFilter{
		protocol: strings.ToLower(strings.TrimSpace(raw["protocol"])),
		engine:   strings.ToLower(strings.TrimSpace(raw["engine"])),
	}
}

func (f nodeFilter) matches(entry *node.NodeEntry) bool {
	if entry == nil {
		return false
	}
	if f.protocol != "" && !strings.EqualFold(nodeProtocol(entry), f.protocol) {
		return false
	}
	if f.engine != "" && !strings.EqualFold(nodeEngine(entry), f.engine) {
		return false
	}
	return true
}

// nodeProtocol reads the outbound type of a node entry.
func nodeProtocol(entry *node.NodeEntry) string {
	if entry == nil {
		return ""
	}
	outbound := entry.Outbound.Load()
	if outbound == nil {
		return ""
	}
	return (*outbound).Type()
}

// nodeEngine reports which kernel serves the node. WP07 publishes the real
// decision; until then every node is a sing-box node.
func nodeEngine(entry *node.NodeEntry) string {
	if entry == nil {
		return ""
	}
	return "singbox"
}

// intelGeoDir is $PRISM_CACHE_DIR/geo, the directory of the offline databases
// (WP09 §3).
func (a *prismApp) intelGeoDir() string {
	if a == nil || a.envCfg == nil || strings.TrimSpace(a.envCfg.CacheDir) == "" {
		return ""
	}
	return filepath.Join(a.envCfg.CacheDir, intelGeoDirName)
}

// intelGeoDatabases builds the bounded background refresher of the offline
// databases (WP09 §3). It performs no network I/O itself: the downloads run in
// the refresher goroutine, so a missing database only degrades its provider to
// PROVIDER_UNAVAILABLE until the file lands.
func (a *prismApp) intelGeoDatabases(registry *providers.Registry) *providers.GeoManager {
	if a == nil || registry == nil {
		return nil
	}
	geoDir := a.intelGeoDir()
	if geoDir == "" {
		return nil
	}
	direct := netutil.NewDirectDownloader(
		func() time.Duration { return providers.GeoDBRequestTimeout },
		currentDownloadUserAgent,
	)
	direct.MaxBodyBytes = providers.GeoDBMaxDownloadBytes
	manager := providers.NewGeoManager(providers.GeoManagerOptions{
		Dir: geoDir,
		// The direct download is the normal path; a node is borrowed when it
		// fails, like the country.mmdb download of the upstream GeoIP service.
		Downloader: &netutil.RetryDownloader{
			Direct:              direct,
			ProxyAttemptTimeout: providers.GeoDBRequestTimeout,
			NodePicker:          a.downloadNodePicker(),
			ProxyFetch:          a.intelGeoNodeFetch,
		},
		Setting: func(id string) (providers.Setting, bool) {
			return registry.Setting(id)
		},
		Now:  time.Now,
		Logf: log.Printf,
	})
	a.geoDBs = manager
	return manager
}

// intelGeoNodeFetch downloads one database through a node with the database
// size bound instead of the resource default: the MaxMind archive is larger
// than a subscription document, and a download above its bound must fail
// instead of being truncated.
func (a *prismApp) intelGeoNodeFetch(ctx context.Context, hash node.Hash, target string) ([]byte, error) {
	if a == nil || a.topoRuntime == nil || a.topoRuntime.outboundMgr == nil {
		return nil, errors.New("outbound manager is not available")
	}
	body, _, err := a.topoRuntime.outboundMgr.FetchWithOptions(ctx, hash, target, netutil.OutboundHTTPOptions{
		RequireStatusOK: true,
		UserAgent:       currentDownloadUserAgent(),
		MaxBodyBytes:    providers.GeoDBMaxDownloadBytes,
	})
	return body, err
}
