package subscription

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Tests for the YAML resource guard (WP06 §9 hardening, yaml_guard.go).
//
// The guard exists because yaml.v3's decoder costs O(n²) time for one mapping
// with n keys (its duplicate-key check compares every new key against the keys
// already decoded): on this tree a single 200000-key mapping inside a body that
// satisfies looksLikeClashYAML costs 131 s of CPU, and the 32 MiB input cap
// would allow hours. These tests are all local: no network, no fixtures outside
// the package.

const yamlDepthChildEnv = "PRISM_SUB_DEPTH_CHILD"

// deepClashYAML builds a body that looks like a Clash configuration and nests
// prefix `[` brackets `levels` deep — kB of input, all of it inside one flow
// collection.
func deepClashYAML(levels int) string {
	return "proxies: " + strings.Repeat("[", levels)
}

// wideClashYAML builds a Clash body whose single proxy mapping carries `keys`
// keys, the shape that makes the yaml.v3 decoder quadratic.
func wideClashYAML(keys int) string {
	var b strings.Builder
	b.WriteString("proxies:\n  - name: wide\n")
	for i := 0; i < keys; i++ {
		b.WriteString("    x")
		b.WriteString(itoaSmall(i))
		b.WriteString(": 1\n")
	}
	return b.String()
}

// manyProxiesClashYAML builds a realistic large configuration: `proxies` with
// `count` entries of four keys each.
func manyProxiesClashYAML(count int) string {
	var b strings.Builder
	b.WriteString("proxies:\n")
	for i := 0; i < count; i++ {
		b.WriteString("  - name: n")
		b.WriteString(itoaSmall(i))
		b.WriteString("\n    type: ss\n    server: 198.51.100.1\n    port: 8388\n")
	}
	return b.String()
}

func itoaSmall(v int) string {
	if v == 0 {
		return "0"
	}
	var digits [20]byte
	pos := len(digits)
	for v > 0 {
		pos--
		digits[pos] = byte('0' + v%10)
		v /= 10
	}
	return string(digits[pos:])
}

// realClashConfig is a realistic node list: nested ws-opts/headers/tls blocks,
// a proxy-group and rules. It must never be refused by the guard.
const realClashConfig = `port: 7890
socks-port: 7891
mode: rule
log-level: info
proxies:
  - name: tokyo-ss
    type: ss
    server: 198.51.100.10
    port: 8388
    cipher: aes-256-gcm
    password: secret
    udp: true
    plugin: v2ray-plugin
    plugin-opts:
      mode: websocket
      tls: true
      host: example.com
      headers:
        X-Custom: "1"
        User-Agent: "chrome"
  - name: osaka-vless
    type: vless
    server: 198.51.100.11
    port: 443
    uuid: 11111111-2222-3333-4444-555555555555
    network: grpc
    tls: true
    servername: example.com
    flow: xtls-rprx-vision
    reality-opts:
      public-key: aGVsbG8
      short-id: abcd
    grpc-opts:
      grpc-service-name: svc
    ws-opts:
      path: /ws
      headers:
        Host: example.com
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - tokyo-ss
      - osaka-vless
rules:
  - DOMAIN-SUFFIX,example.com,PROXY
  - GEOIP,CN,DIRECT
  - MATCH,PROXY
`

func TestScanYAMLLimits_RealClashConfigStaysWithinLimits(t *testing.T) {
	result := scanYAMLLimits(realClashConfig, yamlScanLimits{
		MaxDepth: MaxYAMLNestingDepth,
		MaxKeys:  MaxYAMLMappingKeys,
	})
	if result.DepthBreach || result.MappingKeyBreach {
		t.Fatalf("real clash config refused: depth=%d keys=%d", result.Depth, result.MappingKeys)
	}
	if result.Depth < 4 {
		t.Fatalf("depth = %d, want the nested ws-opts/headers structure to be counted", result.Depth)
	}
}

