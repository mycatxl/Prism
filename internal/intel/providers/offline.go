package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang"

	"prism/internal/quality"
)

// ---------------------------------------------------------------------------
// Country lookup (Resin's country.mmdb)
// ---------------------------------------------------------------------------

// CountryLookup is the region-filter database of the running application
// (internal/geoip.Service satisfies it).
type CountryLookup interface {
	Lookup(ip netip.Addr) string
}

// GeoCountryOptions configures the geo_country offline provider.
type GeoCountryOptions struct {
	Lookup CountryLookup
	TTL    time.Duration
	Now    func() time.Time
}

// GeoCountry reports the country of an address from the local country.mmdb.
type GeoCountry struct {
	spec   Spec
	lookup CountryLookup
	ttl    time.Duration
	now    func() time.Time
}

// NewGeoCountryProvider builds the geo_country provider.
func NewGeoCountryProvider(opts GeoCountryOptions) *GeoCountry {
	ttl := opts.TTL
	if ttl <= 0 {
		// country.mmdb is refreshed daily from the MetaCubeX rules release, so
		// the evidence stays valid for a month (WP09 §3 lists no explicit TTL).
		ttl = 30 * 24 * time.Hour
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &GeoCountry{
		spec: Spec{
			ID: "geo_country", Name: "country.mmdb", Website: "https://github.com/MetaCubeX/meta-rules-dat",
			Terms: "Bundled with Prism and updated from the MetaCubeX meta-rules-dat release; " +
				"no external quota applies. Region filtering keeps using the same database.",
			Kind: KindOffline, Profile: GeoCountryProfile,
			RequiresKey: false, DefaultEnabled: true,
			DefaultDailyLimit: 0, DefaultQPS: 0, BatchSize: 1,
			DefaultTTL: ttl, SupportsIPv6: true,
		},
		lookup: opts.Lookup, ttl: ttl, now: now,
	}
}

// Spec implements OfflineProvider.
func (p *GeoCountry) Spec() Spec { return p.spec }

// Lookup implements OfflineProvider.
func (p *GeoCountry) Lookup(raw netip.Addr) Result {
	ip, unsupported, ok := PublicIP(raw)
	if !ok {
		return unsupported
	}
	if p.lookup == nil {
		return Failure(CodeUnavailable, "country.mmdb is not available")
	}
	code := strings.ToUpper(strings.TrimSpace(p.lookup.Lookup(ip)))
	if len(code) != 2 {
		return Failure(CodeUnavailable, "country.mmdb has no record for this address")
	}
	now := p.now().UTC()
	return Result{Evidence: &quality.Evidence{
		IP: ip.String(), Provider: "geo_country", Profile: GeoCountryProfile,
		IPType: "unknown", CountryCode: code, Grade: "unknown",
		ObservedAt: now, ValidUntil: now.Add(p.ttl),
	}}
}

// ---------------------------------------------------------------------------
// mmdb-backed offline providers
// ---------------------------------------------------------------------------

// MMDBRecord is the normalised record extracted from a MaxMind-compatible
// database. Missing fields stay zero.
type MMDBRecord struct {
	ASN          int
	Organization string
	CountryCode  string
	RegisteredCC string
	City         string
	Region       string
}

// MMDBFile is an opened MaxMind-compatible database.
type MMDBFile struct {
	reader *maxminddb.Reader
	path   string
}

// OpenMMDBFile opens a local mmdb database.
func OpenMMDBFile(path string) (*MMDBFile, error) {
	reader, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	return &MMDBFile{reader: reader, path: path}, nil
}

// Close releases the database.
func (f *MMDBFile) Close() error {
	if f == nil || f.reader == nil {
		return nil
	}
	return f.reader.Close()
}

// Path returns the database file path.
func (f *MMDBFile) Path() string {
	if f == nil {
		return ""
	}
	return f.path
}

// Lookup decodes one address. The record is walked as a generic map because the
// ASN, City and Country databases share no single schema.
func (f *MMDBFile) Lookup(ip netip.Addr) (MMDBRecord, bool) {
	var record MMDBRecord
	if f == nil || f.reader == nil || !ip.IsValid() {
		return record, false
	}
	var raw map[string]any
	if err := f.reader.Lookup(net.IP(ip.Unmap().AsSlice()), &raw); err != nil || raw == nil {
		return record, false
	}
	record.ASN = intField(raw["autonomous_system_number"])
	record.Organization = stringField(raw["autonomous_system_organization"])
	if record.Organization == "" {
		record.Organization = stringField(raw["organization"])
	}
	record.CountryCode = countryCode(mapField(raw["country"]))
	record.RegisteredCC = countryCode(mapField(raw["registered_country"]))
	record.City = firstName(mapField(raw["city"]))
	if region := firstSubdivision(raw["subdivisions"]); region != "" {
		record.Region = region
	}
	return record, true
}

// MMDBOptions configures one database-backed offline provider.
type MMDBOptions struct {
	ID       string
	Name     string
	Website  string
	Terms    string
	Profile  string
	TTL      time.Duration
	CityPath string
	ASNPath  string
	Now      func() time.Time
}

// MMDBProvider reads ASN/city/country records from one or two local databases.
// The files are opened lazily so a database downloaded after startup is picked
// up without a restart, and a missing file is an explainable failure.
type MMDBProvider struct {
	spec     Spec
	ttl      time.Duration
	cityPath string
	asnPath  string
	now      func() time.Time

	mu   sync.Mutex
	city *MMDBFile
	asn  *MMDBFile
}

// NewMMDBProvider builds one mmdb-backed offline provider.
func NewMMDBProvider(opts MMDBOptions) *MMDBProvider {
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &MMDBProvider{
		spec: Spec{
			ID: opts.ID, Name: opts.Name, Website: opts.Website, Terms: opts.Terms,
			Kind: KindOffline, Profile: opts.Profile,
			RequiresKey: false, DefaultEnabled: true,
			DefaultDailyLimit: 0, DefaultQPS: 0, BatchSize: 1,
			DefaultTTL: ttl, SupportsIPv6: true,
		},
		ttl: ttl, cityPath: opts.CityPath, asnPath: opts.ASNPath, now: now,
	}
}

// Spec implements OfflineProvider.
func (p *MMDBProvider) Spec() Spec { return p.spec }

// Close releases the open databases.
func (p *MMDBProvider) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.city != nil {
		_ = p.city.Close()
		p.city = nil
	}
	if p.asn != nil {
		_ = p.asn.Close()
		p.asn = nil
	}
}

