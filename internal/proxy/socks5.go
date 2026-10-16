package proxy

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"prism/internal/keys"
	"prism/internal/outbound"
	"prism/internal/routing"
)

const (
	socks5Version                   = 0x05
	socks5MethodNoAuth              = 0x00
	socks5MethodUserPass            = 0x02
	socks5MethodNoAcceptable        = 0xFF
	socks5CommandConnect            = 0x01
	socks5AddressTypeIPv4           = 0x01
	socks5AddressTypeDomain         = 0x03
	socks5AddressTypeIPv6           = 0x04
	socks5ReplySucceeded            = 0x00
	socks5ReplyGeneralFailure       = 0x01
	socks5ReplyConnectionNotAllowed = 0x02
	socks5ReplyCommandUnsupported   = 0x07
	socks5ReplyAddressUnsupported   = 0x08
	socks5UserPassVersion           = 0x01
	socks5UserPassStatusSuccess     = 0x00
	socks5UserPassStatusFailure     = 0x01
)

var socks5HandshakeTimeout = 15 * time.Second

// Socks5InboundConfig holds dependencies for the SOCKS5 inbound handler.
type Socks5InboundConfig struct {
	ProxyToken string
	// AccessKeys resolves an access key sent in the RFC 1929 password field.
	// It may be nil or unconfigured: the password then falls back to the
	// global proxy token, which is the transitional behaviour.
	AccessKeys       AccessKeyVerifier
	Router           *routing.Router
	Pool             outbound.PoolAccessor
	Health           HealthRecorder
	Events           EventEmitter
	MetricsSink      MetricsEventSink
	ProxyBypassRules []string
	// DirectDenyPrivate enables the local dial guard (PRISM_DIRECT_DENY_PRIVATE)
	// on this inbound's local direct branch, keeping Resin behaviour when false.
	DirectDenyPrivate bool

	// BlockedDestinationPorts lists the destination ports this inbound's local
	// direct branch refuses (PRISM_BLOCKED_DESTINATION_PORTS). It is independent
	// of DirectDenyPrivate: clearing that switch never reopens a blocked port.
	BlockedDestinationPorts []int
	// DialTimeout bounds the local direct dial
	// (PRISM_PROXY_TRANSPORT_DIAL_TIMEOUT). Zero uses the guard default.
	DialTimeout time.Duration
	// Metering attributes this inbound's traffic to the access key that caused
	// it. Nil disables the accounting.
	Metering MeteringSink
	// Quota decides whether a request may proceed. Nil allows everything.
	Quota QuotaGate
	// QuotaGracePercent is the tolerance a live tunnel gets before it is cut
	// for running past the key's allowance, in percent of the limit. Zero
	// applies DefaultStreamGracePercent; a negative value means no tolerance
	// at all.
	QuotaGracePercent int
	// Sessions holds this inbound's live sessions per key, so a revoked key's
	// sessions can be ended. Nil leaves revocation to the next request.
	Sessions SessionTracker
	// QuotaLogf reports a session the gate refused. A refusal happens before a
	// request log entry exists, so it would otherwise leave no trace at all;
	// the line names the key id and the reason and nothing else. Nil keeps a
	// refusal silent, which is what a deployment without plans gets.
	QuotaLogf func(format string, args ...any)
}

// Socks5Inbound implements SOCKS5 CONNECT over a raw TCP connection.
type Socks5Inbound struct {
	token      string
	accessKeys AccessKeyVerifier
	tunnel     tunnelDeps
	events     EventEmitter
	quota      QuotaGate
	// quotaGracePercent is QuotaGracePercent, resolved once at construction.
	quotaGracePercent int
	quotaLogf         func(format string, args ...any)
	sessions          SessionTracker
}

type socks5HandshakeResult struct {
	platformName string
	account      string
	target       string
	key          *keys.KeyState
	ok           bool
}

// NewSocks5Inbound creates a new SOCKS5 inbound handler.
func NewSocks5Inbound(cfg Socks5InboundConfig) *Socks5Inbound {
	ev := cfg.Events
	if ev == nil {
		ev = NoOpEventEmitter{}
	}
	return &Socks5Inbound{
		token:      cfg.ProxyToken,
		accessKeys: cfg.AccessKeys,
		tunnel: tunnelDeps{
			router:      cfg.Router,
			pool:        cfg.Pool,
			health:      cfg.Health,
			metricsSink: cfg.MetricsSink,
			metering:    cfg.Metering,
			bypass:      NewTargetBypassMatcher(cfg.ProxyBypassRules),
			directGuard: NewDirectDialGuard(cfg.DirectDenyPrivate).WithBlockedDestinationPorts(cfg.BlockedDestinationPorts),
			dialTimeout: cfg.DialTimeout,
		},
		events:            ev,
		quota:             cfg.Quota,
		quotaGracePercent: normalizeQuotaGracePercent(cfg.QuotaGracePercent),
		quotaLogf:         cfg.QuotaLogf,
		sessions:          cfg.Sessions,
	}
}

