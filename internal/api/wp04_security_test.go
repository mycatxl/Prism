package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/model"
	"prism/internal/state"
)

// --- helpers ---

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

func doAuthedRequest(handler http.Handler, remoteAddr, xff, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// --- §4.6 management API login failure limiting ---

// TestAuthRateLimit_BlocksAfterTenFailures covers the acceptance requirement:
// eleven consecutive failures return 429 with Retry-After, and the block also
// applies to a correct token from the same address.
func TestAuthRateLimit_BlocksAfterTenFailures(t *testing.T) {
	limiter := NewAuthFailureLimiter(10, time.Minute, 5*time.Minute, nil)
	handler := AuthMiddleware("correct-token", limiter, okHandler())

	for i := 1; i <= 10; i++ {
		rec := doAuthedRequest(handler, "192.0.2.10:4000", "", "wrong-token")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i, rec.Code)
		}
		if rec.Header().Get("Retry-After") != "" {
			t.Fatalf("attempt %d: unexpected Retry-After before the block", i)
		}
	}

	rec := doAuthedRequest(handler, "192.0.2.10:4000", "", "wrong-token")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th attempt: got %d, want 429 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "RATE_LIMITED") {
		t.Fatalf("11th attempt: body %s does not mention RATE_LIMITED", rec.Body.String())
	}
	retryAfter := rec.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("11th attempt: missing Retry-After header")
	}
	if retryAfter != "300" {
		t.Fatalf("Retry-After: got %q, want %q", retryAfter, "300")
	}

	rec = doAuthedRequest(handler, "192.0.2.10:4000", "", "correct-token")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked address with a valid token: got %d, want 429", rec.Code)
	}

	// A different client address is unaffected.
	rec = doAuthedRequest(handler, "192.0.2.11:4000", "", "correct-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("other address: got %d, want 200", rec.Code)
	}
}

// TestAuthRateLimit_IgnoresForgedXForwardedFor verifies that an attacker cannot
// rotate X-Forwarded-For to escape the limit while the peer is not a trusted
// proxy.
func TestAuthRateLimit_IgnoresForgedXForwardedFor(t *testing.T) {
	limiter := NewAuthFailureLimiter(10, time.Minute, 5*time.Minute, nil)
	handler := AuthMiddleware("correct-token", limiter, okHandler())

	for i := 1; i <= 10; i++ {
		xff := "203.0.113." + strconv.Itoa(i)
		rec := doAuthedRequest(handler, "192.0.2.20:4000", xff, "wrong-token")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i, rec.Code)
		}
	}
	rec := doAuthedRequest(handler, "192.0.2.20:4000", "203.0.113.200", "wrong-token")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("forged X-Forwarded-For bypassed the limit: got %d, want 429", rec.Code)
	}
}

// TestAuthRateLimit_TrustsConfiguredProxyXForwardedFor verifies that the last
// untrusted X-Forwarded-For address becomes the limit key when the peer is a
// configured trusted proxy.
func TestAuthRateLimit_TrustsConfiguredProxyXForwardedFor(t *testing.T) {
	limiter := NewAuthFailureLimiter(10, time.Minute, 5*time.Minute, []string{"10.0.0.0/8"})
	handler := AuthMiddleware("correct-token", limiter, okHandler())

	for i := 1; i <= 10; i++ {
		rec := doAuthedRequest(handler, "10.1.2.3:4000", "198.51.100.7", "wrong-token")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i, rec.Code)
		}
	}
	rec := doAuthedRequest(handler, "10.1.2.3:4000", "198.51.100.7", "wrong-token")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("forwarded client: got %d, want 429", rec.Code)
	}

	// Another forwarded client through the same proxy is not blocked.
	rec = doAuthedRequest(handler, "10.1.2.3:4000", "198.51.100.8", "correct-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("other forwarded client: got %d, want 200", rec.Code)
	}

	// The rightmost untrusted hop wins: trusted proxies inside the chain are
	// skipped.
	chainLimiter := NewAuthFailureLimiter(1, time.Minute, time.Minute, []string{"10.0.0.0/8"})
	chainHandler := AuthMiddleware("correct-token", chainLimiter, okHandler())
	if rec := doAuthedRequest(chainHandler, "10.9.9.9:1", "198.51.100.9, 10.4.4.4", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("chained XFF first attempt: got %d, want 401", rec.Code)
	}
	if rec := doAuthedRequest(chainHandler, "10.9.9.9:1", "198.51.100.9, 10.4.4.4", "wrong"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("chained XFF second attempt: got %d, want 429", rec.Code)
	}
}

