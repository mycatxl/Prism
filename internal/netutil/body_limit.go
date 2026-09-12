package netutil

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
)

const DefaultResourceMaxBytes int64 = 32 << 20
const ProbeResponseMaxBytes int64 = 64 << 10

func readLimitedBody(body io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = DefaultResourceMaxBytes
	}
	if limit == math.MaxInt64 {
		return nil, &NonRetryableError{Err: errors.New("invalid response size limit")}
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, &NonRetryableError{Err: fmt.Errorf("response exceeds %d bytes", limit)}
	}
	return data, nil
}

// Resource URLs frequently contain subscription credentials in paths or query
// strings. Operational errors identify only the origin, never those values.
func resourceOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "configured resource"
	}
	return parsed.Scheme + "://" + parsed.Host
}

func sanitizeRequestError(err error) error {
	var requestError *url.Error
	if errors.As(err, &requestError) {
		return &url.Error{Op: requestError.Op, URL: resourceOrigin(requestError.URL), Err: sanitizeRequestError(requestError.Err)}
	}
	return err
}

func resourceRedirect(request *http.Request, previous []*http.Request) error {
	if len(previous) >= 10 {
		return &NonRetryableError{Err: errors.New("resource exceeded 10 redirects")}
	}
	request.Header.Del("Referer")
	if len(previous) > 0 && (request.URL.Scheme != previous[0].URL.Scheme || request.URL.Host != previous[0].URL.Host) {
		request.Header.Del("Authorization")
		request.Header.Del("Proxy-Authorization")
		request.Header.Del("Cookie")
	}
	return nil
}
