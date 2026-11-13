package usage

import (
	"testing"

	"prism/internal/keys"
	"prism/internal/model"
)

func TestRevokedSnapshotCannotResumeAfterRepublication(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "credentials", true: "disable-reenable"}[remove], func(t *testing.T) {
			store := keys.NewStore()
			c := NewCounters()
			g := &Gate{Keys: store, Limiter: NewLimiter(c)}
			key := &keys.KeyState{KeyID: "a", UserID: "alice", Status: model.KeyStatusActive, UserStatus: model.UserStatusActive, Plan: keys.PlanSnapshot{TrafficBytes: 10}}
			store.Upsert(key)
			old, _ := store.Get(key.KeyID)
			other := *key
			other.KeyID, other.UserID = "b", "bob"
			store.Upsert(&other)
			bob, _ := store.Get(other.KeyID)
			if remove {
				store.Delete(key.KeyID)
			} else {
				store.RevokeSessions(key.KeyID)
			}
			store.Upsert(key)
			if ok, _ := g.Check(old, 0); ok {
				t.Fatal("late registration reused revoked authentication")
			}
			if !g.Exceeded(old, 0) {
				t.Fatal("existing stream ignored revoked generation")
			}
			if n, _ := g.ReserveTraffic(old, "p", false, 10); n != 0 {
				t.Fatal("revoked stream consumed quota")
			}
			fresh, _ := store.Get(key.KeyID)
			for _, current := range []*keys.KeyState{fresh, bob} {
				n, finish := g.ReserveTraffic(current, "p", false, 10)
				if n != 10 {
					t.Fatalf("current key %s refused: %d", current.KeyID, n)
				}
				finish(n)
			}
			if c.UserBytes("alice") != 10 || c.UserBytes("bob") != 10 {
				t.Fatal("revocation crossed tenant accounting boundary")
			}
		})
	}
}
