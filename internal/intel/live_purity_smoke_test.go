package intel

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"

	"prism/internal/intel/egress"
	"prism/internal/intel/providers"
	"prism/internal/netutil"
	"prism/internal/node"
	"prism/internal/nodeprivacy"
)

// A real network dial, with no environment proxy or provider redirection.
type liveSmokeOutbound struct{ adapter.Outbound }

func (liveSmokeOutbound) DialContext(ctx context.Context, network string, address M.Socksaddr) (net.Conn, error) {
	return (&net.Dialer{Timeout: 12 * time.Second}).DialContext(ctx, network, address.String())
}

// TestLivePuritySmoke is opt-in because it calls the public egress and IPPure
// services. This direct-only diagnostic cannot pass independent-node acceptance,
// even if different destinations/address families report different local exits.
// No credentials or private keys are generated; errors print only stable codes.
func TestLivePuritySmoke(t *testing.T) {
	if os.Getenv("PRISM_LIVE_PURITY_SMOKE") != "1" {
		t.Skip("opt-in live smoke")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ob := liveSmokeOutbound{}
	options := netutil.OutboundHTTPOptions{NoRedirect: true, RequireStatusOK: true, MaxBodyBytes: 8192}
	t.Log("stage=egress: actual public trace over production outbound transport")
	body, _, err := netutil.HTTPGetViaOutbound(ctx, ob, egress.FallbackTraceURLv4, options)
	if err != nil {
		t.Fatalf("stage=egress code=%s", liveSmokeError(err))
	}
	trace, err := egress.ParseTrace(body)
	if err != nil {
		t.Fatal("stage=egress code=INVALID_TRACE")
	}
	t.Log("stage=ippure: actual public provider, no response fixture or seeded evidence")
	provider := providers.NewIPPureProvider(providers.IPPureOptions{})
	result := provider.Lookup(ctx, ob, trace.IP)
	if result.Err != nil {
		t.Fatalf("stage=ippure code=%s", result.Err.Code)
	}
	if result.Evidence == nil || result.Evidence.FraudScore == nil {
		t.Fatal("stage=ippure code=NO_FRAUD_SCORE")
	}
	t.Logf("stage=ippure fraud_score=%d purity=%d", *result.Evidence.FraudScore, 100-*result.Evidence.FraudScore)
	t.Log("stage=privacy: checking actual own-asset exit before local forwarding")
	self, _, err := netutil.HTTPGetViaOutbound(ctx, ob, nodeprivacy.DefaultSelfProbeURL, options)
	if err != nil {
		t.Fatalf("stage=privacy code=%s", liveSmokeError(err))
	}
	own, err := netip.ParseAddr(strings.TrimSpace(string(self)))
	if err != nil {
		t.Fatal("stage=privacy code=INVALID_SELF_IP")
	}
	policy, err := node.NewPrivacyPolicy(nodeprivacy.Config{ProbeStatus: "ready", ActiveIPs: []string{own.Unmap().String()}})
	if err != nil {
		t.Fatal("stage=privacy code=INVALID_POLICY")
	}
	if policy.Denies(trace.IP) {
		t.Fatal("stage=privacy code=OWN_ASSET_EGRESS: local node uses this host's exit; independent node required for forwarding acceptance")
	}
	t.Fatal("stage=privacy code=DIRECT_EGRESS: differing direct exits do not prove an independent encrypted upstream; binary HTTP/SOCKS/reverse acceptance remains unverified")
}

func liveSmokeError(err error) string {
	var status *netutil.HTTPStatusError
	var network net.Error
	if errors.As(err, &status) {
		return "HTTP_FAILED"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout() {
		return "TIMEOUT"
	}
	return "NETWORK_OR_TLS_FAILED"
}
