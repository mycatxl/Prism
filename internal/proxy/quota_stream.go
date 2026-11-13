package proxy

import (
	"errors"
	"io"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"prism/internal/keys"
	"prism/internal/usage"
)

// DefaultStreamGracePercent keeps live streams within their purchased allowance.
const DefaultStreamGracePercent = 0

// trafficReserver atomically reserves and accounts traffic across every key of
// a tenant. Completion refunds bytes that the I/O did not transfer.
type trafficReserver interface {
	ReserveTraffic(key *keys.KeyState, platformID string, ingress bool, requested int64) (int64, func(int64))
}

// streamQuotaCheckInterval is how many bytes may flow between two allowance
// checks on a live stream, and how many bytes a stream batches before it reports
// them to the key's sink.
//
// It is the granularity of the enforcement: the check itself is an atomic load
// of a counter, but running it per byte would put a branch on the copy path for
// no benefit. A 32 KB step is the same bound the connection counters already
// report at, so a stream that crosses its allowance is cut within one step of
// doing so.
const streamQuotaCheckInterval int64 = 32 * 1024

// errStreamQuotaExceeded ends a copy loop whose stream has run past the key's
// allowance. It is reported to the caller that owns the loop, not to the client:
// by the time it exists the response is already streaming.
var errStreamQuotaExceeded = errors.New("proxy: stream stopped: traffic allowance exceeded")

// streamQuota enforces a key's traffic allowance on a stream that is already
// running.
//
// Counting is not enforcing: the counters know a limit was crossed, but they
// cannot stop a transfer that is mid-flight, and the enforcer's sweep is a
// second away by design. A quota that is only checked when a request starts
// therefore lets every request that started before the limit finish - which, for
// a single large download, is the whole allowance and more. This closes that
// window from inside the copy loop.
//
// A nil *streamQuota allows everything, so a request with no key and a
// deployment with no gate both behave exactly as they did before quotas existed.
type streamQuota struct {
	platformID   string
	gate         QuotaGate
	key          *keys.KeyState
	gracePercent int
	// interval is how many bytes pass between checks. It is a field so a test
	// can drive the crossing without moving megabytes.
	interval int64
	// abort ends the stream. It is what actually stops the transfer: the
	// cancellation of the request context, or the close of both ends of a
	// tunnel.
	abort func()

	// reportIngress and reportEgress hand the stream's bytes to the key's sink
	// as they flow, in whole steps. They are nil where the connection already
	// reports its own bytes: on the tunnel paths the upstream connection is
	// counting and flushing per 32 KB by itself, and reporting here as well
	// would bill the same byte twice.
	reportIngress func(int64)
	reportEgress  func(int64)

	// bytes is how much has flowed through this stream, in both directions.
	// The allowance is a total, so both directions count towards the same
	// number.
	bytes atomic.Int64
	// next is the byte count at which the next check runs.
	next atomic.Int64
	// pendingIngress and pendingEgress are the bytes not yet handed to the sink.
	pendingIngress atomic.Int64
	pendingEgress  atomic.Int64
	// limited records that the allowance was found exhausted, so the copy loops
	// can tell a cut from a network error.
	limited atomic.Bool
	once    sync.Once
}

// newStreamQuota returns the enforcer of one stream, or nil when there is
// nothing to enforce.
func newStreamQuota(gate QuotaGate, key *keys.KeyState, gracePercent int, abort func()) *streamQuota {
	if gate == nil || key == nil {
		return nil
	}
	if gracePercent < 0 {
		gracePercent = 0
	}
	return &streamQuota{
		gate:         gate,
		key:          key,
		gracePercent: gracePercent,
		interval:     streamQuotaCheckInterval,
		abort:        abort,
	}
}

// withLiveReports returns the quota with the functions that carry its bytes to
// the key's sink as they flow.
//
// The HTTP paths need them because their bytes are copied somewhere the request
// handler does not own: the transport reads a request body from its write loop
// and httputil copies the bodies of a reverse-proxied request and response. The
// counters a live quota reads would otherwise stay still for the whole transfer.
func (q *streamQuota) withLiveReports(ingress, egress func(int64)) *streamQuota {
	if q == nil {
		return nil
	}
	q.reportIngress = ingress
	q.reportEgress = egress
	return q
}

