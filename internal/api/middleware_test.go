package api

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
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
// was added by the security work; the upstream cases only exercise the
// token check, so an effectively unbounded limiter keeps their intent.
func prismAuthLimiter() *AuthFailureLimiter {
	return NewAuthFailureLimiter(1000, time.Minute, time.Minute, nil)
}

// TestListenerServerConnectionBounds pins the listener-level connection hygiene
// on the constructor every served listener uses.
//
// It used to assert the fields of a *Server.httpServer that nothing ever
// served: cmd/prism only took Handler(), so the assertion guaranteed nothing
// about the process (audit). The served listeners are built in cmd/prism
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
	if RequestBodyTimeout <= 0 {
		t.Errorf("RequestBodyTimeout = %v, want a positive body-phase bound", RequestBodyTimeout)
	}
}

// TestRequestBodyDeadlineBoundsOnlyRequestsWithABody pins the second half of:
// ReadHeaderTimeout stops at the end of the header block, and ReadTimeout has to
// stay unset for the SSE stream, so the body phase would otherwise be unbounded.
//
// The middleware must arm the read deadline for a request that carries a body and
// leave every other request alone - in particular the SSE endpoint, which is a
// GET with no body and is meant to stay open for the whole life of a job. The
// switch itself is asserted directly; the deadline reaching the connection is
// covered end-to-end by TestRequestBodyDeadlineAbortsASlowBody below, which needs
// a real net.Conn.
func TestRequestBodyDeadlineBoundsOnlyRequestsWithABody(t *testing.T) {
	// hasRequestBody is the switch the middleware turns on.
	if hasRequestBody(httptest.NewRequest(http.MethodGet, "/x", nil)) {
		t.Error("a bodyless GET was reported as carrying a body")
	}
	if !hasRequestBody(httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("a"))) {
		t.Error("a POST with a body was reported as bodyless")
	}
	// A chunked request has ContentLength -1 and still carries a body.
	chunked := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("a"))
	chunked.ContentLength = -1
	if !hasRequestBody(chunked) {
		t.Error("a chunked request (unknown length) must count as carrying a body")
	}
	if hasRequestBody(&http.Request{Body: http.NoBody}) {
		t.Error("http.NoBody must count as bodyless")
	}

	// A body-carrying request still passes through untouched.
	handler := RequestBodyDeadlineMiddleware(time.Minute, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(body)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/platforms", strings.NewReader(`{"name":"p"}`)))
	if rec.Body.String() != `{"name":"p"}` {
		t.Fatalf("body = %q, want it passed through unchanged", rec.Body.String())
	}

	// The SSE shape - a GET with no body - must be left alone.
	streamed := false
	streamHandler := RequestBodyDeadlineMiddleware(time.Minute, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamed = true
		_, _ = w.Write([]byte("data: frame\n\n"))
	}))
	rec2 := httptest.NewRecorder()
	streamHandler.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/v1/intel/jobs/j1/events", nil))
	if !streamed || rec2.Body.String() != "data: frame\n\n" {
		t.Fatalf("SSE-shaped request was disturbed: streamed=%v body=%q", streamed, rec2.Body.String())
	}
}

// TestRequestBodyDeadlineAbortsASlowBody proves the deadline really reaches the
// connection: a client that announces a body and then stalls is cut off instead
// of holding the connection, while a normal request over the same middleware is
// served.
//
// The assertion is on the handler's read, not on a status code: once the read
// deadline expires net/http drops the connection rather than letting the handler
// answer, and that is the outcome the bound exists to produce.
func TestRequestBodyDeadlineAbortsASlowBody(t *testing.T) {
	const timeout = 200 * time.Millisecond
	readErr := make(chan error, 1)
	handler := RequestBodyDeadlineMiddleware(timeout, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		readErr <- err
		if err != nil {
			http.Error(w, "read body", http.StatusRequestTimeout)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	srv := NewListenerServer(handler)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	// A slow body: the headers are complete, so ReadHeaderTimeout no longer
	// applies, and the client then stalls.
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "POST /x HTTP/1.1\r\nHost: t\r\nContent-Length: 10\r\n\r\n"); err != nil {
		t.Fatalf("write headers: %v", err)
	}

	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("the stalled body was read without an error: the body phase is unbounded")
		}
		t.Logf("the stalled body was cut off: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the stalled body was never cut off: the body phase is unbounded")
	}

	// A normal body over the same server is served.
	fast, err := http.Post("http://"+ln.Addr().String()+"/x", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("fast POST: %v", err)
	}
	defer fast.Body.Close()
	if fast.StatusCode != http.StatusNoContent {
		t.Fatalf("fast POST status = %d, want %d", fast.StatusCode, http.StatusNoContent)
	}
	if err := <-readErr; err != nil {
		t.Fatalf("a normal body was cut off too: %v", err)
	}
}

