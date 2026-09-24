package intel

import (
	"context"
	"fmt"
	"log"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"prism/internal/intel/checks"
	"prism/internal/intel/egress"
	"prism/internal/intel/jobs"
	"prism/internal/intel/providers"
	"prism/internal/intel/store"
	"prism/internal/node"
)

// parseNodeHash decodes the canonical 32-character hex node hash used as the
// TEXT key of every intel.db table.
func parseNodeHash(nodeHash string) (node.Hash, error) {
	hash, err := node.ParseHex(strings.TrimSpace(nodeHash))
	if err != nil {
		return node.Zero, err
	}
	return hash, nil
}

// egressObservationQueue bounds the non-blocking egress observer queue of §4.
// A full queue drops the sample and increments a counter (R4).
const egressObservationQueue = 4096

// egressObservationBatch is how many observations are written per transaction.
const egressObservationBatch = 256

// egressObservationFlush bounds how long one observation may wait in the queue.
const egressObservationFlush = 250 * time.Millisecond

// StepHandlers is the seam WP09 (offline/online providers, via-node sources,
// unlock checks) and WP10 (assessment) fill in. Every field is optional: a nil
// handler makes the pipeline step a no-op skip so WP08 stays runnable on its own.
type StepHandlers struct {
	// Egress overrides the built-in egress probe of step 1.
	Egress func(ctx context.Context, jobID, nodeHash string) jobs.StepResult
	// Offline runs the offline data sources of step 2.
	Offline func(ctx context.Context, jobID, nodeHash string) jobs.StepResult
	// EnqueueOnline enqueues the online provider queue tasks of step 3.
	EnqueueOnline func(ctx context.Context, jobID, nodeHash string) jobs.StepResult
	// ViaNode runs the via-node data sources of step 4.
	ViaNode func(ctx context.Context, jobID, nodeHash string) jobs.StepResult
	// Checks runs the unlock checks of step 5.
	Checks func(ctx context.Context, jobID, nodeHash string) jobs.StepResult
	// Assess recomputes the purity assessment of step 6.
	Assess func(ctx context.Context, jobID, nodeHash string) jobs.StepResult
}

// handler returns the handler of one pipeline step.
func (h StepHandlers) handler(step jobs.Step) func(ctx context.Context, jobID, nodeHash string) jobs.StepResult {
	switch step {
	case jobs.StepEgress:
		return h.Egress
	case jobs.StepOffline:
		return h.Offline
	case jobs.StepEnqueueOnline:
		return h.EnqueueOnline
	case jobs.StepViaNode:
		return h.ViaNode
	case jobs.StepChecks:
		return h.Checks
	case jobs.StepAssess:
		return h.Assess
	default:
		return nil
	}
}

// pipelineRunner implements jobs.StepRunner for the WP08 step pipeline.
type pipelineRunner struct {
	handlers StepHandlers
	prober   *egress.Probe
	onChange func(ips []netip.Addr)
}

// RunStep executes one pipeline step for one node.
func (r pipelineRunner) RunStep(ctx context.Context, kind jobs.Kind, step jobs.Step, jobID, nodeHash string) jobs.StepResult {
	if handler := r.handlers.handler(step); handler != nil {
		return handler(ctx, jobID, nodeHash)
	}
	if step != jobs.StepEgress {
		// No WP09/WP10 handler registered yet: the step is a no-op, never a
		// failure, so the item still reaches a terminal state.
		return jobs.StepResult{Summary: map[string]any{"skipped": "no handler for step " + fmt.Sprint(int(step))}}
	}
	if r.prober == nil {
		return jobs.StepResult{Summary: map[string]any{"skipped": "egress probe unavailable"}}
	}

	hash, err := parseNodeHash(nodeHash)
	if err != nil {
		return jobs.StepResult{ErrorCode: "INVALID_NODE_HASH", Summary: map[string]any{"error": err.Error()}}
	}
	result, err := r.prober.Run(ctx, hash)
	if err != nil {
		return jobs.StepResult{ErrorCode: "EGRESS_PROBE_FAILED", Summary: map[string]any{"error": err.Error()}}
	}
	summary := map[string]any{
		"ipv4":       result.Row.IPv4,
		"ipv6":       result.Row.IPv6,
		"colo":       result.Row.Colo,
		"loc":        result.Row.Loc,
		"changed":    result.Changed,
		"v6_checked": result.V6Checked,
	}
	if result.Changed && r.onChange != nil {
		ips := make([]netip.Addr, 0, 2)
		if addr, err := netip.ParseAddr(result.Row.IPv4); err == nil && addr.IsValid() {
			ips = append(ips, addr)
		}
		if addr, err := netip.ParseAddr(result.Row.IPv6); err == nil && addr.IsValid() {
			ips = append(ips, addr)
		}
		if len(ips) > 0 {
			r.onChange(ips)
		}
	}
	return jobs.StepResult{Summary: summary}
}

