package intel

import (
	"context"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/intel/egress"
	"prism/internal/intel/jobs"
	"prism/internal/intel/store"
	"prism/internal/netutil"
	"prism/internal/node"
)

// egressReuseHash is a canonical 32-character node hash.
const egressReuseHash = "23e5ca7b65ffcc6abedf2c6a0b30e844"

// countingTraceFetcher answers the Cloudflare trace shape and counts calls, so a
// test can tell "the probe ran" from "the observation was reused".
type countingTraceFetcher struct {
	calls atomic.Int32
}

func (f *countingTraceFetcher) FetchWithOptions(_ context.Context, _ node.Hash, _ string, _ netutil.OutboundHTTPOptions) ([]byte, time.Duration, error) {
	f.calls.Add(1)
	return []byte("fl=test\nip=198.51.100.7\nloc=JP\ncolo=NRT\n"), 0, nil
}

// newReuseRunner builds a pipeline runner whose prober reads the given store.
func newReuseRunner(t *testing.T, st *store.Store, now time.Time) (pipelineRunner, *countingTraceFetcher) {
	t.Helper()
	fetcher := &countingTraceFetcher{}
	prober := &egress.Probe{Fetcher: fetcher, Store: st, Now: func() time.Time { return now }}
	return pipelineRunner{prober: prober}, fetcher
}

// ageEgress records one observation `age` older than now.
func ageEgress(t *testing.T, st *store.Store, now time.Time, age time.Duration) {
	t.Helper()
	hash, err := node.ParseHex(egressReuseHash)
	if err != nil {
		t.Fatalf("ParseHex: %v", err)
	}
	if _, _, err := st.RecordEgress(context.Background(), store.EgressObservation{
		NodeHash: hash.String(),
		IPv4:     netip.MustParseAddr("198.51.100.7"),
		Colo:     "NRT",
		Loc:      "JP",
		NowNs:    now.Add(-age).UnixNano(),
	}); err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
}

// TestChecksPipelineReusesFreshEgress pins the §3.2 rule: the checks pipeline's
// step 1 is "出口探测（15 分钟内已探测则跳过）". Only that pipeline may reuse an
// observation; intel, full and egress re-probe because there the address is a
// product of the run. The boundary is exclusive: an observation that is exactly
// 15 minutes old is stale and must be probed again.
func TestChecksPipelineReusesFreshEgress(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		kind      jobs.Kind
		age       time.Duration // < 0 means "record nothing"
		wantProbe bool
		// wantAge is the expected summary age_seconds of a reused observation.
		wantAge int
	}{
		{name: "checks, 5m old", kind: jobs.KindChecks, age: 5 * time.Minute, wantProbe: false, wantAge: 300},
		{name: "checks, 14m59s old", kind: jobs.KindChecks, age: 14*time.Minute + 59*time.Second, wantProbe: false, wantAge: 899},
		{name: "checks, exactly 15m old", kind: jobs.KindChecks, age: 15 * time.Minute, wantProbe: true},
		{name: "checks, 20m old", kind: jobs.KindChecks, age: 20 * time.Minute, wantProbe: true},
		{name: "checks, never probed", kind: jobs.KindChecks, age: -1, wantProbe: true},
		{name: "intel, 5m old", kind: jobs.KindIntel, age: 5 * time.Minute, wantProbe: true},
		{name: "full, 5m old", kind: jobs.KindFull, age: 5 * time.Minute, wantProbe: true},
		{name: "egress, 5m old", kind: jobs.KindEgress, age: 5 * time.Minute, wantProbe: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			runner, fetcher := newReuseRunner(t, st, now)
			if tc.age >= 0 {
				ageEgress(t, st, now, tc.age)
			}

			result := runner.RunStep(context.Background(), tc.kind, jobs.StepEgress, "job-1", egressReuseHash)

			if probed := fetcher.calls.Load() > 0; probed != tc.wantProbe {
				t.Fatalf("probe ran = %v, want %v (summary %v)", probed, tc.wantProbe, result.Summary)
			}
			if result.ErrorCode != "" {
				t.Fatalf("ErrorCode = %q, want empty (summary %v)", result.ErrorCode, result.Summary)
			}
			reused, hasReused := result.Summary["reused"].(bool)
			if tc.wantProbe {
				if hasReused && reused {
					t.Fatalf("reused a stale observation: %v", result.Summary)
				}
				return
			}
			if !hasReused || !reused {
				t.Fatalf("summary carries no reuse flag: %v", result.Summary)
			}
			// A reused step still reports the stored observation, so the item
			// summary the operator reads does not lose the address.
			if got, _ := result.Summary["ipv4"].(string); got != "198.51.100.7" {
				t.Fatalf("summary ipv4 = %q, want the stored address", got)
			}
			if got, _ := result.Summary["colo"].(string); got != "NRT" {
				t.Fatalf("summary colo = %q, want the stored colo", got)
			}
			if got, _ := result.Summary["age_seconds"].(int); got != tc.wantAge {
				t.Fatalf("summary age_seconds = %v, want %d", result.Summary["age_seconds"], tc.wantAge)
			}
		})
	}
}

// TestChecksPipelineReuseDoesNotNotifyOnChange: a reused observation cannot have
// changed, so the §4 OnEgressChange hook must stay quiet.
func TestChecksPipelineReuseDoesNotNotifyOnChange(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	st := newTestStore(t)
	ageEgress(t, st, now, 2*time.Minute)

	var notified atomic.Int32
	runner := pipelineRunner{
		prober:   &egress.Probe{Store: st, Now: func() time.Time { return now }},
		onChange: func([]netip.Addr) { notified.Add(1) },
	}
	result := runner.RunStep(context.Background(), jobs.KindChecks, jobs.StepEgress, "job-1", egressReuseHash)

	if notified.Load() != 0 {
		t.Fatal("OnEgressChange fired for a reused observation")
	}
	if reused, _ := result.Summary["reused"].(bool); !reused {
		t.Fatalf("expected a reuse, got %v", result.Summary)
	}
}

// TestChecksPipelineReuseRequiresAStore: without a store the runner must fall
// through to the prober rather than invent an observation or panic.
func TestChecksPipelineReuseRequiresAStore(t *testing.T) {
	runner := pipelineRunner{prober: &egress.Probe{}}
	result := runner.RunStep(context.Background(), jobs.KindChecks, jobs.StepEgress, "job-1", egressReuseHash)

	if _, ok := result.Summary["reused"]; ok {
		t.Fatalf("reused an observation without a store: %v", result.Summary)
	}
	// The probe then fails cleanly on the missing store, which is the
	// pre-existing behaviour and must not turn into a panic.
	if result.ErrorCode != "EGRESS_PROBE_FAILED" {
		t.Fatalf("ErrorCode = %q, want EGRESS_PROBE_FAILED", result.ErrorCode)
	}
}

// TestChecksPipelineReuseRejectsAFutureObservation: a clock that moved backwards
// (or a corrupted row) must not be treated as "fresh".
func TestChecksPipelineReuseRejectsAFutureObservation(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	st := newTestStore(t)
	ageEgress(t, st, now, -time.Hour) // observed an hour in the future

	runner, fetcher := newReuseRunner(t, st, now)
	result := runner.RunStep(context.Background(), jobs.KindChecks, jobs.StepEgress, "job-1", egressReuseHash)

	if fetcher.calls.Load() == 0 {
		t.Fatalf("a future observation was reused: %v", result.Summary)
	}
}
