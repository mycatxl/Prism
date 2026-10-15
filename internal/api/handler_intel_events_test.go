package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"prism/internal/intel/jobs"
	"prism/internal/service"
)

// HTTP-level coverage of the SSE endpoint GET /api/v1/intel/jobs/{id}/events.
// EventSource sends same-origin cookies automatically, so production routes the
// stream through the same identity middleware as every other management API.
// Query credentials are never accepted.

// newIntelEventsServer starts a real server carrying the SSE route over a live
// intel service, and returns it together with a freshly created job id.
func newIntelEventsServer(t *testing.T, limiter *AuthFailureLimiter) (*httptest.Server, string, *service.ControlPlaneService) {
	t.Helper()
	cp := &service.ControlPlaneService{}
	svc := wireIntelForTest(t, cp)

	job, err := svc.CreateJob(context.Background(), jobs.Request{
		Kind:  jobs.KindEgress,
		Scope: jobs.Scope{All: true},
	}, jobs.CreatedByAdmin(), jobs.PriorityManual)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	inner := http.NewServeMux()
	inner.Handle("GET /api/v1/intel/jobs/{id}/events", HandleIntelJobEvents(cp))
	identity := &Identity{
		AdminKeys: fakeAdminKeys{
			credential: testAdminCredential,
			identity:   AdminKeyIdentity{KeyID: "test-key", UserID: "u-admin", Role: "admin"},
		},
		FailureLimiter: limiter,
	}
	handler := limiterMiddleware(limiter, identity.Middleware(inner))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, job.ID, cp
}

func newEventsLimiter() *AuthFailureLimiter {
	return NewAuthFailureLimiter(1000, time.Minute, time.Minute, nil)
}

// eventsURL renders the endpoint URL for a job, optionally carrying an ignored
// query string used to prove query credentials do not authenticate requests.
func eventsURL(srv *httptest.Server, jobID, query string) string {
	url := srv.URL + "/api/v1/intel/jobs/" + jobID + "/events"
	if query != "" {
		url += "?" + query
	}
	return url
}

// TestIntelJobEvents_Authentication pins the single management authentication
// path used by the SSE endpoint.
func TestIntelJobEvents_Authentication(t *testing.T) {
	for _, tc := range []struct {
		name       string
		query      string
		header     string
		wantStatus int
	}{
		{"admin access key", "", "Bearer " + testAdminCredential, http.StatusOK},
		{"query token without header", "access_token=" + testAdminCredential, "", http.StatusUnauthorized},
		{"wrong query token with no header", "access_token=nope", "", http.StatusUnauthorized},
		{"wrong header token", "", "Bearer nope", http.StatusUnauthorized},
		{"bare header value", "", testAdminCredential, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, jobID, _ := newIntelEventsServer(t, newEventsLimiter())

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, eventsURL(srv, jobID, tc.query), nil)
			if err != nil {
				t.Fatalf("NewRequestWithContext: %v", err)
			}
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, tc.wantStatus, body)
			}
			if tc.wantStatus != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				if strings.Contains(string(body), "event:") {
					t.Fatalf("a rejected request wrote an SSE frame: %q", body)
				}
				return
			}
			if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
				t.Errorf("Content-Type = %q, want text/event-stream", ct)
			}
			if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", cc)
			}
			if frames := readSSEUntil(t, resp.Body, "event: progress", 5*time.Second); len(frames) == 0 {
				t.Error("the stream must replay the current state first")
			}
		})
	}
}

