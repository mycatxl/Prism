package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prism/internal/publicsource"
)

// setBaseEnv pins every variable loadConfig reads so a developer's shell or CI
// environment cannot leak into the assertions.
func setBaseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PUBLIC_SOURCE_PRISM_URL", "http://127.0.0.1:2260")
	t.Setenv("PUBLIC_SOURCE_PRISM_ADMIN_TOKEN", "admin")
	// Pinned so an ambient shared admin token on the developer's machine
	// cannot change the outcome of the token-source tests, and so the
	// deprecated spellings never leak in either.
	t.Setenv("PRISM_ADMIN_TOKEN", "")
	t.Setenv("RESIN_ADMIN_TOKEN", "")
	t.Setenv("PUBLIC_SOURCE_RESIN_URL", "")
	t.Setenv("PUBLIC_SOURCE_RESIN_ADMIN_TOKEN", "")
	t.Setenv("PUBLIC_SOURCE_EPHEMERAL", "")
	t.Setenv("PUBLIC_SOURCE_INCREMENTAL_ALIVE_NODES", "")
	t.Setenv("PUBLIC_SOURCE_EPHEMERAL_EVICT_DELAY", "")
	t.Setenv("PUBLIC_SOURCE_SERVE_ADDR", "")
	t.Setenv("PUBLIC_SOURCE_SERVE_PATH", "")
	t.Setenv("PUBLIC_SOURCE_PRESETS", "classic")
	t.Setenv("PUBLIC_SOURCE_URLS", "")
	t.Setenv("PUBLIC_SOURCE_SUBSCRIPTION_NAME", "")
	t.Setenv("PUBLIC_SOURCE_SYNC_INTERVAL", "")
	t.Setenv("PUBLIC_SOURCE_FETCH_TIMEOUT", "")
	t.Setenv("PUBLIC_SOURCE_MAX_NODES", "")
	t.Setenv("PUBLIC_SOURCE_MAX_UPLOAD_BYTES", "")
	t.Setenv("PUBLIC_SOURCE_GIST_ENABLED", "")
	t.Setenv("PUBLIC_SOURCE_GIST_QUERIES", "")
	t.Setenv("PUBLIC_SOURCE_GIST_MAX_PAGES", "")
	t.Setenv("PUBLIC_SOURCE_GIST_MAX_GISTS", "")
	t.Setenv("PUBLIC_SOURCE_GIST_CACHE_TTL", "")
}

func TestParseSources(t *testing.T) {
	sources, err := parseSources(`["https://example.com/a.txt","http://example.com/b.txt"]`)
	if err != nil || len(sources) != 2 {
		t.Fatalf("parseSources() = %#v, %v", sources, err)
	}
}

