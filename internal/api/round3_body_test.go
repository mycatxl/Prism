package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestRound3ConcurrentBodyBufferingHasGlobalBound(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{}, cap(apiBodyReadSlots))
	var wg sync.WaitGroup
	h := RequestBodyLimitMiddleware(1024, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for range cap(apiBodyReadSlots) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/public/ldc/notify", nil)
			req.Body = &round3BlockedBody{started: started, gate: gate}
			req.ContentLength = -1
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	for range cap(apiBodyReadSlots) {
		<-started
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/public/ldc/notify", strings.NewReader("x")))
	close(gate)
	wg.Wait()
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("concurrent body bound: status=%d", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/public/ldc/notify", strings.NewReader("x")))
	if rr.Code != 200 || len(apiBodyReadSlots) != 0 {
		t.Fatal("body admission slots leaked")
	}
}

type round3BlockedBody struct {
	started chan struct{}
	gate    chan struct{}
}

func (b *round3BlockedBody) Read([]byte) (int, error) {
	b.started <- struct{}{}
	<-b.gate
	return 0, io.EOF
}
func (*round3BlockedBody) Close() error { return nil }

func TestRound3BodyLimitBeforeIgnoredBodyHandlers(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		for _, chunked := range []bool{false, true} {
			for _, body := range []string{"1234", "12345"} {
				called := false
				h := RequestBodyLimitMiddleware(4, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(204) }))
				req := httptest.NewRequest(method, "/api/v1/public/ldc/notify?trade_status=TRADE_SUCCESS", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				if chunked {
					req.ContentLength = -1
				}
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, req)
				want := 204
				if len(body) > 4 {
					want = 413
				}
				if rr.Code != want || called != (want == 204) {
					t.Fatalf("method=%s chunked=%v size=%d status=%d called=%v", method, chunked, len(body), rr.Code, called)
				}
			}
		}
	}
}
