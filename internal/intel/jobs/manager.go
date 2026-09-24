package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"prism/internal/intel/store"
)

// uses it to drop intel.db rows of deleted nodes (§6).
type NodeCensus interface {
	KnownNodeHashes() map[string]struct{}
}

// maxNodeDeletionsPerPass bounds one orphan-node cleanup pass (R4).
const maxNodeDeletionsPerPass = 10_000

// cleanupInterval is the daily maintenance period of §6.
const cleanupInterval = 24 * time.Hour

// vacuumInterval is the weekly PRAGMA optimize / VACUUM period of §6.
const vacuumInterval = 7 * 24 * time.Hour

// Options configures a Manager. Store, Runner and Scope are required.
type Options struct {
	Store  *store.Store
	Runner StepRunner
	Scope  ScopeResolver
	Config ConfigProvider
	Clock  Clock
	Census NodeCensus
	// ProviderSpecs and Lookup enable the per-provider queue workers (§3.3).
	ProviderSpecs ProviderSpecSource
	Lookup        OnlineLookup
	// OnEvidenceWritten is called after a batch lookup stored evidence so the
	// caller can re-assess the affected IPs (WP10 §1.8).
	OnEvidenceWritten func(ips []netip.Addr)
	// IDGen returns a new job id. Nil uses a random UUID v4 string.
	IDGen func() string
	// Logf is the log sink; nil uses the standard logger.
	Logf func(format string, args ...any)
	// Tick is the scheduler period; nil uses one second.
	Tick time.Duration
}

// RecoverStats reports what the startup recovery did (§3.5).
type RecoverStats struct {
	RequeuedJobItems  int64 `json:"requeued_job_items"`
	RequeuedQueueRows int64 `json:"requeued_queue_rows"`
}

// Stats is the observable state of the executor.
type Stats struct {
	Workers            int   `json:"workers"`
	NodeWorkers        int   `json:"node_workers"`
	MaxRunningJobs     int   `json:"max_running_jobs"`
	ActiveJobIDs       int   `json:"active_job_ids"`
	SSESubscribers     int   `json:"sse_subscribers"`
	SSEDroppedFrames   int64 `json:"sse_dropped_frames"`
	SchedulingFailures int64 `json:"scheduling_failures"`
	CleanupRuns        int64 `json:"cleanup_runs"`
	NodesPruned        int64 `json:"nodes_pruned"`
}

// Manager owns the node worker pool, the job lifecycle and the SSE hub.
type Manager struct {
	store  *store.Store
	runner StepRunner
	scope  ScopeResolver

	config ConfigProvider
	clock  Clock
	census NodeCensus
	idGen  func() string
	logf   func(string, ...any)
	tick   time.Duration
	owner  string

	hub *hub

	// provider runs one queue worker per enabled online-ip provider. It is nil
	// when the caller did not supply provider specs and a lookup implementation.
	provider *providerPool

	stopCh chan struct{}
	stop   sync.Once
	wg     sync.WaitGroup

	started      atomic.Bool
	workerCount  atomic.Int64
	workerTarget atomic.Int64
	activeJobs   atomic.Value // []string

	schedulingFailures atomic.Int64
	cleanupRuns        atomic.Int64
	nodesPruned        atomic.Int64
	lastVacuumNs       atomic.Int64
}

// New builds a Manager. It panics on a missing Store, Runner or Scope, matching
// the constructor contract of R5.
func New(opts Options) *Manager {
	if opts.Store == nil {
		panic("jobs.New: Store is required")
	}
	if opts.Runner == nil {
		panic("jobs.New: Runner is required")
	}
	if opts.Scope == nil {
		panic("jobs.New: Scope is required")
	}
	clock := opts.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	config := opts.Config
	if config == nil {
		config = func() Config { return Config{Enabled: true}.Normalize() }
	}
	idGen := opts.IDGen
	if idGen == nil {
		idGen = newJobID
	}
	logf := opts.Logf
	if logf == nil {
		logf = log.Printf
	}
	tick := opts.Tick
	if tick <= 0 {
		tick = defaultTick
	}

	m := &Manager{
		store:  opts.Store,
		runner: opts.Runner,
		scope:  opts.Scope,
		config: config,
		clock:  clock,
		census: opts.Census,
		idGen:  idGen,
		logf:   logf,
		tick:   tick,
		owner:  "intel-worker-" + newShortID(),
		hub:    newHub(time.Second),
		stopCh: make(chan struct{}),
	}
	m.activeJobs.Store([]string(nil))
	if opts.ProviderSpecs != nil && opts.Lookup != nil {
		m.provider = newProviderPool(m, opts.ProviderSpecs, opts.Lookup, opts.OnEvidenceWritten)
	}
	return m
}