func TestParseSourcesSkipsBlankEntries(t *testing.T) {
	sources, err := parseSources(`["https://example.com/a.txt","","   "]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("blank entries must be skipped, got %#v", sources)
	}
}

func TestParseSourcesRejectsNonHTTP(t *testing.T) {
	if _, err := parseSources(`["ftp://example.com/a.txt"]`); err == nil {
		t.Fatal("expected error for non-HTTP source")
	}
}

func TestParseSourcesUnsetMeansNoCustomSource(t *testing.T) {
	sources, err := parseSources("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sources) != 0 {
		t.Fatalf("unset PUBLIC_SOURCE_URLS must add no source, got %#v", sources)
	}
}

func TestParsePresetsDefaultsToClassic(t *testing.T) {
	presets, err := parsePresets("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(presets) != 1 || presets[0] != publicsource.PresetClassic {
		t.Fatalf("parsePresets(\"\") = %#v, want [classic]", presets)
	}
}

func TestParsePresetsCommaSeparated(t *testing.T) {
	presets, err := parsePresets(" HTTP , Nodes ,, ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{publicsource.PresetHTTP, publicsource.PresetNodes}
	if len(presets) != len(want) {
		t.Fatalf("parsePresets() = %#v, want %#v", presets, want)
	}
	for i := range want {
		if presets[i] != want[i] {
			t.Fatalf("parsePresets()[%d] = %q, want %q", i, presets[i], want[i])
		}
	}
}

func TestParsePresetsJSONArray(t *testing.T) {
	presets, err := parsePresets(`["nodes","socks"]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(presets) != 2 || presets[0] != publicsource.PresetNodes || presets[1] != publicsource.PresetSOCKS {
		t.Fatalf("parsePresets() = %#v", presets)
	}
}

func TestParsePresetsRejectsJSONWithoutNames(t *testing.T) {
	if _, err := parsePresets(`["","   "]`); err == nil {
		t.Fatal("expected error for a preset list with no names")
	}
}

func TestMergeSourcesDropsDuplicates(t *testing.T) {
	merged := mergeSources(
		[]string{"https://a.example/x.txt", "https://b.example/y.txt"},
		[]string{"https://b.example/y.txt", "https://c.example/z.txt"},
	)
	want := []string{"https://a.example/x.txt", "https://b.example/y.txt", "https://c.example/z.txt"}
	if len(merged) != len(want) {
		t.Fatalf("mergeSources() = %#v, want %#v", merged, want)
	}
	for i := range want {
		if merged[i] != want[i] {
			t.Fatalf("mergeSources()[%d] = %q, want %q", i, merged[i], want[i])
		}
	}
}

func TestLoadConfigRequiresRemotePrism(t *testing.T) {
	t.Setenv("PUBLIC_SOURCE_PRISM_URL", "")
	t.Setenv("PUBLIC_SOURCE_PRISM_ADMIN_TOKEN", "")
	t.Setenv("PUBLIC_SOURCE_RESIN_URL", "")
	t.Setenv("PUBLIC_SOURCE_RESIN_ADMIN_TOKEN", "")
	t.Setenv("PRISM_ADMIN_TOKEN", "")
	t.Setenv("RESIN_ADMIN_TOKEN", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() unexpectedly succeeded")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	setBaseEnv(t)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.interval != 30*time.Minute || cfg.fetchTimeout != 30*time.Second {
		t.Fatalf("unexpected duration defaults: %+v", cfg)
	}
	if cfg.maxNodes != 1000 || cfg.maxUploadBytes != defaultMaxUploadBytes {
		t.Fatalf("unexpected size defaults: %+v", cfg)
	}
	if cfg.subscriptionName != "Public sources" {
		t.Fatalf("unexpected subscription name: %q", cfg.subscriptionName)
	}
	if cfg.gistEnabled {
		t.Fatal("gist discovery must be opt-in")
	}
}

func TestLoadConfigHonoursOverrides(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_SYNC_INTERVAL", "5m")
	t.Setenv("PUBLIC_SOURCE_FETCH_TIMEOUT", "2m")
	t.Setenv("PUBLIC_SOURCE_MAX_NODES", "42")
	t.Setenv("PUBLIC_SOURCE_MAX_UPLOAD_BYTES", "4096")
	t.Setenv("PUBLIC_SOURCE_SUBSCRIPTION_NAME", "Custom")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.interval != 5*time.Minute || cfg.fetchTimeout != 2*time.Minute {
		t.Fatalf("overrides ignored: %+v", cfg)
	}
	if cfg.maxNodes != 42 || cfg.maxUploadBytes != 4096 || cfg.subscriptionName != "Custom" {
		t.Fatalf("overrides ignored: %+v", cfg)
	}
}

func TestLoadConfigRejectsTooSmallInterval(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_SYNC_INTERVAL", "5s")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected error for interval below 30s")
	}
}

func TestLoadConfigRejectsUnknownPreset(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_PRESETS", "http,deffo-not-a-preset")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected error for an unknown preset name")
	}
}

func TestLoadConfigDefaultPresetsPreserveHistoricalSource(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_PRESETS", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("expected the default preset, got %v", err)
	}
	classic, err := publicsource.PresetURLs([]string{publicsource.PresetClassic})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.sources) != len(classic) || cfg.sources[0] != classic[0] {
		t.Fatalf("unexpected default sources: %#v", cfg.sources)
	}
	if !strings.Contains(cfg.sources[0], "TheSpeedX/PROXY-List") {
		t.Fatalf("default source is %q, want the TheSpeedX list", cfg.sources[0])
	}
}

func TestLoadConfigPresetSelectionExpands(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_PRESETS", "http,nodes")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want, err := publicsource.PresetURLs([]string{publicsource.PresetHTTP, publicsource.PresetNodes})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.sources) != len(want) {
		t.Fatalf("got %d sources, want %d", len(cfg.sources), len(want))
	}
	if len(cfg.presets) != 2 {
		t.Fatalf("cfg.presets = %#v", cfg.presets)
	}
}

func TestLoadConfigCustomSourcesAreAppended(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_PRESETS", "none")
	t.Setenv("PUBLIC_SOURCE_URLS", `["https://example.com/proxies.txt"]`)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.sources) != 1 || cfg.sources[0] != "https://example.com/proxies.txt" {
		t.Fatalf("unexpected sources: %#v", cfg.sources)
	}
}

func TestLoadConfigRequiresSomeSourceUnlessGistEnabled(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_PRESETS", "none")
	t.Setenv("PUBLIC_SOURCE_URLS", "[]")

	if _, err := loadConfig(); err == nil {
		t.Fatal("expected an error when no static source and gist discovery is off")
	}

	t.Setenv("PUBLIC_SOURCE_GIST_ENABLED", "true")
	if _, err := loadConfig(); err != nil {
		t.Fatalf("gist-only configuration must be valid, got %v", err)
	}
}

