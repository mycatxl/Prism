// Command subprobe parses real subscription files with Prism's own parser.
//
// It has two read-only modes:
//
//	subprobe <file>...            field coverage per protocol
//	subprobe -dial <file>...      dial the selected protocols with Prism's own
//	                              sing-box runtime and report the failure class
//
// It never prints node credentials: no password, UUID, PSK or server address
// ever reaches stdout. Failures are reported as a class plus a short node hash
// prefix, which is enough to correlate with the pool without leaking an entry.
//
// Temporary diagnostic tool: not part of the shipped binary.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"

	"prism/internal/config"
	"prism/internal/node"
	"prism/internal/outbound"
	"prism/internal/subscription"
)

var dialTarget = "https://1.1.1.1/cdn-cgi/trace"

func main() {
	target := flag.String("target", "", "override the probe URL")
	dialMode := flag.Bool("dial", false, "dial the selected protocols and report failure classes")
	protoList := flag.String("proto", "hysteria2,trojan", "comma separated protocols for -dial")
	timeout := flag.Duration("timeout", 12*time.Second, "per-node dial timeout")
	flag.Parse()

	if *target != "" {
		dialTarget = *target
	}

	paths := flag.Args()
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: subprobe [-dial] [-proto a,b] [-timeout 12s] <file>...")
		os.Exit(2)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			continue
		}
		nodes, err := subscription.ParseGeneralSubscription(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: parse: %v\n", path, err)
			continue
		}
		if *dialMode {
			if err := dialReport(nodes, *protoList, *timeout); err != nil {
				fmt.Fprintf(os.Stderr, "%s: dial: %v\n", path, err)
			}
			continue
		}
		report(path, nodes)
	}
}

// ---------------------------------------------------------------------------
// mode 1: field coverage
// ---------------------------------------------------------------------------

func report(path string, nodes []subscription.ParsedNode) {
	byProto := map[string][]map[string]any{}
	for _, parsed := range nodes {
		var obj map[string]any
		if json.Unmarshal(parsed.RawOptions, &obj) != nil {
			continue
		}
		proto, _ := obj["type"].(string)
		byProto[proto] = append(byProto[proto], obj)
	}

	fmt.Printf("=== %s: %d parsed nodes ===\n", path, len(nodes))
	protos := make([]string, 0, len(byProto))
	for proto := range byProto {
		protos = append(protos, proto)
	}
	sort.Strings(protos)
	for _, proto := range protos {
		fmt.Printf("  %-14s %d\n", proto, len(byProto[proto]))
	}

	for _, proto := range []string{"hysteria2", "hysteria", "trojan"} {
		group := byProto[proto]
		if len(group) == 0 {
			continue
		}
		counts := map[string]int{}
		for _, obj := range group {
			bump(counts, "server_port", hasKey(obj, "server_port"))
			bump(counts, "server_ports", hasKey(obj, "server_ports"))
			bump(counts, "up_mbps", hasKey(obj, "up_mbps"))
			bump(counts, "down_mbps", hasKey(obj, "down_mbps"))
			bump(counts, "obfs", hasKey(obj, "obfs"))
			bump(counts, "hop_interval", hasKey(obj, "hop_interval"))
			tls, _ := obj["tls"].(map[string]any)
			if tls == nil {
				continue
			}
			bump(counts, "tls.server_name", hasKey(tls, "server_name"))
			bump(counts, "tls.alpn", hasKey(tls, "alpn"))
			bump(counts, "tls.utls", hasKey(tls, "utls"))
			bump(counts, "tls.reality", hasKey(tls, "reality"))
			bump(counts, "tls.certificate", hasKey(tls, "certificate"))
			bump(counts, "tls.certificate_path", hasKey(tls, "certificate_path"))
			if v, ok := tls["insecure"]; ok && v == true {
				counts["tls.insecure=true"]++
			}
		}
		fmt.Printf("\n--- %s field coverage (%d nodes) ---\n", proto, len(group))
		keys := make([]string, 0, len(counts))
		for key := range counts {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Printf("  %-22s %d/%d\n", key, counts[key], len(group))
		}
	}
	fmt.Println()
}

// ---------------------------------------------------------------------------
// mode 2: real dialing
// ---------------------------------------------------------------------------

type dialOutcome struct {
	proto   string
	class   string
	hash    string
	detail  string
	elapsed time.Duration
	// config is a credential-free summary of the node's transport settings.
	config string
}

func dialReport(nodes []subscription.ParsedNode, protoList string, timeout time.Duration) error {
	wanted := map[string]bool{}
	for _, proto := range strings.Split(protoList, ",") {
		if trimmed := strings.TrimSpace(proto); trimmed != "" {
			wanted[trimmed] = true
		}
	}

	builder, err := outbound.NewSingboxBuilderWithConfig(outbound.SingboxBuilderConfig{
		DNSUpstreams: config.DefaultNodeDNSUpstreams(),
	})
	if err != nil {
		return fmt.Errorf("build sing-box runtime: %w", err)
	}
	defer builder.Close()

	var outcomes []dialOutcome
	for _, parsed := range nodes {
		var obj map[string]any
		if json.Unmarshal(parsed.RawOptions, &obj) != nil {
			continue
		}
		proto, _ := obj["type"].(string)
		if !wanted[proto] {
			continue
		}
		hash := node.HashFromRawOptions(parsed.RawOptions).Hex()
		if len(hash) > 10 {
			hash = hash[:10]
		}
		outcomes = append(outcomes, dialOne(builder, parsed.RawOptions, proto, hash, timeout))
	}
	printDialReport(protoList, outcomes)
	return nil
}

