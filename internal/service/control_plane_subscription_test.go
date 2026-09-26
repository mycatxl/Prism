package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"prism/internal/state"
	"prism/internal/subscription"
	"prism/internal/topology"
)

// Subscription CRUD of internal/service. internal/api/api contract tests cover
// the happy paths and the update_interval/ephemeral_node_evict_delay limits
// through the handler; the branches below (the source_type combinations, the
// required/forbidden field matrix, the parse-report degradation paths) are only
// reachable through the service.

func newSubscriptionFixture(t *testing.T) (*ControlPlaneService, *topology.SubscriptionManager, *state.StateEngine) {
	t.Helper()
	engine, closeEngine := newStateEngineForTest(t)
	t.Cleanup(closeEngine)
	subMgr := topology.NewSubscriptionManager()
	return &ControlPlaneService{Engine: engine, SubMgr: subMgr}, subMgr, engine
}

func TestParseSubscriptionSourceType(t *testing.T) {
	tests := []struct {
		name    string
		raw     *string
		want    string
		wantErr bool
	}{
		{name: "absent defaults to remote", raw: nil, want: subscription.SourceTypeRemote},
		{name: "remote", raw: intelTestStrPtr("remote"), want: subscription.SourceTypeRemote},
		{name: "local", raw: intelTestStrPtr("local"), want: subscription.SourceTypeLocal},
		{name: "case and padding are ignored", raw: intelTestStrPtr("  LOCAL "), want: subscription.SourceTypeLocal},
		{name: "empty is rejected", raw: intelTestStrPtr(""), wantErr: true},
		{name: "unknown value is rejected", raw: intelTestStrPtr("ftp"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, verr := parseSubscriptionSourceType(tt.raw)
			if tt.wantErr {
				if verr == nil {
					t.Fatalf("parseSubscriptionSourceType(%v) = %q, want an error", tt.raw, got)
				}
				if verr.Code != "INVALID_ARGUMENT" || verr.Message != "source_type: must be remote or local" {
					t.Fatalf("error = %+v, want INVALID_ARGUMENT/source_type", verr)
				}
				return
			}
			if verr != nil {
				t.Fatalf("parseSubscriptionSourceType(%v) = %v", tt.raw, verr)
			}
			if got != tt.want {
				t.Fatalf("parseSubscriptionSourceType = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCreateSubscriptionValidation(t *testing.T) {
	cp, _, _ := newSubscriptionFixture(t)

	remoteURL := "https://example.com/sub"
	localContent := "127.0.0.1:8080"

	tests := []struct {
		name    string
		req     CreateSubscriptionRequest
		message string
		prefix  string
	}{
		{
			name:    "name is required",
			req:     CreateSubscriptionRequest{URL: intelTestStrPtr(remoteURL)},
			message: "name is required",
		},
		{
			name:    "blank name is rejected",
			req:     CreateSubscriptionRequest{Name: intelTestStrPtr("   "), URL: intelTestStrPtr(remoteURL)},
			message: "name is required",
		},
		{
			name:    "unknown source type",
			req:     CreateSubscriptionRequest{Name: intelTestStrPtr("sub"), SourceType: intelTestStrPtr("ftp"), URL: intelTestStrPtr(remoteURL)},
			message: "source_type: must be remote or local",
		},
		{
			name:    "remote without a url",
			req:     CreateSubscriptionRequest{Name: intelTestStrPtr("sub"), SourceType: intelTestStrPtr("remote")},
			message: "url is required for remote subscription",
		},
		{
			name:    "remote with a blank url",
			req:     CreateSubscriptionRequest{Name: intelTestStrPtr("sub"), URL: intelTestStrPtr("   ")},
			message: "url is required for remote subscription",
		},
		{
			name:   "remote with a relative url",
			req:    CreateSubscriptionRequest{Name: intelTestStrPtr("sub"), URL: intelTestStrPtr("/sub")},
			prefix: "url: ",
		},
		{
			name: "remote with content",
			req: CreateSubscriptionRequest{
				Name: intelTestStrPtr("sub"), URL: intelTestStrPtr(remoteURL),
				Content: intelTestStrPtr(localContent),
			},
			message: "content is not allowed for remote subscription",
		},
		{
			name:    "local without content",
			req:     CreateSubscriptionRequest{Name: intelTestStrPtr("sub"), SourceType: intelTestStrPtr("local")},
			message: "content is required for local subscription",
		},
		{
			name: "local with a blank content",
			req: CreateSubscriptionRequest{
				Name: intelTestStrPtr("sub"), SourceType: intelTestStrPtr("local"),
				Content: intelTestStrPtr("  "),
			},
			message: "content is required for local subscription",
		},
		{
			name: "local with a url",
			req: CreateSubscriptionRequest{
				Name: intelTestStrPtr("sub"), SourceType: intelTestStrPtr("local"),
				Content: intelTestStrPtr(localContent), URL: intelTestStrPtr(remoteURL),
			},
			message: "url is not allowed for local subscription",
		},
		{
			name: "unparsable update interval",
			req: CreateSubscriptionRequest{
				Name: intelTestStrPtr("sub"), URL: intelTestStrPtr(remoteURL),
				UpdateInterval: intelTestStrPtr("not-a-duration"),
			},
			prefix: "update_interval: ",
		},
		{
			name: "update interval below the minimum",
			req: CreateSubscriptionRequest{
				Name: intelTestStrPtr("sub"), URL: intelTestStrPtr(remoteURL),
				UpdateInterval: intelTestStrPtr("10s"),
			},
			message: "update_interval: must be >= 30s",
		},
		{
			name: "unparsable evict delay",
			req: CreateSubscriptionRequest{
				Name: intelTestStrPtr("sub"), URL: intelTestStrPtr(remoteURL),
				EphemeralNodeEvictDelay: intelTestStrPtr("not-a-duration"),
			},
			prefix: "ephemeral_node_evict_delay: ",
		},
		{
			name: "negative evict delay",
			req: CreateSubscriptionRequest{
				Name: intelTestStrPtr("sub"), URL: intelTestStrPtr(remoteURL),
				EphemeralNodeEvictDelay: intelTestStrPtr("-1s"),
			},
			message: "ephemeral_node_evict_delay: must be non-negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cp.CreateSubscription(tt.req)
			if !isServiceErrorCode(err, "INVALID_ARGUMENT") {
				t.Fatalf("CreateSubscription = %v, want INVALID_ARGUMENT", err)
			}
			var svcErr *ServiceError
			if !errors.As(err, &svcErr) {
				t.Fatalf("CreateSubscription = %T, want *ServiceError", err)
			}
			if tt.message != "" && svcErr.Message != tt.message {
				t.Fatalf("message = %q, want %q", svcErr.Message, tt.message)
			}
			if tt.prefix != "" && !strings.HasPrefix(svcErr.Message, tt.prefix) {
				t.Fatalf("message = %q, want prefix %q", svcErr.Message, tt.prefix)
			}
		})
	}
}

func TestCreateSubscription_RemoteAndLocal(t *testing.T) {
	cp, subMgr, engine := newSubscriptionFixture(t)

	t.Run("remote", func(t *testing.T) {
		resp, err := cp.CreateSubscription(CreateSubscriptionRequest{
			Name:                    intelTestStrPtr("  remote-sub  "),
			URL:                     intelTestStrPtr("  https://example.com/sub  "),
			SourceType:              intelTestStrPtr("REMOTE"),
			UpdateInterval:          intelTestStrPtr("45s"),
			Enabled:                 intelTestBoolPtr(false),
			Ephemeral:               intelTestBoolPtr(true),
			IncrementalAliveNodes:   intelTestBoolPtr(true),
			EphemeralNodeEvictDelay: intelTestStrPtr("30m"),
			AutoIntel:               intelTestBoolPtr(false),
		})
		if err != nil {
			t.Fatalf("CreateSubscription: %v", err)
		}
		if resp.ID == "" {
			t.Fatal("created subscription has no id")
		}
		if resp.Name != "remote-sub" {
			t.Errorf("name = %q, want the trimmed name", resp.Name)
		}
		if resp.SourceType != subscription.SourceTypeRemote || resp.URL != "https://example.com/sub" {
			t.Errorf("source/url = %q/%q", resp.SourceType, resp.URL)
		}
		if resp.Content != "" {
			t.Errorf("content = %q, want empty for a remote subscription", resp.Content)
		}
		if resp.UpdateInterval != "45s" || resp.EphemeralNodeEvictDelay != "30m0s" {
			t.Errorf("intervals = %q/%q", resp.UpdateInterval, resp.EphemeralNodeEvictDelay)
		}
		if resp.Enabled || !resp.Ephemeral || !resp.IncrementalAliveNodes || resp.AutoIntel {
			t.Errorf("flags = enabled:%v ephemeral:%v incremental:%v auto_intel:%v",
				resp.Enabled, resp.Ephemeral, resp.IncrementalAliveNodes, resp.AutoIntel)
		}
		if resp.CreatedAt == "" {
			t.Error("created_at must be rendered")
		}
		if resp.NodeCount != 0 || resp.HealthyNodeCount != 0 {
			t.Errorf("a fresh subscription has no nodes: %+v", resp)
		}

		if subMgr.Lookup(resp.ID) == nil {
			t.Fatal("the created subscription was not registered in the runtime manager")
		}
		rows, err := engine.ListSubscriptions()
		if err != nil {
			t.Fatalf("ListSubscriptions: %v", err)
		}
		found := false
		for _, row := range rows {
			if row.ID != resp.ID {
				continue
			}
			found = true
			if row.SourceType != subscription.SourceTypeRemote || row.URL != "https://example.com/sub" {
				t.Errorf("persisted row = %+v", row)
			}
			if row.UpdateIntervalNs != int64(45*time.Second) {
				t.Errorf("persisted update_interval = %d, want 45s", row.UpdateIntervalNs)
			}
			if row.EphemeralNodeEvictDelayNs != int64(30*time.Minute) {
				t.Errorf("persisted evict delay = %d, want 30m", row.EphemeralNodeEvictDelayNs)
			}
			if row.Enabled || !row.Ephemeral || !row.IncrementalAliveNodes || row.AutoIntel {
				t.Errorf("persisted flags = %+v", row)
			}
		}
		if !found {
			t.Fatal("the created subscription is missing from the persisted rows")
		}
	})

	t.Run("local", func(t *testing.T) {
		resp, err := cp.CreateSubscription(CreateSubscriptionRequest{
			Name:                  intelTestStrPtr("local-sub"),
			SourceType:            intelTestStrPtr("local"),
			Content:               intelTestStrPtr("127.0.0.1:8080"),
			AutoIntel:             intelTestBoolPtr(true),
			Enabled:               intelTestBoolPtr(true),
			IncrementalAliveNodes: intelTestBoolPtr(false),
		})
		if err != nil {
			t.Fatalf("CreateSubscription(local): %v", err)
		}
		if resp.SourceType != subscription.SourceTypeLocal || resp.Content != "127.0.0.1:8080" || resp.URL != "" {
			t.Fatalf("local response = %+v", resp)
		}
		// createReply carries the documented defaults.
		if resp.UpdateInterval != (5*time.Minute).String() || resp.EphemeralNodeEvictDelay != (72*time.Hour).String() {
			t.Errorf("default intervals = %q/%q", resp.UpdateInterval, resp.EphemeralNodeEvictDelay)
		}
		if !resp.AutoIntel || !resp.Enabled || resp.Ephemeral {
			t.Errorf("default flags = %+v", resp)
		}
	})
}

func TestListSubscriptionsAndGetSubscription(t *testing.T) {
	cp, _, _ := newSubscriptionFixture(t)

	enabled, err := cp.CreateSubscription(CreateSubscriptionRequest{
		Name: intelTestStrPtr("enabled-sub"),
		URL:  intelTestStrPtr("https://example.com/enabled"),
	})
	if err != nil {
		t.Fatalf("CreateSubscription(enabled): %v", err)
	}
	disabled, err := cp.CreateSubscription(CreateSubscriptionRequest{
		Name:    intelTestStrPtr("disabled-sub"),
		URL:     intelTestStrPtr("https://example.com/disabled"),
		Enabled: intelTestBoolPtr(false),
	})
	if err != nil {
		t.Fatalf("CreateSubscription(disabled): %v", err)
	}

	all, err := cp.ListSubscriptions(nil)
	if err != nil {
		t.Fatalf("ListSubscriptions(nil): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListSubscriptions(nil) = %d, want 2", len(all))
	}

	onlyEnabled, err := cp.ListSubscriptions(intelTestBoolPtr(true))
	if err != nil {
		t.Fatalf("ListSubscriptions(true): %v", err)
	}
	if len(onlyEnabled) != 1 || onlyEnabled[0].ID != enabled.ID {
		t.Fatalf("ListSubscriptions(true) = %+v, want only %s", onlyEnabled, enabled.ID)
	}

	onlyDisabled, err := cp.ListSubscriptions(intelTestBoolPtr(false))
	if err != nil {
		t.Fatalf("ListSubscriptions(false): %v", err)
	}
	if len(onlyDisabled) != 1 || onlyDisabled[0].ID != disabled.ID {
		t.Fatalf("ListSubscriptions(false) = %+v, want only %s", onlyDisabled, disabled.ID)
	}

	got, err := cp.GetSubscription(enabled.ID)
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if got.ID != enabled.ID || got.SourceType != subscription.SourceTypeRemote || got.UpdateInterval != (5*time.Minute).String() {
		t.Fatalf("GetSubscription = %+v", got)
	}

	if _, err := cp.GetSubscription("11111111-2222-3333-4444-555555555555"); !isServiceErrorCode(err, "NOT_FOUND") {
		t.Fatalf("GetSubscription(unknown) = %v, want NOT_FOUND", err)
	}

	// An empty manager answers with an empty (never nil) list.
	empty := &ControlPlaneService{SubMgr: topology.NewSubscriptionManager()}
	list, err := empty.ListSubscriptions(nil)
	if err != nil {
		t.Fatalf("ListSubscriptions(empty): %v", err)
	}
	if list == nil || len(list) != 0 {
		t.Fatalf("ListSubscriptions(empty) = %#v, want an empty slice", list)
	}
}

func TestGetSubscriptionParseReportBranches(t *testing.T) {
	cp, subMgr, engine := newSubscriptionFixture(t)

	if _, err := cp.GetSubscriptionParseReport("11111111-2222-3333-4444-555555555555"); !isServiceErrorCode(err, "NOT_FOUND") {
		t.Fatalf("GetSubscriptionParseReport(unknown) = %v, want NOT_FOUND", err)
	}

	// A live subscription whose state row is gone reports "not parsed yet"
	// instead of hiding the subscription behind a 404.
	live := subscription.NewSubscription("sub-live-only", "live", "https://example.com/live", true, false)
	subMgr.Register(live)
	resp, err := cp.GetSubscriptionParseReport(live.ID)
	if err != nil {
		t.Fatalf("GetSubscriptionParseReport(live only): %v", err)
	}
	if resp.Parsed || resp.Truncated || resp.SubscriptionID != live.ID {
		t.Fatalf("live-only report = %+v", resp)
	}
	if resp.Skipped == nil || len(resp.Skipped) != 0 || resp.Summary.Reasons == nil || len(resp.Summary.Reasons) != 0 {
		t.Fatalf("an unparsed report must carry empty (never nil) lists: %+v", resp)
	}

	created, err := cp.CreateSubscription(CreateSubscriptionRequest{
		Name: intelTestStrPtr("report-sub"),
		URL:  intelTestStrPtr("https://example.com/report"),
	})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	// Persisted, never parsed.
	resp, err = cp.GetSubscriptionParseReport(created.ID)
	if err != nil {
		t.Fatalf("GetSubscriptionParseReport: %v", err)
	}
	if resp.Parsed || resp.SubscriptionID != created.ID {
		t.Fatalf("fresh report = %+v", resp)
	}

	// An explicitly empty report is treated like "not parsed yet".
	if err := engine.SetSubscriptionParseReport(created.ID, ""); err != nil {
		t.Fatalf("SetSubscriptionParseReport(empty): %v", err)
	}
	resp, err = cp.GetSubscriptionParseReport(created.ID)
	if err != nil {
		t.Fatalf("GetSubscriptionParseReport(empty): %v", err)
	}
	if resp.Parsed || resp.Truncated {
		t.Fatalf("empty report = %+v", resp)
	}

	// The 64 KiB store marker: the report was dropped, the size is reported.
	if err := engine.SetSubscriptionParseReport(created.ID, `{"truncated":true,"original_bytes":70000}`); err != nil {
		t.Fatalf("SetSubscriptionParseReport(marker): %v", err)
	}
	resp, err = cp.GetSubscriptionParseReport(created.ID)
	if err != nil {
		t.Fatalf("GetSubscriptionParseReport(marker): %v", err)
	}
	if !resp.Truncated || resp.OriginalBytes != 70000 || resp.Parsed {
		t.Fatalf("truncated report = %+v", resp)
	}
	if len(resp.Skipped) != 0 || resp.Summary.Total != 0 {
		t.Fatalf("a truncated report carries no details: %+v", resp)
	}

	// A real parse result is decoded and summarised.
	report := `{"skipped":[{"name":"drop-ssr","type":"ssr","source":"sub","reason":"ENGINE_NOT_BUILT","detail":"kernel missing"}],` +
		`"stats":{"total":3,"imported":2,"skipped":1,"by_engine":{"ssr":1},"by_protocol":{}}}`
	if err := engine.SetSubscriptionParseReport(created.ID, report); err != nil {
		t.Fatalf("SetSubscriptionParseReport(report): %v", err)
	}
	resp, err = cp.GetSubscriptionParseReport(created.ID)
	if err != nil {
		t.Fatalf("GetSubscriptionParseReport(report): %v", err)
	}
	if !resp.Parsed || resp.Truncated {
		t.Fatalf("parsed report = %+v", resp)
	}
	if resp.Stats.Total != 3 || resp.Stats.Imported != 2 || resp.Stats.Skipped != 1 {
		t.Fatalf("stats = %+v", resp.Stats)
	}
	if len(resp.Skipped) != 1 || resp.Skipped[0].Name != "drop-ssr" || resp.Skipped[0].Reason != "ENGINE_NOT_BUILT" {
		t.Fatalf("skipped = %+v", resp.Skipped)
	}
	if resp.Summary.Total != 3 || resp.Summary.Imported != 2 || resp.Summary.Skipped != 1 {
		t.Fatalf("summary = %+v", resp.Summary)
	}
	if len(resp.Summary.Reasons) != 1 || resp.Summary.Reasons[0].Reason != "ENGINE_NOT_BUILT" || resp.Summary.Reasons[0].Count != 1 {
		t.Fatalf("summary reasons = %+v", resp.Summary.Reasons)
	}

	// A report that parses as the marker shape but not as a parse result is an
	// internal error, not a silent empty answer.
	if err := engine.SetSubscriptionParseReport(created.ID, `{"stats":{"total":"nope"}}`); err != nil {
		t.Fatalf("SetSubscriptionParseReport(broken): %v", err)
	}
	if _, err := cp.GetSubscriptionParseReport(created.ID); !isServiceErrorCode(err, "INTERNAL") {
		t.Fatalf("GetSubscriptionParseReport(broken) = %v, want INTERNAL", err)
	}

	// Without state storage the live runtime view is all that is available.
	bare := &ControlPlaneService{SubMgr: subMgr}
	resp, err = bare.GetSubscriptionParseReport(created.ID)
	if err != nil {
		t.Fatalf("GetSubscriptionParseReport(without state storage): %v", err)
	}
	if resp.Parsed || resp.SubscriptionID != created.ID {
		t.Fatalf("report without state storage = %+v", resp)
	}
}

// A subscription write or read against a broken state store is an INTERNAL
// error; the runtime manager must not keep a half-created subscription either.
func TestSubscriptionStoreFailures(t *testing.T) {
	engine, closeEngine := newStateEngineForTest(t)
	subMgr := topology.NewSubscriptionManager()
	cp := &ControlPlaneService{Engine: engine, SubMgr: subMgr}

	created, err := cp.CreateSubscription(CreateSubscriptionRequest{
		Name: intelTestStrPtr("broken-store"),
		URL:  intelTestStrPtr("https://example.com/broken"),
	})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	closeEngine()

	resp, err := cp.CreateSubscription(CreateSubscriptionRequest{
		Name: intelTestStrPtr("after-close"),
		URL:  intelTestStrPtr("https://example.com/after"),
	})
	if !isServiceErrorCode(err, "INTERNAL") {
		t.Fatalf("CreateSubscription with a broken store = %v, want INTERNAL", err)
	}
	if resp != nil {
		t.Fatal("a subscription that could not be persisted must not be returned")
	}
	listed, err := cp.ListSubscriptions(nil)
	if err != nil {
		t.Fatalf("ListSubscriptions: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("runtime subscriptions = %+v, want only the persisted one", listed)
	}
	if _, err := cp.GetSubscriptionParseReport(created.ID); !isServiceErrorCode(err, "INTERNAL") {
		t.Fatalf("GetSubscriptionParseReport with a broken store = %v, want INTERNAL", err)
	}
}