func TestLoadConfigRejectsInvalidGistTTL(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_GIST_CACHE_TTL", "1m")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected error for gist cache TTL below 5m")
	}
}

func TestLoadDotenvFileReadsValues(t *testing.T) {
	const key = "PUBLIC_SOURCE_DOTENV_PROBE"
	original, hadOriginal := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadOriginal {
			_ = os.Setenv(key, original)
			return
		}
		_ = os.Unsetenv(key)
	})

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("# comment\n"+key+"=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadDotenvFile(path); err != nil {
		t.Fatalf("loadDotenvFile: %v", err)
	}
	if got := os.Getenv(key); got != "from-dotenv" {
		t.Fatalf("%s = %q, want from-dotenv", key, got)
	}
}

func TestLoadDotenvFileMissingIsNotAnError(t *testing.T) {
	if err := loadDotenvFile(filepath.Join(t.TempDir(), "does-not-exist.env")); err != nil {
		t.Fatalf("a missing .env must be tolerated, got %v", err)
	}
}

func TestLoadDotenvFileEmptyPathIsNoop(t *testing.T) {
	if err := loadDotenvFile("   "); err != nil {
		t.Fatalf("an empty env file path must be a no-op, got %v", err)
	}
}

func TestLoadConfigClampsGistTTLForShortIntervals(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_GIST_ENABLED", "true")
	t.Setenv("PUBLIC_SOURCE_SYNC_INTERVAL", "30s")
	t.Setenv("PUBLIC_SOURCE_GIST_CACHE_TTL", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("a short sync interval must clamp the gist cache TTL, got %v", err)
	}
	if cfg.gistCacheTTL != 5*time.Minute {
		t.Fatalf("gistCacheTTL = %v, want 5m", cfg.gistCacheTTL)
	}
}

func TestLoadConfigTracksIntervalWhenLongEnough(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_SYNC_INTERVAL", "45m")
	t.Setenv("PUBLIC_SOURCE_GIST_CACHE_TTL", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.gistCacheTTL != 45*time.Minute {
		t.Fatalf("gistCacheTTL = %v, want 45m", cfg.gistCacheTTL)
	}
}

func TestLoadConfigFallsBackToSharedAdminToken(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_PRISM_ADMIN_TOKEN", "")
	t.Setenv("PRISM_ADMIN_TOKEN", "shared-token")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("a shared .env must work with PRISM_ADMIN_TOKEN, got %v", err)
	}
	if cfg.adminToken != "shared-token" {
		t.Fatal("the fallback token was not picked up")
	}
	if cfg.tokenSource != "PRISM_ADMIN_TOKEN" {
		t.Fatalf("tokenSource = %q, want PRISM_ADMIN_TOKEN", cfg.tokenSource)
	}
}

// The deprecated Resin spelling of the shared token must keep working, because
// a .env written before the rename still carries it.
func TestLoadConfigFallsBackToDeprecatedResinAdminToken(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_PRISM_ADMIN_TOKEN", "")
	t.Setenv("RESIN_ADMIN_TOKEN", "legacy-shared-token")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("RESIN_ADMIN_TOKEN must still be accepted, got %v", err)
	}
	if cfg.adminToken != "legacy-shared-token" || cfg.tokenSource != "RESIN_ADMIN_TOKEN" {
		t.Fatalf("token=%q source=%q", cfg.adminToken, cfg.tokenSource)
	}
}

func TestLoadConfigPrefersTheExplicitPublicSourceToken(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_PRISM_ADMIN_TOKEN", "explicit-token")
	t.Setenv("PRISM_ADMIN_TOKEN", "shared-token")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.adminToken != "explicit-token" {
		t.Fatal("the explicit token must win over the fallback")
	}
	if cfg.tokenSource != "PUBLIC_SOURCE_PRISM_ADMIN_TOKEN" {
		t.Fatalf("tokenSource = %q", cfg.tokenSource)
	}
}

func TestLoadConfigRequiresSomeToken(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_PRISM_ADMIN_TOKEN", "")
	t.Setenv("PRISM_ADMIN_TOKEN", "")
	t.Setenv("RESIN_ADMIN_TOKEN", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected an error when no admin token is configured at all")
	}
}

func TestAdminURLWithoutAPortIsAccepted(t *testing.T) {
	// Publishing Resin behind a domain means the URL carries no explicit port.
	// The client never required one; this locks that in.
	urls := []string{
		"https://rsp.example.com",
		"http://127.0.0.1",
		"https://rsp.example.com:8443",
	}
	for _, target := range urls {
		if _, err := publicsource.NewResinClient(target, "token", nil); err != nil {
			t.Fatalf("NewResinClient(%q) error = %v", target, err)
		}
	}
}