// TestAuthRateLimit_SuccessfulRequestsAreNotCounted verifies that only failed
// authentications count towards the limit.
func TestAuthRateLimit_SuccessfulRequestsAreNotCounted(t *testing.T) {
	limiter := NewAuthFailureLimiter(10, time.Minute, 5*time.Minute, nil)
	handler := AuthMiddleware("correct-token", limiter, okHandler())

	for i := 0; i < 20; i++ {
		rec := doAuthedRequest(handler, "192.0.2.30:4000", "", "correct-token")
		if rec.Code != http.StatusOK {
			t.Fatalf("success %d: got %d, want 200", i, rec.Code)
		}
	}
	if len(limiter.entries) != 0 {
		t.Fatalf("successful requests created %d limiter entries, want 0", len(limiter.entries))
	}
	for i := 1; i <= 10; i++ {
		rec := doAuthedRequest(handler, "192.0.2.30:4000", "", "wrong-token")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: got %d, want 401", i, rec.Code)
		}
	}
	if rec := doAuthedRequest(handler, "192.0.2.30:4000", "", "wrong-token"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("failure 11: got %d, want 429", rec.Code)
	}
}

// TestAuthRateLimit_CapsTableSize verifies the bounded table: once full, the
// oldest entry is evicted.
func TestAuthRateLimit_CapsTableSize(t *testing.T) {
	limiter := NewAuthFailureLimiter(10, time.Minute, 5*time.Minute, nil)
	limiter.maxEntries = 2
	limiter.RecordFailure("203.0.113.1")
	limiter.RecordFailure("203.0.113.2")
	limiter.RecordFailure("203.0.113.3")

	if len(limiter.entries) != 2 {
		t.Fatalf("entries: got %d, want 2", len(limiter.entries))
	}
	if _, ok := limiter.entries["203.0.113.1"]; ok {
		t.Fatal("oldest entry was not evicted")
	}
	if _, ok := limiter.entries["203.0.113.3"]; !ok {
		t.Fatal("newest entry missing")
	}
}

// --- §4.7 audit logging ---

type fakeAuditStore struct {
	mu        sync.Mutex
	nextID    int64
	entries   []model.AuditEntry
	pruneCall struct {
		olderThanNs int64
		keepMax     int
	}
}

var _ AuditLogStore = (*fakeAuditStore)(nil)

func (f *fakeAuditStore) AppendAudit(entry model.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	entry.ID = f.nextID
	f.entries = append(f.entries, entry)
	return nil
}

func (f *fakeAuditStore) PruneAudit(olderThanNs int64, keepMax int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruneCall.olderThanNs = olderThanNs
	f.pruneCall.keepMax = keepMax
	return 0, nil
}

func (f *fakeAuditStore) snapshot() []model.AuditEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.AuditEntry(nil), f.entries...)
}

func (f *fakeAuditStore) ListAudit(beforeID int64, limit int) ([]model.AuditEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	if beforeID <= 0 {
		beforeID = int64(^uint64(0) >> 1)
	}
	result := make([]model.AuditEntry, 0, limit)
	for i := len(f.entries) - 1; i >= 0 && len(result) < limit; i-- {
		if f.entries[i].ID < beforeID {
			result = append(result, f.entries[i])
		}
	}
	return result, nil
}

