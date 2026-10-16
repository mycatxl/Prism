package usage

import (
	"io"
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
	closeFn func(io.Closer) error
}

// connSet is one key's live connections, guarded by its own lock.
//
// A per-key lock rather than one global lock: a busy key must not block
// registration for every other key, which is the same reasoning that keeps the
// counters lock-free.
type connSet struct {
	mu    sync.Mutex
	conns map[io.Closer]struct{}
}

// NewConnRegistry returns an empty registry that closes connections by calling
// Close on them.
func NewConnRegistry() *ConnRegistry {
	return &ConnRegistry{
		conns:   xsync.NewMap[string, *connSet](),
		closeFn: func(conn io.Closer) error { return conn.Close() },
	}
}

// Track registers one connection for a key.
//
// A nil connection, or an empty key id, is ignored: the single-tenant path has
// no key to attribute the connection to, and it is never force-closed by a
// quota.
//
// Reading the key's set and adding to it happen under one map critical section.
// Doing it in two steps lost connections: Untrack, having just removed the last
// connection, and CloseKey both take the set out of the map, and a Track that
// had already read it then wrote into a set nothing could reach again - never
// closed by CloseKey or CloseAll and missing from Count. The invariant restored
// here is the reason the registry exists: once Track returns, CloseKey(keyID)
// and CloseAll can see the connection, unless a CloseKey that ran before it
// already has it in the list it is closing.
func (r *ConnRegistry) Track(keyID string, conn io.Closer) {
	if r == nil || keyID == "" || conn == nil {
		return
	}
	r.conns.Compute(keyID, func(set *connSet, loaded bool) (*connSet, xsync.ComputeOp) {
		if !loaded || set == nil {
			set = &connSet{conns: make(map[io.Closer]struct{})}
		}
		set.mu.Lock()
		set.conns[conn] = struct{}{}
		set.mu.Unlock()
		return set, xsync.UpdateOp
	})
}

// Untrack removes one connection. It is safe to call for a connection that was
// never tracked, which keeps the caller's defer simple.
func (r *ConnRegistry) Untrack(keyID string, conn io.Closer) {
	if r == nil || keyID == "" || conn == nil {
		return
	}
	r.conns.Compute(keyID, func(set *connSet, loaded bool) (*connSet, xsync.ComputeOp) {
		if !loaded || set == nil {
			return nil, xsync.CancelOp
		}
		set.mu.Lock()
		delete(set.conns, conn)
		empty := len(set.conns) == 0
		set.mu.Unlock()
		if !empty {
			return set, xsync.UpdateOp
		}
		// Drop the empty set so a key that stops being used does not leave an
		// entry behind for the life of the process. The emptiness test and the
		// delete run in the same critical section that Track inserts under, so
		// a concurrent Track either got this set before the delete and cancels
		// it, or gets a fresh set after it - it is never written into a set
		// that has already left the map.
		return nil, xsync.DeleteOp
	})
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
// The set is detached from the map in the same critical section that Track
// inserts under, and the connections are closed outside it. A Track that has
// not returned yet either got there first - in which case its connection is in
// the list below and is closed here - or gets a fresh set after the detach that
// the next CloseKey will see. The new connection belongs to a request that has
// not yet been told the key is over its limit, so closing it here would race
// the caller's own check; leaving it tracked is what lets the next check find
// it.
func (r *ConnRegistry) CloseKey(keyID string) int {
	if r == nil || keyID == "" {
		return 0
	}
	var conns []io.Closer
	r.conns.Compute(keyID, func(set *connSet, loaded bool) (*connSet, xsync.ComputeOp) {
		if !loaded || set == nil {
			return nil, xsync.CancelOp
		}
		set.mu.Lock()
		conns = make([]io.Closer, 0, len(set.conns))
		for conn := range set.conns {
			conns = append(conns, conn)
		}
		set.conns = make(map[io.Closer]struct{})
		set.mu.Unlock()
		return nil, xsync.DeleteOp
	})

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
