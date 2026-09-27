// This file implements the §3.6 scheduled refresh of the intel subsystem.
//
// The plan requires a periodic job driven by intel_refresh_schedule:
//
//	**定时刷新**：按 `intel_refresh_schedule` 创建一个 `created_by: "system:refresh"`、
//	优先级 10 的任务，范围是符合以下条件的节点：存在已过期证据、已过期检测，或出口
//	观测超过 48 小时。
//
// Without it, evidence that passed its TTL is never re-queried and a deployment
// that leaves intel_auto_checks off (the default) keeps reading stale unlock
// results forever. This file supplies the missing trigger.
package intel

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"

	"prism/internal/intel/jobs"
)

const (
	// staleEgressWindow is the "出口观测超过 48 小时" condition of §3.6.
	staleEgressWindow = 48 * time.Hour
	// maxRefreshScopePerSource bounds each of the three scope queries. Nodes past
	// it stay stale and are picked up by the next run, so the bound never loses
	// work - it only spreads it.
	maxRefreshScopePerSource = 50_000
	// refreshRunTimeout bounds one refresh pass. Building the scope is a handful
	// of indexed queries; the bound is there so a wedged database cannot make the
	// scheduler stop honouring its own stop channel.
	refreshRunTimeout = 5 * time.Minute
	// refreshScheduleRetry is how long the loop waits before re-reading a
	// schedule it could not parse. A running instance should never see one -
	// PATCH validates the expression - so this only covers a corrupted row.
	refreshScheduleRetry = time.Minute
)

// RefreshScheduleOptions configures the §3.6 scheduled refresh. Every field
// except Logf is required for the scheduler to do anything; a missing one makes
// the pass a no-op with a logged reason instead of a panic.
type RefreshScheduleOptions struct {
	// Schedule returns the cron expression (intel_refresh_schedule). It is read
	// on every wake-up, so a PATCH takes effect without a restart.
	Schedule func() string
	// AutoChecks returns intel_auto_checks. It selects the job kind exactly the
	// way the subscription auto-enqueue does, so a deployment that never opted
	// into unlock checks does not start paying for them once a day.
	AutoChecks func() bool
	// Census reports the node hashes that still exist. A node the pool has
	// dropped is filtered out of the scope: jobs.Scope.NodeHashes is taken as
	// given by the scope resolver, so nothing else would remove it.
	Census jobs.NodeCensus
	// Logf receives diagnostics.
	Logf func(format string, args ...any)
}

// refreshScheduler creates the §3.6 scheduled refresh job.
type refreshScheduler struct {
	svc  *Service
	opts RefreshScheduleOptions

	stopCh chan struct{}
	wg     sync.WaitGroup
	once   sync.Once

	started atomic.Bool

	// runHook is a test seam: it runs after one pass finished, with the number of
	// jobs created, so a test can observe the result without racing the timer.
	runHook func(created int)

	created     atomic.Int64
	lastRunNs   atomic.Int64
	lastErr     atomic.Value // string
	lastScanned atomic.Int64
}

func newRefreshScheduler(svc *Service, opts RefreshScheduleOptions) *refreshScheduler {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	return &refreshScheduler{svc: svc, opts: opts, stopCh: make(chan struct{})}
}

// EnableRefreshSchedule installs the §3.6 scheduled refresh. It is a no-op until
// Start launches the loop.
func (s *Service) EnableRefreshSchedule(opts RefreshScheduleOptions) {
	if s == nil {
		return
	}
	s.refresh = newRefreshScheduler(s, opts)
}

// RefreshScheduleStats reports the counters of the scheduled refresh: how many
// jobs it created, when it last ran (UnixNano, 0 when it has not), the size of
// the last scope it resolved, and the last error message (empty when none).
func (s *Service) RefreshScheduleStats() (created int64, lastRunNs int64, lastScanned int64, lastErr string) {
	if s == nil || s.refresh == nil {
		return 0, 0, 0, ""
	}
	message, _ := s.refresh.lastErr.Load().(string)
	return s.refresh.created.Load(), s.refresh.lastRunNs.Load(), s.refresh.lastScanned.Load(), message
}

