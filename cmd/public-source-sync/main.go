package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"prism/internal/netutil"
	"prism/internal/publicsource"
)

const (
	// defaultMaxUploadBytes stays below Prism's default PRISM_API_MAX_BODY_BYTES
	// (1 MiB) so the sync payload is never rejected with 413.
	defaultMaxUploadBytes = 900 * 1024
	// defaultEnvFile mirrors Resin's own behaviour: an optional .env in the
	// working directory is loaded before the environment is read.
	defaultEnvFile = ".env"
	// minGistCacheTTL stops discovery from hammering GitHub more than once per
	// five minutes, even when the sync interval is shorter.
	minGistCacheTTL = 5 * time.Minute
)

// loadDotenvFile loads variables from path. A missing file is not an error, so
// the same binary also works with plain environment variables (systemd, docker,
// or an exported shell).
func loadDotenvFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := godotenv.Load(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("load %s: %w", path, err)
	}
	return nil
}

// envCompat reads name and falls back to the upstream legacy spelling, the same
// rule internal/config applies to its PRISM_/RESIN_ pair. The upstream spelling
// stays accepted so one .env keeps working for both binaries.
func envCompat(name, legacy string) (value string, source string) {
	if value = strings.TrimSpace(os.Getenv(name)); value != "" {
		return value, name
	}
	if value = strings.TrimSpace(os.Getenv(legacy)); value != "" {
		return value, legacy
	}
	return "", name
}

func main() {
	if err := loadDotenvFile(envOr("PUBLIC_SOURCE_ENV_FILE", defaultEnvFile)); err != nil {
		log.Fatalf("config: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	downloader := netutil.NewDirectDownloader(
		func() time.Duration { return cfg.fetchTimeout },
		func() string { return "Prism/PublicSourceSync/1.0" },
	)

	collectorCfg := publicsource.Config{
		Enabled:          true,
		Interval:         cfg.interval,
		Sources:          cfg.sources,
		MaxNodes:         cfg.maxNodes,
		SourceTimeout:    cfg.fetchTimeout,
		MaxContentBytes:  cfg.maxUploadBytes,
		OnAttempt:        logAttempt,
		ExtraContentName: "github-gist",
	}

	if cfg.gistEnabled {
		discovery := publicsource.NewGistDiscovery(publicsource.GistDiscoveryConfig{
			Queries:  cfg.gistQueries,
			MaxPages: cfg.gistMaxPages,
			MaxGists: cfg.gistMaxGists,
			MaxNodes: cfg.maxNodes,
			CacheTTL: cfg.gistCacheTTL,
		})
		collectorCfg.ExtraContent = discovery.Discover
	}

	// Two ways to get the aggregate into Resin:
	//
	//   pull mode  (PUBLIC_SOURCE_SERVE_ADDR set) — serve the payload over HTTP
	//              and let Resin fetch it as a remote subscription. No admin
	//              token is needed at all, and Resin controls the cadence.
	//   push mode  (default) — upload into a local subscription over the Admin
	//              API, which needs an admin token.
	var (
		collector *publicsource.Collector
		server    *http.Server
	)
	if cfg.serveAddr != "" {
		collector = publicsource.NewCollector(collectorCfg, downloader, nil)
		server = &http.Server{
			Addr:              cfg.serveAddr,
			Handler:           newSnapshotServer(collector, cfg.servePath),
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      60 * time.Second,
		}
	} else {
		client, err := publicsource.NewResinClient(cfg.resinURL, cfg.adminToken, &http.Client{Timeout: 45 * time.Second})
		if err != nil {
			log.Fatalf("resin client: %v", err)
		}
		collector = publicsource.NewCollector(collectorCfg, downloader, func(snapshot publicsource.Snapshot) error {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			opts := publicsource.LocalSubscriptionOptions{
				Ephemeral:               cfg.ephemeral,
				IncrementalAliveNodes:   cfg.incrementalAliveNodes,
				EphemeralNodeEvictDelay: cfg.evictDelay,
			}
			if err := client.UpsertLocalSubscription(ctx, cfg.subscriptionName, snapshot.Content, cfg.interval, opts); err != nil {
				return err
			}
			log.Printf("synced subscription=%q sources=%d candidates=%d unique_nodes=%d bytes=%d degraded=%t",
				cfg.subscriptionName, snapshot.SourceCount, snapshot.CandidateCount, snapshot.UniqueNodes, len(snapshot.Content), snapshot.Degraded)
			return nil
		})
	}

	collector.Start()

	if server != nil {
		go func() {
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("serve error: %v", err)
			}
		}()
		log.Printf("public source sync serving; addr=%s path=%s interval=%s presets=%s static_sources=%d gist_enabled=%t gist_queries=%d max_nodes=%d max_upload_bytes=%d",
			cfg.serveAddr, cfg.servePath, cfg.interval, presetLabel(cfg.presets), len(cfg.sources), cfg.gistEnabled, len(cfg.gistQueries), cfg.maxNodes, cfg.maxUploadBytes)
		log.Printf("now create a remote subscription in Resin pointing at this host%s with update_interval <= %s; no admin token is needed for that", cfg.servePath, cfg.interval)
	} else {
		log.Printf("public source sync started; resin=%s admin_token_source=%s subscription=%q interval=%s ephemeral=%t incremental_alive_nodes=%t evict_delay=%s presets=%s static_sources=%d gist_enabled=%t gist_queries=%d max_nodes=%d max_upload_bytes=%d",
			cfg.resinURL, cfg.tokenSource, cfg.subscriptionName, cfg.interval, cfg.ephemeral, cfg.incrementalAliveNodes, cfg.evictDelay, presetLabel(cfg.presets), len(cfg.sources), cfg.gistEnabled, len(cfg.gistQueries), cfg.maxNodes, cfg.maxUploadBytes)
	}

	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-stopCtx.Done()
	collector.Stop()
	if server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = server.Shutdown(shutdownCtx)
		cancel()
	}
	log.Println("public source sync stopped")
}

