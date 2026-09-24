package publicsource

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResinClientUpsertLocalSubscriptionCreateThenUpdate(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	var patchBody map[string]any
	listCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer admin-token" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/subscriptions":
			listCount++
			if listCount == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "total": 0})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{
				map[string]any{"id": "sub-1", "name": "Public sources", "source_type": "local"},
			}, "total": 1})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/subscriptions":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sub-1", "name": "Public sources", "source_type": "local"})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/subscriptions/sub-1":
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &patchBody)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/subscriptions/sub-1/actions/refresh":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewResinClient(server.URL, "admin-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.UpsertLocalSubscription(ctx, "Public sources", `{"outbounds":[]}`, time.Minute, LocalSubscriptionOptions{}); err != nil {
		t.Fatalf("create sync failed: %v", err)
	}
	if err := client.UpsertLocalSubscription(ctx, "Public sources", `{"outbounds":[{"type":"http"}]}`, time.Minute, LocalSubscriptionOptions{}); err != nil {
		t.Fatalf("update sync failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"GET /api/v1/subscriptions",
		"POST /api/v1/subscriptions",
		"POST /api/v1/subscriptions/sub-1/actions/refresh",
		"GET /api/v1/subscriptions",
		"PATCH /api/v1/subscriptions/sub-1",
		"POST /api/v1/subscriptions/sub-1/actions/refresh",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls[%d] = %q, want %q", i, calls[i], want[i])
		}
	}
	// Updating must not fight an operator who disabled the subscription.
	if _, present := patchBody["enabled"]; present {
		t.Fatalf("PATCH must not force enabled: %#v", patchBody)
	}
	if patchBody["content"] == nil {
		t.Fatalf("PATCH must send content: %#v", patchBody)
	}
}

func TestResinClientRejectsCredentialsInBaseURL(t *testing.T) {
	if _, err := NewResinClient("http://user:pass@127.0.0.1:2260", "tok", nil); err == nil {
		t.Fatal("expected error for base URL with credentials")
	}
}

func TestResinClientRejectsEmptyToken(t *testing.T) {
	if _, err := NewResinClient("http://127.0.0.1:2260", "  ", nil); err == nil {
		t.Fatal("expected error for empty admin token")
	}
}

func TestResinClientDoesNotFollowRedirects(t *testing.T) {
	var redirected int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer source.Close()

	client, err := NewResinClient(source.URL, "admin-token", source.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = client.UpsertLocalSubscription(context.Background(), "Public sources", `{"outbounds":[]}`, time.Minute, LocalSubscriptionOptions{})
	if err == nil {
		t.Fatal("expected redirect to surface as an error")
	}
	if redirected != 0 {
		t.Fatalf("admin token must never be replayed to a redirect target, visited %d times", redirected)
	}
}

func TestResinClientReportsStructuredErrorCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "CONFLICT", "message": "platform name already exists"},
		})
	}))
	defer server.Close()

	client, err := NewResinClient(server.URL, "admin-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = client.UpsertLocalSubscription(context.Background(), "Public sources", `{"outbounds":[]}`, time.Minute, LocalSubscriptionOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); !strings.Contains(got, "CONFLICT") {
		t.Fatalf("error must expose the structured code, got %q", got)
	}
}
