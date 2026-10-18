package proxy

import (
	"context"
	"crypto/subtle"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"prism/internal/keys"
	"prism/internal/netutil"
	"prism/internal/outbound"
	"prism/internal/platform"
	"prism/internal/routing"
	"prism/internal/safego"
)

// PlatformLookup provides read-only access to platforms.
type PlatformLookup interface {
	GetPlatform(id string) (*platform.Platform, bool)
	GetPlatformByName(name string) (*platform.Platform, bool)
}

// ReverseProxyConfig holds dependencies for the reverse proxy.
type ReverseProxyConfig struct {
	ProxyToken string
	// AccessKeys resolves an access key presented as the first path segment.
	// It may be nil or unconfigured: the segment then falls back to the global
	// proxy token, which is the transitional behaviour.
	AccessKeys        AccessKeyVerifier
	Router            *routing.Router
	Pool              outbound.PoolAccessor
	PlatformLookup    PlatformLookup
	Health            HealthRecorder
	Matcher           AccountRuleMatcher
	Events            EventEmitter
	MetricsSink       MetricsEventSink
	OutboundTransport OutboundTransportConfig
	TransportPool     *OutboundTransportPool
	ProxyBypassRules  []string
	// DirectDenyPrivate enables the local dial guard on every local dial path
	// (this proxy's bypass branch, the forward HTTP proxy, CONNECT and SOCKS5)
	// while keeping node-routed requests unaffected. It is the
	// PRISM_DIRECT_DENY_PRIVATE switch and defaults to false, which keeps the
	// upstream Resin behaviour.
	DirectDenyPrivate bool

	// BlockedDestinationPorts lists the destination ports the same guard
	// refuses on every local dial path (PRISM_BLOCKED_DESTINATION_PORTS). It is
	// independent of DirectDenyPrivate: clearing that switch never reopens a
	// blocked port.
	BlockedDestinationPorts []int
	// AuthGuard optionally rate limits failed proxy authentications. A nil
	// guard keeps the upstream Resin behaviour (PRISM_PROXY_AUTH_FAIL_LIMIT
	// defaults to 0, which disables it).
	AuthGuard AuthFailureGuard
	// Metering attributes this proxy's traffic to the access key that caused
	// it. Nil disables the accounting.
	Metering MeteringSink
	// Quota decides whether a request may proceed. Nil allows everything.
	Quota QuotaGate
	// QuotaGracePercent is the tolerance a live stream gets before it is cut
	// for running past the key's allowance, in percent of the limit. Zero
	// applies DefaultStreamGracePercent; a negative value means no tolerance at
	// all.
	QuotaGracePercent int
	// Sessions holds this proxy's live requests per key, so a revocation,
	// a disable or an exhausted allowance ends a request that is already
	// running rather than only the next one. Nil leaves that to the next
	// request.
	Sessions SessionTracker
	// QuotaLogf reports a request the gate refused. A refusal happens before
	// the request lifecycle exists, so it leaves no trace in the request log;
	// the line names the key id and the reason and nothing else. Nil keeps a
	// refusal silent, which is what a deployment without plans gets.
	QuotaLogf func(format string, args ...any)
}