func TestScanYAMLLimits_FlowNestingBombIsRefused(t *testing.T) {
	result := scanYAMLLimits(deepClashYAML(1<<20), yamlScanLimits{
		MaxDepth: MaxYAMLNestingDepth,
		MaxKeys:  MaxYAMLMappingKeys,
	})
	if !result.DepthBreach {
		t.Fatalf("flow nesting bomb accepted: %+v", result)
	}
	if result.Depth != MaxYAMLNestingDepth+1 {
		t.Fatalf("depth = %d, want %d", result.Depth, MaxYAMLNestingDepth+1)
	}
}

func TestScanYAMLLimits_BlockIndentationBombIsRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString("proxies:\n")
	for i := 0; i < MaxYAMLNestingDepth*4; i++ {
		b.WriteString(strings.Repeat(" ", i+1))
		b.WriteString("a:\n")
	}
	result := scanYAMLLimits(b.String(), yamlScanLimits{
		MaxDepth: MaxYAMLNestingDepth,
		MaxKeys:  MaxYAMLMappingKeys,
	})
	if !result.DepthBreach {
		t.Fatalf("indentation bomb accepted: %+v", result)
	}
}

func TestScanYAMLLimits_BlockScalarContentIsNotStructure(t *testing.T) {
	// A legitimate literal block (a certificate, a script) may contain brackets
	// and `key:`-looking lines; none of it is YAML structure.
	var b strings.Builder
	b.WriteString("proxies:\n  - name: a\n    certificate: |\n")
	for i := 0; i < 5*MaxYAMLMappingKeys; i++ {
		b.WriteString("      key")
		b.WriteString(itoaSmall(i))
		b.WriteString(": [[[[[[[[[[\n")
	}
	result := scanYAMLLimits(b.String(), yamlScanLimits{
		MaxDepth: MaxYAMLNestingDepth,
		MaxKeys:  MaxYAMLMappingKeys,
	})
	if result.DepthBreach || result.MappingKeyBreach {
		t.Fatalf("block scalar content refused: depth=%d keys=%d", result.Depth, result.MappingKeys)
	}
}

func TestScanYAMLLimits_OneWideMappingIsRefused(t *testing.T) {
	result := scanYAMLLimits(wideClashYAML(MaxYAMLMappingKeys+1), yamlScanLimits{
		MaxDepth: MaxYAMLNestingDepth,
		MaxKeys:  MaxYAMLMappingKeys,
	})
	if !result.MappingKeyBreach {
		t.Fatalf("wide mapping accepted: %+v", result)
	}
	if result.MappingKeys != MaxYAMLMappingKeys+1 {
		t.Fatalf("keys = %d, want %d", result.MappingKeys, MaxYAMLMappingKeys+1)
	}
}

func TestScanYAMLLimits_FlowMappingKeysAreCounted(t *testing.T) {
	// The same wide mapping written as one flow mapping on one line.
	var b strings.Builder
	b.WriteString("proxies: [{")
	for i := 0; i < MaxYAMLMappingKeys+1; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("x")
		b.WriteString(itoaSmall(i))
		b.WriteString(": 1")
	}
	b.WriteString("}]")
	result := scanYAMLLimits(b.String(), yamlScanLimits{
		MaxDepth: MaxYAMLNestingDepth,
		MaxKeys:  MaxYAMLMappingKeys,
	})
	if !result.MappingKeyBreach {
		t.Fatalf("wide flow mapping accepted: %+v", result)
	}
}

func TestScanYAMLLimits_ManySmallMappingsStayWithinLimits(t *testing.T) {
	// A large but real configuration: many proxies, each their own mapping.
	result := scanYAMLLimits(manyProxiesClashYAML(MaxYAMLMappingKeys+5000), yamlScanLimits{
		MaxDepth: MaxYAMLNestingDepth,
		MaxKeys:  MaxYAMLMappingKeys,
	})
	if result.DepthBreach || result.MappingKeyBreach {
		t.Fatalf("many small mappings refused: depth=%d keys=%d", result.Depth, result.MappingKeys)
	}
}

