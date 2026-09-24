package inspection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"prism/internal/config"
	"prism/internal/quality"
)

type Store interface {
	LoadQualityRecords(context.Context) ([]quality.Record, error)
	ConfigureInspectionSource(context.Context, string, string) (quality.Budget, error)
	ScheduleInspection(context.Context, quality.Task, bool, int, time.Time) (quality.Record, error)
	ClaimInspection(context.Context, string, string, string, int, time.Time) (*quality.Record, quality.Budget, error)
	CompleteInspection(context.Context, quality.Task, quality.Completion, time.Time) (quality.Record, bool, error)
}

type Source struct {
	ID           string
	Name         string
	Profile      string
	Website      string
	Configured   bool
	RequiresKey  bool
	HasKey       bool
	DailyLimit   int
	CredentialID string
	Provider     Provider
}

type SourceStatus struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Website       string     `json:"website"`
	Configured    bool       `json:"configured"`
	RequiresKey   bool       `json:"requires_key"`
	HasKey        bool       `json:"has_key"`
	DailyLimit    int        `json:"daily_limit"`
	UsedToday     int        `json:"used_today"`
	Queued        int        `json:"queued"`
	Running       int        `json:"running"`
	Failed        int        `json:"failed"`
	Paused        bool       `json:"paused"`
	NextAllowedAt *time.Time `json:"next_allowed_at,omitempty"`
	ErrorCode     string     `json:"error_code,omitempty"`
}

// Status is the /api/v1/quality/status wire shape and must stay identical to
// the WebUI QualityStatus type (internal/api/web/src/features/quality/types.ts).
// Empty lists are always serialized as arrays, never as null.
type Status struct {
	Enabled             bool                 `json:"enabled"`
	KnownIPs            int                  `json:"known_ips"`
	CheckedIPs          int                  `json:"checked_ips"`
	LowRiskIPs          int                  `json:"low_risk_ips"`
	HighRiskIPs         int                  `json:"high_risk_ips"`
	StaleIPs            int                  `json:"stale_ips"`
	QueueCapacity       int                  `json:"queue_capacity"`
	DroppedObservations uint64               `json:"dropped_observations"`
	StorageError        string               `json:"storage_error"`
	Sources             []SourceStatus       `json:"sources"`
	ManualSources       []ManualSourceStatus `json:"manual_sources"`
	RegistrySources     []RegistryStatus     `json:"registry_sources"`
}

type RequestResult struct {
	Quality  quality.Summary `json:"quality"`
	Queued   bool            `json:"queued"`
	Warnings []string        `json:"warnings"`
}

type job struct {
	source Source
	task   quality.Task
}

type Manager struct {
	store          Store
	cfg            config.QualityConfig
	sources        []Source
	inventory      func(func(netip.Addr) bool)
	owner          string
	ctx            context.Context
	cancel         context.CancelFunc
	once           sync.Once
	stopOnce       sync.Once
	wg             sync.WaitGroup
	work           chan job
	observations   chan netip.Addr
	wake           chan struct{}
	dropped        atomic.Uint64
	mu             sync.RWMutex
	records        map[string]quality.Record
	budgets        map[string]quality.Budget
	active         map[string]int
	inFlight       int
	storageError   string
	storageRetryAt time.Time
	observeMu      sync.Mutex
	observing      map[string]struct{}
}

func credentialID(provider, key string) string {
	digest := sha256.Sum256([]byte("prism/inspection/credential/v1\x00" + provider + "\x00" + key))
	return hex.EncodeToString(digest[:])
}

func DefaultSources(cfg config.QualityConfig) []Source {
	sources := []Source{
		{ID: quality.ProviderID, Name: "ProxyCheck", Profile: quality.ProfileID, Website: "https://proxycheck.io/api/",
			Configured: true, HasKey: cfg.APIKey != "", DailyLimit: cfg.DailyLimit,
			CredentialID: credentialID(quality.ProviderID, cfg.APIKey),
			Provider:     NewProxyCheckProvider(cfg.APIKey, cfg.Timeout, cfg.CacheTTL)},
		{ID: quality.AbuseProviderID, Name: "AbuseIPDB", Profile: quality.AbuseProfileID, Website: "https://www.abuseipdb.com/",
			Configured: cfg.AbuseAPIKey != "", RequiresKey: true, HasKey: cfg.AbuseAPIKey != "", DailyLimit: 900,
			CredentialID: credentialID(quality.AbuseProviderID, cfg.AbuseAPIKey)},
	}
	if cfg.AbuseAPIKey != "" {
		sources[1].Provider = NewAbuseIPDBProvider(cfg.AbuseAPIKey, cfg.Timeout, cfg.CacheTTL)
	}
	return sources
}

