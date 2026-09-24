package providers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/oschwald/maxminddb-golang"

	"prism/internal/netutil"
)

// WP09 §3: the offline database refresher. Every test serves its files from
// httptest into a t.TempDir() cache directory; no test touches the network.

// --- minimal MaxMind database writer ---------------------------------------

// mmdbMetadataMarker separates the data section from the metadata of a MaxMind
// database (see the MaxMind DB format specification).
var mmdbMetadataMarker = []byte("\xab\xcd\xefMaxMind.com")

const (
	mmdbTypeString  = 2
	mmdbTypeUint16  = 5
	mmdbTypeUint32  = 6
	mmdbTypeMap     = 7
	mmdbTypeUint64  = 9
	mmdbTypeSlice   = 11
	mmdbRecordSize  = 24
	mmdbIPv4Bits    = 32
	mmdbSeparatorSz = 16
)

// mmdbControl renders the control byte(s) of one value. Types above "map" are
// written in the extended form of the format (a control byte of type 0 plus
// the type number reduced by 7).
func mmdbControl(typeNum byte, size int) []byte {
	switch {
	case size < 29 && typeNum > 7:
		return []byte{byte(size), typeNum - 7}
	case size < 29:
		return []byte{typeNum<<5 | byte(size)}
	case size < 29+256 && typeNum > 7:
		return []byte{29, typeNum - 7, byte(size - 29)}
	case size < 29+256:
		return []byte{typeNum<<5 | 29, byte(size - 29)}
	default:
		panic("mmdb fixture: value too large")
	}
}

func mmdbString(value string) []byte {
	return append(mmdbControl(mmdbTypeString, len(value)), value...)
}

func mmdbUint(value uint64, width int) []byte {
	typeNum := byte(mmdbTypeUint32)
	switch width {
	case 2:
		typeNum = mmdbTypeUint16
	case 8:
		typeNum = mmdbTypeUint64
	}
	out := mmdbControl(typeNum, width)
	for shift := width - 1; shift >= 0; shift-- {
		out = append(out, byte(value>>(8*uint(shift))))
	}
	return out
}

// mmdbField renders one map entry.
func mmdbField(key string, value []byte) [2][]byte {
	return [2][]byte{mmdbString(key), value}
}

func mmdbMap(entries ...[2][]byte) []byte {
	out := mmdbControl(mmdbTypeMap, len(entries))
	for _, entry := range entries {
		out = append(out, entry[0]...)
		out = append(out, entry[1]...)
	}
	return out
}

func mmdbSlice(items ...[]byte) []byte {
	out := mmdbControl(mmdbTypeSlice, len(items))
	for _, item := range items {
		out = append(out, item...)
	}
	return out
}

// buildMMDB renders a valid MaxMind database with exactly one IPv4 prefix. The
// search tree is a chain of one node per address bit, so the record of the
// prefix is reachable and every other address stays empty.
func buildMMDB(t *testing.T, databaseType, network string, record []byte) []byte {
	t.Helper()
	prefix, err := netip.ParsePrefix(network)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() <= 0 || prefix.Bits() > mmdbIPv4Bits {
		t.Fatalf("fixture prefix %q: expected an IPv4 prefix such as 8.8.8.8/32", network)
	}
	address := prefix.Addr().As4()
	bits := prefix.Bits()
	nodeCount := uint64(bits)

	// Every record starts as "empty" (= nodeCount, meaning no data). The chain
	// then walks the prefix bits and ends in the data pointer of the record,
	// which is nodeCount + separator + data offset 0.
	nodes := make([][2]uint64, bits)
	for index := range nodes {
		nodes[index] = [2]uint64{nodeCount, nodeCount}
	}
	for index := 0; index < bits; index++ {
		bit := (address[index/8] >> (7 - uint(index%8))) & 1
		if index == bits-1 {
			nodes[index][bit] = nodeCount + mmdbSeparatorSz
			continue
		}
		nodes[index][bit] = uint64(index + 1)
	}

	tree := make([]byte, 0, bits*6)
	for _, node := range nodes {
		for _, value := range node {
			tree = append(tree, byte(value>>16), byte(value>>8), byte(value))
		}
	}

	metadata := mmdbMap(
		mmdbField("binary_format_major_version", mmdbUint(2, 2)),
		mmdbField("binary_format_minor_version", mmdbUint(0, 2)),
		mmdbField("build_epoch", mmdbUint(uint64(testNow.Unix()), 4)),
		mmdbField("database_type", mmdbString(databaseType)),
		mmdbField("description", mmdbMap(mmdbField("en", mmdbString("Prism test fixture")))),
		mmdbField("ip_version", mmdbUint(4, 2)),
		mmdbField("languages", mmdbSlice(mmdbString("en"))),
		mmdbField("node_count", mmdbUint(nodeCount, 4)),
		mmdbField("record_size", mmdbUint(mmdbRecordSize, 2)),
	)

	out := make([]byte, 0, len(tree)+mmdbSeparatorSz+len(record)+len(metadata)+len(mmdbMetadataMarker))
	out = append(out, tree...)
	out = append(out, make([]byte, mmdbSeparatorSz)...)
	out = append(out, record...)
	out = append(out, mmdbMetadataMarker...)
	out = append(out, metadata...)
	return out
}

