package providers

// WP09 §3: download and refresh the offline databases (DB-IP Lite, MaxMind
// GeoLite2, IPinfo Lite) into $PRISM_CACHE_DIR/geo.
//
// Rules of this file:
//
//   - Nothing here blocks the startup or a lookup. The mmdb providers open
//     their files lazily (offline.go), so a missing database keeps answering
//     PROVIDER_UNAVAILABLE until the background download lands.
//   - Every download is bounded: one at a time, one request timeout, a maximum
//     downloaded size and a maximum unpacked size (R4).
//   - A downloaded file must open as a MaxMind database before it replaces the
//     installed one, and the previous generation stays on disk as
//     "<name>.mmdb.previous". A corrupt, truncated or oversized download can
//     therefore never destroy a working database.
//   - A credential (MaxMind account_id + license_key, IPinfo token) only ever
//     reaches the request. It is never logged, returned or repeated in an
//     error message (R6).
//   - A database whose provider is not configured is skipped with a recorded
//     reason instead of failing (WP09 §3).

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/oschwald/maxminddb-golang"

	"prism/internal/netutil"
)

// Vendor endpoints of the offline databases (WP09 §3). The download base URLs
// are overridable through GeoDBBaseURLs so tests point them at httptest.
const (
	dbipDownloadBaseURL    = "https://download.db-ip.com/free/"
	maxMindDownloadBaseURL = "https://download.maxmind.com/geoip/databases/"
	ipInfoLiteDownloadURL  = "https://ipinfo.io/data/ipinfo_lite.mmdb"
)

// GeoDBPreviousSuffix is the kept fallback of the previous database generation.
const GeoDBPreviousSuffix = ".previous"

// Bounds and schedule of the refresher (R4). Every over-limit value has a
// documented behaviour: the download fails, the installed database stays in
// place and the reason is recorded in the provider status.
const (
	// GeoDBRefreshInterval is the file age at which a database is downloaded
	// again. The vendors publish weekly (IPinfo Lite) to monthly (DB-IP, and
	// MaxMind's EULA asks for a 30-day refresh), so a weekly check is the
	// shortest sane schedule.
	GeoDBRefreshInterval = 7 * 24 * time.Hour
	// geoDBPollInterval is how often the refresher re-evaluates the files. A
	// fresh file makes the pass a cheap stat, not a download.
	geoDBPollInterval = time.Hour
	// GeoDBMaxDownloadBytes bounds one downloaded (compressed) body.
	GeoDBMaxDownloadBytes int64 = 128 << 20
	// GeoDBMaxDatabaseBytes bounds the unpacked database.
	GeoDBMaxDatabaseBytes int64 = 512 << 20
	// GeoDBRequestTimeout bounds one background download attempt.
	GeoDBRequestTimeout = 5 * time.Minute
	// GeoDBManualTimeout bounds the manual "refresh now" action.
	GeoDBManualTimeout = 3 * time.Minute
	// geoDBBackoffBase/geoDBBackoffMax bound the retry delay after failures:
	// the delay doubles per consecutive failure and stops at the maximum.
	geoDBBackoffBase = 5 * time.Minute
	geoDBBackoffMax  = 6 * time.Hour
)

// GeoDB outcomes reported by the settings API and by the manual refresh action.
const (
	GeoOutcomeRefreshed = "refreshed"
	GeoOutcomeUpToDate  = "up-to-date"
	GeoOutcomeSkipped   = "skipped"
	GeoOutcomeFailed    = "failed"
)

// GeoDB error codes. They are stable identifiers; the matching reason never
// repeats a credential or a download URL.
const (
	// GeoCodeNotReady reports a database that is not configured or disabled.
	// GeoCodeNotReady reports a database that is not configured (or disabled).
	GeoCodeNotReady = "GEO_NOT_READY"
	// GeoCodeCancelled reports a download that the shutdown cancelled; it is
	// recorded but not logged as a failure.
	GeoCodeCancelled = "GEO_CANCELLED"
	// GeoCodeBusy reports a refresh that found another download in flight.
	GeoCodeBusy = "GEO_BUSY"
	// GeoCodeRequest reports a transport failure.
	GeoCodeRequest = "GEO_REQUEST_FAILED"
	// GeoCodeHTTPStatus reports a non-200 answer of the vendor.
	GeoCodeHTTPStatus = "GEO_HTTP_STATUS"
	// GeoCodeTooLarge reports a body above the configured size bound.
	GeoCodeTooLarge = "GEO_TOO_LARGE"
	// GeoCodeInvalidDB reports a download that is not a valid database.
	GeoCodeInvalidDB = "GEO_INVALID_DATABASE"
	// GeoCodeDiskFull reports a cache device without free space.
	GeoCodeDiskFull = "GEO_DISK_FULL"
	// GeoCodeWrite reports a write failure of the cache directory.
	GeoCodeWrite = "GEO_WRITE_FAILED"
	// GeoCodeUnavailable reports a refresher without a downloader.
	GeoCodeUnavailable = "GEO_UNAVAILABLE"
)

