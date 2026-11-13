package main

import (
	"bufio"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"prism/internal/keys"
	"prism/internal/model"
	"prism/internal/proxy"
)

func newEndpointInboundMux(
	currentEndpoint func() model.Endpoint,
	proxyToken string,
	forward, reverse, apiHandler, tokenActionHandler http.Handler,
	authGuard proxy.AuthFailureGuard,
) http.Handler {
	if forward == nil {
		forward = http.NotFoundHandler()
	}
	if reverse == nil {
		reverse = http.NotFoundHandler()
	}
	if apiHandler == nil {
		apiHandler = http.NotFoundHandler()
	}
	if tokenActionHandler == nil {
		tokenActionHandler = http.NotFoundHandler()
	}

	// The management handler already recovers panics (api.Server.Handler wraps
	// its mux). The proxy handlers are mounted here directly, on a listener of
	// their own, so they get the same treatment: without it a panic in the data
	// plane drops the connection with nothing written, which a client sees as a
	// dead proxy rather than as one failed request.
	forward = recoverProxyPanic("forward", forward)
	reverse = recoverProxyPanic("reverse", reverse)
	tokenActionHandler = recoverProxyPanic("token action", tokenActionHandler)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint := model.Endpoint{}
		if currentEndpoint != nil {
			endpoint = currentEndpoint()
		}
		if shouldRouteForwardProxy(r) {
			if !endpoint.AllowProxy || !endpoint.AllowHTTPForward {
				writeEndpointCapabilityDisabled(w)
				return
			}
			r = r.WithContext(proxy.ContextWithInboundPolicy(r.Context(), proxy.InboundPolicy{
				RequireProxyAuthInfo: endpoint.RequireProxyAuthInfo,
			}))
			forward.ServeHTTP(w, r)
			return
		}
		if shouldRouteControlPlane(r) {
			if r.URL != nil && r.URL.Path != "/healthz" && !endpoint.AllowManagement {
				w.Header().Set("Connection", "close")
				http.NotFound(w, r)
				return
			}
			apiHandler.ServeHTTP(w, r)
			return
		}
		if shouldRouteTokenAPI(r, proxyToken) {
			if !endpoint.AllowProxy {
				writeEndpointCapabilityDisabled(w)
				return
			}
			// Token actions are another credentialed proxy entry point. Check the
			// shared limiter before invoking the verifier, just as the forward and
			// reverse entries do.
			if authGuard != nil {
				clientIP := proxy.ProxyClientIP(r)
				if blocked, retryAfter := authGuard.Blocked(clientIP); blocked {
					writeInboundRateLimited(w, retryAfter)
					return
				}
			}
			tokenActionHandler.ServeHTTP(w, r)
			return
		}
		if !endpoint.AllowProxy || !endpoint.AllowHTTPReverse {
			writeEndpointCapabilityDisabled(w)
			return
		}
		if shouldRejectReverseProxyByToken(r, proxyToken) {
			// this path-token decision lives in the mux, so the mux
			// meters it too; internal/proxy meters the checks it owns itself.
			// A blocked client is answered 429 before the comparison, exactly
			// like the forward and CONNECT entries.
			if authGuard != nil {
				clientIP := proxy.ProxyClientIP(r)
				if blocked, retryAfter := authGuard.Blocked(clientIP); blocked {
					writeInboundRateLimited(w, retryAfter)
					return
				}
				authGuard.RecordFailure(clientIP)
			}
			writeInboundAuthFailed(w)
			return
		}
		reverse.ServeHTTP(w, r)
	})
}

// recoverProxyPanic answers a panicking data-plane handler with the proxy's own
// 502 and keeps the connection usable.
//
// The proxy speaks to clients that are not the console: an HTTP proxy client
// reads the status line, and a CONNECT tunnel reads the handshake reply. A
// dropped connection there is indistinguishable from a broken proxy, so the
// failure is reported as a proxy error instead. The stack goes to the server
// log - it can name a target or a credential - and never into the response.
//
// http.ErrAbortHandler is repanicked: it is how a handler abandons a response
// it has already started, and net/http is the one that has to finish that.
func recoverProxyPanic(name string, next http.Handler) http.Handler {
	if next == nil {
		return http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracked := &proxyRecoveryResponseWriter{ResponseWriter: w}
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			tracked.closeHijacked()
			if recovered == http.ErrAbortHandler {
				panic(recovered)
			}
			log.Printf("proxy: recovered a panic in the %s handler", name)
			if tracked.committed {
				// The response head or a hijacked socket is already committed. A
				// second 502 would append a forged response to the original bytes;
				// abort the connection instead.
				panic(http.ErrAbortHandler)
			}
			writeInboundProxyFailure(w)
		}()
		next.ServeHTTP(tracked, r)
	})
}

// proxyRecoveryResponseWriter tracks commitment and hijacking without hiding
// the interfaces the data-plane handlers rely on. Its Unwrap method also lets
// http.ResponseController reach the underlying writer for flush and hijack.
type proxyRecoveryResponseWriter struct {
	http.ResponseWriter
	committed    bool
	hijackedConn net.Conn
}

func (w *proxyRecoveryResponseWriter) WriteHeader(statusCode int) {
	w.committed = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *proxyRecoveryResponseWriter) Write(p []byte) (int, error) {
	w.committed = true
	return w.ResponseWriter.Write(p)
}

func (w *proxyRecoveryResponseWriter) Unwrap() http.ResponseWriter {
	if w == nil {
		return nil
	}
	return w.ResponseWriter
}

