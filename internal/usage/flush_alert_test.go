package usage

import (
	"errors"
	"testing"
)

func TestFlusherReportsFailureWithoutLosingRetryableCounters(t *testing.T) {
	counters := NewCounters()
	store := newFakeFlushStore()
	failure := errors.New("fixture storage failure")
	store.rowErr = failure
	counters.Add(CounterKey{KeyID: "fixture-key"}, Delta{Requests: 2})
	var reports int
	f := &Flusher{Counters: counters, Store: store, OnError: func(err error) {
		if !errors.Is(err, failure) {
			t.Error("observer lost the storage error")
		}
		reports++
	}}
	if _, err := f.FlushOnce(1); err == nil || reports != 1 {
		t.Fatal("failed persistence was not reported")
	}
	store.rowErr = nil
	result, err := f.FlushOnce(2)
	if err != nil || result.Rows != 1 || store.rows[0].Requests != 2 || reports != 1 {
		t.Fatal("reporting changed counter requeue/retry behavior")
	}
}
