package api

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"strings"
)

//go:embed web/dist
var webFS embed.FS

func newWebUIHandler() http.Handler {
	mux := http.NewServeMux()

	// Extract dist directory from embedded FS
	distFS, err := fs.Sub(webFS, "web/dist")
	if err != nil {
		log.Printf("WebUI: failed to load dist files: %v", err)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "WebUI not available", http.StatusInternalServerError)
		})
	}

	// Serve static assets (JS, CSS, images)
	mux.Handle("/assets/", http.FileServer(http.FS(distFS)))

	// Serve static images from root
	mux.HandleFunc("/prism-mark.png", func(w http.ResponseWriter, r *http.Request) {
		serveStaticFile(w, r, distFS, "prism-mark.png")
	})
	mux.HandleFunc("/vite.svg", func(w http.ResponseWriter, r *http.Request) {
		serveStaticFile(w, r, distFS, "vite.svg")
	})

	// Serve index.html for root and all other paths (SPA fallback)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// If requesting a specific file that exists, serve it
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// Try to read the requested file
		if _, err := fs.Stat(distFS, path); err == nil && path != "index.html" {
			http.FileServer(http.FS(distFS)).ServeHTTP(w, r)
			return
		}

		// Fallback to index.html for SPA routing
		indexHTML, err := fs.ReadFile(distFS, "index.html")
		if err != nil {
			http.Error(w, "WebUI not available", http.StatusInternalServerError)
			log.Printf("WebUI: failed to read index.html: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})

	return mux
}

func serveStaticFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, filename string) {
	data, err := fs.ReadFile(fsys, filename)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Set content type based on extension
	if strings.HasSuffix(filename, ".png") {
		w.Header().Set("Content-Type", "image/png")
	} else if strings.HasSuffix(filename, ".svg") {
		w.Header().Set("Content-Type", "image/svg+xml")
	}

	w.Write(data)
}
