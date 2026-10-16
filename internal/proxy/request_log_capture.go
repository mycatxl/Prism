package proxy

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

// captureRequestHeaders serializes headers to canonical wire format for
// request-log payload capture. Redact the copy before applying byte limits.
func captureRequestHeaders(header http.Header, accountHeaders ...string) []byte {
	if header == nil {
		return nil
	}
	var buf bytes.Buffer
	_ = headersForRequestLog(header, accountHeaders).Write(&buf)
	return buf.Bytes()
}

// headerWireLen returns canonical wire-format header bytes length.
func headerWireLen(header http.Header) int64 {
	if len(header) == 0 {
		return 0
	}
	var buf bytes.Buffer
	_ = header.Write(&buf)
	return int64(buf.Len())
}

func captureHeadersWithLimit(header http.Header, maxBytes int, accountHeaders ...string) ([]byte, int, bool) {
	payload := captureRequestHeaders(header, accountHeaders...)
	totalLen := len(payload)
	if totalLen == 0 {
		return nil, 0, false
	}
	if maxBytes >= 0 && totalLen > maxBytes {
		return payload[:maxBytes], totalLen, true
	}
	return payload, totalLen, false
}

type payloadCaptureReadCloser struct {
	rc       io.ReadCloser
	maxBytes int

	// mu guards payload and totalLen. The wrapper is the inner half of a body
	// net/http reads from a goroutine the handler does not own: the transport's
	// write loop keeps reading a request body after the round trip returned, and
	// httputil copies a response body while the handler unwinds. The handler
	// reads the capture in requestLifecycle.finish on exactly that path, so an
	// unsynchronised buffer and counter race with the reader - the same reason
	// the sibling wrappers below count with atomics.
	mu       sync.Mutex
	payload  bytes.Buffer
	totalLen int
}

func newPayloadCaptureReadCloser(rc io.ReadCloser, maxBytes int) *payloadCaptureReadCloser {
	return &payloadCaptureReadCloser{
		rc:       rc,
		maxBytes: maxBytes,
	}
}

func (c *payloadCaptureReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		// The counter and the captured bytes move under one lock, so a reader
		// that sees a total never sees a payload that is behind it: the two
		// cannot contradict each other in the request log.
		c.mu.Lock()
		c.totalLen += n
		if c.maxBytes < 0 {
			_, _ = c.payload.Write(p[:n])
		} else {
			remaining := c.maxBytes - c.payload.Len()
			if remaining > 0 {
				if n <= remaining {
					_, _ = c.payload.Write(p[:n])
				} else {
					_, _ = c.payload.Write(p[:remaining])
				}
			}
		}
		c.mu.Unlock()
	}
	return n, err
}

func (c *payloadCaptureReadCloser) Close() error {
	return c.rc.Close()
}

// Payload returns the captured bytes so far, as a copy.
//
// A copy rather than the internal slice: the reader may append to it after this
// returns, and the request log must not hold a buffer that is still being
// written to. The total is a snapshot for the same reason - the body may still
// be moving when the handler finishes.
func (c *payloadCaptureReadCloser) Payload() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.payload.Bytes()...)
}

func (c *payloadCaptureReadCloser) TotalLen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalLen
}

func (c *payloadCaptureReadCloser) Truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalLen > c.payload.Len()
}

// countingReadCloser wraps a body stream and records total read bytes.
//
// The counter is atomic because the transport reads a request body from its own
// write loop, which can still be running when the handler reads the total after
// the round trip returned (a response that arrives before the upload finishes).
// A plain int64 there is a data race, and a torn read would report a byte count
// that never existed.
type countingReadCloser struct {
	rc    io.ReadCloser
	total atomic.Int64
}

func newCountingReadCloser(rc io.ReadCloser) *countingReadCloser {
	return &countingReadCloser{rc: rc}
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		c.total.Add(int64(n))
	}
	return n, err
}

func (c *countingReadCloser) Close() error {
	return c.rc.Close()
}

// Total returns the bytes read so far. It is a snapshot: the reader may still
// be in use by another goroutine, so a caller sees what has moved up to now.
func (c *countingReadCloser) Total() int64 {
	return c.total.Load()
}