// Subscribe registers an SSE client for one job.
func (m *Manager) Subscribe(jobID string) (*Subscription, error) {
	return m.hub.Subscribe(jobID)
}

// Store exposes the underlying intel.db store.
func (m *Manager) Store() *store.Store { return m.store }

// Config returns the current normalized executor configuration.
func (m *Manager) Config() Config { return m.config().Normalize() }

// Enabled reports whether automatic and manual job creation is allowed.
func (m *Manager) Enabled() bool { return m.Config().Enabled }

// Recover clears the leases left behind by a crash. Running jobs stay running so
// the worker pool continues them after the restart (§3.5).
func (m *Manager) Recover(ctx context.Context) (RecoverStats, error) {
	var stats RecoverStats
	items, err := m.store.ResetRunningJobItems(ctx)
	if err != nil {
		return stats, fmt.Errorf("reset running job items: %w", err)
	}
	stats.RequeuedJobItems = items
	rows, err := m.store.ResetStaleProviderItems(ctx)
	if err != nil {
		return stats, fmt.Errorf("reset stale provider items: %w", err)
	}
	stats.RequeuedQueueRows = rows
	return stats, nil
}

// Start recovers stale leases and launches the scheduler and worker pool.
func (m *Manager) Start() {
	if !m.started.CompareAndSwap(false, true) {
		return
	}
	ctx := context.Background()
	recovered, err := m.Recover(ctx)
	if err != nil {
		m.logf("[intel] startup recovery failed: %v", err)
	} else {
		m.logf("[intel] startup recovery: requeued %d job items, %d provider queue rows",
			recovered.RequeuedJobItems, recovered.RequeuedQueueRows)
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.scheduleLoop()
	}()
}

// Stop shuts the pool down and waits for in-flight items to finish.
func (m *Manager) Stop() {
	if !m.started.Load() {
		return
	}
	m.stop.Do(func() { close(m.stopCh) })
	m.wg.Wait()
}

// Create validates the request, expands the scope and persists the job (§3.1).
func (m *Manager) Create(ctx context.Context, req Request, createdBy string, priority int) (store.Job, error) {
	cfg := m.Config()
	if !cfg.Enabled {
		return store.Job{}, ErrDisabled
	}
	if err := req.Validate(); err != nil {
		return store.Job{}, err
	}

	nodes, err := m.scope.ResolveScope(ctx, req.Scope)
	if err != nil {
		return store.Job{}, err
	}
	nodes = normalizeHashes(nodes)
	if len(nodes) > MaxNodesPerJob {
		return store.Job{}, fmt.Errorf("%w: %d > %d", ErrTooManyNodes, len(nodes), MaxNodesPerJob)
	}

	requestJSON, err := json.Marshal(req)
	if err != nil {
		return store.Job{}, err
	}

	now := m.nowNs()
	job := store.Job{
		ID:          m.idGen(),
		Kind:        string(req.Kind),
		Status:      store.JobQueued,
		Priority:    priority,
		RequestJSON: string(requestJSON),
		CreatedBy:   createdBy,
		CreatedAtNs: now,
	}
	items := make([]store.JobItem, 0, len(nodes))
	for _, hash := range nodes {
		items = append(items, store.JobItem{
			NodeHash:    hash,
			Status:      store.ItemQueued,
			NextRunAtNs: now,
			UpdatedAtNs: now,
		})
	}
	if err := m.store.CreateJob(ctx, job, items); err != nil {
		return store.Job{}, err
	}
	created, err := m.store.GetJob(ctx, job.ID)
	if err != nil {
		return store.Job{}, err
	}
	if len(items) == 0 {
		// A scope that expands to nothing settles immediately.
		created, err = m.store.RefreshJob(ctx, job.ID, now)
		if err != nil {
			return store.Job{}, err
		}
	}
	m.publish(ctx, job.ID, true)
	return created, nil
}