// account records n bytes and checks the allowance once enough have moved.
func (q *streamQuota) account(n int64) {
	if q == nil || n <= 0 {
		return
	}
	total := addStreamBytes(&q.bytes, n)
	for {
		next := q.next.Load()
		if total < next {
			return
		}
		// Reserve the next check window with CAS. A Store could move the
		// schedule backwards when ingress and egress cross the boundary at the
		// same instant, delaying the next check by an entire batch.
		nextCheck := total + q.interval
		if total > math.MaxInt64-q.interval {
			nextCheck = math.MaxInt64
		}
		if q.next.CompareAndSwap(next, nextCheck) {
			q.check()
			return
		}
	}
}

func addStreamBytes(value *atomic.Int64, delta int64) int64 {
	for {
		old := value.Load()
		if old == math.MaxInt64 || delta > math.MaxInt64-old {
			if old == math.MaxInt64 || value.CompareAndSwap(old, math.MaxInt64) {
				return math.MaxInt64
			}
			continue
		}
		if value.CompareAndSwap(old, old+delta) {
			return old + delta
		}
	}
}

// ingressBytes records n bytes moving towards the client.
func (q *streamQuota) ingressBytes(n int64) {
	if q == nil || n <= 0 {
		return
	}
	if q.reportIngress != nil && q.pendingIngress.Add(n) >= q.interval {
		q.reportIngress(q.pendingIngress.Swap(0))
	}
	q.account(n)
}

// egressBytes records n bytes moving towards the upstream.
func (q *streamQuota) egressBytes(n int64) {
	if q == nil || n <= 0 {
		return
	}
	if q.reportEgress != nil && q.pendingEgress.Add(n) >= q.interval {
		q.reportEgress(q.pendingEgress.Swap(0))
	}
	q.account(n)
}

// flushReports hands the bytes of a partial step to the sink.
//
// A stream that ends between two steps would otherwise leave its tail
// unreported: the step boundary is what triggers a report, and the end of the
// stream is not one. It is idempotent, so a caller may flush on every exit path.
func (q *streamQuota) flushReports() {
	if q == nil {
		return
	}
	if q.reportIngress != nil {
		if n := q.pendingIngress.Swap(0); n > 0 {
			q.reportIngress(n)
		}
	}
	if q.reportEgress != nil {
		if n := q.pendingEgress.Swap(0); n > 0 {
			q.reportEgress(n)
		}
	}
}

// check asks the gate whether the allowance is used up and ends the stream when
// it is.
func (q *streamQuota) check() {
	if q == nil || q.limited.Load() {
		return
	}
	if !q.gate.Exceeded(q.key, q.gracePercent) {
		return
	}
	q.limited.Store(true)
	q.once.Do(func() {
		if q.abort != nil {
			q.abort()
		}
	})
}

// Exceeded reports whether this stream was cut for its allowance, which is what
// lets the caller distinguish a quota cut from a network failure.
func (q *streamQuota) Exceeded() bool {
	return q != nil && q.limited.Load()
}

// quotaReader counts what is read through it in one direction of a stream and
// stops the read once the allowance is gone.
//
// A Read that returns n>0 together with an error is a legal answer, and the copy
// loops handle it: the bytes that did move are accounted for and the loop ends.
type quotaReader struct {
	reader io.Reader
	quota  *streamQuota
	// ingress says which half of the stream the bytes belong to: true for the
	// bytes flowing to the client, false for the bytes flowing to the upstream.
	ingress bool
}

func (r *quotaReader) Read(p []byte) (int, error) {
	if r.quota.Exceeded() || (r.quota.atomicAccounting() && r.quota.gate.Exceeded(r.quota.key, 0)) {
		if r.quota != nil {
			r.quota.limited.Store(true)
		}
		return 0, errStreamQuotaExceeded
	}
	n, err := r.reader.Read(p)
	if r.quota.atomicAccounting() {
		allowed, finish := r.quota.reserve(int64(n), r.ingress)
		finish(allowed)
		if allowed < int64(n) || (n > 0 && r.quota.gate.Exceeded(r.quota.key, 0)) {
			// Return the permitted prefix before the copy loop closes its peers.
			r.quota.limited.Store(true)
			return int(allowed), errStreamQuotaExceeded
		}
		return n, err
	}
	if r.ingress {
		r.quota.ingressBytes(int64(n))
	} else {
		r.quota.egressBytes(int64(n))
	}
	if err == nil && r.quota.Exceeded() {
		return n, errStreamQuotaExceeded
	}
	return n, err
}

func (q *streamQuota) atomicAccounting() bool {
	if q == nil {
		return false
	}
	_, ok := q.gate.(trafficReserver)
	return ok
}

