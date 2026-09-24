package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
)

// WP10 §4 reads the egress display facts of a whole page in one batched query.

func TestNodeEgressByHashes_BatchesAndIgnoresUnknownInput(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state", FileName))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	for i, row := range []NodeEgress{
		{NodeHash: "node-a", IPv4: "203.0.113.1", Colo: "NRT", V4ObservedNs: 10},
		{NodeHash: "node-b", IPv4: "203.0.113.2", IPv6: "2001:db8::2", Colo: "AMS", V4ObservedNs: 11},
	} {
		if err := st.UpsertNodeEgress(ctx, row); err != nil {
			t.Fatalf("UpsertNodeEgress %d: %v", i, err)
		}
	}

	rows, err := st.NodeEgressByHashes(ctx, []string{"node-a", "node-b", "node-b", "", "node-missing"})
	if err != nil {
		t.Fatalf("NodeEgressByHashes: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (unknown and empty hashes are skipped)", len(rows))
	}
	if row := rows["node-a"]; row.IPv4 != "203.0.113.1" || row.Colo != "NRT" {
		t.Errorf("node-a = %+v", row)
	}
	if row := rows["node-b"]; row.IPv6 != "2001:db8::2" {
		t.Errorf("node-b = %+v", row)
	}

	empty, err := st.NodeEgressByHashes(ctx, nil)
	if err != nil {
		t.Fatalf("NodeEgressByHashes(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("nil input = %v, want an empty map", empty)
	}
}

func TestNodeEgressByHashes_BoundsTheLookup(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state", FileName))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	if err := st.UpsertNodeEgress(ctx, NodeEgress{NodeHash: "node-0", IPv4: "203.0.113.1"}); err != nil {
		t.Fatalf("UpsertNodeEgress: %v", err)
	}

	// One hash over the documented cap: the extra hash is skipped instead of
	// failing the request, so nothing beyond the bound is read.
	hashes := make([]string, 0, MaxEgressLookupHashes+1)
	hashes = append(hashes, "node-0")
	for i := 1; i <= MaxEgressLookupHashes; i++ {
		hashes = append(hashes, "node-"+strconv.Itoa(i))
	}
	rows, err := st.NodeEgressByHashes(ctx, hashes)
	if err != nil {
		t.Fatalf("NodeEgressByHashes: %v", err)
	}
	if _, ok := rows["node-0"]; !ok {
		t.Errorf("the first hash must still resolve: %v", rows)
	}
}