// Lookup implements OfflineProvider.
func (p *MMDBProvider) Lookup(raw netip.Addr) Result {
	ip, unsupported, ok := PublicIP(raw)
	if !ok {
		return unsupported
	}
	city, asn, err := p.open()
	if err != nil {
		return Failure(CodeUnavailable, p.spec.Name+" database is not installed")
	}
	record := MMDBRecord{}
	matched := false
	if city != nil {
		if row, ok := city.Lookup(ip); ok {
			record.CountryCode = row.CountryCode
			record.RegisteredCC = row.RegisteredCC
			record.City = row.City
			record.Region = row.Region
			matched = true
		}
	}
	if asn != nil {
		if row, ok := asn.Lookup(ip); ok {
			record.ASN = row.ASN
			if row.Organization != "" {
				record.Organization = row.Organization
			}
			if record.CountryCode == "" {
				record.CountryCode = row.CountryCode
			}
			matched = true
		}
	}
	if !matched {
		return Failure(CodeUnavailable, p.spec.Name+" has no record for this address")
	}
	now := p.now().UTC()
	evidence := &quality.Evidence{
		IP: ip.String(), Provider: p.spec.ID, Profile: p.spec.Profile,
		IPType: "unknown", Grade: "unknown",
		CountryCode:  boundedText(record.CountryCode, 8),
		RegisteredCC: boundedText(record.RegisteredCC, 8),
		City:         boundedText(record.City, 120),
		Region:       boundedText(record.Region, 120),
		Organization: boundedText(record.Organization, 240),
		ObservedAt:   now, ValidUntil: now.Add(p.ttl),
	}
	if record.ASN > 0 {
		evidence.ASNNumber = record.ASN
		evidence.ASN = "AS" + itoa(record.ASN)
	}
	return Result{Evidence: evidence}
}

