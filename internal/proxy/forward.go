package proxy

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"

	"prism/internal/netutil"
	"prism/internal/outbound"
	"prism/internal/routing"
	"prism/internal/safego"
)

// ForwardProxyConfig holds dependencies for the forward proxy.
type ForwardProxyConfig struct {
	ProxyToken string
	// AccessKeys resolves an access key credential presented in the token
	// position. It may be nil or unconfigured: the credential then falls back
	// to the global proxy token, which is the transitional behaviour.
	AccessKeys        AccessKeyVerifier
	Router            *routing.Router
	Pool              outbound.PoolAccessor
	Health            HealthRecorder
	Events            EventEmitter
	MetricsSink       MetricsEventSink
	OutboundTransport OutboundTransportConfig
	TransportPool     *OutboundTransportPool
	ProxyBypassRules  []string
	// DirectDenyPrivate enables the local dial guard (PRISM_DIRECT_DENY_PRIVATE)
	// on this proxy's local direct branch, keeping Resin behaviour when false.
	DirectDenyPrivate bool

	// BlockedDestinationPorts lists the destination ports this proxy's local
	// direct branch refuses (PRISM_BLOCKED_DESTINATION_PORTS). It is independent
	// of DirectDenyPrivate: clearing that switch never reopens a blocked port.
	BlockedDestinationPorts []int
	// AuthGuard optionally rate limits failed proxy authentications. A nil
	// guard keeps the upstream Resin behaviour (PRISM_PROXY_AUTH_FAIL_LIMIT
	// defaults to 0, which disables it).
	AuthGuard AuthFailureGuard
	// Metering attributes this proxy's traffic to the access key that caused
	// it. Nil disables the accounting; the proxy then behaves exactly as it
	// did before metering existed.
	Metering MeteringSink
	// Quota decides whether a request may proceed. Nil allows everything,
	// which is what a deployment without plans gets.
	Quota QuotaGate
	// QuotaGracePercent is the tolerance a live stream gets before it is cut
	// for running past the key's allowance, in percent of the limit. Zero
	// applies DefaultStreamGracePercent; a negative value means no tolerance at
	// all.
	QuotaGracePercent int
	// Sessions holds this proxy's live tunnels per key, so a revoked key's
	// tunnels can be ended. Nil disables that, leaving revocation to take
	// effect on the next request.
	Sessions SessionTracker
	// QuotaLogf reports a request the gate refused. A refusal happens before
	// the request lifecycle exists, so it leaves no trace in the request log;
	// the line names the key id and the reason and nothing else. Nil keeps a
	// refusal silent, which is what a deployment without plans gets.
	QuotaLogf func(format string, args ...any)
}

// ForwardProxy implements an HTTP forward proxy with Proxy-Authorization
// authentication, HTTP request forwarding, and CONNECT tunneling.
type ForwardProxy struct {
	token             string
	accessKeys        AccessKeyVerifier
	router            *routing.Router
	pool              outbound.PoolAccessor
	health            HealthRecorder
	events            EventEmitter
	metricsSink       MetricsEventSink
	transportConfig   OutboundTransportConfig
	transportPool     *OutboundTransportPool
	transportPoolOnce sync.Once
	directTransport   *http.Transport
	directOnce        sync.Once
	bypass            *TargetBypassMatcher
	directGuard       DirectDialGuard
	authGuard         AuthFailureGuard
	metering          MeteringSink
	quota             QuotaGate
	// quotaGracePercent is QuotaGracePercent, resolved once at construction.
	quotaGracePercent int
	quotaLogf         func(format string, args ...any)
	sessions          SessionTracker
}

