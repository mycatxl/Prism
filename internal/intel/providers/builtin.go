package providers

import (
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// Geo database file names inside $PRISM_CACHE_DIR/geo (WP09 §3).
const (
	DBIPCityFile      = "dbip-city-lite.mmdb"
	DBIPASNFile       = "dbip-asn-lite.mmdb"
	MaxMindCityFile   = "GeoLite2-City.mmdb"
	MaxMindASNFile    = "GeoLite2-ASN.mmdb"
	IPInfoLiteDBFile  = "ipinfo_lite.mmdb"
	defaultHTTPTimout = 10 * time.Second
)

// BuiltinConfig supplies the runtime dependencies of the built-in data sources.
// Every field is optional: a missing dependency turns that provider into an
// explainable failure instead of a nil dereference.
type BuiltinConfig struct {
	// GeoDir is the directory holding the offline databases.
	GeoDir string
	// Country is the region-filter database (Rescan's country.mmdb).
	Country CountryLookup
	// Tor is the shared Onionoo relay registry.
	Tor *TorRegistry
	// Client is the strict HTTP client of the online providers (never uses
	// HTTP_PROXY). A nil client builds one per provider.
	Client *http.Client
	// Resolver is used by the DNSBL provider; nil uses the system resolver.
	Resolver *net.Resolver
	// Timeout bounds one online request.
	Timeout time.Duration
	// Now is the injected clock.
	Now func() time.Time
}

func (c BuiltinConfig) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultHTTPTimout
}

// urlOverride returns the config_json "url" value of a provider, so a
// deployment can point a data source at a mirror or a test server.
func urlOverride(setting Setting) string {
	return setting.ConfigString("url")
}

func mmdbPath(dir, name string) string {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(name) == "" {
		return ""
	}
	return filepath.Join(dir, name)
}

