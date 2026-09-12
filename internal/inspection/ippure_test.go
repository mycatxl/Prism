package inspection

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	"prism/internal/testutil"
)

func TestIPPureOriginalScoreIPv6AndPartialEvidence(t *testing.T) {
	now := time.Now().UTC()
	for _, raw := range []string{`{}`, `{"ip":"127.0.0.1","fraudScore":0}`, `{"ip":"8.8.8.8","fraudScore":101}`, `{"ip":"8.8.8.8","fraudScore":"5"}`, strings.Repeat("x", 33*1024)} {
		if _, err := decodeIPPure([]byte(raw), now); err == nil {
			t.Fatal("invalid IPPure result was accepted")
		}
	}
	review, err := decodeIPPure([]byte(`{"ip":"::ffff:8.8.8.8","asn":15169,"fraudScore":5,"isResidential":true,"isBroadcast":false,"userAgent":"must-not-be-kept"}`), now)
	if err != nil {
		t.Fatal(err)
	}
	e := review.Evidence
	if e.IP != "8.8.8.8" || *e.RiskScore != 5 || e.Native == nil || !*e.Native || e.IPType != "residential" || !review.ScoreSupported {
		t.Fatalf("incorrect source meaning: %+v", review)
	}
	review, err = decodeIPPure([]byte(`{"ip":"2606:4700:4700::1111","fraudScore":0,"isResidential":false}`), now)
	if err != nil || review.ScoreSupported || review.Evidence.RiskScore != nil || review.Evidence.IPType != "unknown" {
		t.Fatal("IPv6 or non-residential result was over-interpreted")
	}
	review, err = decodeIPPure([]byte(`{"ip":"8.8.8.8","isResidential":true}`), now)
	if err != nil || review.ScoreSupported || review.Evidence.RiskScore != nil {
		t.Fatal("missing risk became zero")
	}
}

func TestIPPureSerializesAndKeepsProviderCooldownAcrossNodes(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	checker := NewIPPureCheckerWithFetcher(func(ctx context.Context, ob adapter.Outbound) (IPPureResponse, error) {
		calls.Add(1)
		close(started)
		select {
		case <-ctx.Done():
			return IPPureResponse{}, ctx.Err()
		case <-release:
		}
		return IPPureResponse{StatusCode: 429, RetryAfter: "7200"}, nil
	})
	done := make(chan error, 1)
	go func() { _, err := checker.Check(context.Background(), testutil.NewNoopOutbound()); done <- err }()
	<-started
	_, err := checker.Check(context.Background(), testutil.NewNoopOutbound())
	var failure *ProviderError
	if !errors.As(err, &failure) || failure.Code != "IPPURE_LIMIT" || calls.Load() != 1 {
		t.Fatal("concurrent call escaped single global limiter")
	}
	close(release)
	if err = <-done; !errors.As(err, &failure) || failure.RetryAfter != 2*time.Hour {
		t.Fatal("Retry-After ignored")
	}
	status := checker.Status()
	if status.Busy || status.NextAllowedAt == nil || time.Until(*status.NextAllowedAt) < 119*time.Minute {
		t.Fatal("provider cooldown was lost")
	}
	if _, err = checker.Check(context.Background(), testutil.NewNoopOutbound()); err == nil || calls.Load() != 1 {
		t.Fatal("switching nodes bypassed cooldown")
	}
}

type blockedIPPureOutbound struct {
	testutil.NoopOutbound
	addresses []string
}

func (o *blockedIPPureOutbound) DialContext(_ context.Context, _ string, target M.Socksaddr) (net.Conn, error) {
	o.addresses = append(o.addresses, target.String())
	return nil, errors.New("test upstream credential-must-not-leak")
}

func TestIPPureDialsOnlySelectedNodeAndSanitizesFailure(t *testing.T) {
	outbound := &blockedIPPureOutbound{}
	checker := NewIPPureChecker()
	_, err := checker.Check(context.Background(), outbound)
	if err == nil || strings.Contains(err.Error(), "credential-must-not-leak") {
		t.Fatal("upstream failure leaked or was bypassed")
	}
	if len(outbound.addresses) != 1 || outbound.addresses[0] != "my.ippure.com:443" {
		t.Fatalf("wrong outbound target: %v", outbound.addresses)
	}
	if checker.Status().NextAllowedAt == nil {
		t.Fatal("failed request did not consume cooldown")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	clean := NewIPPureChecker()
	if _, err = clean.Check(canceled, outbound); !errors.Is(err, context.Canceled) || clean.Status().NextAllowedAt != nil {
		t.Fatal("already canceled request started a lookup")
	}
}
