package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"prism/internal/publicsource"
	"prism/internal/subscription"
)

// fakeDownloader stands in for the network in serve-mode tests.
type fakeDownloader struct {
	body string
	err  error
}

func (f fakeDownloader) Download(_ context.Context, _ string) ([]byte, error) {
	return []byte(f.body), f.err
}

func newServeTestCollector(t *testing.T) *publicsource.Collector {
	t.Helper()
	return publicsource.NewCollector(publicsource.Config{
		Enabled:  true,
		Interval: time.Hour,
		Sources:  []string{"https://example.com/list.txt"},
		MaxNodes: 10,
	}, fakeDownloader{body: "1.2.3.4:8080\n5.6.7.8:3128\n9.10.11.12:1080\n"}, nil)
}

// Before the first successful refresh the endpoint must not answer with an empty
// list: Resin would replace its nodes with nothing. 503 makes the pull fail
// safely instead.
func TestSnapshotServerIsUnavailableUntilFirstRefresh(t *testing.T) {
	collector := newServeTestCollector(t)
	handler := newSnapshotServer(collector, "/sub")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sub", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 before a refresh", rec.Code)
	}
}

func TestSnapshotServerServesAPayloadResinCanParse(t *testing.T) {
	collector := newServeTestCollector(t)
	if _, err := collector.RefreshNow(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	handler := newSnapshotServer(collector, "/sub")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sub", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Node-Count"); got != "3" {
		t.Fatalf("X-Node-Count = %q, want 3", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}

	// The whole point: what we serve must parse with the parser Resin itself uses.
	parsed, err := subscription.ParseGeneralSubscription(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("Resin's parser rejected the served payload: %v", err)
	}
	if len(parsed) != 3 {
		t.Fatalf("Resin's parser found %d nodes, want 3", len(parsed))
	}
}

func TestSnapshotServerHealthAndStatus(t *testing.T) {
	collector := newServeTestCollector(t)
	if _, err := collector.RefreshNow(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	handler := newSnapshotServer(collector, "/sub")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status endpoint = %d", rec.Code)
	}
	var payload struct {
		Nodes       int    `json:"nodes"`
		Bytes       int    `json:"bytes"`
		Candidates  int    `json:"candidates"`
		SourceCount int    `json:"source_count"`
		Sources     []any  `json:"sources"`
		LastError   string `json:"last_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("status body is not JSON: %v", err)
	}
	if payload.Nodes != 3 || payload.Bytes == 0 || payload.SourceCount != 1 {
		t.Fatalf("unexpected status payload: %+v", payload)
	}
	if payload.LastError != "" {
		t.Fatalf("last_error = %q, want empty", payload.LastError)
	}
	// The status document must never carry node data.
	if strings.Contains(rec.Body.String(), `"outbounds"`) {
		t.Fatal("the status endpoint leaked node content")
	}
}

func TestSnapshotServerRoutingRules(t *testing.T) {
	collector := newServeTestCollector(t)
	if _, err := collector.RefreshNow(); err != nil {
		t.Fatal(err)
	}

	t.Run("unknown path is 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		newSnapshotServer(collector, "/sub").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("POST is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		newSnapshotServer(collector, "/sub").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sub", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
	})

	t.Run("HEAD returns headers without a body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		newSnapshotServer(collector, "/sub").ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/sub", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("HEAD returned %d body bytes", rec.Body.Len())
		}
		if rec.Header().Get("X-Node-Count") == "" {
			t.Fatal("HEAD must still expose the counters")
		}
	})

	t.Run("a secret can live in the path", func(t *testing.T) {
		secret := "/sub/8f3c1d9a"
		handler := newSnapshotServer(collector, secret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, secret, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sub", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("the bare path must not answer when a secret path is configured, got %d", rec.Code)
		}
	})

	t.Run("blank or root path falls back to /sub", func(t *testing.T) {
		for _, configured := range []string{"", "/", "   "} {
			server := newSnapshotServer(collector, configured)
			if server.path != "/sub" {
				t.Fatalf("path for %q = %q, want /sub", configured, server.path)
			}
		}
	})

	t.Run("a missing leading slash is added", func(t *testing.T) {
		if got := newSnapshotServer(collector, "sub").path; got != "/sub" {
			t.Fatalf("path = %q, want /sub", got)
		}
	})
}
