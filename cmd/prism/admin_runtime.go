package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
)

// adminListener is the optional independent management listener configured
// through PRISM_ADMIN_LISTEN=host:port. It serves only the management surface
// — /ui, /api, /healthz and the public /sub subscription path — and never
// exposes a proxy protocol, so the management plane can live on an internal
// address while the primary listener keeps serving traffic.
type adminListener struct {
	addr     string
	listener net.Listener
	server   *http.Server
	errCh    chan error
}

// startAdminListener binds addr and returns nil when addr is empty, which is
// the default: the management routes then stay on the primary listener only.
func startAdminListener(addr string, apiHandler http.Handler) (*adminListener, error) {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return nil, nil
	}

	listener, err := net.Listen("tcp", trimmed)
	if err != nil {
		return nil, fmt.Errorf("admin listener %s: %w", trimmed, err)
	}

	l := &adminListener{
		addr:     trimmed,
		listener: listener,
		server:   &http.Server{Handler: newAdminOnlyHandler(apiHandler)},
		errCh:    make(chan error, 1),
	}
	log.Printf("Prism admin listener starting on %s (management plane only: /ui, /api, /healthz)", listener.Addr().String())
	go func() {
		if err := l.server.Serve(listener); err != nil &&
			!errors.Is(err, http.ErrServerClosed) &&
			!errors.Is(err, net.ErrClosed) {
			l.errCh <- fmt.Errorf("admin listener: %w", err)
		}
	}()
	return l, nil
}

// Errors reports fatal serve errors of the management listener.
func (l *adminListener) Errors() <-chan error {
	if l == nil {
		return nil
	}
	return l.errCh
}

func (l *adminListener) Shutdown(ctx context.Context) error {
	if l == nil {
		return nil
	}
	return l.server.Shutdown(ctx)
}

// newAdminOnlyHandler restricts the management listener to the management
// paths. Everything else (forward proxy, CONNECT, reverse proxy, SOCKS5 and
// token-action) stays on the primary listener.
func newAdminOnlyHandler(apiHandler http.Handler) http.Handler {
	if apiHandler == nil {
		apiHandler = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isManagementPath(r) {
			http.NotFound(w, r)
			return
		}
		apiHandler.ServeHTTP(w, r)
	})
}

func isManagementPath(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	if r.Method == http.MethodConnect {
		return false
	}
	switch path := r.URL.Path; {
	case path == "/":
		return true
	case path == "/healthz":
		return true
	case path == "/api" || strings.HasPrefix(path, "/api/"):
		return true
	case path == "/ui" || strings.HasPrefix(path, "/ui/"):
		return true
	// The public subscription endpoint is part of the management surface: it is
	// served without the admin token by the same handler, so a URL minted while
	// the operator was talking to this listener resolves here too.
	case path == "/sub" || strings.HasPrefix(path, "/sub/"):
		return true
	default:
		return false
	}
}

// mergeErrorChannels forwards the first error of every channel into one result
// channel; nil channels (disabled listeners) are ignored.
func mergeErrorChannels(channels ...<-chan error) <-chan error {
	merged := make(chan error, 1)
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		go func(ch <-chan error) {
			for err := range ch {
				select {
				case merged <- err:
				default:
				}
			}
		}(channel)
	}
	return merged
}