// TestIntelJobEvents_QueryTokenDoesNotAuthenticateOtherRoutes pins that a query
// credential cannot authenticate any management route.
func TestIntelJobEvents_QueryTokenDoesNotAuthenticateOtherRoutes(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	identity := &Identity{AdminKeys: fakeAdminKeys{
		credential: testAdminCredential,
		identity:   AdminKeyIdentity{KeyID: "test-key", UserID: "u-admin", Role: "admin"},
	}}
	guarded := identity.Middleware(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes?access_token="+testAdminCredential, nil)
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("Identity accepted a query token: status = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminCredential)
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Identity rejected a valid admin key: status = %d", rec.Code)
	}
}

// TestIntelJobEvents_UnknownJobIsNotFound pins that a subscriber cannot open a
// stream for a job that does not exist.
func TestIntelJobEvents_UnknownJobIsNotFound(t *testing.T) {
	srv, _, _ := newIntelEventsServer(t, newEventsLimiter())
	req, err := http.NewRequest(http.MethodGet, eventsURL(srv, "does-not-exist", "access_token="+testAdminCredential), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testAdminCredential)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestIntelJobEvents_FailedAuthsAreRateLimited pins the shared failure limiter
// around the identity middleware.
func TestIntelJobEvents_FailedAuthsAreRateLimited(t *testing.T) {
	limiter := NewAuthFailureLimiter(3, time.Minute, time.Minute, nil)
	srv, jobID, _ := newIntelEventsServer(t, limiter)

	for i := 0; i < 3; i++ {
		resp, err := srv.Client().Get(eventsURL(srv, jobID, ""))
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, resp.StatusCode)
		}
	}

	resp, err := srv.Client().Get(eventsURL(srv, jobID, ""))
	if err != nil {
		t.Fatalf("GET after the failure budget: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after the failure budget: status = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("a 429 must carry Retry-After")
	}
}

// TestIntelJobEvents_SubscriberCapIsEnforced pins the hub cap over HTTP: once
// the subscriber slots are gone, a further subscriber is refused instead of
// hanging. The refusal is 429 (the service documents it that way), never a 500.
func TestIntelJobEvents_SubscriberCapIsEnforced(t *testing.T) {
	srv, jobID, cp := newIntelEventsServer(t, newEventsLimiter())

	// Take every slot through the service: opening 64 real streams is slow and
	// adds nothing over the handler-level assertion below.
	subs := make([]*jobs.Subscription, 0, jobs.SSEMaxSubscribers)
	for i := 0; i < jobs.SSEMaxSubscribers; i++ {
		sub, err := cp.SubscribeIntelJob(context.Background(), jobID)
		if err != nil {
			t.Fatalf("subscriber %d: %v", i, err)
		}
		subs = append(subs, sub)
	}
	t.Cleanup(func() {
		for _, sub := range subs {
			sub.Close()
		}
	})

	req, err := http.NewRequest(http.MethodGet, eventsURL(srv, jobID, "access_token="+testAdminCredential), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testAdminCredential)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET with the cap reached: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("with the cap reached: status = %d, want 429 (body %s)", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "event:") {
		t.Fatalf("a refused subscriber got an SSE frame: %q", body)
	}
}

// TestIntelJobEvents_StreamEndsWhenTheJobFinishes pins the terminal frame: the
// handler writes an `end` event and returns instead of keeping the connection
// open forever.
func TestIntelJobEvents_StreamEndsWhenTheJobFinishes(t *testing.T) {
	cp := &service.ControlPlaneService{}
	svc := wireIntelForTest(t, cp)

	job, err := svc.CreateJob(context.Background(), jobs.Request{
		Kind:  jobs.KindEgress,
		Scope: jobs.Scope{All: true},
	}, jobs.CreatedByAdmin(), jobs.PriorityManual)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	stored, err := svc.Manager().Store().GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !stored.Terminal() {
		t.Fatalf("job = %+v, want a terminal status before subscribing", stored)
	}

	inner := http.NewServeMux()
	inner.Handle("GET /api/v1/intel/jobs/{id}/events", HandleIntelJobEvents(cp))
	identity := &Identity{AdminKeys: fakeAdminKeys{
		credential: testAdminCredential,
		identity:   AdminKeyIdentity{KeyID: "test-key", UserID: "u-admin", Role: "admin"},
	}}
	srv := httptest.NewServer(identity.Middleware(inner))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodGet, eventsURL(srv, job.ID, "access_token="+testAdminCredential), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testAdminCredential)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the stream: %v", err)
	}
	if !strings.Contains(string(body), "event: end") {
		t.Fatalf("a finished job must terminate the stream, got %q", body)
	}
}

// readSSEUntil reads the stream until the needle shows up, the stream ends or the
// deadline passes, and returns the lines it saw.
func readSSEUntil(t *testing.T, body io.Reader, needle string, timeout time.Duration) []string {
	t.Helper()
	type result struct {
		lines []string
	}
	done := make(chan result, 1)
	go func() {
		var lines []string
		scanner := bufio.NewScanner(body)
		for scanner.Scan() {
			line := scanner.Text()
			lines = append(lines, line)
			if strings.Contains(line, needle) {
				break
			}
		}
		done <- result{lines: lines}
	}()
	select {
	case res := <-done:
		return res.lines
	case <-time.After(timeout):
		t.Fatalf("the stream never produced %q", needle)
		return nil
	}
}
