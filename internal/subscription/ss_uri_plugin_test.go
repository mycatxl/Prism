package subscription

import (
	"encoding/base64"
	"strings"
	"testing"

	"prism/internal/node"
)

// Audit 3.8 (#39): parseSSURI used to write the plugin name of a SIP002 link
// into the outbound without checking whether the bundled sing-box has an engine
// for it. A link carrying plugin=shadow-tls / restls / kcptun / gost-plugin (or
// any unknown name) therefore imported a node that entered the pool and every
// platform view, and then failed at dial time with "plugin not found: <name>" -
// a node that can never connect, with no reason shown anywhere. The Clash path
// refuses these through clashProxySkip; these tests pin the share-link twin.

// ssPluginTestUserinfo is base64("aes-128-gcm:pass"), the SIP002 userinfo used
// by every case below.
const ssPluginTestUserinfo = "YWVzLTEyOC1nY206cGFzcw=="

// ssPluginTestBase64Payload is base64("aes-128-gcm:pass@1.1.1.1:8388"), the
// legacy whole-payload form of the same node.
var ssPluginTestBase64Payload = base64.StdEncoding.EncodeToString([]byte("aes-128-gcm:pass@1.1.1.1:8388"))

// TestParseSSURI_PluginWithoutEngineIsRefused is the regression guard: a plugin
// sing-box cannot load must make parseSSURI report "not imported" instead of
// producing a node that is guaranteed to fail at dial time.
func TestParseSSURI_PluginWithoutEngineIsRefused(t *testing.T) {
	cases := []struct {
		name   string
		uri    string
		plugin string
	}{
		{
			name:   "shadow-tls with options",
			uri:    "ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=shadow-tls%3Bhost%3Dexample.com#ss-shadow-tls",
			plugin: "shadow-tls",
		},
		{
			name:   "shadow-tls bare",
			uri:    "ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=shadow-tls#ss-shadow-tls-bare",
			plugin: "shadow-tls",
		},
		{
			name:   "shadowtls spelling",
			uri:    "ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=shadowtls#ss-shadowtls",
			plugin: "shadowtls",
		},
		{
			name:   "restls",
			uri:    "ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=restls#ss-restls",
			plugin: "restls",
		},
		{
			name:   "kcptun",
			uri:    "ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=kcptun#ss-kcptun",
			plugin: "kcptun",
		},
		{
			name:   "gost-plugin",
			uri:    "ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=gost-plugin#ss-gost",
			plugin: "gost-plugin",
		},
		{
			name:   "unknown plugin",
			uri:    "ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=not-a-real-plugin#ss-unknown",
			plugin: "not-a-real-plugin",
		},
		{
			name:   "base64 payload with restls",
			uri:    "ss://" + ssPluginTestBase64Payload + "?plugin=restls#ss-b64-restls",
			plugin: "restls",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, ok := parseSSURI(tc.uri)
			if ok {
				t.Fatalf("parseSSURI accepted plugin %q, want a refusal; raw=%s",
					tc.plugin, string(parsed.RawOptions))
			}
		})
	}
}

// TestSSPluginHasEngine pins the whitelist itself: only the empty name and the
// two SIP002 plugins of docs/PROTOCOLS.md:134 have an engine in this build.
func TestSSPluginHasEngine(t *testing.T) {
	allowed := []string{"", "   ", "obfs-local", "obfs", "simple-obfs", "OBFS", "Simple-Obfs", "v2ray-plugin", "V2Ray-Plugin"}
	for _, plugin := range allowed {
		if !ssPluginHasEngine(plugin) {
			t.Errorf("ssPluginHasEngine(%q) = false, want true", plugin)
		}
	}

	refused := []string{
		"shadow-tls", "shadowtls", "restls", "kcptun", "gost-plugin",
		"not-a-real-plugin", "obfs-localx", "v2ray", "obfs-http",
	}
	for _, plugin := range refused {
		if ssPluginHasEngine(plugin) {
			t.Errorf("ssPluginHasEngine(%q) = true, want false", plugin)
		}
	}
}

// TestParseSSURI_PluginWithEngineStillAccepted is the regression half: the two
// supported plugins keep parsing with exactly the normalisation they had before
// the guard was added.
func TestParseSSURI_PluginWithEngineStillAccepted(t *testing.T) {
	cases := []struct {
		name       string
		uri        string
		wantPlugin string
		wantOpts   string
	}{
		{
			name:       "obfs-local",
			uri:        "ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=obfs-local%3Bobfs%3Dhttp#ss-obfs-local",
			wantPlugin: "obfs-local",
			wantOpts:   "obfs=http",
		},
		{
			name:       "obfs alias",
			uri:        "ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=obfs%3Bobfs%3Dhttp#ss-obfs",
			wantPlugin: "obfs-local",
			wantOpts:   "obfs=http",
		},
		{
			name:       "simple-obfs alias",
			uri:        "ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=simple-obfs%3Bobfs%3Dhttp%3Bobfs-host%3Dobfs.example.com#ss-simple-obfs",
			wantPlugin: "obfs-local",
			wantOpts:   "obfs=http;obfs-host=obfs.example.com",
		},
		{
			name:       "v2ray-plugin",
			uri:        "ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=v2ray-plugin%3Bmode%3Dwebsocket#ss-v2ray",
			wantPlugin: "v2ray-plugin",
			wantOpts:   "mode=websocket",
		},
		{
			name:       "base64 payload with obfs",
			uri:        "ss://" + ssPluginTestBase64Payload + "?plugin=obfs%3Bobfs%3Dhttp#ss-b64-obfs",
			wantPlugin: "obfs-local",
			wantOpts:   "obfs=http",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, ok := parseSSURI(tc.uri)
			if !ok {
				t.Fatalf("parseSSURI refused a supported plugin: %s", tc.uri)
			}
			obj := parseNodeRaw(t, parsed.RawOptions)
			if got := obj["plugin"]; got != tc.wantPlugin {
				t.Fatalf("plugin: got %v, want %q", got, tc.wantPlugin)
			}
			if got := obj["plugin_opts"]; got != tc.wantOpts {
				t.Fatalf("plugin_opts: got %v, want %q", got, tc.wantOpts)
			}
		})
	}
}