// Cancel marks a job canceled and settles its counters (§3.5).
func (m *Manager) Cancel(ctx context.Context, jobID string) error {
	if err := m.store.CancelJob(ctx, jobID); err != nil {
		return err
	}
	m.publish(ctx, jobID, true)
	return nil
}

// RetryFailed requeues the failed items of a job (§3.5).
func (m *Manager) RetryFailed(ctx context.Context, jobID string) (int64, error) {
	retried, err := m.store.RetryFailedJobItems(ctx, jobID, m.nowNs())
	if err != nil {
		return 0, err
	}
	m.publish(ctx, jobID, true)
	return retried, nil
}

// Progress returns the counter view of one job including pending online
// lookups.
func (m *Manager) Progress(ctx context.Context, jobID string) (store.JobProgress, error) {
	return m.store.JobProgressView(ctx, jobID)
}

// Stats reports the executor state for GET /api/v1/intel/status.
func (m *Manager) Stats() Stats {
	cfg := m.Config()
	return Stats{
		Workers:            int(m.workerCount.Load()),
		NodeWorkers:        cfg.NodeWorkers,
		MaxRunningJobs:     cfg.MaxRunningJobs,
		ActiveJobIDs:       len(m.currentActiveJobIDs()),
		SSESubscribers:     m.hub.Subscribers(),
		SSEDroppedFrames:   m.hub.Dropped(),
		SchedulingFailures: m.schedulingFailures.Load(),
		CleanupRuns:        m.cleanupRuns.Load(),
		NodesPruned:        m.nodesPruned.Load(),
	}
}

// Cleanup runs the retention pass of §6 immediately.
func (m *Manager) Cleanup(ctx context.Context) (store.CleanupResult, error) {
	result, err := m.store.Cleanup(ctx, m.clock.Now(), store.DefaultCleanupPolicy())
	if err != nil {
		return result, err
	}
	if m.census != nil {
		pruned, err := m.pruneUnknownNodes(ctx)
		if err != nil {
			return result, err
		}
		m.nodesPruned.Add(pruned)
	}
	m.cleanupRuns.Add(1)
	return result, nil
}

func (m *Manager) scheduleLoop() {
	ticker := time.NewTicker(m.tick)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
		}

		cfg := m.Config()
		m.workerTarget.Store(int64(cfg.NodeWorkers))
		if cfg.Enabled {
			m.ensureWorkers(cfg.NodeWorkers)
			m.refreshActiveJobs(cfg)
		} else {
			m.activeJobs.Store([]string(nil))
		}
		if m.provider != nil && cfg.Enabled {
			m.provider.sync()
		}
		m.maybeCleanup(cfg)
	}
}

// refreshActiveJobs picks the jobs allowed to run. Running jobs keep their slot
// so newly created high-priority jobs queue behind them instead of preempting
// in-flight work (§3.4).
func (m *Manager) refreshActiveJobs(cfg Config) {
	ctx := context.Background()
	active, err := m.store.ListActiveJobs(ctx, maxActiveJobsFetched)
	if err != nil {
		m.schedulingFailures.Add(1)
		m.logf("[intel] list active jobs: %v", err)
		return
	}
	ordered := make([]store.Job, 0, len(active))
	for _, job := range active {
		if job.Status == store.JobRunning {
			ordered = append(ordered, job)
		}
	}
	for _, job := range active {
		if job.Status == store.JobQueued {
			ordered = append(ordered, job)
		}
	}
	limit := cfg.MaxRunningJobs
	if limit > len(ordered) {
		limit = len(ordered)
	}
	ids := make([]string, 0, limit)
	for _, job := range ordered[:limit] {
		ids = append(ids, job.ID)
	}
	m.activeJobs.Store(ids)
}