// Options configures the intel service.
type Options struct {
	Store    *store.Store
	Clock    jobs.Clock
	Config   jobs.ConfigProvider
	Scope    jobs.ScopeResolver
	Handlers StepHandlers
	// Prober overrides the built-in egress probe of step 1.
	Prober *egress.Probe
	// ProviderSpecs and Lookup enable the provider queue workers.
	ProviderSpecs jobs.ProviderSpecSource
	Lookup        jobs.OnlineLookup
	// ProviderSettings is the WP09 §4 settings surface of the data sources. It
	// owns the live provider registry, so a settings change applies without a
	// restart.
	ProviderSettings *providers.SettingsService
	// CheckEngine is the WP09 §5 rule engine, exposed to the checks settings
	// surface (GET/PATCH /api/v1/intel/checks).
	CheckEngine *checks.Engine
	// OnEvidenceWritten runs after a provider batch stored evidence. The
	// assessment of the affected IPs is refreshed before this callback runs.
	OnEvidenceWritten func(ips []netip.Addr)
	// OnEgressChange runs after an egress address changed (§4: enqueue the new IP
	// on every online provider and re-assess within existing evidence).
	OnEgressChange func(ips []netip.Addr)
	// EnabledSources reports which data sources are enabled. It feeds the §1.2
	// coverage denominator.
	EnabledSources func() map[string]bool
	// OnAssessmentChanged runs after assessments were recomputed (WP10 §2 wires
	// it to GlobalNodePool.NotifyEgressIPDirty).
	OnAssessmentChanged func(ips []netip.Addr)
	// AssessmentSweepInterval enables the §1.8 expiry sweep. Zero disables it.
	AssessmentSweepInterval time.Duration
	Census                  jobs.NodeCensus
	// Tick is the scheduler period of the job executor. Zero uses one second;
	// tests use a short tick.
	Tick time.Duration
	Logf func(format string, args ...any)
}

// Service is the intel facade used by the control plane and the API layer.
type Service struct {
	store    *store.Store
	manager  *jobs.Manager
	snapshot *Snapshot
	assessor *Assessor
	prober   *egress.Probe
	observer *egressObserver
	// providerSettings is the WP09 §4 settings surface of the data sources; nil
	// when the caller did not wire it.
	providerSettings *providers.SettingsService
	// checkEngine is the WP09 §5 rule engine behind the checks settings
	// surface; nil when the caller did not wire it.
	checkEngine *checks.Engine
	// auto is the §3.6 subscription auto-enqueue coalescer; nil when the caller
	// did not enable it.
	auto     *autoEnqueueQueue
	handlers StepHandlers
	clock    jobs.Clock

	// onEvidenceWrittenHook is the caller's OnEvidenceWritten, run after the
	// assessments of the new evidence were refreshed.
	onEvidenceWrittenHook func(ips []netip.Addr)

	sweepInterval time.Duration
	sweepStop     chan struct{}
	sweepWG       sync.WaitGroup
	sweepOnce     sync.Once
	lastSweepNs   atomic.Int64
}