// TestParseWithReport_DeeplyNestedYAMLIsRefusedNotFatal runs the nesting bomb
// in a child process: a stack overflow is a fatal error that no recover() can
// catch, so "the process survives" can only be asserted from outside. The child
// also has to observe the recorded refusal reason in the parse report.
func TestParseWithReport_DeeplyNestedYAMLIsRefusedNotFatal(t *testing.T) {
	if os.Getenv(yamlDepthChildEnv) == "1" {
		result, err := ParseWithReport([]byte(deepClashYAML(1 << 20)))
		if err == nil {
			fmt.Println("CHILD-FAIL: expected the depth bomb to be refused")
			os.Exit(3)
		}
		if !strings.Contains(err.Error(), "nesting depth exceeds") {
			fmt.Printf("CHILD-FAIL: unexpected error: %v\n", err)
			os.Exit(4)
		}
		if len(result.Skipped) != 1 || result.Skipped[0].Reason != ReasonDepthExceeded {
			fmt.Printf("CHILD-FAIL: report carried %+v\n", result.Skipped)
			os.Exit(5)
		}
		if result.Stats.Skipped != 1 {
			fmt.Printf("CHILD-FAIL: stats skipped = %d, want 1\n", result.Stats.Skipped)
			os.Exit(6)
		}
		fmt.Printf("CHILD-OK reason=%s detail=%s\n", result.Skipped[0].Reason, result.Skipped[0].Detail)
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestParseWithReport_DeeplyNestedYAMLIsRefusedNotFatal$", "-test.v")
	cmd.Env = append(os.Environ(), yamlDepthChildEnv+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child process did not survive the nesting bomb: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "CHILD-OK") {
		t.Fatalf("child did not report the refusal:\n%s", output)
	}
	if !strings.Contains(string(output), string(ReasonDepthExceeded)) {
		t.Fatalf("child output does not carry the %s reason:\n%s", ReasonDepthExceeded, output)
	}
}

// TestParseWithReport_WideMappingIsRefusedCheaply is the regression test for the
// quadratic decode: before the guard, this body (100000 keys in one mapping,
// 1.5 MiB) took ~24 s of CPU and now must be refused with a reason in
// milliseconds.
func TestParseWithReport_WideMappingIsRefusedCheaply(t *testing.T) {
	body := wideClashYAML(100000)
	started := time.Now()
	result, err := ParseWithReport([]byte(body))
	elapsed := time.Since(started)

	if err == nil {
		t.Fatalf("wide mapping accepted (nodes=%d)", len(result.Nodes))
	}
	if !strings.Contains(err.Error(), "keys") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Reason != ReasonComplexityExceeded {
		t.Fatalf("report carried %+v, want one %s entry", result.Skipped, ReasonComplexityExceeded)
	}
	if elapsed > 20*time.Second {
		t.Fatalf("refusal took %s: the body was decoded instead of refused", elapsed)
	}
}

// TestParseWithReport_DeeplyNestedJSONStaysSafe pins the claim that the JSON
// path needs no guard of its own: encoding/json caps structural nesting at
// maxNestingDepth and answers with an error, and it stays linear for a mapping
// with a very large number of keys.
func TestParseWithReport_DeeplyNestedJSONStaysSafe(t *testing.T) {
	deep := `{"proxies":[{"name":"a","x":` + strings.Repeat("[", 20000) + strings.Repeat("]", 20000) + `}]}`
	result, err := ParseWithReport([]byte(deep))
	if err == nil {
		t.Fatalf("deeply nested JSON accepted (nodes=%d)", len(result.Nodes))
	}
	if strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("deeply nested JSON was not classified as JSON: %v", err)
	}

	var b strings.Builder
	b.WriteString(`{"proxies":[{`)
	for i := 0; i < 100000; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"x`)
		b.WriteString(itoaSmall(i))
		b.WriteString(`":1`)
	}
	b.WriteString("}]}")
	started := time.Now()
	if _, _, err := parseJSONSubscription([]byte(b.String()), newParseReport()); err != nil {
		t.Fatalf("wide JSON refuse: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("wide JSON took %s: the JSON path is not linear", elapsed)
	}
}
