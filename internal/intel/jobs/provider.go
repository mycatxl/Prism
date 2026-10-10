package jobs

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"prism/internal/intel/store"
)

// Provider queue timing of §3.3.
const (
	providerSleepMax     = 30 * time.Second
	providerIdleSleep    = time.Second
	providerQueueTimeout = 60 * time.Second
)

// ProviderQueueSpec is the effective configuration of one online-ip data source
// (WP09 owns the Spec, this package owns the scheduling).
type ProviderQueueSpec struct {
	ProviderID string
	Enabled    bool
	DailyLimit int
	QPS        float64
	// BatchSize is how many IPs one Lookup call may resolve (1 = no batching).
	BatchSize int
	// CredentialID is sha256(provider + key) or "" when the provider needs no
	// key. A changed value automatically lifts a pause (§3.3, WP09 §1).
	CredentialID string
}

// ProviderSpecSource returns the currently enabled provider queue specs.
type ProviderSpecSource func() []ProviderQueueSpec

// ProviderOutcome is the result of one online-ip batch lookup. WP09 persists the
// evidence itself; the queue worker only handles budget, retries and scheduling.
type ProviderOutcome struct {
	// Errors maps each IP to a provider error code. An IP that is absent was
	// resolved successfully.
	Errors map[netip.Addr]string
	// Reported counts how many IPs the provider actually answered, for status.
	Reported int
	// BlockedFor > 0 asks for a 429 cooldown of that length.
	BlockedFor time.Duration
	// Pause asks for a pause (401/403 or an invalid key).
	Pause bool
	// CredentialID is the credential that produced this outcome; a rotation
	// lifts a pause automatically.
	CredentialID string
}

// OnlineLookup performs one batch lookup for one online-ip provider.
type OnlineLookup interface {
	Lookup(ctx context.Context, provider string, ips []netip.Addr) ProviderOutcome
}

// OnlineLookupFunc adapts a function to OnlineLookup.
type OnlineLookupFunc func(ctx context.Context, provider string, ips []netip.Addr) ProviderOutcome

// Lookup implements OnlineLookup.
func (f OnlineLookupFunc) Lookup(ctx context.Context, provider string, ips []netip.Addr) ProviderOutcome {
	return f(ctx, provider, ips)
}

// providerPool runs one worker goroutine per enabled online-ip provider.
type providerPool struct {
	manager *Manager
	specs   ProviderSpecSource
	lookup  OnlineLookup
	// onEvidence is called after evidence was written for the given IPs so the
	// caller can re-assess them (§3.2 step 3, WP10 §1.8).
	onEvidence func(ips []netip.Addr)

	mu      sync.Mutex
	running map[string]struct{}
}

func newProviderPool(m *Manager, specs ProviderSpecSource, lookup OnlineLookup, onEvidence func([]netip.Addr)) *providerPool {
	return &providerPool{
		manager:    m,
		specs:      specs,
		lookup:     lookup,
		onEvidence: onEvidence,
		running:    make(map[string]struct{}),
	}
}

// sync starts a worker for every enabled provider that does not have one yet.
// Workers of removed or disabled providers exit on their next iteration.
func (p *providerPool) sync() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, spec := range p.specs() {
		if !spec.Enabled || spec.ProviderID == "" {
			continue
		}
		if _, ok := p.running[spec.ProviderID]; ok {
			continue
		}
		p.running[spec.ProviderID] = struct{}{}
		p.manager.wg.Add(1)
		go func(id string) {
			defer p.manager.wg.Done()
			defer func() {
				p.mu.Lock()
				delete(p.running, id)
				p.mu.Unlock()
			}()
			p.workerLoop(id)
		}(spec.ProviderID)
	}
}

// specOf returns the current spec of one provider.
func (p *providerPool) specOf(provider string) (ProviderQueueSpec, bool) {
	for _, spec := range p.specs() {
		if spec.ProviderID == provider {
			return spec, true
		}
	}
	return ProviderQueueSpec{}, false
}

func (p *providerPool) workerLoop(provider string) {
	for {
		select {
		case <-p.manager.stopCh:
			return
		default:
		}
		spec, ok := p.specOf(provider)
		if !ok || !spec.Enabled {
			if sleepOrStop(p.manager.stopCh, providerIdleSleep) {
				return
			}
			continue
		}
		wait := p.runOnce(provider, spec)
		if wait <= 0 {
			wait = providerIdleSleep
		}
		if wait > providerSleepMax {
			wait = providerSleepMax
		}
		if sleepOrStop(p.manager.stopCh, wait) {
			return
		}
	}
}

