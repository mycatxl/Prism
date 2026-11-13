package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/keys"
	"prism/internal/usage"
)

type lateCountingConn struct {
	stubConn
	entered chan struct{}
	release chan struct{}
}

type shortUpstreamConn struct {
	stubConn
	accepted int
}

func (c *shortUpstreamConn) Write(p []byte) (int, error) {
	return min(c.accepted, len(p)), io.ErrClosedPipe
}

func TestTunnelRefundsBytesRejectedByUpstream(t *testing.T) {
	for _, accepted := range []int{0, 3} {
		t.Run(fmt.Sprint(accepted), func(t *testing.T) {
			counters := usage.NewCounters()
			gate := &usage.Gate{Limiter: usage.NewLimiter(counters)}
			key := &keys.KeyState{KeyID: "key", UserID: "tenant", Plan: keys.PlanSnapshot{TrafficBytes: 100}}
			upstream := &shortUpstreamConn{accepted: accepted}
			client := &stubConn{}
			result := pumpPreparedTunnelReader(client, strings.NewReader("payload"), &preparedTunnel{upstreamConn: upstream, recordResult: func(bool) {}}, tunnelPumpOptions{quotaGate: gate, quotaKey: key})
			if result.egressBytes != int64(accepted) || counters.UserBytes("tenant") != int64(accepted) {
				t.Fatalf("accepted=%d relay=%d billed=%d", accepted, result.egressBytes, counters.UserBytes("tenant"))
			}
		})
	}
}

func (c *lateCountingConn) Write(p []byte) (int, error) {
	close(c.entered)
	<-c.release
	return len(p), nil
}

func TestCountingConnBillsIOCompletingAfterClose(t *testing.T) {
	base := &lateCountingConn{entered: make(chan struct{}), release: make(chan struct{})}
	counters := usage.NewCounters()
	conn := newAttributedCountingConn(base, nil, &keyAttribution{metering: &usage.Sink{Counters: counters}, keyID: "key", userID: "user"})
	done := make(chan struct{})
	go func() { defer close(done); _, _ = conn.Write([]byte("payload")) }()
	<-base.entered
	_ = conn.Close()
	close(base.release)
	<-done
	if n := counters.UserBytes("user"); n != 7 {
		t.Fatalf("late traffic = %d, want 7", n)
	}
}

func TestUpgradeRelayFlushesSmallTraffic(t *testing.T) {
	client, peer := net.Pipe()
	upstream, origin := net.Pipe()
	defer peer.Close()
	defer origin.Close()
	var ingress, egress atomic.Int64
	done := make(chan tunnelRelayResult, 1)
	go func() {
		done <- pumpPreparedTunnelReader(client, client, &preparedTunnel{upstreamConn: upstream, recordResult: func(bool) {}}, tunnelPumpOptions{
			quotaGate: &countingGate{}, quotaKey: workingKeyState(),
			liveIngressReport: func(n int64) { ingress.Add(n) }, liveEgressReport: func(n int64) { egress.Add(n) },
		})
	}()
	go func() {
		buf := make([]byte, 4)
		_, _ = io.ReadFull(origin, buf)
		_, _ = origin.Write([]byte("pong"))
		_ = origin.Close()
	}()
	_, _ = peer.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(peer, buf); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay did not close")
	}
	if ingress.Load() != 4 || egress.Load() != 4 {
		t.Fatalf("small upgrade billed %d/%d, want 4/4", ingress.Load(), egress.Load())
	}
}

func TestUpstreamErrorsAndStandardLoggerOmitUntrustedText(t *testing.T) {
	for _, secret := range []string{"Authorization: Bearer private-token", "pk_key_private", "SELECT password_hash FROM users", "open /private/database.db"} {
		if got := summarizeUpstreamError(errors.New(secret)); strings.Contains(got.Message, secret) {
			t.Fatalf("unsafe summary: %+v", got)
		}
		if got := sanitizeUpstreamErrMsg(secret); strings.Contains(got, secret) {
			t.Fatalf("unsafe persisted error: %s", got)
		}
	}
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	_, _ = (upstreamErrorLogWriter{}).Write([]byte("malformed trailer Authorization: Bearer private-token"))
	if strings.Contains(output.String(), "private-token") || output.Len() == 0 {
		t.Fatal("unsafe or missing structural diagnostic")
	}
}
