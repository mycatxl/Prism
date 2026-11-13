package usage

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/keys"
	"prism/internal/model"
)

func TestSeedRejectsOldPeriodAndOlderWatermark(t *testing.T) {
	c := NewCounters()
	old := model.UserSubscription{ID: "old", UserID: "u", Status: model.SubscriptionStatusActive, PeriodStartNs: 1, PeriodUsedBytes: 100}
	c.SeedFromSubscriptions([]model.UserSubscription{old}, 0)
	c.BeginUserPeriodTransition("u").Commit("new", 2)
	c.Add(CounterKey{KeyID: "key", UserID: "u"}, Delta{IngressBytes: 7})
	c.SeedFromSubscriptions([]model.UserSubscription{old}, 0)
	if got := c.UserBytes("u"); got != 7 {
		t.Fatalf("old seed changed current usage: %d", got)
	}
	current := old
	current.ID, current.PeriodStartNs, current.PeriodUsedBytes = "new", 2, 7
	c.MarkPersisted("u", 7)
	current.PeriodUsedBytes = 3
	c.SeedFromSubscriptions([]model.UserSubscription{current}, 0)
	current.PeriodUsedBytes = 7
	c.SeedFromSubscriptions([]model.UserSubscription{current}, 0)
	if got := c.UserBytes("u"); got != 7 {
		t.Fatalf("older watermark double counted bytes: %d", got)
	}
}

func TestPeriodSweepCannotReplaceNewAccountingWindow(t *testing.T) {
	c := NewCounters()
	old := activePeriodSub("old", "u", "p", 1)
	old.PlanSnapshotJSON = `{"period":"day"}`
	store := newFakePeriodStore()
	store.subs = []model.UserSubscription{old}
	c.BeginUserPeriodTransition("u").Commit("new", 2)
	c.Add(CounterKey{KeyID: "k", UserID: "u"}, Delta{IngressBytes: 7})
	result, err := (&PeriodReset{Counters: c, Store: store}).RunOnce(int64(48 * time.Hour))
	if err != nil || result.Rolled != 0 || c.UserBytes("u") != 7 {
		t.Fatalf("result=%+v usage=%d err=%v", result, c.UserBytes("u"), err)
	}
	if len(store.resets()) != 0 {
		t.Fatal("stale reset reached persistence")
	}
}

func TestTenantTransitionDoesNotBlockOtherTenants(t *testing.T) {
	c := NewCounters()
	transition := c.BeginUserPeriodTransition("blocked")
	defer transition.Cancel()
	done := make(chan struct{})
	go func() { c.Add(CounterKey{KeyID: "key", UserID: "other"}, Delta{IngressBytes: 7}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unrelated tenant blocked by database transition")
	}
	if got := c.UserBytes("other"); got != 7 {
		t.Fatalf("usage=%d", got)
	}
}

func TestCountersReleaseKeyAndUserMetadataUnderChurn(t *testing.T) {
	c := NewCounters()
	var wg sync.WaitGroup
	for worker := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range 64 {
				id := fmt.Sprintf("%d-%d", worker, n)
				c.Add(CounterKey{KeyID: id, UserID: id}, Delta{IngressBytes: 3, Requests: 1})
				c.Drain(c.Keys())
				c.ForgetUser(id)
			}
		}()
	}
	wg.Wait()
	c.Drain(c.Keys())
	if c.keyBytes.Size() != 0 || c.users.Size() != 0 || len(c.periods) != 0 || len(c.transitions) != 0 {
		t.Fatalf("retained key=%d user=%d periods=%d locks=%d", c.keyBytes.Size(), c.users.Size(), len(c.periods), len(c.transitions))
	}
}

func TestReservationRefreshesReplacedPlanAndCannotReviveDeletedUser(t *testing.T) {
	c := NewCounters()
	store := keys.NewStore()
	old := &keys.KeyState{KeyID: "k", UserID: "u", Status: model.KeyStatusActive, UserStatus: model.UserStatusActive, Plan: keys.PlanSnapshot{TrafficBytes: 100}}
	store.Upsert(old)
	gate := &Gate{Limiter: NewLimiter(c), Keys: store}
	small := *old
	small.Plan.TrafficBytes = 10
	small.Plan.SubscriptionID = "new"
	c.BeginUserPeriodTransition("u").Commit("new", 2)
	if n, _ := gate.ReserveTraffic(old, "p", true, 100); n != 0 {
		t.Fatal("publication gap admitted old plan")
	}
	store.Upsert(&small)
	n, finish := gate.ReserveTraffic(old, "p", true, 100)
	if n != 10 {
		t.Fatalf("old stream reserved %d under new 10 byte plan", n)
	}
	store.Delete(old.KeyID)
	c.ForgetUser("u")
	finish(7)
	if _, present := c.UserBytesPresent("u"); present {
		t.Fatal("late settlement recreated deleted user")
	}
	if n, _ := gate.ReserveTraffic(old, "p", true, 1); n != 0 {
		t.Fatal("revoked key reserved more bytes")
	}
}