// TestRequestBodyDeadlineDoesNotCutALongHandler pins the other side of the bound:
// once the body has been read, the handler may take longer than the deadline.
//
// net/http clears the read deadline when the body reaches EOF (the background
// read starts), so the deadline bounds the body phase, not the handler. A
// handler that reads a small body and then works for longer than the bound must
// still finish and answer - otherwise a slow-but-legitimate write (a job
// creation, a subscription refresh) would break.
func TestRequestBodyDeadlineDoesNotCutALongHandler(t *testing.T) {
	const timeout = 150 * time.Millisecond
	handler := RequestBodyDeadlineMiddleware(timeout, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusRequestTimeout)
			return
		}
		// Work longer than the body deadline.
		time.Sleep(3 * timeout)
		_, _ = w.Write(body)
	}))
	srv := NewListenerServer(handler)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post("http://"+ln.Addr().String()+"/x", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "payload" {
		t.Fatalf("status = %d body = %q, want 200 %q: the body deadline leaked into the handler",
			resp.StatusCode, string(body), "payload")
	}
}

// TestServerWiresTheBodyDeadline is the wiring half of the  body bound: the
// middleware above is only worth anything if the served mux actually installs it.
//
// The check is structural rather than behavioural on purpose. Proving it over a
// real connection would mean waiting out RequestBodyTimeout, which is a minute of
// test time for a single assertion; reading the constructor catches the same
// regression (someone dropping the call) immediately. The behaviour of the
// middleware itself is covered by the three cases above, which inject their own
// timeout.
func TestServerWiresTheBodyDeadline(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	text := stripGoLineComments(string(source))
	if !strings.Contains(text, "RequestBodyDeadlineMiddleware(RequestBodyTimeout,") {
		t.Error("NewServerWithAddress does not install RequestBodyDeadlineMiddleware: the body phase of the served mux would be unbounded")
	}
	// And it must wrap the layers that hide the connection from the controller:
	// the body-limit wrapper and the audit recorder do not expose SetReadDeadline,
	// so an inner position would make the middleware a no-op.
	limit := strings.Index(text, "RequestBodyLimitMiddleware(")
	deadline := strings.Index(text, "RequestBodyDeadlineMiddleware(RequestBodyTimeout,")
	if limit >= 0 && deadline >= 0 && deadline < limit {
		t.Error("RequestBodyDeadlineMiddleware is installed inside RequestBodyLimitMiddleware; the controller cannot reach the connection through the body-limit wrapper")
	}

	// The token-action route is a POST with a body on the same listener, so it
	// needs the same bound. It is a separate handler (cmd/prism mounts it beside
	// the API), which is exactly why it is easy to forget.
	tokenSrc, err := os.ReadFile("handler_token_action.go")
	if err != nil {
		t.Fatalf("read handler_token_action.go: %v", err)
	}
	if !strings.Contains(stripGoLineComments(string(tokenSrc)), "RequestBodyDeadlineMiddleware(RequestBodyTimeout,") {
		t.Error("NewTokenActionHandler does not install RequestBodyDeadlineMiddleware: the token-action POST body would be unbounded")
	}
}

// stripGoLineComments removes // comments so a source scan does not match a
// pattern that only appears in documentation.
func stripGoLineComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	for _, line := range strings.Split(src, "\n") {
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
