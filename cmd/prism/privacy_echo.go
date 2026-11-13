package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"prism/internal/config"
	"prism/internal/netutil"
	"prism/internal/node"
	"prism/internal/nodeprivacy"
	"prism/internal/topology"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
)

func measureNodeIdentity(pool *topology.GlobalNodePool, h node.Hash, exit netip.Addr, cfg config.NodePrivacyConfig) {
	measureNodeIdentityUsing(pool, h, exit, cfg, netutil.HTTPGetViaOutbound)
}

type identityFetcher func(context.Context, adapter.Outbound, string, netutil.OutboundHTTPOptions) ([]byte, time.Duration, error)

func measureNodeIdentityUsing(pool *topology.GlobalNodePool, h node.Hash, exit netip.Addr, cfg config.NodePrivacyConfig, fetch identityFetcher) {
	// The switch controls starting a measurement, never handling its result.
	// An already in-flight leak must still delete after an administrator disables echo.
	if !cfg.EchoEnabled {
		return
	}
	entry, ok := pool.GetEntry(h)
	if !ok {
		return
	}
	started := time.Now()
	status, cause := "measured_failed", "MEASUREMENT_FAILED"
	defer func() {
		if current, ok := pool.GetEntry(h); ok && current == entry {
			if entry.CompleteIdentityMeasurement(exit, status, cfg.EchoURL, cause, started) {
				pool.NotifyNodeDirty(h)
			}
		}
	}()
	if cfg.EchoURL == "" {
		status, cause = "unmeasured", "ENDPOINT_UNCONFIGURED"
		return
	}
	if nodeprivacy.ValidateEchoURL(cfg.EchoURL) != nil {
		cause = "ENDPOINT_INVALID"
		return
	}
	marker, err := nodeprivacy.NewMarker()
	if err != nil {
		cause = "MARKER_GENERATION_FAILED"
		return
	}
	outbound := entry.Outbound.Load()
	if outbound == nil {
		cause = "OUTBOUND_NOT_READY"
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ctx, privacyCancel := entry.BindPrivacyContext(ctx)
	defer privacyCancel()
	body, _, err := fetch(ctx, *outbound, cfg.EchoURL, netutil.OutboundHTTPOptions{Headers: http.Header{nodeprivacy.MarkerHeader: []string{marker}}, NoRedirect: true, RequireStatusOK: true, MaxBodyBytes: 16 * 1024})
	if err != nil {
		cause = identityFailureCode(err)
		return
	}
	if current, ok := pool.GetEntry(h); !ok || current != entry {
		return
	}
	// A late result for a replaced trace sample cannot delete the current entry.
	// The Task 4 peer-mismatch deletion rule still applies to this sample.
	if entry.GetEgressIP() != exit {
		cause = "EGRESS_CHANGED"
		return
	}
	leak, err := nodeprivacy.EvaluateEcho(body, marker, exit)
	if err != nil {
		cause = err.Error()
		return
	}
	if leak {
		pool.DeletePrivacyNode(h, node.PrivacyEchoLeak, "")
		return
	}
	status, cause = "measured", ""
}

// Only stable allowlisted causes leave this boundary; transport URLs and
// credentials are never copied into product responses.
func identityFailureCode(err error) string {
	var status *netutil.HTTPStatusError
	var network net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "TIMEOUT"
	case errors.As(err, &network) && network.Timeout():
		return "TIMEOUT"
	case errors.As(err, &status):
		return "HTTP_FAILED"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "x509") || strings.Contains(message, "tls") || strings.Contains(message, "certificate") {
		return "TLS_FAILED"
	}
	return "NETWORK_FAILED"
}
