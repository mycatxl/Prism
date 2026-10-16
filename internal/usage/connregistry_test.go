package usage

import (
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeConn is a net.Conn that records whether it was closed. The registry only
// ever calls Close, so the rest of the interface can stay unimplemented.
type fakeConn struct {
	net.Conn
	closed atomic.Int32
}

func (c *fakeConn) Close() error {
	c.closed.Add(1)
	return nil
}

func (c *fakeConn) wasClosed() bool { return c.closed.Load() > 0 }

// registryWithCloseSpy returns a registry plus the slice it appends closed
// connections to, so a test can assert which connections were hung up on.
func registryWithCloseSpy() (*ConnRegistry, *[]io.Closer) {
	var closed []io.Closer
	var mu sync.Mutex
	registry := NewConnRegistry()
	registry.closeFn = func(conn io.Closer) error {
		mu.Lock()
		closed = append(closed, conn)
		mu.Unlock()
		return conn.Close()
	}
	return registry, &closed
}

func TestConnRegistry_TrackAndCount(t *testing.T) {
	registry := NewConnRegistry()
	first, second := &fakeConn{}, &fakeConn{}

	registry.Track("k-1", first)
	registry.Track("k-1", second)
	registry.Track("k-2", &fakeConn{})

	if got := registry.Count("k-1"); got != 2 {
		t.Fatalf("k-1 holds %d connections, want 2", got)
	}
	if got := registry.Count("k-2"); got != 1 {
		t.Fatalf("k-2 holds %d connections, want 1", got)
	}
	if got := registry.Count("k-absent"); got != 0 {
		t.Fatalf("an unknown key holds %d connections, want 0", got)
	}

	// Tracking the same connection twice counts once: a caller that registers
	// on both the HTTP and the tunnel path must not inflate the concurrency
	// limit.
	registry.Track("k-1", first)
	if got := registry.Count("k-1"); got != 2 {
		t.Fatalf("k-1 holds %d connections after a repeat Track, want 2", got)
	}
}

func TestConnRegistry_Untrack(t *testing.T) {
	registry := NewConnRegistry()
	conn := &fakeConn{}
	registry.Track("k-1", conn)

	registry.Untrack("k-1", conn)
	if got := registry.Count("k-1"); got != 0 {
		t.Fatalf("k-1 holds %d connections after Untrack, want 0", got)
	}

	// Untracking something that was never tracked is a no-op, which keeps a
	// caller's defer unconditional.
	registry.Untrack("k-1", conn)
	registry.Untrack("k-absent", conn)
	if got := registry.Count("k-1"); got != 0 {
		t.Fatalf("k-1 holds %d connections, want 0", got)
	}
}

// CloseKey closes one key's connections and leaves every other key's alone.
func TestConnRegistry_CloseKeyIsScopedToOneKey(t *testing.T) {
	registry, closed := registryWithCloseSpy()

	firstA, secondA := &fakeConn{}, &fakeConn{}
	firstB := &fakeConn{}
	registry.Track("k-a", firstA)
	registry.Track("k-a", secondA)
	registry.Track("k-b", firstB)

	if got := registry.CloseKey("k-a"); got != 2 {
		t.Fatalf("CloseKey closed %d connections, want 2", got)
	}
	if len(*closed) != 2 {
		t.Fatalf("closed %d connections, want 2", len(*closed))
	}
	if !firstA.wasClosed() || !secondA.wasClosed() {
		t.Fatal("a connection of the closed key was left open")
	}
	if firstB.wasClosed() {
		t.Fatal("closing one key's connections closed another key's")
	}
	if got := registry.Count("k-a"); got != 0 {
		t.Fatalf("k-a still holds %d connections", got)
	}
	if got := registry.Count("k-b"); got != 1 {
		t.Fatalf("k-b holds %d connections, want 1", got)
	}
}

func TestConnRegistry_CloseKeyOnUnknownKeyClosesNothing(t *testing.T) {
	registry, closed := registryWithCloseSpy()
	if got := registry.CloseKey("k-absent"); got != 0 {
		t.Fatalf("CloseKey closed %d connections, want 0", got)
	}
	if len(*closed) != 0 {
		t.Fatalf("closed %d connections, want 0", len(*closed))
	}
}

func TestConnRegistry_CloseAll(t *testing.T) {
	registry, _ := registryWithCloseSpy()
	registry.Track("k-a", &fakeConn{})
	registry.Track("k-a", &fakeConn{})
	registry.Track("k-b", &fakeConn{})

	if got := registry.CloseAll(); got != 3 {
		t.Fatalf("CloseAll closed %d connections, want 3", got)
	}
	if got := registry.Count("k-a"); got != 0 {
		t.Fatalf("k-a still holds %d connections", got)
	}
	if got := registry.Count("k-b"); got != 0 {
		t.Fatalf("k-b still holds %d connections", got)
	}
}

// A key with no connections is not tracked at all: a request that never opened
// a connection must not leave an entry behind.
func TestConnRegistry_EmptyKeyAndNilConnAreIgnored(t *testing.T) {
	registry := NewConnRegistry()

	registry.Track("", &fakeConn{})
	registry.Track("k-1", nil)

	if got := registry.Count(""); got != 0 {
		t.Fatalf("the empty key holds %d connections, want 0", got)
	}
	if got := registry.Count("k-1"); got != 0 {
		t.Fatalf("k-1 holds %d connections, want 0", got)
	}
	if keys := registry.Keys(); len(keys) != 0 {
		t.Fatalf("registry holds %v, want nothing", keys)
	}
}

// A key that loses its last connection must not keep an entry: the registry
// lives for the life of the process, and an entry per key ever used would grow
// without bound.
func TestConnRegistry_EmptySetIsDropped(t *testing.T) {
	registry := NewConnRegistry()
	conn := &fakeConn{}
	registry.Track("k-1", conn)
	if keys := registry.Keys(); len(keys) != 1 {
		t.Fatalf("registry holds %v, want one key", keys)
	}

	registry.Untrack("k-1", conn)
	if keys := registry.Keys(); len(keys) != 0 {
		t.Fatalf("registry still holds %v after the last Untrack", keys)
	}

	// And the key can be tracked again afterwards.
	registry.Track("k-1", conn)
	if got := registry.Count("k-1"); got != 1 {
		t.Fatalf("k-1 holds %d connections after re-tracking, want 1", got)
	}
}

// recordingConn records that the registry closed it. The registry only ever
// calls Close, so this is the whole of the evidence a test needs.
type recordingConn struct {
	name   string
	closed atomic.Bool
}

func (c *recordingConn) Close() error {
	c.closed.Store(true)
	return nil
}

func (c *recordingConn) wasClosed() bool { return c.closed.Load() }

// TestConnRegistry_TrackIsNeverLost is the regression test for the interleaving
// that let a connection escape the registry for the rest of the process's life.
//
// The registry used to read a key's set and then add to it under a second lock
// acquisition. Untrack (when it removed the last connection) and CloseKey could
// remove that set from the map in between, so the connection was written into a
// set nothing could reach again: CloseKey never closed it, CloseAll never
// walked it, Count never saw it. That is exactly the connection the registry
// exists to hang up on when a quota is exhausted or a key is revoked - the
// executor only ever calls CloseKey - and it is also a connection missing from
// the concurrency count.
//
// The test states the invariant the fix has to restore rather than the
// mechanism: every connection handed to Track that its own caller did not
// retire must be closed by a later CloseKey/CloseAll. The storm keeps Track,
// Untrack, CloseKey, CloseAll and Count on the same keys at once, which is what
// opens the window. It is repeated so a single unlucky schedule is not the only
// chance to catch a lost connection.
func TestConnRegistry_TrackIsNeverLost(t *testing.T) {
	const (
		workers = 16
		keys    = 4
		rounds  = 400
		closers = 4
		readers = 2
	)

	registry := NewConnRegistry()

	var keptMu sync.Mutex
	kept := make([]*recordingConn, 0, workers*rounds)
	retired := 0

	var wg sync.WaitGroup
	start := make(chan struct{})

	for w := 0; w < workers; w++ {
		key := fmt.Sprintf("k-%d", w%keys)
		wg.Add(1)
		go func(key string, seed int) {
			defer wg.Done()
			<-start
			for r := 0; r < rounds; r++ {
				conn := &recordingConn{name: fmt.Sprintf("%s-%d", key, r)}
				registry.Track(key, conn)
				if (r+seed)%2 == 0 {
					// Retired by the caller that opened it: nothing is owed
					// for this connection, and the Untrack is the same-key
					// churn that races the other workers' Tracks.
					registry.Untrack(key, conn)
					keptMu.Lock()
					retired++
					keptMu.Unlock()
					continue
				}
				keptMu.Lock()
				kept = append(kept, conn)
				keptMu.Unlock()
			}
		}(key, w)
	}

	// CloseKey is what the quota enforcer calls; CloseAll is the shutdown path.
	for c := 0; c < closers; c++ {
		key := fmt.Sprintf("k-%d", c%keys)
		wg.Add(1)
		go func(key string, id int) {
			defer wg.Done()
			<-start
			for r := 0; r < rounds; r++ {
				if id%2 == 0 {
					registry.CloseKey(key)
					continue
				}
				registry.CloseAll()
			}
		}(key, c)
	}

	// Count is read on every request for the concurrency limit, so it runs
	// against the same churn.
	for r := 0; r < readers; r++ {
		key := fmt.Sprintf("k-%d", r%keys)
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			<-start
			for i := 0; i < rounds; i++ {
				registry.Count(key)
			}
		}(key)
	}

	close(start)
	wg.Wait()

	// The shutdown path must reach everything still tracked.
	registry.CloseAll()

	if retired == 0 || len(kept) == 0 {
		t.Fatalf("the storm retired %d connections and kept %d, want both non-zero", retired, len(kept))
	}
	lost := 0
	for _, conn := range kept {
		if !conn.wasClosed() {
			if lost < 5 {
				t.Errorf("%s was tracked and never closed", conn.name)
			}
			lost++
		}
	}
	if lost > 0 {
		t.Fatalf("%d of %d tracked connections were never closed: Track published them into a set that is no longer in the map, so CloseKey and CloseAll cannot reach them", lost, len(kept))
	}
}