func (m *Manager) currentActiveJobIDs() []string {
	ids, _ := m.activeJobs.Load().([]string)
	return ids
}

func (m *Manager) ensureWorkers(target int) {
	if target > MaxNodeWorkers {
		target = MaxNodeWorkers
	}
	for {
		current := int(m.workerCount.Load())
		if current >= target {
			return
		}
		if !m.workerCount.CompareAndSwap(int64(current), int64(current+1)) {
			continue
		}
		m.wg.Add(1)
		go func(idx int) {
			defer m.wg.Done()
			m.workerLoop(idx)
		}(current)
	}
}

func (m *Manager) workerLoop(idx int) {
	ctx := context.Background()
	for {
		select {
		case <-m.stopCh:
			return
		default:
		}
		if idx >= int(m.workerTarget.Load()) {
			sleepOrStop(m.stopCh, idlePollInterval)
			continue
		}
		item, ok, err := m.claimOne(ctx)
		if err != nil {
			m.schedulingFailures.Add(1)
			m.logf("[intel] claim job item: %v", err)
			sleepOrStop(m.stopCh, idlePollInterval)
			continue
		}
		if !ok {
			sleepOrStop(m.stopCh, idlePollInterval)
			continue
		}
		m.runItem(ctx, item)
	}
}

func (m *Manager) claimOne(ctx context.Context) (store.JobItem, bool, error) {
	ids := m.currentActiveJobIDs()
	if len(ids) == 0 {
		return store.JobItem{}, false, nil
	}
	now := m.nowNs()
	claimed, err := m.store.ClaimJobItems(ctx, store.ClaimOptions{
		NowNs:        now,
		LeaseUntilNs: now + int64(itemLease),
		Limit:        1,
		Owner:        m.owner,
		JobIDs:       ids,
	})
	if err != nil {
		return store.JobItem{}, false, err
	}
	if len(claimed) == 0 {
		return store.JobItem{}, false, nil
	}
	return claimed[0], true, nil
}

// runItem executes the remaining pipeline steps of one node item.
func (m *Manager) runItem(parent context.Context, item store.JobItem) {
	job, err := m.store.GetJob(parent, item.JobID)
	if err != nil {
		return
	}
	if job.Status == store.JobCanceled {
		m.finishItem(parent, item, store.ItemCanceled, "", nil)
		return
	}

	ctx, cancel := context.WithTimeout(parent, itemTimeout)
	defer cancel()

	kind := Kind(job.Kind)
	lastCompleted := item.StepIndex
	summary := map[string]any{}
	for _, step := range PendingSteps(kind, lastCompleted) {
		// A canceled job stops the item before the next step starts (§3.5).
		if m.jobCanceled(ctx, item.JobID) {
			m.finishItem(parent, item, store.ItemCanceled, "", summary)
			return
		}
		if ctx.Err() != nil {
			m.deferItem(parent, item, lastCompleted, m.nowNs()+int64(backoff(item.Attempts-1)), "ITEM_TIMEOUT", summary)
			return
		}

		result := m.runner.RunStep(ctx, kind, step, item.JobID, item.NodeHash)
		for key, value := range result.Summary {
			summary[key] = value
		}

		if result.ErrorCode != "" {
			if item.Attempts >= maxItemAttempts {
				m.finishItem(parent, item, store.ItemFailed, result.ErrorCode, summary)
				return
			}
			m.deferItem(parent, item, lastCompleted, m.nowNs()+int64(backoff(item.Attempts-1)), result.ErrorCode, summary)
			return
		}
		if result.DeferUntilNs > 0 {
			m.deferItem(parent, item, lastCompleted, result.DeferUntilNs, "", summary)
			return
		}

		lastCompleted = int(step)
		if result.Skip {
			m.finishItem(parent, item, store.ItemSkipped, "", summary)
			return
		}
		if err := m.store.SaveJobItemStep(ctx, item.JobID, item.NodeHash, lastCompleted,
			summaryJSON(summary), m.nowNs()+int64(itemLease), m.nowNs()); err != nil {
			m.logf("[intel] save job item step: %v", err)
			m.deferItem(parent, item, lastCompleted, m.nowNs()+int64(backoffBase), "", summary)
			return
		}
	}
	m.finishItem(parent, item, store.ItemDone, "", summary)
}

