package providers

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"prism/internal/model"
)

// --- offline providers -----------------------------------------------------

type fakeCountry struct{ code string }

func (f fakeCountry) Lookup(netip.Addr) string { return f.code }

func TestGeoCountryOffline(t *testing.T) {
	provider := NewGeoCountryProvider(GeoCountryOptions{
		Lookup: fakeCountry{code: "jp"}, Now: func() time.Time { return testNow },
	})
	result := provider.Lookup(testIP)
	evidence := mustEvidence(t, result)
	if evidence.CountryCode != "JP" {
		t.Fatalf("country: %q", evidence.CountryCode)
	}
	if evidence.Provider != "geo_country" {
		t.Fatalf("provider: %s", evidence.Provider)
	}

	missing := NewGeoCountryProvider(GeoCountryOptions{Now: func() time.Time { return testNow }})
	expectCode(t, missing.Lookup(testIP), CodeUnavailable)

	// A database without a record for the address is an explainable failure too.
	empty := NewGeoCountryProvider(GeoCountryOptions{
		Lookup: fakeCountry{code: ""}, Now: func() time.Time { return testNow },
	})
	expectCode(t, empty.Lookup(testIP), CodeUnavailable)
}

func TestMMDBProviderWithoutDatabaseIsExplainable(t *testing.T) {
	provider := NewMMDBProvider(MMDBOptions{
		ID: "dbip_lite", Name: "DB-IP Lite", Profile: DBIPProfile,
		CityPath: "testdata/missing-city.mmdb", ASNPath: "testdata/missing-asn.mmdb",
		Now: func() time.Time { return testNow },
	})
	result := provider.Lookup(testIP)
	expectCode(t, result, CodeUnavailable)
	if !strings.Contains(result.Err.Message, "DB-IP Lite") {
		t.Fatalf("message should name the database: %q", result.Err.Message)
	}
	expectCode(t, provider.Lookup(netip.MustParseAddr("10.0.0.1")), CodeUnsupported)
}

func TestDecodeTorRegistry(t *testing.T) {
	now := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	roles, published, err := DecodeTorRegistry(fixture(t, "tor/details.json"), now)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !published.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("published: %v", published)
	}
	if bits := roles[netip.MustParseAddr("8.8.8.8")]; bits&torExit == 0 || bits&torRelay == 0 {
		t.Fatalf("exit relay roles: %v", bits)
	}
	if bits := roles[netip.MustParseAddr("1.1.1.1")]; bits&torGuard == 0 || bits&torExit != 0 {
		t.Fatalf("guard relay roles: %v", bits)
	}
	if _, ok := roles[netip.MustParseAddr("9.9.9.9")]; ok {
		t.Fatal("a relay that was last seen years ago must be skipped")
	}
	// A registry published too long ago is rejected.
	if _, _, err := DecodeTorRegistry(fixture(t, "tor/details.json"), now.Add(24*time.Hour)); err == nil {
		t.Fatal("a stale registry must be rejected")
	}
}

func TestTorProviderRoles(t *testing.T) {
	registry := NewTorRegistry(TorRegistryOptions{Now: func() time.Time { return testNow }})
	published := testNow.Add(-time.Hour)
	registry.Seed(map[netip.Addr]uint8{testIP: torRelay | torExit}, published, testNow)

	provider := NewTorProvider(TorProviderOptions{Registry: registry, Now: func() time.Time { return testNow }})
	evidence := mustEvidence(t, provider.Lookup(testIP))
	if len(evidence.TorRoles) != 2 || evidence.TorRoles[0] != "exit" {
		t.Fatalf("roles: %v", evidence.TorRoles)
	}
	if evidence.Signals.Tor == nil || !*evidence.Signals.Tor {
		t.Fatal("tor signal missing")
	}
	if evidence.SourceUpdatedAt == nil {
		t.Fatal("source updated at missing")
	}
	// An address without a registry entry produces no evidence, not a clean bill.
	expectCode(t, provider.Lookup(netip.MustParseAddr("9.9.9.9")), CodeUnavailable)
	if status := registry.Status(); !status.Ready || status.Entries != 1 {
		t.Fatalf("status: %+v", status)
	}
}

// --- DNSBL -----------------------------------------------------------------

