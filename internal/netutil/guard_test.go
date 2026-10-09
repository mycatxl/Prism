package netutil

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDenyPrivateRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	d := NewDirectDownloader(func() time.Duration { return time.Second }, func() string { return "" }).DenyPrivate()
	_, err := d.Download(context.Background(), srv.URL)
	if !errors.Is(err, ErrForbiddenResourceAddr) {
		t.Fatalf("want ErrForbiddenResourceAddr, got %v", err)
	}
}