// mmdbCityRecord is the city record of the fixture databases: the test address
// resolves to Mountain View, California, US.
func mmdbCityRecord() []byte {
	return mmdbMap(
		mmdbField("country", mmdbMap(mmdbField("iso_code", mmdbString("US")))),
		mmdbField("registered_country", mmdbMap(mmdbField("iso_code", mmdbString("US")))),
		mmdbField("city", mmdbMap(mmdbField("names", mmdbMap(mmdbField("en", mmdbString("Mountain View")))))),
		mmdbField("subdivisions", mmdbSlice(mmdbMap(mmdbField("names", mmdbMap(mmdbField("en", mmdbString("California"))))))),
	)
}

// mmdbASNRecord is the ASN record of the fixture databases.
func mmdbASNRecord() []byte {
	return mmdbMap(
		mmdbField("autonomous_system_number", mmdbUint(15169, 4)),
		mmdbField("autonomous_system_organization", mmdbString("Google LLC")),
	)
}

// --- helpers ---------------------------------------------------------------

// geoRequests records what the refresher requested, in order.
type geoRequests struct {
	mu    sync.Mutex
	paths []string
	auth  []string
	token []string
}

func (r *geoRequests) note(request *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, request.URL.Path)
	r.auth = append(r.auth, request.Header.Get("Authorization"))
	r.token = append(r.token, request.URL.Query().Get("token"))
}

func (r *geoRequests) Paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

func (r *geoRequests) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.paths)
}

func (r *geoRequests) Authentications() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.auth...)
}

func (r *geoRequests) Tokens() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.token...)
}

func gzipBody(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("gzip fixture: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip fixture: %v", err)
	}
	return buffer.Bytes()
}

// tarGzipBody packs one database into a MaxMind-style release archive.
func tarGzipBody(t *testing.T, member string, payload []byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	header := &tar.Header{Name: member, Mode: 0o644, Size: int64(len(payload)), Typeflag: tar.TypeReg}
	if err := writer.WriteHeader(header); err != nil {
		t.Fatalf("tar fixture: %v", err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("tar fixture: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("tar fixture: %v", err)
	}
	return gzipBody(t, archive.Bytes())
}

// geoRegistry builds the built-in data sources with their effective settings,
// exactly the way the running application resolves them.
func geoRegistry(t *testing.T, env map[string]string) *Registry {
	t.Helper()
	registry := NewRegistry()
	RegisterBuiltins(registry, BuiltinConfig{Now: func() time.Time { return testNow }})
	specs := registry.Specs()
	registry.Apply(ResolveSettings(specs, nil, func(name string) string { return env[name] }))
	return registry
}

// geoBaseURLs points every vendor endpoint at one test server.
func geoBaseURLs(server *httptest.Server) GeoDBBaseURLs {
	return GeoDBBaseURLs{
		DBIP:    server.URL + "/free/",
		MaxMind: server.URL + "/geoip/databases/",
		IPInfo:  server.URL + "/data/ipinfo_lite.mmdb",
	}
}

// newGeoTestManager builds a refresher that only knows the test server.
func newGeoTestManager(t *testing.T, dir string, server *httptest.Server, registry *Registry, options ...func(*GeoManagerOptions)) *GeoManager {
	t.Helper()
	direct := netutil.NewDirectDownloader(
		func() time.Duration { return 5 * time.Second },
		func() string { return "prism-test" },
	)
	direct.Client = server.Client()
	opts := GeoManagerOptions{
		Dir:        dir,
		Downloader: direct,
		BaseURLs:   geoBaseURLs(server),
		Setting: func(id string) (Setting, bool) {
			return registry.Setting(id)
		},
		Now:  func() time.Time { return testNow },
		Logf: func(string, ...any) {},
	}
	for _, apply := range options {
		apply(&opts)
	}
	manager := NewGeoManager(opts)
	if manager == nil {
		t.Fatal("the refresher was not built")
	}
	t.Cleanup(manager.Stop)
	return manager
}

// onlySource filters the production catalog down to one database file, so a
// test can drive the background pass without the other vendors.
func onlySource(t *testing.T, bases GeoDBBaseURLs, fileName string) []GeoDBSource {
	t.Helper()
	for _, source := range GeoDBSources(bases) {
		if source.FileName == fileName {
			return []GeoDBSource{source}
		}
	}
	t.Fatalf("the catalog has no database %q", fileName)
	return nil
}

func installedGeoFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read installed database %s: %v", name, err)
	}
	return body
}

func geoFileStatus(t *testing.T, status *DatabaseStatus, name string) DatabaseFileStatus {
	t.Helper()
	if status == nil {
		t.Fatal("the provider reports no database state")
	}
	for _, file := range status.Files {
		if file.Name == name {
			return file
		}
	}
	t.Fatalf("the database state has no file %q: %+v", name, status.Files)
	return DatabaseFileStatus{}
}

// --- fixtures --------------------------------------------------------------

