package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prism/internal/intel/providers"
	"prism/internal/netutil"
)

// WP09 §3 API surface: GET /api/v1/intel/providers reports the state of the
// offline databases and POST .../actions/refresh runs a bounded manual
// download. No test touches the network: the vendor endpoints are served by
// httptest and the cache directory is a t.TempDir().

// wireIntelGeoForTest attaches an offline database refresher to the settings
func intelGeoItem(t *testing.T, item map[string]any) map[string]any {
	t.Helper()
	database, ok := item["database"].(map[string]any)
	if !ok {
		t.Fatalf("the provider has no database block: %v", item)
	}
	return database
}

func intelGeoFile(t *testing.T, item map[string]any, name string) map[string]any {
	t.Helper()
	database := intelGeoItem(t, item)
	files, ok := database["files"].([]any)
	if !ok || len(files) == 0 {
		t.Fatalf("the database block has no files: %v", database)
	}
	for _, raw := range files {
		file, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("file = %T, want map", raw)
		}
		if file["name"] == name {
			return file
		}
	}
	t.Fatalf("the database block has no file %q: %v", name, files)
	return nil
}

// TestIntelOfflineDatabaseStateAndRefresh covers the offline database surface
// of WP09 §3 end to end: the reported state, the manual refresh action and the
// documented error cases.
func TestIntelOfflineDatabaseStateAndRefresh(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	svc, registry, _ := wireIntelSettingsForTest(t, cp)
	// The settings surface renders the spec defaults until a row is persisted.
	registry.Apply(providers.ResolveSettings(registry.Specs(), nil, func(string) string { return "" }))

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		// Every vendor answers with a body that is not a database.
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	geoDir := filepath.Join(t.TempDir(), "geo")
	manager := providers.NewGeoManager(providers.GeoManagerOptions{
		Dir:        geoDir,
		Downloader: geoTestDownloader(server),
		BaseURLs:   providers.GeoDBBaseURLs{DBIP: server.URL + "/free/", MaxMind: server.URL + "/geoip/", IPInfo: server.URL + "/data/ipinfo_lite.mmdb"},
		Setting: func(id string) (providers.Setting, bool) {
			return registry.Setting(id)
		},
		Now:  time.Now,
		Logf: func(string, ...any) {},
	})
	if manager == nil {
		t.Fatal("the refresher was not built")
	}
	defer manager.Stop()
	svc.ProviderSettings().SetDatabases(manager)

	rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/intel/providers", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", rec.Code, rec.Body.String())
	}
	list := decodeJSONMap(t, rec)
	dbip := intelProviderItem(t, list, "dbip_lite")
	database := intelGeoItem(t, dbip)
	if database["installed"] != false || database["ready"] != true {
		t.Fatalf("database state before the download: %v", database)
	}
	if database["refresh_interval"] != "168h0m0s" {
		t.Fatalf("refresh_interval = %v", database["refresh_interval"])
	}
	cityFile := intelGeoFile(t, dbip, "dbip-city-lite.mmdb")
	if cityFile["installed"] != false || cityFile["size_bytes"].(float64) != 0 {
		t.Fatalf("file state before the download: %v", cityFile)
	}

	// A provider without a downloaded database reports no block at all, and the
	// online sources keep their shape.
	if _, ok := intelProviderItem(t, list, "geo_country")["database"]; ok {
		t.Fatal("geo_country must not report a downloaded database")
	}
	if _, ok := intelProviderItem(t, list, "proxycheck")["database"]; ok {
		t.Fatal("proxycheck must not report a downloaded database")
	}

	// The manual action runs one bounded download per file and reports what
	// happened; a vendor failure is a recorded failure, not an API error.
	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/intel/providers/dbip_lite/actions/refresh", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if requests == 0 {
		t.Fatal("the refresh did not request the vendor")
	}
	refresh := decodeJSONMap(t, rec)
	if refresh["provider_id"] != "dbip_lite" || refresh["outcome"] != "failed" || refresh["duration"] == "" {
		t.Fatalf("refresh result: %v", refresh)
	}
	files, ok := refresh["files"].([]any)
	if !ok || len(files) != 2 {
		t.Fatalf("refresh files = %v", refresh["files"])
	}
	first := files[0].(map[string]any)
	if first["error_code"] != "GEO_HTTP_STATUS" || first["reason"] == "" {
		t.Fatalf("refresh file result: %v", first)
	}
	if cacheControl := rec.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
	}

	// An installed file shows its size and age; the outcome of the attempt is
	// still visible.
	body := make([]byte, 2048)
	if err := os.MkdirAll(geoDir, 0o700); err != nil {
		t.Fatalf("create geo dir: %v", err)
	}
	for _, name := range []string{"dbip-city-lite.mmdb", "dbip-asn-lite.mmdb"} {
		if err := os.WriteFile(filepath.Join(geoDir, name), body, 0o600); err != nil {
			t.Fatalf("write installed database %s: %v", name, err)
		}
	}
	rec = doJSONRequest(t, srv, http.MethodGet, "/api/v1/intel/providers?limit=50", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	dbip = intelProviderItem(t, decodeJSONMap(t, rec), "dbip_lite")
	database = intelGeoItem(t, dbip)
	if database["installed"] != true {
		t.Fatalf("an installed database must be reported: %v", database)
	}
	cityFile = intelGeoFile(t, dbip, "dbip-city-lite.mmdb")
	if cityFile["size_bytes"].(float64) != float64(len(body)) || cityFile["updated_at_ns"].(float64) == 0 ||
		cityFile["age"] == "" || cityFile["last_outcome"] != "failed" || cityFile["error_code"] != "GEO_HTTP_STATUS" {
		t.Fatalf("file state: %v", cityFile)
	}
	if cityFile["reason"] == "" {
		t.Fatalf("the recorded reason is missing: %v", cityFile)
	}

	// A data source that needs a credential reports why it is not downloaded.
	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/intel/providers/maxmind_geolite2/actions/refresh", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("maxmind refresh status = %d, body=%s", rec.Code, rec.Body.String())
	}
	skipped := decodeJSONMap(t, rec)
	if skipped["outcome"] != "skipped" {
		t.Fatalf("an unconfigured MaxMind must be skipped: %v", skipped)
	}
	skippedFiles := skipped["files"].([]any)
	if entry := skippedFiles[0].(map[string]any); entry["error_code"] != "GEO_NOT_READY" || !strings.Contains(entry["reason"].(string), "license_key") {
		t.Fatalf("skip reason: %v", entry)
	}

	// The documented error cases of the action.
	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/intel/providers/geo_country/actions/refresh", nil, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("refresh without a database = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/intel/providers/nope/actions/refresh", nil, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("refresh of an unknown provider = %d, want 404", rec.Code)
	}
	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/intel/providers/dbip_lite/actions/refresh", nil, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh without a token = %d, want 401", rec.Code)
	}
}

