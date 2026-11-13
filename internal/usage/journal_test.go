package usage

import (
	"errors"
	"testing"
	"time"

	"prism/internal/keys"
	"prism/internal/model"
)

type uncertainJournalStore struct {
	rows                   map[string]model.UsageReservation
	bytes                  int64
	unavailable, uncertain bool
}

type signaledFailureStore struct {
	uncertainJournalStore
	failed chan struct{}
}

func (s *signaledFailureStore) SettleUsage(string, int64) (bool, error) {
	close(s.failed)
	return false, errors.New("settlement unavailable")
}

func TestFailedSettlementPublishesLiabilityBeforeReopeningAdmission(t *testing.T) {
	store := &signaledFailureStore{uncertainJournalStore: uncertainJournalStore{rows: map[string]model.UsageReservation{}}, failed: make(chan struct{})}
	journal, err := NewJournal(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	counters := NewCounters()
	gate := &Gate{Limiter: NewLimiter(counters), Journal: journal}
	key := &keys.KeyState{KeyID: "key", UserID: "user", Plan: keys.PlanSnapshot{TrafficBytes: 1000}}
	_, finish := gate.ReserveTraffic(key, "", true, 100)
	// Delay liability publication after the store failure. Admission must stay
	// excluded until that failure is visible to every subsequent reservation.
	journal.mu.Lock()
	done := make(chan struct{})
	go func() { defer close(done); finish(73) }()
	<-store.failed
	admission := make(chan struct{})
	go func() {
		counters.persistenceMu.Lock()
		close(admission)
		counters.persistenceMu.Unlock()
	}()
	select {
	case <-admission:
		t.Error("admission reopened before failed settlement became visible")
	case <-time.After(100 * time.Millisecond):
	}
	journal.mu.Unlock()
	<-done
	<-admission
	if !journal.Blocked(key.UserID) {
		t.Fatal("failed settlement did not close access")
	}
}

func (s *uncertainJournalStore) PendingUsage() ([]model.UsageReservation, error) {
	out := []model.UsageReservation{}
	for _, r := range s.rows {
		out = append(out, r)
	}
	return out, nil
}
func (s *uncertainJournalStore) ReserveUsage(r model.UsageReservation) error {
	s.rows[r.ID] = r
	return nil
}
func (s *uncertainJournalStore) SettleUsage(id string, actual int64) (bool, error) {
	if s.unavailable {
		return false, errors.New("storage unavailable")
	}
	if _, ok := s.rows[id]; !ok {
		return false, nil
	}
	s.bytes += actual
	delete(s.rows, id)
	if s.uncertain {
		s.uncertain = false
		return false, errors.New("commit acknowledgement lost")
	}
	return true, nil
}

func TestJournalRetryCannotDoubleDebitUncertainCommit(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		store := &uncertainJournalStore{rows: map[string]model.UsageReservation{}}
		journal, err := NewJournal(store, nil)
		if err != nil {
			t.Fatal(err)
		}
		counters := NewCounters()
		gate := &Gate{Limiter: NewLimiter(counters), Journal: journal}
		key := &keys.KeyState{KeyID: "key", UserID: "user", Plan: keys.PlanSnapshot{TrafficBytes: 100}}
		n, finish := gate.ReserveTraffic(key, "", true, 100)
		if n != 100 {
			t.Fatal("reservation")
		}
		store.unavailable = !uncertain
		store.uncertain = uncertain
		finish(73)
		if !journal.Blocked("user") {
			t.Fatal("failed completion did not close access")
		}
		if n, _ := gate.ReserveTraffic(key, "", true, 10); n != 0 {
			t.Fatal("failed settlement allowed more bytes")
		}
		store.unavailable = false
		journal.retry()
		journal.retry()
		finish(73)
		if journal.Blocked("user") || store.bytes != 73 || counters.UserBytes("user") != 73 {
			t.Fatal("retry did not reconcile actual bytes exactly once")
		}
		if mark, _ := counters.PersistedBytes("user"); mark != 73 {
			t.Fatal("durable watermark not exact")
		}
		if len(counters.Drain(counters.Keys())) != 0 {
			t.Fatal("settled bytes re-entered async flusher")
		}
	}
}

