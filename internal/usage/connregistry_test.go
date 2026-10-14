package usage

import (
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

// Registration happens once per connection, but closing happens concurrently
// with new requests, so the two must not lose each other.
func TestConnRegistry_ConcurrentTrackAndClose(t *testing.T) {
	registry := NewConnRegistry()

	const goroutines = 32
	const perGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				conn := &fakeConn{}
				registry.Track("k-1", conn)
				registry.Untrack("k-1", conn)
			}
		}()
	}

	// A closer runs alongside: it must never panic and must always leave the
	// registry consistent.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			registry.CloseKey("k-1")
		}
	}()

	wg.Wait()

	// Everything was untracked or closed, so nothing is left.
	if got := registry.Count("k-1"); got != 0 {
		t.Fatalf("k-1 holds %d connections, want 0", got)
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