// runOnce performs one claim/lookup round and returns how long the worker should
// sleep before the next round.
func (p *providerPool) runOnce(provider string, spec ProviderQueueSpec) time.Duration {
	m := p.manager
	ctx, cancel := context.WithTimeout(context.Background(), providerQueueTimeout)
	defer cancel()

	now := m.nowNs()
	batchSize := spec.BatchSize
	if batchSize <= 0 {
		batchSize = 1
	}

	// Claim before the budget. The daily quota and the QPS slot reserve one
	// request to the vendor, so they may only be spent when there is a request
	// to make. Consuming first burned one unit of the day and one request
	// interval on every idle poll: an empty queue claimed nothing but still
	// advanced provider_state, and the idle sleep is one second while the
	// built-in sources default to QPS 1 (§3.3).
	items, err := m.store.ClaimProviderItems(ctx, provider, now, now+int64(providerLease), batchSize, m.owner)
	if err != nil {
		m.schedulingFailures.Add(1)
		m.logf("[intel] claim provider items for %s: %v", provider, err)
		return providerIdleSleep
	}
	if len(items) == 0 {
		// Nothing to do: no work, no budget. The claim is the only store call
		// of this round, apart from the pause lift below.
		//
		// The rotation rule used to ride along on ConsumeProviderBudget, which
		// this round no longer reaches: a replaced key has to clear the pause
		// the old one caused even while the queue is empty, or the provider
		// keeps reporting itself paused until new work shows up. A keyless
		// source has no credential to rotate, so it skips the read.
		if spec.CredentialID != "" {
			if _, err := m.store.LiftProviderPauseForRotatedCredential(ctx, provider, spec.CredentialID); err != nil {
				m.logf("[intel] lift pause of %s: %v", provider, err)
			}
		}
		return providerIdleSleep
	}

	ips := make([]netip.Addr, 0, len(items))
	for _, item := range items {
		if addr, err := netip.ParseAddr(item.IP); err == nil {
			ips = append(ips, addr)
		}
	}
	if len(ips) == 0 {
		// A row without a usable address is settled without a request, so it
		// must not spend a unit either.
		for _, item := range items {
			_ = m.store.FailProviderItem(ctx, provider, item.IP, item.Attempts+1, now,
				"UNSUPPORTED_IP", true)
		}
		return providerIdleSleep
	}

	state, err := m.store.ConsumeProviderBudget(ctx, store.BudgetRequest{
		Provider:     provider,
		Day:          store.DayString(m.clock.Now()),
		DailyLimit:   spec.DailyLimit,
		QPS:          spec.QPS,
		NowNs:        now,
		CredentialID: spec.CredentialID,
	})
	if err != nil {
		// The gate is shut, so the lookup cannot happen. The rows are already
		// leased by the claim above but nothing was sent and nothing failed:
		// hand them back instead of holding them for the whole lease, and sleep
		// until the gate opens. This is the price of claiming first, and it is
		// why the release matches on the owner: only rows this worker still
		// holds are touched.
		p.releaseItems(ctx, provider, items, now)
		return budgetWait(state, now)
	}

	outcome := p.lookup.Lookup(ctx, provider, ips)
	p.applyOutcome(ctx, provider, items, outcome)

	if outcome.Pause {
		if err := m.store.MarkProviderPaused(ctx, provider, "PROVIDER_AUTH", outcome.CredentialID); err != nil {
			m.logf("[intel] pause provider %s: %v", provider, err)
		}
		return providerSleepMax
	}
	if outcome.BlockedFor > 0 {
		until := m.nowNs() + int64(outcome.BlockedFor)
		if err := m.store.MarkProviderBlocked(ctx, provider, until, "PROVIDER_LIMIT"); err != nil {
			m.logf("[intel] block provider %s: %v", provider, err)
		}
		return outcome.BlockedFor
	}
	if p.onEvidence != nil && outcome.Reported > 0 {
		p.onEvidence(ips)
	}
	return providerIdleSleep
}

// releaseItems hands freshly claimed rows back to the queue after a closed
// budget gate. Nothing was sent and nothing failed, so no row may be resolved,
// failed or counted: only the lease of this worker's own rows is cleared.
func (p *providerPool) releaseItems(ctx context.Context, provider string, items []store.QueueItem, nowNs int64) {
	ips := make([]string, 0, len(items))
	for _, item := range items {
		ips = append(ips, item.IP)
	}
	released, err := p.manager.store.ReleaseProviderItems(ctx, provider, ips, p.manager.owner, nowNs)
	if err != nil {
		// The rows stay leased by this worker; their lease expires and the next
		// claim picks them up again (§3.5), so this is not a lost item.
		p.manager.logf("[intel] release provider items for %s: %v", provider, err)
		return
	}
	if released != int64(len(items)) {
		p.manager.logf("[intel] released %d of %d claimed items for %s",
			released, len(items), provider)
	}
}

// applyOutcome resolves or reschedules every claimed queue row.
func (p *providerPool) applyOutcome(ctx context.Context, provider string, items []store.QueueItem, outcome ProviderOutcome) {
	m := p.manager
	now := m.nowNs()
	for _, item := range items {
		addr, err := netip.ParseAddr(item.IP)
		code := ""
		if err == nil && outcome.Errors != nil {
			code = outcome.Errors[addr]
		}
		if code == "" {
			if err := m.store.ResolveProviderItem(ctx, provider, item.IP); err != nil {
				m.logf("[intel] resolve provider item %s/%s: %v", provider, item.IP, err)
			}
			continue
		}
		attempts := item.Attempts + 1
		terminal := attempts >= maxProviderAttempts
		nextRunAt := now + int64(backoff(item.Attempts))
		if terminal {
			nextRunAt = now
		}
		if err := m.store.FailProviderItem(ctx, provider, item.IP, attempts, nextRunAt, code, terminal); err != nil {
			m.logf("[intel] fail provider item %s/%s: %v", provider, item.IP, err)
		}
	}
}

// budgetWait converts a closed budget gate into a sleep duration, capped at
// providerSleepMax by the caller. The queue rows are untouched: a closed gate is
// not a failure (§3.3).
func budgetWait(state store.ProviderState, nowNs int64) time.Duration {
	// Every branch below either returns or assigns wait, so it starts unset.
	var wait time.Duration
	switch {
	case state.Paused:
		return providerSleepMax
	case state.BlockedUntilNs > nowNs:
		wait = time.Duration(state.BlockedUntilNs - nowNs)
	case state.NextRequestAtNs > nowNs:
		wait = time.Duration(state.NextRequestAtNs - nowNs)
	default:
		return providerSleepMax
	}
	if wait < time.Millisecond {
		wait = time.Millisecond
	}
	return wait
}

// sleepOrStop waits for d and reports whether the pool was asked to stop.
func sleepOrStop(stopCh <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-stopCh:
		return true
	case <-timer.C:
		return false
	}
}
