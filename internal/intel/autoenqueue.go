package intel

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"prism/internal/intel/jobs"
)

// SubscriptionAutoIntel is the per-subscription input of the §3.6 automatic
// job: the persisted auto_intel flag and the node hashes that were imported or
// refreshed.
type SubscriptionAutoIntel struct {
	SubscriptionID string
	AutoIntel      bool
	NodeHashes     []string
}

// AutoEnqueueOptions configures the subscription auto-enqueue path (§3.6). It
// honours both switches the plan requires: the subscription's auto_intel flag
// and the system-level intel_enabled setting (through the job manager).
type AutoEnqueueOptions struct {
	// Lookup resolves the persisted auto_intel flag of one subscription.
	Lookup func(subscriptionID string) (autoIntel bool, found bool)
	// AutoChecks reports the runtime intel_auto_checks switch: false creates
	// kind=intel jobs, true creates kind=full jobs.
	AutoChecks func() bool
	// Delay merges the node additions of one refresh into a single job. Zero
	// uses DefaultAutoEnqueueDelay.
	Delay time.Duration
	// MaxNodesPerBatch bounds one merged job. A larger refresh is split into
	// several jobs; nothing is ever truncated or dropped silently.
	MaxNodesPerBatch int
	// MaxSubscriptions bounds the pending map of the coalescer.
	MaxSubscriptions int
	// Logf receives diagnostics.
	Logf func(format string, args ...any)
}

// Auto-enqueue bounds (R4).
const (
	// DefaultAutoEnqueueDelay merges one refresh wave into a single job.
	DefaultAutoEnqueueDelay = 2 * time.Second
	// DefaultAutoEnqueueBatch is the per-job node cap of the coalescer.
	DefaultAutoEnqueueBatch = jobs.MaxNodesPerJob
	// DefaultAutoEnqueueSubscriptions bounds the pending subscription map.
	DefaultAutoEnqueueSubscriptions = 512
)

// ErrAutoEnqueueDisabled reports that the system-level intel_enabled switch is
// off, so no automatic job was created.
var ErrAutoEnqueueDisabled = jobs.ErrDisabled

// autoEnqueueQueue coalesces the node additions of one subscription refresh
// into a single job per subscription (§3.6).
type autoEnqueueQueue struct {
	svc  *Service
	opts AutoEnqueueOptions

	mu      sync.Mutex
	pending map[string]map[string]struct{}
	skipped int64
	created int64
	dropped int64
	failed  int64

	wake     chan struct{}
	overflow chan struct{}
	stopCh   chan struct{}
	wg       sync.WaitGroup
	once     sync.Once
	started  atomic.Bool

	// flushHook is a test seam, mirroring ScheduledRotator.sweepHook: it runs
	// after one flush finished, that is after the job rows were written and the
	// counters below were updated, so a test can observe the coalescer
	// deterministically instead of racing the delay.
	flushHook func()
}

