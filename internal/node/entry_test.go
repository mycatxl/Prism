package node

import (
	"encoding/json"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"prism/internal/testutil"
)

func TestNodeEntry_SubscriptionIDs(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)

	e.AddSubscriptionID("s1")
	e.AddSubscriptionID("s2")
	e.AddSubscriptionID("s1") // idempotent

	ids := e.SubscriptionIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 subs, got %d: %v", len(ids), ids)
	}

	empty := e.RemoveSubscriptionID("s1")
	if empty {
		t.Fatal("should not be empty after removing s1")
	}
	if e.SubscriptionCount() != 1 {
		t.Fatalf("expected 1 sub, got %d", e.SubscriptionCount())
	}

	empty = e.RemoveSubscriptionID("s2")
	if !empty {
		t.Fatal("should be empty after removing s2")
	}

	// Idempotent remove.
	empty = e.RemoveSubscriptionID("s999")
	if !empty {
		t.Fatal("removing nonexistent should report empty if already empty")
	}
}

func TestNodeEntry_MatchRegexs_EmptyRegex(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	if !e.MatchRegexs(nil, nil) {
		t.Fatal("empty regex list should match")
	}
	if !e.MatchRegexs([]*regexp.Regexp{}, nil) {
		t.Fatal("empty regex slice should match")
	}
}

func TestNodeEntry_MatchRegexs_EmptyRegex_RequiresEnabledSubscriptionWhenLookupProvided(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-disabled")

	lookup := func(subID string, hash Hash) (string, bool, []string, bool) {
		switch subID {
		case "sub-disabled":
			return "SubDisabled", false, []string{"node-a"}, true
		case "sub-enabled":
			return "SubEnabled", true, []string{"node-b"}, true
		default:
			return "", false, nil, false
		}
	}

	if e.MatchRegexs([]*regexp.Regexp{}, lookup) {
		t.Fatal("empty regex with lookup should not match when all subscriptions are disabled")
	}

	e.AddSubscriptionID("sub-enabled")
	if !e.MatchRegexs([]*regexp.Regexp{}, lookup) {
		t.Fatal("empty regex with lookup should match when any subscription is enabled")
	}
}

func TestNodeEntry_MatchRegexs_Basic(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-1")

	lookup := func(subID string, hash Hash) (string, bool, []string, bool) {
		if subID == "sub-1" {
			return "MySub", true, []string{"us-node", "fast"}, true
		}
		return "", false, nil, false
	}

	// Match "MySub/us-node" — should match regex "us".
	regexes := []*regexp.Regexp{regexp.MustCompile("us")}
	if !e.MatchRegexs(regexes, lookup) {
		t.Fatal("should match 'us' regex")
	}

	// Should not match "jp".
	regexes = []*regexp.Regexp{regexp.MustCompile("jp")}
	if e.MatchRegexs(regexes, lookup) {
		t.Fatal("should not match 'jp' regex")
	}
}

func TestNodeEntry_MatchRegexs_AllRegexesMustMatch(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-1")

	lookup := func(subID string, hash Hash) (string, bool, []string, bool) {
		return "Provider", true, []string{"us-fast-1"}, true
	}

	// Both "us" and "fast" match "Provider/us-fast-1".
	regexes := []*regexp.Regexp{
		regexp.MustCompile("us"),
		regexp.MustCompile("fast"),
	}
	if !e.MatchRegexs(regexes, lookup) {
		t.Fatal("both regexes should match")
	}

	// "us" matches but "jp" does not.
	regexes = []*regexp.Regexp{
		regexp.MustCompile("us"),
		regexp.MustCompile("jp"),
	}
	if e.MatchRegexs(regexes, lookup) {
		t.Fatal("should not match when one regex fails")
	}
}

