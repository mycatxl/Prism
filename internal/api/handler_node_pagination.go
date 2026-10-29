package api

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"prism/internal/node"
	"prism/internal/service"
)

const maxNodeOffsetWindow = maxPageLimit

type nodeListCursor struct {
	Version   int                     `json:"v"`
	QueryHash string                  `json:"q"`
	SortBy    string                  `json:"s"`
	SortOrder string                  `json:"o"`
	Limit     int                     `json:"l"`
	Offset    int                     `json:"n"`
	Before    bool                    `json:"b,omitempty"`
	Last      service.NodeListSortKey `json:"k"`
}

type nodeListCandidate struct {
	hash node.Hash
	key  service.NodeListSortKey
}

type nodeListCandidateHeap struct {
	items   []nodeListCandidate
	sorting Sorting
}

func (h nodeListCandidateHeap) Len() int { return len(h.items) }
func (h nodeListCandidateHeap) Less(i, j int) bool {
	return compareNodeListSortKeys(h.items[i].key, h.items[j].key, h.sorting) > 0
}
func (h nodeListCandidateHeap) Swap(i, j int) { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *nodeListCandidateHeap) Push(value any) {
	h.items = append(h.items, value.(nodeListCandidate))
}
func (h *nodeListCandidateHeap) Pop() any {
	last := len(h.items) - 1
	value := h.items[last]
	h.items = h.items[:last]
	return value
}

func compareNodeListSortKeys(a, b service.NodeListSortKey, sorting Sorting) int {
	if a.Present != b.Present {
		if a.Present {
			return -1
		}
		return 1
	}
	var order int
	switch sorting.SortBy {
	case "created_at", "region", "tag":
		order = strings.Compare(a.Text, b.Text)
	case "failure_count", "purity_score", "assessed_at":
		if a.Integer < b.Integer {
			order = -1
		} else if a.Integer > b.Integer {
			order = 1
		}
	case "latency":
		if a.Number < b.Number {
			order = -1
		} else if a.Number > b.Number {
			order = 1
		}
	}
	if order == 0 {
		order = strings.Compare(a.NodeHash, b.NodeHash)
	}
	return applySortOrder(order, sorting.SortOrder)
}

func nodeCursorQueryHash(values url.Values) string {
	canonical := make(url.Values, len(values))
	for key, items := range values {
		if key == "cursor" || key == "offset" {
			continue
		}
		canonical[key] = slices.Clone(items)
	}
	sum := sha256.Sum256([]byte(canonical.Encode()))
	return hex.EncodeToString(sum[:])
}

func decodeNodeListCursor(token, queryHash string, sorting Sorting, limit int) (nodeListCursor, error) {
	var cursor nodeListCursor
	if len(token) == 0 || len(token) > 4096 {
		return cursor, fmt.Errorf("cursor: invalid length")
	}
	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || json.Unmarshal(payload, &cursor) != nil {
		return nodeListCursor{}, fmt.Errorf("cursor: invalid encoding")
	}
	if cursor.Version != 1 || cursor.QueryHash != queryHash || cursor.SortBy != sorting.SortBy || cursor.SortOrder != sorting.SortOrder || cursor.Limit != limit || cursor.Offset < 0 {
		return nodeListCursor{}, fmt.Errorf("cursor: does not match this filter and sort")
	}
	if _, err := node.ParseHex(cursor.Last.NodeHash); err != nil {
		return nodeListCursor{}, fmt.Errorf("cursor: invalid node key")
	}
	return cursor, nil
}