// TestParseSSURI_WithoutPluginStillAccepted pins that a plain ss:// link is
// untouched by the guard, in both the SIP002 and the base64 shape.
func TestParseSSURI_WithoutPluginStillAccepted(t *testing.T) {
	uris := map[string]string{
		"sip002": "ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388#ss-plain",
		"base64": "ss://" + ssPluginTestBase64Payload + "#ss-b64",
	}
	for name, uri := range uris {
		t.Run(name, func(t *testing.T) {
			parsed, ok := parseSSURI(uri)
			if !ok {
				t.Fatalf("parseSSURI refused a plugin-less link: %s", uri)
			}
			obj := parseNodeRaw(t, parsed.RawOptions)
			if _, hasPlugin := obj["plugin"]; hasPlugin {
				t.Fatalf("plugin must be absent without a plugin= parameter: %v", obj["plugin"])
			}
			if _, hasOpts := obj["plugin_opts"]; hasOpts {
				t.Fatalf("plugin_opts must be absent without a plugin= parameter: %v", obj["plugin_opts"])
			}
		})
	}
}

// TestShareLinkSkip_SSPluginBlameIsExact pins the attribution rule: the plugin
// is only named when removing it is what makes the link parse. A link that is
// broken for another reason keeps the generic detail instead of blaming its
// plugin.
func TestShareLinkSkip_SSPluginBlameIsExact(t *testing.T) {
	// Unsupported plugin, otherwise fine: the plugin is the cause.
	blamed := "ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=shadow-tls#ss-shadow-tls"
	skipped := shareLinkSkip(blamed)
	if skipped.Detail != "ss(shadow-tls)" || skipped.Reason != node.ReasonEngineNotBuilt {
		t.Errorf("unsupported plugin only: got %+v, want ENGINE_NOT_BUILT ss(shadow-tls)", skipped)
	}

	// Unsupported plugin AND a link that cannot parse without it either: the
	// plugin must not be blamed.
	other := "ss://" + ssPluginTestUserinfo + "@1.1.1.1?plugin=shadow-tls#ss-no-port"
	if skipped := shareLinkSkip(other); skipped.Detail == "ss(shadow-tls)" {
		t.Errorf("link broken for another reason: got detail %q, want the generic detail", skipped.Detail)
	}

	// A supported plugin never appears here at all (the link parses).
	if plugin := ssUnsupportedPlugin("ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=obfs%3Bobfs%3Dhttp#ss-obfs"); plugin != "" {
		t.Errorf("supported plugin: ssUnsupportedPlugin = %q, want empty", plugin)
	}
}

// TestParseWithReport_SSPluginWithoutEngineIsReported walks the public entry
// point: the refused links must not reach Nodes, and each one must appear in
// the report with a detail that names the plugin - otherwise the operator sees
// a node vanish with no reason, which is the second half of the bug.
func TestParseWithReport_SSPluginWithoutEngineIsReported(t *testing.T) {
	body := []byte(strings.Join([]string{
		"ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=shadow-tls%3Bhost%3Dexample.com#ss-shadow-tls",
		"ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=restls#ss-restls",
		"ss://" + ssPluginTestUserinfo + "@1.1.1.1:8388?plugin=obfs%3Bobfs%3Dhttp#ss-obfs",
	}, "\n"))

	result, err := ParseWithReport(body)
	if err != nil {
		t.Fatalf("ParseWithReport: %v", err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("imported nodes: got %d, want only the obfs node (skipped %+v)", len(result.Nodes), result.Skipped)
	}
	if got := result.Nodes[0].Tag; got != "ss-obfs" {
		t.Fatalf("imported node tag: got %q, want ss-obfs", got)
	}

	wantDetail := map[string]string{
		"ss-shadow-tls": "ss(shadow-tls)",
		"ss-restls":     "ss(restls)",
	}
	if len(result.Skipped) != len(wantDetail) {
		t.Fatalf("skip records: got %+v, want one per refused link", result.Skipped)
	}
	for _, skipped := range result.Skipped {
		detail, ok := wantDetail[skipped.Name]
		if !ok {
			t.Fatalf("unexpected skip record %+v", skipped)
		}
		if skipped.Reason != node.ReasonEngineNotBuilt {
			t.Errorf("skip reason for %s: got %q, want %q", skipped.Name, skipped.Reason, node.ReasonEngineNotBuilt)
		}
		if skipped.Detail != detail {
			t.Errorf("skip detail for %s: got %q, want %q", skipped.Name, skipped.Detail, detail)
		}
		if skipped.Source != SourceURI || skipped.Type != "shadowsocks" {
			t.Errorf("skip record for %s: got source=%q type=%q, want %q/shadowsocks",
				skipped.Name, skipped.Source, skipped.Type, SourceURI)
		}
	}
}