func TestNodeEntry_MatchRegexs_DisabledSubSkipped(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-1")

	lookup := func(subID string, hash Hash) (string, bool, []string, bool) {
		return "MySub", false, []string{"us-node"}, true // disabled
	}

	regexes := []*regexp.Regexp{regexp.MustCompile("us")}
	if e.MatchRegexs(regexes, lookup) {
		t.Fatal("disabled sub should not contribute to match")
	}
}

func TestNodeEntry_MatchRegexs_MultiSub(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-1")
	e.AddSubscriptionID("sub-2")

	lookup := func(subID string, hash Hash) (string, bool, []string, bool) {
		switch subID {
		case "sub-1":
			return "Provider-A", true, []string{"eu-node"}, true
		case "sub-2":
			return "Provider-B", true, []string{"us-node"}, true
		}
		return "", false, nil, false
	}

	// Match "us" — should match via sub-2.
	regexes := []*regexp.Regexp{regexp.MustCompile("us")}
	if !e.MatchRegexs(regexes, lookup) {
		t.Fatal("should match via second subscription")
	}
}

func TestNodeEntry_MatchTagFilter_AnyRulesAreORed(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-1")

	lookup := func(string, Hash) (string, bool, []string, bool) {
		return "Provider", true, []string{"jp-node"}, true
	}
	filter := TagFilter{Any: []*regexp.Regexp{
		regexp.MustCompile("hk"),
		regexp.MustCompile("jp"),
	}}
	if !e.MatchTagFilter(filter, lookup) {
		t.Fatal("one ANY match should satisfy the filter")
	}
}

func TestNodeEntry_MatchTagFilter_PositiveRulesRequireSameTag(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-1")

	lookup := func(string, Hash) (string, bool, []string, bool) {
		return "Provider", true, []string{"us-basic", "eu-fast"}, true
	}
	filter := TagFilter{
		Any:  []*regexp.Regexp{regexp.MustCompile("us")},
		Must: []*regexp.Regexp{regexp.MustCompile("fast")},
	}
	if e.MatchTagFilter(filter, lookup) {
		t.Fatal("ANY and MUST matches on different tags must not satisfy the filter")
	}
}

func TestNodeEntry_MatchTagFilter_MustNotRejectsAcrossAllTags(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-1")

	lookup := func(string, Hash) (string, bool, []string, bool) {
		return "Provider", true, []string{"us-fast", "expired-alias"}, true
	}
	filter := TagFilter{
		Any:     []*regexp.Regexp{regexp.MustCompile("us")},
		Must:    []*regexp.Regexp{regexp.MustCompile("fast")},
		MustNot: []*regexp.Regexp{regexp.MustCompile("expired")},
	}
	if e.MatchTagFilter(filter, lookup) {
		t.Fatal("a MUST_NOT match on any tag must reject the whole node")
	}
}

func TestNodeEntry_MatchTagFilter_OnlyMustNot(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-1")

	lookup := func(string, Hash) (string, bool, []string, bool) {
		return "Provider", true, nil, true
	}
	filter := TagFilter{MustNot: []*regexp.Regexp{regexp.MustCompile("expired")}}
	if !e.MatchTagFilter(filter, lookup) {
		t.Fatal("an exclusion-only filter should allow an enabled node with no matching tag")
	}
}

func TestNodeEntry_HasEnabledSubscription(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-disabled")
	e.AddSubscriptionID("sub-enabled")

	lookup := func(subID string, hash Hash) (string, bool, []string, bool) {
		switch subID {
		case "sub-disabled":
			return "SubDisabled", false, []string{"node-a"}, true
		case "sub-enabled":
			return "SubEnabled", true, []string{"node-b"}, true
		default:
			return "", false, nil, false
		}
	}

	if !e.HasEnabledSubscription(lookup) {
		t.Fatal("expected HasEnabledSubscription to be true")
	}
	if e.IsDisabledBySubscriptions(lookup) {
		t.Fatal("expected IsDisabledBySubscriptions to be false")
	}
}