// Errors of the manual refresh action. The API layer maps them onto the
// documented status codes.
var (
	// ErrGeoUnknownProvider reports a provider without a downloadable database.
	ErrGeoUnknownProvider = errors.New("provider has no downloadable database")
	// ErrGeoUnavailable reports a disabled offline database refresher.
	ErrGeoUnavailable = errors.New("offline database downloads are unavailable")
)

// GeoDBKind is the container of a downloaded database.
type GeoDBKind int

const (
	// GeoDBMMDB is the database itself.
	GeoDBMMDB GeoDBKind = iota
	// GeoDBGzip is a gzip-compressed database (DB-IP Lite).
	GeoDBGzip
	// GeoDBTarGzip is a tar.gz archive that holds the database (MaxMind).
	GeoDBTarGzip
	// GeoDBAuto accepts a plain or a gzip-compressed database (IPinfo Lite,
	// whose endpoint serves either form).
	GeoDBAuto
)

// GeoDBSource describes one downloadable offline database.
type GeoDBSource struct {
	// ProviderID is the data source the database belongs to.
	ProviderID string
	// Label names the database in log lines ("DB-IP city").
	Label string
	// FileName is the file name inside the geo directory.
	FileName string
	// Kind is the container of the downloaded body.
	Kind GeoDBKind
	// Candidates renders the candidate URLs of one refresh, in order. A 404
	// moves on to the next candidate: DB-IP names its files after the month
	// and keeps the previous month online until the new one is published.
	Candidates func(now time.Time) []string
	// BasicAuth renders the HTTP basic credentials of the request (MaxMind
	// account_id + license_key). The value never leaves the request (R6).
	BasicAuth func(setting Setting) (string, string)
	// Token renders the query token of the request (IPinfo Lite).
	Token func(setting Setting) string
	// Ready reports whether the database should be downloaded with the
	// effective settings. A false value is a recorded skip, not an error.
	Ready func(setting Setting) (bool, string)
}

// GeoDBBaseURLs overrides the vendor endpoints. The zero value uses the
// vendors of WP09 §3; tests point every field at one httptest server.
type GeoDBBaseURLs struct {
	DBIP    string
	MaxMind string
	IPInfo  string
}

// GeoDBSources returns the offline databases of the three mmdb providers:
// DB-IP city + ASN, MaxMind city + ASN and IPinfo Lite.
func GeoDBSources(bases GeoDBBaseURLs) []GeoDBSource {
	dbip := strings.TrimSpace(bases.DBIP)
	if dbip == "" {
		dbip = dbipDownloadBaseURL
	}
	maxMind := strings.TrimSpace(bases.MaxMind)
	if maxMind == "" {
		maxMind = maxMindDownloadBaseURL
	}
	ipInfo := strings.TrimSpace(bases.IPInfo)
	if ipInfo == "" {
		ipInfo = ipInfoLiteDownloadURL
	}

	notDisabled := func(setting Setting) (bool, string) {
		if !setting.Enabled {
			return false, "the data source is disabled"
		}
		return true, ""
	}

	return []GeoDBSource{
		{
			ProviderID: "dbip_lite", Label: "DB-IP city", FileName: DBIPCityFile, Kind: GeoDBGzip,
			Candidates: func(now time.Time) []string { return geoMonthCandidates(dbip, "dbip-city-lite", now) },
			Ready:      notDisabled,
		},
		{
			ProviderID: "dbip_lite", Label: "DB-IP ASN", FileName: DBIPASNFile, Kind: GeoDBGzip,
			Candidates: func(now time.Time) []string { return geoMonthCandidates(dbip, "dbip-asn-lite", now) },
			Ready:      notDisabled,
		},
		{
			ProviderID: "maxmind_geolite2", Label: "GeoLite2 city", FileName: MaxMindCityFile, Kind: GeoDBTarGzip,
			Candidates: func(time.Time) []string { return []string{maxMind + "GeoLite2-City/download?suffix=tar.gz"} },
			BasicAuth: func(setting Setting) (string, string) {
				return setting.ConfigString("account_id"), setting.ConfigString("license_key")
			},
			Ready: func(setting Setting) (bool, string) {
				if !setting.HasKey() {
					return false, "no MaxMind account_id and license_key are configured"
				}
				return notDisabled(setting)
			},
		},
		{
			ProviderID: "maxmind_geolite2", Label: "GeoLite2 ASN", FileName: MaxMindASNFile, Kind: GeoDBTarGzip,
			Candidates: func(time.Time) []string { return []string{maxMind + "GeoLite2-ASN/download?suffix=tar.gz"} },
			BasicAuth: func(setting Setting) (string, string) {
				return setting.ConfigString("account_id"), setting.ConfigString("license_key")
			},
			Ready: func(setting Setting) (bool, string) {
				if !setting.HasKey() {
					return false, "no MaxMind account_id and license_key are configured"
				}
				return notDisabled(setting)
			},
		},
		{
			ProviderID: "ipinfo_lite", Label: "IPinfo Lite", FileName: IPInfoLiteDBFile, Kind: GeoDBAuto,
			Candidates: func(time.Time) []string { return []string{ipInfo} },
			Token:      func(setting Setting) string { return setting.ConfigString("token") },
			Ready: func(setting Setting) (bool, string) {
				if setting.ConfigString("token") == "" {
					return false, "no IPinfo token is configured"
				}
				return notDisabled(setting)
			},
		},
	}
}

