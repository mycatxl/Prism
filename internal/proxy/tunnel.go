package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"runtime/debug"
	"sync"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	"prism/internal/keys"
	"prism/internal/netutil"
	"prism/internal/outbound"
	"prism/internal/routing"
)

type tunnelDeps struct {
	router      *routing.Router
	pool        outbound.PoolAccessor
	health      HealthRecorder
	metricsSink MetricsEventSink
	// metering attributes this tunnel's bytes to the access key that opened
	// it. Nil disables the accounting and leaves the global sink in charge.
	metering    MeteringSink
	bypass      *TargetBypassMatcher
	directGuard DirectDialGuard
	// dialTimeout bounds the local direct dial (PRISM_PROXY_TRANSPORT_DIAL_TIMEOUT).
	// Zero falls back to the guard default.
	dialTimeout time.Duration
}

type preparedTunnel struct {
	upstreamConn net.Conn
	recordResult func(bool)
}

type tunnelPrepareResult struct {
	route         routing.RouteResult
	session       *preparedTunnel
	proxyErr      *ProxyError
	upstreamStage string
	upstreamErr   error
	canceled      bool
}

type tunnelRelayResult struct {
	ingressBytes int64
	egressBytes  int64
	netOK        bool
	// quotaAborted says the relay was ended on purpose because the key's
	// allowance ran out. It is not a node failure, so the caller must not
	// record a health result for it.
	quotaAborted  bool
	proxyErr      *ProxyError
	upstreamStage string
	upstreamErr   error
}

type tunnelPumpOptions struct {
	onFirstIngressByte func()
	// quotaGate, quotaKey and quotaGracePercent enable the mid-stream
	// allowance check: a tunnel that carries more than the key may move is
	// closed by both ends rather than allowed to finish. A nil gate, or a
	// session with no key, leaves the relay unenforced, exactly as it was
	// before quotas existed.
	quotaGate         QuotaGate
	quotaKey          *keys.KeyState
	quotaGracePercent int
}

type firstByteReader struct {
	reader      io.Reader
	onFirstByte func()
	once        sync.Once
}

func (r *firstByteReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 && r.onFirstByte != nil {
		r.once.Do(r.onFirstByte)
	}
	return n, err
}

func prepareConnectTunnel(
	ctx context.Context,
	deps tunnelDeps,
	platformName string,
	account string,
	target string,
	key *keys.KeyState,
) tunnelPrepareResult {
	if deps.bypass != nil && deps.bypass.ShouldBypass(target) {
		// PRISM_DIRECT_DENY_PRIVATE governs this local dial path too: the
		// CONNECT tunnel and the SOCKS5 inbound share it.
		if err := deps.directGuard.CheckTarget(ctx, target); err != nil {
			return tunnelPrepareResult{proxyErr: ErrDirectTargetDenied}
		}
		// The bypass path moves the user's bytes just as the routed one does,
		// so it is metered too. Its platform id is empty: no platform was
		// resolved, and the traffic is still the key's.
		return prepareDirectConnectTunnel(ctx, deps, target, attributionFor(deps.metering, key, ""))
	}

	routed, routeErr := resolveRoutedOutbound(deps.router, deps.pool, platformName, account, target, key)
	if routeErr != nil {
		return tunnelPrepareResult{proxyErr: routeErr}
	}

	domain := netutil.ExtractDomain(target)
	nodeHashRaw := routed.Route.NodeHash
	if deps.health != nil {
		go deps.health.RecordLatency(nodeHashRaw, domain, nil)
	}

	rawConn, err := routed.Outbound.DialContext(ctx, "tcp", M.ParseSocksaddr(target))
	if err != nil {
		proxyErr := classifyConnectError(err)
		if proxyErr == nil {
			return tunnelPrepareResult{
				route:    routed.Route,
				canceled: true,
			}
		}
		if deps.health != nil {
			recordPassiveResultAsync(deps.health, routed.Route, false)
		}
		return tunnelPrepareResult{
			route:         routed.Route,
			proxyErr:      proxyErr,
			upstreamStage: "connect_dial",
			upstreamErr:   err,
		}
	}

	recordResult := func(ok bool) {
		if deps.health != nil {
			recordPassiveResultAsync(deps.health, routed.Route, ok)
		}
	}

	// The bytes of this tunnel belong to one key. The platform comes from the
	// route, which has already been resolved and checked.
	attributed := attributionFor(deps.metering, key, routed.Route.PlatformID)

	upstreamBase := rawConn
	if deps.metricsSink != nil || attributed != nil {
		if deps.metricsSink != nil {
			deps.metricsSink.OnConnectionLifecycle(ConnectionOutbound, ConnectionOpen)
		}
		// The tunnel's upstream connection is dialled here and belongs to this
		// session alone, so the bytes can be attributed to the key as they
		// flow. This is the only path where that is true: the pooled HTTP
		// transports reuse connections across requests, and attributing one of
		// those would bill the next request's bytes to the wrong tenant.
		upstreamBase = newAttributedCountingConn(rawConn, deps.metricsSink, attributed)
	}

	upstreamConn := newTLSLatencyConn(upstreamBase, func(latency time.Duration) {
		if deps.health != nil {
			deps.health.RecordLatency(nodeHashRaw, domain, &latency)
		}
	})

	return tunnelPrepareResult{
		route: routed.Route,
		session: &preparedTunnel{
			upstreamConn: upstreamConn,
			recordResult: recordResult,
		},
	}
}

