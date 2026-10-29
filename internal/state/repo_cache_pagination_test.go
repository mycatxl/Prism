package state

import (
	"context"
	"testing"

	"prism/internal/model"
)

func TestPageSubscriptionNodeHashes_KeysetContinuationAndEvictedState(t *testing.T) {
	repo := newTestCacheRepo(t)
	if err := repo.BulkUpsertSubscriptionNodes([]model.SubscriptionNode{
		{SubscriptionID: "s1", NodeHash: "n-c", Evicted: true},
		{SubscriptionID: "s1", NodeHash: "n-a"},
		{SubscriptionID: "s1", NodeHash: "n-b"},
		{SubscriptionID: "s2", NodeHash: "n-b"},
		{SubscriptionID: "s2", NodeHash: "n-d"},
	}); err != nil {
		t.Fatalf("BulkUpsertSubscriptionNodes: %v", err)
	}

	ctx := context.Background()
	first, err := repo.PageSubscriptionNodeHashes(ctx, []string{"s2", "s1", "s1", " ", "missing"}, "", "", 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.Scanned != 2 || first.Complete || first.LastSubscriptionID != "s1" || first.LastNodeHash != "n-b" {
		t.Fatalf("first page = %+v, want the first two ordered rows", first)
	}
	if first.Nodes[0].SubscriptionID != "s1" || first.Nodes[0].NodeHash != "n-a" || first.Nodes[1].NodeHash != "n-b" {
		t.Fatalf("first nodes = %+v, want s1/n-a and s1/n-b", first.Nodes)
	}

	second, err := repo.PageSubscriptionNodeHashes(ctx, []string{"s1", "s2"}, first.LastSubscriptionID, first.LastNodeHash, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if second.Scanned != 2 || second.Complete || second.LastSubscriptionID != "s2" || second.LastNodeHash != "n-d" {
		t.Fatalf("second page = %+v, want the exact-limit continuation ending at s2/n-d", second)
	}
	if !second.Nodes[0].Evicted || second.Nodes[0].NodeHash != "n-c" || second.Nodes[1].NodeHash != "n-d" || second.Nodes[1].SubscriptionID != "s2" {
		t.Fatalf("second nodes = %+v, want evicted s1/n-c followed by s2/n-d", second.Nodes)
	}

	third, err := repo.PageSubscriptionNodeHashes(ctx, []string{"s1", "s2"}, second.LastSubscriptionID, second.LastNodeHash, 2)
	if err != nil {
		t.Fatalf("terminal page: %v", err)
	}
	if len(third.Nodes) != 0 || third.Scanned != 0 || !third.Complete {
		t.Fatalf("terminal page = %+v, want an empty complete page", third)
	}
}

func TestPageSubscriptionNodeHashes_EmptyInputsAndLimit(t *testing.T) {
	repo := newTestCacheRepo(t)
	ctx := context.Background()

	for _, ids := range [][]string{nil, []string{"", "  "}, []string{"missing"}} {
		page, err := repo.PageSubscriptionNodeHashes(ctx, ids, "", "", 10)
		if err != nil {
			t.Fatalf("ids=%v: %v", ids, err)
		}
		if !page.Complete || page.Scanned != 0 || len(page.Nodes) != 0 {
			t.Fatalf("ids=%v page=%+v, want empty complete page", ids, page)
		}
	}

	page, err := repo.PageSubscriptionNodeHashes(ctx, []string{"missing"}, "", "", 0)
	if err != nil || !page.Complete || len(page.Nodes) != 0 {
		t.Fatalf("zero-limit page=%+v err=%v", page, err)
	}
}

func TestPageSubscriptionNodeHashes_EvictedRowDoesNotHideLaterActiveRow(t *testing.T) {
	repo := newTestCacheRepo(t)
	if err := repo.BulkUpsertSubscriptionNodes([]model.SubscriptionNode{
		{SubscriptionID: "s1", NodeHash: "same", Evicted: true},
		{SubscriptionID: "s2", NodeHash: "same"},
	}); err != nil {
		t.Fatalf("BulkUpsertSubscriptionNodes: %v", err)
	}

	ctx := context.Background()
	first, err := repo.PageSubscriptionNodeHashes(ctx, []string{"s1", "s2"}, "", "", 1)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Nodes) != 1 || !first.Nodes[0].Evicted || first.Nodes[0].SubscriptionID != "s1" {
		t.Fatalf("first page = %+v, want the earlier evicted row", first)
	}
	second, err := repo.PageSubscriptionNodeHashes(ctx, []string{"s1", "s2"}, first.LastSubscriptionID, first.LastNodeHash, 1)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Nodes) != 1 || second.Nodes[0].Evicted || second.Nodes[0].SubscriptionID != "s2" || second.Nodes[0].NodeHash != "same" {
		t.Fatalf("second page = %+v, want the later active row", second)
	}
}
