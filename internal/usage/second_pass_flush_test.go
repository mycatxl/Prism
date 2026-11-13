package usage

import (
	"prism/internal/model"
	"testing"
)

type rotatingFlushStore struct {
	*fakeFlushStore
	counters *Counters
}

func (s *rotatingFlushStore) FlushUsageHourlyAndCharge(rows []model.UsageRow, charges []UsageCharge) (map[string]UsageChargeResult, error) {
	if err := s.UpsertUsageHourly(rows); err != nil {
		return nil, err
	}
	result := make(map[string]UsageChargeResult)
	for _, charge := range charges {
		result[charge.Key] = UsageChargeResult{Charged: true}
		// The old write succeeds, then a new period begins before FlushOnce
		// receives its result and advances the persistence watermark.
		s.counters.ResetUser(charge.UserID)
	}
	return result, nil
}

func TestCompletedFlushDoesNotAdvanceNextPeriodWatermark(t *testing.T) {
	c := NewCounters()
	store := &rotatingFlushStore{newFakeFlushStore(), c}
	store.subs["user"] = &model.UserSubscription{ID: "subscription", UserID: "user", PeriodStartNs: 1}
	c.Add(CounterKey{KeyID: "key", UserID: "user"}, Delta{IngressBytes: 10})
	flusher := &Flusher{Counters: c, Store: store}
	if result, err := flusher.FlushOnce(2); err != nil || result.Subscriptions != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if watermark, _ := c.PersistedBytes("user"); watermark != 0 {
		t.Fatalf("new period watermark=%d", watermark)
	}
	if total := c.UserBytes("user"); total != 0 {
		t.Fatalf("new period total=%d", total)
	}
}
