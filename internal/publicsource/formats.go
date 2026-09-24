package publicsource

import (
	"bytes"
	"encoding/json"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"prism/internal/subscription"
)

// Format normalisation for public proxy lists.
//
// A lot of the long-lived public lists are not subscriptions at all: they are
// plain text behind a banner of donation addresses, an HTML page, or a JSON
// array of objects. Resin's subscription parser is strict on purpose, and
// relaxing it would change behaviour for every real subscription, so the
// rewriting happens here instead: an unrecognised body is converted into the
// plain `scheme://ip:port` form the parser already understands.
//
// This layer is strictly a fallback. The standard parser is always tried first,
// so nothing that parses today can change meaning.

const (
	// minFallbackProxies stops an error page or a random HTML document from
	// being mined for coincidental `ip:port` pairs.
	minFallbackProxies = 3
	// maxFallbackProxies bounds memory for a pathological body.
	maxFallbackProxies = 50000
	// schemeHintWindow is how much of the body is inspected for a protocol hint.
	schemeHintWindow = 2048
)

// bareProxyRe matches an optionally scheme-qualified IPv4 endpoint. It is the
// same loose shape used by the JS implementations of these lists.
var bareProxyRe = regexp.MustCompile(`(?i)(?:(socks5h|socks5|socks4a|socks4|https|http)://)?((?:\d{1,3}\.){3}\d{1,3}):(\d{1,5})`)

// proxiflyEntry is one element of the proxifly/free-proxy-list JSON array.
type proxiflyEntry struct {
	Proxy    string `json:"proxy"`
	Protocol string `json:"protocol"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
}

// parseSourceBody parses subscription content, falling back to format
// normalisation for the public lists that are not subscriptions. The original
// error is preserved when the fallback cannot help.
func parseSourceBody(body []byte) ([]subscription.ParsedNode, error) {
	parsed, err := subscription.ParseGeneralSubscription(body)
	if err == nil && len(parsed) > 0 {
		return parsed, nil
	}

	rewritten, ok := normalizeSourceBody(body)
	if !ok {
		return parsed, err
	}
	fallback, fallbackErr := subscription.ParseGeneralSubscription(rewritten)
	if fallbackErr != nil || len(fallback) == 0 {
		return parsed, err
	}
	return fallback, nil
}

// normalizeSourceBody rewrites body into a plain proxy list, reporting whether
// a known exotic format was recognised.
func normalizeSourceBody(body []byte) ([]byte, bool) {
	if proxies, ok := parseProxiflyArray(body); ok {
		return encodeProxyLines(proxies), true
	}

	proxies := extractBareProxies(body, detectSchemeHint(body))
	if len(proxies) < minFallbackProxies {
		return nil, false
	}
	return encodeProxyLines(proxies), true
}

// parseProxiflyArray handles the proxifly JSON list, which is an array of
// objects rather than a subscription document.
func parseProxiflyArray(body []byte) ([]string, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, false
	}
	var entries []proxiflyEntry
	if err := json.Unmarshal(trimmed, &entries); err != nil {
		return nil, false
	}

	proxies := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		line, ok := proxiflyLine(entry)
		if !ok {
			continue
		}
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		proxies = append(proxies, line)
		if len(proxies) >= maxFallbackProxies {
			break
		}
	}
	if len(proxies) < minFallbackProxies {
		return nil, false
	}
	return proxies, true
}

// proxiflyLine renders one entry as `scheme://ip:port`, preferring the explicit
// proxy field and falling back to the ip/port pair.
func proxiflyLine(entry proxiflyEntry) (string, bool) {
	scheme := normalizeFallbackScheme(entry.Protocol)
	host := strings.TrimSpace(entry.IP)
	port := entry.Port

	// The proxy field is authoritative when it carries a usable endpoint.
	if proxy := strings.TrimSpace(entry.Proxy); proxy != "" {
		match := bareProxyRe.FindStringSubmatch(proxy)
		if match != nil {
			if s := normalizeFallbackScheme(match[1]); s != "" {
				scheme = s
			}
			host = match[2]
			parsed, err := strconv.Atoi(match[3])
			if err != nil {
				return "", false
			}
			port = parsed
		}
	}

	if scheme == "" || port < 1 || port > 65535 || !isUsableFallbackHost(host) {
		return "", false
	}
	return scheme + "://" + host + ":" + strconv.Itoa(port), true
}

// extractBareProxies scans any text for `ip:port` endpoints, applying the given
// scheme to the ones that carry none of their own.
func extractBareProxies(body []byte, defaultScheme string) []string {
	if defaultScheme == "" {
		defaultScheme = "http"
	}
	matches := bareProxyRe.FindAllSubmatch(body, maxFallbackProxies+1)

	proxies := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		scheme := normalizeFallbackScheme(string(match[1]))
		if scheme == "" {
			scheme = defaultScheme
		}
		host := string(match[2])
		if !isUsableFallbackHost(host) {
			continue
		}
		port, err := strconv.Atoi(string(match[3]))
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		line := scheme + "://" + host + ":" + strconv.Itoa(port)
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		proxies = append(proxies, line)
		if len(proxies) >= maxFallbackProxies {
			break
		}
	}
	return proxies
}

// detectSchemeHint reads a protocol hint from the head of a list, because lists
// like roosterkid's SOCKS5_RAW.txt contain bare endpoints with no scheme at all
// and would otherwise be imported as HTTP proxies.
func detectSchemeHint(body []byte) string {
	head := body
	if len(head) > schemeHintWindow {
		head = head[:schemeHintWindow]
	}
	upper := strings.ToUpper(string(head))
	switch {
	case strings.Contains(upper, "SOCKS5"):
		return "socks5"
	case strings.Contains(upper, "SOCKS4"):
		return "socks4"
	default:
		return "http"
	}
}

// normalizeFallbackScheme maps the many spellings of a protocol to the ones
// Resin accepts, returning "" for anything unknown.
func normalizeFallbackScheme(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "socks5", "socks5h":
		return "socks5"
	case "socks4", "socks4a":
		return "socks4"
	case "https":
		return "https"
	case "http":
		return "http"
	default:
		return ""
	}
}

// isUsableFallbackHost rejects anything that is not a routable IPv4 address.
func isUsableFallbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || isForbiddenIP(host) {
		return false
	}
	return net.ParseIP(host) != nil
}

// encodeProxyLines renders extracted endpoints as newline-separated text, sorted
// so the collector's per-source cache is stable across runs.
func encodeProxyLines(proxies []string) []byte {
	if len(proxies) == 0 {
		return nil
	}
	sorted := append([]string(nil), proxies...)
	sort.Strings(sorted)
	var sb strings.Builder
	for _, proxy := range sorted {
		sb.WriteString(proxy)
		sb.WriteByte('\n')
	}
	return []byte(sb.String())
}
