package api

import (
	"context"
	"encoding/binary"
	"slices"
	"testing"
	"time"

	"prism/internal/node"
	"prism/internal/service"
)

func addSyntheticPaginationNodes(t *testing.T, count int) *testNodePoolFixture {
	t.Helper()
	_, cp, _ := newControlPlaneTestServer(t)
	createdAt := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	raw := []byte(`{"type":"ss","server":"pagination.test","port":443}`)
	for index := 0; index < count; index++ {
		var hash node.Hash
		binary.BigEndian.PutUint64(hash[8:], uint64(index+1))
		cp.Pool.LoadNodeFromBootstrap(node.NewNodeEntry(hash, raw, createdAt, 0))
	}
	return &testNodePoolFixture{cp: cp, count: count}
}

type testNodePoolFixture struct {
	cp    *service.ControlPlaneService
	count int
}

func TestCollectNodeListPage_CursorsCoverPoolBeyondPreviewLimit(t *testing.T) {
	const pageSize = 512
	fixture := addSyntheticPaginationNodes(t, 20_000+37)
	sorting := Sorting{SortBy: "tag", SortOrder: "asc"}
	pagination := Pagination{Limit: pageSize}
	queryHash := nodeCursorQueryHash(nil)

	var got []string
	var firstPage, secondPage []service.NodeSummary
	var cursor *nodeListCursor
	for pageNumber := 0; ; pageNumber++ {
		page, err := collectNodeListPage(context.Background(), fixture.cp, service.NodeFilters{}, sorting, pagination, queryHash, cursor)
		if err != nil {
			t.Fatalf("collect page %d: %v", pageNumber, err)
		}
		if page.Total != fixture.count || page.Scanned != fixture.count {
			t.Fatalf("page %d counts = total %d scanned %d, want %d", pageNumber, page.Total, page.Scanned, fixture.count)
		}
		if pageNumber == 0 {
			firstPage = slices.Clone(page.Items)
			if !page.HasNext || page.HasPrevious {
				t.Fatalf("first page navigation = next %v previous %v", page.HasNext, page.HasPrevious)
			}
		}
		if pageNumber == 1 {
			secondPage = slices.Clone(page.Items)
			if !page.HasNext || !page.HasPrevious {
				t.Fatalf("second page navigation = next %v previous %v", page.HasNext, page.HasPrevious)
			}
			previous, err := decodeNodeListCursor(page.PreviousCursor, queryHash, sorting, pageSize)
			if err != nil {
				t.Fatalf("decode previous cursor: %v", err)
			}
			back, err := collectNodeListPage(context.Background(), fixture.cp, service.NodeFilters{}, sorting, pagination, queryHash, &previous)
			if err != nil {
				t.Fatalf("collect previous page: %v", err)
			}
			if !sameNodeHashes(back.Items, firstPage) {
				t.Fatal("previous cursor did not return the first page")
			}
			if _, err := decodeNodeListCursor(page.NextCursor, queryHash+"changed", sorting, pageSize); err == nil {
				t.Fatal("cursor accepted a different filter hash")
			}
		}
		for _, item := range page.Items {
			got = append(got, item.NodeHash)
		}
		if !page.HasNext {
			break
		}
		next, err := decodeNodeListCursor(page.NextCursor, queryHash, sorting, pageSize)
		if err != nil {
			t.Fatalf("decode next cursor: %v", err)
		}
		cursor = &next
	}

	if len(got) != fixture.count {
		t.Fatalf("visited %d nodes, want %d", len(got), fixture.count)
	}
	if len(firstPage) != pageSize || len(secondPage) != pageSize {
		t.Fatalf("first/second page lengths = %d/%d, want %d", len(firstPage), len(secondPage), pageSize)
	}
	if !slices.IsSorted(got) {
		t.Fatal("cursor traversal is not in deterministic hash order")
	}
	for index := 1; index < len(got); index++ {
		if got[index] == got[index-1] {
			t.Fatalf("duplicate node at cursor boundary: %s", got[index])
		}
	}
}

func TestCollectNodeListPage_PreservesBoundedOffsetWindow(t *testing.T) {
	fixture := addSyntheticPaginationNodes(t, 20_000+37)
	page, err := collectNodeListPage(
		context.Background(), fixture.cp, service.NodeFilters{},
		Sorting{SortBy: "tag", SortOrder: "asc"},
		Pagination{Limit: 37, Offset: 20_000}, "legacy-offset", nil,
	)
	if err != nil {
		t.Fatalf("collect offset page: %v", err)
	}
	if page.Total != fixture.count || len(page.Items) != 37 || page.Offset != 20_000 {
		t.Fatalf("page = total %d items %d offset %d", page.Total, len(page.Items), page.Offset)
	}
}

func sameNodeHashes(a, b []service.NodeSummary) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index].NodeHash != b[index].NodeHash {
			return false
		}
	}
	return true
}
