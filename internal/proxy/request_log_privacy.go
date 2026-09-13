package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const redactedLogValue = "[redacted]"
const maxLogURLBytes = 2048

var loggedErrorURLPattern = regexp.MustCompile("(?i)\\b[a-z][a-z0-9+.-]*://[^\\s\"'<>]+")

// sanitizeLogURL operates on telemetry only. Preserve the resource path for
// diagnostics, but never log URL credentials, query parameters or fragments.
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
	safe := u.String()
	if len(safe) > maxLogURLBytes {
		return safe[:maxLogURLBytes] + "[truncated]"
	}
	return safe
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