func TestGeoFixtureDatabasesAreValid(t *testing.T) {
	city := buildMMDB(t, "DBIP-City-Lite", "8.8.8.8/32", mmdbCityRecord())
	path := filepath.Join(t.TempDir(), DBIPCityFile)
	if err := os.WriteFile(path, city, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	reader, err := maxminddb.Open(path)
	if err != nil {
		t.Fatalf("the fixture is not a MaxMind database: %v", err)
	}
	defer reader.Close()
	if reader.Metadata.DatabaseType != "DBIP-City-Lite" || reader.Metadata.NodeCount != uint(mmdbIPv4Bits) {
		t.Fatalf("fixture metadata: %+v", reader.Metadata)
	}
	if err := reader.Verify(); err != nil {
		t.Fatalf("the fixture does not verify: %v", err)
	}
	var record map[string]any
	if err := reader.Lookup(netip.MustParseAddr("8.8.8.8").AsSlice(), &record); err != nil || record == nil {
		t.Fatalf("the fixture has no record for 8.8.8.8: %v (%v)", err, record)
	}
}

// --- downloads -------------------------------------------------------------

// TestGeoMonthFallbackDownloadsThePreviousMonth is the DB-IP rule of WP09 §3:
// the file of the current month is tried first and the previous month is used
// when the current one is not published yet. The assertion is the request
// sequence, not a real download.
func TestGeoMonthFallbackDownloadsThePreviousMonth(t *testing.T) {
	city := gzipBody(t, buildMMDB(t, "DBIP-City-Lite", "8.8.8.8/32", mmdbCityRecord()))
	asn := gzipBody(t, buildMMDB(t, "DBIP-ASN-Lite", "8.8.8.8/32", mmdbASNRecord()))
	requests := &geoRequests{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.note(r)
		switch {
		case strings.HasSuffix(r.URL.Path, "dbip-city-lite-2026-01.mmdb.gz"):
			_, _ = w.Write(city)
		case strings.HasSuffix(r.URL.Path, "dbip-asn-lite-2026-01.mmdb.gz"):
			_, _ = w.Write(asn)
		default:
			// The current month is not published yet.
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	// The refresher is pinned to February 2026 so the month-named URLs of the
	// current and the previous month are predictable.
	monthNow := time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC)
	manager := newGeoTestManager(t, dir, server, geoRegistry(t, nil),
		func(opts *GeoManagerOptions) { opts.Now = func() time.Time { return monthNow } })
	result, err := manager.RequestRefresh(context.Background(), "dbip_lite")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	want := []string{
		"/free/dbip-city-lite-2026-02.mmdb.gz",
		"/free/dbip-city-lite-2026-01.mmdb.gz",
		"/free/dbip-asn-lite-2026-02.mmdb.gz",
		"/free/dbip-asn-lite-2026-01.mmdb.gz",
	}
	if got := requests.Paths(); !equalStrings(got, want) {
		t.Fatalf("request sequence = %v, want %v", got, want)
	}
	if result.Outcome != GeoOutcomeRefreshed {
		t.Fatalf("outcome = %s (%+v)", result.Outcome, result.Files)
	}
	if !bytes.Equal(installedGeoFile(t, dir, DBIPCityFile), buildMMDB(t, "DBIP-City-Lite", "8.8.8.8/32", mmdbCityRecord())) {
		t.Fatal("the city database was not installed")
	}
	if !bytes.Equal(installedGeoFile(t, dir, DBIPASNFile), buildMMDB(t, "DBIP-ASN-Lite", "8.8.8.8/32", mmdbASNRecord())) {
		t.Fatal("the ASN database was not installed")
	}
}

// TestGeoCorruptDownloadNeverReplacesAWorkingDatabase is the central
// protection of WP09 §3: a body that is not a MaxMind database is rejected
// before the swap, the installed file stays byte-identical and the failure is
// recorded.
func TestGeoCorruptDownloadNeverReplacesAWorkingDatabase(t *testing.T) {
	good := gzipBody(t, buildMMDB(t, "DBIP-City-Lite", "8.8.8.8/32", mmdbCityRecord()))
	var body []byte
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dir := t.TempDir()
	manager := newGeoTestManager(t, dir, server, geoRegistry(t, nil),
		func(opts *GeoManagerOptions) { opts.Sources = onlySource(t, geoBaseURLs(server), DBIPCityFile) })

	mu.Lock()
	body = good
	mu.Unlock()
	result, err := manager.RequestRefresh(context.Background(), "dbip_lite")
	if err != nil || result.Outcome != GeoOutcomeRefreshed {
		t.Fatalf("initial download: %v (%+v)", err, result)
	}
	installed := installedGeoFile(t, dir, DBIPCityFile)

	// The vendor now answers with an HTML error page.
	mu.Lock()
	body = []byte("<html><body>service unavailable</body></html>")
	mu.Unlock()
	result, err = manager.RequestRefresh(context.Background(), "dbip_lite")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if result.Outcome != GeoOutcomeFailed || result.Files[0].ErrorCode != GeoCodeInvalidDB {
		t.Fatalf("a corrupt download must fail with %s: %+v", GeoCodeInvalidDB, result.Files)
	}
	if !bytes.Equal(installedGeoFile(t, dir, DBIPCityFile), installed) {
		t.Fatal("a corrupt download replaced the working database")
	}
	// The rejected download never reached the swap, so the fallback of the
	// installed generation was not touched either.
	if _, err := os.Stat(filepath.Join(dir, DBIPCityFile+GeoDBPreviousSuffix)); !os.IsNotExist(err) {
		t.Fatal("a rejected download must not move the installed database aside")
	}
	status := manager.DatabaseStatus("dbip_lite")
	if status == nil || !status.Installed {
		t.Fatalf("the working database must stay installed: %+v", status)
	}
	file := geoFileStatus(t, status, DBIPCityFile)
	if file.LastOutcome != GeoOutcomeFailed || file.ErrorCode != GeoCodeInvalidDB || file.Reason == "" {
		t.Fatalf("the failure state was not recorded: %+v", file)
	}
	if file.SizeBytes != int64(len(installed)) || file.Age == "" {
		t.Fatalf("size/age are missing from the state: %+v", file)
	}
}

// TestGeoTruncatedDownloadNeverReplacesAWorkingDatabase covers the partial
// download: the last bytes (including the metadata) never arrived.
func TestGeoTruncatedDownloadNeverReplacesAWorkingDatabase(t *testing.T) {
	full := buildMMDB(t, "DBIP-City-Lite", "8.8.8.8/32", mmdbCityRecord())
	good := gzipBody(t, full)
	truncated := full[:len(full)/2]
	var mu sync.Mutex
	body := good
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dir := t.TempDir()
	manager := newGeoTestManager(t, dir, server, geoRegistry(t, nil),
		func(opts *GeoManagerOptions) { opts.Sources = onlySource(t, geoBaseURLs(server), DBIPCityFile) })
	if _, err := manager.RequestRefresh(context.Background(), "dbip_lite"); err != nil {
		t.Fatalf("initial download: %v", err)
	}
	installed := installedGeoFile(t, dir, DBIPCityFile)

	mu.Lock()
	body = gzipBody(t, truncated)
	mu.Unlock()
	result, err := manager.RequestRefresh(context.Background(), "dbip_lite")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if result.Outcome != GeoOutcomeFailed || result.Files[0].ErrorCode != GeoCodeInvalidDB {
		t.Fatalf("a truncated download must fail with %s: %+v", GeoCodeInvalidDB, result.Files)
	}
	if !bytes.Equal(installedGeoFile(t, dir, DBIPCityFile), installed) {
		t.Fatal("a truncated download replaced the working database")
	}
}

// TestGeoOversizedDownloadIsRejected covers the size bound of R4 in both
// directions: the downloaded body and the unpacked database.
func TestGeoOversizedDownloadIsRejected(t *testing.T) {
	good := gzipBody(t, buildMMDB(t, "DBIP-City-Lite", "8.8.8.8/32", mmdbCityRecord()))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(good)
	}))
	defer server.Close()

	dir := t.TempDir()
	manager := newGeoTestManager(t, dir, server, geoRegistry(t, nil),
		func(opts *GeoManagerOptions) { opts.Sources = onlySource(t, geoBaseURLs(server), DBIPCityFile) })
	if _, err := manager.RequestRefresh(context.Background(), "dbip_lite"); err != nil {
		t.Fatalf("initial download: %v", err)
	}
	installed := installedGeoFile(t, dir, DBIPCityFile)

	// A downloader bound that is smaller than the body.
	tight := newGeoTestManager(t, dir, server, geoRegistry(t, nil),
		func(opts *GeoManagerOptions) {
			opts.Sources = onlySource(t, geoBaseURLs(server), DBIPCityFile)
			opts.MaxDownloadBytes = int64(len(good) - 1)
		})
	result, err := tight.RequestRefresh(context.Background(), "dbip_lite")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if result.Files[0].ErrorCode != GeoCodeTooLarge {
		t.Fatalf("an oversized download must fail with %s: %+v", GeoCodeTooLarge, result.Files)
	}
	if !bytes.Equal(installedGeoFile(t, dir, DBIPCityFile), installed) {
		t.Fatal("an oversized download replaced the working database")
	}

	// An unpacked database above the bound (a zip bomb guard).
	bomb := newGeoTestManager(t, dir, server, geoRegistry(t, nil),
		func(opts *GeoManagerOptions) {
			opts.Sources = onlySource(t, geoBaseURLs(server), DBIPCityFile)
			opts.MaxDatabaseBytes = 64
		})
	result, err = bomb.RequestRefresh(context.Background(), "dbip_lite")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if result.Files[0].ErrorCode != GeoCodeTooLarge {
		t.Fatalf("an oversized database must fail with %s: %+v", GeoCodeTooLarge, result.Files)
	}
	if !bytes.Equal(installedGeoFile(t, dir, DBIPCityFile), installed) {
		t.Fatal("an oversized database replaced the working database")
	}
}