func presetLabel(presets []string) string {
	if len(presets) == 0 {
		return "none"
	}
	return strings.Join(presets, "+")
}

// logAttempt surfaces every refresh outcome, including failures, so the process
// never fails silently.
func logAttempt(attempt publicsource.Attempt) {
	for _, result := range attempt.Results {
		if result.Error != "" {
			log.Printf("source=%s candidates=%d accepted=%d cached=%t error=%s",
				result.URL, result.CandidateCount, result.AcceptedCount, result.UsedCache, result.Error)
		}
	}
	if attempt.Err != nil {
		log.Printf("refresh failed: %v", attempt.Err)
		return
	}
	if !attempt.Published {
		log.Printf("refresh produced no publishable snapshot")
		return
	}
	log.Printf("refresh ok: candidates=%d unique_nodes=%d bytes=%d", attempt.CandidateCount, attempt.UniqueNodes, attempt.ContentBytes)
}

type config struct {
	resinURL              string
	adminToken            string
	tokenSource           string
	subscriptionName      string
	interval              time.Duration
	fetchTimeout          time.Duration
	maxNodes              int
	ephemeral             bool
	incrementalAliveNodes bool
	evictDelay            time.Duration
	maxUploadBytes        int
	presets               []string
	sources               []string

	gistEnabled  bool
	gistQueries  []string
	gistMaxPages int
	gistMaxGists int
	gistCacheTTL time.Duration
	serveAddr    string
	servePath    string
}