// geoMonthCandidates renders the month-named DB-IP URLs: the current month
// first and the previous month as the fallback, because a month's file appears
// only after the vendor published it.
func geoMonthCandidates(base, prefix string, now time.Time) []string {
	current := now.UTC()
	previous := current.AddDate(0, -1, 0)
	return []string{
		base + monthFileName(prefix, current),
		base + monthFileName(prefix, previous),
	}
}

// monthFileName renders "dbip-city-lite-2026-01.mmdb.gz".
func monthFileName(prefix string, month time.Time) string {
	return fmt.Sprintf("%s-%04d-%02d.mmdb.gz", prefix, month.Year(), int(month.Month()))
}

// GeoRefreshFileResult is the outcome of one database of a manual refresh.
type GeoRefreshFileResult struct {
	Name       string `json:"name"`
	Outcome    string `json:"outcome"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	Reason     string `json:"reason,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// GeoRefreshResult is the outcome of POST
// /api/v1/intel/providers/{id}/actions/refresh (WP09 §3).
type GeoRefreshResult struct {
	ProviderID string                 `json:"provider_id"`
	Outcome    string                 `json:"outcome"`
	Duration   string                 `json:"duration"`
	Files      []GeoRefreshFileResult `json:"files"`
}

// GeoManagerOptions configures the offline database refresher.
type GeoManagerOptions struct {
	// Dir is the cache directory of the databases ($PRISM_CACHE_DIR/geo).
	Dir string
	// Downloader fetches the files. It bounds the response size and the
	// request duration; nil records every attempt as unavailable.
	Downloader netutil.Downloader
	// Setting resolves the effective settings of one provider. A provider
	// that is not in the map is skipped with a recorded reason.
	Setting func(providerID string) (Setting, bool)
	// Now is the injected clock.
	Now func() time.Time
	// Logf receives operational messages. It never receives a credential.
	Logf func(format string, args ...any)
	// BaseURLs overrides the vendor endpoints (tests).
	BaseURLs GeoDBBaseURLs
	// Sources replaces the whole catalog (tests); nil uses GeoDBSources.
	Sources []GeoDBSource
	// RefreshInterval overrides GeoDBRefreshInterval.
	RefreshInterval time.Duration
	// PollInterval overrides geoDBPollInterval.
	PollInterval time.Duration
	// RequestTimeout overrides GeoDBRequestTimeout.
	RequestTimeout time.Duration
	// ManualTimeout overrides GeoDBManualTimeout.
	ManualTimeout time.Duration
	// MaxDownloadBytes overrides GeoDBMaxDownloadBytes.
	MaxDownloadBytes int64
	// MaxDatabaseBytes overrides GeoDBMaxDatabaseBytes.
	MaxDatabaseBytes int64
}

// geoDBState is the mutable download state of one database file.
type geoDBState struct {
	lastAttempt time.Time
	lastSuccess time.Time
	nextAttempt time.Time
	lastOutcome string
	errorCode   string
	reason      string
	failures    int
	validated   bool
	downloading bool
}

// GeoManager keeps the offline databases of WP09 §3 up to date. It is safe for
// concurrent use: one download runs at a time and the state of every file is
// recorded behind a mutex.
type GeoManager struct {
	dir        string
	downloader netutil.Downloader
	setting    func(string) (Setting, bool)
	now        func() time.Time
	logf       func(string, ...any)

	refreshInterval time.Duration
	pollInterval    time.Duration
	requestTimeout  time.Duration
	manualTimeout   time.Duration
	maxDownload     int64
	maxDatabase     int64

	sources    []GeoDBSource
	byProvider map[string][]GeoDBSource

	slot chan struct{}

	mu     sync.Mutex
	states map[string]*geoDBState

	startOnce sync.Once
	stopOnce  sync.Once
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// NewGeoManager builds the refresher. It performs no network I/O and creates
// no goroutine; call Start for the background loop.
func NewGeoManager(opts GeoManagerOptions) *GeoManager {
	dir := strings.TrimSpace(opts.Dir)
	if dir == "" {
		return nil
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	sources := opts.Sources
	if len(sources) == 0 {
		sources = GeoDBSources(opts.BaseURLs)
	}
	manager := &GeoManager{
		dir:             dir,
		downloader:      opts.Downloader,
		setting:         opts.Setting,
		now:             now,
		logf:            logf,
		refreshInterval: positiveOrDefault(opts.RefreshInterval, GeoDBRefreshInterval),
		pollInterval:    positiveOrDefault(opts.PollInterval, geoDBPollInterval),
		requestTimeout:  positiveOrDefault(opts.RequestTimeout, GeoDBRequestTimeout),
		manualTimeout:   positiveOrDefault(opts.ManualTimeout, GeoDBManualTimeout),
		maxDownload:     positiveBytesOrDefault(opts.MaxDownloadBytes, GeoDBMaxDownloadBytes),
		maxDatabase:     positiveBytesOrDefault(opts.MaxDatabaseBytes, GeoDBMaxDatabaseBytes),
		sources:         sources,
		byProvider:      make(map[string][]GeoDBSource),
		slot:            make(chan struct{}, 1),
		states:          make(map[string]*geoDBState, len(sources)),
	}
	// The base context is created here, so the background loop, an in-flight
	// download and the manual action all share one cancellation.
	manager.ctx, manager.cancel = context.WithCancel(context.Background())
	for _, src := range sources {
		if strings.TrimSpace(src.FileName) == "" {
			panic("providers: a database source needs a file name")
		}
		manager.byProvider[src.ProviderID] = append(manager.byProvider[src.ProviderID], src)
		manager.states[src.FileName] = &geoDBState{}
	}
	if len(sources) == 0 {
		return nil
	}
	return manager
}

func positiveOrDefault(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func positiveBytesOrDefault(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

// Start launches the background refresher. It never blocks: the first pass runs
// in its own goroutine, so a missing database only degrades the offline
// providers to PROVIDER_UNAVAILABLE until the download lands (WP09 §3).
func (m *GeoManager) Start() {
	if m == nil {
		return
	}
	m.startOnce.Do(func() {
		m.wg.Add(1)
		go m.loop()
	})
}

// Stop cancels an in-flight download and waits for the loop to return.
func (m *GeoManager) Stop() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() {
		if m.cancel != nil {
			m.cancel()
		}
	})
	m.wg.Wait()
}

// DatabaseStatus renders the download state of one provider; nil when the
// provider has no downloadable database.
func (m *GeoManager) DatabaseStatus(providerID string) *DatabaseStatus {
	if m == nil {
		return nil
	}
	sources := m.byProvider[strings.TrimSpace(providerID)]
	if len(sources) == 0 {
		return nil
	}
	now := m.now().UTC()
	status := &DatabaseStatus{
		Installed:       true,
		Ready:           true,
		RefreshInterval: DurationString(m.refreshInterval),
		Files:           make([]DatabaseFileStatus, 0, len(sources)),
	}
	setting, registered := m.settingFor(providerID)
	if !registered {
		status.Ready = false
		status.Reason = "the data source is not registered"
	}
	for _, src := range sources {
		file := DatabaseFileStatus{Name: src.FileName}
		if info, err := os.Stat(filepath.Join(m.dir, src.FileName)); err == nil && info.Mode().IsRegular() {
			file.Installed = true
			file.SizeBytes = info.Size()
			file.UpdatedAtNs = info.ModTime().UTC().UnixNano()
			file.Age = DurationString(ageOf(now, info.ModTime().UTC()))
		} else {
			status.Installed = false
		}
		state := m.stateOf(src.FileName)
		file.LastOutcome = state.lastOutcome
		file.ErrorCode = state.errorCode
		file.Reason = state.reason
		file.Downloading = state.downloading
		file.LastAttemptAtNs = unixNanoOrZero(state.lastAttempt)
		file.LastSuccessAtNs = unixNanoOrZero(state.lastSuccess)
		file.NextAttemptAtNs = unixNanoOrZero(state.nextAttempt)
		if file.LastSuccessAtNs > status.LastSuccessAtNs {
			status.LastSuccessAtNs = file.LastSuccessAtNs
		}
		if status.ErrorCode == "" && file.ErrorCode != "" {
			status.ErrorCode = file.ErrorCode
		}
		if file.Downloading {
			status.Downloading = true
		}
		status.Files = append(status.Files, file)
	}
	if registered {
		for _, src := range sources {
			if ready, reason := geoSourceReady(src, setting); !ready {
				status.Ready = false
				if status.Reason == "" {
					status.Reason = reason
				}
				status.ErrorCode = GeoCodeNotReady
			}
		}
	}
	return status
}

// RequestRefresh downloads the databases of one provider now. The result
// reports what happened file by file: a provider that is not configured is a
// recorded skip, a download failure is a recorded failure, and neither is an
// error of the caller.
func (m *GeoManager) RequestRefresh(ctx context.Context, providerID string) (GeoRefreshResult, error) {
	if m == nil {
		return GeoRefreshResult{}, ErrGeoUnavailable
	}
	id := strings.TrimSpace(providerID)
	sources := m.byProvider[id]
	if len(sources) == 0 {
		return GeoRefreshResult{}, fmt.Errorf("%w: %s", ErrGeoUnknownProvider, id)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, m.manualTimeout)
	defer cancel()

	started := m.now().UTC()
	result := GeoRefreshResult{ProviderID: id, Files: make([]GeoRefreshFileResult, 0, len(sources))}
	for _, src := range sources {
		if !m.tryAcquire() {
			result.Files = append(result.Files, m.recordSkip(src, GeoCodeBusy, "another database download is running"))
			continue
		}
		result.Files = append(result.Files, m.refreshFile(ctx, src, true))
		m.release()
	}
	result.Outcome = aggregateGeoOutcome(result.Files)
	result.Duration = DurationString(ageOf(m.now().UTC(), started))
	return result, nil
}

// loop evaluates the databases on every poll tick; the first pass runs as soon
// as Start returns.
func (m *GeoManager) loop() {
	defer m.wg.Done()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-timer.C:
		}
		m.refreshDue()
		timer.Reset(m.pollInterval)
	}
}

// refreshDue downloads every database whose file is missing, invalid or older
// than the refresh interval. Every outcome is recorded per file.
func (m *GeoManager) refreshDue() {
	for _, src := range m.sources {
		if m.ctx.Err() != nil {
			return
		}
		if !m.due(src.FileName) {
			continue
		}
		ctx, cancel := context.WithTimeout(m.ctx, m.requestTimeout)
		if m.acquire(ctx) {
			_ = m.refreshFile(ctx, src, false)
			m.release()
		}
		cancel()
	}
}

// refreshFile brings one database file up to date. force skips the freshness
// check (the manual action); every returned result carries an outcome.
func (m *GeoManager) refreshFile(ctx context.Context, src GeoDBSource, force bool) GeoRefreshFileResult {
	result := GeoRefreshFileResult{Name: src.FileName, Outcome: GeoOutcomeFailed}
	target := filepath.Join(m.dir, src.FileName)

	setting, registered := m.settingFor(src.ProviderID)
	if !registered {
		return m.recordSkip(src, GeoCodeNotReady, "the data source is not registered")
	}
	if ready, reason := geoSourceReady(src, setting); !ready {
		return m.recordSkip(src, GeoCodeNotReady, reason)
	}
	if !force {
		usable, note := m.installedUsable(src.FileName, target)
		if usable {
			m.settle(src.FileName, GeoOutcomeUpToDate, "", "")
			result.Outcome = GeoOutcomeUpToDate
			result.SizeBytes = fileSize(target)
			return result
		}
		if note != "" {
			m.note(src.FileName, note)
		}
	}
	if m.downloader == nil {
		return m.recordFailure(src, GeoCodeUnavailable, "the database download is not available")
	}

	started := m.now().UTC()
	m.startAttempt(src.FileName, started)
	body, code, reason, err := m.fetch(ctx, src, setting)
	if err != nil {
		return m.recordFailure(src, code, reason)
	}
	tmp, err := m.writeTemp(src, body)
	if err != nil {
		code, reason = geoFailureOf(err)
		return m.recordFailure(src, code, reason)
	}
	// The temporary file is removed on every path; the validated rename below
	// is the only way a new database reaches its final name.
	defer func() { _ = os.Remove(tmp) }()

	if err := validateGeoDatabaseFile(tmp); err != nil {
		return m.recordFailure(src, GeoCodeInvalidDB, "the downloaded file is not a valid MaxMind database")
	}
	size, err := m.install(target, tmp, src.FileName)
	if err != nil {
		code, reason = geoFailureOf(err)
		return m.recordFailure(src, code, reason)
	}
	now := m.now().UTC()
	m.settle(src.FileName, GeoOutcomeRefreshed, "", "")
	result.Outcome = GeoOutcomeRefreshed
	result.SizeBytes = size
	result.DurationMs = int64(ageOf(now, started).Milliseconds())
	m.logf("[intel-geo] %s (%s) refreshed: %d bytes in %s", src.FileName, src.Label, size, DurationString(ageOf(now, started)))
	return result
}

// fetch downloads the first candidate that answers. A 404 of the current
// DB-IP month moves on to the previous month; any other failure is recorded
// and the next candidate is tried as well, so a vendor hiccup on the primary
// name still ends with a usable database.
func (m *GeoManager) fetch(ctx context.Context, src GeoDBSource, setting Setting) ([]byte, string, string, error) {
	candidates := src.Candidates(m.now().UTC())
	if len(candidates) == 0 {
		return nil, GeoCodeUnavailable, "the database has no download URL", ErrGeoUnavailable
	}
	var (
		lastCode   = GeoCodeRequest
		lastReason = "the download failed"
		lastErr    error
		tried      int
	)
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, GeoCodeCancelled, "the download was cancelled", err
		}
		target, err := geoRequestURL(src, setting, candidate)
		if err != nil {
			return nil, GeoCodeRequest, "the database URL is invalid", err
		}
		body, err := m.downloader.Download(ctx, target)
		if err == nil {
			if int64(len(body)) > m.maxDownload {
				return nil, GeoCodeTooLarge, "the download is larger than the configured limit", errGeoTooLarge
			}
			return body, "", "", nil
		}
		tried++
		lastErr = err
		if status, ok := geoHTTPStatus(err); ok {
			lastCode = GeoCodeHTTPStatus
			lastReason = "the download was answered with HTTP " + strconv.Itoa(status)
			continue
		}
		lastCode = GeoCodeRequest
		lastReason = "the download request failed"
	}
	if lastErr == nil {
		return nil, GeoCodeUnavailable, "the database has no download URL", ErrGeoUnavailable
	}
	if tried > 1 {
		lastReason += " (tried " + strconv.Itoa(tried) + " sources)"
	}
	return nil, lastCode, lastReason, lastErr
}