func (q *streamQuota) reserve(n int64, ingress bool) (int64, func(int64)) {
	if n <= 0 {
		return 0, func(int64) {}
	}
	return q.gate.(trafficReserver).ReserveTraffic(q.key, q.platformID, ingress, n)
}

// reserveHeader requires the entire header budget before committing a response
// or contacting upstream. The caller settles it when transmission is known.
func (q *streamQuota) reserveHeader(n int64, ingress bool) (func(int64), bool) {
	if !q.atomicAccounting() {
		return func(int64) {}, true
	}
	allowed, finish := q.reserve(n, ingress)
	if allowed < n {
		finish(0)
		q.limited.Store(true)
		return func(int64) {}, false
	}
	return finish, true
}

func (q *streamQuota) write(p []byte, ingress bool, write func([]byte) (int, error)) (int, error) {
	if q.Exceeded() {
		return 0, errStreamQuotaExceeded
	}
	if q.atomicAccounting() {
		allowed, finish := q.reserve(int64(len(p)), ingress)
		defer finish(usage.UnknownTrafficBytes) // panic leaves an unknown outcome
		if allowed == 0 && len(p) > 0 {
			finish(0) // Write was never called
			q.limited.Store(true)
			return 0, errStreamQuotaExceeded
		}
		n, err := write(p[:int(allowed)])
		finish(int64(n))
		if allowed < int64(len(p)) || (n > 0 && q.gate.Exceeded(q.key, 0)) {
			q.limited.Store(true)
			return n, errStreamQuotaExceeded
		}
		return n, err
	}
	n, err := write(p)
	if ingress {
		q.ingressBytes(int64(n))
	} else {
		q.egressBytes(int64(n))
	}
	if err == nil && q.Exceeded() {
		return n, errStreamQuotaExceeded
	}
	return n, err
}

// quotaWriter settles reservations using the bytes the destination accepted.
// It deliberately hides ReaderFrom so io.Copy cannot bypass quota enforcement.
type quotaWriter struct {
	writer      io.Writer
	quota       *streamQuota
	ingress     bool
	transferred *atomic.Int64
}

func (w *quotaWriter) Write(p []byte) (int, error) {
	n, err := w.quota.write(p, w.ingress, w.writer.Write)
	if w.transferred != nil {
		w.transferred.Add(int64(n))
	}
	return n, err
}

// quotaReadCloser is the request-body form of quotaReader: it is what
// net/http's transport reads a body from, so it forwards Close and flushes the
// last partial step when the transport stops reading.
type quotaReadCloser struct {
	quotaReader
	closer io.Closer
}

func newQuotaReadCloser(rc io.ReadCloser, quota *streamQuota) *quotaReadCloser {
	return newQuotaReadCloserDir(rc, quota, false)
}

// newQuotaReadCloserDir is newQuotaReadCloser with the direction made explicit.
//
// A response body is the ingress half of a stream, so a caller that counts bytes
// on the body it copies to the client (a reverse-proxied response) rather than on
// the writer must be able to say so: both halves are charged to the same
// allowance, but they are billed to different sides of the key's traffic.
func newQuotaReadCloserDir(rc io.ReadCloser, quota *streamQuota, ingress bool) *quotaReadCloser {
	return &quotaReadCloser{
		quotaReader: quotaReader{reader: rc, quota: quota, ingress: ingress},
		closer:      rc,
	}
}

func (c *quotaReadCloser) Close() error {
	// The close is the last moment this reader sees: the transport closes a
	// request body when it has finished with it, and the last partial step
	// would otherwise never reach the sink.
	defer c.quota.flushReports()
	return c.closer.Close()
}

// quotaResponseWriter is the writer a handler gives to the copy that feeds the
// client, so the bytes of a response are checked as they are written.
//
// The error its Write returns is what stops the copy: httputil's byte loop has
// no cancellation check of its own, so a write that refuses to continue is the
// only way to end the transfer before the body is exhausted. The bytes that did
// go out are returned as written, so the caller's own count stays truthful.
type quotaResponseWriter struct {
	http.ResponseWriter
	quota *streamQuota
	total int64
}

func (w *quotaResponseWriter) Write(p []byte) (int, error) {
	n, err := w.quota.write(p, true, w.ResponseWriter.Write)
	w.total += int64(n)
	return n, err
}

