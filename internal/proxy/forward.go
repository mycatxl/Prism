package proxy

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"

	"prism/internal/keys"
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
//
// The upgrade of a protocol switch is carried over the strip. Connection and
// Upgrade are hop-by-hop headers - the proxy terminates them, exactly as it
// terminates Proxy-Authorization - so they are removed with the rest and then
// written back, in their canonical form, only for a request that is actually
// asking to switch. Leaving them in would let a client choose the next hop's
// connection semantics; dropping them unconditionally, which is what this did,
// makes a ws:// upgrade through this proxy impossible: the upstream sees an
// ordinary GET and answers 200.
func prepareForwardOutboundRequest(in *http.Request) *http.Request {
	req := in.Clone(in.Context())
	req.RequestURI = ""
	// Do not propagate client-side close semantics to upstream transport reuse.
	req.Close = false
	upgrade := upgradeType(in.Header)
	stripHopByHopHeaders(req.Header)
	if upgrade != "" {
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", upgrade)
	}
	return req
}

// upgradeType returns the protocol an upgrade request asks for, or "".
//
// Both halves have to be present for RFC 7230 to call it an upgrade: a
// Connection token naming Upgrade and an Upgrade header naming a protocol. It is
// the same test net/server and net/http/httputil perform, kept here so the
// request path does not import an internal package for one comparison.
func upgradeType(header http.Header) string {
	// The value travels on to the next hop as the protocol name, so it is read
	// like every other header value: a name that arrives padded must not be
	// forwarded padded, and a value that is only padding names nothing.
	upgrade := strings.TrimSpace(header.Get("Upgrade"))
	if upgrade == "" {
		return ""
	}
	for _, conn := range header.Values("Connection") {
		for _, token := range strings.Split(conn, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "Upgrade") {
				return upgrade
			}
		}
	}
	return ""
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
	//
	// A refusal here is not an authentication failure and must never be recorded
	// as one. The failed-auth guard counts by client address, so charging a
	// quota refusal to it would let one key's exhausted allowance block every
	// other tenant behind the same NAT or office egress after a few requests -
	// tenants whose credentials are perfectly valid. Only the errors returned by
	// authenticate are recorded above; a reservation failure is answered with its
	// own 429 and nothing else.
	quotaErr, reservation := reserveSession(p.quota, p.sessions, identity.Key, newRequestSession(cancelStream), p.quotaLogf)
	if quotaErr != nil {
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

	// The plan's bandwidth limit is charged on the two copies this handler owns:
	// the request body net/http's transport reads from, and the response body the
	// client receives. It cannot be charged on the connection the way a tunnel
	// charges its socket - the HTTP transport is pooled per node, so one
	// connection serves many requests and cannot be said to belong to one key -
	// which is why the gate is installed on the bodies instead. It stops waiting
	// as soon as the request's context ends, so a transfer cancelled mid-throttle
	// releases its goroutine within one step rather than after the whole
	// remainder.
	bandwidth := newBandwidthGate(identity.Key, func() bool { return streamCtx.Err() != nil })

	if outReq.Body != nil && outReq.Body != http.NoBody {
		egressBodyCounter = newCountingReadCloser(outReq.Body)
		var body io.ReadCloser = egressBodyCounter
		if quota != nil {
			body = newQuotaReadCloser(body, quota)
		}
		outReq.Body = throttleBody(body, bandwidth)
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
		// The allowance can end the round trip in two shapes, and they are the
		// same event. If the cancel lands before the transport looks at the
		// stream again it surfaces as context.Canceled; if the transport was
		// inside a body read, the stream's own error surfaces instead and looks
		// like a transport failure. Consulting the allowance first answers both
		// the same way, and it has to be answered: nothing has been written yet
		// - no status line, no headers - so returning silently would leave
		// net/http to complete the exchange as "200, empty body" and the request
		// log to record a success for a request that was in fact refused. The
		// client is told what happened instead, and the log names the cut. It is
		// not a node failure, so no health result is recorded either.
		if quota.Exceeded() {
			lifecycle.setProxyError(ErrQuotaExceeded)
			lifecycle.setUpstreamError("forward_roundtrip_quota", err)
			lifecycle.setHTTPStatus(ErrQuotaExceeded.HTTPCode)
			writeProxyError(w, ErrQuotaExceeded)
			return
		}
		proxyErr := classifyUpstreamError(err)
		if proxyErr == nil {
			// context.Canceled with the allowance untouched: the client went
			// away. Nobody is left to read an answer, so the exchange stays
			// silent and keeps the net-ok semantics the paths before this one
			// have always had.
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

	// A 101 is not a body to copy: the exchange continues in both directions on
	// the same connection from here on, and the hop-by-hop stripping the ordinary
	// path performs would remove the very headers that carry the switch. It is
	// relayed separately below.
	if resp.StatusCode == http.StatusSwitchingProtocols {
		p.serveForwardUpgrade(w, resp, forwardUpgradeState{
			lifecycle: lifecycle,
			bandwidth: bandwidth,
			key:       identity.Key,
			hasRoute:  hasRoute,
			route:     route,
		})
		return
	}

	lifecycle.setHTTPStatus(resp.StatusCode)
	lifecycle.setNetOK(true)

	// Copy end-to-end response headers and body.
	lifecycle.addIngressBytes(copyEndToEndHeaders(w.Header(), resp.Header))
	w.WriteHeader(resp.StatusCode)
	var bodyWriter io.Writer = w
	if quota != nil {
		bodyWriter = &quotaResponseWriter{ResponseWriter: w, quota: quota}
	}
	// The body is streamed, not collected, but net/http buffers a response
	// before it reaches the socket. Without a flush an SSE event, a chunked
	// heartbeat or a slowly produced log tail would sit in that buffer until it
	// filled or the handler returned, which turns every realtime stream this
	// proxy carries into one big response at the end. Every chunk is flushed as
	// it is written; the flusher unwraps the quota wrapper through the Unwrap
	// that wrapper has always exposed for exactly this.
	copiedBytes, copyErr := io.Copy(newResponseFlusher(bodyWriter, w), throttleBody(resp.Body, bandwidth))
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
	//
	// A refusal is a quota or concurrency verdict, not a failed authentication,
	// so it is not charged to the failed-auth guard: that guard blocks by client
	// address, and one key hitting its tunnel limit must not lock out the other
	// tenants sharing the address it came from. The verdict is answered as its
	// own 429 and nothing else.
	tunnelCtx, cancelTunnel := context.WithCancel(r.Context())
	defer cancelTunnel()
	pending := newPendingSession(cancelTunnel)
	quotaErr, reservation := reserveSession(p.quota, p.sessions, identity.Key, pending, p.quotaLogf)
	if quotaErr != nil {
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

// forwardUpgradeState carries the per-request values the 101 relay needs from
// handleHTTP. It is a struct rather than a long parameter list because the
// relay needs the same lifecycle, key and route the ordinary path already
// holds.
type forwardUpgradeState struct {
	lifecycle *requestLifecycle
	bandwidth *bandwidthGate
	key       *keys.KeyState
	hasRoute  bool
	route     routing.RouteResult
}

// serveForwardUpgrade relays a 101 Switching Protocols exchange.
//
// A 101 is the one response that is not a body. After the status line both
// peers keep talking on the same connection, in both directions, for as long as
// the protocol they switched to wants. Copying resp.Body to the client - which
// is what this handler did with every response - carried at most one direction
// of that conversation and dropped Connection/Upgrade on the way, so a ws://
// upgrade through this proxy could not work even when the upstream answered
// 101.
//
// The relay is not a third implementation of the duplex copy: it is the tunnel
// pump this package already drives for CONNECT and SOCKS5, over the hijacked
// client connection and an adapter that presents the transport's upgraded body
// as the other end. That is what gives an upgraded request the same mid-stream
// allowance enforcement, the same byte accounting and the same half-close
// behaviour a tunnel gets, instead of one more copy loop to keep in step.
func (p *ForwardProxy) serveForwardUpgrade(w http.ResponseWriter, resp *http.Response, state forwardUpgradeState) {
	lifecycle := state.lifecycle
	upstream, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		lifecycle.setProxyError(ErrUpstreamRequestFailed)
		lifecycle.setUpstreamError("forward_upgrade_body", errors.New("upstream upgrade body is not a read-write stream"))
		lifecycle.setHTTPStatus(ErrUpstreamRequestFailed.HTTPCode)
		writeProxyError(w, ErrUpstreamRequestFailed)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		lifecycle.setProxyError(ErrUpstreamRequestFailed)
		lifecycle.setUpstreamError("forward_upgrade_hijack", errors.New("response writer does not support hijacking"))
		lifecycle.setHTTPStatus(ErrUpstreamRequestFailed.HTTPCode)
		writeProxyError(w, ErrUpstreamRequestFailed)
		return
	}

	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		lifecycle.setProxyError(ErrUpstreamRequestFailed)
		lifecycle.setUpstreamError("forward_upgrade_hijack", err)
		return
	}

	// The response head is written over the hijacked connection rather than
	// through the ResponseWriter, because the hijack hands the socket over. The
	// body is cleared first so the write emits the status line and the headers
	// only: the upstream response carries Connection and Upgrade itself, and
	// those are exactly the headers the client needs in order to accept the
	// switch, so they travel through untouched instead of being re-derived from
	// the hop-by-hop list.
	body := resp.Body
	resp.Body = nil
	if err := resp.Write(clientBuf); err != nil {
		_ = clientConn.Close()
		_ = body.Close()
		lifecycle.setProxyError(ErrUpstreamRequestFailed)
		lifecycle.setUpstreamError("forward_upgrade_response_write", err)
		return
	}
	if err := clientBuf.Flush(); err != nil {
		_ = clientConn.Close()
		_ = body.Close()
		lifecycle.setProxyError(ErrUpstreamRequestFailed)
		lifecycle.setUpstreamError("forward_upgrade_response_flush", err)
		return
	}
	lifecycle.setHTTPStatus(http.StatusSwitchingProtocols)

	relay := pumpPreparedTunnel(
		clientConn,
		clientBuf.Reader,
		&preparedTunnel{
			upstreamConn: newUpgradeStreamConn(upstream, clientConn, state.bandwidth),
			recordResult: func(bool) {},
		},
		tunnelPumpOptions{
			onFirstIngressByte: lifecycle.markFirstByteReceived,
			// The switched protocol is a stream of the key's bytes and stays
			// inside the key's allowance: a websocket that was already over the
			// limit must not run for as long as its peers want.
			quotaGate:         p.quota,
			quotaKey:          state.key,
			quotaGracePercent: p.quotaGracePercent,
		},
	)
	lifecycle.addIngressBytes(relay.ingressBytes)
	lifecycle.addEgressBytes(relay.egressBytes)
	if relay.proxyErr != nil {
		lifecycle.setProxyError(relay.proxyErr)
		lifecycle.setUpstreamError(relay.upstreamStage, relay.upstreamErr)
	}
	lifecycle.setNetOK(relay.netOK)
}

// upgradeStreamConn presents an upgraded response body as the net.Conn the
// tunnel relay pumps.
//
// net/http hands a 101 response's connection back as an io.ReadWriteCloser and
// nothing more, while pumpPreparedTunnel speaks net.Conn. The adapter is the
// whole of the difference: addresses for reporting, no deadlines because the
// relay sets none and the connection is no longer net/http's to schedule, the
// optional half-close pair forwarded so a protocol that half-closes does not
// tear the other direction down (see countingReadWriteCloser.CloseWrite), and
// the plan's bandwidth limit charged on both directions.
type upgradeStreamConn struct {
	io.ReadWriteCloser
	local     net.Addr
	remote    net.Addr
	bandwidth *bandwidthGate
}

func newUpgradeStreamConn(rwc io.ReadWriteCloser, clientConn net.Conn, bandwidth *bandwidthGate) *upgradeStreamConn {
	conn := &upgradeStreamConn{ReadWriteCloser: rwc, bandwidth: bandwidth}
	if clientConn != nil {
		// The client's addresses stand in for the upstream ones: they are the
		// only pair this handler can name, and the relay uses them for nothing
		// but its own report.
		conn.local = clientConn.LocalAddr()
		conn.remote = clientConn.RemoteAddr()
	}
	return conn
}

func (c *upgradeStreamConn) Read(p []byte) (int, error) {
	n, err := c.ReadWriteCloser.Read(p)
	if n > 0 {
		c.bandwidth.throttle(n)
	}
	return n, err
}

func (c *upgradeStreamConn) Write(p []byte) (int, error) {
	n, err := c.ReadWriteCloser.Write(p)
	if n > 0 {
		c.bandwidth.throttle(n)
	}
	return n, err
}

func (c *upgradeStreamConn) LocalAddr() net.Addr  { return c.local }
func (c *upgradeStreamConn) RemoteAddr() net.Addr { return c.remote }

// Deadlines are accepted and ignored: the relay never sets one, and the socket
// belongs to the switched protocol now, not to net/http's server.
func (c *upgradeStreamConn) SetDeadline(time.Time) error      { return nil }
func (c *upgradeStreamConn) SetReadDeadline(time.Time) error  { return nil }
func (c *upgradeStreamConn) SetWriteDeadline(time.Time) error { return nil }

func (c *upgradeStreamConn) CloseWrite() error { return closeWriteIfSupported(c.ReadWriteCloser) }
func (c *upgradeStreamConn) CloseRead() error  { return closeReadIfSupported(c.ReadWriteCloser) }

// responseFlusher writes a response body to the client and flushes after each
// chunk, so a streaming response reaches the client as it is produced instead of
// when net/http's buffer fills.
//
// The ResponseController is what makes the flush reach the real connection:
// quotaResponseWriter exposes Unwrap for exactly this, and without it a wrapper
// would hide the Flusher the server installed and silently turn every stream
// back into a buffered response. A writer that cannot flush - httptest's
// recorder, an HTTP/2 stream, a connection the client already closed - reports
// http.ErrNotSupported or an error, and the flusher then stops asking rather
// than paying for a probe on every chunk of the body.
type responseFlusher struct {
	writer     io.Writer
	controller *http.ResponseController
}

func newResponseFlusher(writer io.Writer, responseWriter http.ResponseWriter) *responseFlusher {
	flusher := &responseFlusher{writer: writer}
	if responseWriter != nil {
		flusher.controller = http.NewResponseController(responseWriter)
	}
	return flusher
}

func (f *responseFlusher) Write(p []byte) (int, error) {
	n, err := f.writer.Write(p)
	// The flush happens even when the write reported an error: the bytes that
	// did go out are the ones the client is waiting for, and a quota cut reports
	// itself through that same error.
	if n > 0 && f.controller != nil {
		if flushErr := f.controller.Flush(); flushErr != nil {
			// A broken connection is reported by the next write, and a writer
			// that cannot flush never will; either way there is nothing left to
			// ask for on the chunks that follow.
			f.controller = nil
		}
	}
	return n, err
}