func NewManager(store Store, cfg config.QualityConfig, inventory func(func(netip.Addr) bool)) (*Manager, error) {
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 4096
	}
	if cfg.DailyLimit <= 0 {
		cfg.DailyLimit = 80
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 24 * time.Hour
	}
	return NewManagerWithSources(store, cfg, inventory, DefaultSources(cfg))
}

func NewManagerWithSources(store Store, cfg config.QualityConfig, inventory func(func(netip.Addr) bool), sources []Source) (*Manager, error) {
	if store == nil {
		return nil, errors.New("inspection store is required")
	}
	if cfg.Workers < 1 || cfg.Workers > 8 || cfg.QueueSize < 1 || cfg.QueueSize > 4096 {
		return nil, errors.New("invalid inspection resource limits")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		store: store, cfg: cfg, sources: sources, inventory: inventory, owner: uuid.NewString(),
		ctx: ctx, cancel: cancel, work: make(chan job, cfg.Workers), observations: make(chan netip.Addr, cfg.QueueSize), wake: make(chan struct{}, 1),
		records: make(map[string]quality.Record), budgets: make(map[string]quality.Budget), active: make(map[string]int), observing: make(map[string]struct{}),
	}
	records, err := store.LoadQualityRecords(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	for _, record := range records {
		m.records[recordKey(record.Task)] = record
	}
	for _, source := range sources {
		budget, err := store.ConfigureInspectionSource(ctx, source.ID, source.CredentialID)
		if err != nil {
			cancel()
			return nil, err
		}
		m.budgets[source.ID] = budget
	}
	return m, nil
}

func recordKey(task quality.Task) string {
	return task.IP + "\x00" + task.Provider + "\x00" + task.Profile
}

func (m *Manager) Start() {
	m.once.Do(func() {
		if !m.cfg.Enabled {
			return
		}
		m.wg.Add(1 + m.cfg.Workers)
		go m.run()
		for i := 0; i < m.cfg.Workers; i++ {
			go m.worker()
		}
		if m.inventory != nil {
			m.wg.Add(1)
			go m.reconcile()
		}
	})
}

func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		m.cancel()
		m.wg.Wait()
		for _, source := range m.sources {
			if closer, ok := source.Provider.(interface{ Close() }); ok {
				closer.Close()
			}
		}
	})
}

func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) put(record quality.Record) {
	m.mu.Lock()
	key := recordKey(record.Task)
	if previous, ok := m.records[key]; !ok || previous.Task.Generation <= record.Task.Generation {
		m.records[key] = record
	}
	m.storageError = ""
	m.mu.Unlock()
}

// Observe is called after an egress probe. It performs no database or network
// I/O and cannot block a health worker on provider availability.
func (m *Manager) Observe(ip netip.Addr) bool {
	if !m.cfg.Enabled || m.ctx.Err() != nil {
		return false
	}
	ip, err := quality.PublicIP(ip)
	if err != nil {
		return false
	}
	key := ip.String()
	now := time.Now()
	needsWork := false
	m.mu.RLock()
	for _, source := range m.sources {
		if !source.Configured {
			continue
		}
		record, exists := m.records[recordKey(quality.Task{IP: key, Provider: source.ID, Profile: source.Profile})]
		active := exists && (record.Task.State == "queued" || record.Task.State == "running")
		fresh := record.Evidence != nil && now.Before(record.Evidence.ValidUntil)
		if !active && !fresh {
			needsWork = true
			break
		}
	}
	m.mu.RUnlock()
	if !needsWork {
		return true
	}
	m.observeMu.Lock()
	defer m.observeMu.Unlock()
	if _, exists := m.observing[key]; exists {
		return true
	}
	m.observing[key] = struct{}{}
	select {
	case m.observations <- ip:
		return true
	default:
		delete(m.observing, key)
		m.dropped.Add(1)
		return false
	}
}

func (m *Manager) reconcile() {
	defer m.wg.Done()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		m.inventory(func(ip netip.Addr) bool {
			if m.ctx.Err() != nil {
				return false
			}
			m.Observe(ip)
			return true
		})
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) Request(ctx context.Context, ip netip.Addr, force bool) (RequestResult, error) {
	if !m.cfg.Enabled || m.ctx.Err() != nil {
		return RequestResult{}, quality.ErrDisabled
	}
	ip, err := quality.PublicIP(ip)
	if err != nil {
		return RequestResult{}, err
	}
	result := RequestResult{Warnings: []string{}}
	succeeded := false
	var firstError error
	for _, source := range m.sources {
		if !source.Configured {
			continue
		}
		record, err := m.store.ScheduleInspection(ctx, quality.Task{IP: ip.String(), Provider: source.ID, Profile: source.Profile}, force, m.cfg.QueueSize, time.Now().UTC())
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			result.Warnings = append(result.Warnings, source.ID+": inspection could not be queued")
			continue
		}
		succeeded = true
		m.put(record)
		result.Queued = result.Queued || record.Task.State == "queued" || record.Task.State == "running"
	}
	if !succeeded {
		if firstError != nil {
			return result, firstError
		}
		return result, quality.ErrDisabled
	}
	result.Quality = m.Snapshot(ip)
	m.signal()
	return result, nil
}

