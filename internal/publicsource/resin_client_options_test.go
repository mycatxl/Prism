package publicsource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder captures every JSON body the client sends.
type recorder struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (r *recorder) add(body map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, body)
}

func (r *recorder) all() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.bodies...)
}

// The subscription settings must be applied on create *and* on update. An
// existing subscription that only ever received content would keep Resin's
// server-side defaults (ephemeral=false, incremental=true? no: false, 72h)
// forever, which is exactly the mismatch this guards against.
func TestUpsertLocalSubscriptionAppliesSettingsOnCreateAndUpdate(t *testing.T) {
	rec := &recorder{}
	var exists bool

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if exists {
				_, _ = w.Write([]byte(`{"items":[{"id":"sub-1","name":"Public sources","source_type":"local"}],"total":1}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[],"total":0}`))
		case http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			rec.add(body)
			exists = true
			_, _ = w.Write([]byte(`{"id":"sub-1"}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/subscriptions/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/actions/refresh") {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodPatch {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			rec.add(body)
		}
		w.WriteHeader(http.StatusOK)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := NewResinClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("NewResinClient() error = %v", err)
	}

	opts := LocalSubscriptionOptions{
		Ephemeral:               true,
		IncrementalAliveNodes:   true,
		EphemeralNodeEvictDelay: 15 * time.Minute,
	}
	ctx := context.Background()

	// First call creates, second updates.
	if err := client.UpsertLocalSubscription(ctx, "Public sources", `{"outbounds":[]}`, time.Hour, opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := client.UpsertLocalSubscription(ctx, "Public sources", `{"outbounds":[]}`, time.Hour, opts); err != nil {
		t.Fatalf("update: %v", err)
	}

	bodies := rec.all()
	if len(bodies) != 2 {
		t.Fatalf("expected a create and an update body, got %d", len(bodies))
	}

	for i, body := range bodies {
		call := "create"
		if i == 1 {
			call = "update"
		}
		if body["ephemeral"] != true {
			t.Fatalf("%s body: ephemeral = %#v, want true", call, body["ephemeral"])
		}
		if body["incremental_alive_nodes"] != true {
			t.Fatalf("%s body: incremental_alive_nodes = %#v, want true", call, body["incremental_alive_nodes"])
		}
		if body["ephemeral_node_evict_delay"] != "15m0s" {
			t.Fatalf("%s body: ephemeral_node_evict_delay = %#v, want 15m0s", call, body["ephemeral_node_evict_delay"])
		}
		if body["update_interval"] != "1h0m0s" {
			t.Fatalf("%s body: update_interval = %#v", call, body["update_interval"])
		}
	}

	// The create body carries the rest of the subscription definition.
	create := bodies[0]
	if create["source_type"] != "local" || create["enabled"] != true || create["name"] != "Public sources" {
		t.Fatalf("unexpected create body: %#v", create)
	}

	// The update body must never carry `enabled`, so an operator pausing the
	// subscription in the UI stays paused.
	if _, present := bodies[1]["enabled"]; present {
		t.Fatal("the update body must not touch the enabled flag")
	}
}

func TestUpsertLocalSubscriptionRejectsNegativeEvictDelay(t *testing.T) {
	client, err := NewResinClient("http://127.0.0.1:1", "token", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	err = client.UpsertLocalSubscription(ctx, "x", `{"outbounds":[]}`, time.Hour, LocalSubscriptionOptions{
		EphemeralNodeEvictDelay: -time.Minute,
	})
	if err == nil {
		t.Fatal("expected an error for a negative evict delay")
	}
	if !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpsertLocalSubscriptionZeroEvictDelayIsAllowed(t *testing.T) {
	rec := &recorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			rec.add(body)
			_, _ = w.Write([]byte(`{"id":"sub-1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	})
	mux.HandleFunc("/api/v1/subscriptions/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := NewResinClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.UpsertLocalSubscription(ctx, "x", `{"outbounds":[]}`, time.Hour, LocalSubscriptionOptions{}); err != nil {
		t.Fatalf("a zero evict delay must be accepted, got %v", err)
	}
	bodies := rec.all()
	if len(bodies) != 1 {
		t.Fatalf("expected one create body, got %d", len(bodies))
	}
	if bodies[0]["ephemeral"] != false {
		t.Fatalf("ephemeral = %#v, want false", bodies[0]["ephemeral"])
	}
	if bodies[0]["ephemeral_node_evict_delay"] != "0s" {
		t.Fatalf("evict delay = %#v, want 0s", bodies[0]["ephemeral_node_evict_delay"])
	}
}
