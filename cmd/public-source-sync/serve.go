package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"prism/internal/publicsource"
)

// Serve mode: publish the aggregated snapshot over HTTP so Resin can pull it as
// a remote subscription instead of being pushed to over the Admin API.
//
// Why this exists: a remote subscription needs no admin token at all. The sync
// becomes a pure content source, Resin decides the fetch cadence through its own
// `update_interval`, and a sync outage just means Resin keeps serving the nodes
// it already has. The subscription can even be created by hand in the Resin UI,
// which removes the last reason to hand out an admin token.
//
// The endpoint is intentionally plain: Resin fetches it with a bare GET and no
// headers (netutil.Downloader with the clash.meta user agent), so a shared secret
// has to live in the path rather than in an Authorization header.
type snapshotServer struct {
	collector *publicsource.Collector
	path      string
}

const (
	serveHealthPath = "/healthz"
	serveStatusPath = "/status"
)

func newSnapshotServer(collector *publicsource.Collector, path string) *snapshotServer {
	path = strings.TrimSpace(path)
	if path == "" || path == "/" {
		path = "/sub"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return &snapshotServer{collector: collector, path: path}
}

func (s *snapshotServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	switch r.URL.Path {
	case s.path:
		s.serveSubscription(w, r)
	case serveHealthPath:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	case serveStatusPath:
		s.serveStatus(w, r)
	default:
		http.NotFound(w, r)
	}
}

// serveSubscription returns the latest published payload. It answers 503 until a
// first successful refresh has happened, so Resin keeps the nodes it already has
// instead of replacing them with an empty list.
func (s *snapshotServer) serveSubscription(w http.ResponseWriter, r *http.Request) {
	snapshot := s.collector.Status()
	if strings.TrimSpace(snapshot.Content) == "" {
		http.Error(w, "no snapshot published yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Node-Count", strconv.Itoa(snapshot.UniqueNodes))
	if !snapshot.UpdatedAt.IsZero() {
		w.Header().Set("Last-Modified", snapshot.UpdatedAt.UTC().Format(http.TimeFormat))
	}
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(snapshot.Content))
}

// serveStatus reports counters only. It never echoes node data, so it is safe to
// poke from a monitoring script.
func (s *snapshotServer) serveStatus(w http.ResponseWriter, r *http.Request) {
	snapshot := s.collector.Status()
	attempt := s.collector.LastAttempt()

	type sourceStatus struct {
		URL        string `json:"url"`
		Candidates int    `json:"candidates"`
		Accepted   int    `json:"accepted"`
		Cached     bool   `json:"used_cache"`
		Error      string `json:"error,omitempty"`
	}
	sources := make([]sourceStatus, 0, len(attempt.Results))
	for _, result := range attempt.Results {
		sources = append(sources, sourceStatus{
			URL:        result.URL,
			Candidates: result.CandidateCount,
			Accepted:   result.AcceptedCount,
			Cached:     result.UsedCache,
			Error:      result.Error,
		})
	}

	payload := struct {
		Nodes         int            `json:"nodes"`
		Bytes         int            `json:"bytes"`
		SourceCount   int            `json:"source_count"`
		Candidates    int            `json:"candidates"`
		Degraded      bool           `json:"degraded"`
		PublishedAt   string         `json:"published_at,omitempty"`
		LastAttemptAt string         `json:"last_attempt_at,omitempty"`
		LastError     string         `json:"last_error,omitempty"`
		Sources       []sourceStatus `json:"sources"`
	}{
		Nodes:       snapshot.UniqueNodes,
		Bytes:       len(snapshot.Content),
		SourceCount: snapshot.SourceCount,
		Candidates:  snapshot.CandidateCount,
		Degraded:    snapshot.Degraded,
		Sources:     sources,
	}
	if !snapshot.UpdatedAt.IsZero() {
		payload.PublishedAt = snapshot.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if !attempt.At.IsZero() {
		payload.LastAttemptAt = attempt.At.UTC().Format(time.RFC3339)
	}
	if attempt.Err != nil {
		payload.LastError = attempt.Err.Error()
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(payload)
}
