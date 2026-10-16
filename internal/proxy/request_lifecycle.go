package proxy

import (
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"prism/internal/routing"
)

// requestLifecycle captures mutable per-request telemetry and emits both
// metrics and request-log events on completion.
type requestLifecycle struct {
	startedAt           time.Time
	events              EventEmitter
	finished            RequestFinishedEvent
	log                 RequestLogEntry
	firstByteDurationNs atomic.Int64

	// meter attributes this request's traffic to an access key, when one was
	// presented. It is nil on the transitional token path.
	meter *requestMeter

	reqBodyCapture  *payloadCaptureReadCloser
	respBodyCapture *payloadCaptureReadCloser
}

func newRequestLifecycleFromMetadata(
	events EventEmitter,
	clientRemoteAddr string,
	method string,
	proxyType ProxyType,
	isConnect bool,
) *requestLifecycle {
	clientIP := ""
	if host, _, err := net.SplitHostPort(clientRemoteAddr); err == nil {
		clientIP = host
	} else {
		clientIP = clientRemoteAddr // fallback: bare IP or unparseable
	}
	now := time.Now()
	return &requestLifecycle{
		startedAt: now,
		events:    events,
		finished: RequestFinishedEvent{
			ProxyType: proxyType,
			IsConnect: isConnect,
		},
		log: RequestLogEntry{
			StartedAtNs: now.UnixNano(),
			ProxyType:   proxyType,
			ClientIP:    clientIP,
			HTTPMethod:  method,
		},
	}
}

func newRequestLifecycle(
	events EventEmitter,
	r *http.Request,
	proxyType ProxyType,
	isConnect bool,
) *requestLifecycle {
	method := ""
	clientRemoteAddr := ""
	if r != nil {
		method = r.Method
		clientRemoteAddr = r.RemoteAddr
	}
	return newRequestLifecycleFromMetadata(events, clientRemoteAddr, method, proxyType, isConnect)
}

func (l *requestLifecycle) finish() {
	if l.reqBodyCapture != nil {
		l.log.ReqBody = l.reqBodyCapture.Payload()
		l.log.ReqBodyLen = l.reqBodyCapture.TotalLen()
		l.log.ReqBodyTruncated = l.reqBodyCapture.Truncated()
	}
	if l.respBodyCapture != nil {
		l.log.RespBody = l.respBodyCapture.Payload()
		l.log.RespBodyLen = l.respBodyCapture.TotalLen()
		l.log.RespBodyTruncated = l.respBodyCapture.Truncated()
	}

	durationNs := time.Since(l.startedAt).Nanoseconds()
	l.finished.DurationNs = durationNs
	l.log.DurationNs = durationNs
	l.log.FirstByteDurationNs = l.firstByteDurationNs.Load()
	// The request count is reported after the log fields are final: the failed
	// flag reads NetOK, which the paths set last.
	l.reportFinish()
	l.events.EmitRequestFinished(l.finished)
	l.events.EmitRequestLog(l.log)
}

func (l *requestLifecycle) markFirstByteReceived() {
	if l == nil || l.startedAt.IsZero() {
		return
	}
	durationNs := time.Since(l.startedAt).Nanoseconds()
	if durationNs <= 0 {
		durationNs = 1
	}
	// 首字耗时只记录第一次观测到上游响应字节的时间，避免重试或多回调覆盖。
	l.firstByteDurationNs.CompareAndSwap(0, durationNs)
}

func (l *requestLifecycle) setHTTPStatus(code int) {
	l.log.HTTPStatus = code
}

func (l *requestLifecycle) setProxyError(pe *ProxyError) {
	if pe == nil {
		return
	}
	l.log.PrismError = pe.PrismError
}

func (l *requestLifecycle) setUpstreamError(stage string, err error) {
	if l.log.UpstreamStage == "" && stage != "" {
		l.log.UpstreamStage = stage
	}
	if err == nil || l.log.UpstreamErrMsg != "" {
		return
	}
	detail := summarizeUpstreamError(err)
	l.log.UpstreamErrKind = detail.Kind
	l.log.UpstreamErrno = detail.Errno
	l.log.UpstreamErrMsg = detail.Message
}