func (m *Manager) jobCanceled(ctx context.Context, jobID string) bool {
	job, err := m.store.GetJob(ctx, jobID)
	if err != nil {
		return false
	}
	return job.Status == store.JobCanceled
}

func (m *Manager) finishItem(ctx context.Context, item store.JobItem, status, errorCode string, summary map[string]any) {
	if err := m.store.FinishJobItem(ctx, item.JobID, item.NodeHash, status, errorCode,
		summaryJSON(summary), m.nowNs()); err != nil {
		m.logf("[intel] finish job item: %v", err)
	}
	m.publish(ctx, item.JobID, false)
}

func (m *Manager) deferItem(ctx context.Context, item store.JobItem, stepIndex int, nextRunAtNs int64, errorCode string, summary map[string]any) {
	if err := m.store.DeferJobItem(ctx, item.JobID, item.NodeHash, stepIndex, nextRunAtNs,
		summaryJSON(summary), m.nowNs()); err != nil {
		m.logf("[intel] defer job item: %v", err)
	}
	m.publish(ctx, item.JobID, false)
}

// publish recomputes the job counters and pushes one SSE frame.
func (m *Manager) publish(ctx context.Context, jobID string, force bool) {
	job, err := m.store.RefreshJob(ctx, jobID, m.nowNs())
	if err != nil {
		return
	}
	view, err := m.store.JobProgressView(ctx, jobID)
	if err != nil {
		return
	}
	terminal := job.Terminal()
	m.hub.push(jobID, ProgressFromRow(view), m.nowNs(), force || terminal)
	if terminal {
		m.hub.Forget(jobID)
	}
}

func (m *Manager) maybeCleanup(cfg Config) {
	if !cfg.Enabled {
		return
	}
	last := m.lastVacuumNs.Load()
	now := m.clock.Now()
	if last != 0 && now.Sub(timeFromNs(last)) < cleanupInterval {
		return
	}
	m.lastVacuumNs.Store(m.nowNs())
	ctx := context.Background()
	if _, err := m.Cleanup(ctx); err != nil {
		m.logf("[intel] cleanup: %v", err)
	}
	if last == 0 || now.Sub(timeFromNs(last)) >= vacuumInterval {
		if err := m.store.Optimize(); err != nil {
			m.logf("[intel] optimize: %v", err)
		}
	}
}

// pruneUnknownNodes deletes intel.db rows of nodes that no longer exist. One
// pass is capped at maxNodeDeletionsPerPass (R4).
func (m *Manager) pruneUnknownNodes(ctx context.Context) (int64, error) {
	known := m.census.KnownNodeHashes()
	stored, err := m.store.ListNodeHashes(ctx)
	if err != nil {
		return 0, err
	}
	var unknown []string
	for _, hash := range stored {
		if _, ok := known[hash]; ok {
			continue
		}
		unknown = append(unknown, hash)
		if len(unknown) >= maxNodeDeletionsPerPass {
			break
		}
	}
	if len(unknown) == 0 {
		return 0, nil
	}
	return m.store.DeleteNodeData(ctx, unknown)
}

func (m *Manager) nowNs() int64 { return m.clock.Now().UTC().UnixNano() }
func summaryJSON(summary map[string]any) string {
	if len(summary) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func normalizeHashes(nodes []string) []string {
	if len(nodes) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(nodes))
	out := make([]string, 0, len(nodes))
	for _, hash := range nodes {
		trimmed := strings.TrimSpace(hash)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func timeFromNs(ns int64) time.Time { return time.Unix(0, ns).UTC() }

func newJobID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(buf[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexed[0:8], hexed[8:12], hexed[12:16], hexed[16:20], hexed[20:32])
}

func newShortID() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(buf[:])
}