// Count is read on every request to enforce the concurrency limit, so it must
// be exact while connections come and go.
func TestConnRegistry_CountIsExactUnderChurn(t *testing.T) {
	registry := NewConnRegistry()

	const held = 16
	conns := make([]*fakeConn, held)
	for i := range conns {
		conns[i] = &fakeConn{}
		registry.Track("k-1", conns[i])
	}

	var maxSeen atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				if got := int64(registry.Count("k-1")); got > maxSeen.Load() {
					maxSeen.Store(got)
				}
			}
		}()
	}
	wg.Wait()

	if got := maxSeen.Load(); got > held {
		t.Fatalf("Count reported %d connections, want at most %d", got, held)
	}
	if got := registry.Count("k-1"); got != held {
		t.Fatalf("k-1 holds %d connections, want %d", got, held)
	}
}

func TestConnRegistry_NilIsSafe(t *testing.T) {
	var registry *ConnRegistry
	registry.Track("k-1", &fakeConn{})
	registry.Untrack("k-1", &fakeConn{})
	if got := registry.Count("k-1"); got != 0 {
		t.Fatalf("nil registry reported %d connections", got)
	}
	if got := registry.CloseKey("k-1"); got != 0 {
		t.Fatalf("nil registry closed %d connections", got)
	}
	if got := registry.CloseAll(); got != 0 {
		t.Fatalf("nil registry closed %d connections", got)
	}
	if got := registry.Keys(); got != nil {
		t.Fatalf("nil registry reported keys %v", got)
	}
}
