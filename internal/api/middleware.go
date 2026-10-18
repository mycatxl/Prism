package api

import (
	"log"
	"net/http"
	"runtime/debug"
	"time"
)

// RequestBodyLimitMiddleware enforces a max request body size for downstream handlers.
func RequestBodyLimitMiddleware(maxBytes int64, next http.Handler) http.Handler {
	if maxBytes <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r != nil && r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// RequestBodyDeadlineMiddleware bounds how long a client may take to deliver the
// body of a request that carries one.
//
// ReadHeaderTimeout only covers the header phase: with ReadTimeout unset (see the
// constant block in server.go), net/http drops the read deadline once the headers
// are in, so a client that announces a body and then sends it arbitrarily slowly
// keeps a connection and a goroutine for as long as it likes. apiMaxBodyBytes
// caps the size of that body, never the time it takes to arrive.
//
// The deadline is armed only when the request actually has a body, which is what
// keeps the SSE stream working: GET /api/v1/intel/jobs/{id}/events carries no
// body, so its read deadline stays untouched.
//
// Nothing has to be undone afterwards. net/http re-arms the read deadline itself
// after every request (idleTimeout, then ReadHeaderTimeout once the next request
// starts), and it clears the deadline when the body reaches EOF and the
// background read starts - so a handler that runs long after reading its body is
// not cut off, while the body phase and the post-handler drain both stay bounded.
//
// It must wrap the outermost layer so the controller can still reach the
// connection: the audit recorder and the body-limit wrapper do not expose
// SetReadDeadline.
func RequestBodyDeadlineMiddleware(timeout time.Duration, next http.Handler) http.Handler {
	if timeout <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r != nil && hasRequestBody(r) {
			// A connection that does not implement the controller (an httptest
			// recorder in a unit test, for instance) is left unbounded rather
			// than failed: the bound is hygiene, not a correctness condition.
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(timeout))
		}
		next.ServeHTTP(w, r)
	})
}

// hasRequestBody reports whether r carries a body the client still has to send.
// ContentLength -1 is a chunked request and counts; 0 with http.NoBody does not.
func hasRequestBody(r *http.Request) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return false
	}
	return r.ContentLength != 0
}

// PanicRecoveryMiddleware turns a panicking handler into the same JSON error
// every other failure produces.
//
// Without it net/http closes the connection and writes nothing, so the caller
// gets a transport error where the API contract promises a body, and the stack
// goes to the server's error log through the default handler - which is the one
// path that does not go through the audit recorder.
//
// The stack is logged rather than returned: a panic message can carry a
// credential or a SQL fragment, and the response is the one place a caller can
// read it. The reply is the generic internal error, byte for byte the same as
// any other 500, so a caller cannot tell a panic from a database failure.
//
// It is installed once, in Handler, so every route is covered - including the
// ones added later.
func PanicRecoveryMiddleware(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			// A client that hung up makes the handler panic with
			// http.ErrAbortHandler, which is the documented way to abort a
			// response. Repanicking lets net/http do exactly that instead of
			// writing a 500 into a connection nobody is reading.
			if recovered == http.ErrAbortHandler {
				panic(recovered)
			}
			log.Printf("api: recovered a panic serving %s %s: %v\n%s",
				requestMethod(r), requestPath(r), recovered, debug.Stack())
			WriteError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		}()
		next.ServeHTTP(w, r)
	})
}

// requestMethod and requestPath keep the recovery log readable when the panic
// happens before the request was validated - r itself may be nil there.
func requestMethod(r *http.Request) string {
	if r == nil {
		return "?"
	}
	return r.Method
}

func requestPath(r *http.Request) string {
	if r == nil || r.URL == nil {
		return "?"
	}
	return r.URL.Path
}