// Unwrap keeps the wrapper transparent for http.NewResponseController, which is
// how httputil.ReverseProxy flushes and how it hijacks the connection for a 101
// upgrade. A wrapper that hid those would turn a websocket into a copy error.
func (w *quotaResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// quotaReadWriteCloser is the upgrade-stream form of the quota wrappers.
//
// A 101 response is copied by net/http/httputil's own switchProtocolCopier: one
// goroutine reads the backend (ingress, towards the client) and another writes
// it (egress, towards the upstream). Both directions have to be charged, and
// both have to be able to stop the copy, which is what this wrapper does around
// the backend stream. Without it an upgraded connection is the one transfer
// that escapes the allowance: its bytes were only counted, and only once the
// stream had ended, so a key that was already over its limit could keep a
// websocket running for as long as the peers wanted.
//
// The error a Read or Write returns is what ends a copier's loop: httputil
// returns from handleUpgradeResponse on the first non-nil value it receives and
// then closes both connections (see switchProtocolCopier). The abort of the
// stream quota is the other half of the same cut: it cancels the request
// context, which is what closes the backend through the goroutine httputil
// starts for exactly that.
type quotaReadWriteCloser struct {
	rwc          io.ReadWriteCloser
	quota        *streamQuota
	ingressBytes atomic.Int64
	mu           sync.Mutex
	peer         io.Closer
	timer        *time.Timer
	closed       bool
}

func newQuotaReadWriteCloser(rwc io.ReadWriteCloser, quota *streamQuota) *quotaReadWriteCloser {
	return &quotaReadWriteCloser{rwc: rwc, quota: quota}
}

func (c *quotaReadWriteCloser) Read(p []byte) (int, error) {
	return (&quotaReader{reader: c.rwc, quota: c.quota, ingress: true}).Read(p)
}

func (c *quotaReadWriteCloser) Write(p []byte) (int, error) {
	return c.quota.write(p, false, c.rwc.Write)
}

// WriteTo lets httputil's ingress copy settle at the client writer, where a
// short write is observable. Charging on the backend Read would bill read-ahead.
func (c *quotaReadWriteCloser) WriteTo(dst io.Writer) (int64, error) {
	c.mu.Lock()
	peer, _ := dst.(io.Closer)
	if c.closed {
		c.mu.Unlock()
		if peer != nil {
			_ = peer.Close()
		}
		return 0, io.ErrClosedPipe
	}
	c.peer = peer
	c.mu.Unlock()
	n, err := io.Copy(&quotaWriter{writer: dst, quota: c.quota, ingress: true, transferred: &c.ingressBytes}, c.rwc)
	c.startHalfCloseTimer()
	return n, err
}

func (c *quotaReadWriteCloser) startHalfCloseTimer() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed && c.timer == nil {
		c.timer = time.AfterFunc(tunnelHalfCloseTimeout, func() { _ = c.Close() })
	}
}

func (c *quotaReadWriteCloser) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	if c.timer != nil {
		c.timer.Stop()
	}
	peer := c.peer
	c.mu.Unlock()
	if peer != nil {
		_ = peer.Close()
	}
	defer c.quota.flushReports()
	return c.rwc.Close()
}

// CloseWrite and CloseRead keep the optional half-close pair transparent. The
// upgrade copier probes CloseWrite and reads a missing capability as a copy
// that is over, which tears both directions down while the other one is still
// running (see countingReadWriteCloser.CloseWrite), so a wrapper on the backend
// of an upgrade must forward it exactly as the counting wrapper does.
func (c *quotaReadWriteCloser) CloseWrite() error {
	if c == nil {
		return nil
	}
	c.startHalfCloseTimer()
	return closeWriteIfSupported(c.rwc)
}

func (c *quotaReadWriteCloser) CloseRead() error {
	if c == nil {
		return nil
	}
	return closeReadIfSupported(c.rwc)
}

// normalizeQuotaGracePercent resolves the configured tolerance. Zero means "use
// the default"; a negative value means no grace at all, which is the way to ask
// for a stream to be cut exactly at the limit.
func normalizeQuotaGracePercent(configured int) int {
	switch {
	case configured < 0:
		return 0
	case configured == 0:
		return DefaultStreamGracePercent
	default:
		return configured
	}
}

// quotaTransport reserves request headers before the upstream sees them.
type quotaTransport struct {
	base      http.RoundTripper
	quota     *streamQuota
	committed func() bool
}

func (t quotaTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	n := headerWireLen(r.Header)
	finish, ok := t.quota.reserveHeader(n, false)
	if !ok {
		return nil, errStreamQuotaExceeded
	}
	defer finish(usage.UnknownTrafficBytes)
	resp, err := t.base.RoundTrip(r)
	if t.committed() {
		finish(n)
	} else {
		finish(0)
	}
	return resp, err
}
