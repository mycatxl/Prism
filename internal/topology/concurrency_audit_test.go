package topology

import (
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/subscription"
)

func TestRefreshCannotRecreateDeletedSubscriptionNodes(t *testing.T) {
	mgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s", "source", "url", true, false)
	mgr.Register(sub)
	pool := newTestPool(mgr)
	entered, resume := make(chan struct{}), make(chan struct{})
	body := makeSubscriptionJSON(`{"type":"shadowsocks","server":"1.1.1.1","server_port":443}`)
	sched := newTestScheduler(mgr, pool, func(string) ([]byte, error) { close(entered); <-resume; return body, nil })
	done := make(chan struct{})
	go func() { sched.UpdateSubscription(sub); close(done) }()
	<-entered
	sub.WithOpLock(func() { mgr.Unregister(sub.ID) })
	close(resume)
	<-done
	if pool.Size() != 0 || sub.ManagedNodes().Size() != 0 || sub.LastUpdatedNs.Load() != 0 {
		t.Fatal("deleted source recreated nodes or published state")
	}
	sched.Stop()
}

func TestSchedulerStopJoinsManualRefreshAndRejectsNewWork(t *testing.T) {
	mgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s", "source", "url", true, false)
	mgr.Register(sub)
	entered, resume := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	sched := newTestScheduler(mgr, newTestPool(mgr), func(string) ([]byte, error) {
		calls.Add(1)
		close(entered)
		<-resume
		return []byte(`{"outbounds":[]}`), nil
	})
	sched.UpdateSubscriptionAsync(sub)
	<-entered
	stopped := make(chan struct{})
	go func() { sched.Stop(); close(stopped) }()
	select {
	case <-stopped:
		close(resume)
		t.Fatal("Stop returned with refresh still active")
	case <-time.After(20 * time.Millisecond):
	}
	close(resume)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop never joined refresh")
	}
	sched.UpdateSubscription(sub)
	sched.UpdateSubscriptionAsync(sub)
	sched.ForceRefreshAllAsync()
	sched.Start()
	if calls.Load() != 1 || sub.LastCheckedNs.Load() != 0 {
		t.Fatal("stopped scheduler admitted or published work")
	}
}