// open lazily opens the configured databases.
func (p *MMDBProvider) open() (*MMDBFile, *MMDBFile, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.city == nil && p.cityPath != "" {
		if file, err := OpenMMDBFile(p.cityPath); err == nil {
			p.city = file
		}
	}
	if p.asn == nil && p.asnPath != "" {
		if file, err := OpenMMDBFile(p.asnPath); err == nil {
			p.asn = file
		}
	}
	if p.city == nil && p.asn == nil {
		return nil, nil, errors.New("no mmdb database is available")
	}
	return p.city, p.asn, nil
}

func mapField(value any) map[string]any {
	if record, ok := value.(map[string]any); ok {
		return record
	}
	return nil
}

func countryCode(country map[string]any) string {
	if country == nil {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(stringField(country["iso_code"])))
}

func firstName(record map[string]any) string {
	if record == nil {
		return ""
	}
	names := mapField(record["names"])
	if names == nil {
		return ""
	}
	if name := stringField(names["en"]); name != "" {
		return name
	}
	for _, name := range names {
		if text := stringField(name); text != "" {
			return text
		}
	}
	return ""
}

func firstSubdivision(value any) string {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return ""
	}
	return firstName(mapField(list[0]))
}

func stringField(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strings.TrimSpace(itoaFloat(typed))
	default:
		return ""
	}
}