func loadConfig() (config, error) {
	// These two name the Prism instance the sync pushes into, so they follow
	// Prism's own PRISM_ spelling while the upstream RESIN_ spelling stays
	// accepted. The admin token may also come from the shared PRISM_ADMIN_TOKEN
	// (or its deprecated RESIN_ spelling): a deployment that shares a single
	// .env with Prism only defines that one, so one config file works for both
	// binaries.
	resinURL, _ := envCompat("PUBLIC_SOURCE_PRISM_URL", "PUBLIC_SOURCE_RESIN_URL")
	adminToken, tokenSource := envCompat("PUBLIC_SOURCE_PRISM_ADMIN_TOKEN", "PUBLIC_SOURCE_RESIN_ADMIN_TOKEN")
	if sharedToken, sharedSource := envCompat("PRISM_ADMIN_TOKEN", "RESIN_ADMIN_TOKEN"); adminToken == "" && sharedToken != "" {
		adminToken, tokenSource = sharedToken, sharedSource
	}

	// Pull mode serves content for Resin to fetch, so it never talks to the Admin
	// API and needs neither a URL nor a token.
	serveAddr := strings.TrimSpace(os.Getenv("PUBLIC_SOURCE_SERVE_ADDR"))
	servePath := strings.TrimSpace(os.Getenv("PUBLIC_SOURCE_SERVE_PATH"))
	if servePath == "" {
		servePath = "/sub"
	}
	if serveAddr == "" && (resinURL == "" || adminToken == "") {
		return config{}, errors.New(
			"set PUBLIC_SOURCE_PRISM_URL plus an admin token to push into Prism, or set " +
				"PUBLIC_SOURCE_SERVE_ADDR to serve the aggregate for Prism to pull instead. " +
				"The token may also come from PRISM_ADMIN_TOKEN (accepted for a shared .env)")
	}

	interval, err := parseDurationEnv("PUBLIC_SOURCE_SYNC_INTERVAL", 30*time.Minute)
	if err != nil {
		return config{}, err
	}
	if interval < 30*time.Second {
		return config{}, errors.New("PUBLIC_SOURCE_SYNC_INTERVAL must be at least 30s")
	}

	fetchTimeout, err := parseDurationEnv("PUBLIC_SOURCE_FETCH_TIMEOUT", 30*time.Second)
	if err != nil {
		return config{}, err
	}
	if fetchTimeout <= 0 {
		return config{}, errors.New("PUBLIC_SOURCE_FETCH_TIMEOUT must be positive")
	}

	maxNodes, err := parseIntEnv("PUBLIC_SOURCE_MAX_NODES", 1000)
	if err != nil || maxNodes < 0 {
		return config{}, errors.New("PUBLIC_SOURCE_MAX_NODES must be a non-negative integer")
	}

	maxUploadBytes, err := parseIntEnv("PUBLIC_SOURCE_MAX_UPLOAD_BYTES", defaultMaxUploadBytes)
	if err != nil || maxUploadBytes <= 0 {
		return config{}, errors.New("PUBLIC_SOURCE_MAX_UPLOAD_BYTES must be a positive integer")
	}

	gistEnabled, err := parseBoolEnv("PUBLIC_SOURCE_GIST_ENABLED", false)
	if err != nil {
		return config{}, err
	}

	gistQueries, err := parseQueries(os.Getenv("PUBLIC_SOURCE_GIST_QUERIES"))
	if err != nil {
		return config{}, err
	}

	gistMaxPages, err := parseIntEnv("PUBLIC_SOURCE_GIST_MAX_PAGES", 2)
	if err != nil || gistMaxPages <= 0 {
		return config{}, errors.New("PUBLIC_SOURCE_GIST_MAX_PAGES must be a positive integer")
	}

	gistMaxGists, err := parseIntEnv("PUBLIC_SOURCE_GIST_MAX_GISTS", 40)
	if err != nil || gistMaxGists <= 0 {
		return config{}, errors.New("PUBLIC_SOURCE_GIST_MAX_GISTS must be a positive integer")
	}

	// Gist discoveries are cached so one cycle never searches GitHub twice. An
	// unset value tracks the sync interval but is clamped up to the floor: a
	// short interval must not turn into a config error the user never caused.
	gistCacheTTL, err := parseDurationEnv("PUBLIC_SOURCE_GIST_CACHE_TTL", 0)
	if err != nil {
		return config{}, err
	}
	if gistCacheTTL <= 0 {
		gistCacheTTL = interval
		if gistCacheTTL < minGistCacheTTL {
			gistCacheTTL = minGistCacheTTL
		}
	} else if gistCacheTTL < minGistCacheTTL {
		return config{}, errors.New("PUBLIC_SOURCE_GIST_CACHE_TTL must be at least 5m")
	}

	// These three mirror Resin's own subscription settings. The defaults suit a
	// public list that churns: keep healthy nodes that rotated off the list, and
	// sweep the ones that have been failing for a while.
	ephemeral, err := parseBoolEnv("PUBLIC_SOURCE_EPHEMERAL", true)
	if err != nil {
		return config{}, err
	}
	incrementalAliveNodes, err := parseBoolEnv("PUBLIC_SOURCE_INCREMENTAL_ALIVE_NODES", true)
	if err != nil {
		return config{}, err
	}
	evictDelay, err := parseDurationEnv("PUBLIC_SOURCE_EPHEMERAL_EVICT_DELAY", 15*time.Minute)
	if err != nil {
		return config{}, err
	}
	if evictDelay < 0 {
		return config{}, errors.New("PUBLIC_SOURCE_EPHEMERAL_EVICT_DELAY must not be negative")
	}
	presets, err := parsePresets(os.Getenv("PUBLIC_SOURCE_PRESETS"))
	if err != nil {
		return config{}, err
	}
	presetURLs, err := publicsource.PresetURLs(presets)
	if err != nil {
		return config{}, err
	}

	custom, err := parseSources(os.Getenv("PUBLIC_SOURCE_URLS"))
	if err != nil {
		return config{}, err
	}
	sources := mergeSources(presetURLs, custom)
	if len(sources) == 0 && !gistEnabled {
		return config{}, errors.New(
			"no static source configured: set PUBLIC_SOURCE_PRESETS (e.g. \"all\") " +
				"and/or PUBLIC_SOURCE_URLS, or enable PUBLIC_SOURCE_GIST_ENABLED")
	}

	return config{
		resinURL:              resinURL,
		adminToken:            adminToken,
		tokenSource:           tokenSource,
		subscriptionName:      envOr("PUBLIC_SOURCE_SUBSCRIPTION_NAME", "Public sources"),
		interval:              interval,
		fetchTimeout:          fetchTimeout,
		maxNodes:              maxNodes,
		maxUploadBytes:        maxUploadBytes,
		presets:               presets,
		sources:               sources,
		gistEnabled:           gistEnabled,
		gistQueries:           gistQueries,
		gistMaxPages:          gistMaxPages,
		gistMaxGists:          gistMaxGists,
		gistCacheTTL:          gistCacheTTL,
		ephemeral:             ephemeral,
		incrementalAliveNodes: incrementalAliveNodes,
		evictDelay:            evictDelay,
		serveAddr:             serveAddr,
		servePath:             servePath,
	}, nil
}

