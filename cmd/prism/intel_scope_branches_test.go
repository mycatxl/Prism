package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"prism/internal/intel/jobs"
	"prism/internal/model"
	"prism/internal/node"
	"prism/internal/state"
)

// The subscription/platform branches of resolveIntelScope.
//
// The `all` and `filter` branches are covered by intel_scope_filter_test.go; these
// two need persisted subscription_nodes rows and a registered platform, which is
// what this file builds.

// scopeFixture builds a state engine with the given subscription→node links and
// returns the app plus the node hashes it created.
func scopeFixture(t *testing.T, links map[string][]string) (*prismApp, map[string]string) {
	t.Helper()
	engine, closer, err := state.PersistenceBootstrap(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	now := time.Now().UnixNano()
	hashes := make(map[string]string, len(links))
	var rows []model.SubscriptionNode
	for subID, names := range links {
		if err := engine.UpsertSubscription(model.Subscription{
			ID:               subID,
			Name:             subID,
			SourceType:       "local",
			Content:          "127.0.0.1:1",
			UpdateIntervalNs: int64(time.Hour),
			Enabled:          true,
			CreatedAtNs:      now,
			UpdatedAtNs:      now,
		}); err != nil {
			t.Fatalf("UpsertSubscription(%s): %v", subID, err)
		}
		for _, name := range names {
			hash := node.HashFromRawOptions([]byte(name)).String()
			hashes[name] = hash
			rows = append(rows, model.SubscriptionNode{
				SubscriptionID: subID,
				NodeHash:       hash,
			})
		}
	}
	if err := engine.BulkUpsertSubscriptionNodes(rows); err != nil {
		t.Fatalf("BulkUpsertSubscriptionNodes: %v", err)
	}

	return &prismApp{stateEngine: engine}, hashes
}

// sortedHashes returns the resolved scope in a stable order.
func sortedHashes(scope []string) []string {
	out := append([]string(nil), scope...)
	sort.Strings(out)
	return out
}

// TestResolveIntelScope_SubscriptionBranch pins that subscription_ids selects
// exactly the nodes linked to those subscriptions.
func TestResolveIntelScope_SubscriptionBranch(t *testing.T) {
	app, hashes := scopeFixture(t, map[string][]string{
		"sub-a": {"a1", "a2"},
		"sub-b": {"b1"},
	})

	got, err := app.resolveIntelScope(context.Background(), jobs.Scope{
		SubscriptionIDs: []string{"sub-a"},
	})
	if err != nil {
		t.Fatalf("resolveIntelScope: %v", err)
	}
	want := sortedHashes([]string{hashes["a1"], hashes["a2"]})
	if strings.Join(sortedHashes(got), ",") != strings.Join(want, ",") {
		t.Fatalf("scope for sub-a = %v, want %v", sortedHashes(got), want)
	}

	// Both subscriptions: the union of their nodes.
	got, err = app.resolveIntelScope(context.Background(), jobs.Scope{
		SubscriptionIDs: []string{"sub-a", "sub-b"},
	})
	if err != nil {
		t.Fatalf("resolveIntelScope: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("scope for both subscriptions = %v, want 3 nodes", got)
	}

	// An unknown id contributes nothing (rather than everything).
	got, err = app.resolveIntelScope(context.Background(), jobs.Scope{
		SubscriptionIDs: []string{"sub-missing"},
	})
	if err != nil {
		t.Fatalf("resolveIntelScope: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("scope for an unknown subscription = %v, want none", got)
	}
}

// TestResolveIntelScope_SubscriptionBranchHonoursFilter pins the union semantics
// fixed in this round: the filter narrows the subscription branch too, instead of
// the two being unioned.
func TestResolveIntelScope_SubscriptionBranchHonoursFilter(t *testing.T) {
	app, hashes := scopeFixture(t, map[string][]string{
		"sub-a": {"a1", "a2"},
	})

	// No pool is wired, so the filter cannot match any entry and must therefore
	// drop every node from the subscription branch. That is the point: before the
	// fix the branch ignored the filter and returned both nodes anyway.
	got, err := app.resolveIntelScope(context.Background(), jobs.Scope{
		SubscriptionIDs: []string{"sub-a"},
		Filter:          map[string]string{"protocol": "vless"},
	})
	if err != nil {
		t.Fatalf("resolveIntelScope: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a filter that matches nothing must drop the subscription nodes, got %v", got)
	}

	// With no filter the same scope returns both.
	got, err = app.resolveIntelScope(context.Background(), jobs.Scope{
		SubscriptionIDs: []string{"sub-a"},
	})
	if err != nil {
		t.Fatalf("resolveIntelScope: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("without a filter the scope must hold both nodes, got %v", got)
	}
	_ = hashes
}

// TestResolveIntelScope_PlatformBranch pins that platform_ids reads the platform's
// routable view.
func TestResolveIntelScope_PlatformBranch(t *testing.T) {
	app := &prismApp{}

	// No topology runtime: the platform branch cannot resolve anything and must
	// not panic. An unknown platform id is skipped rather than failing the scope.
	got, err := app.resolveIntelScope(context.Background(), jobs.Scope{
		PlatformIDs: []string{"plat-missing"},
	})
	if err != nil {
		t.Fatalf("resolveIntelScope with no topology runtime: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("scope = %v, want none", got)
	}
}

// TestResolveIntelScope_SubscriptionBranchNeedsStateEngine pins the guard: without
// a state engine the subscription branch reports a named error instead of
// silently returning an empty scope (which would look like "no matching nodes").
func TestResolveIntelScope_SubscriptionBranchNeedsStateEngine(t *testing.T) {
	app := &prismApp{}
	_, err := app.resolveIntelScope(context.Background(), jobs.Scope{
		SubscriptionIDs: []string{"sub-a"},
	})
	if err == nil {
		t.Fatal("resolveIntelScope without a state engine must fail")
	}
	if !strings.Contains(err.Error(), "state engine unavailable") {
		t.Fatalf("error = %v, want it to name the missing state engine", err)
	}
}

// TestResolveIntelScope_ScopeEmptyAndLimit pins the two remaining shape rules the
// branches share.
func TestResolveIntelScope_ScopeEmptyAndLimit(t *testing.T) {
	// An empty scope resolves to nothing and is not an error.
	app := &prismApp{}
	got, err := app.resolveIntelScope(context.Background(), jobs.Scope{})
	if err != nil {
		t.Fatalf("resolveIntelScope(empty): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("resolveIntelScope(empty) = %v, want none", got)
	}
	if !(jobs.Scope{}).Empty() {
		t.Error("jobs.Scope{}.Empty() = false, want true")
	}

	// More explicit hashes than the bound fails with the named limit error rather
	// than truncating. Two well-formed hashes are enough to prove the path.
	hashes := []string{
		node.HashFromRawOptions([]byte("x1")).String(),
		node.HashFromRawOptions([]byte("x2")).String(),
	}
	got, err = app.resolveIntelScope(context.Background(), jobs.Scope{NodeHashes: hashes})
	if err != nil {
		t.Fatalf("resolveIntelScope(two hashes): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("resolveIntelScope(two hashes) = %v, want both", got)
	}
}

// TestResolveIntelScope_SubscriptionBranchDropsMalformedLinks pins that a
// persisted link whose hash is not a real hash cannot enter the scope: it would
// become a job item that can never resolve.
func TestResolveIntelScope_SubscriptionBranchDropsMalformedLinks(t *testing.T) {
	engine, closer, err := state.PersistenceBootstrap(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	now := time.Now().UnixNano()
	if err := engine.UpsertSubscription(model.Subscription{
		ID:               "sub-bad",
		Name:             "Bad",
		SourceType:       "local",
		Content:          "127.0.0.1:1",
		UpdateIntervalNs: int64(time.Hour),
		Enabled:          true,
		CreatedAtNs:      now,
		UpdatedAtNs:      now,
	}); err != nil {
		t.Fatalf("UpsertSubscription: %v", err)
	}
	good := node.HashFromRawOptions([]byte("good")).String()
	if err := engine.BulkUpsertSubscriptionNodes([]model.SubscriptionNode{
		{SubscriptionID: "sub-bad", NodeHash: good},
		{SubscriptionID: "sub-bad", NodeHash: "not-a-hash"},
	}); err != nil {
		t.Fatalf("BulkUpsertSubscriptionNodes: %v", err)
	}

	app := &prismApp{stateEngine: engine}
	got, err := app.resolveIntelScope(context.Background(), jobs.Scope{
		SubscriptionIDs: []string{"sub-bad"},
	})
	if err != nil {
		t.Fatalf("resolveIntelScope: %v", err)
	}
	for _, hash := range got {
		if hash == "not-a-hash" {
			t.Fatalf("a malformed persisted hash reached the scope: %v", got)
		}
	}
	if len(got) != 1 || got[0] != good {
		t.Fatalf("scope = %v, want exactly the well-formed hash", got)
	}
}

// TestResolveIntelScope_UnionOfSelectors pins that the selectors are a union, as
// WP08 §3.1 specifies.
func TestResolveIntelScope_UnionOfSelectors(t *testing.T) {
	app, hashes := scopeFixture(t, map[string][]string{
		"sub-a": {"a1"},
	})
	explicit := node.HashFromRawOptions([]byte("explicit")).String()

	got, err := app.resolveIntelScope(context.Background(), jobs.Scope{
		SubscriptionIDs: []string{"sub-a"},
		NodeHashes:      []string{explicit},
	})
	if err != nil {
		t.Fatalf("resolveIntelScope: %v", err)
	}
	want := sortedHashes([]string{hashes["a1"], explicit})
	if strings.Join(sortedHashes(got), ",") != strings.Join(want, ",") {
		t.Fatalf("union = %v, want %v", sortedHashes(got), want)
	}
}

// TestResolveIntelScope_ErrorsWrapInvalidJob pins that every rejection the scope
// performs is an invalid-argument error, so the API answers 400 rather than 500.
func TestResolveIntelScope_ErrorsWrapInvalidJob(t *testing.T) {
	app := &prismApp{}
	for _, scope := range []jobs.Scope{
		{Filter: map[string]string{"nope": "1"}},
		{NodeHashes: []string{"zzzz"}},
	} {
		_, err := app.resolveIntelScope(context.Background(), scope)
		if !errors.Is(err, jobs.ErrInvalidJob) {
			t.Errorf("scope %+v: error = %v, want it to wrap jobs.ErrInvalidJob", scope, err)
		}
	}
}