func intField(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case uint64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
		return 0
	default:
		return 0
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func itoaFloat(value float64) string {
	return itoa(int(value))
}

// ---------------------------------------------------------------------------
// torproject (Onionoo public relay registry)
// ---------------------------------------------------------------------------

const (
	torRegistryURL      = "https://onionoo.torproject.org/details?type=relay&running=true&fields=or_addresses,exit_addresses,flags,last_seen"
	torRegistryMaxBody  = 4 * 1024 * 1024
	torRefreshInterval  = time.Hour
	torRefreshMinGap    = 5 * time.Minute
	torRegistryValidity = 6 * time.Hour
)

const (
	torRelay uint8 = 1 << iota
	torGuard
	torExit
)

// TorStatus is the observable state of the Tor registry.
type TorStatus struct {
	ID        string     `json:"id"`
	Ready     bool       `json:"ready"`
	Entries   int        `json:"entries"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	ErrorCode string     `json:"error_code,omitempty"`
}

// TorRegistryOptions configures the public Tor relay registry.
type TorRegistryOptions struct {
	// URL overrides the Onionoo endpoint; tests point it at httptest.
	URL    string
	Client *http.Client
	Now    func() time.Time
}

// TorRegistry holds the decoded Onionoo relay roles. Lookups never wait on the
// network: a stale registry schedules a bounded background refresh instead.
type TorRegistry struct {
	mu          sync.Mutex
	roles       map[netip.Addr]uint8
	published   time.Time
	checked     time.Time
	nextRefresh time.Time
	modified    string
	refreshing  bool
	errorCode   string
	client      *http.Client
	url         string
	now         func() time.Time
}

// NewTorRegistry builds an empty registry.
func NewTorRegistry(opts TorRegistryOptions) *TorRegistry {
	client := opts.Client
	if client == nil {
		client = NewStrictClient(12 * time.Second)
	}
	target := strings.TrimSpace(opts.URL)
	if target == "" {
		target = torRegistryURL
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &TorRegistry{client: client, url: target, now: now}
}

// Roles returns the roles of one address. The second result is false when the
// registry has no entry for the address (which cannot rule out a private bridge).
func (r *TorRegistry) Roles(ip netip.Addr) ([]string, bool) {
	if r == nil || !ip.IsValid() {
		return nil, false
	}
	r.mu.Lock()
	bits, ok := r.roles[ip.Unmap()]
	r.mu.Unlock()
	if !ok {
		return nil, false
	}
	var roles []string
	if bits&torExit != 0 {
		roles = append(roles, "exit")
	}
	if bits&torGuard != 0 {
		roles = append(roles, "guard")
	}
	if bits&torRelay != 0 {
		roles = append(roles, "relay")
	}
	return roles, true
}

// Snapshot returns the registry freshness metadata.
func (r *TorRegistry) Snapshot() (published, checked time.Time, entries int, errCode string) {
	if r == nil {
		return time.Time{}, time.Time{}, 0, "REGISTRY_UNAVAILABLE"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.published, r.checked, len(r.roles), r.errorCode
}

// Status renders the registry status for the settings page.
func (r *TorRegistry) Status() TorStatus {
	published, checked, entries, errCode := r.Snapshot()
	status := TorStatus{ID: "torproject", Entries: entries, ErrorCode: errCode}
	status.Ready = entries > 0 && r.now().UTC().Sub(published) < torRegistryValidity
	if !published.IsZero() {
		value := published
		status.UpdatedAt = &value
	}
	_ = checked
	return status
}

// Seed installs a decoded registry (used by tests and by the startup cache).
func (r *TorRegistry) Seed(roles map[netip.Addr]uint8, published, checked time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.roles = roles
	r.published = published
	r.checked = checked
}

// Refresh fetches and decodes the Onionoo registry once.
func (r *TorRegistry) Refresh(ctx context.Context) error {
	r.mu.Lock()
	modified := r.modified
	client := r.client
	target := r.url
	r.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return r.failed("REGISTRY_REQUEST", torRefreshMinGap)
	}
	req.Header.Set("User-Agent", providerUserAgent)
	req.Header.Set("Accept", "application/json")
	if modified != "" {
		req.Header.Set("If-Modified-Since", modified)
	}
	resp, err := client.Do(req)
	if err != nil {
		return r.failed("REGISTRY_UNAVAILABLE", torRefreshMinGap)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		r.mu.Lock()
		r.nextRefresh = r.now().UTC().Add(torRefreshInterval)
		r.errorCode = ""
		r.mu.Unlock()
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return r.failed("REGISTRY_UNAVAILABLE", torRefreshMinGap)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, torRegistryMaxBody+1))
	if err != nil || len(body) > torRegistryMaxBody {
		return r.failed("REGISTRY_RESPONSE", torRefreshMinGap)
	}
	now := r.now().UTC()
	roles, published, err := DecodeTorRegistry(body, now)
	if err != nil {
		return r.failed("REGISTRY_RESPONSE", torRefreshMinGap)
	}
	r.mu.Lock()
	r.roles, r.published, r.checked = roles, published, now
	r.modified = resp.Header.Get("Last-Modified")
	r.nextRefresh = now.Add(torRefreshInterval)
	r.refreshing = false
	r.errorCode = ""
	r.mu.Unlock()
	return nil
}

func (r *TorRegistry) failed(code string, delay time.Duration) error {
	r.mu.Lock()
	r.refreshing = false
	r.errorCode = code
	r.nextRefresh = r.now().UTC().Add(delay)
	r.mu.Unlock()
	return errors.New(code)
}

// maybeRefresh schedules a background refresh when the registry is stale. It is
// bounded: at most one refresh runs at a time and never more often than
// torRefreshMinGap.
func (r *TorRegistry) maybeRefresh() {
	if r == nil {
		return
	}
	now := r.now().UTC()
	r.mu.Lock()
	if r.refreshing || now.Before(r.nextRefresh) {
		r.mu.Unlock()
		return
	}
	r.refreshing = true
	r.nextRefresh = now.Add(torRefreshMinGap)
	r.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = r.Refresh(ctx)
	}()
}

// TorProviderOptions configures the torproject offline provider.
type TorProviderOptions struct {
	Registry *TorRegistry
	TTL      time.Duration
	Now      func() time.Time
}

// TorProvider reports the public Tor roles of an address.
type TorProvider struct {
	spec     Spec
	registry *TorRegistry
	ttl      time.Duration
	now      func() time.Time
}

// NewTorProvider builds the torproject provider.
func NewTorProvider(opts TorProviderOptions) *TorProvider {
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = torRegistryValidity
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &TorProvider{
		spec: Spec{
			ID: "torproject", Name: "Tor Project Onionoo", Website: "https://onionoo.torproject.org/",
			Terms: "Public CC0 relay registry; Prism downloads it at most once an hour and " +
				"never sends an inventory address. See https://metrics.torproject.org/onionoo.html.",
			Kind: KindOffline, Profile: TorProfile,
			RequiresKey: false, DefaultEnabled: true,
			DefaultDailyLimit: 0, DefaultQPS: 0, BatchSize: 1,
			DefaultTTL: ttl, SupportsIPv6: true,
		},
		registry: opts.Registry, ttl: ttl, now: now,
	}
}

// Spec implements OfflineProvider.
func (p *TorProvider) Spec() Spec { return p.spec }

// Lookup implements OfflineProvider. A missing registry entry is not evidence:
// an address that is not a public relay stays unobserved for this provider.
func (p *TorProvider) Lookup(raw netip.Addr) Result {
	ip, unsupported, ok := PublicIP(raw)
	if !ok {
		return unsupported
	}
	if p.registry == nil {
		return Failure(CodeUnavailable, "Tor registry is not available")
	}
	p.registry.maybeRefresh()
	roles, listed := p.registry.Roles(ip)
	if !listed {
		return Failure(CodeUnavailable, "Tor registry has no public relay for this address")
	}
	published, checked, _, _ := p.registry.Snapshot()
	validUntil := checked.Add(p.ttl)
	if limit := published.Add(p.ttl); limit.Before(validUntil) {
		validUntil = limit
	}
	now := p.now().UTC()
	if !validUntil.After(now) {
		validUntil = now.Add(p.ttl)
	}
	signals := quality.Signals{}
	if len(roles) > 0 {
		yes := true
		signals.Tor = &yes
	}
	evidence := &quality.Evidence{
		IP: ip.String(), Provider: "torproject", Profile: TorProfile,
		IPType: "unknown", SourceType: "Public Tor registry", Grade: "unknown",
		TorRoles: roles, Signals: signals, ObservedAt: now, ValidUntil: validUntil,
	}
	if !published.IsZero() {
		value := published
		evidence.SourceUpdatedAt = &value
	}
	return Result{Evidence: evidence}
}

// DecodeTorRegistry decodes one Onionoo details payload into relay roles.
func DecodeTorRegistry(body []byte, now time.Time) (map[netip.Addr]uint8, time.Time, error) {
	var data struct {
		Published string `json:"relays_published"`
		Relays    []struct {
			ORAddresses   []string `json:"or_addresses"`
			ExitAddresses []string `json:"exit_addresses"`
			Flags         []string `json:"flags"`
			LastSeen      string   `json:"last_seen"`
		} `json:"relays"`
	}
	invalid := errors.New("invalid public Tor registry data")
	if len(body) > torRegistryMaxBody || json.Unmarshal(body, &data) != nil ||
		data.Relays == nil || len(data.Relays) > 30000 {
		return nil, time.Time{}, invalid
	}
	published, err := time.Parse("2006-01-02 15:04:05", data.Published)
	if err != nil || published.After(now.Add(5*time.Minute)) || now.Sub(published) > torRegistryValidity {
		return nil, time.Time{}, invalid
	}
	result := make(map[netip.Addr]uint8)
	for _, relay := range data.Relays {
		lastSeen, err := time.Parse("2006-01-02 15:04:05", relay.LastSeen)
		if err != nil || now.Sub(lastSeen) > torRegistryValidity || lastSeen.After(now.Add(5*time.Minute)) {
			continue
		}
		guard, running := false, false
		for _, flag := range relay.Flags {
			switch flag {
			case "Guard":
				guard = true
			case "Running":
				running = true
			}
		}
		if !running {
			continue
		}
		for _, raw := range relay.ORAddresses {
			// OR addresses may contain a port list, so split host from port text.
			host, _, err := net.SplitHostPort(raw)
			if err != nil {
				continue
			}
			ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
			if err != nil {
				continue
			}
			if _, err := quality.PublicIP(ip); err != nil {
				continue
			}
			bits := torRelay
			if guard {
				bits |= torGuard
			}
			result[ip.Unmap()] |= bits
		}
		for _, raw := range relay.ExitAddresses {
			ip, err := netip.ParseAddr(raw)
			if err != nil {
				continue
			}
			if _, err := quality.PublicIP(ip); err != nil {
				continue
			}
			result[ip.Unmap()] |= torExit
		}
	}
	return result, published, nil
}
