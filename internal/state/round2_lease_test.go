package state

import (
	"testing"

	"prism/internal/model"
	"prism/internal/routing"
)

func TestBulkLeaseDeletionSurvivesRestart(t *testing.T) {
	for _, purge := range []bool{false, true} {
		name := "delete_all"
		if purge {
			name = "purge_platform"
		}
		t.Run(name, func(t *testing.T) {
			stateDir, cacheDir := t.TempDir(), t.TempDir()
			engine, closer, err := PersistenceBootstrap(stateDir, cacheDir)
			if err != nil {
				t.Fatal(err)
			}
			defer closer.Close()
			rows := []model.Lease{
				{PlatformID: "platform", UserID: "alice", Account: "shared", NodeHash: "11111111111111111111111111111111", EgressIP: "203.0.113.1"},
				{PlatformID: "platform", UserID: "bob", Account: "shared", NodeHash: "11111111111111111111111111111111", EgressIP: "203.0.113.1"},
			}
			if err := engine.BulkUpsertLeases(rows); err != nil {
				t.Fatal(err)
			}
			router := routing.NewRouter(routing.RouterConfig{OnLeaseEvent: func(e routing.LeaseEvent) {
				if e.Type == routing.LeaseRemove {
					engine.MarkLeaseDelete(e.PlatformID, e.UserID, e.Account)
				}
			}})
			router.RestoreLeases(rows)
			removed := 0
			if purge {
				removed = router.PurgePlatformState("platform")
			} else {
				removed = router.DeleteAllLeases("platform")
			}
			if removed != len(rows) {
				t.Fatalf("removed=%d, want %d", removed, len(rows))
			}
			if err := engine.FlushDirtySets(CacheReaders{}); err != nil {
				t.Fatal(err)
			}
			remaining, err := engine.LoadAllLeases()
			if err != nil || len(remaining) != 0 {
				t.Fatalf("persisted leases survived removal: %v, err=%v", remaining, err)
			}
			if err := closer.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, reopened, err := PersistenceBootstrap(stateDir, cacheDir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			remaining, err = restarted.LoadAllLeases()
			if err != nil || len(remaining) != 0 {
				t.Fatalf("restart restored removed tenant leases: %v, err=%v", remaining, err)
			}
		})
	}
}
