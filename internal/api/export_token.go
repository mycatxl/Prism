package api

import (
	"container/list"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"time"
)

// Subscription token and rate-limit helpers for /sub/{token} (WP11 §4.3).
//
// The plaintext token is derived from 32 bytes of crypto/rand, shown to the
// administrator exactly once (at creation or rotation) and never stored: only
// its SHA-256 digest reaches state.db, and the API never echoes the digest
// either (model.ExportProfile.TokenSHA256 is `json:"-"`).

const (
	// exportTokenBytes is the entropy of a subscription token.
	exportTokenBytes = 32
)

// NewExportProfileToken returns a fresh URL-safe subscription token.
func NewExportProfileToken() (string, error) {
	buffer := make([]byte, exportTokenBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// ExportProfileTokenSHA256 hashes a subscription token for storage and lookup.
func ExportProfileTokenSHA256(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Rate limits of §4.3: 60 requests per minute per token and 120 per minute per
// client IP. Exceeding either answers 429.
const (
	exportTokenPerMinute = 60
	exportIPPerMinute    = 120
	// exportRateWindow is the sliding window both limits use.
	exportRateWindow = time.Minute
	// exportRateMaxKeys bounds the limiter tables; the oldest entry is evicted
	// when a table is full, so an attacker cannot grow memory without bound.
	exportRateMaxKeys = 65536
)

// rateCounter is one bounded sliding-window counter table.
type rateCounter struct {
	window  time.Duration
	limit   int
	maxKeys int
	entries map[string]*rateEntry
	order   *list.List
	now     func() time.Time
}

type rateEntry struct {
	key     string
	hits    int
	started time.Time
	element *list.Element
}

func newRateCounter(limit int, window time.Duration, maxKeys int) *rateCounter {
	if window <= 0 {
		window = exportRateWindow
	}
	return &rateCounter{
		window:  window,
		limit:   limit,
		maxKeys: maxKeys,
		entries: make(map[string]*rateEntry),
		order:   list.New(),
		now:     time.Now,
	}
}

// allow records one request for key and reports whether it stays within the
// limit, plus the retry-after hint.
func (c *rateCounter) allow(key string) (bool, time.Duration) {
	if c == nil || c.limit <= 0 || key == "" {
		return true, 0
	}
	now := c.now()

	c.evictExpired(now)
	entry, ok := c.entries[key]
	if !ok {
		if c.maxKeys > 0 && len(c.entries) >= c.maxKeys {
			// Bounded table: drop the oldest key rather than growing.
			if oldest := c.order.Front(); oldest != nil {
				c.remove(oldest.Value.(*rateEntry))
			}
		}
		entry = &rateEntry{key: key, started: now}
		entry.element = c.order.PushBack(entry)
		c.entries[key] = entry
	}
	if now.Sub(entry.started) >= c.window {
		entry.hits = 0
		entry.started = now
	}
	entry.hits++
	c.order.MoveToBack(entry.element)
	if entry.hits > c.limit {
		return false, entry.started.Add(c.window).Sub(now)
	}
	return true, 0
}

// evictExpired drops entries whose window has passed, so idle clients do not
// hold capacity away from active ones.
func (c *rateCounter) evictExpired(now time.Time) {
	for element := c.order.Front(); element != nil; {
		entry := element.Value.(*rateEntry)
		next := element.Next()
		if now.Sub(entry.started) >= c.window {
			c.remove(entry)
		}
		element = next
	}
}

func (c *rateCounter) remove(entry *rateEntry) {
	delete(c.entries, entry.key)
	if entry.element != nil {
		c.order.Remove(entry.element)
		entry.element = nil
	}
}

// exportSubscriptionLimiter applies both §4.3 limits.
type exportSubscriptionLimiter struct {
	byToken *rateCounter
	byIP    *rateCounter
}

func newExportSubscriptionLimiter() *exportSubscriptionLimiter {
	return &exportSubscriptionLimiter{
		byToken: newRateCounter(exportTokenPerMinute, exportRateWindow, exportRateMaxKeys),
		byIP:    newRateCounter(exportIPPerMinute, exportRateWindow, exportRateMaxKeys),
	}
}

// allow reports whether the request may proceed. The token key is the SHA-256
// digest, never the plaintext, so the limiter carries no credential either.
func (l *exportSubscriptionLimiter) allow(tokenHash string, clientIP string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	if ok, retry := l.byToken.allow(tokenHash); !ok {
		return false, retry
	}
	if ok, retry := l.byIP.allow(clientIP); !ok {
		return false, retry
	}
	return true, 0
}

// writeExportRateLimited answers 429 with Retry-After.
func writeExportRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	if retryAfter > 0 {
		w.Header().Set("Retry-After", itoaSeconds(retryAfter))
	}
	WriteError(w, http.StatusTooManyRequests, "RATE_LIMITED", "subscription rate limit exceeded; retry later")
}

func itoaSeconds(d time.Duration) string {
	seconds := int((d + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return formatInt(seconds)
}

func formatInt(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [20]byte
	pos := len(digits)
	for value > 0 {
		pos--
		digits[pos] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		pos--
		digits[pos] = '-'
	}
	return string(digits[pos:])
}