// attributionFor builds the metering identity of one tunnel.
//
// It answers nil when there is nothing to attribute to: no metering sink, or a
// request that carried no key (the transitional global-token path). A nil
// result leaves the connection reporting only to the global metrics sink,
// which is exactly the behaviour before access keys existed.
func attributionFor(metering MeteringSink, key *keys.KeyState, platformID string) *keyAttribution {
	if metering == nil || key == nil || key.KeyID == "" {
		return nil
	}
	return &keyAttribution{
		metering:   metering,
		keyID:      key.KeyID,
		userID:     key.UserID,
		platformID: platformID,
	}
}

func prepareDirectConnectTunnel(
	ctx context.Context,
	deps tunnelDeps,
	target string,
	attributed *keyAttribution,
) tunnelPrepareResult {
	// The dial-time half of the guard runs inside this dialer, so the address
	// connected to is validated even after a re-resolution.
	dialer := deps.directGuard.dialerWithTimeout(deps.dialTimeout)
	rawConn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		proxyErr := classifyConnectError(err)
		if proxyErr == nil {
			return tunnelPrepareResult{canceled: true}
		}
		return tunnelPrepareResult{
			proxyErr:      proxyErr,
			upstreamStage: "connect_direct_dial",
			upstreamErr:   err,
		}
	}

	upstreamConn := rawConn
	if deps.metricsSink != nil || attributed != nil {
		if deps.metricsSink != nil {
			deps.metricsSink.OnConnectionLifecycle(ConnectionOutbound, ConnectionOpen)
		}
		upstreamConn = newAttributedCountingConn(rawConn, deps.metricsSink, attributed)
	}
	return tunnelPrepareResult{
		session: &preparedTunnel{
			upstreamConn: upstreamConn,
			recordResult: func(bool) {},
		},
	}
}

func pumpPreparedTunnel(
	clientConn net.Conn,
	clientReader *bufio.Reader,
	session *preparedTunnel,
	opts tunnelPumpOptions,
) tunnelRelayResult {
	clientToUpstream, err := makeTunnelClientReader(clientConn, clientReader)
	if err != nil {
		if session != nil && session.upstreamConn != nil {
			_ = session.upstreamConn.Close()
		}
		if clientConn != nil {
			_ = clientConn.Close()
		}
		return tunnelRelayResult{
			proxyErr:      ErrUpstreamRequestFailed,
			upstreamStage: "connect_client_prefetch_drain",
			upstreamErr:   err,
		}
	}
	return pumpPreparedTunnelReader(clientConn, clientToUpstream, session, opts)
}