// NewService assembles the store projection and the batch executor.
func NewService(opts Options) (*Service, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("intel: store is required")
	}
	clock := opts.Clock
	if clock == nil {
		clock = jobs.SystemClock{}
	}
	logf := opts.Logf
	if logf == nil {
		logf = log.Printf
	}
	prober := opts.Prober
	if prober == nil && opts.Store != nil {
		prober = &egress.Probe{Store: opts.Store, Now: clock.Now}
	}
	if prober != nil && prober.Store == nil {
		prober.Store = opts.Store
	}
	if prober != nil && prober.Now == nil {
		prober.Now = clock.Now
	}

	svc := &Service{
		store:            opts.Store,
		snapshot:         NewSnapshot(),
		prober:           prober,
		handlers:         opts.Handlers,
		providerSettings: opts.ProviderSettings,
		checkEngine:      opts.CheckEngine,
		clock:            clock,
		sweepInterval:    opts.AssessmentSweepInterval,
		sweepStop:        make(chan struct{}),
	}
	svc.assessor = NewAssessor(AssessorOptions{
		Store:    opts.Store,
		Snapshot: svc.snapshot,
		Enabled:  opts.EnabledSources,
		OnChange: opts.OnAssessmentChanged,
		Now:      clock.Now,
		Logf:     logf,
	})
	svc.onEvidenceWrittenHook = opts.OnEvidenceWritten
	// WP10 §1.8: the assessment pipeline step is on by default.
	if svc.handlers.Assess == nil {
		svc.handlers.Assess = svc.assessStep
	}
	// WP09 §5: the unlock-check handler writes node_checks; the in-memory
	// projection must follow immediately so the routing and list surfaces see
	// the new outcome without waiting for a restart.
	if svc.handlers.Checks != nil {
		svc.handlers.Checks = svc.withCheckProjection(svc.handlers.Checks)
	}
	svc.manager = jobs.New(jobs.Options{
		Store:             opts.Store,
		Runner:            pipelineRunner{handlers: svc.handlers, prober: prober, onChange: opts.OnEgressChange},
		Scope:             opts.Scope,
		Config:            opts.Config,
		Clock:             clock,
		Census:            opts.Census,
		ProviderSpecs:     opts.ProviderSpecs,
		Lookup:            opts.Lookup,
		OnEvidenceWritten: svc.onEvidenceWritten,
		Tick:              opts.Tick,
		Logf:              logf,
	})
	svc.observer = newEgressObserver(opts.Store, clock, opts.OnEgressChange, logf)
	return svc, nil
}

// assessStep is the default jobs.StepAssess handler: it recomputes the purity
//
// It only writes an assessment once the evidence it needs actually exists. A
// job item whose steps ran out of order (a resumed item, an interrupted run, a
// node whose provider steps were all skipped) must produce an explicit,
// explainable result instead of a bogus verdict, so a missing egress row or a
// missing evidence row is reported as a named reason and nothing is persisted.
func (s *Service) assessStep(ctx context.Context, jobID, nodeHash string) jobs.StepResult {
	if s == nil || s.assessor == nil || s.store == nil {
		return jobs.StepResult{Summary: map[string]any{"skipped": "assessment is unavailable"}}
	}
	egress, ok, err := s.store.GetNodeEgress(ctx, nodeHash)
	if err != nil {
		return jobs.StepResult{ErrorCode: "ASSESS_FAILED", Summary: map[string]any{"error": err.Error()}}
	}
	if !ok {
		return jobs.StepResult{Summary: map[string]any{
			"step":    "assess",
			"skipped": "assessment skipped: no egress observation for this node yet",
		}}
	}
	addresses := egressAddresses(egress)
	if len(addresses) == 0 {
		return jobs.StepResult{Summary: map[string]any{
			"step":    "assess",
			"skipped": "assessment skipped: the node has no usable egress address",
		}}
	}

	assessable := make([]netip.Addr, 0, len(addresses))
	missing := make([]string, 0, len(addresses))
	for _, ip := range addresses {
		usable, err := s.hasUsableEvidence(ctx, ip)
		if err != nil {
			return jobs.StepResult{ErrorCode: "ASSESS_FAILED", Summary: map[string]any{"error": err.Error()}}
		}
		if !usable {
			missing = append(missing, ip.String())
			continue
		}
		assessable = append(assessable, ip)
	}

	summary := map[string]any{"step": "assess"}
	if len(assessable) > 0 {
		summary["assessed"] = s.assessor.AssessMany(ctx, assessable)
	}
	if len(missing) > 0 {
		summary["without_evidence"] = missing
		summary["reason"] = "no stored evidence for these addresses yet"
	}
	if len(assessable) == 0 {
		summary["skipped"] = "assessment skipped: no stored evidence for " + strings.Join(missing, ", ")
	}
	return jobs.StepResult{Summary: summary}
}

