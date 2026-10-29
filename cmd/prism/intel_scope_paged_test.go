package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"prism/internal/config"
	"prism/internal/intel/jobs"
	"prism/internal/intel/store"
	"prism/internal/model"
	"prism/internal/node"
)

func TestResolveScopePage_ResumesUnionSkipsEvictedAndExplicitDuplicates(t *testing.T) {
	app, hashes := scopeFixture(t, map[string][]string{
		"sub-a": {"active", "evicted"},
	})
	if err := app.stateEngine.BulkUpsertSubscriptionNodes([]model.SubscriptionNode{
		{SubscriptionID: "sub-a", NodeHash: hashes["active"]},
		{SubscriptionID: "sub-a", NodeHash: hashes["evicted"], Evicted: true},
	}); err != nil {
		t.Fatalf("mark subscription rows: %v", err)
	}

	intelStore, err := store.Open(t.TempDir() + "/intel.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = intelStore.Close() })
	now := time.Now().UTC()
	refreshHash := node.HashFromRawOptions([]byte("refresh-only")).String()
	for _, hash := range []string{hashes["active"], refreshHash} {
		if err := intelStore.UpsertNodeEgress(context.Background(), store.NodeEgress{
			NodeHash:     hash,
			IPv4:         "198.51.100.10",
			V4ObservedNs: now.Add(-49 * time.Hour).UnixNano(),
			V6ObservedNs: 0,
			V6CheckedNs:  0,
		}); err != nil {
			t.Fatalf("UpsertNodeEgress(%s): %v", hash, err)
		}
	}
	app.intelStore = intelStore

	scope := jobs.Scope{
		NodeHashes:      []string{hashes["active"]},
		SubscriptionIDs: []string{"sub-a"},
		RefreshStale:    true,
	}
	ctx := context.Background()
	cursor := ""
	seen := make(map[string]int)
	var pages int
	var last jobs.ScopePage
	for pages = 0; pages < 12; pages++ {
		page, err := app.ResolveScopePage(ctx, scope, cursor, 1)
		if err != nil {
			t.Fatalf("ResolveScopePage page %d: %v", pages, err)
		}
		last = page
		for _, hash := range page.Nodes {
			seen[hash]++
		}
		if page.Complete {
			break
		}
		if page.Cursor == cursor {
			t.Fatalf("page %d did not advance cursor %q", pages, cursor)
		}
		cursor = page.Cursor
	}
	if !last.Complete {
		t.Fatalf("resolver did not complete after %d pages; last=%+v", pages+1, last)
	}
	if len(seen) != 2 || seen[hashes["active"]] != 1 || seen[refreshHash] != 1 {
		t.Fatalf("resolved nodes = %v, want one active and one refresh node", seen)
	}
	if seen[hashes["evicted"]] != 0 {
		t.Fatalf("evicted subscription node entered the scope: %v", seen)
	}
}

func TestValidateScopeNode_RefreshRequiresCurrentStaleness(t *testing.T) {
	_, pool := newBootstrapTestRuntime(config.NewDefaultRuntimeConfig())
	raw := json.RawMessage(`{"type":"ss","server":"refresh.example","port":443}`)
	hash := node.HashFromRawOptions(raw)
	pool.AddNodeFromSub(hash, raw, "sub-a")

	intelStore, err := store.Open(t.TempDir() + "/intel.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = intelStore.Close() })
	app := &prismApp{
		topoRuntime: &topologyRuntime{pool: pool},
		intelStore:  intelStore,
	}
	ctx := context.Background()
	now := time.Now().UTC()
	if err := intelStore.UpsertNodeEgress(ctx, store.NodeEgress{
		NodeHash:     hash.String(),
		IPv4:         "198.51.100.11",
		V4ObservedNs: now.UnixNano(),
	}); err != nil {
		t.Fatalf("fresh egress: %v", err)
	}

	scope := jobs.Scope{RefreshStale: true}
	if selected, err := app.ValidateScopeNode(ctx, scope, hash.String()); err != nil || selected {
		t.Fatalf("fresh refresh node = %v err=%v, want false", selected, err)
	}

	if err := intelStore.UpsertNodeEgress(ctx, store.NodeEgress{
		NodeHash:     hash.String(),
		IPv4:         "198.51.100.11",
		V4ObservedNs: now.Add(-49 * time.Hour).UnixNano(),
	}); err != nil {
		t.Fatalf("stale egress: %v", err)
	}
	if selected, err := app.ValidateScopeNode(ctx, scope, hash.String()); err != nil || !selected {
		t.Fatalf("stale refresh node = %v err=%v, want true", selected, err)
	}

	if err := intelStore.UpsertNodeEgress(ctx, store.NodeEgress{
		NodeHash:     hash.String(),
		IPv4:         "198.51.100.11",
		V4ObservedNs: now.UnixNano(),
	}); err != nil {
		t.Fatalf("fresh egress again: %v", err)
	}
	if err := intelStore.UpsertNodeCheck(ctx, store.NodeCheck{
		NodeHash:     hash.String(),
		CheckID:      "netflix",
		ValidUntilNs: now.Add(-time.Minute).UnixNano(),
	}); err != nil {
		t.Fatalf("expired check: %v", err)
	}
	if selected, err := app.ValidateScopeNode(ctx, scope, hash.String()); err != nil || selected {
		t.Fatalf("expired check with checks disabled = %v err=%v, want false", selected, err)
	}
	scope.RefreshStaleChecks = true
	if selected, err := app.ValidateScopeNode(ctx, scope, hash.String()); err != nil || !selected {
		t.Fatalf("expired check with checks enabled = %v err=%v, want true", selected, err)
	}
}

func TestResolveScopePage_DeduplicatesPoolSubscriptionRefreshAcrossPages(t *testing.T) {
	app, hashes := scopeFixture(t, map[string][]string{
		"sub-a": {"active"},
	})
	_, pool := newBootstrapTestRuntime(config.NewDefaultRuntimeConfig())
	activeRaw := json.RawMessage(`{"type":"ss","server":"active.example","port":443}`)
	activeHash, err := node.ParseHex(hashes["active"])
	if err != nil {
		t.Fatalf("ParseHex(active): %v", err)
	}
	pool.AddNodeFromSub(activeHash, activeRaw, "sub-a")
	app.topoRuntime = &topologyRuntime{pool: pool}

	intelStore, err := store.Open(t.TempDir() + "/intel.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = intelStore.Close() })
	refreshHash := node.HashFromRawOptions([]byte("refresh-only")).String()
	now := time.Now().UTC()
	for _, hash := range []string{activeHash.String(), refreshHash} {
		if err := intelStore.UpsertNodeEgress(context.Background(), store.NodeEgress{
			NodeHash:     hash,
			IPv4:         "198.51.100.12",
			V4ObservedNs: now.Add(-49 * time.Hour).UnixNano(),
		}); err != nil {
			t.Fatalf("UpsertNodeEgress(%s): %v", hash, err)
		}
	}
	app.intelStore = intelStore

	scope := jobs.Scope{All: true, SubscriptionIDs: []string{"sub-a"}, RefreshStale: true}
	cursor := ""
	counts := make(map[string]int)
	completed := false
	for pageNo := 0; pageNo < 12; pageNo++ {
		page, err := app.ResolveScopePage(context.Background(), scope, cursor, 1)
		if err != nil {
			t.Fatalf("ResolveScopePage page %d: %v", pageNo, err)
		}
		for _, hash := range page.Nodes {
			counts[hash]++
		}
		if page.Complete {
			completed = true
			break
		}
		if page.Cursor == cursor {
			t.Fatalf("page %d did not advance cursor %q", pageNo, cursor)
		}
		cursor = page.Cursor
	}
	if !completed {
		t.Fatalf("resolver did not complete; counts=%v", counts)
	}
	if counts[activeHash.String()] != 1 || counts[refreshHash] != 1 || len(counts) != 2 {
		t.Fatalf("cross-source counts = %v, want active and refresh-only exactly once", counts)
	}
}

func TestResolveScopePage_DeduplicatesAcrossSelectedSubscriptions(t *testing.T) {
	app, hashes := scopeFixture(t, map[string][]string{
		"sub-a": {"shared"},
		"sub-b": {"shared"},
	})
	scope := jobs.Scope{SubscriptionIDs: []string{"sub-a", "sub-b"}}
	cursor := ""
	seen := make(map[string]int)
	for pageNo := 0; pageNo < 4; pageNo++ {
		page, err := app.ResolveScopePage(context.Background(), scope, cursor, 1)
		if err != nil {
			t.Fatalf("ResolveScopePage page %d: %v", pageNo, err)
		}
		for _, hash := range page.Nodes {
			seen[hash]++
		}
		if page.Complete {
			break
		}
		if page.Cursor == cursor {
			t.Fatalf("page %d did not advance cursor %q", pageNo, cursor)
		}
		cursor = page.Cursor
	}
	if seen[hashes["shared"]] != 1 || len(seen) != 1 {
		t.Fatalf("shared subscription counts = %v, want one emitted node", seen)
	}
}

func TestResolveScopePage_BoundsScanWhenFilterRejectsPool(t *testing.T) {
	_, pool := newBootstrapTestRuntime(config.NewDefaultRuntimeConfig())
	for _, name := range []string{"scan-a", "scan-b", "scan-c"} {
		raw := json.RawMessage(`{"type":"ss","server":"` + name + `","port":443}`)
		pool.AddNodeFromSub(node.HashFromRawOptions(raw), raw, "sub-scan")
	}
	app := &prismApp{topoRuntime: &topologyRuntime{pool: pool}}
	scope := jobs.Scope{Filter: map[string]string{"protocol": "vless"}}

	cursor := ""
	var scanned int64
	for pageNo := 0; pageNo < 5; pageNo++ {
		page, err := app.ResolveScopePage(context.Background(), scope, cursor, 1)
		if err != nil {
			t.Fatalf("ResolveScopePage page %d: %v", pageNo, err)
		}
		if page.Scanned > 1 || len(page.Nodes) != 0 {
			t.Fatalf("page %d = %+v, want at most one scanned candidate and no matches", pageNo, page)
		}
		scanned += page.Scanned
		if page.Complete {
			if scanned != 3 {
				t.Fatalf("scanned = %d, want all three pool entries before completion", scanned)
			}
			return
		}
		if page.Cursor == cursor {
			t.Fatalf("page %d did not advance cursor %q", pageNo, cursor)
		}
		cursor = page.Cursor
	}
	t.Fatal("resolver did not complete within the bounded scan pages")
}