func (m *Manager) run() {
	defer m.wg.Done()
	defer close(m.work)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case ip := <-m.observations:
			_, err := m.Request(m.ctx, ip, false)
			if err != nil && !errors.Is(err, context.Canceled) {
				m.dropped.Add(1)
			}
			m.observeMu.Lock()
			delete(m.observing, ip.String())
			m.observeMu.Unlock()
		case <-ticker.C:
		case <-m.wake:
		}
		m.dispatch()
	}
}

func (m *Manager) dispatch() {
	for _, source := range m.sources {
		if !source.Configured || m.ctx.Err() != nil {
			continue
		}
		m.mu.RLock()
		free := m.inFlight < m.cfg.Workers && m.active[source.ID] < 2
		budget := m.budgets[source.ID]
		storageRetryAt := m.storageRetryAt
		m.mu.RUnlock()
		now := time.Now().UTC()
		if !free || now.Before(storageRetryAt) || budget.Paused || now.Before(budget.BlockedUntil) || now.Before(budget.NextRequestAt) {
			continue
		}
		record, budget, err := m.store.ClaimInspection(m.ctx, source.ID, source.Profile, m.owner, source.DailyLimit, now)
		m.mu.Lock()
		if err == nil {
			m.budgets[source.ID] = budget
		}
		if err != nil && m.ctx.Err() == nil {
			m.storageError = "QUALITY_STORAGE_UNAVAILABLE"
			m.storageRetryAt = now.Add(30 * time.Second)
		}
		m.mu.Unlock()
		if err != nil || record == nil {
			continue
		}
		m.put(*record)
		if record.Task.State != "running" {
			continue
		}
		m.mu.Lock()
		m.active[source.ID]++
		m.inFlight++
		m.mu.Unlock()
		m.work <- job{source: source, task: record.Task}
	}
}