func encodeNodeListCursor(cursor nodeListCursor) string {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

type nodeListSelection struct {
	Items                           []service.NodeSummary
	Total                           int
	Scanned                         int
	Offset                          int
	HasNext                         bool
	HasPrevious                     bool
	NextCursor                      string
	PreviousCursor                  string
	UniqueEgressIPs                 int
	UniqueHealthyEgressIPs          int
	UniqueEgressIPsTruncated        bool
	UniqueHealthyEgressIPsTruncated bool
}

func collectNodeListPage(
	ctx context.Context,
	cp *service.ControlPlaneService,
	filters service.NodeFilters,
	sorting Sorting,
	pagination Pagination,
	queryHash string,
	cursor *nodeListCursor,
) (nodeListSelection, error) {
	var result nodeListSelection
	pageOffset := pagination.Offset
	window := pagination.Limit
	candidateSorting := sorting
	if cursor == nil {
		if pagination.Offset < 0 || pagination.Limit < 1 || pagination.Offset > maxNodeOffsetWindow-pagination.Limit {
			return result, fmt.Errorf("offset: page window must be <= %d; continue with a cursor", maxNodeOffsetWindow)
		}
		window = pagination.Offset + pagination.Limit
	} else {
		pageOffset = cursor.Offset
		if cursor.Before {
			candidateSorting.SortOrder = reverseSortOrder(sorting.SortOrder)
			pageOffset = max(0, cursor.Offset-pagination.Limit)
		}
	}
	if window <= 0 || window > maxNodeOffsetWindow {
		return result, fmt.Errorf("limit: page window must be between 1 and %d", maxNodeOffsetWindow)
	}
	result.Offset = pageOffset

	candidates := &nodeListCandidateHeap{items: make([]nodeListCandidate, 0, window), sorting: candidateSorting}
	heap.Init(candidates)
	remainingInDirection := 0
	stats, err := cp.RangeFilteredNodeEntriesContext(ctx, filters, func(hash node.Hash, entry *node.NodeEntry) bool {
		key := cp.NodeListSortKey(hash, entry, sorting.SortBy)
		if cursor != nil {
			comparison := compareNodeListSortKeys(key, cursor.Last, sorting)
			if cursor.Before && comparison >= 0 || !cursor.Before && comparison <= 0 {
				return true
			}
		}
		remainingInDirection++
		candidate := nodeListCandidate{hash: hash, key: key}
		if candidates.Len() < window {
			heap.Push(candidates, candidate)
			return true
		}
		if compareNodeListSortKeys(key, candidates.items[0].key, candidateSorting) < 0 {
			heap.Pop(candidates)
			heap.Push(candidates, candidate)
		}
		return true
	})
	if err != nil {
		return result, err
	}
	slices.SortFunc(candidates.items, func(a, b nodeListCandidate) int {
		return compareNodeListSortKeys(a.key, b.key, candidateSorting)
	})
	if cursor != nil && cursor.Before {
		slices.Reverse(candidates.items)
	}

	start := 0
	if cursor == nil {
		start = min(pagination.Offset, len(candidates.items))
	}
	end := min(start+pagination.Limit, len(candidates.items))
	pageCandidates := candidates.items[start:end]
	items := make([]service.NodeSummary, 0, len(pageCandidates))
	var firstKey, lastKey service.NodeListSortKey
	for _, candidate := range pageCandidates {
		summary, getErr := cp.GetNode(candidate.hash.Hex())
		if getErr != nil || summary == nil {
			continue
		}
		if len(items) == 0 {
			firstKey = candidate.key
		}
		lastKey = candidate.key
		items = append(items, *summary)
	}

	result.Items = items
	result.Total = stats.Total
	result.Scanned = stats.Scanned
	result.UniqueEgressIPs = stats.UniqueEgressIPs
	result.UniqueHealthyEgressIPs = stats.UniqueHealthyEgressIPs
	result.UniqueEgressIPsTruncated = stats.UniqueEgressIPsTruncated
	result.UniqueHealthyEgressIPsTruncated = stats.UniqueHealthyEgressIPsTruncated
	result.HasPrevious = pageOffset > 0
	result.HasNext = stats.Total > pageOffset+len(pageCandidates)
	if cursor != nil && cursor.Before {
		result.HasPrevious = remainingInDirection > len(pageCandidates)
		result.HasNext = stats.Total > pageOffset+len(pageCandidates)
	}
	if len(items) == 0 {
		return result, nil
	}
	if result.HasNext {
		nextOffset := pageOffset + len(pageCandidates)
		result.NextCursor = encodeNodeListCursor(nodeListCursor{
			Version: 1, QueryHash: queryHash, SortBy: sorting.SortBy, SortOrder: sorting.SortOrder,
			Limit: pagination.Limit, Offset: nextOffset, Last: lastKey,
		})
	}
	if result.HasPrevious {
		previousOffset := pageOffset
		if cursor != nil && cursor.Before {
			previousOffset = pageOffset
		}
		result.PreviousCursor = encodeNodeListCursor(nodeListCursor{
			Version: 1, QueryHash: queryHash, SortBy: sorting.SortBy, SortOrder: sorting.SortOrder,
			Limit: pagination.Limit, Offset: previousOffset, Before: true, Last: firstKey,
		})
	}
	return result, nil
}

func reverseSortOrder(order string) string {
	if order == "desc" {
		return "asc"
	}
	return "desc"
}
