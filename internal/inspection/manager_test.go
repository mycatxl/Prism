package inspection

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/quality"
	"prism/internal/state"
)

type providerFunc func(context.Context, netip.Addr) (*quality.Evidence, error)

func (f providerFunc) Lookup(ctx context.Context, ip netip.Addr) (*quality.Evidence, error) {
	return f(ctx, ip)
}

func managerEvidence(ip netip.Addr) *quality.Evidence {
	score := 12
	now := time.Now().UTC()
	return &quality.Evidence{IP: ip.Unmap().String(), Provider: quality.ProviderID, Profile: quality.ProfileID, IPType: "residential",
		RiskScore: &score, Grade: "low", ObservedAt: now, ValidUntil: now.Add(time.Hour)}
}

func managerStore(t *testing.T) *state.StateEngine {
	t.Helper()
	directory := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(filepath.Join(directory, "state"), filepath.Join(directory, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	return engine
}

func testManager(t *testing.T, store Store, provider Provider, queue int) *Manager {
	t.Helper()
	cfg := config.QualityConfig{Enabled: true, Workers: 2, QueueSize: queue, Timeout: 2 * time.Second, CacheTTL: time.Hour, DailyLimit: 10}
	m, err := NewManagerWithSources(store, cfg, nil, []Source{{ID: quality.ProviderID, Name: "Test source", Profile: quality.ProfileID,
		Configured: true, DailyLimit: 10, CredentialID: "test-source", Provider: provider}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Stop)
	return m
}

func awaitCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for inspection state")
}

func TestManagerDeduplicatesIPAndSeparatesNodeExitChanges(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	provider := providerFunc(func(ctx context.Context, ip netip.Addr) (*quality.Evidence, error) {
		calls.Add(1)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return managerEvidence(ip), nil
		}
	})
	m := testManager(t, managerStore(t), provider, 32)
	m.Start()
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Request(context.Background(), netip.MustParseAddr("::ffff:8.8.8.8"), true); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	awaitCondition(t, func() bool { return calls.Load() == 1 })
	close(release)
	awaitCondition(t, func() bool { return m.Snapshot(netip.MustParseAddr("8.8.8.8")).State == "valid" })
	if calls.Load() != 1 {
		t.Fatal("same exit IP generated duplicate provider calls")
	}
	if snapshot := m.Snapshot(netip.MustParseAddr("1.1.1.1")); snapshot.Evidence != nil || snapshot.State != "unobserved" {
		t.Fatal("previous exit evidence attached to a different IP")
	}
	if status := m.Status(); status.Sources[0].UsedToday != 1 {
		t.Fatal("deduplicated requests consumed extra budget")
	}
}

type delayedScheduleStore struct {
	Store
	first     atomic.Bool
	scheduled chan struct{}
	release   chan struct{}
}

func (s *delayedScheduleStore) ScheduleInspection(ctx context.Context, task quality.Task, force bool, capacity int, now time.Time) (quality.Record, error) {
	record, err := s.Store.ScheduleInspection(ctx, task, force, capacity, now)
	if err == nil && s.first.CompareAndSwap(false, true) {
		close(s.scheduled)
		select {
		case <-s.release:
		case <-ctx.Done():
			return record, ctx.Err()
		}
	}
	return record, err
}

func TestManagerLateScheduleResponseCannotRevertCompletedCache(t *testing.T) {
	store := &delayedScheduleStore{Store: managerStore(t), scheduled: make(chan struct{}), release: make(chan struct{})}
	m := testManager(t, store, providerFunc(func(_ context.Context, ip netip.Addr) (*quality.Evidence, error) { return managerEvidence(ip), nil }), 8)
	m.Start()
	finished := make(chan error, 1)
	go func() {
		_, err := m.Request(context.Background(), netip.MustParseAddr("8.8.8.8"), false)
		finished <- err
	}()
	select {
	case <-store.scheduled:
	case <-time.After(time.Second):
		t.Fatal("request was not scheduled")
	}
	// The dispatcher claims and finishes the durable task while the original
	// request is still waiting to publish its older queued snapshot.
	awaitCondition(t, func() bool { return m.Snapshot(netip.MustParseAddr("8.8.8.8")).State == "valid" })
	close(store.release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if got := m.Snapshot(netip.MustParseAddr("8.8.8.8")); got.State != "valid" || got.Task.State != "done" {
		t.Fatalf("completed state reverted: %+v", got)
	}
}

type rejectCompletionStore struct {
	Store
	rejected atomic.Bool
	err      error
}

func (s *rejectCompletionStore) CompleteInspection(ctx context.Context, task quality.Task, result quality.Completion, now time.Time) (quality.Record, bool, error) {
	if s.rejected.CompareAndSwap(false, true) {
		if s.err != nil {
			return quality.Record{}, false, s.err
		}
		// Model another lease generation having already published a success.
		record, _, err := s.Store.CompleteInspection(ctx, task, quality.Completion{Evidence: managerEvidence(netip.MustParseAddr(task.IP))}, now)
		return record, false, err
	}
	return s.Store.CompleteInspection(ctx, task, result, now)
}

func TestManagerRejectedOrUncommittedFailureCannotPauseSource(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		name := "stale attempt"
		if failWrite {
			name = "storage failure"
		}
		t.Run(name, func(t *testing.T) {
			store := &rejectCompletionStore{Store: managerStore(t)}
			if failWrite {
				store.err = errors.New("simulated storage failure")
			}
			var calls atomic.Int32
			p := providerFunc(func(_ context.Context, ip netip.Addr) (*quality.Evidence, error) {
				if calls.Add(1) == 1 {
					return nil, &ProviderError{Code: "PROVIDER_AUTH", Message: "late auth failure", Pause: true}
				}
				return managerEvidence(ip), nil
			})
			m := testManager(t, store, p, 8)
			m.Start()
			if _, err := m.Request(context.Background(), netip.MustParseAddr("8.8.8.8"), false); err != nil {
				t.Fatal(err)
			}
			awaitCondition(t, func() bool {
				return store.rejected.Load() && m.Status().Sources[0].Running == 0 || m.Status().StorageError != ""
			})
			status := m.Status()
			if status.Sources[0].Paused {
				t.Fatal("an uncommitted failure paused the source")
			}
			if failWrite {
				if status.StorageError == "" {
					t.Fatal("storage error was not surfaced")
				}
			} else {
				if m.Snapshot(netip.MustParseAddr("8.8.8.8")).State != "valid" {
					t.Fatal("authoritative newer evidence was not loaded")
				}
				if _, err := m.Request(context.Background(), netip.MustParseAddr("1.1.1.1"), false); err != nil {
					t.Fatal(err)
				}
				awaitCondition(t, func() bool { return m.Snapshot(netip.MustParseAddr("1.1.1.1")).State == "valid" })
			}
		})
	}
}

func TestManagerStopCancelsRequestsAndRetainsPendingTask(t *testing.T) {
	store := managerStore(t)
	started := make(chan struct{})
	m := testManager(t, store, providerFunc(func(ctx context.Context, _ netip.Addr) (*quality.Evidence, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}), 8)
	m.Start()
	if _, err := m.Request(context.Background(), netip.MustParseAddr("8.8.8.8"), false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not start")
	}
	done := make(chan struct{})
	go func() { m.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not cancel provider")
	}
	records, err := store.LoadQualityRecords(context.Background())
	if err != nil || len(records) != 1 || records[0].Task.State != "queued" || records[0].Task.Owner != "" {
		t.Fatalf("pending task lost at shutdown: %+v %v", records, err)
	}
	next := testManager(t, store, providerFunc(func(_ context.Context, ip netip.Addr) (*quality.Evidence, error) { return managerEvidence(ip), nil }), 8)
	next.Start()
	awaitCondition(t, func() bool { return next.Snapshot(netip.MustParseAddr("8.8.8.8")).State == "valid" })
	if next.Status().Sources[0].UsedToday != 2 {
		t.Fatal("restart forgot the reserved allowance for a canceled request")
	}
}

func TestObserveIsBoundedWithoutDatabaseOrNetworkWork(t *testing.T) {
	m := testManager(t, managerStore(t), providerFunc(func(context.Context, netip.Addr) (*quality.Evidence, error) {
		t.Error("unexpected provider call")
		return nil, nil
	}), 1)
	if !m.Observe(netip.MustParseAddr("8.8.8.8")) || !m.Observe(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("duplicate observation should merge")
	}
	if m.Observe(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("observation queue exceeded capacity")
	}
	if m.Status().KnownIPs != 0 || m.Status().DroppedObservations != 1 {
		t.Fatal("Observe performed synchronous persistence or lost overflow accounting")
	}
}