// ReverseProxy implements an HTTP reverse proxy.
type ReverseProxy struct {
	token             string
	accessKeys        AccessKeyVerifier
	router            *routing.Router
	pool              outbound.PoolAccessor
	platLook          PlatformLookup
	health            HealthRecorder
	matcher           AccountRuleMatcher
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

// NewReverseProxy creates a new reverse proxy handler.
func NewReverseProxy(cfg ReverseProxyConfig) *ReverseProxy {
	ev := cfg.Events
	if ev == nil {
		ev = NoOpEventEmitter{}
	}
	transportCfg := normalizeOutboundTransportConfig(cfg.OutboundTransport)
	transportPool := cfg.TransportPool
	if transportPool == nil {
		transportPool = NewOutboundTransportPool(transportCfg)
	}
	return &ReverseProxy{
		token:             cfg.ProxyToken,
		accessKeys:        cfg.AccessKeys,
		router:            cfg.Router,
		pool:              cfg.Pool,
		platLook:          cfg.PlatformLookup,
		health:            cfg.Health,
		matcher:           cfg.Matcher,
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

func (p *ReverseProxy) outboundHTTPTransport(routed routedOutbound) *http.Transport {
	p.transportPoolOnce.Do(func() {
		if p.transportPool == nil {
			p.transportPool = NewOutboundTransportPool(p.transportConfig)
		}
	})
	return p.transportPool.Get(routed.Route.NodeHash, routed.Outbound, p.metricsSink)
}

func (p *ReverseProxy) directHTTPTransport() *http.Transport {
	p.directOnce.Do(func() {
		p.directTransport = newDirectHTTPTransport(p.transportConfig, p.metricsSink, p.directGuard)
	})
	return p.directTransport
}

// parsedPath holds the result of parsing a reverse proxy request path.
type parsedPath struct {
	PlatformName string
	Account      string
	Protocol     string
	Host         string
	// Path preserves the original escaped remaining path after host (may be
	// empty), e.g. "v1/users/team%2Fa/profile".
	Path string
	// Key is the access key the first path segment resolved to, or nil when the
	// request authenticated with the transitional global proxy token.
	Key *keys.KeyState
}

// forwardingIdentityHeaders are commonly used to disclose proxy chain identity.
// These are stripped from outbound reverse-proxy requests.
var forwardingIdentityHeaders = []string{
	// Internal account override headers must not leak to upstream services.
	// Both the Prism and the upstream Resin spelling are stripped (X4).
	"X-Prism-Account",
	"X-Resin-Account",
	"Forwarded",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Forwarded-Port",
	"X-Forwarded-Server",
	"Via",
	"X-Real-IP",
	"X-Client-IP",
	"True-Client-IP",
	"CF-Connecting-IP",
	"X-ProxyUser-Ip",
}

func stripForwardingIdentityHeaders(header http.Header) {
	if header == nil {
		return
	}
	for _, h := range forwardingIdentityHeaders {
		header.Del(h)
	}
}

// decodePathSegmentV1 decodes one escaped URL path segment for V1 parsing.
func decodePathSegmentV1(segment string) (string, *ProxyError) {
	decoded, err := url.PathUnescape(segment)
	if err != nil {
		return "", ErrURLParseError
	}
	return decoded, nil
}

func (p *ReverseProxy) parsePath(rawPath string) (*parsedPath, *ProxyError) {
	return p.parsePathV1(rawPath)
}

// parsePathV1 parses reverse proxy path using V1 identity rules.
//
// rawPath must be r.URL.EscapedPath (not r.URL.Path) to preserve escaped
// delimiters in trailing path segments.
func (p *ReverseProxy) parsePathV1(rawPath string) (*parsedPath, *ProxyError) {
	path := strings.TrimPrefix(rawPath, "/")
	if path == "" {
		return nil, ErrAuthFailed
	}

	segments := strings.SplitN(path, "/", 5) // token, identity, protocol, host, rest
	token, perr := decodePathSegmentV1(segments[0])
	if perr != nil {
		return nil, perr
	}
	// The first segment carries either an access key or the transitional global
	// proxy token. A key-shaped segment never falls back to the token
	// comparison: a revoked key must be refused outright rather than retried as
	// a token.
	var keyState *keys.KeyState
	if keys.IsCredentialShape(token) {
		if p.accessKeys != nil {
			keyState = p.accessKeys.VerifyCredential(token)
		}
		if keyState == nil {
			return nil, ErrAuthFailed
		}
	} else if p.token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(p.token)) != 1 {
		return nil, ErrAuthFailed
	}
	if len(segments) < 4 {
		return nil, ErrURLParseError
	}

	identity, perr := decodePathSegmentV1(segments[1])
	if perr != nil {
		return nil, perr
	}
	platName, account := parseV1PlatformAccountIdentity(identity)

	protocolSeg, perr := decodePathSegmentV1(segments[2])
	if perr != nil {
		return nil, perr
	}
	protocol := strings.ToLower(protocolSeg)
	if protocol != "http" && protocol != "https" {
		return nil, ErrInvalidProtocol
	}

	host, perr := decodePathSegmentV1(segments[3])
	if perr != nil {
		return nil, perr
	}
	if host == "" {
		return nil, ErrInvalidHost
	}
	// Syntax check only: private and loopback targets are valid here because
	// node-routed requests never reach them from this host. Local direct
	// (bypass) requests are checked separately when PRISM_DIRECT_DENY_PRIVATE
	// is enabled.
	if !isValidHost(host) {
		return nil, ErrInvalidHost
	}

	remainingPath := ""
	if len(segments) == 5 {
		remainingPath = segments[4]
	}

	return &parsedPath{
		PlatformName: platName,
		Account:      account,
		Protocol:     protocol,
		Host:         host,
		Path:         remainingPath,
		Key:          keyState,
	}, nil
}

func buildReverseTargetURL(parsed *parsedPath, rawQuery string) (*url.URL, *ProxyError) {
	targetURL := parsed.Protocol + "://" + parsed.Host
	if parsed.Path != "" {
		targetURL += "/" + parsed.Path
	}
	if rawQuery != "" {
		targetURL += "?" + rawQuery
	}
	target, err := url.Parse(targetURL)
	if err != nil {
		return nil, ErrInvalidHost
	}
	return target, nil
}

func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	detailCfg := reverseDetailCaptureConfig{
		Enabled:             false,
		ReqHeadersMaxBytes:  -1,
		ReqBodyMaxBytes:     -1,
		RespHeadersMaxBytes: -1,
		RespBodyMaxBytes:    -1,
	}
	if provider, ok := p.events.(interface {
		reverseDetailCaptureConfig() reverseDetailCaptureConfig
	}); ok {
		detailCfg = provider.reverseDetailCaptureConfig()
	}

	parsed, perr := p.parsePath(r.URL.EscapedPath())
	if perr != nil {
		if perr == ErrAuthFailed {
			if guardProxyAuthFailure(w, r, p.authGuard) {
				return
			}
			recordProxyAuthFailure(p.authGuard, r)
		}
		writeProxyError(w, perr)
		return
	}

	// One context ends the whole request: a quota cut cancels it, and so does
	// the session registry when the key is revoked while the request is in
	// flight.
	streamCtx, cancelStream := context.WithCancel(r.Context())
	defer cancelStream()
	r = r.WithContext(streamCtx)

	// The allowance is checked before anything is dialled, and the check
	// reserves the session it is about to admit. The concurrency limit counts
	// the requests this key already holds, which is what the registry answers;
	// reading that count and registering afterwards let a burst of requests all
	// see a count of zero.
	quotaErr, reservation := reserveSession(p.quota, p.sessions, parsed.Key, newRequestSession(cancelStream), p.quotaLogf)
	if quotaErr != nil {
		writeProxyError(w, quotaErr)
		return
	}
	// An HTTP request is a session of this key exactly as a tunnel is: a
	// revocation, a disable or an exhausted allowance must end a request that is
	// already running rather than only refusing the next one. The unregister is
	// deferred, so every exit path from here on releases the count.
	defer reservation.Release()

	lifecycle := newRequestLifecycle(p.events, r, ProxyTypeReverse, false)
	lifecycle.setTarget(parsed.Host, "")
	// The reverse proxy's upstream transport is pooled per node, exactly like
	// the forward proxy's, so its connections are reused across requests and
	// cannot carry a key. The lifecycle can: it is per request.
	lifecycle.attachMeter(p.metering, parsed.Key, false)
	upstreamTrace := newUpstreamRequestTrace(lifecycle.markFirstByteReceived)
	var pendingEgressHeaderBytes int64
	var egressBodyCounter *countingReadCloser
	var ingressBodyCounter *countingReadCloser
	var upgradedStreamCounter *countingReadWriteCloser
	var upgradedStreamQuota *quotaReadWriteCloser
	behaviorPlatform := p.resolvePlatformForAccountBehavior(parsed.PlatformName)
	// Share one rule snapshot between extraction and capture, including during
	// account-rule hot reloads.
	accountHeaders := p.accountExtractionHeaders(parsed, behaviorPlatform)
	if detailCfg.Enabled {
		reqHeaders, reqHeadersLen, reqHeadersTruncated := captureHeadersWithLimit(
			r.Header, detailCfg.ReqHeadersMaxBytes, accountHeaders...,
		)
		lifecycle.setReqHeadersCaptured(reqHeaders, reqHeadersLen, reqHeadersTruncated)
	}
	if r.Body != nil && r.Body != http.NoBody {
		body := r.Body
		if detailCfg.Enabled {
			reqBodyCapture := newPayloadCaptureReadCloser(body, detailCfg.ReqBodyMaxBytes)
			body = reqBodyCapture
			lifecycle.setReqBodyCapture(reqBodyCapture)
		}
		egressBodyCounter = newCountingReadCloser(body)
		r.Body = egressBodyCounter
	}
	defer lifecycle.finish()

	// Resolve account in three phases:
	// 1) Prefer an explicit account override, then a path account.
	// 2) If extraction fails, apply miss-action (REJECT or treat-as-empty).
	// 3) Continue routing with the resulting account (possibly empty).
	account, behavior, extractionFailed := resolveReverseAccount(parsed, r, behaviorPlatform, accountHeaders)
	lifecycle.setAccount(account)
	if parsed.Account == "" && accountOverrideHeader(r) == "" && behaviorRequiresAccountExtraction(behavior) {
		lifecycle.setAccount(logHeaderAccount(account, p.token))
	}

	if shouldRejectReverseProxyAccountExtractionFailure(extractionFailed, behaviorPlatform) {
		lifecycle.setProxyError(ErrAccountRejected)
		lifecycle.setHTTPStatus(ErrAccountRejected.HTTPCode)
		writeProxyError(w, ErrAccountRejected)
		return
	}

	target, targetErr := buildReverseTargetURL(parsed, r.URL.RawQuery)
	if targetErr != nil {
		lifecycle.setProxyError(targetErr)
		lifecycle.setHTTPStatus(targetErr.HTTPCode)
		writeProxyError(w, targetErr)
		return
	}
	lifecycle.setTarget(parsed.Host, target.String())

	var route routing.RouteResult
	var hasRoute bool
	var transport *http.Transport
	var nodeHashRaw = route.NodeHash
	domain := netutil.ExtractDomain(parsed.Host)
	if p.bypass != nil && p.bypass.ShouldBypass(parsed.Host) {
		// Bypass targets are dialled locally by operator-configured rules and
		// never create a routing lease (upstream Resin behaviour).
		// PRISM_DIRECT_DENY_PRIVATE optionally refuses loopback, private,
		// link-local, CGNAT, reserved and cloud metadata direct targets. The
		// same policy covers every local dial path (forward proxy, CONNECT and
		// SOCKS5); it never affects node-routed requests.
		if err := p.directGuard.CheckTarget(r.Context(), parsed.Host); err != nil {
			lifecycle.setProxyError(ErrDirectTargetDenied)
			lifecycle.setHTTPStatus(ErrDirectTargetDenied.HTTPCode)
			writeProxyError(w, ErrDirectTargetDenied)
			return
		}
		transport = p.directHTTPTransport()
	} else {
		routed, routeErr := resolveRoutedOutbound(p.router, p.pool, parsed.PlatformName, account, parsed.Host, parsed.Key)
		if routeErr != nil {
			lifecycle.setProxyError(routeErr)
			lifecycle.setHTTPStatus(routeErr.HTTPCode)
			writeProxyError(w, routeErr)
			return
		}
		route = routed.Route
		hasRoute = true
		nodeHashRaw = route.NodeHash
		lifecycle.setRouteResult(route)
		if p.health != nil {
			safego.Go("a reverse latency record", func() {
				p.health.RecordLatency(nodeHashRaw, domain, nil)
			})
		}
		transport = p.outboundHTTPTransport(routed)
	}

	// The allowance is enforced on the stream itself: a transfer that crosses
	// the limit is cut while it is still running rather than when it ends. The
	// bytes are handed to the key's sink as they flow, because the copies that
	// move them belong to net/http's transport write loop and to httputil, not
	// to this handler: committing them only at the end would leave the counters
	// the check reads frozen for the whole transfer. The request log still takes
	// its exact totals from the same counters through addIngressBytesLive and
	// addEgressBytesLive, since the sink has already been told about every byte.
	quota := newStreamQuota(p.quota, parsed.Key, p.quotaGracePercent, cancelStream)
	if quota != nil {
		ingressReport, egressReport := lifecycle.liveReporter()
		quota.withLiveReports(ingressReport, egressReport)
	}
	// The tail of a partial report step is flushed on every exit path, including
	// the one that unwinds through httputil's ErrAbortHandler panic.
	defer quota.flushReports()
	if egressBodyCounter != nil && quota != nil {
		r.Body = newQuotaReadCloser(egressBodyCounter, quota)
	}

	proxy := &httputil.ReverseProxy{
		// Rewrite, not the deprecated Director: it runs after the hop-by-hop
		// headers and the client-supplied forwarding headers are gone, and it
		// never re-adds X-Forwarded-For, so the stripping below is the only
		// thing that decides what upstream sees.
		Rewrite: func(pr *httputil.ProxyRequest) {
			// Assign the target URL directly rather than through SetURL: SetURL
			// joins the inbound path with the target path and merges the two
			// queries, which would double the path of an already complete
			// target.
			pr.Out.URL = target
			pr.Out.Host = parsed.Host
			stripForwardingIdentityHeaders(pr.Out.Header)
			pendingEgressHeaderBytes = headerWireLen(pr.Out.Header)

			// Compose request-progress trace first so egress commit logic can
			// observe whether the upstream request was actually written.
			reqCtx := httptrace.WithClientTrace(pr.Out.Context(), upstreamTrace.clientTrace())

			// Add httptrace for TLS latency measurement on HTTPS.
			if parsed.Protocol == "https" && hasRoute && p.health != nil {
				reporter := newReverseLatencyReporter(p.health, nodeHashRaw, domain)
				reqCtx = httptrace.WithClientTrace(reqCtx, reporter.clientTrace())
			}
			pr.Out = pr.Out.WithContext(reqCtx)
		},
		Transport: transport,
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			proxyErr := classifyUpstreamError(err)
			if proxyErr == nil {
				// context.Canceled — no health recording, silently close.
				// Treat as net-ok for request-log semantics when canceled
				// before upstream response.
				lifecycle.setNetOK(true)
				return
			}
			lifecycle.setProxyError(proxyErr)
			lifecycle.setUpstreamError("reverse_roundtrip", err)
			lifecycle.setNetOK(false)
			lifecycle.setHTTPStatus(proxyErr.HTTPCode)
			if hasRoute {
				recordPassiveResultAsync(p.health, route, false)
			}
			writeProxyError(rw, proxyErr)
		},
		ModifyResponse: func(resp *http.Response) error {
			lifecycle.setHTTPStatus(resp.StatusCode)
			lifecycle.addIngressBytes(headerWireLen(resp.Header))
			if resp.StatusCode == http.StatusSwitchingProtocols {
				// 101 upgrade responses require a writable backend body
				// (io.ReadWriteCloser). Do not wrap resp.Body here; wrapping with a
				// read-only wrapper breaks websocket/h2c upgrade tunneling in
				// net/http/httputil.ReverseProxy.
				//
				// We can still account upgrade-session traffic by wrapping with a
				// read-write counter that preserves io.ReadWriteCloser semantics,
				// and enforce the key's allowance on it: httputil copies both
				// directions through this body, so a wrapper that reports the
				// bytes as they flow and refuses to continue once the allowance
				// is gone is what keeps an upgraded connection inside the same
				// budget as every other transfer. Wrapping with a read-only
				// wrapper would break websocket/h2c upgrade tunnelling.
				if rwc, ok := resp.Body.(io.ReadWriteCloser); ok {
					upgradedStreamCounter = newCountingReadWriteCloser(rwc)
					if quota != nil {
						upgradedStreamQuota = newQuotaReadWriteCloser(upgradedStreamCounter, quota)
						resp.Body = upgradedStreamQuota
					} else {
						resp.Body = upgradedStreamCounter
					}
				}
				if detailCfg.Enabled {
					respHeaders, respHeadersLen, respHeadersTruncated := captureHeadersWithLimit(resp.Header, detailCfg.RespHeadersMaxBytes, accountHeaders...)
					lifecycle.setRespHeadersCaptured(respHeaders, respHeadersLen, respHeadersTruncated)
				}
				lifecycle.setNetOK(true)
				if hasRoute {
					recordPassiveResultAsync(p.health, route, true)
				}
				return nil
			}
			if resp.Body != nil && resp.Body != http.NoBody {
				body := resp.Body
				if detailCfg.Enabled {
					respHeaders, respHeadersLen, respHeadersTruncated := captureHeadersWithLimit(resp.Header, detailCfg.RespHeadersMaxBytes, accountHeaders...)
					lifecycle.setRespHeadersCaptured(respHeaders, respHeadersLen, respHeadersTruncated)
					respBodyCapture := newPayloadCaptureReadCloser(body, detailCfg.RespBodyMaxBytes)
					body = respBodyCapture
					lifecycle.setRespBodyCapture(respBodyCapture)
				}
				ingressBodyCounter = newCountingReadCloser(body)
				if quota != nil {
					// The body read by httputil's copy loop is the only place
					// this handler sees the response bytes, so it is where the
					// ingress half is counted and checked.
					resp.Body = newQuotaReadCloserDir(ingressBodyCounter, quota, true)
				} else {
					resp.Body = ingressBodyCounter
				}
			} else if detailCfg.Enabled {
				respHeaders, respHeadersLen, respHeadersTruncated := captureHeadersWithLimit(resp.Header, detailCfg.RespHeadersMaxBytes, accountHeaders...)
				lifecycle.setRespHeadersCaptured(respHeaders, respHeadersLen, respHeadersTruncated)
			}
			// Intentional coarse-grained policy:
			// mark node success once upstream response headers arrive.
			// Further attribution for mid-body stream failures is expensive and noisy
			// (client abort vs upstream reset vs network blip), and the added
			// complexity is not worth it for the current phase.
			lifecycle.setNetOK(true)
			if hasRoute {
				recordPassiveResultAsync(p.health, route, true)
			}
			return nil
		},
	}

	// The accounting is deferred rather than written after the call: a copy that
	// fails makes httputil panic with http.ErrAbortHandler, and that panic is a
	// normal end for a transfer the allowance cut short. Deferred, the totals
	// still reach the request log on that path, and the bytes that did move are
	// neither lost nor billed twice.
	defer func() {
		if upstreamTrace.shouldCommitEgress() {
			lifecycle.addEgressBytes(pendingEgressHeaderBytes)
			if egressBodyCounter != nil {
				if quota != nil {
					// The stream already reported these bytes to the sink.
					lifecycle.addEgressBytesLive(egressBodyCounter.Total())
				} else {
					lifecycle.addEgressBytes(egressBodyCounter.Total())
				}
			}
		}
		if ingressBodyCounter != nil {
			if quota != nil {
				lifecycle.addIngressBytesLive(ingressBodyCounter.Total())
			} else {
				lifecycle.addIngressBytes(ingressBodyCounter.Total())
			}
		}
		// Upgrade-session traffic. The counters are atomic, so reading them here
		// is race-free even though httputil's copy goroutines own the writes: in
		// the clean path handleUpgradeResponse only returns after both directions
		// sent on its error channel (i.e. both copies finished), while an error
		// path can return after the first one — there the read is simply a
		// partial snapshot, which is a statistics-precision limit, not a race.
		// When the upgraded stream carried the quota wrapper it reported every
		// step to the sink as it flowed, so only the request log takes these
		// totals; committing them the ordinary way as well would bill the same
		// byte twice.
		if upgradedStreamCounter != nil {
			if upgradedStreamQuota != nil {
				lifecycle.addIngressBytesLive(upgradedStreamCounter.TotalRead())
				lifecycle.addEgressBytesLive(upgradedStreamCounter.TotalWrite())
			} else {
				lifecycle.addIngressBytes(upgradedStreamCounter.TotalRead())
				lifecycle.addEgressBytes(upgradedStreamCounter.TotalWrite())
			}
		}
		if quota.Exceeded() {
			// The transfer was cut on purpose: the key ran out of allowance.
			// The upstream carried what it was given, so it is not marked
			// failed, and the cut is visible in the request log.
			lifecycle.setProxyError(ErrQuotaExceeded)
			lifecycle.setNetOK(true)
		}
	}()

	proxy.ServeHTTP(w, r)
}