// TestGeoRefreshKeepsThePreviousGeneration proves the swap of a valid
// download: the new file is installed, the old one stays as the fallback and
// the state reports size and age.
func TestGeoRefreshKeepsThePreviousGeneration(t *testing.T) {
	first := buildMMDB(t, "DBIP-City-Lite", "8.8.8.8/32", mmdbCityRecord())
	second := buildMMDB(t, "DBIP-City-Lite", "9.9.9.9/32", mmdbCityRecord())
	body := gzipBody(t, first)
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dir := t.TempDir()
	manager := newGeoTestManager(t, dir, server, geoRegistry(t, nil),
		func(opts *GeoManagerOptions) { opts.Sources = onlySource(t, geoBaseURLs(server), DBIPCityFile) })
	if _, err := manager.RequestRefresh(context.Background(), "dbip_lite"); err != nil {
		t.Fatalf("initial download: %v", err)
	}

	mu.Lock()
	body = gzipBody(t, second)
	mu.Unlock()
	result, err := manager.RequestRefresh(context.Background(), "dbip_lite")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if result.Outcome != GeoOutcomeRefreshed || result.Files[0].SizeBytes != int64(len(second)) {
		t.Fatalf("refresh result: %+v", result.Files)
	}
	if !bytes.Equal(installedGeoFile(t, dir, DBIPCityFile), second) {
		t.Fatal("the refreshed database was not installed")
	}
	if !bytes.Equal(installedGeoFile(t, dir, DBIPCityFile+GeoDBPreviousSuffix), first) {
		t.Fatal("the previous generation was not kept as the fallback")
	}
	status := manager.DatabaseStatus("dbip_lite")
	file := geoFileStatus(t, status, DBIPCityFile)
	if !file.Installed || file.SizeBytes != int64(len(second)) || file.LastOutcome != GeoOutcomeRefreshed ||
		file.LastSuccessAtNs == 0 || file.LastAttemptAtNs == 0 || file.Age == "" {
		t.Fatalf("database state: %+v", file)
	}
}

