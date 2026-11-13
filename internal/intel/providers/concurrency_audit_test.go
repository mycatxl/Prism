package providers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type auditCanceledDownloader struct {
	entered chan struct{}
	once    sync.Once
}

func (d *auditCanceledDownloader) Download(ctx context.Context, _ string) ([]byte, error) {
	d.once.Do(func() { close(d.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestGeoManagerStopCancelsAndJoinsManualRefresh(t *testing.T) {
	download := &auditCanceledDownloader{entered: make(chan struct{})}
	registry := geoRegistry(t, nil)
	m := NewGeoManager(GeoManagerOptions{Dir: t.TempDir(), Downloader: download, Setting: registry.Setting})
	defer m.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = m.RequestRefresh(ctx, "dbip_lite") }()
	select {
	case <-download.entered:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	stopped := make(chan struct{})
	go func() { m.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("Stop did not cancel manual download")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("manual refresh outlived Stop")
	}
	if _, err := m.RequestRefresh(context.Background(), "dbip_lite"); !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh after stop=%v", err)
	}
}
