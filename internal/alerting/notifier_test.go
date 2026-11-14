package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"prism/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testAlertConfig() *config.RuntimeConfig {
	c := config.NewDefaultRuntimeConfig()
	c.AlertTelegramEnabled = true
	c.AlertTelegramBotToken = "123456:TEST_TOKEN_FOR_LOCAL_FIXTURES_ONLY"
	c.AlertTelegramChatID = "-1001234567"
	return c
}

func TestTelegramDeliveryRetriesDeduplicatesAndUsesUpdatedConfig(t *testing.T) {
	var cfg atomic.Pointer[config.RuntimeConfig]
	cfg.Store(testAlertConfig())
	var attempts atomic.Int32
	delivered := make(chan string, 4)
	n := NewNotifier(cfg.Load, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.telegram.org" || r.Method != http.MethodPost {
			t.Error("notification must use the Telegram HTTPS POST endpoint")
		}
		var body struct {
			ChatID string `json:"chat_id"`
			Text   string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if strings.Contains(body.Text, "TOKEN") {
			t.Error("message exposed a credential")
		}
		status, response := http.StatusOK, `{"ok":true}`
		if attempts.Add(1) == 1 {
			status, response = http.StatusServiceUnavailable, `{"ok":false}`
		} else {
			delivered <- body.ChatID
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(response)), Header: http.Header{}}, nil
	})}, nil)
	n.Start()
	defer n.Stop()
	if !n.Report(DiskPressure) || n.Report(DiskPressure) {
		t.Fatal("one pending incident must be queued only once")
	}
	select {
	case <-delivered:
	case <-time.After(3 * time.Second):
		t.Fatal("retry never delivered")
	}
	if n.Report(DiskPressure) {
		t.Fatal("repeated incident bypassed the cooldown")
	}
	updated := *cfg.Load()
	updated.AlertTelegramChatID = "-1007654321"
	cfg.Store(&updated)
	if err := n.SendTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if chat := <-delivered; chat != updated.AlertTelegramChatID {
		t.Fatal("test delivery did not read the current settings")
	}
	disabled := updated
	disabled.AlertTelegramEnabled = false
	cfg.Store(&disabled)
	if n.Report(NodePoolUnavailable) {
		t.Fatal("disabled channel queued a notification")
	}
}

func TestTelegramFailuresNeverExposeTokenURLOrProviderBody(t *testing.T) {
	cfg := testAlertConfig()
	var mu sync.Mutex
	var logs strings.Builder
	n := NewNotifier(func() *config.RuntimeConfig { return cfg }, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("transport failed at %s", r.URL.String())
	})}, func(format string, args ...any) { mu.Lock(); defer mu.Unlock(); fmt.Fprintf(&logs, format, args...) })
	n.Start()
	defer n.Stop()
	if err := n.SendTest(context.Background()); err == nil || strings.Contains(err.Error(), cfg.AlertTelegramBotToken) || strings.Contains(err.Error(), "https://") {
		t.Fatal("failure must contain only a safe classification")
	}
	n.Report(UsageRefreshFailed)
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		text := logs.String()
		mu.Unlock()
		if text != "" {
			if strings.Contains(text, cfg.AlertTelegramBotToken) || strings.Contains(text, "https://") {
				t.Fatal("notification logger exposed a token-bearing URL")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("delivery failure was not observable")
}

func TestNotifierStopCancelsInFlightDelivery(t *testing.T) {
	entered := make(chan struct{})
	n := NewNotifier(testAlertConfig, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}, nil)
	n.Start()
	n.Report(DatabaseLocked)
	<-entered
	done := make(chan struct{})
	go func() { n.Stop(); n.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the Telegram request")
	}
}

type codedError int

func (e codedError) Error() string { return "fixture SQLite failure" }
func (e codedError) Code() int     { return int(e) }

func TestDatabaseLockClassification(t *testing.T) {
	for _, err := range []error{codedError(5), codedError(6), codedError(5 | 2<<8), fmt.Errorf("wrapped: %w", codedError(6)), errors.New("database is locked (SQLITE_BUSY)")} {
		if !IsDatabaseLocked(err) {
			t.Fatal("SQLite lock was not detected")
		}
	}
	for _, err := range []error{nil, codedError(1), syscall.ENOSPC, errors.New("ordinary network failure")} {
		if IsDatabaseLocked(err) {
			t.Fatal("unrelated failure classified as a database lock")
		}
	}
}