// startDNSServer serves the answers map on a loopback UDP socket. A name that
// is absent answers NXDOMAIN.
func startDNSServer(t *testing.T, answers map[string][]string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		msg := new(dns.Msg)
		msg.SetReply(req)
		name := strings.TrimSuffix(strings.ToLower(req.Question[0].Name), ".")
		records, ok := answers[name]
		if !ok {
			msg.Rcode = dns.RcodeNameError
		} else {
			for _, record := range records {
				msg.Answer = append(msg.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A:   net.ParseIP(record),
				})
			}
		}
		_ = w.WriteMsg(msg)
	})
	server := &dns.Server{PacketConn: pc, Handler: handler}
	go func() { _ = server.ActivateAndServe() }()
	t.Cleanup(func() { _ = server.Shutdown() })
	return pc.LocalAddr().String()
}

func localResolver(addr string) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "udp", addr)
		},
	}
}

func TestDNSBLListedAndNotListed(t *testing.T) {
	addr := startDNSServer(t, map[string][]string{
		"8.8.8.8.zen.spamhaus.org": {"127.0.0.4"},
		"8.8.8.8.bl.spamcop.net":   {"127.0.0.2"},
		"8.8.8.8.psbl.surriel.com": {},
	})
	provider := NewDNSBLProvider(DNSBLOptions{
		Resolver: localResolver(addr), Now: func() time.Time { return testNow },
	})
	evidence := mustEvidence(t, provider.Lookup(context.Background(), []netip.Addr{testIP})[testIP])
	if len(evidence.DNSBLListed) != 2 {
		t.Fatalf("listed zones: %v", evidence.DNSBLListed)
	}
	if len(evidence.DNSBLChecked) != 3 {
		t.Fatalf("checked zones: %v", evidence.DNSBLChecked)
	}
	if evidence.Profile != DNSBLProfile {
		t.Fatalf("profile: %s", evidence.Profile)
	}
}

func TestDNSBLRefusedIsNotAListing(t *testing.T) {
	addr := startDNSServer(t, map[string][]string{
		"8.8.8.8.zen.spamhaus.org": {"127.255.255.254"},
	})
	provider := NewDNSBLProvider(DNSBLOptions{
		Zones:    []string{"zen.spamhaus.org"},
		Resolver: localResolver(addr), Now: func() time.Time { return testNow },
	})
	result := provider.Lookup(context.Background(), []netip.Addr{testIP})[testIP]
	expectCode(t, result, CodeDNSBLRefused)
}

func TestDNSBLUnsupportedIPv6AndQueryFailure(t *testing.T) {
	provider := NewDNSBLProvider(DNSBLOptions{
		Zones:    []string{"zen.spamhaus.org"},
		Resolver: localResolver("127.0.0.1:1"), Now: func() time.Time { return testNow },
	})
	v6 := netip.MustParseAddr("2606:4700:4700::1111")
	expectCode(t, provider.Lookup(context.Background(), []netip.Addr{v6})[v6], CodeUnsupported)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	expectCode(t, provider.Lookup(ctx, []netip.Addr{testIP})[testIP], CodeUnavailable)
}

func TestResolverFromURL(t *testing.T) {
	if resolver, err := ResolverFromURL(""); err != nil || resolver != nil {
		t.Fatalf("empty resolver: %v %v", resolver, err)
	}
	resolver, err := ResolverFromURL("udp://127.0.0.1:5353")
	if err != nil || resolver == nil {
		t.Fatalf("udp resolver: %v %v", resolver, err)
	}
	if _, err := ResolverFromURL("https://1.1.1.1"); err == nil {
		t.Fatal("an unsupported scheme must be rejected")
	}
	if _, err := ResolverFromURL("udp://"); err == nil {
		t.Fatal("a resolver without a host must be rejected")
	}
}

// --- settings merge --------------------------------------------------------

func TestDefaultSettingMergesCredentials(t *testing.T) {
	env := func(name string) string {
		switch name {
		case "PRISM_QUALITY_API_KEY":
			return "env-key"
		default:
			return ""
		}
	}
	spec := NewProxyCheckProvider(ProxyCheckOptions{}).Spec()
	setting := DefaultSetting(spec, env)
	if !setting.HasKey() || setting.APIKey != "env-key" {
		t.Fatalf("key not picked up: %+v", setting)
	}
	if setting.DailyLimit != 900 {
		t.Fatalf("a keyed provider defaults to 900/day, got %d", setting.DailyLimit)
	}
	if !setting.Enabled || !setting.Runnable() {
		t.Fatal("proxycheck is enabled by default")
	}
	if setting.CredentialID() == "" {
		t.Fatal("credential fingerprint missing")
	}

	anonymous := DefaultSetting(spec, nil)
	if anonymous.HasKey() || anonymous.DailyLimit != 80 {
		t.Fatalf("anonymous limit: %d", anonymous.DailyLimit)
	}
	if anonymous.CredentialID() != "" {
		t.Fatal("an anonymous provider has no credential fingerprint")
	}
}