// geoRequestURL renders the request URL of one candidate. The MaxMind
// credentials travel as HTTP basic authentication and the IPinfo token as a
// query parameter; both stay inside the request (R6).
func geoRequestURL(src GeoDBSource, setting Setting, candidate string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(candidate))
	if err != nil || parsed.Host == "" {
		return "", errors.New("invalid download URL")
	}
	if src.Token != nil {
		if token := src.Token(setting); token != "" {
			query := parsed.Query()
			query.Set("token", token)
			parsed.RawQuery = query.Encode()
		}
	}
	if src.BasicAuth != nil {
		user, password := src.BasicAuth(setting)
		if user != "" || password != "" {
			parsed.User = url.UserPassword(user, password)
		}
	}
	return parsed.String(), nil
}

// writeTemp streams the downloaded body into a temporary file inside the geo
// directory, removing the container of the vendor file. The caller removes the
// returned file.
func (m *GeoManager) writeTemp(src GeoDBSource, body []byte) (string, error) {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return "", geoWriteFailure(err)
	}
	file, err := os.CreateTemp(m.dir, "."+src.FileName+".download-*")
	if err != nil {
		return "", geoWriteFailure(err)
	}
	name := file.Name()
	_, err = unwrapDatabase(file, body, src, m.maxDatabase)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if closeErr != nil {
		_ = os.Remove(name)
		return "", geoWriteFailure(closeErr)
	}
	return name, nil
}

