package requestlog

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"prism/internal/proxy"
)

func TestStopRetriesRecoveredStorageDuringBackoff(t *testing.T) {
	store := &flakyInserter{failuresLeft: 1}
	svc := NewService(ServiceConfig{QueueSize: 8, FlushBatch: 4, FlushInterval: time.Hour})
	svc.insert = store
	svc.retryBackoffMin = time.Hour
	svc.retryBackoffMax = time.Hour
	svc.Start()
	svc.EmitRequestLog(testEntry("/recover"))
	svc.FlushNow()
	svc.Stop()
	attempts, accepted := store.state()
	if attempts != 2 || len(accepted) != 1 || svc.DroppedEntries() != 0 {
		t.Fatalf("attempts=%d accepted=%d dropped=%d", attempts, len(accepted), svc.DroppedEntries())
	}
}

func TestConcurrentRetentionAndReaders(t *testing.T) {
	repo := NewRepo(t.TempDir(), 1, 2)
	if err := repo.Open(); err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	start := make(chan struct{})
	errs := make(chan error, 5)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := range 32 {
			_, err := repo.InsertBatch([]proxy.RequestLogEntry{{ID: fmt.Sprint(i), StartedAtNs: int64(i), TargetHost: "example.com"}})
			if err != nil {
				errs <- err
				return
			}
		}
	}()
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 32 {
				if _, _, _, err := repo.List(ListFilter{Limit: 10}); err != nil {
					errs <- err
					return
				}
				if _, err := repo.GetByID("0"); err != nil {
					errs <- err
					return
				}
				if _, err := repo.GetPayloads("0"); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
