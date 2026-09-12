package netutil

import (
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"prism/internal/node"
	"prism/internal/testutil"
)

func TestResourceLimitsApplyToChunkedAndDecompressedResponses(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		name := "chunked"
		if compressed {
			name = "gzip"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if compressed {
					w.Header().Set("Content-Encoding", "gzip")
					writer := gzip.NewWriter(w)
					_, _ = writer.Write([]byte(strings.Repeat("x", 4096)))
					_ = writer.Close()
				} else {
					w.(http.Flusher).Flush()
					_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
				}
			}))
			defer server.Close()
			downloader := NewDirectDownloader(func() time.Duration { return time.Second }, func() string { return "Prism-test" })
			downloader.MaxBodyBytes = 512
			proxyAttempts := 0
			retry := &RetryDownloader{
				Direct:     downloader,
				NodePicker: func(string) (node.Hash, error) { proxyAttempts++; return node.Zero, errors.New("must not retry") },
				ProxyFetch: func(context.Context, node.Hash, string) ([]byte, error) { return nil, errors.New("must not retry") },
			}
			data, err := retry.Download(context.Background(), server.URL)
			var tooLarge *NonRetryableError
			if !errors.As(err, &tooLarge) || data != nil {
				t.Fatalf("expected bounded response error, got %v", err)
			}
			if proxyAttempts != 0 {
				t.Fatal("oversized downloads must not be repeated through proxies")
			}
		})
	}
}

func TestOutboundResourceResponseIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(strings.Repeat("x", 2048)))
	}))
	defer server.Close()
	outbound, err := (&testutil.StubOutboundBuilder{}).Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := HTTPGetViaOutbound(context.Background(), outbound, server.URL, OutboundHTTPOptions{MaxBodyBytes: 128})
	var tooLarge *NonRetryableError
	if !errors.As(err, &tooLarge) || data != nil {
		t.Fatalf("expected bounded response error, got %v", err)
	}
}

func TestResourceErrorsDoNotExposeSubscriptionCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	privateURL := strings.Replace(server.URL, "http://", "http://private-user:private-password@", 1) + "/private-path?token=private-query#private-fragment"
	downloader := NewDirectDownloader(func() time.Duration { return time.Second }, func() string { return "" })
	_, statusErr := downloader.Download(context.Background(), privateURL)
	server.Close()
	_, networkErr := downloader.Download(context.Background(), privateURL)
	for _, err := range []error{statusErr, networkErr} {
		if err == nil {
			t.Fatal("expected a request failure")
		}
		for _, secret := range []string{"private-user", "private-password", "private-path", "private-query", "private-fragment"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatal("request failure exposed a credential-bearing URL")
			}
		}
	}
}

func TestResourceRedirectDoesNotForwardOriginCredentials(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Referer") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credentials or a private source URL reached another origin")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("source authentication was lost")
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	downloader := NewDirectDownloader(func() time.Duration { return time.Second }, func() string { return "" })
	privateURL := strings.Replace(source.URL, "http://", "http://user:secret@", 1) + "/feed?token=secret-query"
	data, err := downloader.Download(context.Background(), privateURL)
	if err != nil || string(data) != "ok" {
		t.Fatalf("redirect failed: %v", err)
	}
}