// RunRefreshNow runs one refresh pass immediately and reports how many jobs it
// created. It is the manual form of the scheduled pass and the entry point tests
// drive, so the pass is testable without waiting for a cron boundary.
func (s *Service) RunRefreshNow(ctx context.Context) (int, error) {
	if s == nil || s.refresh == nil {
		return 0, nil
	}
	return s.refresh.runOnce(ctx)
}

func (r *refreshScheduler) start() {
	if r == nil || !r.started.CompareAndSwap(false, true) {
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.loop()
	}()
}

func (r *refreshScheduler) stop() {
	if r == nil {
		return
	}
	r.once.Do(func() { close(r.stopCh) })
	r.wg.Wait()
}

// loop waits for the next scheduled boundary. The schedule is re-read every time
// so a PATCH applies to the next wake-up without a restart.
func (r *refreshScheduler) loop() {
	for {
		now := r.svc.clock.Now().UTC()
		delay, err := r.nextDelay(now)
		if err != nil {
			r.recordError(err)
			r.opts.Logf("[intel] scheduled refresh is not running: %v", err)
			select {
			case <-r.stopCh:
				return
			case <-time.After(refreshScheduleRetry):
				continue
			}
		}
		r.opts.Logf("[intel] scheduled refresh: next pass in %s", delay.Round(time.Second))

		timer := time.NewTimer(delay)
		select {
		case <-r.stopCh:
			timer.Stop()
			return
		case <-timer.C:
			ctx, cancel := context.WithTimeout(context.Background(), refreshRunTimeout)
			if _, err := r.runOnce(ctx); err != nil {
				r.opts.Logf("[intel] scheduled refresh failed: %v", err)
			}
			cancel()
		}
	}
}

// nextDelay returns how long to wait before the next pass.
func (r *refreshScheduler) nextDelay(now time.Time) (time.Duration, error) {
	expr := ""
	if r.opts.Schedule != nil {
		expr = strings.TrimSpace(r.opts.Schedule())
	}
	if expr == "" {
		return 0, fmt.Errorf("intel_refresh_schedule is empty")
	}
	schedule, err := cron.ParseStandard(expr)
	if err != nil {
		return 0, fmt.Errorf("intel_refresh_schedule %q is not a cron expression: %w", expr, err)
	}
	next := schedule.Next(now)
	if next.IsZero() {
		return 0, fmt.Errorf("intel_refresh_schedule %q has no next run after %s", expr, now)
	}
	return next.Sub(now), nil
}

// runOnce resolves the scope and creates one job. It returns 1 when a job was
// created and 0 when nothing was stale - an empty scope is a normal outcome of a
// daily pass, not an error.
func (r *refreshScheduler) runOnce(ctx context.Context) (created int, err error) {
	defer func() {
		if r.runHook != nil {
			r.runHook(created)
		}
	}()
	if r == nil || r.svc == nil || r.svc.manager == nil || r.svc.store == nil {
		return 0, nil
	}

	now := r.svc.clock.Now().UTC()
	kind := jobs.SubscriptionKindOf(r.autoChecks())

	hashes, err := r.collectScope(ctx, now, kind)
	if err != nil {
		r.recordError(err)
		return 0, err
	}
	r.lastScanned.Store(int64(len(hashes)))
	if len(hashes) == 0 {
		r.lastRunNs.Store(now.UnixNano())
		r.recordError(nil)
		r.opts.Logf("[intel] scheduled refresh: nothing is stale, no job created")
		return 0, nil
	}

	if _, err := r.svc.manager.Create(ctx, jobs.Request{
		Kind:  kind,
		Scope: jobs.Scope{NodeHashes: hashes},
	}, jobs.CreatedByRefresh(), jobs.PriorityRefresh); err != nil {
		r.recordError(err)
		return 0, err
	}

	r.created.Add(1)
	r.lastRunNs.Store(now.UnixNano())
	r.recordError(nil)
	r.opts.Logf("[intel] scheduled refresh: created a kind=%s job for %d node(s)", kind, len(hashes))
	return 1, nil
}

