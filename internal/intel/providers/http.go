package providers

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// maxResponseBytes bounds every provider response body read.
const maxResponseBytes = 64 * 1024

// NewStrictClient builds the HTTP client used by the online-ip data sources
// (WP09 §1): it dials directly, never inherits HTTP_PROXY, never follows a
// redirect and bounds every phase with a timeout.
func NewStrictClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			// Provider credentials are never sent through an inventory node or
			// an inherited HTTP_PROXY configuration.
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: timeout,
			MaxIdleConns:          2,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       60 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// CloseIdle closes the idle connections of a strict client.
func CloseIdle(client *http.Client) {
	if client == nil {
		return
	}
	client.CloseIdleConnections()
}

// response carries a bounded successful HTTP response.
type response struct {
	Status     int
	Header     http.Header
	Body       []byte
	RetryAfter time.Duration
}

// performRequest executes one request and returns a bounded response. Transport
// errors are converted into PROVIDER_UNAVAILABLE results without echoing the
// URL, because provider URLs may embed credentials (R6).
func performRequest(ctx context.Context, client *http.Client, req *http.Request, vendor string) (response, Result) {
	if client == nil {
		return response{}, Failure(CodeUnavailable, vendor+" client is not configured")
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return response{}, Failure(CodeUnavailable, vendor+" request was canceled")
		}
		return response{}, Failure(CodeUnavailable, vendor+" could not be reached")
	}
	defer resp.Body.Close()

	out := response{
		Status:     resp.StatusCode,
		Header:     resp.Header.Clone(),
		RetryAfter: BoundedRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
	}
	if resp.StatusCode != http.StatusOK {
		return out, statusFailure(out, vendor)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return out, Failure(CodeResponse, vendor+" response is unreadable or too large")
	}
	out.Body = body
	return out, Result{}
}

// statusFailure converts an unsuccessful status code into the documented
// provider error code.
func statusFailure(resp response, vendor string) Result {
	switch resp.Status {
	case http.StatusTooManyRequests:
		return Result{Err: &ProviderError{
			Code:       CodeLimit,
			Message:    vendor + " request limit reached",
			RetryAfter: resp.RetryAfter,
			Pause:      true,
		}}
	case http.StatusUnauthorized, http.StatusForbidden:
		return Result{Err: &ProviderError{
			Code:    CodeAuth,
			Message: vendor + " rejected the credentials",
			Pause:   true,
		}}
	default:
		if resp.Status == http.StatusPaymentRequired {
			return Result{Err: &ProviderError{
				Code:       CodeLimit,
				Message:    vendor + " quota exhausted for this account",
				RetryAfter: resp.RetryAfter,
			}}
		}
		return Failure(CodeUnavailable, vendor+" returned an unsuccessful response")
	}
}

// BoundedRetryAfter clamps a Retry-After header (delta or HTTP date) to
// [1 minute, 24 hours].
func BoundedRetryAfter(raw string, now time.Time) time.Duration {
	delay := time.Hour
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		delay = time.Duration(min(seconds, 86400)) * time.Second
	} else if date, err := http.ParseTime(raw); err == nil && date.After(now) {
		delay = date.Sub(now)
	}
	return min(max(delay, time.Minute), 24*time.Hour)
}

// boundedText collapses whitespace and truncates a provider string field.
func boundedText(value string, limit int) string {
	runes := []rune(joinFields(value))
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return string(runes)
}

// joinFields collapses runs of whitespace into single spaces.
func joinFields(value string) string {
	out := make([]rune, 0, len(value))
	space := false
	for _, r := range value {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if space {
				continue
			}
			space = true
			out = append(out, ' ')
			continue
		}
		space = false
		out = append(out, r)
	}
	for len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	for len(out) > 0 && out[0] == ' ' {
		out = out[1:]
	}
	return string(out)
}
