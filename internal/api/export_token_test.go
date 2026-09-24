package api

import (
	"strings"
	"testing"
	"time"
)

// TestExportProfileTokenEntropy checks the subscription token has enough
// entropy and never repeats.
func TestExportProfileTokenEntropy(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		token, err := NewExportProfileToken()
		if err != nil {
			t.Fatalf("NewExportProfileToken: %v", err)
		}
		if len(token) < 32 {
			t.Fatalf("token is too short: %d characters", len(token))
		}
		if strings.ContainsAny(token, "+/=") {
			t.Fatalf("token is not URL-safe: %q", token)
		}
		if seen[token] {
			t.Fatalf("token repeated: %q", token)
		}
		seen[token] = true
	}
}

// TestExportProfileTokenSHA256 checks the digest is stable and 32 bytes long.
func TestExportProfileTokenSHA256(t *testing.T) {
	const token = "a-token"
	first := ExportProfileTokenSHA256(token)
	second := ExportProfileTokenSHA256(token)
	if first != second {
		t.Fatalf("digest is not deterministic: %q vs %q", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("digest length = %d, want 64 hex characters", len(first))
	}
	if first == token {
		t.Fatal("digest equals the plaintext token")
	}
	if ExportProfileTokenSHA256("a-token ") == first {
		t.Fatal("digest ignores trailing whitespace")
	}
}

// TestExportSubscriptionLimiterPerTokenBound checks the §4.3 per-token limit.
func TestExportSubscriptionLimiterPerTokenBound(t *testing.T) {
	limiter := newExportSubscriptionLimiter()
	for i := 0; i < exportTokenPerMinute; i++ {
		if ok, _ := limiter.allow("token-hash", "10.0.0.1"); !ok {
			t.Fatalf("request %d was limited too early", i+1)
		}
	}
	ok, retry := limiter.allow("token-hash", "10.0.0.1")
	if ok {
		t.Fatal("the limiter allowed more than the per-token limit")
	}
	if retry <= 0 || retry > exportRateWindow {
		t.Fatalf("retry-after = %v", retry)
	}
	// A different token still has its own budget.
	if ok, _ := limiter.allow("other-token-hash", "10.0.0.1"); !ok {
		t.Fatal("the per-token limit leaked into another token")
	}
}

// TestExportSubscriptionLimiterPerIPBound checks the §4.3 per-IP limit.
func TestExportSubscriptionLimiterPerIPBound(t *testing.T) {
	limiter := newExportSubscriptionLimiter()
	for i := 0; i < exportIPPerMinute; i++ {
		token := "token-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if ok, _ := limiter.allow(token, "10.0.0.2"); !ok {
			t.Fatalf("request %d was limited too early", i+1)
		}
	}
	if ok, _ := limiter.allow("fresh-token", "10.0.0.2"); ok {
		t.Fatal("the limiter allowed more than the per-IP limit")
	}
	if ok, _ := limiter.allow("fresh-token", "10.0.0.3"); !ok {
		t.Fatal("the per-IP limit leaked into another client")
	}
}

// TestRateCounterWindowResets checks the window rolls over.
func TestRateCounterWindowResets(t *testing.T) {
	counter := newRateCounter(2, 10*time.Millisecond, 8)
	now := time.Unix(1700000000, 0)
	counter.now = func() time.Time { return now }

	if ok, _ := counter.allow("k"); !ok {
		t.Fatal("first request limited")
	}
	if ok, _ := counter.allow("k"); !ok {
		t.Fatal("second request limited")
	}
	if ok, _ := counter.allow("k"); ok {
		t.Fatal("third request allowed inside the window")
	}
	now = now.Add(11 * time.Millisecond)
	if ok, _ := counter.allow("k"); !ok {
		t.Fatal("request after the window was limited")
	}
}

// TestRateCounterIsBounded checks the table evicts instead of growing.
func TestRateCounterIsBounded(t *testing.T) {
	counter := newRateCounter(1, time.Minute, 4)
	for i := 0; i < 100; i++ {
		key := "key-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		counter.allow(key)
	}
	if len(counter.entries) > 4 {
		t.Fatalf("limiter table grew to %d entries", len(counter.entries))
	}
}

// TestExportNowNsIsUTC checks the profile clock is UTC Unix nanoseconds.
func TestExportNowNsIsUTC(t *testing.T) {
	value := exportNowNs()
	if value <= 0 {
		t.Fatalf("exportNowNs = %d", value)
	}
	delta := time.Since(time.Unix(0, value))
	if delta < 0 || delta > time.Minute {
		t.Fatalf("exportNowNs is not the current clock: %v off", delta)
	}
}

// TestNewExportProfileIDIsUnique checks the identifier shape.
func TestNewExportProfileIDIsUnique(t *testing.T) {
	first := newExportProfileID()
	second := newExportProfileID()
	if first == second || len(first) != 36 {
		t.Fatalf("ids = %q and %q", first, second)
	}
}
