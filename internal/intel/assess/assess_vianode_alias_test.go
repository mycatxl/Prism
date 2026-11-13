package assess

import (
	"net/netip"
	"prism/internal/quality"
	"testing"
)

func TestAuxiliaryAliasesStayIndependentOfPurity(t *testing.T) {
	rows := evidence(ev(SourceProxycheck).risk(10), ev(SourceProxycheckNode).risk(90), ev(SourceIPAPIIsNode).typeOf("datacenter").flag("proxy", true))
	current := CurrentEvidence(testIPv4Addr(t), rows, testNow)
	if len(current) != 2 {
		t.Fatal("same vendor counted twice")
	}
	out := Assess(testIPv4Addr(t), rows, testNow, nil)
	if out.PurityScore != nil || out.IPType != IPTypeDatacenter || out.Flags&FlagProxy == 0 {
		t.Fatalf("auxiliary evidence became purity or was lost: %+v", out)
	}
	rows = append(rows, ev(SourceIPPure).fraud(20).e)
	out = Assess(testIPv4Addr(t), rows, testNow, nil)
	if out.PurityScore == nil || *out.PurityScore != 80 || len(out.Components) != 1 || out.Components[0].Source != SourceIPPure {
		t.Fatal("alias moved purity")
	}
}

func hasReason(out Assessment, want string) bool {
	for _, reason := range out.Reasons {
		if reason == want {
			return true
		}
	}
	return false
}
func testIPv4Addr(t *testing.T) netip.Addr { t.Helper(); return netip.MustParseAddr(testIPv4) }

var _ = quality.Evidence{}
