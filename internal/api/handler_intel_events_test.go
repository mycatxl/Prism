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
//
// The endpoint is the one route that deliberately bypasses AuthMiddleware: an
// EventSource cannot set an Authorization header, so it accepts the admin token
// from ?access_token= as well. That makes it the only place where the token may
// travel in a URL, and the only place where the query-parameter fallback is
// allowed to work at all. Both halves of that contract are pinned here, plus the
// subscriber cap the hub enforces and the terminal frame.
//
// The tests drive a real httptest.Server with the route registered on a mux, so
// the path parameter is populated exactly as it is in production.

// newIntelEventsServer starts a real server carrying the SSE route over a live
// intel service, and returns it together with a freshly created job id.
func newIntelEventsServer(t *testing.T, adminToken string, limiter *AuthFailureLimiter) (*httptest.Server, string, *service.ControlPlaneService) {
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

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/intel/jobs/{id}/events", HandleIntelJobEvents(adminToken, limiter, cp))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, job.ID, cp
}

func newEventsLimiter() *AuthFailureLimiter {
	return NewAuthFailureLimiter(1000, time.Minute, time.Minute, nil)
}

// eventsURL renders the endpoint URL for a job, optionally carrying the token in
// the query string.
func eventsURL(srv *httptest.Server, jobID, query string) string {
	url := srv.URL + "/api/v1/intel/jobs/" + jobID + "/events"
	if query != "" {
		url += "?" + query
	}
	return url
}

// TestIntelJobEvents_Authentication pins every way the endpoint decides whether
// a caller may subscribe.
func TestIntelJobEvents_Authentication(t *testing.T) {
	const token = "sse-admin-token"

	for _, tc := range []struct {
		name       string
		query      string
		header     string
		adminToken string
		wantStatus int
	}{
		{"query token", "access_token=" + token, "", token, http.StatusOK},
		{"authorization bearer", "", "Bearer " + token, token, http.StatusOK},
		{"authorization bare value", "", token, token, http.StatusOK},
		{"missing token", "", "", token, http.StatusUnauthorized},
		{"wrong query token", "access_token=nope", "", token, http.StatusUnauthorized},
		{"wrong header token", "", "Bearer nope", token, http.StatusUnauthorized},
		{"empty configured token accepts anyone", "", "", "", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, jobID, _ := newIntelEventsServer(t, tc.adminToken, newEventsLimiter())

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
				// A rejected subscription must never open the stream.
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
			// The first frames replay the current state so a reconnecting client
			// does not have to poll the job first.
			if frames := readSSEUntil(t, resp.Body, "event: progress", 5*time.Second); len(frames) == 0 {
				t.Error("the stream must replay the current state first")
			}
		})
	}
}

// TestIntelJobEvents_QueryTokenDoesNotAuthenticateOtherRoutes pins that the
// ?access_token= fallback is scoped to the SSE endpoint. If it ever leaked into
// AuthMiddleware, every management route would start accepting the admin token
// from a URL — and URLs end up in reverse-proxy access logs.
func TestIntelJobEvents_QueryTokenDoesNotAuthenticateOtherRoutes(t *testing.T) {
	const token = "sse-admin-token"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	guarded := AuthMiddleware(token, newEventsLimiter(), inner)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes?access_token="+token, nil)
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("AuthMiddleware accepted a query token: status = %d, want 401", rec.Code)
	}

	// The header still works on the management surface.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("AuthMiddleware rejected a valid header token: status = %d", rec.Code)
	}
}

// TestIntelJobEvents_UnknownJobIsNotFound pins that a subscriber cannot open a
// stream for a job that does not exist.
func TestIntelJobEvents_UnknownJobIsNotFound(t *testing.T) {
	srv, _, _ := newIntelEventsServer(t, "tok", newEventsLimiter())
	resp, err := srv.Client().Get(eventsURL(srv, "does-not-exist", "access_token=tok"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestIntelJobEvents_FailedAuthsAreRateLimited pins that a wrong token counts
// against the shared authentication-failure budget, so the SSE endpoint cannot
// be used to brute-force the admin token without being blocked.
func TestIntelJobEvents_FailedAuthsAreRateLimited(t *testing.T) {
	limiter := NewAuthFailureLimiter(3, time.Minute, time.Minute, nil)
	srv, jobID, _ := newIntelEventsServer(t, "right", limiter)

	// Exhaust the failure budget.
	for i := 0; i < 3; i++ {
		resp, err := srv.Client().Get(eventsURL(srv, jobID, "access_token=wrong"))
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, resp.StatusCode)
		}
	}

	// The next attempt is blocked before the token is even compared.
	resp, err := srv.Client().Get(eventsURL(srv, jobID, "access_token=right"))
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
	srv, jobID, cp := newIntelEventsServer(t, "tok", newEventsLimiter())

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

	resp, err := srv.Client().Get(eventsURL(srv, jobID, "access_token=tok"))
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
	if err := svc.Manager().Store().CancelJob(context.Background(), job.ID); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/intel/jobs/{id}/events", HandleIntelJobEvents("tok", newEventsLimiter(), cp))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(eventsURL(srv, job.ID, "access_token=tok"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// A terminal job must close the stream, so the body reaches EOF.
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
