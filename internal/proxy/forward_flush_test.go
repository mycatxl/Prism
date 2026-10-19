package proxy

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

// The forward proxy copied the upstream body with one io.Copy and never flushed.
// net/http buffers a response (4 KB of headers plus the chunk writer's own
// buffer) until it fills or the handler returns, so a server-sent-events stream,
// a chunked heartbeat or a slow log tail reached the client as one lump at the
// end - if the client had not already timed out waiting. This test drives a real
// server and a real client, because that is where the buffering lives: an
// httptest.ResponseRecorder answers immediately and would pass even without a
// flush.
func TestForwardProxy_ResponseBodyIsFlushedPerChunk(t *testing.T) {
	firstChunk := []byte("event: hello\ndata: 1\n\n")
	release := make(chan struct{})
	upstreamDone := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(firstChunk)
		// The first event is on the wire from upstream's point of view.
		w.(http.Flusher).Flush()
		// Nothing else is produced until the client has been seen to read the
		// first event. Without a flush in the proxy this blocks forever.
		select {
		case <-release:
		case <-time.After(5 * time.Second):
			t.Error("client never read the first chunk; the proxy buffered the stream")
			return
		}
		_, _ = w.Write([]byte("event: bye\ndata: 2\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()

	env := newProxyE2EEnv(t)
	setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return net.Dial("tcp", upstream.Listener.Addr().String())
	})
	key := workingKeyState()
	fp, _, _, _ := newQuotaForwardProxy(t, env, &mutableKeyStore{state: key})
	proxySrv := httptest.NewServer(fp)
	defer proxySrv.Close()

	proxyURL, err := url.Parse(proxySrv.URL)
	if err != nil {
		t.Fatalf("parse proxy url: %v", err)
	}
	proxyURL.User = url.UserPassword("plat", testCredential)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	t.Cleanup(client.CloseIdleConnections)

	resp, err := client.Get(upstream.URL + "/events")
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// Reading the stream line by line in the background is what turns
	// "eventually" into "while the handler is still running": the proxy handler
	// cannot return until the upstream does, and the upstream cannot produce
	// its second event until this read has completed.
	lines := make(chan string, 8)
	payload := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(resp.Body)
		var all strings.Builder
		for {
			line, readErr := reader.ReadString('\n')
			all.WriteString(line)
			if readErr != nil {
				payload <- all.String()
				return
			}
			lines <- line
		}
	}()

	select {
	case line := <-lines:
		if line != "event: hello\n" {
			close(release)
			t.Fatalf("first line = %q, want %q", line, "event: hello\n")
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("the first chunk did not reach the client while the upstream was still writing")
	}
	// Let upstream finish so the handler can return and the stream can drain.
	close(release)
	select {
	case <-upstreamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream handler did not finish")
	}
	select {
	case body := <-payload:
		if want := "event: hello\ndata: 1\n\nevent: bye\ndata: 2\n\n"; body != want {
			t.Fatalf("body = %q, want %q", body, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the response body never ended")
	}
}

// The same guard for a body the response writer cannot flush through: a writer
// that reports ErrNotSupported must stop being asked, otherwise every chunk of
// every HTTP/2 or recorder response would pay for a flush attempt that can never
// succeed. This is a unit check of the writer rather than an end-to-end one, so
// the counting is explicit.
func TestResponseFlusher_StopsAskingAnUnflushableWriter(t *testing.T) {
	writer := &countingUnflushableWriter{}
	flusher := newResponseFlusher(writer, unflushableResponseWriter{})
	n, err := flusher.Write([]byte("chunk"))
	if err != nil || n != len("chunk") {
		t.Fatalf("write = %d, %v", n, err)
	}
	if flusher.controller != nil {
		t.Fatal("a writer that cannot flush kept its controller; every later chunk would probe again")
	}
	if _, err := flusher.Write([]byte("chunk")); err != nil {
		t.Fatalf("second write = %v", err)
	}
	if writer.writes != 2 {
		t.Fatalf("writes = %d, want 2", writer.writes)
	}
}

type countingUnflushableWriter struct{ writes int }

func (w *countingUnflushableWriter) Write(p []byte) (int, error) {
	w.writes++
	return len(p), nil
}

// unflushableResponseWriter is a bare http.ResponseWriter: no Flusher, no
// Unwrap, which is what http.NewResponseController reports ErrNotSupported for.
type unflushableResponseWriter struct{ header http.Header }

func (w unflushableResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w unflushableResponseWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w unflushableResponseWriter) WriteHeader(int) {}