// autoChecks reads the intel_auto_checks switch; a missing reader means "off",
// which matches the shipped default.
func (r *refreshScheduler) autoChecks() bool {
	if r.opts.AutoChecks == nil {
		return false
	}
	return r.opts.AutoChecks()
}

// collectScope resolves the three §3.6 conditions into one de-duplicated, bounded
// list. The census is applied before the cap so the cap counts nodes that really
// exist.
func (r *refreshScheduler) collectScope(ctx context.Context, now time.Time, kind jobs.Kind) ([]string, error) {
	if r == nil || r.svc == nil || r.svc.store == nil {
		return nil, fmt.Errorf("scheduled refresh: the scheduler is not wired to a store")
	}
	st := r.svc.store
	seen := make(map[string]struct{})

	add := func(hashes []string) {
		for _, hash := range hashes {
			if trimmed := strings.TrimSpace(hash); trimmed != "" {
				seen[trimmed] = struct{}{}
			}
		}
	}

	// Condition 1: 存在已过期证据.
	expiredEvidence, err := st.ListNodeHashesWithExpiredEvidence(ctx, now.UnixNano(), maxRefreshScopePerSource)
	if err != nil {
		return nil, fmt.Errorf("expired-evidence scope: %w", err)
	}
	add(expiredEvidence)

	// Condition 2: 已过期检测. Only a pipeline that runs step 5 can refresh a
	// check, so kind=intel drops this condition instead of enqueueing nodes whose
	// check step would be skipped.
	if kind == jobs.KindFull {
		expiredChecks, err := st.ListExpiredChecks(ctx, now.UnixNano(), maxRefreshScopePerSource)
		if err != nil {
			return nil, fmt.Errorf("expired-check scope: %w", err)
		}
		hashes := make([]string, 0, len(expiredChecks))
		for _, row := range expiredChecks {
			hashes = append(hashes, row.NodeHash)
		}
		add(hashes)
	}

	// Condition 3: 出口观测超过 48 小时.
	staleEgress, err := st.ListStaleNodeEgress(ctx, now.Add(-staleEgressWindow).UnixNano(), maxRefreshScopePerSource)
	if err != nil {
		return nil, fmt.Errorf("stale-egress scope: %w", err)
	}
	add(staleEgress)

	out := make([]string, 0, len(seen))
	for hash := range seen {
		out = append(out, hash)
	}
	sort.Strings(out)

	// A node the pool has dropped must never be refreshed: the scope resolver
	// takes explicit hashes as given, so nothing downstream would remove it.
	if r.opts.Census != nil {
		known := r.opts.Census.KnownNodeHashes()
		kept := make([]string, 0, len(out))
		for _, hash := range out {
			if _, ok := known[hash]; ok {
				kept = append(kept, hash)
			}
		}
		if dropped := len(out) - len(kept); dropped > 0 {
			r.opts.Logf("[intel] scheduled refresh: dropped %d node(s) the pool no longer knows", dropped)
		}
		out = kept
	}

	if len(out) > jobs.MaxNodesPerJob {
		r.opts.Logf("[intel] scheduled refresh: %d node(s) are stale, refreshing the first %d this pass",
			len(out), jobs.MaxNodesPerJob)
		out = out[:jobs.MaxNodesPerJob]
	}
	return out, nil
}

func (r *refreshScheduler) recordError(err error) {
	if err == nil {
		r.lastErr.Store("")
		return
	}
	r.lastErr.Store(err.Error())
}