// TestGeoHTTPFailureKeepsTheDatabaseAndBacksOff covers a vendor failure: the
// reason is recorded, the installed file stays and the next attempt is delayed.
func TestGeoHTTPFailureKeepsTheDatabaseAndBacksOff(t *testing.T) {
	good := gzipBody(t, buildMMDB(t, "DBIP-City-Lite", "8.8.8.8/32", mmdbCityRecord()))
	status := http.StatusOK
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write(good)
	}))
	defer server.Close()

	dir := t.TempDir()
	manager := newGeoTestManager(t, dir, server, geoRegistry(t, nil),
		func(opts *GeoManagerOptions) { opts.Sources = onlySource(t, geoBaseURLs(server), DBIPCityFile) })
	if _, err := manager.RequestRefresh(context.Background(), "dbip_lite"); err != nil {
		t.Fatalf("initial download: %v", err)
	}
	installed := installedGeoFile(t, dir, DBIPCityFile)

	mu.Lock()
	status = http.StatusInternalServerError
	mu.Unlock()
	result, err := manager.RequestRefresh(context.Background(), "dbip_lite")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if result.Outcome != GeoOutcomeFailed || result.Files[0].ErrorCode != GeoCodeHTTPStatus {
		t.Fatalf("HTTP failure result: %+v", result.Files)
	}
	if !strings.Contains(result.Files[0].Reason, "500") {
		t.Fatalf("the failure reason must name the status: %q", result.Files[0].Reason)
	}
	if !bytes.Equal(installedGeoFile(t, dir, DBIPCityFile), installed) {
		t.Fatal("a failed download replaced the working database")
	}
	file := geoFileStatus(t, manager.DatabaseStatus("dbip_lite"), DBIPCityFile)
	if file.Downloading || file.NextAttemptAtNs <= file.LastAttemptAtNs {
		t.Fatalf("the failed attempt must back off: %+v", file)
	}
}

