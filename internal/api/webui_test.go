package api

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"testing/fstest"
)

// TestWebDistCarriesATrackedPlaceholder guards the one thing the compiler cannot
// guard for itself.
//
// `internal/api/webui.go` embeds `all:web/dist`, and go:embed is a compile-time
// error when the pattern matches no files: a fresh clone whose dist/ directory
// does not exist cannot build the backend at all, not even with `make backend`,
// which the README documents as the backend-only path. Both .gitignore files
// already reserved an exception for `dist/.gitkeep`; this asserts the file is
// actually present, so the reserved exception cannot quietly rot again.
func TestWebDistCarriesATrackedPlaceholder(t *testing.T) {
	entries, err := os.ReadDir(webDistDir)
	if err != nil {
		t.Fatalf("read %s: %v -- go:embed all:%s cannot compile without this directory", webDistDir, err, webDistDir)
	}
	if len(entries) == 0 {
		t.Fatalf("%s is empty -- go:embed all:%s fails in a fresh clone", webDistDir, webDistDir)
	}
}

// TestWebUINotBuiltHandlerAnswers503 pins the documented backend-only behaviour:
// with no compiled UI the panel answers 503 and names the make target that fixes
// it, rather than 404 or a blank page.
func TestWebUINotBuiltHandlerAnswers503(t *testing.T) {
	rec := httptest.NewRecorder()
	newWebUINotBuiltHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Body.String(); got != webNotBuiltMessage {
		t.Fatalf("body = %q, want %q", got, webNotBuiltMessage)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
}

// TestWebUIHandlerFromEmptyFSSaysNotBuilt covers the state a fresh clone is in:
// the embed resolves (the directory exists, it just holds no UI), so the handler
// is constructed normally and then reports the missing index.html itself.
func TestWebUIHandlerFromEmptyFSSaysNotBuilt(t *testing.T) {
	h := newWebUIHandlerFromFS(fstest.MapFS{})

	for _, path := range []string{"/ui/", "/ui/", "/ui/nodes"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status = %d, want %d", path, rec.Code, http.StatusServiceUnavailable)
		}
		if got := rec.Body.String(); got != webNotBuiltMessage {
			t.Fatalf("%s: body = %q, want %q", path, got, webNotBuiltMessage)
		}
	}
}

// TestWebUIRootRedirectHandler pins the "/ui" -> "/ui/" redirect. The mux wires
// "/ui" to this handler and "/ui/" to newWebUIHandler, so without it a browser
// asking for /ui (no slash) would fall through to the root redirect instead.
func TestWebUIRootRedirectHandler(t *testing.T) {
	h := newUIRootRedirectHandler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if loc := rec.Header().Get("Location"); loc != "/ui/" {
		t.Fatalf("Location = %q, want %q", loc, "/ui/")
	}

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/ui/"},
		{http.MethodGet, "/other"},
		{http.MethodPost, "/ui"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: status = %d, want %d", tc.method, tc.path, rec.Code, http.StatusNotFound)
		}
	}
}

// TestWebUIHandlerServesAssetsAndFallsBackToIndex covers the routing contract of
// the embedded panel: real files are served, extension-less paths fall back to
// index.html (the SPA router), and anything that looks like a missing file stays
// a 404 instead of being answered with HTML.
func TestWebUIHandlerServesAssetsAndFallsBackToIndex(t *testing.T) {
	const indexBody = "<html>prism-workbench</html>"
	distFS := fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte(indexBody)},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log('prism')")},
	}
	h := newWebUIHandlerFromFS(distFS)

	cases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   string
	}{
		{"ui root serves index", http.MethodGet, "/ui/", http.StatusOK, indexBody},
		{"asset is served", http.MethodGet, "/ui/assets/app.js", http.StatusOK, "console.log('prism')"},
		// "/ui" without the trailing slash is wired to newUIRootRedirectHandler
		// in server.go, so this handler must not answer it (see
		// TestWebUIRootRedirectHandler).
		{"ui without slash is not this handler's job", http.MethodGet, "/ui", http.StatusNotFound, ""},
		{"spa route falls back to index", http.MethodGet, "/ui/nodes/abc", http.StatusOK, indexBody},
		{"missing asset stays 404", http.MethodGet, "/ui/assets/missing.js", http.StatusNotFound, ""},
		{"path outside /ui/ is 404", http.MethodGet, "/api/v1/nodes", http.StatusNotFound, ""},
		{"writes are refused", http.MethodPost, "/ui/", http.StatusNotFound, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantBody != "" {
				if got := rec.Body.String(); got != tc.wantBody {
					t.Fatalf("body = %q, want %q", got, tc.wantBody)
				}
			}
		})
	}
}

// TestWebUIHandlerUsesTheEmbeddedFS checks the production constructor wires the
// real embed rather than falling through to the not-built handler by accident.
// It must not fail just because no UI was compiled: the fallback is a documented
// state, so the assertion is that the handler answers *something* sane for /ui/.
func TestWebUIHandlerUsesTheEmbeddedFS(t *testing.T) {
	if _, err := fs.Stat(webFS, webDistDir); err != nil {
		t.Fatalf("embed does not contain %s: %v", webDistDir, err)
	}

	h := newWebUIHandler()
	if h == nil {
		t.Fatal("newWebUIHandler returned nil")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))

	switch rec.Code {
	case http.StatusOK:
		if rec.Body.Len() == 0 {
			t.Fatal("a built UI answered 200 with an empty body")
		}
	case http.StatusServiceUnavailable:
		if got := rec.Body.String(); got != webNotBuiltMessage {
			t.Fatalf("503 body = %q, want %q", got, webNotBuiltMessage)
		}
	default:
		t.Fatalf("/ui/ answered %d; want 200 (built UI) or 503 (not built)", rec.Code)
	}
}