// resolveDefaultPlatform looks up the default platform for reverse-proxy
// miss-action checks when PlatformName is empty.
func (p *ReverseProxy) resolveDefaultPlatform() *platform.Platform {
	if plat, ok := p.platLook.GetPlatform(platform.DefaultPlatformID); ok {
		return plat
	}
	return nil
}

func (p *ReverseProxy) resolvePlatformForAccountBehavior(platformName string) *platform.Platform {
	if p == nil || p.platLook == nil {
		return nil
	}
	if platformName != "" {
		if plat, ok := p.platLook.GetPlatformByName(platformName); ok {
			return plat
		}
		return nil
	}
	return p.resolveDefaultPlatform()
}

func effectiveEmptyAccountBehavior(plat *platform.Platform) platform.ReverseProxyEmptyAccountBehavior {
	if plat == nil {
		return platform.ReverseProxyEmptyAccountBehaviorRandom
	}
	behavior := platform.ReverseProxyEmptyAccountBehavior(plat.ReverseProxyEmptyAccountBehavior)
	if behavior.IsValid() {
		return behavior
	}
	return platform.ReverseProxyEmptyAccountBehaviorRandom
}

func (p *ReverseProxy) resolveReverseProxyAccount(
	parsed *parsedPath,
	r *http.Request,
	plat *platform.Platform,
) (string, platform.ReverseProxyEmptyAccountBehavior, bool) {
	return resolveReverseAccount(parsed, r, plat, p.accountExtractionHeaders(parsed, plat))
}

