package store

import (
	"context"
	"testing"
)

func TestPageRefreshNodeHashes_StableUnionDedupAndCursor(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()

	if err := st.UpsertNodeEgress(ctx, NodeEgress{
		NodeHash: "node-a", IPv4: "198.51.100.1", V4ObservedNs: 1,
	}); err != nil {
		t.Fatalf("node-a egress: %v", err)
	}
	if err := st.UpsertNodeEgress(ctx, NodeEgress{
		NodeHash: "node-b", IPv6: "2001:db8::2", V4ObservedNs: 0,
	}); err != nil {
		t.Fatalf("node-b egress: %v", err)
	}
	if err := st.UpsertNodeEgress(ctx, NodeEgress{
		NodeHash: "node-c", IPv4: "198.51.100.3", V4ObservedNs: 2,
	}); err != nil {
		t.Fatalf("node-c egress: %v", err)
	}
	if err := st.UpsertEvidence(ctx, Evidence{
		IP: "198.51.100.1", Provider: "proxycheck", ValidUntilNs: 50,
	}); err != nil {
		t.Fatalf("node-a evidence: %v", err)
	}
	if err := st.UpsertEvidence(ctx, Evidence{
		IP: "2001:db8::2", Provider: "proxycheck", ValidUntilNs: 50,
	}); err != nil {
		t.Fatalf("node-b evidence: %v", err)
	}
	if err := st.UpsertNodeCheck(ctx, NodeCheck{
		NodeHash: "node-d", CheckID: "netflix", ValidUntilNs: 50,
	}); err != nil {
		t.Fatalf("node-d check: %v", err)
	}

	// node-a is both expired-evidence and stale-egress; UNION must emit it once.
	first, err := st.PageRefreshNodeHashes(ctx, 100, 100, true, "", 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.NodeHashes[0] != "node-a" || first.NodeHashes[1] != "node-b" || first.Cursor != "node-b" || first.Complete {
		t.Fatalf("first page = %+v, want node-a/node-b and an incomplete cursor", first)
	}
	if first.Scanned != 2 {
		t.Fatalf("first scanned = %d, want 2", first.Scanned)
	}

	second, err := st.PageRefreshNodeHashes(ctx, 100, 100, true, first.Cursor, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.NodeHashes) != 2 || second.NodeHashes[0] != "node-c" || second.NodeHashes[1] != "node-d" || second.Complete {
		t.Fatalf("second page = %+v, want node-c/node-d exact-limit page", second)
	}

	third, err := st.PageRefreshNodeHashes(ctx, 100, 100, true, second.Cursor, 2)
	if err != nil {
		t.Fatalf("terminal page: %v", err)
	}
	if len(third.NodeHashes) != 0 || third.Scanned != 0 || !third.Complete || third.Cursor != second.Cursor {
		t.Fatalf("terminal page = %+v, want empty complete page retaining cursor", third)
	}
}

func TestPageRefreshNodeHashes_ExcludesExpiredChecksWhenDisabled(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	if err := st.UpsertNodeCheck(ctx, NodeCheck{NodeHash: "check-only", CheckID: "netflix", ValidUntilNs: 10}); err != nil {
		t.Fatalf("UpsertNodeCheck: %v", err)
	}

	page, err := st.PageRefreshNodeHashes(ctx, 100, 100, false, "", 10)
	if err != nil {
		t.Fatalf("PageRefreshNodeHashes: %v", err)
	}
	if len(page.NodeHashes) != 0 || !page.Complete {
		t.Fatalf("page = %+v, want no expired-check node when checks are disabled", page)
	}
}

func TestPageRefreshNodeHashes_EmptyLimit(t *testing.T) {
	st := openTemp(t)
	page, err := st.PageRefreshNodeHashes(context.Background(), 100, 100, true, "", 0)
	if err != nil || !page.Complete || len(page.NodeHashes) != 0 {
		t.Fatalf("zero-limit page = %+v err=%v", page, err)
	}
}

func TestNodeNeedsRefresh_RechecksEvidenceChecksAndEgress(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	if err := st.UpsertNodeEgress(ctx, NodeEgress{
		NodeHash: "node", IPv4: "198.51.100.9", V4ObservedNs: 90,
	}); err != nil {
		t.Fatalf("UpsertNodeEgress: %v", err)
	}
	if err := st.UpsertEvidence(ctx, Evidence{
		IP: "198.51.100.9", Provider: "proxycheck", ValidUntilNs: 200,
	}); err != nil {
		t.Fatalf("fresh evidence: %v", err)
	}
	if got, err := st.NodeNeedsRefresh(ctx, "node", 100, 50, false); err != nil || got {
		t.Fatalf("fresh node = %v err=%v, want false", got, err)
	}

	if err := st.UpsertEvidence(ctx, Evidence{
		IP: "198.51.100.9", Provider: "proxycheck", ValidUntilNs: 100,
	}); err != nil {
		t.Fatalf("expired evidence: %v", err)
	}
	if got, err := st.NodeNeedsRefresh(ctx, "node", 100, 50, false); err != nil || !got {
		t.Fatalf("expired evidence = %v err=%v, want true", got, err)
	}

	if err := st.UpsertEvidence(ctx, Evidence{
		IP: "198.51.100.9", Provider: "proxycheck", ValidUntilNs: 200,
	}); err != nil {
		t.Fatalf("restore evidence: %v", err)
	}
	if err := st.UpsertNodeCheck(ctx, NodeCheck{
		NodeHash: "node", CheckID: "netflix", ValidUntilNs: 100,
	}); err != nil {
		t.Fatalf("expired check: %v", err)
	}
	if got, err := st.NodeNeedsRefresh(ctx, "node", 100, 50, false); err != nil || got {
		t.Fatalf("expired check with checks disabled = %v err=%v, want false", got, err)
	}
	if got, err := st.NodeNeedsRefresh(ctx, "node", 100, 50, true); err != nil || !got {
		t.Fatalf("expired check with checks enabled = %v err=%v, want true", got, err)
	}

	if err := st.UpsertNodeCheck(ctx, NodeCheck{
		NodeHash: "node", CheckID: "netflix", ValidUntilNs: 200,
	}); err != nil {
		t.Fatalf("restore check: %v", err)
	}
	if err := st.UpsertNodeEgress(ctx, NodeEgress{
		NodeHash: "node", IPv4: "198.51.100.9", V4ObservedNs: 49,
	}); err != nil {
		t.Fatalf("stale egress: %v", err)
	}
	if got, err := st.NodeNeedsRefresh(ctx, "node", 100, 50, true); err != nil || !got {
		t.Fatalf("stale egress = %v err=%v, want true", got, err)
	}
	if err := st.UpsertNodeCheck(ctx, NodeCheck{NodeHash: "check-only", CheckID: "netflix", ValidUntilNs: 100}); err != nil {
		t.Fatalf("check-only row: %v", err)
	}
	if got, err := st.NodeNeedsRefresh(ctx, "check-only", 100, 50, true); err != nil || !got {
		t.Fatalf("check-only node = %v err=%v, want true", got, err)
	}
	if got, err := st.NodeNeedsRefresh(ctx, "missing", 100, 50, true); err != nil || got {
		t.Fatalf("missing node = %v err=%v, want false", got, err)
	}
}