func TestMergeSettingRespectsPersistedRow(t *testing.T) {
	spec := NewProxyCheckProvider(ProxyCheckOptions{}).Spec()
	row := &model.IntelProviderSetting{
		ProviderID: "proxycheck", Enabled: true, APIKey: "state-key",
		DailyLimit: 1_500_000, QPS: 2, TTLNs: int64(12 * time.Hour),
		ConfigJSON: `{"zones":["zen.spamhaus.org"]}`, UpdatedAtNs: 42,
	}
	setting := MergeSetting(spec, row, nil)
	if setting.Source != "state" || setting.DailyLimit != 1_000_000 {
		t.Fatalf("hard cap not applied: %+v", setting)
	}
	if setting.QPS != 2 || setting.TTL != 12*time.Hour || setting.UpdatedAtNs != 42 {
		t.Fatalf("row values not applied: %+v", setting)
	}

	unlimited := MergeSetting(spec, &model.IntelProviderSetting{ProviderID: "proxycheck", Enabled: true, DailyLimit: -1}, nil)
	if unlimited.DailyLimit != 0 {
		t.Fatalf("a negative limit means unlimited, got %d", unlimited.DailyLimit)
	}

	disabled := MergeSetting(spec, &model.IntelProviderSetting{ProviderID: "proxycheck", Enabled: false, APIKey: "state-key"}, nil)
	if disabled.Runnable() {
		t.Fatal("a disabled provider must not run")
	}
}

func TestKeyDependentEnablement(t *testing.T) {
	spec := NewAbuseIPDBProvider(AbuseIPDBOptions{}).Spec()
	if setting := DefaultSetting(spec, nil); setting.Enabled || setting.Runnable() {
		t.Fatal("AbuseIPDB without a key must stay disabled")
	}
	withKey := DefaultSetting(spec, func(name string) string {
		if name == "PRISM_ABUSEIPDB_API_KEY" {
			return "abc"
		}
		return ""
	})
	if !withKey.Enabled || !withKey.Runnable() {
		t.Fatal("AbuseIPDB with a key must be enabled")
	}

	maxmind, _ := findSpec(t, "maxmind_geolite2")
	missing := DefaultSetting(maxmind, func(name string) string {
		if name == "PRISM_MAXMIND_ACCOUNT_ID" {
			return "12345"
		}
		return ""
	})
	if missing.HasKey() || missing.Runnable() {
		t.Fatal("MaxMind needs both account_id and license_key")
	}
	complete := DefaultSetting(maxmind, func(name string) string {
		switch name {
		case "PRISM_MAXMIND_ACCOUNT_ID":
			return "12345"
		case "PRISM_MAXMIND_LICENSE_KEY":
			return "secret"
		}
		return ""
	})
	if !complete.HasKey() || !complete.Runnable() {
		t.Fatal("MaxMind with both fields must be runnable")
	}
	if complete.CredentialID() == "" || strings.Contains(complete.CredentialID(), "secret") {
		t.Fatal("the credential fingerprint must be a hash")
	}
}

func findSpec(t *testing.T, id string) (Spec, *Registry) {
	t.Helper()
	registry := NewRegistry()
	RegisterBuiltins(registry, BuiltinConfig{Now: func() time.Time { return testNow }})
	spec, ok := registry.Spec(id)
	if !ok {
		t.Fatalf("provider %s is not registered", id)
	}
	return spec, registry
}

// --- registry --------------------------------------------------------------

type fakeStore struct {
	rows     []model.IntelProviderSetting
	upserted []model.IntelProviderSetting
}

func (f *fakeStore) ListIntelProviderSettings() ([]model.IntelProviderSetting, error) {
	return f.rows, nil
}

func (f *fakeStore) UpsertIntelProviderSetting(s model.IntelProviderSetting) error {
	f.upserted = append(f.upserted, s)
	return nil
}

func TestRegisterBuiltinsCatalog(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry, BuiltinConfig{Now: func() time.Time { return testNow }})
	want := []string{
		"abuseipdb", "dbip_lite", "dnsbl", "geo_country", "ip_api", "ipapi_is",
		"ipinfo_lite", "ippure", "ipqs", "maxmind_geolite2", "proxycheck", "torproject",
	}
	got := registry.IDs()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("registered providers:\n got %v\nwant %v", got, want)
	}
	for _, spec := range registry.Specs() {
		if spec.Website == "" || spec.Terms == "" || spec.Profile == "" {
			t.Fatalf("spec %s is incomplete: %+v", spec.ID, spec)
		}
		if spec.Kind == KindOnlineIP && spec.BatchSize <= 0 {
			t.Fatalf("spec %s has no batch size", spec.ID)
		}
	}
	// A duplicate definition is a programming error.
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate provider ids must panic")
		}
	}()
	RegisterBuiltins(registry, BuiltinConfig{})
}