// egressAddresses renders the stored egress row as the two addresses the
// assessment runs on.
func egressAddresses(row store.NodeEgress) []netip.Addr {
	out := make([]netip.Addr, 0, 2)
	for _, raw := range []string{row.IPv4, row.IPv6} {
		addr, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil || !addr.IsValid() {
			continue
		}
		out = append(out, addr.Unmap())
	}
	return out
}

// hasUsableEvidence reports whether intel.db holds at least one successful
// evidence row for an address.
func (s *Service) hasUsableEvidence(ctx context.Context, ip netip.Addr) (bool, error) {
	rows, err := s.store.ListEvidenceByIP(ctx, ip.Unmap().String())
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.Status == store.StatusOk {
			return true, nil
		}
	}
	return false, nil
}

// withCheckProjection wraps the unlock-check step so the in-memory projection
// reflects the freshly stored node_checks rows (§5). The refresh is best effort:
// a projection miss must never fail a check step that already succeeded.
func (s *Service) withCheckProjection(inner func(ctx context.Context, jobID, nodeHash string) jobs.StepResult) func(ctx context.Context, jobID, nodeHash string) jobs.StepResult {
	return func(ctx context.Context, jobID, nodeHash string) jobs.StepResult {
		result := inner(ctx, jobID, nodeHash)
		s.syncNodeChecks(ctx, nodeHash)
		return result
	}
}

// syncNodeChecks reloads one node's stored checks into the projection.
func (s *Service) syncNodeChecks(ctx context.Context, nodeHash string) {
	if s == nil || s.store == nil || s.snapshot == nil {
		return
	}
	rows, err := s.store.ListNodeChecks(ctx, nodeHash)
	if err != nil {
		return
	}
	for _, row := range rows {
		s.snapshot.SetCheck(row.NodeHash, row.CheckID, OutcomeLite{
			Outcome:    OutcomeCode(row.Outcome),
			ValidUntil: row.ValidUntilNs,
			ObservedAt: row.ObservedAtNs,
		})
	}
}

// onEvidenceWritten refreshes the assessment of freshly written evidence and
// then runs the caller's callback (WP10 §1.8).
func (s *Service) onEvidenceWritten(ips []netip.Addr) {
	if s.assessor != nil && len(ips) > 0 {
		for _, ip := range ips {
			if _, err := s.assessor.AssessIP(context.Background(), ip); err != nil {
				s.assessor.logf("[intel] assess %s: %v", ip, err)
			}
		}
	}
	if s.onEvidenceWrittenHook != nil {
		s.onEvidenceWrittenHook(ips)
	}
}

// Store returns the intel.db handle.
func (s *Service) Store() *store.Store { return s.store }

// Assessor returns the purity assessor (WP10 §1).
func (s *Service) Assessor() *Assessor { return s.assessor }

// Assess recomputes and persists the assessment of one IP.
func (s *Service) Assess(ctx context.Context, ip netip.Addr) (store.Assessment, error) {
	if s == nil || s.assessor == nil {
		return store.Assessment{}, nil
	}
	return s.assessor.AssessIP(ctx, ip)
}

// AssessNode recomputes the assessments of one node's observed egress IPs.
func (s *Service) AssessNode(ctx context.Context, nodeHash string) (int, error) {
	if s == nil || s.assessor == nil {
		return 0, nil
	}
	return s.assessor.AssessNode(ctx, nodeHash)
}

// SweepAssessments recalculates the assessments that expired since the previous
// sweep and returns how many rows were refreshed (WP10 §1.8).
func (s *Service) SweepAssessments(ctx context.Context) (int, error) {
	if s == nil || s.assessor == nil {
		return 0, nil
	}
	changed, err := s.assessor.SweepExpired(ctx, s.lastSweepNs.Load())
	s.lastSweepNs.Store(s.clock.Now().UTC().UnixNano())
	return changed, err
}

