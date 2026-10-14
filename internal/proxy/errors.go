// Package proxy implements the forward and reverse proxy data plane for Prism.
package proxy

import (
	"context"
	"errors"
	"net/http"
	"os"

	"prism/internal/routing"
)

// ProxyError represents a structured proxy error response.
type ProxyError struct {
	HTTPCode   int
	PrismError string // X-Prism-Error header value
	Message    string // plain-text body
}

// Predefined proxy errors aligned with the design system error specification.
var (
	ErrAuthRequired = &ProxyError{
		HTTPCode:   http.StatusProxyAuthRequired,
		PrismError: "AUTH_REQUIRED",
		Message:    "Proxy authentication required",
	}
	ErrAuthFailed = &ProxyError{
		HTTPCode:   http.StatusForbidden,
		PrismError: "AUTH_FAILED",
		Message:    "Proxy authentication failed",
	}
	ErrURLParseError = &ProxyError{
		HTTPCode:   http.StatusBadRequest,
		PrismError: "URL_PARSE_ERROR",
		Message:    "Failed to parse request URL",
	}
	ErrInvalidProtocol = &ProxyError{
		HTTPCode:   http.StatusBadRequest,
		PrismError: "INVALID_PROTOCOL",
		Message:    "Protocol must be http or https",
	}
	ErrInvalidHost = &ProxyError{
		HTTPCode:   http.StatusBadRequest,
		PrismError: "INVALID_HOST",
		Message:    "Invalid or empty host",
	}
	ErrPlatformNotFound = &ProxyError{
		HTTPCode:   http.StatusNotFound,
		PrismError: "PLATFORM_NOT_FOUND",
		Message:    "Platform not found",
	}
	// Returned when an access key authenticates but may not reach the platform
	// the request names. It is distinct from ErrAuthFailed on purpose: the
	// credential is valid, so the client is told what is actually wrong, and
	// the 403 keeps the key from being retried against other platforms.
	ErrPlatformForbidden = &ProxyError{
		HTTPCode:   http.StatusForbidden,
		PrismError: "PLATFORM_FORBIDDEN",
		Message:    "Access key is not allowed to use this platform",
	}
	ErrAccountRejected = &ProxyError{
		HTTPCode:   http.StatusForbidden,
		PrismError: "ACCOUNT_REJECTED",
		Message:    "Account extraction failed and platform rejects unmatched requests",
	}
	// ErrQuotaExceeded is returned when the key is valid but has used up its
	// allowance, or is otherwise not in a state to serve requests (disabled,
	// expired, or over its concurrency limit). 429 rather than 403: the
	// credential is fine, and the client's correct response is to back off
	// rather than to stop presenting it.
	//
	// The message stays general on purpose. Saying which limit was hit would
	// tell a caller about an account they may not own.
	ErrQuotaExceeded = &ProxyError{
		HTTPCode:   http.StatusTooManyRequests,
		PrismError: "QUOTA_EXCEEDED",
		Message:    "Access key is not allowed to make more requests",
	}
	// Returned only while PRISM_DIRECT_DENY_PRIVATE is enabled, on every local
	// dial path: the reverse-proxy bypass branch, the forward HTTP proxy, the
	// CONNECT tunnel and the SOCKS5 inbound.
	ErrDirectTargetDenied = &ProxyError{
		HTTPCode:   http.StatusForbidden,
		PrismError: "DIRECT_TARGET_DENIED",
		Message:    "Direct target is not allowed",
	}
	ErrRateLimited = &ProxyError{
		HTTPCode:   http.StatusTooManyRequests,
		PrismError: "RATE_LIMITED",
		Message:    "Too many failed authentication attempts; retry later",
	}
	ErrNoAvailableNodes = &ProxyError{
		HTTPCode:   http.StatusServiceUnavailable,
		PrismError: "NO_AVAILABLE_NODES",
		Message:    "No available nodes for routing",
	}
	ErrUpstreamConnectFailed = &ProxyError{
		HTTPCode:   http.StatusBadGateway,
		PrismError: "UPSTREAM_CONNECT_FAILED",
		Message:    "Failed to connect to upstream",
	}
	ErrUpstreamTimeout = &ProxyError{
		HTTPCode:   http.StatusGatewayTimeout,
		PrismError: "UPSTREAM_TIMEOUT",
		Message:    "Upstream connection or response timed out",
	}
	ErrUpstreamRequestFailed = &ProxyError{
		HTTPCode:   http.StatusBadGateway,
		PrismError: "UPSTREAM_REQUEST_FAILED",
		Message:    "Upstream request failed",
	}
	ErrInternalError = &ProxyError{
		HTTPCode:   http.StatusInternalServerError,
		PrismError: "INTERNAL_ERROR",
		Message:    "Internal proxy error",
	}
)

// writeProxyError writes a standardised proxy error response.
// For 407 responses, the Proxy-Authenticate header is added automatically.
//
// PRISM-DEVIATION: X4 — every error response carries the Prism header and the
// upstream Resin spelling with the same value, so Resin clients that read
// X-Resin-Error keep working.
func writeProxyError(w http.ResponseWriter, pe *ProxyError) {
	if pe.HTTPCode == http.StatusProxyAuthRequired {
		w.Header().Set("Proxy-Authenticate", `Basic realm="Prism"`)
	}
	w.Header().Set("X-Prism-Error", pe.PrismError)
	w.Header().Set("X-Resin-Error", pe.PrismError)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(pe.HTTPCode)
	// The status line and headers are already committed, so a failed body write
	// can no longer be reported to the client; the request log carries the
	// outcome.
	_, _ = w.Write([]byte(pe.Message))
}

// classifyUpstreamError maps an upstream error to the appropriate ProxyError.
// Used for forward HTTP and reverse proxy paths (NOT CONNECT).
// Returns nil for context.Canceled (client-initiated cancellation should not
// be treated as a node health failure).
func classifyUpstreamError(err error) *ProxyError {
	if err == nil {
		return nil
	}
	// Client-initiated cancel — not a node failure.
	if errors.Is(err, context.Canceled) {
		return nil
	}
	// Timeout (context deadline or OS-level).
	if os.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return ErrUpstreamTimeout
	}
	// Everything else (dial failure, read/write errors, connection reset, etc.).
	// In non-CONNECT paths, all upstream failures are UPSTREAM_REQUEST_FAILED.
	return ErrUpstreamRequestFailed
}

// classifyConnectError classifies errors in the CONNECT dial path.
// In CONNECT, all errors are dial-phase errors, so non-timeout/non-canceled
// errors always map to UPSTREAM_CONNECT_FAILED (not UPSTREAM_REQUEST_FAILED).
func classifyConnectError(err error) *ProxyError {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	if os.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return ErrUpstreamTimeout
	}
	return ErrUpstreamConnectFailed
}

// mapRouteError translates a routing-layer error into a ProxyError.
func mapRouteError(err error) *ProxyError {
	if errors.Is(err, routing.ErrPlatformNotFound) {
		return ErrPlatformNotFound
	}
	if errors.Is(err, routing.ErrNoAvailableNodes) {
		return ErrNoAvailableNodes
	}
	return ErrInternalError
}
