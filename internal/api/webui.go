package api

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
)

//go:embed web
var webFS embed.FS

func newWebUIHandler() http.Handler {
	mux := http.NewServeMux()

	// Serve static files
	staticFS, err := fs.Sub(webFS, "web/static")
	if err != nil {
		log.Printf("WebUI: failed to load static files: %v", err)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "WebUI not available", http.StatusInternalServerError)
		})
	}
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// Serve index.html for root and all other paths
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		indexHTML, err := webFS.ReadFile("web/templates/index.html")
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