// RecomputeForeignProfiles recalculates assessments written by another profile
// (WP10 §1.8 startup pass).
func (s *Service) RecomputeForeignProfiles(ctx context.Context) (int, error) {
	if s == nil || s.assessor == nil {
		return 0, nil
	}
	return s.assessor.RecomputeForeignProfiles(ctx, 0)
}

// Snapshot returns the in-memory projection.
func (s *Service) Snapshot() *Snapshot { return s.snapshot }

// Manager returns the batch job executor.
func (s *Service) Manager() *jobs.Manager { return s.manager }

// Prober returns the egress probe.
func (s *Service) Prober() *egress.Probe { return s.prober }

// ProviderSettings returns the WP09 §4 data source settings surface (nil when
// the caller did not wire it).
func (s *Service) ProviderSettings() *providers.SettingsService {
	if s == nil {
		return nil
	}
	return s.providerSettings
}

// CheckEngine returns the WP09 §5 unlock-check rule engine (nil when the caller
// did not wire it).
func (s *Service) CheckEngine() *checks.Engine {
	if s == nil {
		return nil
	}
	return s.checkEngine
}

// Start loads the projection and launches the executor.
func (s *Service) Start() error {
	if err := s.ReloadSnapshot(context.Background()); err != nil {
		return err
	}
	s.observer.Start()
	s.manager.Start()
	// §3.6: the subscription auto-enqueue coalescer is part of the executor.
	if s.auto != nil {
		s.auto.start()
	}
	s.startSweeper()
	return nil
}

// startSweeper launches the §1.8 expiry sweep. It is a no-op unless an interval
// was configured.
func (s *Service) startSweeper() {
	if s == nil || s.sweepInterval <= 0 {
		return
	}
	if s.lastSweepNs.Load() == 0 {
		s.lastSweepNs.Store(s.clock.Now().UTC().UnixNano())
	}
	s.sweepWG.Add(1)
	go func() {
		defer s.sweepWG.Done()
		ticker := time.NewTicker(s.sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.sweepStop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				if _, err := s.SweepAssessments(ctx); err != nil {
					s.assessor.logf("[intel] assessment sweep: %v", err)
				}
				cancel()
			}
		}
	}()
}

// Stop shuts the executor down, stops the sweep and drains the egress observer.
func (s *Service) Stop() {
	s.sweepOnce.Do(func() {
		if s.sweepStop != nil {
			close(s.sweepStop)
		}
	})
	s.sweepWG.Wait()
	// The coalescer flushes its buffered nodes before the pool stops: a flushed
	// job stays queued in intel.db and resumes on the next start.
	if s.auto != nil {
		s.auto.stop()
	}
	s.sweepWG.Wait()
	s.manager.Stop()
	s.observer.Stop()
}

// ReloadSnapshot rebuilds the projection from intel.db (§5: it is only a cache
// and must be fully reconstructible after a restart).
func (s *Service) ReloadSnapshot(ctx context.Context) error {
	return s.snapshot.LoadFrom(ctx, s.store)
}

// RecordAssessment persists an assessment and updates the projection.
func (s *Service) RecordAssessment(ctx context.Context, row store.Assessment) error {
	if err := s.store.UpsertAssessment(ctx, row); err != nil {
		return err
	}
	s.snapshot.SetAssessmentFromRow(row)
	return nil
}

// RecordCheck persists a node check and updates the projection.
func (s *Service) RecordCheck(ctx context.Context, row store.NodeCheck) error {
	if err := s.store.UpsertNodeCheck(ctx, row); err != nil {
		return err
	}
	s.snapshot.SetCheck(row.NodeHash, row.CheckID, OutcomeLite{
		Outcome:    OutcomeCode(row.Outcome),
		ValidUntil: row.ValidUntilNs,
		ObservedAt: row.ObservedAtNs,
	})
	return nil
}

