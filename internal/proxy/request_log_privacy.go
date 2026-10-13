package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"prism/internal/keys"
)

const redactedLogValue = "[redacted]"
const maxLogURLBytes = 2048

var loggedErrorURLPattern = regexp.MustCompile("(?i)\\b[a-z][a-z0-9+.-]*://[^\\s\"'<>]+")

// sanitizeLogURL operates on telemetry only. Preserve the resource path for
// diagnostics, but never log URL credentials, query parameters or fragments.
//
// The path is then swept for key-shaped segments. A reverse-proxy request
// carries its credential as the first path segment, so a caller that forgets to
// redact one would otherwise write a usable credential into the request log;
// doing it here means no caller can.
func sanitizeLogURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || (u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https") {
		return redactedLogValue
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	redactAccessKeySegments(u)
	safe := u.String()
	if len(safe) > maxLogURLBytes {
		return safe[:maxLogURLBytes] + "[truncated]"
	}
	return safe
}

// redactAccessKeySegments replaces every path segment that looks like an access
// key with the redacted marker.
//
// Both Path and RawPath are rewritten. They are two renderings of the same
// path - RawPath is the escaped form, kept only when escaping is lossy - and a
// URL round trip prefers RawPath when it is set, so redacting only Path would
// leave the credential in the output of a request whose path needed escaping.
func redactAccessKeySegments(u *url.URL) {
	if u == nil {
		return
	}
	u.Path = redactAccessKeyPath(u.Path)
	if u.RawPath != "" {
		u.RawPath = redactAccessKeyRawPath(u.RawPath)
	}
}

// mayHoldAnAccessKey reports whether a path rendering could contain a key and
// therefore needs the per-segment sweep.
//
// The sweep decodes every segment, which is wasted work on the ordinary path of
// every logged URL. The two conditions that can hide a key are a literal prefix
// and an escape sequence, because a key that is fully percent-encoded has
// neither a literal prefix nor anything else recognizable in the raw form. A
// path with neither is returned untouched.
func mayHoldAnAccessKey(path string) bool {
	return strings.Contains(path, keys.CredentialPrefix) || strings.Contains(path, "%")
}

// redactAccessKeyPath rewrites the decoded rendering of a path.
func redactAccessKeyPath(path string) string {
	if path == "" || !mayHoldAnAccessKey(path) {
		return path
	}
	segments := strings.Split(path, "/")
	changed := false
	for i, segment := range segments {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			// A segment that cannot be decoded cannot be proven not to be a
			// credential, so it is redacted rather than kept.
			segments[i] = redactedLogValue
			changed = true
			continue
		}
		if keys.IsCredentialShape(decoded) {
			segments[i] = redactedLogValue
			changed = true
		}
	}
	if !changed {
		return path
	}
	return strings.Join(segments, "/")
}

// redactAccessKeyRawPath rewrites the escaped rendering of a path.
//
// The marker is written percent-escaped here because this rendering is emitted
// verbatim: a literal bracket would either be re-escaped into a different
// string or produce a path that no longer parses. Segments that are not keys
// keep their original spelling, so the rest of the path is untouched.
func redactAccessKeyRawPath(rawPath string) string {
	if rawPath == "" || !mayHoldAnAccessKey(rawPath) {
		return rawPath
	}
	escapedMarker := url.PathEscape(redactedLogValue)
	segments := strings.Split(rawPath, "/")
	changed := false
	for i, segment := range segments {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			segments[i] = escapedMarker
			changed = true
			continue
		}
		if keys.IsCredentialShape(decoded) {
			segments[i] = escapedMarker
			changed = true
		}
	}
	if !changed {
		return rawPath
	}
	return strings.Join(segments, "/")
}

func sensitiveLogHeader(name string, accountHeaders []string) bool {
	for _, accountHeader := range accountHeaders {
		if strings.EqualFold(name, accountHeader) {
			return true
		}
	}
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "-", ""), "_", ""))
	switch normalized {
	case "cookie", "setcookie", "xprismaccount", "authenticationinfo", "proxyauthenticationinfo":
		return true
	}
	for _, part := range []string{"auth", "apikey", "token", "secret", "password", "credential", "signature", "session", "csrf", "xsrf"} {
		if strings.Contains(normalized, part) {
			return true
		}
	}
	return false
}

func headersForRequestLog(header http.Header, accountHeaders []string) http.Header {
	safe := header.Clone()
	for name, values := range safe {
		if sensitiveLogHeader(name, accountHeaders) {
			safe[name] = []string{redactedLogValue}
			continue
		}
		switch strings.ToLower(name) {
		case "referer", "location", "content-location":
			for i, value := range values {
				values[i] = sanitizeLogURL(value)
			}
		case "refresh", "link":
			// These may contain multiple signed URLs, with their own syntax.
			safe[name] = []string{redactedLogValue}
		}
	}
	return safe
}

// logHeaderAccount deliberately leaves the router's account and lease keys
// untouched. Rotating the proxy token only changes future log pseudonyms.
func logHeaderAccount(account, proxyToken string) string {
	if account == "" {
		return ""
	}
	if proxyToken == "" {
		return redactedLogValue
	}
	mac := hmac.New(sha256.New, []byte(proxyToken))
	_, _ = mac.Write([]byte("prism/request-log/header-account/v1\x00"))
	_, _ = mac.Write([]byte(account))
	return "header-hmac-v1:" + hex.EncodeToString(mac.Sum(nil))
}