// NewForwardProxy creates a new forward proxy handler.
func NewForwardProxy(cfg ForwardProxyConfig) *ForwardProxy {
	ev := cfg.Events
	if ev == nil {
		ev = NoOpEventEmitter{}
	}
	transportCfg := normalizeOutboundTransportConfig(cfg.OutboundTransport)
	transportPool := cfg.TransportPool
	if transportPool == nil {
		transportPool = NewOutboundTransportPool(transportCfg)
	}
	return &ForwardProxy{
		token:             cfg.ProxyToken,
		accessKeys:        cfg.AccessKeys,
		router:            cfg.Router,
		pool:              cfg.Pool,
		health:            cfg.Health,
		events:            ev,
		metricsSink:       cfg.MetricsSink,
		transportConfig:   transportCfg,
		transportPool:     transportPool,
		bypass:            NewTargetBypassMatcher(cfg.ProxyBypassRules),
		directGuard:       NewDirectDialGuard(cfg.DirectDenyPrivate).WithBlockedDestinationPorts(cfg.BlockedDestinationPorts),
		authGuard:         cfg.AuthGuard,
		metering:          cfg.Metering,
		quota:             cfg.Quota,
		quotaGracePercent: normalizeQuotaGracePercent(cfg.QuotaGracePercent),
		quotaLogf:         cfg.QuotaLogf,
		sessions:          cfg.Sessions,
	}
}

func (p *ForwardProxy) outboundHTTPTransport(routed routedOutbound) *http.Transport {
	p.transportPoolOnce.Do(func() {
		if p.transportPool == nil {
			p.transportPool = NewOutboundTransportPool(p.transportConfig)
		}
	})
	return p.transportPool.Get(routed.Route.NodeHash, routed.Outbound, p.metricsSink)
}

func (p *ForwardProxy) directHTTPTransport() *http.Transport {
	p.directOnce.Do(func() {
		p.directTransport = newDirectHTTPTransport(p.transportConfig, p.metricsSink, p.directGuard)
	})
	return p.directTransport
}

func (p *ForwardProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleCONNECT(w, r)
	} else {
		p.handleHTTP(w, r)
	}
}

// authenticate parses Proxy-Authorization and returns the identity it names.
func (p *ForwardProxy) authenticate(r *http.Request) (proxyIdentity, *ProxyError) {
	return p.authenticateV1(r)
}

// authenticateV1 accepts either an access key or the transitional global proxy
// token in the credential's token position.
//
// The order matters: the credential is inspected for a key first, and anything
// that is not a key is compared with the global token. A key-shaped credential
// never falls back to that comparison, so a revoked key cannot authenticate by
// accident on a deployment whose token happens to resemble one.
func (p *ForwardProxy) authenticateV1(r *http.Request) (proxyIdentity, *ProxyError) {
	auth := r.Header.Get("Proxy-Authorization")
	credential, ok := parseProxyAuthorizationCredentialV1(auth)
	if !ok {
		if p.token == "" && !requireProxyAuthInfo(r) {
			return proxyIdentity{}, nil
		}
		return proxyIdentity{}, ErrAuthRequired
	}
	if p.token == "" && requireProxyAuthInfo(r) && !hasBasicUserInfo(credential) {
		return proxyIdentity{}, ErrAuthRequired
	}

	identity, state, isKey := splitAccessKeyCredential(p.accessKeys, credential, clientAddr(r))
	if isKey {
		if state == nil {
			return proxyIdentity{}, ErrAuthFailed
		}
		platName, account := parseV1PlatformAccountIdentity(identity)
		return proxyIdentity{PlatformName: platName, Account: account, Key: state}, nil
	}

	if p.token == "" {
		platName, account := parseForwardCredentialV1WhenAuthDisabled(credential)
		return proxyIdentity{PlatformName: platName, Account: account}, nil
	}

	token, platName, account := parseForwardCredentialV1(credential)
	if subtle.ConstantTimeCompare([]byte(token), []byte(p.token)) != 1 {
		return proxyIdentity{}, ErrAuthFailed
	}
	return proxyIdentity{PlatformName: platName, Account: account}, nil
}

func requireProxyAuthInfo(r *http.Request) bool {
	return r != nil && InboundPolicyFromContext(r.Context()).RequireProxyAuthInfo
}