func TestNodeEntry_IsDisabledBySubscriptions_AllDisabledOrMissing(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-disabled")
	e.AddSubscriptionID("sub-missing")

	lookup := func(subID string, hash Hash) (string, bool, []string, bool) {
		if subID == "sub-disabled" {
			return "SubDisabled", false, []string{"node-a"}, true
		}
		return "", false, nil, false
	}

	if e.HasEnabledSubscription(lookup) {
		t.Fatal("expected HasEnabledSubscription to be false")
	}
	if !e.IsDisabledBySubscriptions(lookup) {
		t.Fatal("expected IsDisabledBySubscriptions to be true")
	}
}

func TestNodeEntry_CircuitBreaker(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	if e.IsCircuitOpen() {
		t.Fatal("should not be circuit-open by default")
	}

	e.CircuitOpenSince.Store(time.Now().UnixNano())
	if !e.IsCircuitOpen() {
		t.Fatal("should be circuit-open after store")
	}

	e.CircuitOpenSince.Store(0)
	if e.IsCircuitOpen() {
		t.Fatal("should not be circuit-open after reset")
	}
}

func TestNodeEntry_LatencyCount(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 16)
	if e.HasLatency() {
		t.Fatal("should not have latency by default")
	}

	e.LatencyTable.LoadEntry("example.com", DomainLatencyStats{
		Ewma:        100 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	if !e.HasLatency() {
		t.Fatal("should have latency after adding an entry")
	}
}

func TestNodeEntry_Outbound(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	if e.HasOutbound() {
		t.Fatal("should not have outbound by default")
	}

	ob := testutil.NewNoopOutbound()
	e.Outbound.Store(&ob)
	if !e.HasOutbound() {
		t.Fatal("should have outbound after store")
	}
}

func TestNodeEntry_IsHealthy(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	if e.IsHealthy() {
		t.Fatal("node without outbound should not be healthy")
	}

	ob := testutil.NewNoopOutbound()
	e.Outbound.Store(&ob)
	if !e.IsHealthy() {
		t.Fatal("node with outbound and no circuit should be healthy")
	}

	e.CircuitOpenSince.Store(time.Now().UnixNano())
	if e.IsHealthy() {
		t.Fatal("circuit-open node should not be healthy")
	}
}

func TestNodeEntry_EgressIP(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)

	// Before any store, should return zero addr.
	addr := e.GetEgressIP()
	if addr.IsValid() {
		t.Fatal("should be invalid before first store")
	}

	ip := netip.MustParseAddr("1.2.3.4")
	e.SetEgressIP(ip)
	if got := e.GetEgressIP(); got != ip {
		t.Fatalf("expected %s, got %s", ip, got)
	}
}

func TestNodeEntry_EgressRegion(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)

	if got := e.GetEgressRegion(); got != "" {
		t.Fatalf("default egress region: got %q, want empty", got)
	}

	e.SetEgressRegion("US")
	if got := e.GetEgressRegion(); got != "us" {
		t.Fatalf("normalized egress region: got %q, want %q", got, "us")
	}

	e.SetEgressRegion("")
	if got := e.GetEgressRegion(); got != "" {
		t.Fatalf("cleared egress region: got %q, want empty", got)
	}
}

func TestNodeEntry_GetRegion_UsesStoredThenGeoIPFallback(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	e.SetEgressIP(netip.MustParseAddr("203.0.113.1"))

	geoLookupCalled := false
	geoLookup := func(_ netip.Addr) string {
		geoLookupCalled = true
		return "jp"
	}

	if got := e.GetRegion(geoLookup); got != "jp" {
		t.Fatalf("fallback region: got %q, want %q", got, "jp")
	}
	if !geoLookupCalled {
		t.Fatal("expected geo lookup to be called without stored region")
	}

	geoLookupCalled = false
	e.SetEgressRegion("US")
	if got := e.GetRegion(geoLookup); got != "us" {
		t.Fatalf("stored region: got %q, want %q", got, "us")
	}
	if geoLookupCalled {
		t.Fatal("geo lookup should not be called when stored region exists")
	}
}