func (l *requestLifecycle) addIngressBytes(n int64) {
	if n > 0 {
		l.log.IngressBytes += n
		l.reportIngress(n)
	}
}

func (l *requestLifecycle) addEgressBytes(n int64) {
	if n > 0 {
		l.log.EgressBytes += n
		l.reportEgress(n)
	}
}

// addIngressBytesLive records bytes that the stream itself reported to the
// key's sink as they flowed. Only the request log is updated here: reporting
// them to the sink again would bill the same byte twice.
func (l *requestLifecycle) addIngressBytesLive(n int64) {
	if l == nil || n <= 0 {
		return
	}
	l.log.IngressBytes += n
}

// addEgressBytesLive is the egress counterpart of addIngressBytesLive.
func (l *requestLifecycle) addEgressBytesLive(n int64) {
	if l == nil || n <= 0 {
		return
	}
	l.log.EgressBytes += n
}

// liveReporter returns the functions a stream calls with the bytes it moves, or
// two nils when there is nothing to report to.
//
// It exists for the streams the request lifecycle does not copy itself: a
// request body is read by net/http's transport write loop, a reverse proxy's
// response is copied inside httputil, and a tunnel is relayed by its own
// goroutines. Committing those bytes only at the end of the request would leave
// the counters a live quota reads frozen for the whole transfer, so a limit
// crossed halfway through a large upload or download would be noticed only once
// the transfer was over - the exact window the mid-stream check exists to
// close. The stream reports every few kilobytes instead, and the request log
// still gets its exact total at the end through addIngressBytesLive and
// addEgressBytesLive.
//
// The identity is captured here, once, rather than read from the lifecycle on
// every call: the reporting happens on the goroutine that moves the bytes, and
// the lifecycle is otherwise owned by the handler's own goroutine.
func (l *requestLifecycle) liveReporter() (ingress, egress func(int64)) {
	if l == nil || l.meter == nil || l.meter.bytesAtConn || l.meter.sink == nil {
		return nil, nil
	}
	sink := l.meter.sink
	keyID := l.meter.keyID
	userID := l.meter.userID
	platformID := l.platformForMetering()
	return func(n int64) {
			sink.OnKeyTrafficDelta(keyID, userID, platformID, n, 0)
		}, func(n int64) {
			sink.OnKeyTrafficDelta(keyID, userID, platformID, 0, n)
		}
}

func (l *requestLifecycle) setNetOK(ok bool) {
	l.finished.NetOK = ok
	l.log.NetOK = ok
}

func (l *requestLifecycle) setAccount(account string) {
	l.log.Account = account
}

func (l *requestLifecycle) setTarget(host, rawURL string) {
	l.log.TargetHost = host
	l.log.TargetURL = sanitizeLogURL(rawURL)
}

func (l *requestLifecycle) setReqHeadersCaptured(reqHeaders []byte, totalLen int, truncated bool) {
	l.log.ReqHeaders = reqHeaders
	l.log.ReqHeadersLen = totalLen
	l.log.ReqHeadersTruncated = truncated
}

func (l *requestLifecycle) setReqBodyCapture(c *payloadCaptureReadCloser) {
	l.reqBodyCapture = c
}

func (l *requestLifecycle) setRespHeadersCaptured(respHeaders []byte, totalLen int, truncated bool) {
	l.log.RespHeaders = respHeaders
	l.log.RespHeadersLen = totalLen
	l.log.RespHeadersTruncated = truncated
}

func (l *requestLifecycle) setRespBodyCapture(c *payloadCaptureReadCloser) {
	l.respBodyCapture = c
}

func (l *requestLifecycle) setRouteResult(result routing.RouteResult) {
	l.finished.PlatformID = result.PlatformID
	l.log.PlatformID = result.PlatformID
	l.log.PlatformName = result.PlatformName
	l.log.NodeHash = result.NodeHash.Hex()
	l.log.NodeTag = result.NodeTag
	l.log.EgressIP = result.EgressIP.String()
	if len(result.Events) > 0 {
		l.log.Events = append(l.log.Events, result.Events...)
	}
}