// countingReadWriteCloser wraps a bidirectional stream and records
// bytes read/written independently.
//
// The counters are atomic because the two directions of a 101 upgrade are
// copied by separate net/http/httputil goroutines (switchProtocolCopier) while
// the proxy's own goroutine reads the totals after ServeHTTP returns
// (reverse.go:481-484). A plain int64 there is a data race.
type countingReadWriteCloser struct {
	rwc        io.ReadWriteCloser
	totalRead  atomic.Int64
	totalWrite atomic.Int64
}

func newCountingReadWriteCloser(rwc io.ReadWriteCloser) *countingReadWriteCloser {
	return &countingReadWriteCloser{rwc: rwc}
}

func (c *countingReadWriteCloser) Read(p []byte) (int, error) {
	n, err := c.rwc.Read(p)
	if n > 0 {
		c.totalRead.Add(int64(n))
	}
	return n, err
}

func (c *countingReadWriteCloser) Write(p []byte) (int, error) {
	n, err := c.rwc.Write(p)
	if n > 0 {
		c.totalWrite.Add(int64(n))
	}
	return n, err
}

func (c *countingReadWriteCloser) Close() error {
	return c.rwc.Close()
}

// CloseWrite forwards the client half-close to the upstream stream.
//
// net/http/httputil's switchProtocolCopier.copyToBackend asserts
//
//	if wc, ok := c.backend.(interface{ CloseWrite() error }); ok {
//	    errc <- wc.CloseWrite()
//	    return
//	}
//	errc <- errCopyDone
//
// and handleUpgradeResponse returns on the first non-nil value it receives
// ("err := <-errc; if err == nil { err = <-errc }"), after which its deferred
// conn.Close() and backConnCloseCh tear down both connections while the
// opposite direction is still copying. A failed assertion therefore turns a
// clean half-close into a full teardown; an unsupported backend must report
// success here so the copier keeps waiting for the other direction.
//
// This is deliberately different from closeWriteErr, which returns
// errHalfCloseUnsupported because its callers (closeWriteConn in tunnel.go)
// only need to know whether the peer really half-closed. A nil receiver means
// there is no stream to abort, so it also reports success.
func (c *countingReadWriteCloser) CloseWrite() error {
	if c == nil {
		return nil
	}
	return closeWriteIfSupported(c.rwc)
}

// CloseRead mirrors CloseWrite for the read half. httputil never calls it on
// this wrapper (it only probes CloseWrite), but the wrapper must stay
// transparent for every caller that probes the optional half-close pair.
func (c *countingReadWriteCloser) CloseRead() error {
	if c == nil {
		return nil
	}
	return closeReadIfSupported(c.rwc)
}

// closeWriteIfSupported half-closes rwc when it exposes CloseWrite and reports
// success when it does not. Returning nil for the unsupported case is what
// keeps httputil's upgrade copier from treating the missing capability as a
// copy failure (see countingReadWriteCloser.CloseWrite).
func closeWriteIfSupported(rwc io.ReadWriteCloser) error {
	closeWriter, ok := rwc.(interface{ CloseWrite() error })
	if !ok {
		return nil
	}
	return closeWriter.CloseWrite()
}

// closeReadIfSupported is the CloseRead counterpart of closeWriteIfSupported.
func closeReadIfSupported(rwc io.ReadWriteCloser) error {
	closeReader, ok := rwc.(interface{ CloseRead() error })
	if !ok {
		return nil
	}
	return closeReader.CloseRead()
}

// TotalRead returns the bytes read so far. The value is a snapshot: the
// upgrade copy goroutines may still be running, so a caller reading it before
// they finish sees an incomplete count. That is a reporting-precision
// limitation, not a race — the atomic load is safe in any case.
func (c *countingReadWriteCloser) TotalRead() int64 {
	return c.totalRead.Load()
}

// TotalWrite returns the bytes written so far, with the same snapshot
// semantics as TotalRead.
func (c *countingReadWriteCloser) TotalWrite() int64 {
	return c.totalWrite.Load()
}