// TestGeoRefreshSkipsUnconfiguredDatabases is the "not configured and not
// required" rule of WP09 §3: a missing licence key or token is a recorded
// skip, no request is made and the settings API shows why.
func TestGeoRefreshSkipsUnconfiguredDatabases(t *testing.T) {
	requests := &geoRequests{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.note(r)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	dir := t.TempDir()
	registry := geoRegistry(t, nil)
	manager := newGeoTestManager(t, dir, server, registry)

	result, err := manager.RequestRefresh(context.Background(), "maxmind_geolite2")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if result.Outcome != GeoOutcomeSkipped || result.Files[0].ErrorCode != GeoCodeNotReady {
		t.Fatalf("an unconfigured MaxMind must be skipped: %+v", result.Files)
	}
	if !strings.Contains(result.Files[0].Reason, "license_key") {
		t.Fatalf("the skip reason must name the missing credential: %q", result.Files[0].Reason)
	}
	if requests.Count() != 0 {
		t.Fatalf("a skipped database must not be requested: %v", requests.Paths())
	}
	status := manager.DatabaseStatus("maxmind_geolite2")
	if status == nil || status.Ready || status.Installed || status.Reason == "" {
		t.Fatalf("database state: %+v", status)
	}
	if status.ErrorCode != GeoCodeNotReady {
		t.Fatalf("database error code: %+v", status)
	}

	token, err := manager.RequestRefresh(context.Background(), "ipinfo_lite")
	if err != nil || token.Outcome != GeoOutcomeSkipped {
		t.Fatalf("an unconfigured IPinfo must be skipped: %v (%+v)", err, token.Files)
	}
	if !strings.Contains(token.Files[0].Reason, "token") {
		t.Fatalf("the skip reason must name the missing token: %q", token.Files[0].Reason)
	}
	if requests.Count() != 0 {
		t.Fatalf("a skipped database must not be requested: %v", requests.Paths())
	}

	// A disabled data source is skipped as well.
	disabled := geoRegistry(t, map[string]string{
		"PRISM_MAXMIND_ACCOUNT_ID":  "42",
		"PRISM_MAXMIND_LICENSE_KEY": "unit-test-key",
	})
	spec, _ := disabled.Spec("maxmind_geolite2")
	disabled.Apply([]Setting{{
		Spec:    spec,
		Enabled: false,
		Config:  map[string]any{"account_id": "42", "license_key": "unit-test-key"},
	}})
	manager2 := newGeoTestManager(t, dir, server, disabled)
	off, err := manager2.RequestRefresh(context.Background(), "maxmind_geolite2")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if off.Outcome != GeoOutcomeSkipped || !strings.Contains(off.Files[0].Reason, "disabled") {
		t.Fatalf("a disabled data source must be skipped: %+v", off.Files)
	}
	if requests.Count() != 0 {
		t.Fatalf("a skipped database must not be requested: %v", requests.Paths())
	}
}

// TestGeoMaxMindUsesBasicAuthAndExtractsTheArchive covers the MaxMind path:
// the credentials travel as HTTP basic authentication, the tar.gz member is
// extracted and the licence key never reaches a log line or a response.
func TestGeoMaxMindUsesBasicAuthAndExtractsTheArchive(t *testing.T) {
	const accountID = "913370"
	const licenseKey = "unit-test-license-key-do-not-leak"
	requests := &geoRequests{}
	payload := buildMMDB(t, "GeoLite2-City", "8.8.8.8/32", mmdbCityRecord())
	archive := tarGzipBody(t, "GeoLite2-City_20260201/GeoLite2-City.mmdb", payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.note(r)
		if strings.HasSuffix(r.URL.Path, "/GeoLite2-City/download") {
			_, _ = w.Write(archive)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	var logged strings.Builder
	dir := t.TempDir()
	registry := geoRegistry(t, map[string]string{
		"PRISM_MAXMIND_ACCOUNT_ID":  accountID,
		"PRISM_MAXMIND_LICENSE_KEY": licenseKey,
	})
	manager := newGeoTestManager(t, dir, server, registry,
		func(opts *GeoManagerOptions) {
			opts.Sources = onlySource(t, geoBaseURLs(server), MaxMindCityFile)
			opts.Logf = func(format string, args ...any) { logged.WriteString(fmt.Sprintf(format, args...)) }
		})
	result, err := manager.RequestRefresh(context.Background(), "maxmind_geolite2")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if result.Outcome != GeoOutcomeRefreshed {
		t.Fatalf("refresh result: %+v", result.Files)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(accountID+":"+licenseKey))
	authentications := requests.Authentications()
	if len(authentications) != 1 || authentications[0] != want {
		t.Fatalf("basic authentication = %v", authentications)
	}
	if got := requests.Paths(); len(got) != 1 || got[0] != "/geoip/databases/GeoLite2-City/download" {
		t.Fatalf("request paths = %v", got)
	}
	if !bytes.Equal(installedGeoFile(t, dir, MaxMindCityFile), payload) {
		t.Fatal("the archive member was not installed")
	}

	// R6: the licence key must not appear in the log, the result or the state.
	encoded, err := json.Marshal(manager.DatabaseStatus("maxmind_geolite2"))
	if err != nil {
		t.Fatalf("encode status: %v", err)
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	for _, text := range []string{logged.String(), string(encoded), string(resultJSON)} {
		if strings.Contains(text, licenseKey) || strings.Contains(text, accountID) {
			t.Fatalf("a credential leaked into a log or a response: %q", text)
		}
	}
}

// TestGeoArchiveWithoutTheDatabaseIsRejected covers a release archive that
// does not contain the expected member.
func TestGeoArchiveWithoutTheDatabaseIsRejected(t *testing.T) {
	payload := buildMMDB(t, "GeoLite2-ASN", "8.8.8.8/32", mmdbASNRecord())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarGzipBody(t, "GeoLite2-ASN_20260201/README.txt", payload))
	}))
	defer server.Close()

	dir := t.TempDir()
	registry := geoRegistry(t, map[string]string{
		"PRISM_MAXMIND_ACCOUNT_ID":  "913370",
		"PRISM_MAXMIND_LICENSE_KEY": "unit-test-license-key",
	})
	manager := newGeoTestManager(t, dir, server, registry,
		func(opts *GeoManagerOptions) { opts.Sources = onlySource(t, geoBaseURLs(server), MaxMindASNFile) })
	result, err := manager.RequestRefresh(context.Background(), "maxmind_geolite2")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if result.Outcome != GeoOutcomeFailed || result.Files[0].ErrorCode != GeoCodeInvalidDB {
		t.Fatalf("a foreign archive must fail with %s: %+v", GeoCodeInvalidDB, result.Files)
	}
	if _, err := os.Stat(filepath.Join(dir, MaxMindASNFile)); !os.IsNotExist(err) {
		t.Fatal("a rejected archive must not install a database")
	}
}

// TestGeoIPInfoSendsTheTokenAndInstallsTheDatabase covers the IPinfo Lite path
// (token in the query string, plain mmdb body) and the R6 rule for its token.
func TestGeoIPInfoSendsTheTokenAndInstallsTheDatabase(t *testing.T) {
	const token = "unit-test-ipinfo-token-do-not-leak"
	requests := &geoRequests{}
	payload := buildMMDB(t, "ipinfo_lite_asn", "8.8.8.8/32", mmdbASNRecord())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.note(r)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	var logged strings.Builder
	dir := t.TempDir()
	registry := geoRegistry(t, map[string]string{"PRISM_IPINFO_TOKEN": token})
	manager := newGeoTestManager(t, dir, server, registry,
		func(opts *GeoManagerOptions) {
			opts.Sources = onlySource(t, geoBaseURLs(server), IPInfoLiteDBFile)
			opts.Logf = func(format string, args ...any) { logged.WriteString(fmt.Sprintf(format, args...)) }
		})
	result, err := manager.RequestRefresh(context.Background(), "ipinfo_lite")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if result.Outcome != GeoOutcomeRefreshed {
		t.Fatalf("refresh result: %+v", result.Files)
	}
	if tokens := requests.Tokens(); len(tokens) != 1 || tokens[0] != token {
		t.Fatalf("token query = %v", tokens)
	}
	if !bytes.Equal(installedGeoFile(t, dir, IPInfoLiteDBFile), payload) {
		t.Fatal("the IPinfo Lite database was not installed")
	}
	encoded, err := json.Marshal(manager.DatabaseStatus("ipinfo_lite"))
	if err != nil {
		t.Fatalf("encode status: %v", err)
	}
	if strings.Contains(string(encoded), token) || strings.Contains(logged.String(), token) {
		t.Fatal("the IPinfo token leaked into a log or a response")
	}
}