func dialOne(builder outbound.OutboundBuilder, raw json.RawMessage, proto, hash string, timeout time.Duration) dialOutcome {
	out := dialOutcome{proto: proto, hash: hash, config: configSummary(raw)}
	ob, err := builder.Build(raw)
	if err != nil {
		out.class = "BUILD_FAILED"
		out.detail = shortReason(err)
		return out
	}
	status, elapsed, err := fetchThrough(ob, timeout)
	out.elapsed = elapsed
	if err != nil {
		out.class = classify(err)
		out.detail = shortReason(err)
		return out
	}
	if status < 200 || status >= 300 {
		out.class = fmt.Sprintf("HTTP_%d", status)
		return out
	}
	out.class = "ok"
	return out
}

// fetchThrough performs the same request the egress probe does, through the
// node's own outbound. It never falls back to a direct dial.
func fetchThrough(ob adapter.Outbound, timeout time.Duration) (int, time.Duration, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return ob.DialContext(ctx, network, M.ParseSocksaddr(address))
		},
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 8 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dialTarget, nil)
	if err != nil {
		return 0, 0, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return 0, elapsed, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, elapsed, nil
}

// classify maps a transport error onto a stable, credential-free class so the
// report stays readable when hundreds of nodes share one failure.
func classify(err error) string {
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "bad port range"):
		return "CONFIG_BAD_PORT_RANGE"
	case strings.Contains(text, "certificate") || strings.Contains(text, "x509"):
		return "TLS_CERTIFICATE"
	case strings.Contains(text, "handshake"):
		return "TLS_HANDSHAKE"
	case strings.Contains(text, "no route"):
		return "NO_ROUTE"
	case strings.Contains(text, "unreachable"):
		return "UNREACHABLE"
	case strings.Contains(text, "connection refused"):
		return "REFUSED"
	case strings.Contains(text, "connection reset"):
		return "RESET"
	case strings.Contains(text, "timeout") || strings.Contains(text, "deadline"):
		return "TIMEOUT"
	case strings.Contains(text, "eof"):
		return "EOF"
	case strings.Contains(text, "authentication") || strings.Contains(text, "unauthorized"):
		return "AUTH"
	default:
		return "OTHER"
	}
}

// shortReason keeps a bounded, credential-free excerpt of the error. Transport
// errors name the proxy target and the failure, never the password or UUID.
func shortReason(err error) string {
	text := strings.Join(strings.Fields(err.Error()), " ")
	if len(text) > 110 {
		text = text[:110] + "..."
	}
	return text
}

func printDialReport(protoList string, outcomes []dialOutcome) {
	byProto := map[string][]dialOutcome{}
	for _, outcome := range outcomes {
		byProto[outcome.proto] = append(byProto[outcome.proto], outcome)
	}
	protos := make([]string, 0, len(byProto))
	for proto := range byProto {
		protos = append(protos, proto)
	}
	sort.Strings(protos)

	fmt.Printf("=== dial report (target %s) ===\n", dialTarget)
	for _, proto := range protos {
		group := byProto[proto]
		counts := map[string]int{}
		for _, outcome := range group {
			counts[outcome.class]++
		}
		keys := make([]string, 0, len(counts))
		for key := range counts {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			if counts[keys[i]] != counts[keys[j]] {
				return counts[keys[i]] > counts[keys[j]]
			}
			return keys[i] < keys[j]
		})
		fmt.Printf("\n--- %s: %d nodes ---\n", proto, len(group))
		for _, key := range keys {
			fmt.Printf("  %-24s %d/%d\n", key, counts[key], len(group))
		}
		fmt.Println("  detail:")
		for _, outcome := range group {
			if outcome.class == "ok" {
				fmt.Printf("    %-10s %-24s %5dms  %s\n", outcome.hash, outcome.class, outcome.elapsed.Milliseconds(), outcome.config)
				continue
			}
			fmt.Printf("    %-10s %-24s %s  %s\n", outcome.hash, outcome.class, outcome.config, outcome.detail)
		}
	}
	fmt.Println()
}

// configSummary renders a credential-free fingerprint of the transport settings
// that shape a QUIC/TLS dial, so a failure class can be correlated with a
// configuration across hundreds of nodes.
func configSummary(raw json.RawMessage) string {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	tls, _ := obj["tls"].(map[string]any)
	flag := func(m map[string]any, key string) string {
		if m == nil {
			return "no"
		}
		if value, ok := m[key]; ok && value != nil {
			return "yes"
		}
		return "no"
	}
	updown := "no"
	if hasKey(obj, "up_mbps") || hasKey(obj, "down_mbps") {
		updown = "yes"
	}
	flags := []string{
		"obfs=" + flag(obj, "obfs"),
		"ports=" + flag(obj, "server_ports"),
		"hop=" + flag(obj, "hop_interval"),
		"updown=" + updown,
		"sni=" + flag(tls, "server_name"),
		"alpn=" + flag(tls, "alpn"),
		"insec=" + flag(tls, "insecure"),
		"utls=" + flag(tls, "utls"),
		"cert=" + flag(tls, "certificate"),
	}
	return "[" + strings.Join(flags, " ") + "]"
}

func hasKey(m map[string]any, key string) bool {
	value, ok := m[key]
	return ok && value != nil
}

func bump(counts map[string]int, key string, present bool) {
	if present {
		counts[key]++
	}
}
