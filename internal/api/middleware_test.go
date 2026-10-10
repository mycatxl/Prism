package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthMiddleware_ValidToken(t *testing.T) {
	handler := AuthMiddleware("secret-token", prismAuthLimiter(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAuthMiddleware_MissingHeader(t *testing.T) {
	handler := AuthMiddleware("secret-token", prismAuthLimiter(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	assertBodyContains(t, rec, "UNAUTHORIZED")
}

func TestAuthMiddleware_WrongToken(t *testing.T) {
	handler := AuthMiddleware("secret-token", prismAuthLimiter(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	assertBodyContains(t, rec, "UNAUTHORIZED")
}

func TestAuthMiddleware_InvalidFormat(t *testing.T) {
	handler := AuthMiddleware("secret-token", prismAuthLimiter(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_EmptyConfiguredToken_DisablesAuth(t *testing.T) {
	handler := AuthMiddleware("", prismAuthLimiter(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestRequestBodyLimitMiddleware_TooLarge(t *testing.T) {
	handler := RequestBodyLimitMiddleware(4, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		t.Fatalf("unexpected read error: %v", err)
	}))

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader("12345"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func assertBodyContains(t *testing.T, rec *httptest.ResponseRecorder, substr string) {
	t.Helper()
	body := rec.Body.String()
	if !strings.Contains(body, substr) {
		t.Errorf("body %q does not contain %q", body, substr)
	}
}

// prismAuthLimiter builds a failure limiter for middleware cases. The limiter
// was added by the WP04 security work; the upstream cases only exercise the
// token check, so an effectively unbounded limiter keeps their intent.
func prismAuthLimiter() *AuthFailureLimiter {
	return NewAuthFailureLimiter(1000, time.Minute, time.Minute, nil)
}

// TestListenerServerConnectionBounds pins the listener-level connection hygiene
// on the constructor every served listener uses.
//
// It used to assert the fields of a *Server.httpServer that nothing ever
// served: cmd/prism only took Handler(), so the assertion guaranteed nothing
// about the process (audit F8). The served listeners are built in cmd/prism
// through NewListenerServer, and cmd/prism/connection_bounds_test.go asserts
// both real serve sites use it; this case pins the values NewListenerServer
// applies, including that the SSE stream is not cut.
func TestListenerServerConnectionBounds(t *testing.T) {
	httpSrv := NewListenerServer(nil)
	if httpSrv == nil {
		t.Fatal("NewListenerServer returned nil")
	}
	if httpSrv.Handler == nil {
		t.Error("Handler = nil, want a non-nil handler (net/http panics on a nil Handler)")
	}

	if httpSrv.ReadHeaderTimeout != ReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %v, want %v", httpSrv.ReadHeaderTimeout, ReadHeaderTimeout)
	}
	if httpSrv.IdleTimeout != IdleTimeout {
		t.Errorf("IdleTimeout = %v, want %v", httpSrv.IdleTimeout, IdleTimeout)
	}
	if httpSrv.MaxHeaderBytes != MaxHeaderBytes {
		t.Errorf("MaxHeaderBytes = %d, want %d", httpSrv.MaxHeaderBytes, MaxHeaderBytes)
	}
	if httpSrv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0 (a deadline would cut the SSE stream)", httpSrv.WriteTimeout)
	}
	// ReadTimeout feeds idleTimeout()/readHeaderTimeout() as a fallback and would
	// also bound a streaming response, so it must stay unset.
	if httpSrv.ReadTimeout != 0 {
		t.Errorf("ReadTimeout = %v, want 0 (ReadHeaderTimeout already bounds the header phase)", httpSrv.ReadTimeout)
	}

	// The bounds must be non-zero: a zero ReadHeaderTimeout is exactly the
	// slowloris hole this constructor exists to close.
	if ReadHeaderTimeout <= 0 || IdleTimeout <= 0 || MaxHeaderBytes <= 0 {
		t.Errorf("connection bounds must be positive: readHeader=%v idle=%v maxHeader=%d",
			ReadHeaderTimeout, IdleTimeout, MaxHeaderBytes)
	}
}
