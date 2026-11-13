package probe

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"prism/internal/netutil"
	"prism/internal/node"
)

func TestRound3MalformedEgressCannotRecoverNode(t *testing.T) {
	pool, hash, entry := newProbeFailureTestPool(t, `{"type":"malformed-egress"}`)
	pool.RecordResult(hash, true)
	mgr := NewProbeManager(ProbeConfig{Pool: pool, Fetcher: func(node.Hash, string) ([]byte, time.Duration, error) {
		return []byte("<html>bad gateway</html>"), time.Millisecond, nil
	}})
	for i := 1; i <= 4; i++ {
		mgr.probeEgress(hash, entry)
		if entry.FailureCount.Load() != int32(i) {
			t.Fatalf("attempt %d failures=%d", i, entry.FailureCount.Load())
		}
		if i >= 3 && entry.CircuitOpenSince.Load() == 0 {
			t.Fatal("malformed response recovered unhealthy node")
		}
	}
	mgr.fetcher = func(node.Hash, string) ([]byte, time.Duration, error) {
		return []byte("ip=203.0.113.9\nloc=US\n"), time.Millisecond, nil
	}
	mgr.probeEgress(hash, entry)
	if entry.CircuitOpenSince.Load() != 0 || entry.FailureCount.Load() != 0 {
		t.Fatal("valid response failed to recover node")
	}
}

func TestRound3ProbeWorkerSurvivesTaskPanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, hash, _ := newProbeFailureTestPool(t, `{"type":"panic-once"}`)
		var calls atomic.Int32
		mgr := NewProbeManager(ProbeConfig{Pool: pool, Concurrency: 1, Fetcher: func(node.Hash, string) ([]byte, time.Duration, error) {
			if calls.Add(1) == 1 {
				panic("injected provider panic")
			}
			return []byte("ip=203.0.113.9\nloc=US\n"), time.Millisecond, nil
		}})
		mgr.wg.Add(1)
		go func() { defer mgr.wg.Done(); mgr.runProbeWorker() }()
		defer mgr.Stop()
		mgr.TriggerImmediateEgressProbe(hash)
		synctest.Wait()
		mgr.TriggerImmediateEgressProbe(hash)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatalf("worker did not recover: calls=%d", calls.Load())
		}
		if state, ok := mgr.taskStates.Load(probeTaskKey{hash: hash, kind: probeTaskKindEgress}); ok && state.flags.Load() != 0 {
			t.Fatalf("task stranded: flags=%d", state.flags.Load())
		}
	})
}

func TestRound3DirectProbeBodyBound(t *testing.T) {
	for _, size := range []int64{netutil.ProbeResponseMaxBytes, netutil.ProbeResponseMaxBytes + 1} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", int(size)))) }))
		fetch := DirectFetcher(func() time.Duration { return time.Second })
		body, _, err := fetch(node.Zero, server.URL)
		server.Close()
		if size > netutil.ProbeResponseMaxBytes {
			if err == nil || body != nil {
				t.Fatal("oversized probe body accepted")
			}
		} else if err != nil || int64(len(body)) != size {
			t.Fatalf("exact limit: len=%d err=%v", len(body), err)
		}
	}
}

func TestRound3ProbeStatusErrorsCannotRecoverNode(t *testing.T) {
	for _, status := range []int{200, 204, 429, 502, 503} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		pool, hash, entry := newProbeFailureTestPool(t, `{"type":"status-recovery"}`)
		for range 3 {
			pool.RecordResult(hash, false)
		}
		mgr := NewProbeManager(ProbeConfig{Pool: pool, Fetcher: DirectFetcher(func() time.Duration { return time.Second })})
		err := mgr.performLatencyProbe(hash, server.URL)
		server.Close()
		if status < 300 {
			if err != nil || entry.CircuitOpenSince.Load() != 0 {
				t.Fatalf("2xx did not recover node: %d %v", status, err)
			}
		} else if err == nil || entry.CircuitOpenSince.Load() == 0 {
			t.Fatalf("HTTP error recovered node: %d %v", status, err)
		}
	}
}

func TestRound3PeriodicProbeRetriesUnusableNodesPromptly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, hash, entry := newProbeFailureTestPool(t, `{"type":"automatic-recovery"}`)
		entry.LastEgressUpdateAttempt.Store(time.Now().UnixNano())
		entry.LastLatencyProbeAttempt.Store(time.Now().UnixNano())
		for range 3 {
			pool.RecordResult(hash, false)
		}
		var calls atomic.Int32
		mgr := NewProbeManager(ProbeConfig{Pool: pool, Concurrency: 1, Fetcher: func(node.Hash, string) ([]byte, time.Duration, error) {
			calls.Add(1)
			return []byte("ip=203.0.113.9\nloc=US\n"), time.Millisecond, nil
		}})
		mgr.wg.Add(1)
		go func() { defer mgr.wg.Done(); mgr.runProbeWorker() }()
		defer mgr.Stop()
		mgr.scanEgress()
		synctest.Wait()
		if calls.Load() != 0 {
			t.Fatal("recovery probes spun without a backoff")
		}
		time.Sleep(time.Minute)
		mgr.scanEgress()
		synctest.Wait()
		if calls.Load() != 1 || !entry.GetEgressIP().IsValid() || entry.CircuitOpenSince.Load() != 0 {
			t.Fatalf("periodic recovery failed: calls=%d egress=%v open=%v", calls.Load(), entry.GetEgressIP(), entry.CircuitOpenSince.Load() != 0)
		}
		// A healthy node returns to its normal sampling interval.
		time.Sleep(time.Minute)
		mgr.scanEgress()
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("healthy node retained recovery probe rate")
		}
	})
}
