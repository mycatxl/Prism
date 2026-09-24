package api

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
)

// webDistDir is the directory inside the embedded FS that holds the compiled
// Prism workbench (React + Vite) assets.
const webDistDir = "web/dist"

// webNotBuiltMessage is returned when the embedded WebUI has no index.html,
// i.e. `make web` was never run before `go build`.
const webNotBuiltMessage = "WebUI not built. Run: make web\n"

//go:embed all:web/dist
var webFS embed.FS

// newWebUIHandler builds the handler that serves the embedded SPA under /ui/.
//
// The handler is registered on "/ui/" and inspects the full request path, so it
// must not be wrapped in http.StripPrefix.
func newWebUIHandler() http.Handler {
	distFS, err := fs.Sub(webFS, webDistDir)
	if err != nil {
		log.Printf("WebUI embed disabled: %v", err)
		return newWebUINotBuiltHandler()
	}
	return newWebUIHandlerFromFS(distFS)
}

func newWebUINotBuiltHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(webNotBuiltMessage))
	})
}

func newWebUIHandlerFromFS(distFS fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		if !strings.HasPrefix(r.URL.Path, "/ui/") {
			http.NotFound(w, r)
			return
		}

		assetPath := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, "/ui/")), "/")
		if assetPath == "" || assetPath == "." {
			assetPath = "index.html"
		}

		if info, err := fs.Stat(distFS, assetPath); err == nil && !info.IsDir() {
			http.ServeFileFS(w, r, distFS, assetPath)
			return
		}

		// Missing requests with file-like paths should remain 404.
		if path.Ext(assetPath) != "" {
			http.NotFound(w, r)
			return
		}

		// SPA fallback: any extension-less path is served index.html.
		if _, err := fs.Stat(distFS, "index.html"); err != nil {
			newWebUINotBuiltHandler().ServeHTTP(w, r)
			return
		}
		http.ServeFileFS(w, r, distFS, "index.html")
	})
}

// newRootRedirectHandler redirects "/" to "/ui/".
func newRootRedirectHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})
}

// newUIRootRedirectHandler redirects "/ui" to "/ui/".
func newUIRootRedirectHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ui" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})
}
