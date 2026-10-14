package usage

import (
	"net"
	"sync"

	"github.com/puzpuzpuz/xsync/v4"
)

// ConnRegistry tracks which connections belong to which access key.
//
// It exists for two things a per-byte counter cannot do on its own:
//
//   - closing a key's live connections the moment its quota is exceeded, its
//     user is disabled or its plan expires, rather than waiting for the next
//     request;
//   - answering "how many connections does this key hold", which is the
//     concurrency limit.
//
// Counting the bytes is not enough to disconnect anyone: the counter knows a
// limit was crossed but not who to hang up on, and the connection that crossed
// it is already mid-transfer. The registry is what turns a number into an
// action.
//
// The map is keyed by key id, so closing one key's connections never touches
// another's. Registration happens once per connection, not per byte, so it does
// not sit on the copy path.
type ConnRegistry struct {
	// conns maps key id -> set of that key's connections.
	conns *xsync.Map[string, *connSet]
	// closeFn closes one connection. It is a field so a test can observe the
	// calls without opening real sockets; production passes the identity.
	closeFn func(net.Conn) error
}

// connSet is one key's live connections, guarded by its own lock.
//
// A per-key lock rather than one global lock: a busy key must not block
// registration for every other key, which is the same reasoning that keeps the
// counters lock-free.
type connSet struct {
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

// NewConnRegistry returns an empty registry that closes connections by calling
// Close on them.
func NewConnRegistry() *ConnRegistry {
	return &ConnRegistry{
		conns:   xsync.NewMap[string, *connSet](),
		closeFn: func(conn net.Conn) error { return conn.Close() },
	}
}

// Track registers one connection for a key.
//
// A nil connection, or an empty key id, is ignored: the single-tenant path has
// no key to attribute the connection to, and it is never force-closed by a
// quota.
func (r *ConnRegistry) Track(keyID string, conn net.Conn) {
	if r == nil || keyID == "" || conn == nil {
		return
	}
	set, _ := r.conns.LoadOrCompute(keyID, func() (*connSet, bool) {
		return &connSet{conns: make(map[net.Conn]struct{})}, false
	})
	set.mu.Lock()
	set.conns[conn] = struct{}{}
	set.mu.Unlock()
}

// Untrack removes one connection. It is safe to call for a connection that was
// never tracked, which keeps the caller's defer simple.
func (r *ConnRegistry) Untrack(keyID string, conn net.Conn) {
	if r == nil || keyID == "" || conn == nil {
		return
	}
	set, ok := r.conns.Load(keyID)
	if !ok || set == nil {
		return
	}
	set.mu.Lock()
	delete(set.conns, conn)
	empty := len(set.conns) == 0
	set.mu.Unlock()
	if empty {
		// Drop the empty set so a key that stops being used does not leave an
		// entry behind for the life of the process. Compute with a predicate
		// keeps a concurrent Track from being lost: if the set gained a
		// connection in between, the delete is cancelled.
		r.conns.Compute(keyID, func(current *connSet, loaded bool) (*connSet, xsync.ComputeOp) {
			if !loaded || current == nil {
				return nil, xsync.CancelOp
			}
			current.mu.Lock()
			defer current.mu.Unlock()
			if len(current.conns) > 0 {
				return current, xsync.CancelOp
			}
			return nil, xsync.DeleteOp
		})
	}
}

// Count reports how many connections one key currently holds.
func (r *ConnRegistry) Count(keyID string) int {
	if r == nil || keyID == "" {
		return 0
	}
	set, ok := r.conns.Load(keyID)
	if !ok || set == nil {
		return 0
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	return len(set.conns)
}

// CloseKey closes every connection of one key and returns how many were closed.
//
// The set is detached from the map before the connections are closed, so a
// concurrent Track cannot be closed by this call after it registered: the new
// connection belongs to a request that has not yet been told the key is over
// its limit, and closing it here would race the caller's own check.
func (r *ConnRegistry) CloseKey(keyID string) int {
	if r == nil || keyID == "" {
		return 0
	}
	set, ok := r.conns.LoadAndDelete(keyID)
	if !ok || set == nil {
		return 0
	}
	set.mu.Lock()
	conns := make([]net.Conn, 0, len(set.conns))
	for conn := range set.conns {
		conns = append(conns, conn)
	}
	set.conns = make(map[net.Conn]struct{})
	set.mu.Unlock()

	for _, conn := range conns {
		_ = r.closeFn(conn)
	}
	return len(conns)
}

// CloseAll closes every tracked connection and returns how many were closed.
// It is the shutdown path.
func (r *ConnRegistry) CloseAll() int {
	if r == nil {
		return 0
	}
	closed := 0
	r.conns.Range(func(keyID string, _ *connSet) bool {
		closed += r.CloseKey(keyID)
		return true
	})
	return closed
}

// Keys reports which keys currently hold at least one connection.
func (r *ConnRegistry) Keys() []string {
	if r == nil {
		return nil
	}
	keys := make([]string, 0, r.conns.Size())
	r.conns.Range(func(keyID string, _ *connSet) bool {
		keys = append(keys, keyID)
		return true
	})
	return keys
}