// ServeConn handles a SOCKS5 session on an already-accepted TCP connection.
func (s *Socks5Inbound) ServeConn(conn net.Conn) {
	s.ServeConnContext(context.Background(), conn)
}

// ServeConnContext handles a SOCKS5 session with a caller-provided base context.
func (s *Socks5Inbound) ServeConnContext(baseCtx context.Context, conn net.Conn) {
	if conn == nil {
		return
	}
	defer conn.Close()
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	reader := bufio.NewReader(conn)

	handshakeCtx, cancelHandshake := context.WithTimeout(baseCtx, socks5HandshakeTimeout)
	defer cancelHandshake()
	handshakePhase := startSocks5HandshakePhase(handshakeCtx, conn)
	defer handshakePhase.Stop()

	requireAuthInfo := InboundPolicyFromContext(baseCtx).RequireProxyAuthInfo && s.token == ""
	handshake := s.performHandshake(conn, reader, requireAuthInfo)
	if !handshake.ok {
		return
	}
	handshakePhase.Stop()

	lifecycle := newRequestLifecycleFromMetadata(
		s.events,
		conn.RemoteAddr().String(),
		"",
		ProxyTypeSocks5Forward,
		true,
	)
	lifecycle.setTarget(handshake.target, "")
	// A SOCKS5 session dials its own upstream connection and holds it for the
	// session, so the bytes are attributed as they flow.
	lifecycle.attachMeter(s.tunnel.metering, handshake.key, true)
	lifecycle.setAccount(handshake.account)
	defer lifecycle.finish()

	// The allowance is checked before anything is dialled, and the check
	// reserves the session it is about to admit: a session that is being
	// established already holds its place, so a burst cannot all pass the check,
	// and a revocation can end it while it dials. The accepted connection is
	// attached to the reservation once the tunnel is announced.
	tunnelCtx, cancelTunnel := context.WithCancel(baseCtx)
	defer cancelTunnel()
	pending := newPendingSession(cancelTunnel)
	quotaErr, reservation := reserveSession(s.quota, s.sessions, handshake.key, pending, s.quotaLogf)
	if quotaErr != nil {
		_ = writeSocks5Reply(conn, socks5ReplyForProxyError(quotaErr), nil)
		lifecycle.setProxyError(quotaErr)
		lifecycle.setNetOK(false)
		return
	}
	defer reservation.Release()

	prepare := prepareConnectTunnel(
		tunnelCtx,
		s.tunnel,
		handshake.platformName,
		handshake.account,
		handshake.target,
		handshake.key,
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
			lifecycle.setNetOK(false)
			_ = writeSocks5Reply(conn, socks5ReplyForProxyError(prepare.proxyErr), nil)
		} else if prepare.canceled {
			lifecycle.setNetOK(true)
		}
		return
	}

	if err := writeSocks5Reply(conn, socks5ReplySucceeded, prepare.session.upstreamConn.LocalAddr()); err != nil {
		prepare.session.upstreamConn.Close()
		lifecycle.setProxyError(ErrUpstreamRequestFailed)
		lifecycle.setUpstreamError("socks5_connect_response_write", err)
		lifecycle.setNetOK(false)
		return
	}

	// The tunnel is announced: the accepted connection is attached to the
	// reservation that was registered before the dial, so the key's count never
	// dipped while the session was being established, and a revoked key still
	// ends it by closing the connection the client holds.
	pending.attach(conn)
	relay := pumpPreparedTunnel(conn, reader, prepare.session, tunnelPumpOptions{
		onFirstIngressByte: lifecycle.markFirstByteReceived,
		quotaGate:          s.quota,
		quotaKey:           handshake.key,
		quotaGracePercent:  s.quotaGracePercent,
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

// socks5ReplyForProxyError maps a proxy error onto the closest SOCKS5 reply
// code. A refused local direct target is "connection not allowed by ruleset"
// (RFC 1928), not a generic failure.
func socks5ReplyForProxyError(proxyErr *ProxyError) byte {
	if proxyErr == ErrDirectTargetDenied {
		return socks5ReplyConnectionNotAllowed
	}
	return socks5ReplyGeneralFailure
}
func (s *Socks5Inbound) performHandshake(conn net.Conn, reader *bufio.Reader, requireAuthInfo bool) socks5HandshakeResult {
	method, ok := s.negotiateMethod(conn, reader, requireAuthInfo)
	if !ok {
		return socks5HandshakeResult{}
	}

	result := socks5HandshakeResult{ok: true}
	if method == socks5MethodUserPass {
		var authOK bool
		result.platformName, result.account, result.key, authOK = s.authenticateUserPass(conn, reader, requireAuthInfo)
		if !authOK {
			return socks5HandshakeResult{}
		}
	}

	target, replyCode, ok := readSocks5ConnectRequest(reader)
	if !ok {
		if replyCode != 0 {
			_ = writeSocks5Reply(conn, replyCode, nil)
		}
		return socks5HandshakeResult{}
	}

	result.target = target
	return result
}

type socks5HandshakePhase struct {
	conn     net.Conn
	stopCh   chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func startSocks5HandshakePhase(baseCtx context.Context, conn net.Conn) *socks5HandshakePhase {
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	phase := &socks5HandshakePhase{
		conn:   conn,
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
	go phase.run(baseCtx)
	return phase
}

func (p *socks5HandshakePhase) run(baseCtx context.Context) {
	defer close(p.done)
	if p == nil || p.conn == nil {
		return
	}

	select {
	case <-p.stopCh:
		return
	case <-baseCtx.Done():
		// Interrupt any blocking handshake read/write. The handler clears these
		// deadlines before transitioning into the long-lived tunnel phase.
		_ = p.conn.SetReadDeadline(time.Now())
		_ = p.conn.SetWriteDeadline(time.Now())
	}
}

func (p *socks5HandshakePhase) Stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		if p.stopCh != nil {
			close(p.stopCh)
		}
		if p.done != nil {
			<-p.done
		}
		if p.conn != nil {
			_ = p.conn.SetReadDeadline(time.Time{})
			_ = p.conn.SetWriteDeadline(time.Time{})
		}
	})
}

func (s *Socks5Inbound) negotiateMethod(conn net.Conn, reader *bufio.Reader, requireAuthInfo bool) (byte, bool) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, false
	}
	if header[0] != socks5Version {
		return 0, false
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return 0, false
	}

	selected := byte(socks5MethodNoAcceptable)
	if s.token != "" || requireAuthInfo {
		if containsSocks5Method(methods, socks5MethodUserPass) {
			selected = socks5MethodUserPass
		}
	} else {
		switch {
		case containsSocks5Method(methods, socks5MethodUserPass):
			selected = socks5MethodUserPass
		case containsSocks5Method(methods, socks5MethodNoAuth):
			selected = socks5MethodNoAuth
		}
	}

	if _, err := conn.Write([]byte{socks5Version, selected}); err != nil {
		return 0, false
	}
	if selected == socks5MethodNoAcceptable {
		return 0, false
	}
	return selected, true
}

