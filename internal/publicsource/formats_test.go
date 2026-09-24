package publicsource

import (
	"encoding/json"
	"strings"
	"testing"
)

// parsedTypes maps "type tag" -> struct{} for every node parseSourceBody returned.
func parsedTypes(t *testing.T, body string) map[string]struct{} {
	t.Helper()
	parsed, err := parseSourceBody([]byte(body))
	if err != nil {
		t.Fatalf("parseSourceBody() error = %v", err)
	}
	types := make(map[string]struct{}, len(parsed))
	for _, item := range parsed {
		var raw map[string]any
		if err := json.Unmarshal(item.RawOptions, &raw); err != nil {
			t.Fatalf("node raw options are not JSON: %v", err)
		}
		tag, _ := raw["tag"].(string)
		typ, _ := raw["type"].(string)
		types[typ+" "+tag] = struct{}{}
	}
	return types
}

func TestNormalizeBannerPrefixedPlainList(t *testing.T) {
	// Shape of roosterkid/openproxylist HTTPS.txt: six banner lines then endpoints.
	body := "HTTP(S) Proxy list updated at 2026-09-22 17:00:02 GMT+7\n" +
		"Website=https://openproxylist.com\n" +
		"Support us:\n" +
		"BTC : 1PJNmhxKETLqaD6eexiNxg8ofT4uF7GKvF\n" +
		"ETH : 0x50403baa42092a3424f41fdc3a8621aeda333ee6\n" +
		"LTC : MAG1cWWEpgdviZChWvr2oyuxD61JPJ1Q43\n" +
		"203.0.113.10:8080\n" +
		"203.0.113.11:3128\n" +
		"203.0.113.12:8888\n"

	rewritten, ok := normalizeSourceBody([]byte(body))
	if !ok {
		t.Fatal("a banner-prefixed list must be recognised")
	}
	text := string(rewritten)
	for _, want := range []string{"http://203.0.113.10:8080", "http://203.0.113.11:3128", "http://203.0.113.12:8888"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rewritten list is missing %q:\n%s", want, text)
		}
	}
	// The donation addresses must not be mined as hosts.
	if strings.Contains(text, "1PJNmhxKETLqaD6eexiNxg8ofT4uF7GKvF") {
		t.Fatal("banner content leaked into the rewritten list")
	}
}

func TestNormalizeBannerPrefixedSocksListUsesSocksScheme(t *testing.T) {
	body := "SOCKS5 Proxy list updated at 2026-09-22 17:00:02 GMT+7\n" +
		"Website=https://openproxylist.com\n" +
		"198.51.100.7:1080\n" +
		"198.51.100.8:1081\n" +
		"198.51.100.9:1082\n"

	rewritten, ok := normalizeSourceBody([]byte(body))
	if !ok {
		t.Fatal("a banner-prefixed SOCKS list must be recognised")
	}
	text := string(rewritten)
	if strings.Contains(text, "http://") {
		t.Fatalf("a SOCKS5 banner must not produce http endpoints:\n%s", text)
	}
	if !strings.Contains(text, "socks5://198.51.100.7:1080") {
		t.Fatalf("unexpected rewrite:\n%s", text)
	}
}

func TestNormalizeSpysMeFormat(t *testing.T) {
	body := "Proxy list (#400) updated at Tue, 22 Sep 26 11:58:01 +0300\n" +
		"Socks proxy=https://spys.me/socks.txt\n" +
		"IP address:Port CountryCode-Anonymity(Noa/Anm/Hia)-SSL_support(S)-Google_passed(+)\n" +
		"192.0.2.10:80 FR-H + \n" +
		"192.0.2.11:3128 US-N + \n" +
		"192.0.2.12:8080 DE-A + \n"

	rewritten, ok := normalizeSourceBody([]byte(body))
	if !ok {
		t.Fatal("the spys.me format must be recognised")
	}
	text := string(rewritten)
	if !strings.Contains(text, "http://192.0.2.10:80") || !strings.Contains(text, "http://192.0.2.12:8080") {
		t.Fatalf("unexpected rewrite:\n%s", text)
	}
}