// RegisterBuiltins defines every data source of WP09 §3 on a registry.
func RegisterBuiltins(reg *Registry, cfg BuiltinConfig) {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	client := cfg.Client

	// --- offline ---------------------------------------------------------

	reg.Define(NewGeoCountryProvider(GeoCountryOptions{}).Spec(), func(setting Setting) any {
		return NewGeoCountryProvider(GeoCountryOptions{
			Lookup: cfg.Country,
			TTL:    setting.EffectiveTTL(),
			Now:    now,
		})
	})

	reg.Define(NewTorProvider(TorProviderOptions{}).Spec(), func(setting Setting) any {
		return NewTorProvider(TorProviderOptions{
			Registry: cfg.Tor,
			TTL:      setting.EffectiveTTL(),
			Now:      now,
		})
	})

	reg.Define(Spec{
		ID: "dbip_lite", Name: "DB-IP Lite", Website: "https://db-ip.com/db/lite/",
		Terms: "DB-IP Lite is published under CC BY 4.0, redistributed for free and updated " +
			"monthly; attribution to DB-IP is required in the user interface.",
		Kind: KindOffline, Profile: DBIPProfile,
		RequiresKey: false, DefaultEnabled: true,
		BatchSize: 1, DefaultTTL: 30 * 24 * time.Hour, SupportsIPv6: true,
	}, func(setting Setting) any {
		return NewMMDBProvider(MMDBOptions{
			ID: "dbip_lite", Name: "DB-IP Lite", Website: "https://db-ip.com/db/lite/",
			Terms:   "DB-IP Lite is published under CC BY 4.0 and updated monthly.",
			Profile: DBIPProfile, TTL: setting.EffectiveTTL(),
			CityPath: mmdbPath(cfg.GeoDir, DBIPCityFile),
			ASNPath:  mmdbPath(cfg.GeoDir, DBIPASNFile),
			Now:      now,
		})
	})

	reg.Define(Spec{
		ID: "maxmind_geolite2", Name: "MaxMind GeoLite2", Website: "https://www.maxmind.com/en/geolite2/eula",
		Terms: "GeoLite2 requires a MaxMind account (account_id + license_key) and is licensed " +
			"under the GeoLite2 EULA: attribution and a 30-day database refresh are mandatory. " +
			"Prism stores the credentials in state.db and verifies the download by opening it.",
		Kind: KindOffline, Profile: MaxMindProfile,
		RequiresKey: true, DefaultEnabled: false,
		BatchSize: 1, DefaultTTL: 30 * 24 * time.Hour, SupportsIPv6: true,
		CredentialFields: []string{"account_id", "license_key"},
	}, func(setting Setting) any {
		return NewMMDBProvider(MMDBOptions{
			ID: "maxmind_geolite2", Name: "MaxMind GeoLite2",
			Website: "https://www.maxmind.com/en/geolite2/eula",
			Terms:   "GeoLite2 is licensed under the MaxMind GeoLite2 EULA.",
			Profile: MaxMindProfile, TTL: setting.EffectiveTTL(),
			CityPath: mmdbPath(cfg.GeoDir, MaxMindCityFile),
			ASNPath:  mmdbPath(cfg.GeoDir, MaxMindASNFile),
			Now:      now,
		})
	})

	reg.Define(Spec{
		ID: "ipinfo_lite", Name: "IPinfo Lite", Website: "https://ipinfo.io/lite",
		Terms: "IPinfo Lite requires a token, is free for non-commercial use and is refreshed " +
			"weekly. See https://ipinfo.io/developers for the current terms.",
		Kind: KindOffline, Profile: IPInfoProfile,
		RequiresKey: true, DefaultEnabled: false,
		BatchSize: 1, DefaultTTL: 30 * 24 * time.Hour, SupportsIPv6: true,
		CredentialFields: []string{"token"},
	}, func(setting Setting) any {
		return NewMMDBProvider(MMDBOptions{
			ID: "ipinfo_lite", Name: "IPinfo Lite", Website: "https://ipinfo.io/lite",
			Terms:   "IPinfo Lite requires a token and is free for non-commercial use.",
			Profile: IPInfoProfile, TTL: setting.EffectiveTTL(),
			CityPath: mmdbPath(cfg.GeoDir, IPInfoLiteDBFile),
			ASNPath:  mmdbPath(cfg.GeoDir, IPInfoLiteDBFile),
			Now:      now,
		})
	})

	// --- online-ip -------------------------------------------------------

	reg.Define(NewProxyCheckProvider(ProxyCheckOptions{}).Spec(), func(setting Setting) any {
		providerClient := client
		return NewProxyCheckProvider(ProxyCheckOptions{
			Key: setting.APIKey, TTL: setting.EffectiveTTL(), Timeout: cfg.timeout(),
			BaseURL: urlOverride(setting), Client: providerClient, Now: now,
		})
	})

	reg.Define(NewAbuseIPDBProvider(AbuseIPDBOptions{}).Spec(), func(setting Setting) any {
		return NewAbuseIPDBProvider(AbuseIPDBOptions{
			Key: setting.APIKey, TTL: setting.EffectiveTTL(), Timeout: cfg.timeout(),
			BaseURL: urlOverride(setting), Client: client, Now: now,
		})
	})

	reg.Define(NewIPQSProvider(IPQSOptions{}).Spec(), func(setting Setting) any {
		return NewIPQSProvider(IPQSOptions{
			Key: setting.APIKey, TTL: setting.EffectiveTTL(), Timeout: cfg.timeout(),
			BaseURL: urlOverride(setting), Client: client, Now: now,
		})
	})

	reg.Define(NewIPAPIISProvider(IPAPIISOptions{}).Spec(), func(setting Setting) any {
		return NewIPAPIISProvider(IPAPIISOptions{
			Key: setting.APIKey, TTL: setting.EffectiveTTL(), Timeout: cfg.timeout(),
			BaseURL: urlOverride(setting), Client: client, Now: now,
		})
	})

	reg.Define(NewDNSBLProvider(DNSBLOptions{}).Spec(), func(setting Setting) any {
		resolver := cfg.Resolver
		if configured := setting.ConfigString("resolver"); configured != "" {
			if custom, err := ResolverFromURL(configured); err == nil && custom != nil {
				resolver = custom
			}
		}
		zoneQPS := setting.QPS
		return NewDNSBLProvider(DNSBLOptions{
			Zones: setting.ConfigStrings("zones"), Resolver: resolver,
			TTL: setting.EffectiveTTL(), ZoneQPS: zoneQPS, Now: now,
		})
	})

	// --- via-node --------------------------------------------------------

	reg.Define(NewIPPureProvider(IPPureOptions{}).Spec(), func(setting Setting) any {
		return NewIPPureProvider(IPPureOptions{
			URL: urlOverride(setting), TTL: setting.EffectiveTTL(),
			Timeout: cfg.timeout(), Now: now,
		})
	})

	reg.Define(NewIPAPIProvider(IPAPIOptions{}).Spec(), func(setting Setting) any {
		return NewIPAPIProvider(IPAPIOptions{
			URL: urlOverride(setting), TTL: setting.EffectiveTTL(),
			Timeout: cfg.timeout(), Now: now,
		})
	})
}