func TestConcurrentReservationsRolloverAndDrainConserveBytes(t *testing.T) {
	c := NewCounters()
	g := &Gate{Limiter: NewLimiter(c)}
	var delivered atomic.Int64
	var persisted atomic.Int64
	stop := make(chan struct{})
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		for {
			select {
			case <-stop:
				return
			default:
			}
			batch := c.Drain(c.Keys())
			// Include a failed persistence attempt; retrying must not bill twice.
			c.Requeue(batch)
			for _, delta := range c.Drain(c.Keys()) {
				persisted.Add(delta.Total())
			}
		}
	}()
	var wg sync.WaitGroup
	for tenant := range 8 {
		user := fmt.Sprintf("u-%d", tenant)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for generation := range 30 {
				c.BeginUserPeriodTransition(user).Commit("sub", int64(generation+1))
			}
		}()
		for stream := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				key := &keys.KeyState{KeyID: fmt.Sprintf("%s-%d", user, stream), UserID: user, Plan: keys.PlanSnapshot{TrafficBytes: 100000}}
				for range 100 {
					n, finish := g.ReserveTraffic(key, "platform", true, 7)
					finish(n / 2)
					delivered.Add(n / 2)
				}
			}()
		}
	}
	wg.Wait()
	close(stop)
	<-flushed
	for _, delta := range c.Drain(c.Keys()) {
		persisted.Add(delta.Total())
	}
	if got, want := persisted.Load(), delivered.Load(); got != want || want != 9600 {
		t.Fatalf("persisted=%d delivered=%d want=9600", got, want)
	}
	if c.keyBytes.Size() != 0 || len(c.transitions) != 0 {
		t.Fatal("drained workload retained keys or transition locks")
	}
}

type auditCommitBarrierStore struct {
	*fakeFlushStore
	committed chan struct{}
	resume    chan struct{}
}

func (s *auditCommitBarrierStore) FlushUsageHourlyAndCharge(_ []model.UsageRow, charges []UsageCharge) (map[string]UsageChargeResult, error) {
	result := make(map[string]UsageChargeResult)
	for _, charge := range charges {
		result[charge.Key] = UsageChargeResult{Charged: true}
	}
	close(s.committed)
	<-s.resume
	return result, nil
}

func TestSeedWaitsForCommittedFlushWatermark(t *testing.T) {
	c := NewCounters()
	sub := model.UserSubscription{ID: "s", UserID: "u", PeriodStartNs: 1, Status: model.SubscriptionStatusActive}
	c.SeedFromSubscriptions([]model.UserSubscription{sub}, 0)
	c.Add(CounterKey{KeyID: "k", UserID: "u"}, Delta{IngressBytes: 100})
	store := &auditCommitBarrierStore{fakeFlushStore: newFakeFlushStore(), committed: make(chan struct{}), resume: make(chan struct{})}
	flushed := make(chan error, 1)
	go func() { _, err := (&Flusher{Counters: c, Store: store}).FlushOnce(2); flushed <- err }()
	<-store.committed
	sub.PeriodUsedBytes = 100 // the database snapshot now includes this flush
	seeded := make(chan struct{})
	go func() { c.SeedFromSubscriptions([]model.UserSubscription{sub}, 0); close(seeded) }()
	select {
	case <-seeded:
		close(store.resume)
		<-flushed
		t.Fatal("seed crossed unacknowledged commit")
	case <-time.After(20 * time.Millisecond):
	}
	close(store.resume)
	if err := <-flushed; err != nil {
		t.Fatal(err)
	}
	<-seeded
	mark, _ := c.PersistedBytes("u")
	if c.UserBytes("u") != 100 || mark != 100 {
		t.Fatalf("usage=%d watermark=%d", c.UserBytes("u"), mark)
	}
}

func TestExistingStreamChecksIncreasedAllowance(t *testing.T) {
	c := NewCounters()
	store := keys.NewStore()
	old := &keys.KeyState{KeyID: "k", UserID: "u", Status: model.KeyStatusActive, UserStatus: model.UserStatusActive, Plan: keys.PlanSnapshot{TrafficBytes: 100}}
	current := *old
	current.Plan.TrafficBytes = 1000
	store.Upsert(&current)
	c.Add(CounterKey{KeyID: "k", UserID: "u"}, Delta{IngressBytes: 150})
	g := &Gate{Limiter: NewLimiter(c), Keys: store}
	if g.Exceeded(old, 0) {
		t.Fatal("existing stream cut using superseded allowance")
	}
	if allowed, reason := g.Check(old, 0); !allowed {
		t.Fatalf("stale authentication denied: %s", reason)
	}
}