func (m *Manager) worker() {
	defer m.wg.Done()
	for work := range m.work {
		now := time.Now().UTC()
		result := quality.Completion{}
		if m.ctx.Err() != nil {
			result.Canceled = true
		} else {
			ctx, cancel := context.WithTimeout(m.ctx, m.cfg.Timeout)
			evidence, err := work.source.Provider.Lookup(ctx, netip.MustParseAddr(work.task.IP))
			cancel()
			now = time.Now().UTC()
			if m.ctx.Err() != nil {
				result.Canceled = true
			} else if err == nil && evidence != nil {
				result.Evidence = evidence
			} else {
				failure := providerFailure(err)
				result.ErrorCode, result.ErrorMessage = failure.Code, failure.Message
				result.RetryAt = now.Add(time.Duration(30*(1<<min(work.task.Attempt-1, 3)))*time.Second + time.Duration(now.Nanosecond()%5000)*time.Millisecond)
				if failure.Code == "PROVIDER_AUTH" {
					result.Pause = true
				} else if failure.Code == "PROVIDER_LIMIT" {
					result.BlockedUntil = now.Add(max(failure.RetryAfter, time.Minute))
					result.RetryAt = result.BlockedUntil
				}
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		record, committed, err := m.store.CompleteInspection(ctx, work.task, result, now)
		cancel()
		m.mu.Lock()
		m.active[work.source.ID]--
		m.inFlight--
		if err != nil {
			m.storageError = "QUALITY_STORAGE_UNAVAILABLE"
			m.storageRetryAt = now.Add(30 * time.Second)
		} else if record.Task.IP != "" {
			key := recordKey(record.Task)
			if previous, ok := m.records[key]; !ok || previous.Task.Generation <= record.Task.Generation {
				m.records[key] = record
			}
			m.storageError = ""
		}
		if err == nil && committed {
			budget := m.budgets[work.source.ID]
			if result.Pause || !result.BlockedUntil.IsZero() {
				if !budget.Paused {
					budget.ErrorCode = result.ErrorCode
				}
				budget.Paused = budget.Paused || result.Pause
				if result.BlockedUntil.After(budget.BlockedUntil) {
					budget.BlockedUntil = result.BlockedUntil
				}
			} else if result.Evidence != nil && !budget.Paused && !now.Before(budget.BlockedUntil) {
				budget.ErrorCode = ""
			}
			m.budgets[work.source.ID] = budget
		}
		m.mu.Unlock()
		m.signal()
	}
}

func (m *Manager) Snapshot(ip netip.Addr) quality.Summary {
	if !ip.IsValid() {
		return quality.Summary{State: "unobserved", Sources: []quality.SourceSummary{}}
	}
	ip = ip.Unmap()
	summary := quality.Summary{IP: ip.String(), State: "unobserved", Sources: []quality.SourceSummary{}}
	if _, err := quality.PublicIP(ip); err != nil {
		summary.State = "unsupported"
		return summary
	}
	now := time.Now()
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, source := range m.sources {
		record, exists := m.records[recordKey(quality.Task{IP: ip.String(), Provider: source.ID, Profile: source.Profile})]
		item := quality.SourceSummary{Provider: source.ID, Configured: source.Configured, State: "unobserved"}
		if exists {
			s := record.Summary(now)
			item.State, item.Evidence, item.Task = s.State, s.Evidence, s.Task
			if source.ID == quality.ProviderID {
				summary.State, summary.Evidence, summary.Task = s.State, s.Evidence, s.Task
			}
		}
		summary.Sources = append(summary.Sources, item)
	}
	return summary
}

func (m *Manager) List(query string) []quality.Summary {
	m.mu.RLock()
	ips := make(map[string]struct{})
	for _, record := range m.records {
		ips[record.Task.IP] = struct{}{}
	}
	m.mu.RUnlock()
	query = strings.ToLower(strings.TrimSpace(query))
	result := make([]quality.Summary, 0, len(ips))
	for ip := range ips {
		address, err := netip.ParseAddr(ip)
		if err != nil {
			continue
		}
		summary := m.Snapshot(address)
		haystack := ip
		if summary.Evidence != nil {
			haystack += " " + summary.Evidence.ASN + " " + summary.Evidence.Organization
		}
		if query == "" || strings.Contains(strings.ToLower(haystack), query) {
			result = append(result, summary)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Evidence != nil && result[j].Evidence != nil && !result[i].Evidence.ObservedAt.Equal(result[j].Evidence.ObservedAt) {
			return result[i].Evidence.ObservedAt.After(result[j].Evidence.ObservedAt)
		}
		if (result[i].Evidence != nil) != (result[j].Evidence != nil) {
			return result[i].Evidence != nil
		}
		return result[i].IP < result[j].IP
	})
	return result
}

func (m *Manager) Status() Status {
	status := Status{Enabled: m.cfg.Enabled, QueueCapacity: m.cfg.QueueSize, DroppedObservations: m.dropped.Load(), Sources: []SourceStatus{}}
	now := time.Now().UTC()
	m.mu.RLock()
	defer m.mu.RUnlock()
	status.StorageError = m.storageError
	known := make(map[string]struct{})
	for _, record := range m.records {
		known[record.Task.IP] = struct{}{}
		if record.Task.Provider != quality.ProviderID || record.Task.Profile != quality.ProfileID || record.Evidence == nil {
			continue
		}
		if !now.Before(record.Evidence.ValidUntil) {
			status.StaleIPs++
			continue
		}
		status.CheckedIPs++
		if record.Evidence.IPType == "conflicting" {
			continue
		}
		switch record.Evidence.Grade {
		case "low":
			status.LowRiskIPs++
		case "high", "severe":
			status.HighRiskIPs++
		}
	}
	status.KnownIPs = len(known)
	for _, source := range m.sources {
		item := SourceStatus{ID: source.ID, Name: source.Name, Website: source.Website, Configured: source.Configured,
			RequiresKey: source.RequiresKey, HasKey: source.HasKey, DailyLimit: source.DailyLimit}
		budget := m.budgets[source.ID]
		if budget.Day >= now.Format("2006-01-02") {
			item.UsedToday = budget.Used
		}
		item.Paused = budget.Paused
		if budget.Paused || now.Before(budget.BlockedUntil) {
			item.ErrorCode = budget.ErrorCode
		}
		next := budget.BlockedUntil
		if budget.NextRequestAt.After(next) {
			next = budget.NextRequestAt
		}
		if next.After(now) {
			item.NextAllowedAt = &next
		}
		for _, record := range m.records {
			if record.Task.Provider != source.ID || record.Task.Profile != source.Profile {
				continue
			}
			switch record.Task.State {
			case "queued":
				item.Queued++
			case "running":
				item.Running++
			case "failed":
				item.Failed++
			}
		}
		status.Sources = append(status.Sources, item)
	}
	return status
}

func (m *Manager) String() string {
	return fmt.Sprintf("inspection(enabled=%t, sources=%d)", m.cfg.Enabled, len(m.sources))
}