// install moves the validated temporary file into place. The previous
// generation is kept as "<name>.previous": a working database is never gone,
// and the fallback of a failed download stays recoverable.
func (m *GeoManager) install(target, tmp, fileName string) (int64, error) {
	previous := target + GeoDBPreviousSuffix
	moved := false
	if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() {
		if err := os.Rename(target, previous); err == nil {
			moved = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, geoWriteFailure(err)
		}
	}
	if err := os.Rename(tmp, target); err != nil {
		if moved {
			// Best effort: put the previous generation back so the installed
			// database is not lost by a failed swap.
			_ = os.Rename(previous, target)
		}
		return 0, geoWriteFailure(err)
	}
	if err := m.markValidated(fileName); err != nil {
		return 0, geoWriteFailure(err)
	}
	return fileSize(target), nil
}

// unwrapDatabase removes the vendor container (gzip or tar.gz) and enforces
// the unpacked size bound while streaming into dst.
func unwrapDatabase(dst io.Writer, body []byte, src GeoDBSource, maxBytes int64) (int64, error) {
	reader := io.Reader(bytes.NewReader(body))
	compressed := src.Kind == GeoDBGzip || src.Kind == GeoDBTarGzip ||
		(src.Kind == GeoDBAuto && len(body) > 2 && body[0] == 0x1f && body[1] == 0x8b)
	if compressed {
		gunzip, err := gzip.NewReader(reader)
		if err != nil {
			return 0, geoFailed(GeoCodeInvalidDB, "the downloaded file is not a gzip archive")
		}
		defer func() { _ = gunzip.Close() }()
		reader = gunzip
	}
	if src.Kind == GeoDBTarGzip {
		archive := tar.NewReader(reader)
		for {
			header, err := archive.Next()
			if err != nil {
				return 0, geoFailed(GeoCodeInvalidDB, "the downloaded archive does not contain "+src.FileName)
			}
			if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
				continue
			}
			if filepath.Base(header.Name) != src.FileName {
				continue
			}
			return copyBounded(dst, archive, maxBytes)
		}
	}
	return copyBounded(dst, reader, maxBytes)
}