// TestIntelOfflineDatabaseNeverReturnsCredentials keeps the R6 rule of the new
// surface: neither the state nor the refresh action repeats a stored key.
func TestIntelOfflineDatabaseNeverReturnsCredentials(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	svc, registry, _ := wireIntelSettingsForTest(t, cp)
	registry.Apply(providers.ResolveSettings(registry.Specs(), nil, func(name string) string {
		switch name {
		case "PRISM_MAXMIND_ACCOUNT_ID":
			return "913370"
		case "PRISM_MAXMIND_LICENSE_KEY":
			return testIntelKey
		case "PRISM_IPINFO_TOKEN":
			return testIntelKey
		default:
			return ""
		}
	}))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	manager := providers.NewGeoManager(providers.GeoManagerOptions{
		Dir:        filepath.Join(t.TempDir(), "geo"),
		Downloader: geoTestDownloader(server),
		BaseURLs: providers.GeoDBBaseURLs{
			DBIP:    server.URL + "/free/",
			MaxMind: server.URL + "/geoip/",
			IPInfo:  server.URL + "/data/ipinfo_lite.mmdb",
		},
		Setting: func(id string) (providers.Setting, bool) {
			return registry.Setting(id)
		},
		Now:  time.Now,
		Logf: func(string, ...any) {},
	})
	if manager == nil {
		t.Fatal("the refresher was not built")
	}
	defer manager.Stop()
	svc.ProviderSettings().SetDatabases(manager)

	for _, call := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/intel/providers"},
		{http.MethodPost, "/api/v1/intel/providers/dbip_lite/actions/refresh"},
		{http.MethodPost, "/api/v1/intel/providers/maxmind_geolite2/actions/refresh"},
		{http.MethodPost, "/api/v1/intel/providers/ipinfo_lite/actions/refresh"},
	} {
		rec := doJSONRequest(t, srv, call.method, call.path, nil, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d, body=%s", call.method, call.path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), testIntelKey) {
			t.Fatalf("%s %s leaked the credential", call.method, call.path)
		}
		if _, err := json.Marshal(decodeJSONMap(t, rec)); err != nil {
			t.Fatalf("decode %s %s: %v", call.method, call.path, err)
		}
	}
}

// TestIntelOfflineDatabaseStatusWithoutRefresher keeps the degraded answer
// honest: without a refresher the field is simply absent.
func TestIntelOfflineDatabaseStatusWithoutRefresher(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	_, registry, _ := wireIntelSettingsForTest(t, cp)
	registry.Apply(providers.ResolveSettings(registry.Specs(), nil, func(string) string { return "" }))

	rec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/intel/providers", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := intelProviderItem(t, decodeJSONMap(t, rec), "dbip_lite")["database"]; ok {
		t.Fatal("a provider without a refresher must not report a database block")
	}
	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/intel/providers/dbip_lite/actions/refresh", nil, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("refresh without a refresher = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
}

// geoTestDownloader bounds the downloads of these tests the way the running
// application does: one request timeout and the database size bound (R4).
func geoTestDownloader(server *httptest.Server) netutil.Downloader {
	direct := netutil.NewDirectDownloader(
		func() time.Duration { return 5 * time.Second },
		func() string { return "prism-test" },
	)
	direct.Client = server.Client()
	direct.MaxBodyBytes = providers.GeoDBMaxDownloadBytes
	return direct
}
