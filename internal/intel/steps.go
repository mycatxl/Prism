package intel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"

	"prism/internal/intel/checks"
	"prism/internal/intel/jobs"
	"prism/internal/intel/providers"
	"prism/internal/intel/store"
)

// Bounds of the WP09 pipeline steps (R4: every queue, worker and cache is
// bounded and has a defined over-limit behaviour).
const (
	// DefaultMaxEvidencePerStep bounds how many provider lookups one pipeline
	// step may perform. Two egress addresses times the built-in data sources
	// stay far below it; the bound exists so a future provider catalogue can
	// never turn one job item into an unbounded amount of work.
	DefaultMaxEvidencePerStep = 64
	// DefaultMaxChecksPerStep bounds how many unlock rules one item may run.
	DefaultMaxChecksPerStep = 32
	// DefaultViaNodeDeferral bounds how long the via-node step may park an item
	// waiting for a data source budget/QPS gate (§3.2 step 4). A gate that opens
	// later than this is reported as an explicit skip instead of an endless
	// requeue.
	DefaultViaNodeDeferral = 30 * time.Minute
	// maxJobRequestCache bounds the per-job request memo of the executor.
	maxJobRequestCache = 128
)

// Named step failure codes. They land in job_items.error_code and in the step
// summary, so an operator can always tell why a step did nothing.
const (
	// CodeStepLimitExceeded marks a step that hit one of the bounds above.
	CodeStepLimitExceeded = "STEP_LIMIT_EXCEEDED"
	// CodeNodeOutboundUnavailable marks a via-node/check step whose node has no
	// ready outbound yet; the item is retried with backoff.
	CodeNodeOutboundUnavailable = "NODE_OUTBOUND_UNAVAILABLE"
	// CodeJobUnavailable marks a step whose job request could not be read.
	CodeJobUnavailable = "JOB_UNAVAILABLE"
	// CodeProviderUnavailable marks a provider that is not runnable.
	CodeProviderUnavailable = "PROVIDER_UNAVAILABLE"
)

// StepOptions supplies the runtime dependencies of the WP09 pipeline steps
// (offline data sources, the online provider queue, via-node data sources and
// the unlock checks). A nil field degrades the matching step to an explicit,
// explainable skip instead of a nil dereference.
type StepOptions struct {
	// Store is the authoritative intel.db handle.
	Store *store.Store
	// Registry holds the built-in data sources and their effective settings.
	Registry *providers.Registry
	// Checks is the unlock rule engine.
	Checks *checks.Engine
	// Outbound resolves the node under test to its live node outbound. Requests
	// of the via-node sources and of every check step leave through it and never
	// through the host network, the platform router or a bypass rule.
	Outbound func(nodeHash string) (adapter.Outbound, bool)
	// Config reports the live runtime configuration (intel_enabled).
	Config jobs.ConfigProvider
	// Clock is the injected clock.
	Clock jobs.Clock
	// Logf receives diagnostics.
	Logf func(format string, args ...any)

	// Bounds; zero uses the defaults above.
	MaxEvidencePerStep int
	MaxChecksPerStep   int
	ViaNodeDeferral    time.Duration
}

// NewStepHandlers builds the real pipeline handlers of WP09. Steps whose
// dependency is missing keep a nil handler so the pipeline reports
// {"skipped":"no handler for step N"} exactly as before.
func NewStepHandlers(opts StepOptions) StepHandlers {
	e := newStepExecutor(opts)
	handlers := StepHandlers{}
	if e.store != nil && e.registry != nil {
		handlers.Offline = e.offlineStep
		handlers.EnqueueOnline = e.enqueueOnlineStep
		handlers.ViaNode = e.viaNodeStep
	}
	if e.store != nil && e.checks != nil && e.outbound != nil {
		handlers.Checks = e.checksStep
	}
	return handlers
}

// NewOnlineLookup builds the batch lookup used by the online-ip queue workers
// (§3.3). It performs the provider call, persists the evidence (including failed
// attempts) and reports the outcome to the scheduler. A nil result means the
// queue workers must not start.
func NewOnlineLookup(opts StepOptions) jobs.OnlineLookup {
	e := newStepExecutor(opts)
	if e.store == nil || e.registry == nil {
		return nil
	}
	return jobs.OnlineLookupFunc(e.onlineLookup)
}