// TestGeoRefreshRepairsACorruptInstalledDatabase proves the second direction of
// the validation: a file that was damaged outside Prism is re-downloaded by the
// background pass instead of being trusted because it is fresh.
func TestGeoRefreshRepairsACorruptInstalledDatabase(t *testing.T) {
	good := gzipBody(t, buildMMDB(t, "DBIP-City-Lite", "8.8.8.8/32", mmdbCityRecord()))
	requests := &geoRequests{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.note(r)
		_, _ = w.Write(good)
	}))
	defer server.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DBIPCityFile), []byte("partially written database"), 0o600); err != nil {
		t.Fatalf("write damaged database: %v", err)
	}
	manager := newGeoTestManager(t, dir, server, geoRegistry(t, nil),
		func(opts *GeoManagerOptions) { opts.Sources = onlySource(t, geoBaseURLs(server), DBIPCityFile) })

	// The background pass runs the same evaluation as the manual refresh, but
	// honours the freshness of the file.
	manager.refreshDue()
	if requests.Count() == 0 {
		t.Fatal("a damaged installed database was treated as fresh")
	}
	if !bytes.Equal(installedGeoFile(t, dir, DBIPCityFile), buildMMDB(t, "DBIP-City-Lite", "8.8.8.8/32", mmdbCityRecord())) {
		t.Fatal("the damaged database was not repaired")
	}
	file := geoFileStatus(t, manager.DatabaseStatus("dbip_lite"), DBIPCityFile)
	if file.LastOutcome != GeoOutcomeRefreshed {
		t.Fatalf("the repair was not recorded: %+v", file)
	}

	// A fresh and valid file is not downloaded again.
	before := requests.Count()
	manager.refreshDue()
	if requests.Count() != before {
		t.Fatalf("a fresh database was downloaded again: %v", requests.Paths()[before:])
	}
}

// TestGeoStartDoesNotBlockAndProvidersPickTheDatabaseUp is the "never block
// startup" rule: Start returns while the download is still running, the offline
// provider answers PROVIDER_UNAVAILABLE in the meantime and the file is picked
// up without a restart once it landed.
func TestGeoStartDoesNotBlockAndProvidersPickTheDatabaseUp(t *testing.T) {
	city := gzipBody(t, buildMMDB(t, "DBIP-City-Lite", "8.8.8.8/32", mmdbCityRecord()))
	asn := gzipBody(t, buildMMDB(t, "DBIP-ASN-Lite", "8.8.8.8/32", mmdbASNRecord()))
	releaseCh := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-releaseCh
		if strings.Contains(r.URL.Path, "dbip-asn-lite") {
			_, _ = w.Write(asn)
			return
		}
		_, _ = w.Write(city)
	}))
	defer server.Close()
	defer release()

	dir := t.TempDir()
	manager := newGeoTestManager(t, dir, server, geoRegistry(t, nil),
		func(opts *GeoManagerOptions) { opts.Sources = onlySource(t, geoBaseURLs(server), DBIPCityFile) })

	provider := NewMMDBProvider(MMDBOptions{
		ID: "dbip_lite", Name: "DB-IP Lite", Profile: DBIPProfile,
		CityPath: filepath.Join(dir, DBIPCityFile),
		ASNPath:  filepath.Join(dir, DBIPASNFile),
		Now:      func() time.Time { return testNow },
	})
	expectCode(t, provider.Lookup(testIP), CodeUnavailable)

	started := time.Now()
	manager.Start()
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Start blocked for %v", elapsed)
	}
	if err := waitForCondition(t, 5*time.Second, func() bool {
		status := manager.DatabaseStatus("dbip_lite")
		return status != nil && status.Downloading
	}); err != nil {
		t.Fatalf("the download did not start in the background: %v", err)
	}
	// The download is still held open: the provider keeps its explained failure.
	expectCode(t, provider.Lookup(testIP), CodeUnavailable)

	release()
	if err := waitForCondition(t, 5*time.Second, func() bool {
		status := manager.DatabaseStatus("dbip_lite")
		return status != nil && status.Installed
	}); err != nil {
		t.Fatalf("the database was not installed in the background: %v", err)
	}
	// Only the city database is configured in this test, so the evidence carries
	// the location and no ASN.
	evidence := mustEvidence(t, provider.Lookup(testIP))
	if evidence.CountryCode != "US" || evidence.City != "Mountain View" || evidence.Region != "California" {
		t.Fatalf("the downloaded database was not used: %+v", evidence)
	}
}

