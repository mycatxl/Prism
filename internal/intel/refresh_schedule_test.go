package intel

import (
	"context"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"prism/internal/intel/jobs"
	"prism/internal/intel/store"
	"prism/internal/node"
)

// refreshTestClock freezes time so the 48-hour and TTL comparisons are exact.
type refreshTestClock struct{ now time.Time }

func (c refreshTestClock) Now() time.Time { return c.now }

// Four distinct, well-formed node hashes: one per §3.6 condition plus a control.
const (
	refreshExpiredEvidenceHash = "11111111111111111111111111111111"
	refreshExpiredCheckHash    = "22222222222222222222222222222222"
	refreshStaleEgressHash     = "33333333333333333333333333333333"
	refreshFreshHash           = "44444444444444444444444444444444"
)

// refreshCensus is a fixed KnownNodeHashes set.
type refreshCensus map[string]struct{}

func (c refreshCensus) KnownNodeHashes() map[string]struct{} { return c }

// newRefreshService assembles a Service whose job manager is never started, so a
// created job stays queued and a test can inspect it without a worker racing in.
func newRefreshServiceBare(t *testing.T, now time.Time) (*Service, *store.Store) {
	t.Helper()
	st := openIntelStore(t)
	svc, err := NewService(Options{
		Store: st,
		Scope: jobs.ScopeResolverFunc(func(_ context.Context, scope jobs.Scope) ([]string, error) {
			return scope.NodeHashes, nil
		}),
		Config: func() jobs.Config {
			return jobs.Config{Enabled: true, MaxRunningJobs: 1}.Normalize()
		},
		Clock: refreshTestClock{now: now},
		Logf:  func(string, ...any) {},
		Tick:  20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, st
}

// newRefreshServiceWithSchedule enables the §3.6 scheduler on a schedule that
// never fires during a test, which is what lets the scope tests call the
// collector directly.
func newRefreshServiceWithSchedule(t *testing.T, now time.Time) (*Service, *store.Store) {
	t.Helper()
	svc, st := newRefreshServiceBare(t, now)
	svc.EnableRefreshSchedule(RefreshScheduleOptions{
		Schedule: func() string { return "0 4 * * *" },
		Logf:     func(string, ...any) {},
	})
	return svc, st
}

// seedEgress records one node's egress observation `age` before now.
func seedEgress(t *testing.T, st *store.Store, now time.Time, hash, ip string, age time.Duration) {
	t.Helper()
	parsed, err := node.ParseHex(hash)
	if err != nil {
		t.Fatalf("ParseHex(%s): %v", hash, err)
	}
	if _, _, err := st.RecordEgress(context.Background(), store.EgressObservation{
		NodeHash: parsed.String(),
		IPv4:     netip.MustParseAddr(ip),
		Colo:     "NRT",
		Loc:      "JP",
		NowNs:    now.Add(-age).UnixNano(),
	}); err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
}

// seedExpiredEvidence writes one already-expired evidence row for an address.
func seedExpiredEvidence(t *testing.T, st *store.Store, now time.Time, ip string) {
	t.Helper()
	if err := st.UpsertEvidence(context.Background(), store.Evidence{
		IP:           ip,
		Provider:     "proxycheck",
		Profile:      "proxycheck-v3",
		Status:       store.StatusOk,
		ObservedAtNs: now.Add(-48 * time.Hour).UnixNano(),
		ValidUntilNs: now.Add(-24 * time.Hour).UnixNano(),
	}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}
}

// seedExpiredCheck writes one already-expired unlock-check row for a node.
func seedExpiredCheck(t *testing.T, st *store.Store, now time.Time, hash string) {
	t.Helper()
	parsed, err := node.ParseHex(hash)
	if err != nil {
		t.Fatalf("ParseHex(%s): %v", hash, err)
	}
	if err := st.UpsertNodeCheck(context.Background(), store.NodeCheck{
		NodeHash:     parsed.String(),
		CheckID:      "netflix",
		CheckVersion: 1,
		Outcome:      "available",
		ObservedAtNs: now.Add(-48 * time.Hour).UnixNano(),
		ValidUntilNs: now.Add(-24 * time.Hour).UnixNano(),
	}); err != nil {
		t.Fatalf("UpsertNodeCheck: %v", err)
	}
}

// seedAllThreeConditions plants one node per §3.6 condition plus a fresh control.
func seedAllThreeConditions(t *testing.T, st *store.Store, now time.Time) {
	t.Helper()
	// Condition 1: expired evidence on the node's current egress address.
	seedEgress(t, st, now, refreshExpiredEvidenceHash, "198.51.100.10", time.Minute)
	seedExpiredEvidence(t, st, now, "198.51.100.10")
	// Condition 2: an expired unlock check.
	seedEgress(t, st, now, refreshExpiredCheckHash, "198.51.100.11", time.Minute)
	seedExpiredCheck(t, st, now, refreshExpiredCheckHash)
	// Condition 3: the egress observation is older than 48 hours.
	seedEgress(t, st, now, refreshStaleEgressHash, "198.51.100.12", 49*time.Hour)
	// Control: fresh in every respect, must never enter a scope.
	seedEgress(t, st, now, refreshFreshHash, "198.51.100.13", time.Minute)
}

// TestRefreshScopeCollectsTheThreeConditions pins the §3.6 scope definition.
func TestRefreshScopeCollectsTheThreeConditions(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	svc, st := newRefreshServiceWithSchedule(t, now)
	seedAllThreeConditions(t, st, now)

	got, err := svc.refresh.collectScope(context.Background(), now, jobs.KindFull)
	if err != nil {
		t.Fatalf("collectScope: %v", err)
	}
	want := []string{refreshExpiredEvidenceHash, refreshExpiredCheckHash, refreshStaleEgressHash}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scope = %v, want %v (the fresh control must not appear)", got, want)
	}
}

// TestRefreshScopeKeepsChecksOutOfKindIntel: kind=intel has no check step, so an
// expired check alone must not enqueue a node - that would spend the whole
// evidence pipeline to refresh something the run cannot refresh.
func TestRefreshScopeKeepsChecksOutOfKindIntel(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	svc, st := newRefreshServiceWithSchedule(t, now)
	seedAllThreeConditions(t, st, now)

	got, err := svc.refresh.collectScope(context.Background(), now, jobs.KindIntel)
	if err != nil {
		t.Fatalf("collectScope: %v", err)
	}
	want := []string{refreshExpiredEvidenceHash, refreshStaleEgressHash}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kind=intel scope = %v, want %v", got, want)
	}
}