// TestAuditLog_RecordsWritesOnly covers the acceptance requirement: successful
// management writes are recorded, reads and failed writes are not, and the
// detail holds key names only.
func TestAuditLog_RecordsWritesOnly(t *testing.T) {
	const (
		adminToken = "admin-token-for-audit"
		payload    = `{"name":"platform-secret-value","sticky_ttl":3600}`
	)

	store := &fakeAuditStore{}
	var sawBody string

	authed := http.NewServeMux()
	authed.Handle("PATCH /api/v1/platforms/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sawBody = string(body)
		WriteJSON(w, http.StatusOK, map[string]string{"status": "patched"})
	}))
	authed.Handle("POST /api/v1/platforms/{id}/actions/refresh", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	}))
	authed.Handle("POST /api/v1/platforms", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, http.StatusConflict, "CONFLICT", "already exists")
	}))
	authed.Handle("GET /api/v1/platforms", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "listed"})
	}))

	handler := RequestBodyLimitMiddleware(1<<20, AuditMiddleware(store, adminToken, 1<<20, authed))

	// 1. A successful write is audited and the handler still sees the full body.
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/platforms/plat-1", strings.NewReader(payload))
	req.RemoteAddr = "192.0.2.50:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH: got %d, want 200", rec.Code)
	}
	if sawBody != payload {
		t.Fatalf("handler body: got %q, want %q", sawBody, payload)
	}

	entries := store.snapshot()
	if len(entries) != 1 {
		t.Fatalf("audit entries after PATCH: got %d, want 1", len(entries))
	}
	entry := entries[0]
	sum := sha256.Sum256([]byte(adminToken))
	if want := hex.EncodeToString(sum[:4]); entry.Actor != want {
		t.Fatalf("actor: got %q, want sha256 prefix %q", entry.Actor, want)
	}
	if entry.Action != "PATCH /api/v1/platforms/{id}" {
		t.Fatalf("action: got %q", entry.Action)
	}
	if entry.Target != "id=plat-1" {
		t.Fatalf("target: got %q, want %q", entry.Target, "id=plat-1")
	}
	if entry.RemoteAddr != "192.0.2.50:1234" {
		t.Fatalf("remote_addr: got %q", entry.RemoteAddr)
	}
	if !strings.Contains(entry.Detail, "name") || !strings.Contains(entry.Detail, "sticky_ttl") {
		t.Fatalf("detail: %q does not list the body key names", entry.Detail)
	}
	if strings.Contains(entry.Detail, "platform-secret-value") || strings.Contains(entry.Detail, "3600") {
		t.Fatalf("detail: %q leaks request body values", entry.Detail)
	}
	if entry.AtNs <= 0 {
		t.Fatal("audit entry has no timestamp")
	}

	// 2. /actions/* routes are audited too.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/platforms/plat-1/actions/refresh", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("action route: got %d, want 202", rec.Code)
	}
	entries = store.snapshot()
	if len(entries) != 2 {
		t.Fatalf("audit entries after action route: got %d, want 2", len(entries))
	}
	if got := entries[1].Action; got != "POST /api/v1/platforms/{id}/actions/refresh" {
		t.Fatalf("action route record: got %q", got)
	}

	// 3. Reads are never audited.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/platforms", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: got %d, want 200", rec.Code)
	}
	if got := len(store.snapshot()); got != 2 {
		t.Fatalf("GET added audit entries: got %d, want 2", got)
	}

	// 4. Failed writes are not audited.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/platforms", strings.NewReader(`{"name":"x"}`))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("failed POST: got %d, want 409", rec.Code)
	}
	if got := len(store.snapshot()); got != 2 {
		t.Fatalf("failed write was audited: got %d entries, want 2", got)
	}
}

// TestAuditLog_ListEndpoint covers GET /api/v1/audit-logs pagination and the
// 200 entry limit cap.
func TestAuditLog_ListEndpoint(t *testing.T) {
	store := &fakeAuditStore{}
	for i := 0; i < 5; i++ {
		_ = store.AppendAudit(model.AuditEntry{
			AtNs:       int64(i),
			Actor:      "abcd1234",
			RemoteAddr: "192.0.2.60:1",
			Action:     "POST /api/v1/platforms",
			Detail:     `{"keys":[]}`,
		})
	}
	handler := HandleListAuditLogs(store)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs?limit=3", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("limit=3: got %d, want 200", rec.Code)
	}
	var page struct {
		Items []model.AuditEntry `json:"items"`
		Limit int                `json:"limit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v (body=%s)", err, rec.Body.String())
	}
	if len(page.Items) != 3 || page.Limit != 3 {
		t.Fatalf("limit=3: got %d items (limit %d), want 3", len(page.Items), page.Limit)
	}
	if page.Items[0].ID != 5 {
		t.Fatalf("items are not ordered by descending id: first id %d", page.Items[0].ID)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs?before_id=4", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("before_id=4: got %d, want 200", rec.Code)
	}
	page = struct {
		Items []model.AuditEntry `json:"items"`
		Limit int                `json:"limit"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if len(page.Items) == 0 || page.Items[0].ID != 3 {
		t.Fatalf("before_id=4: first id %d, want 3", page.Items[0].ID)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs?limit=201", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("limit=201: got %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs?limit=200", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("limit=200: got %d, want 200", rec.Code)
	}
}

// TestAuditLog_RetentionPolicy pins the retention numbers (90 days, 100000 rows)
// and checks that *state.StateRepo satisfies AuditLogStore.
func TestAuditLog_RetentionPolicy(t *testing.T) {
	var _ AuditLogStore = (*state.StateRepo)(nil)

	store := &fakeAuditStore{}
	pruneInterval := 24 * time.Hour
	stop := StartAuditPruner(store, pruneInterval)
	defer stop()

	if _, err := PruneAuditLogs(store); err != nil {
		t.Fatalf("PruneAuditLogs: %v", err)
	}
	if store.pruneCall.keepMax != 100000 {
		t.Fatalf("keepMax: got %d, want 100000", store.pruneCall.keepMax)
	}
	cutoff := time.Unix(0, store.pruneCall.olderThanNs)
	wantDays := 90
	gotDays := int(time.Since(cutoff).Hours() / 24)
	if gotDays < wantDays-1 || gotDays > wantDays+1 {
		t.Fatalf("retention cutoff: got %d days, want about %d", gotDays, wantDays)
	}
}