// TestGeoRequestRefreshRejectsAProviderWithoutADatabase keeps the manual action
// honest for the offline sources that read no mmdb file.
func TestGeoRequestRefreshRejectsAProviderWithoutADatabase(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	manager := newGeoTestManager(t, t.TempDir(), server, geoRegistry(t, nil))
	if _, err := manager.RequestRefresh(context.Background(), "geo_country"); !errors.Is(err, ErrGeoUnknownProvider) {
		t.Fatalf("expected %v, got %v", ErrGeoUnknownProvider, err)
	}
	if status := manager.DatabaseStatus("geo_country"); status != nil {
		t.Fatalf("geo_country has no downloaded database: %+v", status)
	}
}

// TestGeoRefreshIsBoundedWhileADownloadRuns proves the single download slot:
// a second action while the first download is in flight reports GEO_BUSY
// instead of queueing behind an unbounded wait.
func TestGeoRefreshIsBoundedWhileADownloadRuns(t *testing.T) {
	city := gzipBody(t, buildMMDB(t, "DBIP-City-Lite", "8.8.8.8/32", mmdbCityRecord()))
	releaseCh := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	entered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-releaseCh
		_, _ = w.Write(city)
	}))
	defer server.Close()
	defer release()

	dir := t.TempDir()
	manager := newGeoTestManager(t, dir, server, geoRegistry(t, nil),
		func(opts *GeoManagerOptions) { opts.Sources = onlySource(t, geoBaseURLs(server), DBIPCityFile) })

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = manager.RequestRefresh(context.Background(), "dbip_lite")
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first download never started")
	}

	result, err := manager.RequestRefresh(context.Background(), "dbip_lite")
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if result.Outcome != GeoOutcomeSkipped || result.Files[0].ErrorCode != GeoCodeBusy {
		t.Fatalf("a concurrent download must report %s: %+v", GeoCodeBusy, result.Files)
	}
	release()
	<-done
}

// TestGeoStopCancelsAnInFlightDownload keeps the shutdown bounded.
func TestGeoStopCancelsAnInFlightDownload(t *testing.T) {
	releaseCh := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	entered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-releaseCh
	}))
	defer server.Close()
	defer release()

	manager := newGeoTestManager(t, t.TempDir(), server, geoRegistry(t, nil),
		func(opts *GeoManagerOptions) { opts.Sources = onlySource(t, geoBaseURLs(server), DBIPCityFile) })
	manager.Start()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the download never started")
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		manager.Stop()
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not cancel the in-flight download")
	}
}

// --- small units -----------------------------------------------------------

func TestGeoWriteFailureCodes(t *testing.T) {
	full := geoWriteFailure(&os.PathError{Op: "write", Path: "db", Err: syscall.ENOSPC})
	if code, _ := geoFailureOf(full); code != GeoCodeDiskFull {
		t.Fatalf("a full disk must report %s, got %s", GeoCodeDiskFull, code)
	}
	other := geoWriteFailure(errors.New("permission denied"))
	code, reason := geoFailureOf(other)
	if code != GeoCodeWrite || reason == "" {
		t.Fatalf("a write failure must report %s with a reason, got %s (%q)", GeoCodeWrite, code, reason)
	}
}

func TestGeoBackoffIsCapped(t *testing.T) {
	first := geoBackoff(1)
	if first != geoDBBackoffBase {
		t.Fatalf("first failure backoff = %v", first)
	}
	if second := geoBackoff(2); second != 2*geoDBBackoffBase {
		t.Fatalf("second failure backoff = %v", second)
	}
	if capped := geoBackoff(40); capped != geoDBBackoffMax {
		t.Fatalf("the backoff must stop at %v, got %v", geoDBBackoffMax, capped)
	}
}

func TestGeoOutcomeAggregation(t *testing.T) {
	cases := []struct {
		name  string
		files []GeoRefreshFileResult
		want  string
	}{
		{"none", nil, GeoOutcomeUpToDate},
		{"refreshed", []GeoRefreshFileResult{{Outcome: GeoOutcomeRefreshed}}, GeoOutcomeRefreshed},
		{"skipped", []GeoRefreshFileResult{{Outcome: GeoOutcomeSkipped}}, GeoOutcomeSkipped},
		{"failure wins", []GeoRefreshFileResult{{Outcome: GeoOutcomeRefreshed}, {Outcome: GeoOutcomeFailed}}, GeoOutcomeFailed},
		{"refresh wins over skip", []GeoRefreshFileResult{{Outcome: GeoOutcomeSkipped}, {Outcome: GeoOutcomeRefreshed}}, GeoOutcomeRefreshed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := aggregateGeoOutcome(tc.files); got != tc.want {
				t.Fatalf("aggregateGeoOutcome = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestGeoRequestURLKeepsCredentialsOutOfThePath(t *testing.T) {
	source := GeoDBSource{
		Token:     func(Setting) string { return "tok en/value" },
		BasicAuth: func(Setting) (string, string) { return "42", "secret@key" },
	}
	target, err := geoRequestURL(source, Setting{}, "https://example.com/data.mmdb")
	if err != nil {
		t.Fatalf("geoRequestURL: %v", err)
	}
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse rendered URL: %v", err)
	}
	if parsed.Query().Get("token") != "tok en/value" {
		t.Fatalf("token query = %q", parsed.Query().Get("token"))
	}
	password, _ := parsed.User.Password()
	if parsed.User.Username() != "42" || password != "secret@key" {
		t.Fatalf("basic credentials = %v", parsed.User)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return errors.New("the condition was not met in time")
}