// TestRefreshScopeDropsNodesThePoolForgot: the scope resolver takes explicit
// hashes as given, so a node the pool dropped has to be filtered here.
func TestRefreshScopeDropsNodesThePoolForgot(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	svc, st := newRefreshServiceWithSchedule(t, now)
	seedAllThreeConditions(t, st, now)

	svc.EnableRefreshSchedule(RefreshScheduleOptions{
		Schedule: func() string { return "0 4 * * *" },
		Logf:     func(string, ...any) {},
		Census: refreshCensus{
			refreshExpiredEvidenceHash: {},
			refreshStaleEgressHash:     {},
		},
	})

	got, err := svc.refresh.collectScope(context.Background(), now, jobs.KindFull)
	if err != nil {
		t.Fatalf("collectScope: %v", err)
	}
	want := []string{refreshExpiredEvidenceHash, refreshStaleEgressHash}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scope = %v, want %v (the forgotten node must be dropped)", got, want)
	}
}

// TestRefreshScheduleCreatesTheSystemJob checks the §3.6 contract of the created
// job: created_by "system:refresh", priority 10, and the kind the auto-checks
// switch selects.
func TestRefreshScheduleCreatesTheSystemJob(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	for _, autoChecks := range []bool{false, true} {
		name := "intel_auto_checks=false"
		wantKind := jobs.KindIntel
		if autoChecks {
			name = "intel_auto_checks=true"
			wantKind = jobs.KindFull
		}
		t.Run(name, func(t *testing.T) {
			svc, st := newRefreshServiceWithSchedule(t, now)
			seedAllThreeConditions(t, st, now)
			svc.EnableRefreshSchedule(RefreshScheduleOptions{
				Schedule:   func() string { return "0 4 * * *" },
				AutoChecks: func() bool { return autoChecks },
				Logf:       func(string, ...any) {},
			})

			created, err := svc.RunRefreshNow(context.Background())
			if err != nil {
				t.Fatalf("RunRefreshNow: %v", err)
			}
			if created != 1 {
				t.Fatalf("created = %d, want 1", created)
			}

			list, err := st.ListJobs(context.Background(), "", 10, 0)
			if err != nil {
				t.Fatalf("ListJobs: %v", err)
			}
			if len(list) != 1 {
				t.Fatalf("jobs = %d, want exactly 1", len(list))
			}
			job := list[0]
			if job.CreatedBy != jobs.CreatedByRefresh() {
				t.Fatalf("created_by = %q, want %q", job.CreatedBy, jobs.CreatedByRefresh())
			}
			if job.Priority != jobs.PriorityRefresh {
				t.Fatalf("priority = %d, want %d", job.Priority, jobs.PriorityRefresh)
			}
			if job.Kind != string(wantKind) {
				t.Fatalf("kind = %q, want %q", job.Kind, wantKind)
			}
			if job.Status != store.JobQueued {
				t.Fatalf("status = %q, want %q (the manager must not have started)", job.Status, store.JobQueued)
			}
			if job.Total < 1 {
				t.Fatalf("job total = %d, want the stale nodes", job.Total)
			}

			createdCount, lastRunNs, scanned, lastErr := svc.RefreshScheduleStats()
			if createdCount != 1 || lastRunNs != now.UnixNano() || scanned < 1 || lastErr != "" {
				t.Fatalf("stats = (%d, %d, %d, %q), want (1, %d, >=1, \"\")",
					createdCount, lastRunNs, scanned, lastErr, now.UnixNano())
			}
		})
	}
}