// QueueSpecs adapts the WP09 provider registry to the queue-worker
// specification source of WP08 §3.3. The registry resolves enabled, runnable
// and credentialed providers, so the queue workers always see the current
// settings without a restart.
func QueueSpecs(registry *providers.Registry) jobs.ProviderSpecSource {
	if registry == nil {
		return func() []jobs.ProviderQueueSpec { return nil }
	}
	return func() []jobs.ProviderQueueSpec {
		specs := registry.QueueSpecs()
		out := make([]jobs.ProviderQueueSpec, 0, len(specs))
		for _, spec := range specs {
			out = append(out, jobs.ProviderQueueSpec{
				ProviderID:   spec.ProviderID,
				Enabled:      spec.Enabled,
				DailyLimit:   spec.DailyLimit,
				QPS:          spec.QPS,
				BatchSize:    spec.BatchSize,
				CredentialID: spec.CredentialID,
			})
		}
		return out
	}
}

// stepExecutor implements the providers/checks glue with the store, the
// registry and the node outbound resolver as its only side effects.
type stepExecutor struct {
	store    *store.Store
	registry *providers.Registry
	checks   *checks.Engine
	outbound func(nodeHash string) (adapter.Outbound, bool)
	config   jobs.ConfigProvider
	clock    jobs.Clock
	logf     func(string, ...any)

	maxEvidencePerStep int
	maxChecksPerStep   int
	deferral           time.Duration

	mu       sync.Mutex
	requests map[string]jobContext
}

// jobContext is the memoised per-job input of the provider steps: the persisted
// request plus the job priority that seeds the online queue.
type jobContext struct {
	request  jobs.Request
	priority int
}

