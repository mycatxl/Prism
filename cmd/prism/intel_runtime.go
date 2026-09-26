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

// intelEgressTraceURL returns the egress trace target the intel probe must use.
//
// It reads the same runtime setting as the main egress probe, so a deployment
// that points Prism at a self-hosted (or loopback) trace endpoint gets the same
// behaviour from the intel pipeline. An empty result means "use the package
// default", which is what the probe already does when the field is empty.
func (a *prismApp) intelEgressTraceURL() string {
	if a == nil || a.runtimeCfg == nil {
		return ""
	}
	return strings.TrimSpace(runtimeConfigSnapshot(a.runtimeCfg).EgressTraceURL)
}

// initIntelJobs wires the intel store, the in-memory projection and the batch
// executor into the running application (assembly step 12).
func (a *prismApp) initIntelJobs(engine *state.StateEngine, intelStore *store.Store) error {
	if intelStore == nil {
		return nil
	}
	a.stateEngine = engine

	// The intel probe follows the same runtime setting as the main egress probe,
	// so pointing Prism at a self-hosted trace endpoint (or running fully offline
	// against a loopback one) applies to both. Leaving these empty would keep
	// this probe on the compiled-in Cloudflare default while the main probe
	// honoured the configuration.
	//
	// The value is read here rather than through a callback because egress.Probe
	// takes a plain string; a later change of the setting takes effect on the
	// next start, which is the same contract the offline smoke run relies on.
	traceURL := a.intelEgressTraceURL()
	prober := &egress.Probe{
		Fetcher: a.topoRuntime.outboundMgr,
		Store:   intelStore,
		URLv4:   traceURL,
		URLv6:   traceURL,
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

// resolveIntelScope expands a job scope into node hashes. The selectors (all,
// subscription ids, platform ids, node hashes) are a union (WP08 §3.1) and an
// explicit filter is applied on top of the all/subscription/platform selectors —
// but never to node_hashes, because a node named by hash is taken as given. The
// expansion happens server-side and it never truncates: a scope above
// jobs.MaxNodesPerJob fails with the named limit error.
func (a *prismApp) resolveIntelScope(_ context.Context, scope jobs.Scope) ([]string, error) {
	// Reject an unknown filter key instead of ignoring it. Ignoring it would make
	// a narrowing request expand to the whole pool, and the caller would never
	// learn that the key it sent had no effect.
	if unknown := unknownScopeFilterKeys(scope.Filter); len(unknown) > 0 {
		return nil, fmt.Errorf("%w: unknown scope filter key(s) %s", jobs.ErrInvalidJob, strings.Join(unknown, ", "))
	}
	// An explicit hash must be a real node hash. Without this every other string
	// becomes a job item that can never resolve: it burns the item attempt budget
	// (maxItemAttempts) and produces nothing but churn.
	for _, hash := range scope.NodeHashes {
		trimmed := strings.TrimSpace(hash)
		if trimmed == "" {
			continue
		}
		if _, err := node.ParseHex(trimmed); err != nil {
			return nil, fmt.Errorf("%w: malformed node hash %q", jobs.ErrInvalidJob, trimmed)
		}
	}

	selected := make(map[string]struct{})
	add := func(hash string) {
		trimmed := strings.TrimSpace(hash)
		if trimmed == "" {
			return
		}
		selected[trimmed] = struct{}{}
	}

	// A missing pool is not a reason to skip the subscription branch: that branch
	// reads state.db, not the pool. Only the pool-backed selectors (all, filter,
	// platform ids) need it, and those are guarded where they are used below.
	// Returning early here used to drop subscription scopes entirely, so a job
	// created with {subscription_ids:[x]} resolved to an empty scope and silently
	// did nothing.
	hasPool := a != nil && a.topoRuntime != nil && a.topoRuntime.pool != nil

	for _, hash := range scope.NodeHashes {
		add(hash)
	}

	// The filter narrows every selector EXCEPT the explicit node hashes: a node
	// named by hash is taken as given, while the all/subscription/platform scopes
	// are intersected with it. Without this, {subscription_ids:[x], filter:{...}}
	// means "every filtered node in the pool UNION every node of x" instead of
	// "the nodes of x that match the filter".
	filter := newNodeFilter(scope.Filter, a.intelScopeGeoLookup(), a.intelScopeSnapshot())
	// Only a filter that actually constrains something may widen the walk below.
	// Reading len(scope.Filter) instead would treat any non-empty map as a
	// narrowing request.
	hasFilter := filter.constrained()
	// keep decides whether a node found through a selector passes the filter. It
	// needs the pool, so without one it can only answer "no filter, keep".
	keep := func(hash string) bool {
		if !hasFilter {
			return true
		}
		if !hasPool {
			return false
		}
		parsed, err := node.ParseHex(hash)
		if err != nil {
			return false
		}
		entry, ok := a.topoRuntime.pool.GetEntry(parsed)
		if !ok {
			return false
		}
		return filter.matches(entry)
	}

	// An empty filter only expands when scope.all is set; otherwise the pool walk
	// below is skipped and only the explicit selectors contribute. The walk stops
	// one node past the bound so the limit error is always reported instead of a
	// silent truncation.
	if hasPool && (scope.All || hasFilter) {
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
			// A persisted link can carry a hash that is not a real node hash (a
			// hand-edited cache.db, a legacy row). Such a hash would become a job
			// item that can never resolve, so it is dropped here exactly like an
			// explicit node_hashes entry is rejected above.
			if _, err := node.ParseHex(strings.TrimSpace(link.NodeHash)); err != nil {
				continue
			}
			if !keep(link.NodeHash) {
				continue
			}
			add(link.NodeHash)
		}
	}

	if hasPool && len(scope.PlatformIDs) > 0 {
		for _, platformID := range scope.PlatformIDs {
			plat, ok := a.topoRuntime.pool.GetPlatform(strings.TrimSpace(platformID))
			if !ok {
				continue
			}
			plat.View().Range(func(hash node.Hash) bool {
				if keep(hash.String()) {
					add(hash.String())
				}
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

// nodeFilter is the subset of the GET /api/v1/nodes query parameters a job
// scope understands (§3.1): the WP08 protocol/engine pair plus the WP10 §4
// purity filters and the health switch.
//
// matches() runs once per node on the pool.Range hot path, so it only reads
// in-memory state. protocol/engine/region/healthy come from the NodeEntry
// itself (atomic loads; a GeoIP-backed region uses the same in-memory lookup
// the node list uses), and the purity keys read the intel assessment
// projection, which is a map lookup keyed by egress IP — never a database
// query.
type nodeFilter struct {
	protocol   string
	engine     string
	region     string
	ipType     string
	purityBand string
	// verdicts holds the comma-separated `verdict` values; empty means no
	// verdict constraint.
	verdicts []string
	// healthy is true only for the literal "true"; every other value adds no
	// constraint.
	healthy bool

	// geoLookup resolves a node region (nil degrades to the explicit probe
	// region); snapshot is the intel projection (nil matches no purity key).
	geoLookup func(netip.Addr) string
	snapshot  *intel.Snapshot
}

// intelScopeGeoLookup returns the GeoIP lookup the region filter resolves
// with, or nil when the service is not wired into this build.
func (a *prismApp) intelScopeGeoLookup() func(netip.Addr) string {
	if a == nil || a.geoSvc == nil {
		return nil
	}
	return a.geoSvc.Lookup
}

// intelScopeSnapshot returns the in-memory assessment projection the purity
// filters read, or nil when the intel subsystem is not wired into this build.
func (a *prismApp) intelScopeSnapshot() *intel.Snapshot {
	if a == nil || a.intelSvc == nil {
		return nil
	}
	return a.intelSvc.Snapshot()
}

// nodeFilterValue lowercases and trims one scope filter value; every key of
// the scope filter is compared normalized (§3.1).
func nodeFilterValue(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// scopeFilterKeys is the closed set of keys `scope.filter` understands. The node
// list exposes more filters than a job scope can honour (egress_ip, tag_keyword,
// quality_state, purity_min, ...), so a request may carry a key this scope does
// not know. Dropping such a key silently would turn a narrowing request into
// "every node in the pool", which is the opposite of what the caller asked for,
// so unknown keys are rejected instead (see unknownScopeFilterKeys).
var scopeFilterKeys = map[string]struct{}{
	"protocol": {}, "engine": {}, "region": {}, "ip_type": {},
	"purity_band": {}, "verdict": {}, "healthy": {},
}

// unknownScopeFilterKeys returns the scope filter keys that are not part of the
// documented set, sorted for a stable error message.
func unknownScopeFilterKeys(raw map[string]string) []string {
	var unknown []string
	for key := range raw {
		if _, ok := scopeFilterKeys[strings.ToLower(strings.TrimSpace(key))]; !ok {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// constrained reports whether this filter actually restricts anything. It is the
// predicate the expansion uses to decide whether to walk the pool: a filter map
// with no recognised key constrains nothing, whatever its length.
func (f nodeFilter) constrained() bool {
	return f.protocol != "" || f.engine != "" || f.region != "" ||
		f.ipType != "" || f.purityBand != "" || len(f.verdicts) > 0 || f.healthy
}

func newNodeFilter(raw map[string]string, geoLookup func(netip.Addr) string, snapshot *intel.Snapshot) nodeFilter {
	if len(raw) == 0 {
		return nodeFilter{}
	}
	filter := nodeFilter{
		protocol:   nodeFilterValue(raw["protocol"]),
		engine:     nodeFilterValue(raw["engine"]),
		region:     nodeFilterValue(raw["region"]),
		ipType:     nodeFilterValue(raw["ip_type"]),
		purityBand: nodeFilterValue(raw["purity_band"]),
		healthy:    nodeFilterValue(raw["healthy"]) == "true",
		geoLookup:  geoLookup,
		snapshot:   snapshot,
	}
	// verdict is the one §4 key that takes a comma-separated list: the node
	// list reads "favorable,caution" as "either of the two", so the scope does
	// too.
	for _, value := range strings.Split(raw["verdict"], ",") {
		if verdict := nodeFilterValue(value); verdict != "" {
			filter.verdicts = append(filter.verdicts, verdict)
		}
	}
	return filter
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
	if f.healthy && !entry.IsHealthy() {
		return false
	}
	// GetRegion prefers the explicit probe region and falls back to the GeoIP
	// lookup; an unknown region never matches a region filter, exactly like
	// the node list.
	if f.region != "" && !strings.EqualFold(entry.GetRegion(f.geoLookup), f.region) {
		return false
	}
	if f.ipType == "" && f.purityBand == "" && len(f.verdicts) == 0 {
		return true
	}
	// The purity keys require an assessment: an unknown value cannot satisfy
	// a filter. This is the same fail-closed rule the node list uses
	// (internal/service/control_plane_node_intel.go:243).
	lite, ok := f.assessment(entry)
	if !ok {
		return false
	}
	if f.ipType != "" && !strings.EqualFold(intel.IPTypeName(lite.IPType), f.ipType) {
		return false
	}
	if f.purityBand != "" && !f.bandMatches(lite) {
		return false
	}
	if len(f.verdicts) > 0 {
		verdict := intel.VerdictName(lite.Verdict)
		matched := false
		for _, want := range f.verdicts {
			if strings.EqualFold(verdict, want) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// bandMatches applies purity_band. The API vocabulary also accepts "review",
// the legacy name for the review/conflicting verdicts; the node list maps it
// the same way (internal/service/control_plane_node_intel.go:250), so one
// filter string selects the same nodes on both paths.
func (f nodeFilter) bandMatches(lite intel.AssessmentLite) bool {
	if f.purityBand == "review" {
		verdict := intel.VerdictName(lite.Verdict)
		return verdict == intel.VerdictName(intel.VerdictReview) ||
			verdict == intel.VerdictName(intel.VerdictConflicting)
	}
	return strings.EqualFold(intel.BandName(lite.Band), f.purityBand)
}

// assessment resolves the projected assessment of one node's egress IP. The
// projection is keyed by egress IP, so a node without an egress observation
// has no assessment and never matches a purity key.
func (f nodeFilter) assessment(entry *node.NodeEntry) (intel.AssessmentLite, bool) {
	if f.snapshot == nil || entry == nil {
		return intel.AssessmentLite{}, false
	}
	ip := entry.GetEgressIP()
	if !ip.IsValid() {
		return intel.AssessmentLite{}, false
	}
	return f.snapshot.Assessment(ip)
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
