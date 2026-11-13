package api

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"time"
)

// Shared across listeners and routes: a per-request cap alone permits every
// accepted socket to retain a full API body at once. Hold admission through
// the handler so decoded and audit copies have the same concurrency bound.
var apiBodyReadSlots = make(chan struct{}, 32)

// RequestBodyLimitMiddleware enforces a max request body size for downstream handlers.
func RequestBodyLimitMiddleware(maxBytes int64, next http.Handler) http.Handler {
	if maxBytes <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r != nil && r.Body != nil && r.Body != http.NoBody {
			if r.ContentLength > maxBytes {
				w.Header().Set("Connection", "close")
				writeDecodeBodyError(w, &requestBodyTooLargeError{Limit: maxBytes})
				return
			}
			select {
			case apiBodyReadSlots <- struct{}{}:
				defer func() { <-apiBodyReadSlots }()
			default:
				w.Header().Set("Connection", "close")
				w.Header().Set("Retry-After", "1")
				WriteError(w, http.StatusServiceUnavailable, "CAPACITY_EXCEEDED", "too many request bodies in progress")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			// Validate the entire bounded body before a handler can mutate
			// state, including handlers that ignore bodies or only parse forms.
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.Header().Set("Connection", "close")
				var maxErr *http.MaxBytesError
				if errors.As(err, &maxErr) {
					writeDecodeBodyError(w, &requestBodyTooLargeError{Limit: maxErr.Limit})
				} else {
					WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body")
				}
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
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
// Panic values, raw paths and stacks are omitted from both logs and responses:
// any of them can carry a credential, SQL fragment or filesystem path. The
// reply is the same generic internal error as any other 500.
//
// It is installed once, in Handler, so every route is covered - including the
// ones added later.
func PanicRecoveryMiddleware(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracked := &panicResponseWriter{ResponseWriter: w}
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
			// Neither the panic value nor the raw URL is logged. Both are able to
			// carry credentials supplied by a request; the route pattern is enough
			// to identify the failing handler.
			log.Printf("api: recovered a panic serving %s %s", requestMethod(r), requestPattern(r))
			if tracked.committed {
				tracked.closeHijacked()
				panic(http.ErrAbortHandler)
			}
			for name := range tracked.Header() {
				tracked.Header().Del(name)
			}
			WriteError(tracked, http.StatusInternalServerError, "INTERNAL", "internal server error")
		}()
		next.ServeHTTP(tracked, r)
	})
}

// requestMethod and requestPattern keep the recovery log readable when the panic
// happens before the request was validated - r itself may be nil there.
func requestMethod(r *http.Request) string {
	if r == nil {
		return "?"
	}
	return r.Method
}

func requestPattern(r *http.Request) string {
	if r == nil || r.Pattern == "" {
		return "?"
	}
	return r.Pattern
}

// panicResponseWriter records whether a response has begun while preserving
// the optional interfaces used by streaming and upgraded handlers. Once bytes
// are committed, recovery must abort the connection rather than append a JSON
// error to an already successful response.
type panicResponseWriter struct {
	http.ResponseWriter
	committed    bool
	hijackedConn net.Conn
}

func (w *panicResponseWriter) WriteHeader(status int) {
	if w.committed {
		return
	}
	w.committed = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *panicResponseWriter) Write(payload []byte) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(payload)
}

func (w *panicResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *panicResponseWriter) Flush() {
	w.committed = true
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *panicResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	conn, rw, err := hijacker.Hijack()
	if err == nil {
		w.committed = true
		w.hijackedConn = conn
	}
	return conn, rw, err
}

func (w *panicResponseWriter) closeHijacked() {
	if w.hijackedConn != nil {
		_ = w.hijackedConn.Close()
		w.hijackedConn = nil
	}
}
