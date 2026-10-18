package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A panicking handler must answer like every other internal failure. Without
// the middleware net/http closes the connection and writes nothing, so the
// caller sees a transport error where the contract promises a JSON body.
func TestPanicRecoveryAnswersTheGenericInternalError(t *testing.T) {
	handler := PanicRecoveryMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom: a message that must not reach the caller")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/me/overview", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the response is not the documented error shape: %v (%q)", err, rec.Body.String())
	}
	if body.Error.Code != "INTERNAL" {
		t.Fatalf("code = %q, want INTERNAL", body.Error.Code)
	}
	if body.Error.Message != "internal error" {
		t.Fatalf("message = %q, want the generic text", body.Error.Message)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("the panic message reached the caller: %q", rec.Body.String())
	}
}

// The same 500 has to be what a database failure produces, or the caller can
// tell a panic from an ordinary error by reading the body.
func TestPanicRecoveryMatchesTheOrdinaryInternalError(t *testing.T) {
	ordinary := httptest.NewRecorder()
	WriteError(ordinary, http.StatusInternalServerError, "INTERNAL", "internal error")

	panicked := httptest.NewRecorder()
	PanicRecoveryMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})).ServeHTTP(panicked, httptest.NewRequest(http.MethodGet, "/api/v1/me/overview", nil))

	if ordinary.Code != panicked.Code {
		t.Fatalf("status %d vs %d", ordinary.Code, panicked.Code)
	}
	if ordinary.Body.String() != panicked.Body.String() {
		t.Fatalf("a panic answers %q, an internal error answers %q", panicked.Body.String(), ordinary.Body.String())
	}
}

// http.ErrAbortHandler is how a handler aborts a response it has already
// started. Answering it with a 500 would write into a connection the handler
// gave up on, so the middleware has to let net/http do its own thing.
func TestPanicRecoveryLetsAnAbortedResponseAbort(t *testing.T) {
	handler := PanicRecoveryMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Fatalf("the abort was swallowed and recovered as %v", recovered)
		}
	}()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/me/overview", nil))
	t.Fatalf("the handler returned instead of aborting, and answered %d", rec.Code)
}

// A handler that does not panic has to pass through untouched.
func TestPanicRecoveryLeavesAServedResponseAlone(t *testing.T) {
	handler := PanicRecoveryMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("body = %q", rec.Body.String())
	}
}