func newAutoEnqueueQueue(svc *Service, opts AutoEnqueueOptions) *autoEnqueueQueue {
	if opts.Delay <= 0 {
		opts.Delay = DefaultAutoEnqueueDelay
	}
	if opts.MaxNodesPerBatch <= 0 {
		opts.MaxNodesPerBatch = DefaultAutoEnqueueBatch
	}
	if opts.MaxSubscriptions <= 0 {
		opts.MaxSubscriptions = DefaultAutoEnqueueSubscriptions
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	return &autoEnqueueQueue{
		svc:      svc,
		opts:     opts,
		pending:  make(map[string]map[string]struct{}),
		wake:     make(chan struct{}, 1),
		overflow: make(chan struct{}, 1),
		stopCh:   make(chan struct{}),
	}
}

// offer buffers one node hash and reports whether it was accepted. The caller is
// a pool callback, so this never blocks on I/O and never creates a job inline.
func (q *autoEnqueueQueue) offer(subscriptionID, nodeHash string) bool {
	subID := strings.TrimSpace(subscriptionID)
	hash := strings.TrimSpace(nodeHash)
	if subID == "" || hash == "" {
		return false
	}
	q.mu.Lock()
	bucket, ok := q.pending[subID]
	if !ok {
		if len(q.pending) >= q.opts.MaxSubscriptions {
			// Bounded over-limit behaviour: flush what is buffered and reuse
			// the freed slots instead of growing without bound.
			q.mu.Unlock()
			select {
			case q.overflow <- struct{}{}:
			default:
			}
			q.mu.Lock()
			if len(q.pending) >= q.opts.MaxSubscriptions {
				q.dropped++
				q.mu.Unlock()
				q.opts.Logf("[intel] auto-enqueue buffer is full; dropping node %s of %s", hash, subID)
				return false
			}
			bucket = q.pending[subID]
		}
		if bucket == nil {
			bucket = make(map[string]struct{}, 8)
			q.pending[subID] = bucket
		}
	}
	if _, exists := bucket[hash]; exists {
		q.mu.Unlock()
		return true
	}
	bucket[hash] = struct{}{}
	overflow := len(bucket) >= q.opts.MaxNodesPerBatch
	q.mu.Unlock()

	if overflow {
		select {
		case q.overflow <- struct{}{}:
		default:
		}
		return true
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

func (q *autoEnqueueQueue) start() {
	if q == nil || !q.started.CompareAndSwap(false, true) {
		return
	}
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		q.loop()
	}()
}

func (q *autoEnqueueQueue) stop() {
	if q == nil {
		return
	}
	q.once.Do(func() { close(q.stopCh) })
	q.wg.Wait()
}

func (q *autoEnqueueQueue) loop() {
	timer := time.NewTimer(q.opts.Delay)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	armed := false
	for {
		select {
		case <-q.stopCh:
			q.flush(context.Background())
			return
		case <-q.overflow:
			if armed && !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			armed = false
			q.flush(context.Background())
		case <-q.wake:
			if !armed {
				timer.Reset(q.opts.Delay)
				armed = true
			}
		case <-timer.C:
			armed = false
			q.flush(context.Background())
		}
	}
}

// flush creates one job per buffered subscription and returns how many jobs were
// created.
func (q *autoEnqueueQueue) flush(ctx context.Context) int {
	if q == nil {
		return 0
	}
	// The hook signals flush completion: it runs after the job rows below were
	// written and the counters were updated, so tests can use it as a barrier.
	if q.flushHook != nil {
		defer q.flushHook()
	}
	q.mu.Lock()
	pending := q.pending
	q.pending = make(map[string]map[string]struct{}, len(pending))
	q.mu.Unlock()
	if len(pending) == 0 {
		return 0
	}

	subs := make([]SubscriptionAutoIntel, 0, len(pending))
	for subID, bucket := range pending {
		hashes := make([]string, 0, len(bucket))
		for hash := range bucket {
			hashes = append(hashes, hash)
		}
		sort.Strings(hashes)
		entry := SubscriptionAutoIntel{SubscriptionID: subID, NodeHashes: hashes}
		if q.opts.Lookup != nil {
			entry.AutoIntel, _ = q.opts.Lookup(subID)
		} else {
			// Without a lookup there is no auto_intel flag to honour, so the
			// safe default is "do not enqueue".
			entry.AutoIntel = false
		}
		subs = append(subs, entry)
	}

	created, skipped, err := q.svc.AutoEnqueueSubscriptions(ctx, subs, q.autoChecks())
	q.mu.Lock()
	q.created += int64(created)
	q.skipped += int64(skipped)
	if err != nil {
		q.failed++
	}
	q.mu.Unlock()
	if err != nil {
		q.opts.Logf("[intel] subscription auto-enqueue: %v", err)
	}
	return created
}

func (q *autoEnqueueQueue) autoChecks() bool {
	if q == nil || q.opts.AutoChecks == nil {
		return false
	}
	return q.opts.AutoChecks()
}

// Counters of the coalescer: how many jobs were created, how many buffers were
// skipped because auto_intel=false, and how many nodes were dropped by the
// emergency bound.
func (q *autoEnqueueQueue) counters() (created, skipped, dropped, failed int64) {
	if q == nil {
		return 0, 0, 0, 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.created, q.skipped, q.dropped, q.failed
}

// EnableAutoEnqueue installs the §3.6 subscription auto-enqueue hook. It is a
// no-op until Start launches the coalescer.
func (s *Service) EnableAutoEnqueue(opts AutoEnqueueOptions) {
	if s == nil {
		return
	}
	s.auto = newAutoEnqueueQueue(s, opts)
}

// OfferSubscriptionNode buffers one freshly added node of a subscription. It
// reports false when the auto-enqueue hook is not configured or the hard bound
// dropped the sample.
func (s *Service) OfferSubscriptionNode(subscriptionID, nodeHash string) bool {
	if s == nil || s.auto == nil {
		return false
	}
	return s.auto.offer(subscriptionID, nodeHash)
}

// FlushAutoEnqueue creates the buffered jobs immediately and returns how many
// jobs were created.
func (s *Service) FlushAutoEnqueue(ctx context.Context) int {
	if s == nil || s.auto == nil {
		return 0
	}
	return s.auto.flush(ctx)
}

// AutoEnqueueCounters reports the coalescer counters (created, skipped by
// auto_intel=false, dropped, failed batches).
func (s *Service) AutoEnqueueCounters() (created, skipped, dropped, failed int64) {
	if s == nil || s.auto == nil {
		return 0, 0, 0, 0
	}
	return s.auto.counters()
}

// AutoEnqueueSubscriptions creates the automatic jobs of §3.6 for a batch of
// subscriptions. It honours intel_enabled (through the job manager) and the
// per-subscription auto_intel flag, merges the node hashes of one subscription
// into a single job, and refuses - with a named error - a batch that exceeds the
// per-job node cap instead of truncating it.
func (s *Service) AutoEnqueueSubscriptions(ctx context.Context, subs []SubscriptionAutoIntel, autoChecks bool) (int, int, error) {
	if s == nil || s.manager == nil {
		return 0, 0, nil
	}
	if !s.manager.Enabled() {
		// intel_enabled=false: no automatic job at all (WP08 §3.4).
		return 0, 0, ErrAutoEnqueueDisabled
	}
	kind := jobs.SubscriptionKindOf(autoChecks)
	created := 0
	skipped := 0
	for _, sub := range subs {
		subID := strings.TrimSpace(sub.SubscriptionID)
		if subID == "" {
			skipped++
			continue
		}
		if !sub.AutoIntel {
			// auto_intel=false: the subscription never enqueues intel work.
			skipped++
			continue
		}
		hashes := normalizeSubscriptionHashes(sub.NodeHashes)
		if len(hashes) == 0 {
			skipped++
			continue
		}
		if len(hashes) > jobs.MaxNodesPerJob {
			return created, skipped, fmt.Errorf("%w: subscription %s: %d > %d",
				jobs.ErrTooManyNodes, subID, len(hashes), jobs.MaxNodesPerJob)
		}
		if _, err := s.manager.Create(ctx, jobs.Request{
			Kind:  kind,
			Scope: jobs.Scope{NodeHashes: hashes},
		}, jobs.CreatedBySubscription(subID), jobs.PrioritySubscription); err != nil {
			if errors.Is(err, jobs.ErrDisabled) {
				return created, skipped, ErrAutoEnqueueDisabled
			}
			return created, skipped, err
		}
		created++
	}
	return created, skipped, nil
}

// AutoEnqueueSubscriptionNodes is the single-subscription form used at import
// time: it wraps the node hashes into the §3.6 request shape.
func (s *Service) AutoEnqueueSubscriptionNodes(ctx context.Context, subscriptionID string, autoIntel bool, nodeHashes []string, autoChecks bool) (bool, error) {
	created, _, err := s.AutoEnqueueSubscriptions(ctx, []SubscriptionAutoIntel{{
		SubscriptionID: subscriptionID,
		AutoIntel:      autoIntel,
		NodeHashes:     nodeHashes,
	}}, autoChecks)
	if err != nil {
		return false, err
	}
	return created > 0, nil
}

// normalizeSubscriptionHashes trims, de-duplicates and sorts one batch of node
// hashes.
func normalizeSubscriptionHashes(hashes []string) []string {
	if len(hashes) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(hashes))
	out := make([]string, 0, len(hashes))
	for _, raw := range hashes {
		hash := strings.TrimSpace(raw)
		if hash == "" {
			continue
		}
		if _, ok := seen[hash]; ok {
			continue
		}
		seen[hash] = struct{}{}
		out = append(out, hash)
	}
	sort.Strings(out)
	return out
}
