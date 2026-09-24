package node

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestClassifyProbeError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ProbeErrorClass
	}{
		{"nil", nil, ProbeErrorNone},
		{"empty message", errors.New(""), ProbeErrorOther},
		{"bad port range", errors.New("parse config: bad port range 1000-900"), ProbeErrorBadPortRange},
		{"x509", errors.New("x509: certificate signed by unknown authority"), ProbeErrorTLSCertificate},
		{"certificate marker", errors.New("tls: failed to verify certificate"), ProbeErrorTLSCertificate},
		{"handshake", errors.New("remote error: tls: handshake failure"), ProbeErrorTLSHandshake},
		{"no route", errors.New("dial tcp: no route to host"), ProbeErrorNoRoute},
		{"unreachable", errors.New("dial tcp: network is unreachable"), ProbeErrorUnreachable},
		{"connection refused", errors.New("dial tcp 127.0.0.1:443: connect: connection refused"), ProbeErrorRefused},
		{"refused mixed case", errors.New("Connection Refused"), ProbeErrorRefused},
		{"connection reset", errors.New("read tcp: connection reset by peer"), ProbeErrorReset},
		{"timeout", errors.New("i/o timeout"), ProbeErrorTimeout},
		{"deadline", errors.New("context deadline exceeded"), ProbeErrorTimeout},
		{"eof", errors.New("unexpected EOF"), ProbeErrorEOF},
		{"authentication", errors.New("authentication failed"), ProbeErrorAuth},
		{"unauthorized", errors.New("401 unauthorized"), ProbeErrorAuth},
		{"other", errors.New("write: broken pipe"), ProbeErrorOther},
		// Marker precedence is part of the contract.
		{"certificate before handshake", errors.New("tls handshake failure: certificate expired"), ProbeErrorTLSCertificate},
		{"handshake before timeout", errors.New("handshake timeout"), ProbeErrorTLSHandshake},
		{"no route before unreachable", errors.New("connect: no route to host, host unreachable"), ProbeErrorNoRoute},
		{"refused before timeout", errors.New("connection refused (timeout)"), ProbeErrorRefused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyProbeError(tc.err); got != tc.want {
				t.Fatalf("ClassifyProbeError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestProbeErrorClassTokensAreDistinct guards the stable log/API tokens of
// every class. ProbeErrorBuild is not produced by ClassifyProbeError (build
// failures are classified by the outbound builder), so only the tokens are
// asserted here.
func TestProbeErrorClassTokensAreDistinct(t *testing.T) {
	classes := []ProbeErrorClass{
		ProbeErrorNone, ProbeErrorBuild, ProbeErrorBadPortRange, ProbeErrorTLSCertificate,
		ProbeErrorTLSHandshake, ProbeErrorTimeout, ProbeErrorRefused, ProbeErrorReset,
		ProbeErrorNoRoute, ProbeErrorUnreachable, ProbeErrorEOF, ProbeErrorAuth,
		ProbeErrorParse, ProbeErrorOther,
	}
	seen := make(map[ProbeErrorClass]bool, len(classes))
	for _, c := range classes {
		if c == ProbeErrorNone {
			continue
		}
		if seen[c] {
			t.Fatalf("duplicate probe error class token %q", c)
		}
		seen[c] = true
	}
	if got := ProbeErrorNone.String(); got != "" {
		t.Fatalf("ProbeErrorNone.String() = %q, want empty", got)
	}
	if got := ProbeErrorBuild.String(); got != "BUILD_FAILED" {
		t.Fatalf("ProbeErrorBuild.String() = %q, want BUILD_FAILED", got)
	}
}

func TestBoundedProbeErrorDetail(t *testing.T) {
	if got := BoundedProbeErrorDetail(nil); got != "" {
		t.Fatalf("nil error: got %q, want empty", got)
	}
	if got := BoundedProbeErrorDetail(errors.New("  dial \t tcp\n\n\n failed  ")); got != "dial tcp failed" {
		t.Fatalf("whitespace collapse: got %q, want %q", got, "dial tcp failed")
	}

	long := BoundedProbeErrorDetail(errors.New(strings.Repeat("x", 500)))
	if len(long) != 240 {
		t.Fatalf("long detail: got len %d, want 240", len(long))
	}
	if long != strings.Repeat("x", 240) {
		t.Fatal("long detail: unexpected truncated content")
	}

	// Multi-byte runes must never be cut in half.
	multi := BoundedProbeErrorDetail(errors.New("x" + strings.Repeat("é", 300)))
	if len(multi) > 240 {
		t.Fatalf("multi-byte detail: got len %d, want <= 240", len(multi))
	}
	if !utf8.ValidString(multi) {
		t.Fatal("multi-byte detail must stay valid UTF-8 after truncation")
	}
}

func TestNodeEntry_ProbeFailureRoundTrip(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)

	if class, detail, at := e.GetProbeFailure(); class != ProbeErrorNone || detail != "" || !at.IsZero() {
		t.Fatalf("zero value: got (%q, %q, %v)", class, detail, at)
	}

	recorded := time.Unix(1700000000, 123456789).UTC()
	e.SetProbeFailure(ProbeErrorTimeout, "i/o timeout", recorded)
	class, detail, at := e.GetProbeFailure()
	if class != ProbeErrorTimeout || detail != "i/o timeout" {
		t.Fatalf("round trip: got (%q, %q)", class, detail)
	}
	if at.UnixNano() != recorded.UnixNano() {
		t.Fatalf("round trip timestamp: got %v, want %v", at, recorded)
	}

	// A zero timestamp must still yield a usable record.
	e.SetProbeFailure(ProbeErrorReset, "connection reset by peer", time.Time{})
	class, detail, at = e.GetProbeFailure()
	if class != ProbeErrorReset || detail != "connection reset by peer" || at.IsZero() {
		t.Fatalf("zero timestamp: got (%q, %q, %v)", class, detail, at)
	}

	// Probe-failure bookkeeping is independent of LastError (build failures).
	e.SetLastError("build failed")
	e.SetProbeFailure(ProbeErrorEOF, "unexpected EOF", time.Time{})
	if got := e.GetLastError(); got != "build failed" {
		t.Fatalf("LastError must be untouched by SetProbeFailure, got %q", got)
	}
	e.ClearProbeFailure()
	if class, detail, at := e.GetProbeFailure(); class != ProbeErrorNone || detail != "" || !at.IsZero() {
		t.Fatalf("cleared: got (%q, %q, %v)", class, detail, at)
	}
	if got := e.GetLastError(); got != "build failed" {
		t.Fatalf("LastError must be untouched by ClearProbeFailure, got %q", got)
	}

	// A None class or an empty detail clears instead of storing a half record.
	e.SetProbeFailure(ProbeErrorTimeout, "t", time.Time{})
	e.SetProbeFailure(ProbeErrorNone, "t", time.Time{})
	if class, _, _ := e.GetProbeFailure(); class != ProbeErrorNone {
		t.Fatalf("ProbeErrorNone must clear, got %q", class)
	}
	e.SetProbeFailure(ProbeErrorTimeout, "t", time.Time{})
	e.SetProbeFailure(ProbeErrorTimeout, "", time.Time{})
	if class, _, _ := e.GetProbeFailure(); class != ProbeErrorNone {
		t.Fatalf("empty detail must clear, got %q", class)
	}
}