func (w *proxyRecoveryResponseWriter) Flush() {
	w.committed = true
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *proxyRecoveryResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
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

func (w *proxyRecoveryResponseWriter) closeHijacked() {
	if w == nil || w.hijackedConn == nil {
		return
	}
	_ = w.hijackedConn.Close()
	w.hijackedConn = nil
}

// PRISM-DEVIATION: X4 — error responses carry both the Prism and the upstream
// Resin header, with the same value.
func writeInboundProxyFailure(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	w.Header().Set("X-Prism-Error", "PROXY_FAILURE")
	w.Header().Set("X-Resin-Error", "PROXY_FAILURE")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_, _ = w.Write([]byte("Proxy request failed"))
}

// PRISM-DEVIATION: X4 — error responses carry both the Prism and the upstream
// Resin header, with the same value.
func writeEndpointCapabilityDisabled(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	w.Header().Set("X-Prism-Error", "ENDPOINT_CAPABILITY_DISABLED")
	w.Header().Set("X-Resin-Error", "ENDPOINT_CAPABILITY_DISABLED")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte("Proxy capability is disabled on this endpoint"))
}

func shouldRouteForwardProxy(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.Method == http.MethodConnect {
		return true
	}
	if r.URL != nil && r.URL.IsAbs() {
		return true
	}
	uri := strings.ToLower(strings.TrimSpace(r.RequestURI))
	return strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://")
}

func shouldRouteTokenAPI(r *http.Request, proxyToken string) bool {
	if r == nil {
		return false
	}
	if !isCanonicalInboundPath(r) {
		return false
	}
	segments := escapedPathSegments(r)
	if len(segments) < 2 {
		return false
	}
	token, ok := decodePathSegment(segments[0])
	if !ok {
		return false
	}
	if !isTokenPositionCandidate(token, proxyToken) {
		return false
	}
	apiSeg, ok := decodePathSegment(segments[1])
	return ok && apiSeg == "api"
}

func shouldRejectReverseProxyByToken(r *http.Request, _ string) bool {
	if r == nil {
		return false
	}
	if !isCanonicalInboundPath(r) {
		return true
	}
	segments := escapedPathSegments(r)
	if len(segments) == 0 {
		return false
	}
	token, ok := decodePathSegment(segments[0])
	if !ok {
		// A malformed first segment cannot be a valid credential. Treat it as
		// an authentication failure at the entry point so it cannot bypass the
		// shared failure limiter and reach a different route.
		return true
	}
	// The reverse parser resolves the key, but the mux must never admit a
	// legacy global-token segment as a reverse-proxy candidate.
	return !keys.IsCredentialShape(token)
}

// isTokenPositionCandidate reports whether a path segment may carry a
// credential.
//
// The mux only recognizes the shape; the real check happens in the handler,
// which owns the key store. That keeps this layer free of the pepper and the
// key store while still letting a key-shaped segment through to the handler
// that can verify it.
//
// isTokenPositionCandidate reports whether a path segment may carry an
// AccessKey. The configured proxy token is intentionally ignored: only a
// structurally valid AccessKey is admitted to the token-action route.
func isTokenPositionCandidate(segment string, _ string) bool {
	return keys.IsCredentialShape(segment)
}

// PRISM-DEVIATION: X4 — error responses carry both the Prism and the upstream
// Resin header, with the same value.
func writeInboundAuthFailed(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	w.Header().Set("X-Prism-Error", "AUTH_FAILED")
	w.Header().Set("X-Resin-Error", "AUTH_FAILED")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte("Proxy authentication failed"))
}

// shouldRouteControlPlane reports whether a request belongs to the management
// surface, which `endpoint.AllowManagement` gates as a whole.
func shouldRouteControlPlane(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.URL == nil {
		return false
	}
	if !isCanonicalInboundPath(r) {
		return false
	}
	switch p := r.URL.Path; {
	case p == "/":
		return true
	case p == "/healthz":
		return true
	case p == "/api" || strings.HasPrefix(p, "/api/"):
		return true
	case p == "/ui" || strings.HasPrefix(p, "/ui/"):
		return true
	default:
		return false
	}
}

func escapedPathSegments(r *http.Request) []string {
	if r == nil || r.URL == nil {
		return nil
	}
	if !isCanonicalInboundPath(r) {
		return nil
	}
	rawPath := r.URL.EscapedPath()
	if rawPath == "" {
		rawPath = r.URL.Path
	}
	path := strings.TrimPrefix(rawPath, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

// isCanonicalInboundPath keeps the protocol dispatchers from making different
// routing decisions for equivalent URL spellings. The reverse and token-action
// grammars use path segments as credentials and control data, so duplicate
// slashes, dot segments, backslashes and decoded controls are rejected instead
// of being cleaned into a different route by one downstream handler.
func isCanonicalInboundPath(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	p := r.URL.Path
	if p == "" {
		return true
	}
	if !strings.HasPrefix(p, "/") || strings.Contains(p, "//") || strings.ContainsAny(p, "\\\x00\r\n\t") {
		return false
	}
	for _, segment := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	clean := path.Clean(p)
	if p != "/" && strings.HasSuffix(p, "/") {
		clean += "/"
	}
	return clean == p
}

func decodePathSegment(segment string) (string, bool) {
	decoded, err := url.PathUnescape(segment)
	if err != nil {
		return "", false
	}
	return decoded, true
}

// writeInboundRateLimited answers the proxy-entry 429 of the auth-failure
// limiter. PRISM-DEVIATION: X4 — like every other inbound error response it
// carries both the Prism and the upstream Resin error header, with the same
// value.
func writeInboundRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	w.Header().Set("Connection", "close")
	if retryAfter > 0 {
		seconds := int((retryAfter + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
	}
	w.Header().Set("X-Prism-Error", "RATE_LIMITED")
	w.Header().Set("X-Resin-Error", "RATE_LIMITED")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte("Proxy authentication rate limit exceeded"))
}