// TestRefreshScheduleSkipsWhenNothingIsStale: an empty scope is a normal outcome
// of a daily pass, so it must create no job and report no error.
func TestRefreshScheduleSkipsWhenNothingIsStale(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	svc, st := newRefreshServiceWithSchedule(t, now)
	seedEgress(t, st, now, refreshFreshHash, "198.51.100.13", time.Minute)
	svc.EnableRefreshSchedule(RefreshScheduleOptions{
		Schedule: func() string { return "0 4 * * *" },
		Logf:     func(string, ...any) {},
	})

	created, err := svc.RunRefreshNow(context.Background())
	if err != nil {
		t.Fatalf("RunRefreshNow: %v", err)
	}
	if created != 0 {
		t.Fatalf("created = %d, want 0", created)
	}
	list, err := st.ListJobs(context.Background(), "", 10, 0)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("jobs = %d, want none", len(list))
	}
}

// TestRefreshScheduleIsOffUnlessEnabled: without EnableRefreshSchedule the manual
// entry point is a no-op rather than a nil dereference.
func TestRefreshScheduleIsOffUnlessEnabled(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	svc, st := newRefreshServiceBare(t, now)
	seedAllThreeConditions(t, st, now)

	created, err := svc.RunRefreshNow(context.Background())
	if err != nil || created != 0 {
		t.Fatalf("RunRefreshNow without EnableRefreshSchedule = (%d, %v), want (0, nil)", created, err)
	}
	createdCount, lastRunNs, scanned, lastErr := svc.RefreshScheduleStats()
	if createdCount != 0 || lastRunNs != 0 || scanned != 0 || lastErr != "" {
		t.Fatalf("stats = (%d, %d, %d, %q), want all zero", createdCount, lastRunNs, scanned, lastErr)
	}
}

// TestRefreshScheduleNextDelay covers the cron arithmetic, including the two
// failure shapes that must not busy-loop.
func TestRefreshScheduleNextDelay(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		expr    string
		want    time.Duration
		wantErr bool
	}{
		{name: "daily 04:00", expr: "0 4 * * *", want: 16 * time.Hour},
		{name: "every 15 minutes", expr: "*/15 * * * *", want: 15 * time.Minute},
		{name: "empty", expr: "", wantErr: true},
		{name: "not a cron expression", expr: "tomorrow please", wantErr: true},
		{name: "five seconds spec is not standard", expr: "*/5 * * * * *", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &refreshScheduler{opts: RefreshScheduleOptions{
				Schedule: func() string { return tc.expr },
			}}
			got, err := s.nextDelay(now)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("nextDelay(%q) = %s, want an error", tc.expr, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("nextDelay(%q): %v", tc.expr, err)
			}
			if got != tc.want {
				t.Fatalf("nextDelay(%q) = %s, want %s", tc.expr, got, tc.want)
			}
		})
	}
}

// TestRefreshScheduleStartStopIsClean: the loop must honour its stop channel
// while parked on a long timer, and a second stop must not panic.
func TestRefreshScheduleStartStopIsClean(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	svc, _ := newRefreshServiceWithSchedule(t, now)
	// The default schedule is the next 04:00, so the loop parks immediately.
	svc.EnableRefreshSchedule(RefreshScheduleOptions{
		Schedule: func() string { return "0 4 * * *" },
		Logf:     func(string, ...any) {},
	})

	svc.refresh.start()
	// A second start must be a no-op, not a second goroutine.
	svc.refresh.start()
	svc.refresh.stop()
	svc.refresh.stop()

	// Stopping through the Service is idempotent too.
	svc.Stop()
	svc.Stop()
}

// TestRefreshScheduleRecordsScopeErrors: a database failure must surface as an
// error and land in the stats instead of being swallowed.
func TestRefreshScheduleRecordsScopeErrors(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	svc, st := newRefreshServiceWithSchedule(t, now)
	svc.EnableRefreshSchedule(RefreshScheduleOptions{
		Schedule: func() string { return "0 4 * * *" },
		Logf:     func(string, ...any) {},
	})

	// Closing the store makes every scope query fail.
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := svc.RunRefreshNow(context.Background()); err == nil {
		t.Fatal("RunRefreshNow succeeded against a closed store, want an error")
	}
	_, _, _, lastErr := svc.RefreshScheduleStats()
	if lastErr == "" {
		t.Fatal("stats did not record the failure")
	}
}

// TestRefreshScheduleSurvivesAMissingCensus keeps the documented behaviour: a
// scheduler without a census still refreshes, it just cannot drop stale hashes.
func TestRefreshScheduleSurvivesAMissingCensus(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	svc, st := newRefreshServiceWithSchedule(t, now)
	seedAllThreeConditions(t, st, now)
	svc.EnableRefreshSchedule(RefreshScheduleOptions{
		Schedule: func() string { return "0 4 * * *" },
		Logf:     func(string, ...any) {},
	})

	created, err := svc.RunRefreshNow(context.Background())
	if err != nil {
		t.Fatalf("RunRefreshNow: %v", err)
	}
	if created != 1 {
		t.Fatalf("created = %d, want 1", created)
	}
}