// ObserveEgress records a successful periodic egress probe. The callback path is
// non-blocking: samples land in a bounded queue and a full queue drops the sample
// and increments the discarded counter (§4, R4).
func (s *Service) ObserveEgress(hash string, ip netip.Addr, loc string) {
	if s == nil || s.observer == nil {
		return
	}
	s.observer.Offer(hash, ip, loc)
}

// DiscardedEgressObservations reports how many samples were dropped because the
// observer queue was full.
func (s *Service) DiscardedEgressObservations() int64 {
	if s == nil || s.observer == nil {
		return 0
	}
	return s.observer.Discarded()
}

// CreateJob validates and persists a job through the executor.
func (s *Service) CreateJob(ctx context.Context, req jobs.Request, createdBy string, priority int) (store.Job, error) {
	return s.manager.Create(ctx, req, createdBy, priority)
}

// egressObserver serialises the non-blocking egress callback into batched writes.
type egressObserver struct {
	store    *store.Store
	clock    jobs.Clock
	onChange func(ips []netip.Addr)
	logf     func(string, ...any)

	queue   chan store.EgressObservation
	dropped atomic.Int64
	written atomic.Int64

	stopCh chan struct{}
	wg     sync.WaitGroup
	once   sync.Once
}

func newEgressObserver(st *store.Store, clock jobs.Clock, onChange func([]netip.Addr), logf func(string, ...any)) *egressObserver {
	return &egressObserver{
		store:    st,
		clock:    clock,
		onChange: onChange,
		logf:     logf,
		queue:    make(chan store.EgressObservation, egressObservationQueue),
		stopCh:   make(chan struct{}),
	}
}

// Offer enqueues one observation without blocking.
func (o *egressObserver) Offer(hash string, ip netip.Addr, loc string) {
	if o == nil || !ip.IsValid() || strings.TrimSpace(hash) == "" {
		return
	}
	obs := store.EgressObservation{
		NodeHash: hash,
		Colo:     "",
		Loc:      loc,
		NowNs:    o.clock.Now().UTC().UnixNano(),
	}
	if ip.Is6() {
		obs.IPv6 = ip
		obs.V6Checked = true
	} else {
		obs.IPv4 = ip
	}
	select {
	case o.queue <- obs:
	default:
		// Bounded over-limit behaviour: drop and count.
		o.dropped.Add(1)
	}
}

// Start launches the writer goroutine.
func (o *egressObserver) Start() {
	if o == nil {
		return
	}
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.writeLoop()
	}()
}

// Stop drains the queue and waits for the writer.
func (o *egressObserver) Stop() {
	if o == nil {
		return
	}
	o.once.Do(func() { close(o.stopCh) })
	o.wg.Wait()
}

// Discarded reports the dropped sample count.
func (o *egressObserver) Discarded() int64 {
	if o == nil {
		return 0
	}
	return o.dropped.Load()
}

// Written reports how many observations were persisted.
func (o *egressObserver) Written() int64 {
	if o == nil {
		return 0
	}
	return o.written.Load()
}

func (o *egressObserver) writeLoop() {
	ticker := time.NewTicker(egressObservationFlush)
	defer ticker.Stop()

	batch := make([]store.EgressObservation, 0, egressObservationBatch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, obs := range batch {
			_, changed, err := o.store.RecordEgress(ctx, obs)
			if err != nil {
				o.logf("[intel] record observed egress: %v", err)
				continue
			}
			o.written.Add(1)
			if changed && o.onChange != nil {
				ips := make([]netip.Addr, 0, 2)
				if obs.IPv4.IsValid() {
					ips = append(ips, obs.IPv4)
				}
				if obs.IPv6.IsValid() {
					ips = append(ips, obs.IPv6)
				}
				if len(ips) > 0 {
					o.onChange(ips)
				}
			}
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-o.stopCh:
			drain(o.queue, &batch)
			flush()
			return
		case obs := <-o.queue:
			batch = append(batch, obs)
			if len(batch) >= egressObservationBatch {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func drain(queue chan store.EgressObservation, batch *[]store.EgressObservation) {
	for {
		select {
		case obs := <-queue:
			*batch = append(*batch, obs)
		default:
			return
		}
	}
}