// copyBounded copies at most maxBytes+1 bytes and reports the over-limit case.
func copyBounded(dst io.Writer, src io.Reader, maxBytes int64) (int64, error) {
	written, err := io.Copy(dst, io.LimitReader(src, maxBytes+1))
	if err != nil {
		return written, err
	}
	if written > maxBytes {
		return written, errGeoTooLarge
	}
	return written, nil
}

// validateGeoDatabaseFile proves that a downloaded file really is a MaxMind
// database before it replaces the installed generation: the metadata must open
// and one public resolver address must traverse the search tree. A truncated,
// corrupt or foreign file fails here.
func validateGeoDatabaseFile(path string) error {
	reader, err := maxminddb.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	if metadata := reader.Metadata; strings.TrimSpace(metadata.DatabaseType) == "" ||
		metadata.NodeCount == 0 || (metadata.IPVersion != 4 && metadata.IPVersion != 6) {
		return errors.New("incomplete MaxMind database metadata")
	}
	// The probe address may have no record (the lookup simply stays empty); a
	// decode error however means the tree or the data section is broken.
	probe := netip.MustParseAddr("1.1.1.1")
	var record map[string]any
	return reader.Lookup(net.IP(probe.AsSlice()), &record)
}

// installedUsable reports whether the installed file is fresh enough to keep.
// The file itself is validated once per process, so an externally corrupted
// database is re-downloaded instead of being trusted forever.
func (m *GeoManager) installedUsable(fileName, target string) (bool, string) {
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return false, ""
	}
	if ageOf(m.now().UTC(), info.ModTime().UTC()) >= m.refreshInterval {
		return false, ""
	}
	if !m.takeValidation(fileName) {
		return true, ""
	}
	if err := validateGeoDatabaseFile(target); err != nil {
		return false, "the installed database is not a valid MaxMind database"
	}
	return true, ""
}

