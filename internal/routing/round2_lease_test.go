package routing

import (
	"testing"

	"prism/internal/model"
)

func TestBulkLeaseRemovalPreservesTenantOwners(t *testing.T) {
	for _, purge := range []bool{false, true} {
		name := "delete_all"
		if purge {
			name = "purge_platform"
		}
		t.Run(name, func(t *testing.T) {
			router, plat, _ := ownerFixture(t, "203.0.113.1")
			persisted := make(map[model.LeaseKey]bool)
			for _, userID := range []string{"alice", "bob", ""} {
				if _, err := router.RouteRequest(plat.Name, userID, "shared", "example.com"); err != nil {
					t.Fatal(err)
				}
				persisted[model.LeaseKey{PlatformID: plat.ID, UserID: userID, Account: "shared"}] = true
			}
			router.onLeaseEvent = func(e LeaseEvent) {
				if e.Type == LeaseRemove {
					key := model.LeaseKey{PlatformID: e.PlatformID, UserID: e.UserID, Account: e.Account}
					if !persisted[key] {
						t.Errorf("removal did not identify an existing owner: %+v", key)
					}
					delete(persisted, key)
				}
			}
			var removed int
			if purge {
				removed = router.PurgePlatformState(plat.ID)
			} else {
				removed = router.DeleteAllLeases(plat.ID)
			}
			if removed != 3 || len(persisted) != 0 {
				t.Fatalf("removed=%d, surviving persisted owners=%v", removed, persisted)
			}
		})
	}
}
