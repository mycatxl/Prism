package keys

import (
	"testing"
	"time"
)

func TestRangeCallbackDoesNotHoldReplacementLock(t *testing.T) {
	store := NewStore()
	store.Upsert(&KeyState{KeyID: "key"})
	done := make(chan struct{})
	go func() {
		store.Range(func(state *KeyState) bool {
			loaded := make(chan struct{})
			go func() { _ = store.Load(nil, nil, nil); close(loaded) }()
			<-loaded
			store.Delete(state.KeyID)
			return true
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Range callback deadlocked with Load")
	}
}