// due reports whether the next attempt of one file is allowed: the poll tick
// and the failure backoff both feed nextAttempt.
func (m *GeoManager) due(fileName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.now().UTC().Before(m.stateLocked(fileName).nextAttempt)
}

// takeValidation claims the single validation of one installed file.
func (m *GeoManager) takeValidation(fileName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.stateLocked(fileName)
	if state.validated {
		return false
	}
	state.validated = true
	return true
}

// markValidated records that the file on disk is the file this manager wrote.
func (m *GeoManager) markValidated(fileName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateLocked(fileName).validated = true
	return nil
}

// startAttempt records the beginning of one download.
func (m *GeoManager) startAttempt(fileName string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.stateLocked(fileName)
	state.lastAttempt = now
	state.downloading = true
	state.reason = ""
}

// settle records a finished attempt without a failure.
func (m *GeoManager) settle(fileName, outcome, code, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.stateLocked(fileName)
	state.downloading = false
	state.lastOutcome = outcome
	state.errorCode = code
	state.reason = reason
	if outcome == GeoOutcomeRefreshed {
		state.lastSuccess = m.now().UTC()
		state.lastAttempt = state.lastSuccess
		state.failures = 0
		state.nextAttempt = state.lastSuccess.Add(m.refreshInterval)
		return
	}
	if state.lastAttempt.IsZero() {
		state.lastAttempt = m.now().UTC()
	}
	state.nextAttempt = m.now().UTC().Add(m.pollInterval)
}

// note records an observed problem without changing the outcome.
func (m *GeoManager) note(fileName, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateLocked(fileName).reason = reason
}

// recordSkip records a database that was not downloaded because its provider
// is not configured or disabled (WP09 §3).
func (m *GeoManager) recordSkip(src GeoDBSource, code, reason string) GeoRefreshFileResult {
	m.mu.Lock()
	state := m.stateLocked(src.FileName)
	state.downloading = false
	state.lastOutcome = GeoOutcomeSkipped
	state.errorCode = code
	state.reason = reason
	state.nextAttempt = m.now().UTC().Add(m.pollInterval)
	m.mu.Unlock()
	return GeoRefreshFileResult{Name: src.FileName, Outcome: GeoOutcomeSkipped, ErrorCode: code, Reason: reason}
}

