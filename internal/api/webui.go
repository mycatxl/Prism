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
// must not be wrapped in http.StripPrefix. authRequired is reported at
// /ui/session.json; see newWebUIHandlerFromFS for why it is served from here.
func newWebUIHandler(authRequired bool) http.Handler {
	distFS, err := fs.Sub(webFS, webDistDir)
	if err != nil {
		log.Printf("WebUI embed disabled: %v", err)
		return newWebUINotBuiltHandler()
	}
	return newWebUIHandlerFromFS(distFS, authRequired)
}

func newWebUINotBuiltHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(webNotBuiltMessage))
	})
}

func newWebUIHandlerFromFS(distFS fs.FS, authRequired bool) http.Handler {
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

		// The console's own auth mode, answered here rather than from an authenticated
		// endpoint. The login page needs the answer *before* it holds a token, and a 401
		// there is a console error the operator cannot dismiss -- on the first screen of
		// every secured deployment. /ui/ is already gated by the access point's
		// allow_management (cmd/prism/inbound_mux.go), so this adds no reach the console
		// does not already have, and it replaces an inference ("401 means auth is on")
		// with the deployment stating the fact.
		if assetPath == "session.json" {
			w.Header().Set("Cache-Control", "no-store")
			WriteJSON(w, http.StatusOK, map[string]bool{"auth_required": authRequired})
			return
		}

		if info, err := fs.Stat(distFS, assetPath); err == nil && !info.IsDir() {
			http.ServeFileFS(w, r, distFS, assetPath)
			return
		}

		// No index.html means the UI was never compiled (`make web`), so every
		// /ui/ path reports how to build it. This comes before the file-like 404
		// below: "/ui/" resolves to "index.html", which has an extension, so the
		// not-built message would otherwise be unreachable for exactly the URL a
		// user opens first.
		if _, err := fs.Stat(distFS, "index.html"); err != nil {
			newWebUINotBuiltHandler().ServeHTTP(w, r)
			return
		}

		// Missing requests with file-like paths should remain 404.
		if path.Ext(assetPath) != "" {
			http.NotFound(w, r)
			return
		}

		// SPA fallback: any extension-less path is served index.html.
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
