package proxy

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"prism/internal/keys"
	"prism/internal/usage"
)

type payloadConn struct {
	stubHalfCloseConn
	payload io.Reader
}

func (c *payloadConn) Read(p []byte) (int, error) { return c.payload.Read(p) }

type shortResponseWriter struct {
	header   http.Header
	accepted int
}

func (w *shortResponseWriter) Header() http.Header { return w.header }
func (w *shortResponseWriter) WriteHeader(int)     {}
func (w *shortResponseWriter) Write(p []byte) (int, error) {
	return min(w.accepted, len(p)), io.ErrClosedPipe
}

func TestDownstreamShortWritesRefundAcrossTunnelHTTPAndUpgrade(t *testing.T) {
	for _, kind := range []string{"tunnel", "http", "upgrade"} {
		for _, accepted := range []int{0, 3} {
			t.Run(fmt.Sprintf("%s/%d", kind, accepted), func(t *testing.T) {
				counters := usage.NewCounters()
				key := &keys.KeyState{KeyID: "key", UserID: "tenant", Plan: keys.PlanSnapshot{TrafficBytes: 100}}
				gate := &usage.Gate{Limiter: usage.NewLimiter(counters)}
				q := newStreamQuota(gate, key, 0, nil)
				upstream := &payloadConn{payload: strings.NewReader("payload")}
				client := &shortUpstreamConn{accepted: accepted}
				var n int64
				switch kind {
				case "tunnel":
					r := pumpPreparedTunnelReader(client, strings.NewReader(""), &preparedTunnel{upstreamConn: upstream}, tunnelPumpOptions{quotaGate: gate, quotaKey: key})
					n = r.ingressBytes
				case "http":
					w := &quotaResponseWriter{ResponseWriter: &shortResponseWriter{header: make(http.Header), accepted: accepted}, quota: q}
					n, _ = io.Copy(w, upstream)
				case "upgrade":
					body := newQuotaReadWriteCloser(upstream, q)
					n, _ = io.Copy(client, body)
					_ = body.Close()
				}
				if n != int64(accepted) || counters.UserBytes("tenant") != n {
					t.Fatalf("accepted=%d copied=%d billed=%d", accepted, n, counters.UserBytes("tenant"))
				}
				drained := counters.Drain(counters.Keys())
				var billed int64
				for _, delta := range drained {
					billed += delta.IngressBytes
				}
				if billed != n {
					t.Fatalf("persistent ingress=%d copied=%d", billed, n)
				}
			})
		}
	}
}

func TestLatencyWrappedTunnelBillsOnceWithPlatform(t *testing.T) {
	counters := usage.NewCounters()
	gate := &usage.Gate{Limiter: usage.NewLimiter(counters)}
	key := &keys.KeyState{KeyID: "key", UserID: "tenant", Plan: keys.PlanSnapshot{TrafficBytes: 100}}
	counted := newAttributedCountingConn(&stubHalfCloseConn{}, nil, &keyAttribution{metering: &usage.Sink{Counters: counters}, keyID: "key", userID: "tenant", platformID: "platform"})
	session := &preparedTunnel{upstreamConn: newTLSLatencyConn(counted, nil)}
	r := pumpPreparedTunnelReader(&stubHalfCloseConn{}, strings.NewReader("payload"), session, tunnelPumpOptions{quotaGate: gate, quotaKey: key})
	if r.egressBytes != 7 || counters.UserBytes("tenant") != 7 {
		t.Fatalf("copied=%d billed=%d", r.egressBytes, counters.UserBytes("tenant"))
	}
	for bucket, delta := range counters.Drain(counters.Keys()) {
		if bucket.PlatformID != "platform" || delta.EgressBytes != 7 {
			t.Fatalf("bucket=%+v delta=%+v", bucket, delta)
		}
	}
}

func TestPendingReservationDoesNotPreemptOppositeRead(t *testing.T) {
	counters := usage.NewCounters()
	gate := &usage.Gate{Limiter: usage.NewLimiter(counters)}
	key := &keys.KeyState{KeyID: "key", UserID: "tenant", Plan: keys.PlanSnapshot{TrafficBytes: 7}}
	q := newStreamQuota(gate, key, 0, nil)
	reserved, finish := q.reserve(7, false)
	defer finish(0)
	if reserved != 7 {
		t.Fatal("reservation refused")
	}
	r := &quotaReader{reader: strings.NewReader(""), quota: q, ingress: true}
	if _, err := r.Read(make([]byte, 1)); err != io.EOF || q.Exceeded() {
		t.Fatalf("unsettled reservation aborted peer: %v", err)
	}
}

func TestUpgradeHalfCloseClosesSilentPeers(t *testing.T) {
	previous := tunnelHalfCloseTimeout
	tunnelHalfCloseTimeout = 20 * time.Millisecond
	defer func() { tunnelHalfCloseTimeout = previous }()
	upstream, origin := net.Pipe()
	defer origin.Close()
	client, peer := net.Pipe()
	defer peer.Close()
	body := newQuotaReadWriteCloser(upstream, nil)
	defer body.Close()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = io.Copy(client, body) }()
	if err := body.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("silent upgraded stream leaked a copier")
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("client was not closed: %v", err)
	}
}