// Protocol names and display details are rendered into the UI and matched by
// node filters, and both the engine type name and the transport name come from
// untrusted subscription content. These tests pin the mapping and the bound.
func TestNormalizeProtocolName(t *testing.T) {
	cases := []struct {
		name     string
		engine   string
		typeName string
		want     string
	}{
		{"singbox type is lowercased", EngineSingbox, "Trojan", "trojan"},
		{"singbox type is trimmed", EngineSingbox, "  vless  ", "vless"},
		{"singbox ss is left alone", EngineSingbox, "ss", "ss"},
		{"mihomo ss becomes shadowsocks", EngineMihomo, "ss", "shadowsocks"},
		{"mihomo socks5 becomes socks", EngineMihomo, "socks5", "socks"},
		{"mihomo keeps other names", EngineMihomo, "vmess", "vmess"},
		{"mihomo mapping is applied after folding", EngineMihomo, "SS", "shadowsocks"},
		{"empty stays empty", EngineSingbox, "", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := normalizeProtocolName(testCase.engine, testCase.typeName)
			if got != testCase.want {
				t.Errorf("normalizeProtocolName(%q, %q) = %q, want %q",
					testCase.engine, testCase.typeName, got, testCase.want)
			}
		})
	}
}

// TestNormalizeProtocolName_BoundsWithoutSplittingRunes pins that the bound
// counts characters. It used to slice the byte string at 32, which for a name of
// 33 CJK characters cut the eleventh character in half and produced invalid
// UTF-8 in the value filters match against.
func TestNormalizeProtocolName_BoundsWithoutSplittingRunes(t *testing.T) {
	got := normalizeProtocolName(EngineSingbox, strings.Repeat("中", 33))
	if !utf8.ValidString(got) {
		t.Fatalf("result = %q, want valid UTF-8", got)
	}
	if runes := utf8.RuneCountInString(got); runes != 32 {
		t.Errorf("rune count = %d, want 32", runes)
	}
	if want := strings.Repeat("中", 32); got != want {
		t.Errorf("result = %q, want %q", got, want)
	}
}

// TestNormalizeProtocolName_KeepsShortMultiByteNames is the other side of that
// rule: 20 CJK characters are 60 bytes, so a byte bound would have truncated a
// name that is within the limit.
func TestNormalizeProtocolName_KeepsShortMultiByteNames(t *testing.T) {
	name := strings.Repeat("中", 20)
	if got := normalizeProtocolName(EngineSingbox, name); got != name {
		t.Errorf("result = %q, want it unchanged", got)
	}
}

