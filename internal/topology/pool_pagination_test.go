package topology

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"prism/internal/node"
)

func TestPageNodeHashes_StableContinuationAndDeletion(t *testing.T) {
	pool := newTestPool(NewSubscriptionManager())
	hashes := make([]node.Hash, 0, 5)
	for _, name := range []string{"node-a", "node-b", "node-c", "node-d", "node-e"} {
		raw := json.RawMessage(`{"name":"` + name + `"}`)
		hash := node.HashFromRawOptions(raw)
		hashes = append(hashes, hash)
		pool.AddNodeFromSub(hash, raw, "sub-1")
	}
	sort.Slice(hashes, func(i, j int) bool { return hashes[i].String() < hashes[j].String() })

	first, err := pool.PageNodeHashes("", 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.Scanned != 2 || first.Complete || first.Cursor != hashes[1].String() {
		t.Fatalf("first page = %+v, want two scanned entries and an incomplete cursor", first)
	}
	if got := hashStrings(first.Hashes); !reflect.DeepEqual(got, hashStrings(hashes[:2])) {
		t.Fatalf("first hashes = %v, want %v", got, hashStrings(hashes[:2]))
	}

	// Removing an entry after the cursor must not make the continuation rewind or
	// stall. The remaining greater hashes are still returned in order.
	pool.RemoveNodeFromSub(hashes[2], "sub-1")
	second, err := pool.PageNodeHashes(first.Cursor, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if second.Scanned != 2 || second.Complete || second.Cursor != hashes[4].String() {
		t.Fatalf("second page = %+v, want the two surviving greater hashes", second)
	}
	if got := hashStrings(second.Hashes); !reflect.DeepEqual(got, hashStrings(hashes[3:])) {
		t.Fatalf("second hashes = %v, want %v", got, hashStrings(hashes[3:]))
	}

	third, err := pool.PageNodeHashes(second.Cursor, 2)
	if err != nil {
		t.Fatalf("terminal page: %v", err)
	}
	if len(third.Hashes) != 0 || third.Scanned != 0 || !third.Complete || third.Cursor != second.Cursor {
		t.Fatalf("terminal page = %+v, want an empty complete page retaining the cursor", third)
	}
}

func TestPageNodeHashes_EmptyLimitAndInvalidCursor(t *testing.T) {
	pool := newTestPool(NewSubscriptionManager())

	zero, err := pool.PageNodeHashes("", 0)
	if err != nil || !zero.Complete || len(zero.Hashes) != 0 {
		t.Fatalf("zero-limit page = %+v, err=%v", zero, err)
	}

	empty, err := pool.PageNodeHashes("", 3)
	if err != nil || !empty.Complete || empty.Scanned != 0 || len(empty.Hashes) != 0 {
		t.Fatalf("empty pool page = %+v, err=%v", empty, err)
	}

	if _, err := pool.PageNodeHashes("not-a-hash", 1); err == nil {
		t.Fatal("invalid pool cursor must fail")
	}
}

func hashStrings(hashes []node.Hash) []string {
	out := make([]string, len(hashes))
	for i, hash := range hashes {
		out[i] = hash.String()
	}
	return out
}
