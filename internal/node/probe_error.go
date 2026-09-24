package node

import (
	"strings"
	"unicode/utf8"
)

// maxProbeErrorDetailBytes bounds the probe-failure detail stored on a node
// entry and emitted in logs. Network and TLS stacks can embed large blobs
// (whole handshake transcripts, config dumps) into error strings, so the
// detail must never grow unbounded.
const maxProbeErrorDetailBytes = 240

// ProbeErrorClass names the coarse reason why an ACTIVE probe attempt (egress
// or latency) failed. Values are stable machine-readable tokens that end up in
// logs.
//
// Classification is purely marker-based: node credentials (password, UUID,
// PSK, subscription token, ...) are never inspected, extracted or copied into
// a class value or into a detail string, so recording a probe failure can
// never leak a secret.
type ProbeErrorClass string

const (
	// ProbeErrorNone means "no probe failure recorded".
	ProbeErrorNone ProbeErrorClass = ""
	// ProbeErrorBuild means the node could not be built into an outbound.
	ProbeErrorBuild ProbeErrorClass = "BUILD_FAILED"
	// ProbeErrorBadPortRange means the node config carries an invalid port range.
	ProbeErrorBadPortRange ProbeErrorClass = "CONFIG_BAD_PORT_RANGE"
	// ProbeErrorTLSCertificate means certificate/x509 verification failed.
	ProbeErrorTLSCertificate ProbeErrorClass = "TLS_CERTIFICATE"
	// ProbeErrorTLSHandshake means the TLS handshake itself failed.
	ProbeErrorTLSHandshake ProbeErrorClass = "TLS_HANDSHAKE"
	// ProbeErrorTimeout means the attempt timed out or hit a deadline.
	ProbeErrorTimeout ProbeErrorClass = "TIMEOUT"
	// ProbeErrorRefused means the remote endpoint refused the connection.
	ProbeErrorRefused ProbeErrorClass = "REFUSED"
	// ProbeErrorReset means the connection was reset by the peer.
	ProbeErrorReset ProbeErrorClass = "RESET"
	// ProbeErrorNoRoute means there is no route to the host.
	ProbeErrorNoRoute ProbeErrorClass = "NO_ROUTE"
	// ProbeErrorUnreachable means the host or network is unreachable.
	ProbeErrorUnreachable ProbeErrorClass = "UNREACHABLE"
	// ProbeErrorEOF means the peer closed the stream early.
	ProbeErrorEOF ProbeErrorClass = "EOF"
	// ProbeErrorAuth means authentication/authorization was rejected.
	ProbeErrorAuth ProbeErrorClass = "AUTH"
	// ProbeErrorParse means the probe response body could not be parsed.
	ProbeErrorParse ProbeErrorClass = "PARSE_FAILED"
	// ProbeErrorOther is any failure the fixed markers do not recognize.
	ProbeErrorOther ProbeErrorClass = "OTHER"
)

// String returns the stable token of the class; ProbeErrorNone renders as "".
func (c ProbeErrorClass) String() string {
	return string(c)
}

// ClassifyProbeError maps an error onto a coarse ProbeErrorClass by matching a
// FIXED, ordered list of lowercase marker substrings against err.Error().
//
// The marker list is intentionally closed: it never inspects credentials and
// never returns any part of the error text, so it is safe to call on errors
// produced from node configs. nil maps to ProbeErrorNone; every non-nil error
// that matches no marker (including an empty message) maps to ProbeErrorOther.
func ClassifyProbeError(err error) ProbeErrorClass {
	if err == nil {
		return ProbeErrorNone
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "bad port range"):
		return ProbeErrorBadPortRange
	case strings.Contains(msg, "certificate"), strings.Contains(msg, "x509"):
		return ProbeErrorTLSCertificate
	case strings.Contains(msg, "handshake"):
		return ProbeErrorTLSHandshake
	case strings.Contains(msg, "no route"):
		return ProbeErrorNoRoute
	case strings.Contains(msg, "unreachable"):
		return ProbeErrorUnreachable
	case strings.Contains(msg, "connection refused"):
		return ProbeErrorRefused
	case strings.Contains(msg, "connection reset"):
		return ProbeErrorReset
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline"):
		return ProbeErrorTimeout
	case strings.Contains(msg, "eof"):
		return ProbeErrorEOF
	case strings.Contains(msg, "authentication"), strings.Contains(msg, "unauthorized"):
		return ProbeErrorAuth
	default:
		return ProbeErrorOther
	}
}

// BoundedProbeErrorDetail renders err for storage and logging: runs of
// whitespace collapse into single spaces, leading/trailing whitespace is
// trimmed, and the result is cut at maxProbeErrorDetailBytes on a rune
// boundary (so the returned string is always valid UTF-8). nil yields "".
func BoundedProbeErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if len(msg) <= maxProbeErrorDetailBytes {
		return msg
	}
	cut := maxProbeErrorDetailBytes
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut]
}
