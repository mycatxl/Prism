package main

import (
	"context"
	"errors"
	"net"
	"runtime"
	"testing"
	"testing/synctest"
	"time"

	"prism/internal/model"
	"prism/internal/usage"
)

func TestConnChannelListenerBurstWakesConsumersAndProducers(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	synctest.Test(t, func(t *testing.T) {
		l := newConnChannelListener()
		defer l.Close()
		for range connChannelListenerCapacity {
			if err := l.Enqueue(nil); err != nil {
				t.Fatal(err)
			}
		}
		for range 3 * connChannelListenerCapacity {
			go func() { _ = l.Enqueue(nil) }()
		}
		synctest.Wait() // all producers are waiting on a full queue
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range 4 * connChannelListenerCapacity {
				if _, err := l.Accept(); err != nil {
					return
				}
			}
		}()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("consumer asleep with queued connections; producer/consumer signals were mixed")
		}
	})
}

func TestDemuxShutdownDeadlineRejectsDelayedAcceptedWorker(t *testing.T) {
	demux := &inboundDemuxServer{}
	if !demux.tryStartConnWorker() {
		t.Fatal("worker refused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	returned := make(chan error, 1)
	go func() { returned <- demux.Shutdown(ctx) }()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown=%v", err)
		}
	case <-time.After(time.Second):
		demux.workerWG.Done()
		t.Fatal("shutdown ignored deadline while waiting for unscheduled worker")
	}
	conn, peer := net.Pipe()
	defer peer.Close()
	defer conn.Close()
	done := make(chan struct{})
	go func() { demux.handleAcceptedConn(conn); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("late accepted socket entered sniff after shutdown")
	}
	if len(demux.activeConns) != 0 || len(demux.sniffConns) != 0 {
		t.Fatal("late worker retained socket")
	}
}

type reseedAuditStore struct {
	sub     model.UserSubscription
	charges []model.UsageCharge
}

func (s *reseedAuditStore) GetActiveUserSubscription(string) (*model.UserSubscription, error) {
	return &s.sub, nil
}
func (s *reseedAuditStore) UpsertUsageHourly([]model.UsageRow) error     { return nil }
func (s *reseedAuditStore) AddSubscriptionUsedBytes(string, int64) error { return nil }
func (s *reseedAuditStore) FlushUsageHourlyAndCharge(_ []model.UsageRow, charges []model.UsageCharge) (map[string]model.UsageChargeResult, error) {
	s.charges = charges
	return map[string]model.UsageChargeResult{}, nil
}

func TestReseedBindsZeroUsageBeforeAllowingTraffic(t *testing.T) {
	c := usage.NewCounters()
	c.RequireSeed()
	old := model.UserSubscription{ID: "old", UserID: "u", PeriodStartNs: 1, Status: model.SubscriptionStatusActive}
	source := &fakeUsageSeedSource{failures: 1, subs: []model.UserSubscription{old}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reseedUsageUntilApplied(ctx, c, source, time.Millisecond, nil)
	if !c.SeedReady() {
		t.Fatal("recovery never became ready")
	}
	c.Add(usage.CounterKey{KeyID: "k", UserID: "u"}, usage.Delta{IngressBytes: 7})
	c.BeginUserPeriodTransition("u").Commit("new", 2)
	store := &reseedAuditStore{sub: model.UserSubscription{ID: "new", UserID: "u", PeriodStartNs: 2}}
	if _, err := (&usage.Flusher{Counters: c, Store: store}).FlushOnce(3); err != nil {
		t.Fatal(err)
	}
	if len(store.charges) != 1 || store.charges[0].SubscriptionID != "old" || store.charges[0].PeriodStartNs != 1 {
		t.Fatalf("old traffic rebound to new subscription: %+v", store.charges)
	}
}