func newStepExecutor(opts StepOptions) *stepExecutor {
	clock := opts.Clock
	if clock == nil {
		clock = jobs.SystemClock{}
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	e := &stepExecutor{
		store:              opts.Store,
		registry:           opts.Registry,
		checks:             opts.Checks,
		outbound:           opts.Outbound,
		config:             opts.Config,
		clock:              clock,
		logf:               logf,
		maxEvidencePerStep: opts.MaxEvidencePerStep,
		maxChecksPerStep:   opts.MaxChecksPerStep,
		deferral:           opts.ViaNodeDeferral,
		requests:           make(map[string]jobContext),
	}
	if e.maxEvidencePerStep <= 0 {
		e.maxEvidencePerStep = DefaultMaxEvidencePerStep
	}
	if e.maxChecksPerStep <= 0 {
		e.maxChecksPerStep = DefaultMaxChecksPerStep
	}
	if e.deferral <= 0 {
		e.deferral = DefaultViaNodeDeferral
	}
	return e
}

// skipped builds the pipeline's no-op result: the step did nothing for a named
// reason but the item still reaches a terminal state.
func skipped(reason string) jobs.StepResult {
	return jobs.StepResult{Summary: map[string]any{"skipped": reason}}
}

func (e *stepExecutor) now() time.Time { return e.clock.Now().UTC() }

// enabled reports whether intel job execution is switched on (intel_enabled).
func (e *stepExecutor) enabled() bool {
	if e.config == nil {
		return true
	}
	return e.config().Normalize().Enabled
}

// jobFor returns the memoised job context of one item. The memo is bounded: a
// full map is dropped instead of growing without bound.
func (e *stepExecutor) jobFor(ctx context.Context, jobID string) (jobContext, error) {
	e.mu.Lock()
	if cached, ok := e.requests[jobID]; ok {
		e.mu.Unlock()
		return cached, nil
	}
	e.mu.Unlock()

	job, err := e.store.GetJob(ctx, jobID)
	if err != nil {
		return jobContext{}, err
	}
	var req jobs.Request
	if strings.TrimSpace(job.RequestJSON) != "" {
		if err := json.Unmarshal([]byte(job.RequestJSON), &req); err != nil {
			return jobContext{}, fmt.Errorf("decode job request %s: %w", jobID, err)
		}
	}
	if kind, err := jobs.ParseKind(job.Kind); err == nil {
		req.Kind = kind
	}
	entry := jobContext{request: req, priority: job.Priority}
	if entry.priority <= 0 {
		entry.priority = jobs.PriorityRefresh
	}

	e.mu.Lock()
	if len(e.requests) >= maxJobRequestCache {
		// Bounded over-limit behaviour: drop the memo instead of growing.
		e.requests = make(map[string]jobContext, maxJobRequestCache)
	}
	e.requests[jobID] = entry
	e.mu.Unlock()
	return entry, nil
}

// egressIPs reads the node's persisted egress addresses (step 1 output).
func (e *stepExecutor) egressIPs(ctx context.Context, nodeHash string) ([]netip.Addr, bool, error) {
	row, ok, err := e.store.GetNodeEgress(ctx, nodeHash)
	if err != nil || !ok {
		return nil, false, err
	}
	ips := make([]netip.Addr, 0, 2)
	for _, raw := range []string{row.IPv4, row.IPv6} {
		addr, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil || !addr.IsValid() {
			continue
		}
		ips = append(ips, addr.Unmap())
	}
	return ips, true, nil
}

// resolveOutbound returns the live outbound of the node under test.
func (e *stepExecutor) resolveOutbound(nodeHash string) (adapter.Outbound, bool) {
	if e.outbound == nil {
		return nil, false
	}
	ob, ok := e.outbound(nodeHash)
	if !ok || ob == nil {
		return nil, false
	}
	return ob, true
}

// providerSelection filters a provider selection against an enabled set. An
// empty selection means "every enabled provider" (WP09 §3).
func providerSelection(selection []string, available []providers.Spec) []providers.Spec {
	if len(selection) == 0 {
		return available
	}
	wanted := make(map[string]struct{}, len(selection))
	for _, id := range selection {
		wanted[strings.TrimSpace(id)] = struct{}{}
	}
	out := make([]providers.Spec, 0, len(available))
	for _, spec := range available {
		if _, ok := wanted[spec.ID]; ok {
			out = append(out, spec)
		}
	}
	return out
}

// offlineSpecs returns the enabled offline data sources of one selection.
func (e *stepExecutor) offlineSpecs(selection []string) []providers.Spec {
	specs := make([]providers.Spec, 0, 4)
	for _, setting := range e.registry.Settings() {
		if setting.Spec.Kind != providers.KindOffline || !setting.Enabled {
			continue
		}
		specs = append(specs, setting.Spec)
	}
	return providerSelection(selection, specs)
}

// viaNodeSpecs returns the enabled via-node data sources of one selection.
func (e *stepExecutor) viaNodeSpecs(selection []string) []providers.Spec {
	specs := make([]providers.Spec, 0, 4)
	for _, setting := range e.registry.Settings() {
		if setting.Spec.Kind != providers.KindViaNode || !setting.Enabled {
			continue
		}
		specs = append(specs, setting.Spec)
	}
	return providerSelection(selection, specs)
}

// persistResult writes one provider conclusion into intel.db. Failures are
// stored too (status=error/unsupported) so the assessment can tell "the source
// said nothing" from "the source was never asked" (WP10 §1.3).
func (e *stepExecutor) persistResult(ctx context.Context, spec providers.Spec, ip netip.Addr, result providers.Result, viaNode string) {
	if e.store == nil {
		return
	}
	now := e.now()
	row := store.Evidence{
		IP:           ip.Unmap().String(),
		Provider:     spec.ID,
		Profile:      spec.Profile,
		ViaNodeHash:  viaNode,
		ObservedAtNs: now.UnixNano(),
	}
	switch {
	case result.Evidence != nil:
		ev := *result.Evidence
		if strings.TrimSpace(ev.IP) == "" {
			ev.IP = row.IP
		}
		if strings.TrimSpace(ev.Provider) == "" {
			ev.Provider = spec.ID
		}
		if strings.TrimSpace(ev.Profile) == "" {
			ev.Profile = spec.Profile
		}
		if ev.ObservedAt.IsZero() {
			ev.ObservedAt = now
		}
		if ev.ValidUntil.IsZero() {
			ev.ValidUntil = now.Add(specTTL(spec))
		}
		row.Status = store.StatusOk
		row.ValidUntilNs = ev.ValidUntil.UTC().UnixNano()
		row.NormalizedJSON = marshalStepJSON(ev)
		row.RawJSON = rawJSON(result.Raw)
	case result.Err != nil:
		row.Status = store.StatusError
		if result.Err.Code == providers.CodeUnsupported {
			row.Status = store.StatusUnsupported
		}
		row.ErrorCode = result.Err.Code
		row.ValidUntilNs = now.Add(specTTL(spec)).UnixNano()
	default:
		row.Status = store.StatusError
		row.ErrorCode = providers.CodeResponse
		row.ValidUntilNs = now.Add(specTTL(spec)).UnixNano()
	}
	if err := e.store.UpsertEvidence(ctx, row); err != nil {
		e.logf("[intel] store %s evidence for %s: %v", spec.ID, row.IP, err)
	}
}

func specTTL(spec providers.Spec) time.Duration {
	if spec.DefaultTTL > 0 {
		return spec.DefaultTTL
	}
	return 24 * time.Hour
}

func rawJSON(raw []byte) sql.NullString {
	if len(raw) == 0 {
		return sql.NullString{}
	}
	if len(raw) > providers.MaxRawBytes {
		raw = raw[:providers.MaxRawBytes]
	}
	return sql.NullString{String: string(raw), Valid: true}
}

func marshalStepJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// ---------------------------------------------------------------------------
// step 2: offline completion
// ---------------------------------------------------------------------------

func (e *stepExecutor) offlineStep(ctx context.Context, jobID, nodeHash string) jobs.StepResult {
	if !e.enabled() {
		return skipped("intel jobs are disabled (intel_enabled=false)")
	}
	jc, err := e.jobFor(ctx, jobID)
	if err != nil {
		return jobs.StepResult{ErrorCode: CodeJobUnavailable, Summary: map[string]any{"error": err.Error()}}
	}
	ips, ok, err := e.egressIPs(ctx, nodeHash)
	if err != nil {
		return jobs.StepResult{ErrorCode: "EVIDENCE_READ_FAILED", Summary: map[string]any{"error": err.Error()}}
	}
	if !ok {
		return skipped("no egress observation for this node yet")
	}
	if len(ips) == 0 {
		return skipped("the node has no usable egress address")
	}
	specs := e.offlineSpecs(jc.request.Providers)
	if len(specs) == 0 {
		return skipped("no enabled offline data source")
	}
	if len(ips)*len(specs) > e.maxEvidencePerStep {
		return jobs.StepResult{
			ErrorCode: CodeStepLimitExceeded,
			Summary: map[string]any{
				"reason":      "offline lookups exceed the per-step bound",
				"addresses":   len(ips),
				"data_source": len(specs),
				"limit":       e.maxEvidencePerStep,
			},
		}
	}

	looked := 0
	failed := 0
	for _, ip := range ips {
		for _, spec := range specs {
			provider, ok := e.registry.Offline(spec.ID)
			if !ok {
				continue
			}
			result := provider.Lookup(ip)
			e.persistResult(ctx, spec, ip, result, "")
			looked++
			if result.Failed() {
				failed++
			}
		}
	}
	return jobs.StepResult{Summary: map[string]any{
		"offline_sources": len(specs),
		"offline_lookups": looked,
		"offline_failed":  failed,
	}}
}

// ---------------------------------------------------------------------------
// step 3: enqueue the online provider queue
// ---------------------------------------------------------------------------

func (e *stepExecutor) enqueueOnlineStep(ctx context.Context, jobID, nodeHash string) jobs.StepResult {
	if !e.enabled() {
		return skipped("intel jobs are disabled (intel_enabled=false)")
	}
	jc, err := e.jobFor(ctx, jobID)
	if err != nil {
		return jobs.StepResult{ErrorCode: CodeJobUnavailable, Summary: map[string]any{"error": err.Error()}}
	}
	ips, ok, err := e.egressIPs(ctx, nodeHash)
	if err != nil {
		return jobs.StepResult{ErrorCode: "EVIDENCE_READ_FAILED", Summary: map[string]any{"error": err.Error()}}
	}
	if !ok {
		return skipped("no egress observation for this node yet")
	}
	if len(ips) == 0 {
		return skipped("the node has no usable egress address")
	}
	specs := e.onlineSpecs(jc.request.Providers)
	if len(specs) == 0 {
		return skipped("no enabled online data source")
	}

	now := e.now().UnixNano()
	priority := jc.priority
	enqueued := 0
	fresh := 0
	queued := make([]string, 0, len(specs))
	for _, setting := range specs {
		queued = append(queued, setting.Spec.ID)
		for _, ip := range ips {
			if !jc.request.Force {
				has, err := e.store.HasValidEvidence(ctx, ip.String(), setting.Spec.ID, now)
				if err != nil {
					return jobs.StepResult{ErrorCode: "EVIDENCE_READ_FAILED", Summary: map[string]any{"error": err.Error()}}
				}
				if has {
					fresh++
					continue
				}
			}
			// §3.2 step 3: a lookup without current evidence re-queues a
			// terminal queue row; a queued or running row only has its
			// priority raised, so two jobs never duplicate one lookup.
			mode := store.EnqueueRefresh
			if jc.request.Force {
				mode = store.EnqueueForce
			}
			inserted, err := e.store.EnqueueProviderItem(ctx, store.QueueItem{
				Provider:     setting.Spec.ID,
				IP:           ip.String(),
				Priority:     priority,
				JobID:        jobID,
				Status:       store.QueueQueued,
				NextRunAtNs:  now,
				EnqueuedAtNs: now,
			}, mode)
			if err != nil {
				return jobs.StepResult{ErrorCode: "PROVIDER_ENQUEUE_FAILED", Summary: map[string]any{"error": err.Error()}}
			}
			if inserted {
				enqueued++
			}
		}
	}
	return jobs.StepResult{Summary: map[string]any{
		"online_providers": queued,
		"online_enqueued":  enqueued,
		"online_fresh":     fresh,
	}}
}

// onlineSpecs returns the enabled online-ip settings of one selection.
func (e *stepExecutor) onlineSpecs(selection []string) []providers.Setting {
	settings := make([]providers.Setting, 0, 6)
	for _, setting := range e.registry.Settings() {
		if setting.Spec.Kind != providers.KindOnlineIP || !setting.Enabled || !setting.Runnable() {
			continue
		}
		settings = append(settings, setting)
	}
	if len(selection) == 0 {
		return settings
	}
	wanted := make(map[string]struct{}, len(selection))
	for _, id := range selection {
		wanted[strings.TrimSpace(id)] = struct{}{}
	}
	out := make([]providers.Setting, 0, len(settings))
	for _, setting := range settings {
		if _, ok := wanted[setting.Spec.ID]; ok {
			out = append(out, setting)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// step 4: via-node data sources
// ---------------------------------------------------------------------------

func (e *stepExecutor) viaNodeStep(ctx context.Context, jobID, nodeHash string) jobs.StepResult {
	if !e.enabled() {
		return skipped("intel jobs are disabled (intel_enabled=false)")
	}
	jc, err := e.jobFor(ctx, jobID)
	if err != nil {
		return jobs.StepResult{ErrorCode: CodeJobUnavailable, Summary: map[string]any{"error": err.Error()}}
	}
	ips, ok, err := e.egressIPs(ctx, nodeHash)
	if err != nil {
		return jobs.StepResult{ErrorCode: "EVIDENCE_READ_FAILED", Summary: map[string]any{"error": err.Error()}}
	}
	if !ok {
		return skipped("no egress observation for this node yet")
	}
	if len(ips) == 0 {
		return skipped("the node has no usable egress address")
	}
	specs := e.viaNodeSpecs(jc.request.Providers)
	if len(specs) == 0 {
		return skipped("no enabled via-node data source")
	}
	if len(ips)*len(specs) > e.maxEvidencePerStep {
		return jobs.StepResult{
			ErrorCode: CodeStepLimitExceeded,
			Summary: map[string]any{
				"reason":      "via-node lookups exceed the per-step bound",
				"addresses":   len(ips),
				"data_source": len(specs),
				"limit":       e.maxEvidencePerStep,
			},
		}
	}
	ob, ready := e.resolveOutbound(nodeHash)
	if !ready {
		return jobs.StepResult{
			ErrorCode: CodeNodeOutboundUnavailable,
			Summary:   map[string]any{"reason": "the node connection is not ready"},
		}
	}

	now := e.now()
	looked := 0
	failed := 0
	ran := make([]string, 0, len(specs))
	skippedSources := make([]string, 0, len(specs))
	var deferUntil time.Time
	for _, spec := range specs {
		setting, ok := e.registry.Setting(spec.ID)
		if !ok {
			continue
		}
		provider, ok := e.registry.ViaNode(spec.ID)
		if !ok {
			continue
		}
		wait, reason := e.consumeViaNodeBudget(ctx, setting, nodeHash, now)
		if reason != "" {
			skippedSources = append(skippedSources, spec.ID+": "+reason)
			e.logf("[intel] via-node %s skipped: %s", spec.ID, reason)
			continue
		}
		if wait > 0 {
			// §3.2 step 4: never block the worker; park the item instead.
			until := now.Add(wait)
			if deferUntil.IsZero() || until.Before(deferUntil) {
				deferUntil = until
			}
			continue
		}
		ran = append(ran, spec.ID)
		for _, ip := range ips {
			result := provider.Lookup(ctx, ob, ip)
			e.persistResult(ctx, spec, ip, result, nodeHash)
			looked++
			if result.Failed() {
				failed++
			}
		}
	}

	summary := map[string]any{
		"via_node_sources": ran,
		"via_node_lookups": looked,
		"via_node_failed":  failed,
	}
	if len(skippedSources) > 0 {
		summary["via_node_skipped"] = skippedSources
	}
	if !deferUntil.IsZero() {
		return jobs.StepResult{DeferUntilNs: deferUntil.UnixNano(), Summary: summary}
	}
	if len(ran) == 0 {
		summary["skipped"] = "every via-node data source is rate limited right now"
	}
	return jobs.StepResult{Summary: summary}
}

// consumeViaNodeBudget applies the per-node daily budget and the provider-wide
// QPS valve of one via-node data source. Because the request leaves through the
// node itself, the vendor's anonymous quota belongs to that node's address, so
// the daily budget is counted per node and only the QPS valve is provider-wide
// (it is what keeps the whole inventory from reaching the vendor at once). It
// returns a wait duration when the item should be parked, or a non-empty reason
// when the source must be skipped.
func (e *stepExecutor) consumeViaNodeBudget(ctx context.Context, setting providers.Setting, nodeHash string, now time.Time) (time.Duration, string) {
	state, err := e.store.ConsumeViaNodeBudget(ctx, store.ViaNodeBudgetRequest{
		Provider:       setting.Spec.ID,
		NodeHash:       nodeHash,
		Day:            store.DayString(now),
		NodeDailyLimit: setting.DailyLimit,
		GlobalQPS:      setting.QPS,
		NowNs:          now.UnixNano(),
		CredentialID:   setting.CredentialID(),
	})
	if err == nil {
		return 0, ""
	}
	if state.Paused {
		return 0, "paused (PROVIDER_AUTH)"
	}
	wait := time.Duration(0)
	switch {
	case state.BlockedUntilNs > now.UnixNano():
		wait = time.Duration(state.BlockedUntilNs - now.UnixNano())
	case state.NextRequestAtNs > now.UnixNano():
		wait = time.Duration(state.NextRequestAtNs - now.UnixNano())
	}
	if wait <= 0 {
		// A closed gate without a next-allowed time means the daily budget is
		// exhausted; the next UTC day is the earliest retry.
		wait = now.Truncate(24 * time.Hour).Add(24 * time.Hour).Sub(now)
	}
	if wait > e.deferral {
		return 0, fmt.Sprintf("budget or quota exhausted for %s", wait.Round(time.Second))
	}
	return wait, ""
}

// ---------------------------------------------------------------------------
// step 5: unlock checks
// ---------------------------------------------------------------------------

func (e *stepExecutor) checksStep(ctx context.Context, jobID, nodeHash string) jobs.StepResult {
	if !e.enabled() {
		return skipped("unlock checks skipped: intel jobs are disabled (intel_enabled=false)")
	}
	jc, err := e.jobFor(ctx, jobID)
	if err != nil {
		return jobs.StepResult{ErrorCode: CodeJobUnavailable, Summary: map[string]any{"error": err.Error()}}
	}
	if len(jc.request.Checks) > e.maxChecksPerStep {
		return jobs.StepResult{
			ErrorCode: CodeStepLimitExceeded,
			Summary: map[string]any{
				"reason": "unlock checks exceed the per-step bound",
				"checks": len(jc.request.Checks),
				"limit":  e.maxChecksPerStep,
			},
		}
	}
	ob, ready := e.resolveOutbound(nodeHash)
	if !ready {
		return jobs.StepResult{
			ErrorCode: CodeNodeOutboundUnavailable,
			Summary:   map[string]any{"reason": "the node connection is not ready"},
		}
	}

	egress := netip.Addr{}
	if ips, ok, err := e.egressIPs(ctx, nodeHash); err == nil && ok && len(ips) > 0 {
		egress = ips[0]
	}
	results := e.checks.Run(ctx, checks.RunRequest{
		NodeKey:  nodeHash,
		Outbound: ob,
		Egress:   egress,
		RuleIDs:  jc.request.Checks,
	})
	if len(results) == 0 {
		return skipped("no enabled unlock check selected")
	}

	now := e.now()
	outcomes := make(map[string]any, len(results))
	stored := 0
	for _, result := range results {
		ttl := result.TTL
		if ttl <= 0 {
			ttl = checks.DefaultTTL
		}
		row := store.NodeCheck{
			NodeHash:     nodeHash,
			CheckID:      result.CheckID,
			CheckVersion: result.Version,
			EgressIP:     result.EgressIP,
			Outcome:      result.Outcome,
			Region:       result.Region,
			DetailJSON:   marshalStepJSON(result.Detail),
			LatencyMs:    result.LatencyMs,
			ObservedAtNs: now.UnixNano(),
			ValidUntilNs: now.Add(ttl).UnixNano(),
		}
		if err := e.store.UpsertNodeCheck(ctx, row); err != nil {
			e.logf("[intel] store check %s/%s: %v", nodeHash, result.CheckID, err)
			continue
		}
		stored++
		outcomes[result.CheckID] = result.Outcome
	}
	if stored == 0 {
		return jobs.StepResult{ErrorCode: "CHECK_STORE_FAILED", Summary: map[string]any{"reason": "no check result could be stored"}}
	}
	return jobs.StepResult{Summary: map[string]any{
		"checks":         outcomes,
		"checks_stored":  stored,
		"checks_skipped": len(results) - stored,
	}}
}

// ---------------------------------------------------------------------------
// online-ip queue worker lookup (§3.3)
// ---------------------------------------------------------------------------

// onlineLookup performs one batch lookup and persists the evidence. The queue
// worker keeps ownership of budget, retries and scheduling.
func (e *stepExecutor) onlineLookup(ctx context.Context, provider string, ips []netip.Addr) jobs.ProviderOutcome {
	outcome := jobs.ProviderOutcome{Errors: map[netip.Addr]string{}}
	setting, known := e.registry.Setting(provider)
	if !known {
		for _, ip := range ips {
			outcome.Errors[ip] = providers.CodeUnavailable
		}
		return outcome
	}
	outcome.CredentialID = setting.CredentialID()
	impl, ok := e.registry.Online(provider)
	if !ok {
		for _, ip := range ips {
			outcome.Errors[ip] = providers.CodeUnavailable
		}
		return outcome
	}

	results := impl.Lookup(ctx, ips)
	for _, ip := range ips {
		result, found := results[ip]
		if !found {
			result, found = results[ip.Unmap()]
		}
		if !found {
			result = providers.Failure(providers.CodeResponse, "the data source returned no result for this address")
		}
		e.persistResult(ctx, setting.Spec, ip, result, "")
		if result.Evidence != nil {
			outcome.Reported++
			continue
		}
		code := providers.CodeResponse
		if result.Err != nil {
			code = result.Err.Code
			if result.Err.RetryAfter > 0 && outcome.BlockedFor == 0 {
				outcome.BlockedFor = result.Err.RetryAfter
			}
			if result.Err.Pause {
				outcome.Pause = true
			}
		}
		outcome.Errors[ip] = code
	}
	return outcome
}