func TestRegistryApplyRebuildsInstances(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry, BuiltinConfig{Now: func() time.Time { return testNow }})

	rows := []model.IntelProviderSetting{
		{ProviderID: "proxycheck", Enabled: true, APIKey: "key-1", DailyLimit: 80, QPS: 1},
		{ProviderID: "abuseipdb", Enabled: false, APIKey: ""},
		{ProviderID: "ipqs", Enabled: false, APIKey: ""},
		{ProviderID: "ipapi_is", Enabled: false},
		{ProviderID: "dnsbl", Enabled: true},
	}
	registry.Apply(ResolveSettings(registry.Specs(), rows, nil))

	if _, ok := registry.Online("proxycheck"); !ok {
		t.Fatal("an enabled provider must be instantiated")
	}
	if _, ok := registry.Online("abuseipdb"); ok {
		t.Fatal("a disabled provider must not be instantiated")
	}
	if _, ok := registry.Offline("geo_country"); !ok {
		t.Fatal("geo_country is enabled by default")
	}
	if _, ok := registry.ViaNode("ippure"); !ok {
		t.Fatal("ippure is enabled by default")
	}

	specs := registry.QueueSpecs()
	if len(specs) == 0 {
		t.Fatal("queue specs missing")
	}
	byID := map[string]QueueSpec{}
	for _, spec := range specs {
		byID[spec.ProviderID] = spec
	}
	if spec := byID["proxycheck"]; !spec.Enabled || spec.DailyLimit != 80 || spec.CredentialID == "" {
		t.Fatalf("proxycheck queue spec: %+v", spec)
	}
	if spec := byID["abuseipdb"]; spec.Enabled {
		t.Fatal("a disabled provider must not get queue workers")
	}
	// A key rotation changes the credential fingerprint so a pause is lifted.
	before := byID["proxycheck"].CredentialID
	rotated := ResolveSettings(registry.Specs(), []model.IntelProviderSetting{
		{ProviderID: "proxycheck", Enabled: true, APIKey: "key-2", DailyLimit: 80, QPS: 1},
	}, nil)
	registry.Apply(rotated)
	setting, _ := registry.Setting("proxycheck")
	if setting.CredentialID() == before {
		t.Fatal("a rotated key must change the credential fingerprint")
	}
}

func TestSeedSettingsWritesEnvironmentDefaultsOnce(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry, BuiltinConfig{Now: func() time.Time { return testNow }})

	env := func(name string) string {
		switch name {
		case "PRISM_QUALITY_API_KEY":
			return "legacy-key"
		case "PRISM_ABUSEIPDB_API_KEY":
			return "abuse-key"
		case "PRISM_QUALITY_QUEUE_SIZE":
			return "4096"
		default:
			return ""
		}
	}
	store := &fakeStore{}
	written, warnings, err := SeedSettings(store, registry.Specs(), env, testNow.UnixNano())
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if written != 2 {
		t.Fatalf("seeded rows: %d", written)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], DeprecatedQualityQueueSizeEnv) {
		t.Fatalf("deprecation warning missing: %v", warnings)
	}
	for _, row := range store.upserted {
		if row.ProviderID == "proxycheck" && (row.APIKey != "legacy-key" || row.DailyLimit != 900) {
			t.Fatalf("proxycheck row: %+v", row)
		}
		if row.ProviderID == "abuseipdb" && !row.Enabled {
			t.Fatalf("a keyed provider must be enabled when seeded from the environment: %+v", row)
		}
	}

	// A second run must not overwrite the user's stored settings.
	store.rows = store.upserted
	written, _, err = SeedSettings(store, registry.Specs(), env, testNow.UnixNano())
	if err != nil {
		t.Fatalf("reseed: %v", err)
	}
	if written != 0 {
		t.Fatalf("environment values must be written once, got %d", written)
	}
}

func TestSpecsRejectIncompleteDefinitions(t *testing.T) {
	registry := NewRegistry()
	defer func() {
		if recover() == nil {
			t.Fatal("a provider without a profile must panic")
		}
	}()
	registry.Define(Spec{ID: "broken", Name: "broken"}, func(Setting) any { return nil })
}