// recordFailure records a failed download, keeps the installed database in
// place and backs the next attempt off.
func (m *GeoManager) recordFailure(src GeoDBSource, code, reason string) GeoRefreshFileResult {
	m.mu.Lock()
	state := m.stateLocked(src.FileName)
	state.downloading = false
	state.lastOutcome = GeoOutcomeFailed
	state.errorCode = code
	state.reason = reason
	state.failures++
	state.nextAttempt = m.now().UTC().Add(geoBackoff(state.failures))
	state.validated = true
	m.mu.Unlock()
	if code != GeoCodeCancelled {
		m.logf("[intel-geo] %s (%s) download failed: %s (%s)", src.FileName, src.Label, reason, code)
	}
	return GeoRefreshFileResult{Name: src.FileName, Outcome: GeoOutcomeFailed, ErrorCode: code, Reason: reason}
}

// geoBackoff doubles the delay per consecutive failure up to geoDBBackoffMax.
func geoBackoff(failures int) time.Duration {
	delay := geoDBBackoffBase
	for i := 1; i < failures && delay < geoDBBackoffMax; i++ {
		delay *= 2
	}
	if delay > geoDBBackoffMax {
		delay = geoDBBackoffMax
	}
	return delay
}

// stateOf copies the state of one file.
func (m *GeoManager) stateOf(fileName string) geoDBState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *m.stateLocked(fileName)
}

func (m *GeoManager) stateLocked(fileName string) *geoDBState {
	state, ok := m.states[fileName]
	if !ok {
		state = &geoDBState{}
		m.states[fileName] = state
	}
	return state
}

// settingFor resolves the effective settings of one provider.
func (m *GeoManager) settingFor(providerID string) (Setting, bool) {
	if m.setting == nil {
		return Setting{}, false
	}
	return m.setting(strings.TrimSpace(providerID))
}

// acquire takes the single download slot, bounded by ctx.
func (m *GeoManager) acquire(ctx context.Context) bool {
	select {
	case m.slot <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// tryAcquire takes the download slot without waiting.
func (m *GeoManager) tryAcquire() bool {
	select {
	case m.slot <- struct{}{}:
		return true
	default:
		return false
	}
}

func (m *GeoManager) release() {
	select {
	case <-m.slot:
	default:
	}
}

// geoSourceReady asks the catalog whether a database may be downloaded with
// these settings.
func geoSourceReady(src GeoDBSource, setting Setting) (bool, string) {
	if src.Ready == nil {
		return true, ""
	}
	return src.Ready(setting)
}

// geoHTTPStatus extracts the HTTP status of a downloader error.
func geoHTTPStatus(err error) (int, bool) {
	var statusErr *netutil.HTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode, true
	}
	return 0, false
}

// aggregateGeoOutcome reduces the per-file outcomes to one action outcome.
func aggregateGeoOutcome(files []GeoRefreshFileResult) string {
	outcome := GeoOutcomeUpToDate
	if len(files) == 0 {
		return outcome
	}
	for _, file := range files {
		switch file.Outcome {
		case GeoOutcomeFailed:
			return GeoOutcomeFailed
		case GeoOutcomeRefreshed:
			outcome = GeoOutcomeRefreshed
		case GeoOutcomeSkipped:
			if outcome != GeoOutcomeRefreshed {
				outcome = GeoOutcomeSkipped
			}
		}
	}
	return outcome
}

// errGeoTooLarge is the internal over-limit failure of a download.
var errGeoTooLarge = geoFailed(GeoCodeTooLarge, "the database is larger than the configured limit")

// geoFailure is an internal download failure with a stable code and a reason
// that never repeats a credential or a download URL.
type geoFailure struct {
	code   string
	reason string
}

func (f *geoFailure) Error() string { return f.code + ": " + f.reason }

func geoFailed(code, reason string) error { return &geoFailure{code: code, reason: reason} }

// geoFailureOf extracts the code and reason of a download failure.
func geoFailureOf(err error) (string, string) {
	var failure *geoFailure
	if errors.As(err, &failure) {
		return failure.code, failure.reason
	}
	return GeoCodeRequest, "the download failed"
}

// geoWriteFailure maps a cache write failure: a full disk gets its own code so
// the settings page can tell it apart from a permission problem.
func geoWriteFailure(err error) error {
	if errors.Is(err, syscall.ENOSPC) {
		return geoFailed(GeoCodeDiskFull, "the cache directory ran out of space")
	}
	return geoFailed(GeoCodeWrite, "the database could not be written to the cache directory")
}

// fileSize reports the size of an installed database (0 when it is missing).
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	return info.Size()
}

// ageOf renders the age of an instant, never negative.
func ageOf(now, then time.Time) time.Duration {
	age := now.Sub(then)
	if age < 0 {
		return 0
	}
	return age
}

func unixNanoOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixNano()
}
