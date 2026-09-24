package publicsource

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The subscription create/update endpoints echo the subscription back, content
// included, so pushing a large local subscription produces a large response.
// The client must stream that response rather than rejecting it at a fixed
// buffer limit — otherwise a successful push looks like a failure and the sync
// retries content the server already applied.
func TestResinClientAcceptsLargeEchoedResponse(t *testing.T) {
	const contentSize = 8 << 20 // comfortably over the old 4 MiB cap

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"sub-1","name":"Public sources","content":%q}`,
			strings.Repeat("x", contentSize))
	}))
	defer server.Close()

	client, err := NewResinClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("NewResinClient() error = %v", err)
	}

	var got struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	ctx := context.Background()
	if err := client.doJSON(ctx, http.MethodPost, "/api/v1/subscriptions", map[string]string{"name": "x"}, &got); err != nil {
		t.Fatalf("doJSON() error = %v", err)
	}
	if got.ID != "sub-1" || got.Name != "Public sources" {
		t.Fatalf("unexpected decode: id=%q name=%q", got.ID, got.Name)
	}
	if len(got.Content) != contentSize {
		t.Fatalf("content length = %d, want %d", len(got.Content), contentSize)
	}
}

// A response with no body must not be treated as a decode error.
func TestResinClientToleratesEmptySuccessBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewResinClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("NewResinClient() error = %v", err)
	}

	var out map[string]any
	ctx := context.Background()
	if err := client.doJSON(ctx, http.MethodPost, "/x", nil, &out); err != nil {
		t.Fatalf("doJSON() on an empty body error = %v", err)
	}
}

// A 413 from Resin must still surface as a readable, structured error.
func TestResinClientSurfacesStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte(`{"error":{"code":"PAYLOAD_TOO_LARGE","message":"request body exceeds 1048576 bytes"}}`))
	}))
	defer server.Close()

	client, err := NewResinClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("NewResinClient() error = %v", err)
	}

	ctx := context.Background()
	err = client.doJSON(ctx, http.MethodPost, "/api/v1/subscriptions", map[string]string{"content": "x"}, nil)
	if err == nil {
		t.Fatal("expected an error for a 413 response")
	}
	if !strings.Contains(err.Error(), "PAYLOAD_TOO_LARGE") {
		t.Fatalf("error does not carry the structured code: %v", err)
	}
}

// An error body must stay bounded: a huge error payload must not be buffered in
// full just to build a message.
func TestResinClientBoundsErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("E", 4<<20)))
	}))
	defer server.Close()

	client, err := NewResinClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("NewResinClient() error = %v", err)
	}

	ctx := context.Background()
	err = client.doJSON(ctx, http.MethodGet, "/x", nil, nil)
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if len(err.Error()) > (errorBodyLimit + 512) {
		t.Fatalf("error message is %d bytes, expected it to be bounded near %d",
			len(err.Error()), errorBodyLimit)
	}
}

// With no destination the body must still be drained, so the connection is left
// in a reusable state instead of half-read.
func TestResinClientDrainsWhenNoDestination(t *testing.T) {
	handled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("y", 1<<20)))
		close(handled)
	}))
	defer server.Close()

	client, err := NewResinClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatalf("NewResinClient() error = %v", err)
	}

	ctx := context.Background()
	if err := client.doJSON(ctx, http.MethodPost, "/x", map[string]string{"a": "b"}, nil); err != nil {
		t.Fatalf("doJSON() error = %v", err)
	}
	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("the server handler never completed")
	}
}