func TestJournalRetryAfterReactivationDoesNotRefundSettledReservation(t *testing.T) {
	store := &uncertainJournalStore{rows: map[string]model.UsageReservation{}, bytes: 60}
	journal, err := NewJournal(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	counters := NewCounters()
	sub := model.UserSubscription{ID: "a", UserID: "user", PeriodStartNs: 1, PeriodUsedBytes: 60}
	counters.BeginUserPeriodTransition("user").ResumeSubscription(sub, nil)
	gate := &Gate{Limiter: NewLimiter(counters), Journal: journal}
	key := &keys.KeyState{KeyID: "key", UserID: "user", Plan: keys.PlanSnapshot{TrafficBytes: 100}}
	n, finish := gate.ReserveTraffic(key, "", false, 40)
	if n != 40 {
		t.Fatal("reservation failed")
	}
	store.uncertain = true
	finish(10) // Commit succeeds; only its acknowledgement is lost.
	counters.BeginUserPeriodTransition("user").Commit("b", 2)
	sub.PeriodUsedBytes = store.bytes
	counters.BeginUserPeriodTransition("user").ResumeSubscription(sub, nil)
	journal.retry()
	if got := counters.UserBytes("user"); got != 70 || counters.reserved["user"] != 0 {
		t.Fatalf("restored usage=%d, reserved=%d; want 70, 0", got, counters.reserved["user"])
	}
	n, finish = gate.ReserveTraffic(key, "", false, 100)
	if n != 30 {
		t.Fatalf("remaining allowance=%d, want 30", n)
	}
	finish(n)
}

func TestRecoveredLiabilityCannotBeBypassedByNewPeriodOrKey(t *testing.T) {
	store := &uncertainJournalStore{rows: map[string]model.UsageReservation{"pending": {UserID: "user", Bytes: 100}}}
	journal, err := NewJournal(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	counters := NewCounters()
	counters.BeginUserPeriodTransition("user").Commit("replacement", 100)
	gate := &Gate{Limiter: NewLimiter(counters), Journal: journal}
	key := &keys.KeyState{KeyID: "replacement", UserID: "user", Plan: keys.PlanSnapshot{TrafficBytes: 100}}
	if n, _ := gate.ReserveTraffic(key, "", true, 100); n != 0 {
		t.Fatal("replacement bypassed unresolved liability")
	}
	if ok, reason := gate.Check(key, 0); ok || reason != "accounting" {
		t.Fatal("unresolved liability not observable at admission")
	}
}

func TestUnknownCompletionCannotBeRetriedOrRefunded(t *testing.T) {
	store := &uncertainJournalStore{rows: map[string]model.UsageReservation{}}
	journal, err := NewJournal(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	counters := NewCounters()
	gate := &Gate{Limiter: NewLimiter(counters), Journal: journal}
	key := &keys.KeyState{KeyID: "key", UserID: "user", Plan: keys.PlanSnapshot{TrafficBytes: 1000}}
	_, finish := gate.ReserveTraffic(key, "", true, 100)
	finish(UnknownTrafficBytes)
	finish(0) // cleanup cannot invent an actual amount after the unknown result
	journal.retry()
	if len(store.rows) != 1 || store.bytes != 0 || counters.UserBytes(key.UserID) != 100 || !journal.Blocked(key.UserID) {
		t.Fatal("unknown outcome was guessed or its hold was cleared")
	}
	counters.BeginUserPeriodTransition(key.UserID).Commit("new-period", 1)
	key.KeyID = "replacement"
	if n, _ := gate.ReserveTraffic(key, "", true, 1); n != 0 {
		t.Fatal("replacement period or key bypassed unknown live write")
	}
}