func hasBasicUserInfo(credential string) bool {
	separator := strings.LastIndexByte(credential, ':')
	return separator > 0
}

// parseProxyAuthorizationCredentialV1 decodes Basic credential for V1
// forward-auth flows.
func parseProxyAuthorizationCredentialV1(auth string) (string, bool) {
	if auth == "" {
		return "", false
	}

	// Expect "<scheme> <base64>"; scheme is case-insensitive per RFC.
	authFields := strings.Fields(auth)
	if len(authFields) != 2 || !strings.EqualFold(authFields[0], "Basic") {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(authFields[1])
	if err != nil {
		return "", false
	}
	return string(decoded), true
}

// hop-by-hop headers that must not be forwarded to the next hop.
var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"TE",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// stripHopByHopHeaders removes hop-by-hop headers from a header map,
// including any headers listed in the Connection header.
func stripHopByHopHeaders(header http.Header) {
	if header == nil {
		return
	}
	// Remove custom headers listed in Connection.
	for _, connHeaders := range header.Values("Connection") {
		for _, h := range strings.Split(connHeaders, ",") {
			if h = strings.TrimSpace(h); h != "" {
				header.Del(h)
			}
		}
	}
	for _, h := range hopByHopHeaders {
		header.Del(h)
	}
}

// copyEndToEndHeaders copies only end-to-end headers from src to dst and
// returns the canonical wire-format header length after filtering.
func copyEndToEndHeaders(dst, src http.Header) int64 {
	if dst == nil || src == nil {
		return 0
	}
	headers := src.Clone()
	stripHopByHopHeaders(headers)
	totalLen := headerWireLen(headers)
	for k, vv := range headers {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
	return totalLen
}

// prepareForwardOutboundRequest clones an inbound forward-proxy request into a
// client request suitable for http.Transport.RoundTrip.
func prepareForwardOutboundRequest(in *http.Request) *http.Request {
	req := in.Clone(in.Context())
	req.RequestURI = ""
	// Do not propagate client-side close semantics to upstream transport reuse.
	req.Close = false
	stripHopByHopHeaders(req.Header)
	return req
}

func (p *ForwardProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if guardProxyAuthFailure(w, r, p.authGuard) {
		return
	}
	identity, authErr := p.authenticate(r)
	if authErr != nil {
		recordProxyAuthFailure(p.authGuard, r)
		writeProxyError(w, authErr)
		return
	}
	platName, account := identity.PlatformName, identity.Account

	// One context ends the whole request: a quota cut cancels it, and so does
	// the session registry when the key is revoked while the request is in
	// flight.
	streamCtx, cancelStream := context.WithCancel(r.Context())
	defer cancelStream()

	// The allowance is checked before the lifecycle exists, so a refused request
	// never reaches the routing layer and never opens an upstream connection.
	// The check reserves the session it is about to admit: the count, the
	// decision and the registration are one step, so a key at its concurrency
	// limit cannot admit a burst of requests that all read the count before any
	// of them had registered.
	quotaErr, reservation := reserveSession(p.quota, p.sessions, identity.Key, newRequestSession(cancelStream), p.quotaLogf)
	if quotaErr != nil {
		recordProxyAuthFailure(p.authGuard, r)
		writeProxyError(w, quotaErr)
		return
	}
	// An HTTP request is a session of this key exactly as a tunnel is, and it is
	// registered under the same key id: a revocation, a disable or an exhausted
	// allowance must end a request that is already running rather than only
	// refusing the next one. The unregister is deferred, so every exit path from
	// here on releases the count.
	defer reservation.Release()

	lifecycle := newRequestLifecycle(p.events, r, ProxyTypeForward, false)
	lifecycle.setTarget(r.Host, r.URL.String())
	// Bytes are attributed from the lifecycle here: the HTTP transport is
	// pooled per node and its connections outlive a single request, so a
	// connection cannot be said to belong to one key.
	lifecycle.attachMeter(p.metering, identity.Key, false)
	defer lifecycle.finish()
	lifecycle.setAccount(account)

	var route routing.RouteResult
	var hasRoute bool
	var transport *http.Transport
	if p.bypass != nil && p.bypass.ShouldBypass(r.Host) {
		// PRISM_DIRECT_DENY_PRIVATE applies to this local dial path exactly as
		// it does to the reverse-proxy bypass branch and to CONNECT/SOCKS5.
		if err := p.directGuard.CheckTarget(r.Context(), r.Host); err != nil {
			lifecycle.setProxyError(ErrDirectTargetDenied)
			lifecycle.setHTTPStatus(ErrDirectTargetDenied.HTTPCode)
			writeProxyError(w, ErrDirectTargetDenied)
			return
		}
		transport = p.directHTTPTransport()
	} else {
		routed, routeErr := resolveRoutedOutbound(p.router, p.pool, platName, account, r.Host, identity.Key)
		if routeErr != nil {
			lifecycle.setProxyError(routeErr)
			lifecycle.setHTTPStatus(routeErr.HTTPCode)
			writeProxyError(w, routeErr)
			return
		}
		route = routed.Route
		hasRoute = true
		lifecycle.setRouteResult(route)
		if p.health != nil {
			safego.Go("a forward latency record", func() {
				p.health.RecordLatency(route.NodeHash, netutil.ExtractDomain(r.Host), nil)
			})
		}
		transport = p.outboundHTTPTransport(routed)
	}
	// The upstream request carries the stream's context, so cancelling it ends
	// both the round trip and anything still copying.
	outReq := prepareForwardOutboundRequest(r.WithContext(streamCtx))
	upstreamTrace := newUpstreamRequestTrace(lifecycle.markFirstByteReceived)
	outReq = outReq.WithContext(httptrace.WithClientTrace(outReq.Context(), upstreamTrace.clientTrace()))
	pendingEgressHeaderBytes := headerWireLen(outReq.Header)
	var egressBodyCounter *countingReadCloser

	// The allowance is enforced on the stream itself: a transfer that crosses
	// the limit is cut while it is still running rather than when it ends. The
	// bytes are handed to the key's sink as they flow, so the counters the check
	// reads keep moving; the request log takes its totals from the same stream
	// through addEgressBytesLive and addIngressBytesLive, since the sink has
	// already been told about every byte.
	quota := newStreamQuota(p.quota, identity.Key, p.quotaGracePercent, cancelStream)
	if quota != nil {
		ingressReport, egressReport := lifecycle.liveReporter()
		quota.withLiveReports(ingressReport, egressReport)
	}
	// The tail of a partial report step is flushed on every exit path.
	defer quota.flushReports()

	if outReq.Body != nil && outReq.Body != http.NoBody {
		egressBodyCounter = newCountingReadCloser(outReq.Body)
		var body io.ReadCloser = egressBodyCounter
		if quota != nil {
			body = newQuotaReadCloser(body, quota)
		}
		outReq.Body = body
	}

	// Forward the request.
	resp, err := transport.RoundTrip(outReq)
	// The header bytes count as sent only once the request reached upstream. The
	// body's bytes are committed either way: they left the client and were
	// consumed here, and dropping them would make a failed or aborted upload
	// free.
	if upstreamTrace.shouldCommitEgress() {
		lifecycle.addEgressBytes(pendingEgressHeaderBytes)
	}
	if egressBodyCounter != nil {
		if quota != nil {
			lifecycle.addEgressBytesLive(egressBodyCounter.Total())
		} else {
			lifecycle.addEgressBytes(egressBodyCounter.Total())
		}
	}
	if err != nil {
		proxyErr := classifyUpstreamError(err)
		if proxyErr == nil {
			// context.Canceled — skip health recording, close silently.
			// Request ended due to client-side cancellation before upstream
			// response; treat as net-ok in request log semantics.
			lifecycle.setNetOK(true)
			return
		}
		lifecycle.setProxyError(proxyErr)
		lifecycle.setUpstreamError("forward_roundtrip", err)
		lifecycle.setHTTPStatus(proxyErr.HTTPCode)
		if hasRoute {
			recordPassiveResultAsync(p.health, route, false)
		}
		writeProxyError(w, proxyErr)
		return
	}
	defer resp.Body.Close()

	lifecycle.setHTTPStatus(resp.StatusCode)
	lifecycle.setNetOK(true)

	// Copy end-to-end response headers and body.
	lifecycle.addIngressBytes(copyEndToEndHeaders(w.Header(), resp.Header))
	w.WriteHeader(resp.StatusCode)
	var bodyWriter io.Writer = w
	if quota != nil {
		bodyWriter = &quotaResponseWriter{ResponseWriter: w, quota: quota}
	}
	copiedBytes, copyErr := io.Copy(bodyWriter, resp.Body)
	if quota != nil {
		// The bytes reached the sink as they were written; only the request log
		// needs the total now.
		lifecycle.addIngressBytesLive(copiedBytes)
	} else {
		lifecycle.addIngressBytes(copiedBytes)
	}
	if copyErr != nil {
		if quota.Exceeded() {
			// The response was cut on purpose: the key ran out of allowance.
			// The node carried what it was given, so it is not marked failed.
			lifecycle.setProxyError(ErrQuotaExceeded)
			lifecycle.setUpstreamError("forward_stream_quota", copyErr)
			lifecycle.setNetOK(true)
			return
		}
		if shouldRecordForwardCopyFailure(r, copyErr) {
			lifecycle.setProxyError(ErrUpstreamRequestFailed)
			lifecycle.setUpstreamError("forward_upstream_to_client_copy", copyErr)
			lifecycle.setNetOK(false)
			if hasRoute {
				recordPassiveResultAsync(p.health, route, false)
			}
		}
		return
	}

	// Full body transfer succeeded — count as network success even for 5xx HTTP.
	if hasRoute {
		recordPassiveResultAsync(p.health, route, true)
	}
}

func (p *ForwardProxy) handleCONNECT(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if guardProxyAuthFailure(w, r, p.authGuard) {
		return
	}
	identity, authErr := p.authenticate(r)
	if authErr != nil {
		recordProxyAuthFailure(p.authGuard, r)
		writeProxyError(w, authErr)
		return
	}
	platName, account := identity.PlatformName, identity.Account

	// The concurrency limit counts this key's live tunnels, which is why the
	// session registry is consulted here rather than at authentication. The
	// reservation is registered before anything is dialled: a tunnel that is
	// being established already holds its place, so a burst cannot all pass the
	// check, and a revocation can end it while it dials. The client connection
	// is attached to the reservation once it is hijacked.
	tunnelCtx, cancelTunnel := context.WithCancel(r.Context())
	defer cancelTunnel()
	pending := newPendingSession(cancelTunnel)
	quotaErr, reservation := reserveSession(p.quota, p.sessions, identity.Key, pending, p.quotaLogf)
	if quotaErr != nil {
		recordProxyAuthFailure(p.authGuard, r)
		writeProxyError(w, quotaErr)
		return
	}
	defer reservation.Release()

	lifecycle := newRequestLifecycle(p.events, r, ProxyTypeForward, true)
	lifecycle.setTarget(target, "")
	// A CONNECT tunnel dials its own upstream connection and holds it for the
	// session, so the bytes can be attributed as they flow rather than at the
	// end. That is what lets a long download be cut off mid-stream.
	lifecycle.attachMeter(p.metering, identity.Key, true)
	defer lifecycle.finish()
	lifecycle.setAccount(account)

	prepare := prepareConnectTunnel(
		tunnelCtx,
		tunnelDeps{
			router:      p.router,
			pool:        p.pool,
			health:      p.health,
			metricsSink: p.metricsSink,
			metering:    p.metering,
			bypass:      p.bypass,
			directGuard: p.directGuard,
			dialTimeout: p.transportConfig.DialTimeout,
		},
		platName,
		account,
		target,
		identity.Key,
	)
	if prepare.route.PlatformID != "" {
		lifecycle.setRouteResult(prepare.route)
	}
	if prepare.session == nil {
		if prepare.proxyErr != nil {
			lifecycle.setProxyError(prepare.proxyErr)
			if prepare.upstreamStage != "" {
				lifecycle.setUpstreamError(prepare.upstreamStage, prepare.upstreamErr)
			}
			lifecycle.setHTTPStatus(prepare.proxyErr.HTTPCode)
			writeProxyError(w, prepare.proxyErr)
		} else if prepare.canceled {
			lifecycle.setNetOK(true)
		}
		return
	}

	// Hijack the client connection.
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		prepare.session.upstreamConn.Close()
		lifecycle.setProxyError(ErrUpstreamRequestFailed)
		lifecycle.setUpstreamError("connect_hijack", errors.New("response writer does not support hijacking"))
		lifecycle.setHTTPStatus(ErrUpstreamRequestFailed.HTTPCode)
		prepare.session.recordResult(false)
		writeProxyError(w, ErrUpstreamRequestFailed)
		return
	}

	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		prepare.session.upstreamConn.Close()
		lifecycle.setProxyError(ErrUpstreamRequestFailed)
		lifecycle.setUpstreamError("connect_hijack", err)
		prepare.session.recordResult(false)
		return
	}

	// Write the raw CONNECT success line with proper reason phrase.
	if _, err := clientBuf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		prepare.session.upstreamConn.Close()
		clientConn.Close()
		lifecycle.setProxyError(ErrUpstreamRequestFailed)
		lifecycle.setUpstreamError("connect_client_response_write", err)
		lifecycle.setNetOK(false)
		return
	}
	if err := clientBuf.Flush(); err != nil {
		prepare.session.upstreamConn.Close()
		clientConn.Close()
		lifecycle.setProxyError(ErrUpstreamRequestFailed)
		lifecycle.setUpstreamError("connect_client_response_flush", err)
		lifecycle.setNetOK(false)
		return
	}
	// The tunnel is established: the client connection is attached to the
	// reservation that was registered before the dial, so the key's count never
	// dipped while the tunnel was being established, and a revoked key still
	// ends the session by closing the client connection - closing the upstream
	// one would leave the client waiting on a socket nobody is reading.
	pending.attach(clientConn)
	lifecycle.setHTTPStatus(http.StatusOK)
	relay := pumpPreparedTunnel(clientConn, clientBuf.Reader, prepare.session, tunnelPumpOptions{
		onFirstIngressByte: lifecycle.markFirstByteReceived,
		quotaGate:          p.quota,
		quotaKey:           identity.Key,
		quotaGracePercent:  p.quotaGracePercent,
	})
	lifecycle.addIngressBytes(relay.ingressBytes)
	lifecycle.addEgressBytes(relay.egressBytes)
	if relay.proxyErr != nil {
		lifecycle.setProxyError(relay.proxyErr)
		lifecycle.setUpstreamError(relay.upstreamStage, relay.upstreamErr)
	}
	lifecycle.setNetOK(relay.netOK)
	// A relay the allowance cut short is not a node failure: the node carried
	// the bytes it was given, so it keeps its health record.
	if !relay.quotaAborted {
		prepare.session.recordResult(relay.netOK)
	}
}

// shouldRecordForwardCopyFailure decides whether an HTTP response body copy
// error should be treated as an upstream/node failure.
func shouldRecordForwardCopyFailure(r *http.Request, copyErr error) bool {
	if copyErr == nil {
		return false
	}
	// Client-side cancellation while streaming should not penalise node health.
	if r != nil && errors.Is(r.Context().Err(), context.Canceled) {
		return false
	}
	return classifyUpstreamError(copyErr) != nil
}