func resolveReverseAccount(
	parsed *parsedPath,
	r *http.Request,
	plat *platform.Platform,
	headers []string,
) (string, platform.ReverseProxyEmptyAccountBehavior, bool) {
	behavior := effectiveEmptyAccountBehavior(plat)
	if headerAccount := accountOverrideHeader(r); headerAccount != "" {
		return headerAccount, behavior, false
	}

	account := ""
	if parsed != nil {
		account = parsed.Account
	}
	if account != "" {
		return account, behavior, false
	}
	if r == nil {
		return account, behavior, behaviorRequiresAccountExtraction(behavior)
	}

	switch behavior {
	case platform.ReverseProxyEmptyAccountBehaviorRandom:
		return account, behavior, false
	case platform.ReverseProxyEmptyAccountBehaviorFixedHeader, platform.ReverseProxyEmptyAccountBehaviorAccountHeaderRule:
		if len(headers) == 0 {
			return account, behavior, true
		}
		account = extractAccountFromHeaders(r, headers)
		return account, behavior, account == ""
	}
	return account, behavior, false
}

// accountOverrideHeader returns the explicit account override of a reverse
// proxy request. The Prism header wins when both spellings are present, and
// X-Resin-Account keeps upstream Resin clients working (deviation X4).
func accountOverrideHeader(r *http.Request) string {
	if r == nil {
		return ""
	}
	if account := r.Header.Get("X-Prism-Account"); account != "" {
		return account
	}
	return r.Header.Get("X-Resin-Account")
}

