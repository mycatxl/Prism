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
const maxPersistedLogPayloadBytes = 1 << 20

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
	for _, part := range []string{"auth", "apikey", "token", "secret", "password", "pepper", "credential", "signature", "privatekey", "psk", "session", "csrf", "xsrf"} {
		if strings.Contains(normalized, part) {
			return true
		}
	}
	return false
}

// sanitizeLogPayload replaces every captured body wholesale. Arbitrary bodies
// can be compressed, multipart, binary or use application-specific field names,
// so field-level parsing cannot uphold the rule that credentials never become
// durable telemetry. Length and truncation metadata are kept separately.
func sanitizeLogPayload(payload []byte) []byte {
	if len(payload) == 0 {
		return nil
	}
	return []byte(redactedLogValue)
}

// SanitizeRequestLogEntry is the persistence-boundary privacy filter. Producers
// already sanitize their normal paths, but the request-log repository is an
// exported sink and must stay safe when a future producer, importer or test
// constructs an entry directly.
func SanitizeRequestLogEntry(entry RequestLogEntry) RequestLogEntry {
	entry.TargetURL = sanitizeLogURL(entry.TargetURL)
	entry.UpstreamErrMsg = sanitizeUpstreamErrMsg(entry.UpstreamErrMsg)
	entry.ReqHeaders = capLogPayload(entry.ReqHeaders, &entry.ReqHeadersLen, &entry.ReqHeadersTruncated)
	entry.ReqBody = capLogPayload(entry.ReqBody, &entry.ReqBodyLen, &entry.ReqBodyTruncated)
	entry.RespHeaders = capLogPayload(entry.RespHeaders, &entry.RespHeadersLen, &entry.RespHeadersTruncated)
	entry.RespBody = capLogPayload(entry.RespBody, &entry.RespBodyLen, &entry.RespBodyTruncated)
	entry.ReqHeaders = sanitizeLogHeaders(entry.ReqHeaders)
	entry.ReqBody = sanitizeLogPayload(entry.ReqBody)
	entry.RespHeaders = sanitizeLogHeaders(entry.RespHeaders)
	entry.RespBody = sanitizeLogPayload(entry.RespBody)
	entry.ReqHeaders = capSanitizedLogPayload(entry.ReqHeaders, &entry.ReqHeadersTruncated)
	entry.ReqBody = capSanitizedLogPayload(entry.ReqBody, &entry.ReqBodyTruncated)
	entry.RespHeaders = capSanitizedLogPayload(entry.RespHeaders, &entry.RespHeadersTruncated)
	entry.RespBody = capSanitizedLogPayload(entry.RespBody, &entry.RespBodyTruncated)
	entry.Events = append([]string(nil), entry.Events...)
	return entry
}

// SanitizeStoredRequestLogPayloads is a read-side backstop for legacy/imported
// shards written before persistence-boundary redaction was enforced.
func SanitizeStoredRequestLogPayloads(reqHeaders, reqBody, respHeaders, respBody []byte) ([]byte, []byte, []byte, []byte) {
	return sanitizeLogHeaders(reqHeaders), sanitizeLogPayload(reqBody),
		sanitizeLogHeaders(respHeaders), sanitizeLogPayload(respBody)
}

// SanitizeStoredRequestLogSummary applies the same URL/error policy when an
// older or imported shard is read.
func SanitizeStoredRequestLogSummary(targetURL, upstreamErrMsg string) (string, string) {
	return sanitizeLogURL(targetURL), sanitizeUpstreamErrMsg(upstreamErrMsg)
}

func capLogPayload(payload []byte, total *int, truncated *bool) []byte {
	if *total < len(payload) {
		*total = len(payload)
	}
	if len(payload) > maxPersistedLogPayloadBytes {
		payload = payload[:maxPersistedLogPayloadBytes]
		*truncated = true
	}
	return payload
}

func capSanitizedLogPayload(payload []byte, truncated *bool) []byte {
	if len(payload) > maxPersistedLogPayloadBytes {
		*truncated = true
		return payload[:maxPersistedLogPayloadBytes]
	}
	return payload
}

func sanitizeLogHeaders(payload []byte) []byte {
	if len(payload) == 0 {
		return nil
	}
	lines := strings.Split(string(payload), "\n")
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimSuffix(line, "\r")
		name, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		if sensitiveLogHeader(name, nil) {
			ending := ""
			if strings.HasSuffix(line, "\r") {
				ending = "\r"
			}
			lines[i] = name + ": " + redactedLogValue + ending
			changed = true
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "location", "content-location", "referer":
			lines[i] = name + ": " + sanitizeLogURL(strings.TrimSpace(value))
			changed = true
		}
	}
	if !changed {
		return append([]byte(nil), payload...)
	}
	return []byte(strings.Join(lines, "\n"))
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