// authenticateUserPass performs the RFC 1929 sub-negotiation.
//
// The password field carries either an access key or the transitional global
// proxy token; the username field carries the identity, exactly as it does on
// the token path. A key-shaped password is resolved against the key store and
// never falls back to the token comparison, so a revoked key cannot
// authenticate by accident.
func (s *Socks5Inbound) authenticateUserPass(conn net.Conn, reader *bufio.Reader, requireAuthInfo bool) (string, string, *keys.KeyState, bool) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return "", "", nil, false
	}
	if header[0] != socks5UserPassVersion {
		_, _ = conn.Write([]byte{socks5UserPassVersion, socks5UserPassStatusFailure})
		return "", "", nil, false
	}

	username := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, username); err != nil {
		return "", "", nil, false
	}

	plen := []byte{0}
	if _, err := io.ReadFull(reader, plen); err != nil {
		return "", "", nil, false
	}
	password := make([]byte, int(plen[0]))
	if _, err := io.ReadFull(reader, password); err != nil {
		return "", "", nil, false
	}

	// The key, when present, lives in the password field; the identity stays in
	// the username field, so the client configuration shape is unchanged.
	//
	// A key-shaped password is resolved against the key store and never falls
	// back to the token comparison: a revoked key must be refused outright
	// rather than retried as a token.
	var keyState *keys.KeyState
	if keys.IsCredentialShape(string(password)) {
		keyState = s.verifyAccessKeyPassword(string(password))
		if keyState == nil {
			_, _ = conn.Write([]byte{socks5UserPassVersion, socks5UserPassStatusFailure})
			return "", "", nil, false
		}
	} else if s.token != "" && subtle.ConstantTimeCompare(password, []byte(s.token)) != 1 {
		_, _ = conn.Write([]byte{socks5UserPassVersion, socks5UserPassStatusFailure})
		return "", "", nil, false
	}
	if requireAuthInfo && (len(username) == 0 || len(password) == 0) {
		_, _ = conn.Write([]byte{socks5UserPassVersion, socks5UserPassStatusFailure})
		return "", "", nil, false
	}

	if _, err := conn.Write([]byte{socks5UserPassVersion, socks5UserPassStatusSuccess}); err != nil {
		return "", "", nil, false
	}

	platformName, account := parseV1PlatformAccountIdentity(string(username))
	return platformName, account, keyState, true
}

