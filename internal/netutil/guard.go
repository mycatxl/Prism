package netutil

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"prism/internal/addrpolicy"
)

// ErrForbiddenResourceAddr is returned when a resource download (or one of its
// redirects) resolves to loopback, a private range, link-local, CGNAT or a
// cloud metadata address.
var ErrForbiddenResourceAddr = errors.New("resource address is private or reserved")

// GuardedTransport returns an HTTP transport that ignores HTTP(S)_PROXY from
// the environment and refuses to connect to private or reserved addresses. The
// check runs on the resolved IP right before connect, so it also covers
// redirects and DNS rebinding.
func GuardedTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrForbiddenResourceAddr, address)
			}
			if addrpolicy.AddrIsForbidden(ap.Addr().Unmap()) {
				return fmt.Errorf("%w: %s", ErrForbiddenResourceAddr, ap.Addr())
			}
			return nil
		},
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = dialer.DialContext
	return t
}

// DenyPrivate makes the downloader refuse private and reserved targets.
func (d *DirectDownloader) DenyPrivate() *DirectDownloader {
	d.Client = &http.Client{CheckRedirect: resourceRedirect, Transport: GuardedTransport()}
	return d
}
