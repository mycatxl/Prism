// Package alerting delivers operational faults without requiring a database write.
package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"prism/internal/config"
)

type Kind string

const (
	DiskPressure        Kind = "disk_pressure"
	DatabaseLocked      Kind = "database_locked"
	UsageRefreshFailed  Kind = "usage_refresh_failed"
	NodePoolUnavailable Kind = "node_pool_unavailable"
	BackgroundPanic     Kind = "background_panic"
	PersistenceFailed   Kind = "persistence_failed"
	TestNotification    Kind = "test"
)

// Messages are fixed classifications. Raw errors, URLs, credentials, customer
// identifiers and recovered panic values never enter the external payload.
var messages = map[Kind]string{
	DiskPressure:        "Core storage is low, full or unavailable. Check the state, cache and log filesystems.",
	DatabaseLocked:      "SQLite lock contention affected a background persistence operation. Check storage latency and competing writers.",
	UsageRefreshFailed:  "Usage accounting or refresh failed. Check durable accounting, pending reservations and storage; affected access may remain closed.",
	NodePoolUnavailable: "The node pool has no healthy enabled nodes, or a previously populated pool disappeared. Check subscriptions, upstreams and probes.",
	BackgroundPanic:     "A background task recovered a panic. Inspect local diagnostics and node-pool/accounting health.",
	PersistenceFailed:   "Cache persistence failed; dirty entries remain queued for retry. Inspect local storage diagnostics.",
	TestNotification:    "Telegram operational notifications are connected.",
}

var ErrDisabled = errors.New("Telegram notifications are disabled or incomplete")

type Notifier struct {
	config    func() *config.RuntimeConfig
	client    *http.Client
	logf      func(string, ...any)
	queue     chan Kind
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	startOnce sync.Once
	started   atomic.Bool
	mu        sync.Mutex
	pending   map[Kind]bool
	last      map[Kind]time.Time
}

func NewNotifier(cfg func() *config.RuntimeConfig, client *http.Client, logf func(string, ...any)) *Notifier {
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second}}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copy.Timeout == 0 {
		copy.Timeout = 5 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Notifier{config: cfg, client: &copy, logf: logf, queue: make(chan Kind, 16), ctx: ctx, cancel: cancel, done: make(chan struct{}), pending: make(map[Kind]bool), last: make(map[Kind]time.Time)}
}

func (n *Notifier) current() *config.RuntimeConfig {
	if n == nil || n.config == nil {
		return nil
	}
	return n.config()
}

// Report is nonblocking and its state is bounded by the fixed incident vocabulary.
func (n *Notifier) Report(kind Kind) bool {
	c := n.current()
	if c == nil || !c.AlertTelegramEnabled || messages[kind] == "" || kind == TestNotification || n.ctx.Err() != nil {
		return false
	}
	cooldown := c.AlertCooldownMinutes
	if cooldown <= 0 {
		cooldown = config.DefaultAlertCooldownMinutes
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.pending[kind] || time.Since(n.last[kind]) < time.Duration(cooldown)*time.Minute {
		return false
	}
	select {
	case n.queue <- kind:
		n.pending[kind] = true
		return true
	default:
		if n.logf != nil {
			n.logf("operational alert queue full; incident=%s", kind)
		}
		return false
	}
}

func (n *Notifier) ReportError(kind Kind, err error) {
	if err == nil {
		return
	}
	n.Report(kind)
	if IsDatabaseLocked(err) {
		n.Report(DatabaseLocked)
	}
	var coded interface{ Code() int }
	if errors.Is(err, syscall.ENOSPC) || (errors.As(err, &coded) && coded.Code()&255 == 13) {
		n.Report(DiskPressure)
	}
}

func IsDatabaseLocked(err error) bool {
	if err == nil {
		return false
	}
	var coded interface{ Code() int }
	if errors.As(err, &coded) && (coded.Code()&255 == 5 || coded.Code()&255 == 6) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "database is locked") || strings.Contains(text, "database table is locked") || strings.Contains(text, "sqlite_busy") || strings.Contains(text, "sqlite_locked")
}

func (n *Notifier) Start() {
	if n == nil {
		return
	}
	n.startOnce.Do(func() {
		n.started.Store(true)
		go func() {
			defer close(n.done)
			for {
				select {
				case <-n.ctx.Done():
					return
				case kind := <-n.queue:
					n.deliver(kind)
				}
			}
		}()
	})
}

func (n *Notifier) Stop() {
	if n == nil {
		return
	}
	n.cancel()
	if n.started.Load() {
		<-n.done
	}
}

func (n *Notifier) deliver(kind Kind) {
	attempted := false
	defer func() {
		n.mu.Lock()
		delete(n.pending, kind)
		if attempted {
			n.last[kind] = time.Now()
		}
		n.mu.Unlock()
	}()
	for attempt := 0; attempt < 3; attempt++ {
		retry, err := n.send(n.ctx, kind)
		if err == nil || errors.Is(err, ErrDisabled) || n.ctx.Err() != nil {
			attempted = err == nil
			return
		}
		attempted = true
		if !retry || attempt == 2 {
			if n.logf != nil {
				n.logf("operational alert delivery failed; incident=%s", kind)
			}
			return
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-n.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (n *Notifier) SendTest(ctx context.Context) error {
	_, err := n.send(ctx, TestNotification)
	return err
}

func (n *Notifier) send(ctx context.Context, kind Kind) (retry bool, err error) {
	defer func() {
		if recover() != nil {
			retry, err = false, errors.New("Telegram delivery failed")
		}
	}()
	c := n.current()
	if c == nil || !c.AlertTelegramEnabled || c.AlertTelegramBotToken == "" || c.AlertTelegramChatID == "" {
		return false, ErrDisabled
	}
	// Validate a copy, never mutate a published runtime snapshot.
	copy := *c
	if copy.ValidateOperationalAlerts() != nil {
		return false, ErrDisabled
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"chat_id": c.AlertTelegramChatID, "text": "Prism operational alert: " + string(kind) + "\n" + messages[kind], "disable_web_page_preview": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+c.AlertTelegramBotToken+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return false, errors.New("Telegram request could not be created")
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := n.client.Do(req)
	if err != nil {
		return true, errors.New("Telegram connection failed")
	}
	defer response.Body.Close()
	var result struct {
		OK bool `json:"ok"`
	}
	if response.StatusCode != http.StatusOK {
		return response.StatusCode == 429 || response.StatusCode >= 500, fmt.Errorf("Telegram rejected the notification (HTTP %d)", response.StatusCode)
	}
	if json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&result) != nil || !result.OK {
		return false, errors.New("Telegram did not acknowledge the notification")
	}
	return false, nil
}
