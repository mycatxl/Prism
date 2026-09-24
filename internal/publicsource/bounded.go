package publicsource

import (
	"container/heap"
	"encoding/json"
	"sort"
)

// topNodes keeps only the nodes with the smallest hashes.
//
// Why this exists: the published payload is always "sort the accepted nodes by
// hash and take the first MaxNodes", so the nodes that never make that prefix
// are pure dead weight. Public lists are large — one source alone can carry
// 60k endpoints — and materialising every node from every source cost ~120 MiB
// of RSS for a 900 KB output. Keeping a bounded set makes memory proportional to
// what is actually published.
//
// It is not an approximation. Because the output is a sorted prefix, retaining
// the k smallest hashes yields byte-identical content to retaining everything
// and sorting afterwards; the eviction rule below is exactly that prefix.
type topNodes struct {
	// limit is the maximum number of retained nodes. Zero means unbounded, which
	// is what MaxNodes=0 asks for.
	limit int
	heap  hashMaxHeap
	index map[string]struct{}
}

type hashEntry struct {
	hash string
	raw  json.RawMessage
}

// hashMaxHeap is a max-heap on the hash, so the largest retained hash — the one
// to evict first — is always the root.
type hashMaxHeap []hashEntry

func (h hashMaxHeap) Len() int           { return len(h) }
func (h hashMaxHeap) Less(i, j int) bool { return h[i].hash > h[j].hash }
func (h hashMaxHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *hashMaxHeap) Push(x any)        { *h = append(*h, x.(hashEntry)) }
func (h *hashMaxHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

func newTopNodes(limit int) *topNodes {
	if limit < 0 {
		limit = 0
	}
	return &topNodes{
		limit: limit,
		index: make(map[string]struct{}),
	}
}

// Add offers a node. It reports whether the node is now retained when
// interested is true, so callers can count distinct accepted nodes.
func (t *topNodes) Add(hash string, raw json.RawMessage) {
	if _, dup := t.index[hash]; dup {
		return
	}
	if t.limit == 0 || len(t.heap) < t.limit {
		t.index[hash] = struct{}{}
		heap.Push(&t.heap, hashEntry{hash: hash, raw: raw})
		return
	}
	// The set is full. Once it is full the largest retained hash never grows, so
	// anything not smaller than it can be dropped for good.
	if hash >= t.heap[0].hash {
		return
	}
	delete(t.index, t.heap[0].hash)
	t.heap[0] = hashEntry{hash: hash, raw: raw}
	heap.Fix(&t.heap, 0)
	t.index[hash] = struct{}{}
}

// Len reports how many nodes are retained.
func (t *topNodes) Len() int {
	if t == nil {
		return 0
	}
	return len(t.heap)
}

// Sorted returns the retained nodes in ascending hash order, which is the order
// the payload must be built in.
func (t *topNodes) Sorted() []hashEntry {
	if t == nil {
		return nil
	}
	out := append([]hashEntry(nil), t.heap...)
	sort.Slice(out, func(i, j int) bool { return out[i].hash < out[j].hash })
	return out
}

// Range visits every retained node in unspecified order.
func (t *topNodes) Range(fn func(hash string, raw json.RawMessage) bool) {
	if t == nil {
		return
	}
	for _, entry := range t.heap {
		if !fn(entry.hash, entry.raw) {
			return
		}
	}
}
