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

	state, err := m.store.ConsumeProviderBudget(ctx, store.BudgetRequest{
		Provider:     provider,
		Day:          store.DayString(m.clock.Now()),
		DailyLimit:   spec.DailyLimit,
		QPS:          spec.QPS,
		NowNs:        now,
		CredentialID: spec.CredentialID,
	})
	if err != nil {
		return budgetWait(state, now)
	}

	items, err := m.store.ClaimProviderItems(ctx, provider, now, now+int64(providerLease), batchSize, m.owner)
	if err != nil {
		m.schedulingFailures.Add(1)
		m.logf("[intel] claim provider items for %s: %v", provider, err)
		return providerIdleSleep
	}
	if len(items) == 0 {
		return providerIdleSleep
	}

	ips := make([]netip.Addr, 0, len(items))
	for _, item := range items {
		if addr, err := netip.ParseAddr(item.IP); err == nil {
			ips = append(ips, addr)
		}
	}
	if len(ips) == 0 {
		for _, item := range items {
			_ = m.store.FailProviderItem(ctx, provider, item.IP, item.Attempts+1, now,
				"UNSUPPORTED_IP", true)
		}
		return providerIdleSleep
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
	wait := providerIdleSleep
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
