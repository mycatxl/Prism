package tenant

import (
	"log"
	"sync"
	"time"
)

// Retention bounds usage and session tables.
type Retention struct {
	HourlyDays int // usage_hourly rows older than this fold into usage_daily
	DailyDays  int // usage_daily rows older than this are deleted
}

// RunMaintenance performs one retention pass at now: hourly → daily rollup,
// daily pruning and expired-session cleanup. Each step is one transaction.
func RunMaintenance(s Store, r Retention, now time.Time) error {
	day := int64(24 * time.Hour / time.Second)
	// Cut on a day boundary so a day is never split across the two tables.
	today := now.Unix() - now.Unix()%day
	rolled, err := s.RollupUsage(today - int64(r.HourlyDays)*day)
	if err != nil {
		return err
	}
	pruned, err := s.PruneUsageDaily(today - int64(r.DailyDays)*day)
	if err != nil {
		return err
	}
	if _, err := s.PruneSessions(now.UnixNano()); err != nil {
		return err
	}
	if rolled > 0 || pruned > 0 {
		log.Printf("tenant: usage maintenance rolled up %d hourly rows, pruned %d daily rows", rolled, pruned)
	}
	return nil
}

// StartMaintenance runs RunMaintenance once now and then every interval
// until the returned stop function is called.
func StartMaintenance(s Store, r Retention, interval time.Duration) func() {
	done := make(chan struct{})
	var once sync.Once
	run := func() {
		if err := RunMaintenance(s, r, time.Now()); err != nil {
			log.Printf("tenant: usage maintenance failed: %v", err)
		}
	}
	go func() {
		run()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				run()
			case <-done:
				return
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

// Runtime bundles what the data plane and API need when multi-tenant mode
// is on.
type Runtime struct {
	Store Store
	Keys  Keys
	Cache *KeyCache
}

// Bootstrap loads the pepper (creating <stateDir>/key_pepper on first run),
// derives subkeys and fills the credential cache from the store.
func Bootstrap(s Store, stateDir string) (*Runtime, error) {
	pepper, err := LoadOrCreatePepper(stateDir)
	if err != nil {
		return nil, err
	}
	keys, err := DeriveKeys(pepper)
	if err != nil {
		return nil, err
	}
	records, err := s.LoadAuthSnapshot()
	if err != nil {
		return nil, err
	}
	cache := NewKeyCache(keys.KeyHMAC)
	if err := cache.Load(records); err != nil {
		return nil, err
	}
	return &Runtime{Store: s, Keys: keys, Cache: cache}, nil
}
