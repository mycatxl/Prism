package topology

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/subscription"
)

// A refresh attempt writes the parse report (the grouped summary the
// subscription API renders, plus the persisted JSON). That write used to happen
// right after ParseWithReport returned — before WithOpLock and before either
// stale guard — so an attempt that was about to be discarded as stale still
// overwrote the summary of the attempt that actually landed.
//
// The observable damage is a report that describes a different node set than
// the one in the pool: the panel says "imported 100 nodes" while the pool holds
// the newer attempt's 80. The fix moves the report write inside WithOpLock and
// after both guards, so only the attempt that lands writes its own report.

func TestScheduler_StaleSuccessDoesNotOverwriteParseSummary(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "http://example.com", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)

	// The two attempts parse documents that differ in node count, so the summary
	// each one produces is distinguishable.
	staleBody := makeSubscriptionJSON(
		`{"type":"shadowsocks","tag":"stale-1","server":"1.1.1.1","server_port":443}`,
		`{"type":"shadowsocks","tag":"stale-2","server":"1.1.1.2","server_port":443}`,
		`{"type":"shadowsocks","tag":"stale-3","server":"1.1.1.3","server_port":443}`,
	)
	freshBody := makeSubscriptionJSON(
		`{"type":"vmess","tag":"fresh-1","server":"2.2.2.2","server_port":443}`,
	)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	fetcher := func(string) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
			return staleBody, nil
		}
		return freshBody, nil
	}

	// Capture every summary the scheduler writes, in order.
	var mu sync.Mutex
	var written []int
	sched := NewSubscriptionScheduler(SchedulerConfig{
		SubManager: subMgr,
		Pool:       pool,
		Fetcher:    fetcher,
		OnParseReport: func(string, string) {
			mu.Lock()
			defer mu.Unlock()
			written = append(written, sub.ParseSummary().Imported)
		},
	})

	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		sched.UpdateSubscription(sub) // older attempt: blocked, then stale
	}()

	<-firstStarted

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		sched.UpdateSubscription(sub) // newer attempt: lands first
	}()
	<-done2

	close(releaseFirst)
	<-done1

	// The pool holds the fresh attempt's single node.
	if got := pool.Size(); got != 1 {
		t.Fatalf("pool size = %d, want 1 (the newer attempt's node)", got)
	}

	summary := sub.ParseSummary()
	if summary == nil {
		t.Fatal("no parse summary was stored")
	}
	if summary.Imported != 1 {
		t.Fatalf("parse summary reports imported=%d, want 1: the summary must describe the attempt that landed, not the stale one",
			summary.Imported)
	}

	// The stale attempt must not have contributed a report at all.
	mu.Lock()
	defer mu.Unlock()
	for _, imported := range written {
		if imported == 3 {
			t.Fatalf("the stale attempt wrote its report (imported=3) over the applied one; reports written: %v", written)
		}
	}
}

// The fetch-failure path already guards its write; this pins that the guard and
// the report write stay on the same side of the lock boundary, so a failure
// arriving after a newer success cannot rewrite the summary either.
func TestScheduler_StaleParseFailureDoesNotOverwriteParseSummary(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "http://example.com", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)

	staleBody := makeSubscriptionJSON(
		`{"type":"shadowsocks","tag":"stale-1","server":"1.1.1.1","server_port":443}`,
		`{"type":"shadowsocks","tag":"stale-2","server":"1.1.1.2","server_port":443}`,
	)
	freshBody := makeSubscriptionJSON(
		`{"type":"vmess","tag":"fresh-1","server":"2.2.2.2","server_port":443}`,
	)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	fetcher := func(string) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
			return staleBody, nil
		}
		return freshBody, nil
	}
	sched := newTestScheduler(subMgr, pool, fetcher)

	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		sched.UpdateSubscription(sub)
	}()
	<-firstStarted

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		sched.UpdateSubscription(sub)
	}()
	<-done2
	close(releaseFirst)
	<-done1

	if summary := sub.ParseSummary(); summary == nil || summary.Imported != 1 {
		t.Fatalf("parse summary = %+v, want the applied attempt's (imported=1)", summary)
	}
}

// The applied attempt must still write its report: the guard must not turn into
// "never write". This is the reverse control for the two tests above.
func TestScheduler_AppliedAttemptStillWritesParseSummary(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "http://example.com", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	body := makeSubscriptionJSON(
		`{"type":"vmess","tag":"only-1","server":"2.2.2.2","server_port":443}`,
	)

	reported := make(chan int, 4)
	sched := NewSubscriptionScheduler(SchedulerConfig{
		SubManager: subMgr,
		Pool:       pool,
		Fetcher:    makeMockFetcher(body, nil),
		OnParseReport: func(string, string) {
			select {
			case reported <- sub.ParseSummary().Imported:
			default:
			}
		},
	})

	sched.UpdateSubscription(sub)

	select {
	case imported := <-reported:
		if imported != 1 {
			t.Fatalf("reported imported=%d, want 1", imported)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the applied attempt wrote no parse report")
	}
}