func TestAdminURLRejectsCredentials(t *testing.T) {
	if _, err := publicsource.NewResinClient("https://user:pass@rsp.example.com", "token", nil); err == nil {
		t.Fatal("expected an error for a URL carrying credentials")
	}
}

func TestLoadConfigSubscriptionModeDefaults(t *testing.T) {
	setBaseEnv(t)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ephemeral || !cfg.incrementalAliveNodes {
		t.Fatalf("ephemeral and incremental must default to on: %+v", cfg)
	}
	if cfg.evictDelay != 15*time.Minute {
		t.Fatalf("evictDelay = %v, want 15m", cfg.evictDelay)
	}
}

func TestLoadConfigSubscriptionModeOverrides(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_EPHEMERAL", "false")
	t.Setenv("PUBLIC_SOURCE_INCREMENTAL_ALIVE_NODES", "false")
	t.Setenv("PUBLIC_SOURCE_EPHEMERAL_EVICT_DELAY", "2h")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ephemeral || cfg.incrementalAliveNodes {
		t.Fatalf("overrides ignored: %+v", cfg)
	}
	if cfg.evictDelay != 2*time.Hour {
		t.Fatalf("evictDelay = %v, want 2h", cfg.evictDelay)
	}
}

func TestLoadConfigRejectsNegativeEvictDelay(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_EPHEMERAL_EVICT_DELAY", "-1m")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected an error for a negative evict delay")
	}
}

func TestLoadConfigServeModeNeedsNoPrismTarget(t *testing.T) {
	// Pull mode is the whole point of the serve endpoint: the sync never talks to
	// the Admin API, so it must not demand a URL or a token.
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_PRISM_URL", "")
	t.Setenv("PUBLIC_SOURCE_PRISM_ADMIN_TOKEN", "")
	t.Setenv("PUBLIC_SOURCE_RESIN_URL", "")
	t.Setenv("PUBLIC_SOURCE_RESIN_ADMIN_TOKEN", "")
	t.Setenv("PRISM_ADMIN_TOKEN", "")
	t.Setenv("RESIN_ADMIN_TOKEN", "")
	t.Setenv("PUBLIC_SOURCE_SERVE_ADDR", "127.0.0.1:8787")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("serve mode must not require a Prism target, got %v", err)
	}
	if cfg.serveAddr != "127.0.0.1:8787" {
		t.Fatalf("serveAddr = %q", cfg.serveAddr)
	}
	if cfg.servePath != "/sub" {
		t.Fatalf("servePath = %q, want the /sub default", cfg.servePath)
	}
}

func TestLoadConfigServePathOverride(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_SERVE_ADDR", "127.0.0.1:8787")
	t.Setenv("PUBLIC_SOURCE_SERVE_PATH", "/sub/deadbeef")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.servePath != "/sub/deadbeef" {
		t.Fatalf("servePath = %q", cfg.servePath)
	}
}

func TestLoadConfigRequiresEitherPrismOrServeTarget(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_PRISM_URL", "")
	t.Setenv("PUBLIC_SOURCE_PRISM_ADMIN_TOKEN", "")
	t.Setenv("PUBLIC_SOURCE_RESIN_URL", "")
	t.Setenv("PUBLIC_SOURCE_RESIN_ADMIN_TOKEN", "")
	t.Setenv("PRISM_ADMIN_TOKEN", "")
	t.Setenv("RESIN_ADMIN_TOKEN", "")
	t.Setenv("PUBLIC_SOURCE_SERVE_ADDR", "")

	if _, err := loadConfig(); err == nil {
		t.Fatal("expected an error when neither a Prism target nor a serve address is set")
	}
}

// The upstream Resin spelling of the two variables that name the target instance
// stays accepted, so a .env written for Resin keeps working after the rename.
func TestLoadConfigAcceptsDeprecatedResinSpelling(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("PUBLIC_SOURCE_PRISM_URL", "")
	t.Setenv("PUBLIC_SOURCE_PRISM_ADMIN_TOKEN", "")
	t.Setenv("PUBLIC_SOURCE_RESIN_URL", "http://127.0.0.1:2260")
	t.Setenv("PUBLIC_SOURCE_RESIN_ADMIN_TOKEN", "legacy-token")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("the deprecated RESIN spelling must still work, got %v", err)
	}
	if cfg.resinURL != "http://127.0.0.1:2260" || cfg.adminToken != "legacy-token" {
		t.Fatalf("legacy values were not picked up: url=%q token=%q", cfg.resinURL, cfg.adminToken)
	}
	if cfg.tokenSource != "PUBLIC_SOURCE_RESIN_ADMIN_TOKEN" {
		t.Fatalf("tokenSource = %q", cfg.tokenSource)
	}
}