func TestNormalizeProtocolTransport(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"tcp":         "",
		"TCP":         "",
		"  ":          "",
		" splithttp ": "xhttp",
		"ws":          "ws",
		"grpc":        "grpc",
		"HTTP":        "http",
	}
	for raw, want := range cases {
		if got := normalizeProtocolTransport(raw); got != want {
			t.Errorf("normalizeProtocolTransport(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestNormalizeProtocolTransport_BoundsUnknownNames pins the bound on the
// default branch: an unknown transport name is copied into the node detail, so
// it must not carry an unbounded string into storage and the UI.
func TestNormalizeProtocolTransport_BoundsUnknownNames(t *testing.T) {
	if got := normalizeProtocolTransport(strings.Repeat("x", 500)); len(got) != 32 {
		t.Errorf("length = %d, want 32", len(got))
	}
}

func TestProtocolDetailFor(t *testing.T) {
	longType := strings.Repeat("中", 40)
	cases := []struct {
		name string
		doc  NodeDoc
		want string
	}{
		{
			"plain outbound",
			NodeDoc{Engine: EngineSingbox, Type: "Trojan", Main: json.RawMessage(`{"type":"trojan"}`)},
			"trojan",
		},
		{
			"transport detail",
			NodeDoc{Engine: EngineSingbox, Type: "vless",
				Main: json.RawMessage(`{"type":"vless","transport":{"type":"splithttp"}}`)},
			"vless+xhttp",
		},
		{
			"tcp transport adds nothing",
			NodeDoc{Engine: EngineSingbox, Type: "vless",
				Main: json.RawMessage(`{"type":"vless","transport":{"type":"tcp"}}`)},
			"vless",
		},
		{
			"reality detail",
			NodeDoc{Engine: EngineSingbox, Type: "vless",
				Main: json.RawMessage(`{"type":"vless","tls":{"reality":{"enabled":true}}}`)},
			"vless+reality",
		},
		{
			"transport and reality",
			NodeDoc{Engine: EngineSingbox, Type: "vless",
				Main: json.RawMessage(`{"type":"vless","transport":{"type":"ws"},"tls":{"reality":{"enabled":true}}}`)},
			"vless+ws+reality",
		},
		{
			"reality disabled",
			NodeDoc{Engine: EngineSingbox, Type: "vless",
				Main: json.RawMessage(`{"type":"vless","tls":{"reality":{"enabled":false}}}`)},
			"vless",
		},
		{
			"chain names its first dependency",
			NodeDoc{Engine: EngineSingbox, Type: "trojan", Chain: true,
				Main: json.RawMessage(`{"type":"trojan"}`),
				Deps: []json.RawMessage{json.RawMessage(`{"type":"Socks"}`)}},
			"trojan+socks",
		},
		{
			"chain without a readable dependency",
			NodeDoc{Engine: EngineSingbox, Type: "trojan", Chain: true,
				Main: json.RawMessage(`{"type":"trojan"}`),
				Deps: []json.RawMessage{json.RawMessage(`{"tag":"x"}`)}},
			"trojan",
		},
		{
			// The dependency name is untrusted too, so it is bounded like the
			// main type instead of being copied whole.
			"chain bounds its dependency name",
			NodeDoc{Engine: EngineSingbox, Type: "trojan", Chain: true,
				Main: json.RawMessage(`{"type":"trojan"}`),
				Deps: []json.RawMessage{json.RawMessage(`{"type":"` + longType + `"}`)}},
			"trojan+" + strings.Repeat("中", 32),
		},
		{
			"mihomo base name",
			NodeDoc{Engine: EngineMihomo, Type: "ss", Main: json.RawMessage(`{"type":"ss"}`)},
			"shadowsocks",
		},
		{
			"empty type has no detail",
			NodeDoc{Engine: EngineSingbox, Type: "", Main: json.RawMessage(`{}`)},
			"",
		},
		{
			"main that is not an object",
			NodeDoc{Engine: EngineSingbox, Type: "trojan", Main: json.RawMessage(`[]`)},
			"trojan",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := protocolDetailFor(testCase.doc); got != testCase.want {
				t.Errorf("protocolDetailFor() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestBoundProtocolToken(t *testing.T) {
	if got := boundProtocolToken(""); got != "" {
		t.Errorf("boundProtocolToken(\"\") = %q, want \"\"", got)
	}
	short := strings.Repeat("a", 32)
	if got := boundProtocolToken(short); got != short {
		t.Errorf("a name of exactly the limit was changed: %q", got)
	}
	if got := boundProtocolToken(strings.Repeat("a", 33)); got != short {
		t.Errorf("length = %d, want 32", len(got))
	}
}

func TestGetLastEgressUpdate(t *testing.T) {
	entry := &NodeEntry{}
	if got := entry.GetLastEgressUpdate(); !got.IsZero() {
		t.Errorf("GetLastEgressUpdate() = %v, want the zero time before any observation", got)
	}
	entry.LastEgressUpdate.Store(1_700_000_000_000_000_000)
	got := entry.GetLastEgressUpdate()
	if got.IsZero() {
		t.Fatal("GetLastEgressUpdate() = zero time, want the stored instant")
	}
	if got.Location().String() != "UTC" {
		t.Errorf("location = %v, want UTC", got.Location())
	}
}