func (p *ReverseProxy) accountExtractionHeaders(parsed *parsedPath, plat *platform.Platform) []string {
	switch effectiveEmptyAccountBehavior(plat) {
	case platform.ReverseProxyEmptyAccountBehaviorFixedHeader:
		return fixedAccountHeadersForPlatform(plat)
	case platform.ReverseProxyEmptyAccountBehaviorAccountHeaderRule:
		if p != nil && p.matcher != nil && parsed != nil {
			return p.matcher.Match(parsed.Host, parsed.Path)
		}
	}
	return nil
}

func behaviorRequiresAccountExtraction(behavior platform.ReverseProxyEmptyAccountBehavior) bool {
	switch behavior {
	case platform.ReverseProxyEmptyAccountBehaviorFixedHeader, platform.ReverseProxyEmptyAccountBehaviorAccountHeaderRule:
		return true
	default:
		return false
	}
}

func shouldRejectReverseProxyAccountExtractionFailure(
	extractionFailed bool,
	plat *platform.Platform,
) bool {
	if !extractionFailed || plat == nil {
		return false
	}
	return platform.NormalizeReverseProxyMissAction(plat.ReverseProxyMissAction) == platform.ReverseProxyMissActionReject
}

func fixedAccountHeadersForPlatform(plat *platform.Platform) []string {
	if plat == nil {
		return nil
	}
	if len(plat.ReverseProxyFixedAccountHeaders) > 0 {
		return append([]string(nil), plat.ReverseProxyFixedAccountHeaders...)
	}
	if plat.ReverseProxyFixedAccountHeader == "" {
		return nil
	}
	_, headers, err := platform.NormalizeFixedAccountHeaders(plat.ReverseProxyFixedAccountHeader)
	if err != nil {
		return nil
	}
	return headers
}

// isValidHost validates that the host segment is a reasonable hostname or host:port.
// Rejects empty hosts and hosts containing URL-unsafe characters.
//
// This is a syntax check only. Private, loopback and link-local targets are
// deliberately accepted: when a request is routed through a node the target is
// reached from that node's network, so it is not a local SSRF surface. Local
// direct (bypass) targets can be restricted separately through
// PRISM_DIRECT_DENY_PRIVATE.
func isValidHost(host string) bool {
	if host == "" {
		return false
	}
	// Reject hosts with obviously invalid characters and userinfo marker.
	if strings.ContainsAny(host, "/ \t\n\r@") {
		return false
	}
	// Unbracketed multi-colon literals are ambiguous in URL host syntax.
	// Require bracket form for IPv6 when used in host[:port].
	if strings.Count(host, ":") > 1 && !strings.HasPrefix(host, "[") {
		return false
	}

	u, err := url.Parse("http://" + host)
	if err != nil {
		return false
	}
	// Host segment must be a plain host[:port] without userinfo/path/query.
	if u.User != nil || u.Host == "" || u.Host != host {
		return false
	}
	return u.Hostname() != ""
}