func pumpPreparedTunnelReader(
	clientConn net.Conn,
	clientToUpstream io.Reader,
	session *preparedTunnel,
	opts tunnelPumpOptions,
) tunnelRelayResult {
	if clientConn == nil || clientToUpstream == nil || session == nil || session.upstreamConn == nil {
		return tunnelRelayResult{}
	}

	type copyResult struct {
		n   int64
		err error
	}
	var closeBothOnce sync.Once
	closeBoth := func() {
		closeBothOnce.Do(func() {
			_ = clientConn.Close()
			_ = session.upstreamConn.Close()
		})
	}
	// relay runs one direction of the copy on its own goroutine and always
	// reports a result. A panic ends that direction, closes both ends so the
	// peer is not left waiting, and is reported as a copy error: the relay below
	// waits on these two channels, so a goroutine that died without sending
	// would leave the tunnel parked forever as well.
	relay := func(results chan<- copyResult, copyFn func() (int64, error)) {
		go func() {
			var n int64
			var err error
			defer func() {
				if recovered := recover(); recovered != nil {
					log.Printf("proxy: recovered a panic relaying a tunnel: %v\n%s", recovered, debug.Stack())
					closeBoth()
					if err == nil {
						err = fmt.Errorf("relay panicked: %v", recovered)
					}
				}
				results <- copyResult{n: n, err: err}
			}()
			n, err = copyFn()
		}()
	}
	// The allowance is enforced from inside the relay. Both directions count
	// against the same allowance, which is why one quota covers the two copies,
	// and the abort closes both ends - a cut that left one of them open would
	// leave the peer waiting on a socket nobody reads.
	quota := newStreamQuota(opts.quotaGate, opts.quotaKey, opts.quotaGracePercent, closeBoth)

	ingressBytesCh := make(chan copyResult, 1)
	egressBytesCh := make(chan copyResult, 1)
	// Both directions run on their own goroutine, and neither may take the
	// process down: a panic in a copy loop is one broken tunnel, and the relay
	// below waits on these two channels for the byte counts, so a goroutine that
	// died without sending would leave the tunnel parked forever as well.
	relay(egressBytesCh, func() (int64, error) {
		clientReader := io.Reader(clientToUpstream)
		if quota != nil {
			clientReader = &quotaReader{reader: clientToUpstream, quota: quota}
		}
		n, copyErr := io.Copy(session.upstreamConn, clientReader)
		if !isBenignTunnelCopyError(copyErr) || !closeWriteConn(session.upstreamConn) {
			closeBoth()
		}
		return n, copyErr
	})
	relay(ingressBytesCh, func() (int64, error) {
		var upstreamReader io.Reader = session.upstreamConn
		if quota != nil {
			upstreamReader = &quotaReader{reader: upstreamReader, quota: quota, ingress: true}
		}
		if opts.onFirstIngressByte != nil {
			// 隧道首字耗时以目标站点返回的第一批字节为准，而不是 CONNECT/SOCKS 握手完成。
			upstreamReader = &firstByteReader{reader: upstreamReader, onFirstByte: opts.onFirstIngressByte}
		}
		n, copyErr := io.Copy(clientConn, upstreamReader)
		if !isBenignTunnelCopyError(copyErr) || !closeWriteConn(clientConn) {
			closeBoth()
		}
		return n, copyErr
	})

	ingressResult := <-ingressBytesCh
	egressResult := <-egressBytesCh
	closeBoth()

	// A relay cut for the key's allowance is not a network failure: the tunnel
	// was ended on purpose, and blaming the node would take a healthy one out of
	// rotation because a user ran out of traffic.
	if quota.Exceeded() {
		return tunnelRelayResult{
			ingressBytes: ingressResult.n,
			egressBytes:  egressResult.n,
			netOK:        true,
			quotaAborted: true,
			proxyErr:     ErrQuotaExceeded,
		}
	}

	ingressErrBenign := isBenignTunnelCopyError(ingressResult.err)
	egressErrBenign := isBenignTunnelCopyError(egressResult.err)
	// A client-side TCP reset after the upstream response has already started is
	// a shutdown artifact, not an upstream failure. This commonly happens when a
	// tunnel client exits immediately after consuming the response.
	if !egressErrBenign && ingressResult.n > 0 && isClientReadResetError(egressResult.err) {
		egressErrBenign = true
	}
	result := tunnelRelayResult{
		ingressBytes: ingressResult.n,
		egressBytes:  egressResult.n,
		netOK:        true,
	}
	// Zero or one-way traffic is not a node failure: whether the tunnel carries
	// bytes is decided by the client and the target, not by node quality. The
	// SOCKS5 inbound has always behaved this way; CONNECT now matches it. Only
	// a real copy error below marks the relay as failed.
	switch {
	case !ingressErrBenign:
		result.netOK = false
		result.proxyErr = ErrUpstreamRequestFailed
		result.upstreamStage = "connect_upstream_to_client_copy"
		result.upstreamErr = ingressResult.err
	case !egressErrBenign:
		result.netOK = false
		result.proxyErr = ErrUpstreamRequestFailed
		result.upstreamStage = "connect_client_to_upstream_copy"
		result.upstreamErr = egressResult.err
	}
	return result
}

func closeWriteConn(conn net.Conn) bool {
	return closeWriteErr(conn) == nil
}

// makeTunnelClientReader returns a reader for client->upstream copy that
// preserves any bytes already buffered by a protocol reader before tunneling.
func makeTunnelClientReader(clientConn net.Conn, buffered *bufio.Reader) (io.Reader, error) {
	if buffered == nil {
		return clientConn, nil
	}
	n := buffered.Buffered()
	if n == 0 {
		return clientConn, nil
	}
	prefetched := make([]byte, n)
	if _, err := io.ReadFull(buffered, prefetched); err != nil {
		return nil, err
	}
	return io.MultiReader(bytes.NewReader(prefetched), clientConn), nil
}