func TestNormalizeHTMLPage(t *testing.T) {
	// Shape of my-proxy.com/free-proxy-list.html: endpoints carry a #CC suffix.
	body := `<!DOCTYPE html><html><head><style>body{margin:0;padding:0}</style></head>` +
		`<body><table><tr><td>192.0.2.20:8000#US</td></tr>` +
		`<tr><td>192.0.2.21:8001#DE</td></tr>` +
		`<tr><td>192.0.2.22:8002#FR</td></tr></table></body></html>`

	rewritten, ok := normalizeSourceBody([]byte(body))
	if !ok {
		t.Fatal("an HTML proxy page must be recognised")
	}
	text := string(rewritten)
	for _, want := range []string{"http://192.0.2.20:8000", "http://192.0.2.21:8001", "http://192.0.2.22:8002"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rewritten page is missing %q:\n%s", want, text)
		}
	}
}

func TestNormalizeRejectsNoise(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"html error":    "<html><body><h1>404 Not Found</h1></body></html>",
		"single proxy":  "192.0.2.30:8080",
		"private hosts": "10.0.0.1:80\n192.168.1.1:80\n127.0.0.1:80\n169.254.1.1:80",
		"bad ports":     "192.0.2.40:0\n192.0.2.41:99999\n192.0.2.42:abc",
		"json garbage":  `[{"unexpected": true}, {"id": 1}]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if rewritten, ok := normalizeSourceBody([]byte(body)); ok {
				t.Fatalf("body must not be rewritten, got:\n%s", rewritten)
			}
		})
	}
}

func TestNormalizeProxiflyJSONArray(t *testing.T) {
	body := `[
  {"proxy":"socks5://208.102.51.6:58208","protocol":"socks5","ip":"208.102.51.6","port":58208,"geolocation":{"country":"US"}},
  {"proxy":"http://208.102.51.7:8080","protocol":"http","ip":"208.102.51.7","port":8080,"geolocation":{"country":"DE"}},
  {"proxy":"","protocol":"socks5","ip":"208.102.51.8","port":1080,"geolocation":{"country":"FR"}}
]`

	rewritten, ok := normalizeSourceBody([]byte(body))
	if !ok {
		t.Fatal("a proxifly JSON array must be recognised")
	}
	text := string(rewritten)
	for _, want := range []string{"socks5://208.102.51.6:58208", "http://208.102.51.7:8080", "socks5://208.102.51.8:1080"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rewritten JSON is missing %q:\n%s", want, text)
		}
	}
}

func TestParseSourceBodyPrefersStandardParser(t *testing.T) {
	// A real subscription must go through the standard parser and keep working.
	body := "vless://11111111-2222-3333-4444-555555555555@203.0.113.90:443?type=tcp#demo\n" +
		"192.0.2.50:8080\n"

	types := parsedTypes(t, body)
	if _, ok := types["vless demo"]; !ok {
		t.Fatalf("the URI line was not parsed by the standard parser: %#v", types)
	}
}

func TestParseSourceBodyFallsBackOnlyWhenNeeded(t *testing.T) {
	// Whatever path handles this body, all three endpoints must survive.
	body := "Donation list\n" +
		"203.0.113.60:1080\n" +
		"203.0.113.61:1080\n" +
		"203.0.113.62:1080\n"

	parsed, err := parseSourceBody([]byte(body))
	if err != nil {
		t.Fatalf("parseSourceBody() error = %v", err)
	}
	if len(parsed) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(parsed))
	}
}

func TestParseSourceBodyKeepsOriginalErrorWhenFallbackFails(t *testing.T) {
	if _, err := parseSourceBody([]byte("<html>completely unrelated</html>")); err == nil {
		t.Fatal("an unrecognised body must still report the original parse error")
	}
}

func TestDetectSchemeHint(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{"SOCKS5 Proxy list", "socks5"},
		{"socks4 list", "socks4"},
		{"HTTP(S) Proxy list", "http"},
		{"just some text", "http"},
	}
	for _, tc := range cases {
		if got := detectSchemeHint([]byte(tc.body)); got != tc.want {
			t.Fatalf("detectSchemeHint(%q) = %q, want %q", tc.body, got, tc.want)
		}
	}
}

func TestNormalizeFallbackScheme(t *testing.T) {
	cases := map[string]string{
		"socks5": "socks5", "SOCKS5H": "socks5", "socks4a": "socks4",
		"https": "https", "HTTP": "http", "vmess": "", "": "",
	}
	for in, want := range cases {
		if got := normalizeFallbackScheme(in); got != want {
			t.Fatalf("normalizeFallbackScheme(%q) = %q, want %q", in, got, want)
		}
	}
}