// verifyAccessKeyPassword resolves the password field as an access key. It
// answers nil when there is no verifier, when the value is not key-shaped, and
// when the key does not verify; the caller has already established the shape.
func (s *Socks5Inbound) verifyAccessKeyPassword(password string) *keys.KeyState {
	if s.accessKeys == nil {
		return nil
	}
	return s.accessKeys.VerifyCredential(password)
}

func containsSocks5Method(methods []byte, candidate byte) bool {
	for _, method := range methods {
		if method == candidate {
			return true
		}
	}
	return false
}

func readSocks5ConnectRequest(reader *bufio.Reader) (string, byte, bool) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return "", 0, false
	}
	if header[0] != socks5Version {
		return "", socks5ReplyGeneralFailure, false
	}
	if header[1] != socks5CommandConnect {
		return "", socks5ReplyCommandUnsupported, false
	}

	host, ok := readSocks5Address(reader, header[3])
	if !ok {
		if header[3] != socks5AddressTypeIPv4 && header[3] != socks5AddressTypeDomain && header[3] != socks5AddressTypeIPv6 {
			return "", socks5ReplyAddressUnsupported, false
		}
		return "", socks5ReplyGeneralFailure, false
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBuf); err != nil {
		return "", 0, false
	}
	port := strconv.Itoa(int(binary.BigEndian.Uint16(portBuf)))
	return net.JoinHostPort(host, port), 0, true
}

func readSocks5Address(reader *bufio.Reader, atyp byte) (string, bool) {
	switch atyp {
	case socks5AddressTypeIPv4:
		ip := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, ip); err != nil {
			return "", false
		}
		return net.IP(ip).String(), true
	case socks5AddressTypeIPv6:
		ip := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, ip); err != nil {
			return "", false
		}
		return net.IP(ip).String(), true
	case socks5AddressTypeDomain:
		size := []byte{0}
		if _, err := io.ReadFull(reader, size); err != nil {
			return "", false
		}
		domain := make([]byte, int(size[0]))
		if _, err := io.ReadFull(reader, domain); err != nil {
			return "", false
		}
		return string(domain), true
	default:
		return "", false
	}
}

func writeSocks5Reply(w io.Writer, replyCode byte, addr net.Addr) error {
	atyp := byte(socks5AddressTypeIPv4)
	hostBytes := []byte{0, 0, 0, 0}
	port := uint16(0)

	if tcpAddr, ok := addr.(*net.TCPAddr); ok && tcpAddr != nil {
		if ip4 := tcpAddr.IP.To4(); ip4 != nil {
			atyp = socks5AddressTypeIPv4
			hostBytes = append([]byte(nil), ip4...)
		} else if ip16 := tcpAddr.IP.To16(); ip16 != nil {
			atyp = socks5AddressTypeIPv6
			hostBytes = append([]byte(nil), ip16...)
		}
		if tcpAddr.Port >= 0 && tcpAddr.Port <= 65535 {
			port = uint16(tcpAddr.Port)
		}
	}

	resp := make([]byte, 0, 6+len(hostBytes))
	resp = append(resp, socks5Version, replyCode, 0x00, atyp)
	resp = append(resp, hostBytes...)
	resp = binary.BigEndian.AppendUint16(resp, port)
	_, err := w.Write(resp)
	return err
}