// parsePresets parses the built-in source preset selection. It accepts a
// comma-separated list ("http,nodes") and, for symmetry with PUBLIC_SOURCE_URLS,
// a JSON array (["http","nodes"]).
//
// An unset value keeps the historical single-source default. An explicitly
// empty value is treated as unset so an empty line in a .env file cannot quietly
// disable every preset; use "none" for that.
func parsePresets(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return publicsource.DefaultPresetNames(), nil
	}

	var names []string
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &names); err != nil {
			return nil, fmt.Errorf("PUBLIC_SOURCE_PRESETS must be a comma-separated list or a JSON string array: %w", err)
		}
	} else {
		names = strings.Split(trimmed, ",")
	}

	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("PUBLIC_SOURCE_PRESETS contains no preset name")
	}
	return out, nil
}

// parseSources parses the extra, user-supplied source URLs. Unset means no
// custom source: the built-in presets already carry the common public lists.
func parseSources(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var sources []string
	if err := json.Unmarshal([]byte(raw), &sources); err != nil {
		return nil, fmt.Errorf("PUBLIC_SOURCE_URLS must be a JSON string array: %w", err)
	}
	out := make([]string, 0, len(sources))
	for _, source := range sources {
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
			return nil, fmt.Errorf("PUBLIC_SOURCE_URLS contains non-HTTP(S) URL: %q", source)
		}
		out = append(out, source)
	}
	return out, nil
}

// mergeSources joins preset and custom URLs, preserving order and dropping
// duplicates so a URL present in both is only fetched once.
func mergeSources(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, group := range groups {
		for _, url := range group {
			if _, dup := seen[url]; dup {
				continue
			}
			seen[url] = struct{}{}
			out = append(out, url)
		}
	}
	return out
}

// parseQueries parses the gist search terms, defaulting to the protocol
// prefixes that actually identify node lists.
func parseQueries(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return publicsource.DefaultGistQueries(), nil
	}
	var queries []string
	if err := json.Unmarshal([]byte(raw), &queries); err != nil {
		return nil, fmt.Errorf("PUBLIC_SOURCE_GIST_QUERIES must be a JSON string array: %w", err)
	}
	out := make([]string, 0, len(queries))
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query != "" {
			out = append(out, query)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("PUBLIC_SOURCE_GIST_QUERIES must contain at least one non-empty query")
	}
	return out, nil
}

func parseDurationEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s is invalid: %w", key, err)
	}
	return parsed, nil
}

func parseIntEnv(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s is invalid: %w", key, err)
	}
	return parsed, nil
}

func parseBoolEnv(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s is invalid: %w", key, err)
	}
	return parsed, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
