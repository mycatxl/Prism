package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"prism/internal/keys"
	"prism/internal/node"
	"prism/internal/platform"
)

// platformRestrictedKey builds a key state that only allows the named platform
// IDs. The rest of the fields are left at values that make the state usable, so
// a failure can only come from the platform check.
func platformRestrictedKey(platformIDs ...string) *keys.KeyState {
	return &keys.KeyState{
		KeyID:      "key-1",
		UserID:     "user-1",
		Scope:      "proxy",
		Status:     "active",
		Platforms:  platformIDs,
		UserStatus: "active",
	}
}

// TestResolveRoutedOutbound_PlatformIsolation is the isolation assertion for
// P2: a key may only reach the platforms it names, and the check happens at the
// one place every entry point converges on.
func TestResolveRoutedOutbound_PlatformIsolation(t *testing.T) {
	cases := []struct {
		name         string
		platformName string
		key          *keys.KeyState
		wantErr      *ProxyError
	}{
		{
			name:         "key allowed on this platform",
			platformName: "plat",
			key:          platformRestrictedKey("plat-id"),
		},
		{
			name:         "key not allowed on this platform",
			platformName: "plat",
			key:          platformRestrictedKey("some-other-platform-id"),
			wantErr:      ErrPlatformForbidden,
		},
		{
			name:         "key with no restriction reaches every platform",
			platformName: "plat",
			key:          platformRestrictedKey(),
		},
		{
			name:         "one allowed platform among several",
			platformName: "plat",
			key:          platformRestrictedKey("a", "plat-id", "b"),
		},
		{
			// The transitional path: the global token imposes no restriction.
			name:         "global token is unrestricted",
			platformName: "plat",
			key:          nil,
		},
		{
			name:         "unknown platform still reports not found",
			platformName: "no-such-platform",
			key:          platformRestrictedKey("plat-id"),
			wantErr:      ErrPlatformNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newProxyE2EEnv(t)

			_, err := resolveRoutedOutbound(env.router, env.pool, tc.platformName, "acct", "example.com", tc.key)
			if err != tc.wantErr {
				t.Fatalf("resolveRoutedOutbound error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// The check must run before the route is handed out, not after: a forbidden
// platform must never reach the point where a node is dialled.
func TestResolveRoutedOutbound_RefusesBeforeRouting(t *testing.T) {
	env := newProxyE2EEnv(t)

	routed, err := resolveRoutedOutbound(
		env.router, env.pool, "plat", "acct", "example.com",
		platformRestrictedKey("some-other-platform-id"),
	)
	if err != ErrPlatformForbidden {
		t.Fatalf("error = %v, want ErrPlatformForbidden", err)
	}
	if routed.Route.NodeHash != (node.Hash{}) || routed.Outbound != nil {
		t.Fatal("a refused request must not carry a route or an outbound")
	}
}

// A platform the key does allow must still route normally: the check adds a
// restriction, it does not break the happy path.
func TestResolveRoutedOutbound_AllowedPlatformStillRoutes(t *testing.T) {
	env := newProxyE2EEnv(t)

	routed, err := resolveRoutedOutbound(
		env.router, env.pool, "plat", "acct", "example.com",
		platformRestrictedKey("plat-id"),
	)
	if err != nil {
		t.Fatalf("an allowed platform was refused: %v", err)
	}
	if routed.Route.PlatformID != "plat-id" {
		t.Fatalf("routed platform = %q, want plat-id", routed.Route.PlatformID)
	}
	if routed.Outbound == nil {
		t.Fatal("an allowed request must carry an outbound")
	}
}

// The error the data plane reports must be the dedicated 403, not a generic
// failure: the client's credential is valid, and the response should say so.
func TestErrPlatformForbidden_Shape(t *testing.T) {
	if ErrPlatformForbidden.HTTPCode != 403 {
		t.Fatalf("HTTPCode = %d, want 403", ErrPlatformForbidden.HTTPCode)
	}
	if ErrPlatformForbidden.PrismError != "PLATFORM_FORBIDDEN" {
		t.Fatalf("PrismError = %q, want PLATFORM_FORBIDDEN", ErrPlatformForbidden.PrismError)
	}
}

func TestBypassPathsStillEnforceRestrictedKeyPlatform(t *testing.T) {
	assertBypassPathsRejectKey(t, platformRestrictedKey("some-other-platform-id"))
}

func TestBypassPathsRejectIPTierRestrictions(t *testing.T) {
	restricted := platformRestrictedKey()
	restricted.Plan.IPTiers = []string{"residential:clean"}
	assertBypassPathsRejectKey(t, restricted)
}

func assertBypassPathsRejectKey(t *testing.T, restricted *keys.KeyState) {
	t.Helper()
	env := newProxyE2EEnv(t)

	t.Run("forward HTTP", func(t *testing.T) {
		fp := NewForwardProxy(ForwardProxyConfig{
			AccessKeys:       keyVerifierStub{credential: testCredential, state: restricted},
			Router:           env.router,
			Events:           NoOpEventEmitter{},
			ProxyBypassRules: []string{"bypass.test"},
		})
		req := httptest.NewRequest(http.MethodGet, "http://bypass.test/resource", nil)
		req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
		rec := httptest.NewRecorder()
		fp.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || rec.Header().Get("X-Prism-Error") != ErrPlatformForbidden.PrismError {
			t.Fatalf("status/header = %d/%q, want 403/%s", rec.Code, rec.Header().Get("X-Prism-Error"), ErrPlatformForbidden.PrismError)
		}
	})

	t.Run("reverse HTTP", func(t *testing.T) {
		rp := NewReverseProxy(ReverseProxyConfig{
			AccessKeys:       keyVerifierStub{credential: testCredential, state: restricted},
			Router:           env.router,
			Events:           NoOpEventEmitter{},
			ProxyBypassRules: []string{"bypass.test"},
		})
		req := httptest.NewRequest(http.MethodGet, "/"+testCredential+"/plat:acct/https/bypass.test/resource", nil)
		rec := httptest.NewRecorder()
		rp.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || rec.Header().Get("X-Prism-Error") != ErrPlatformForbidden.PrismError {
			t.Fatalf("status/header = %d/%q, want 403/%s", rec.Code, rec.Header().Get("X-Prism-Error"), ErrPlatformForbidden.PrismError)
		}
	})

	t.Run("CONNECT and SOCKS shared path", func(t *testing.T) {
		result := prepareConnectTunnel(
			context.Background(),
			tunnelDeps{router: env.router, bypass: NewTargetBypassMatcher([]string{"bypass.test"})},
			"plat",
			"acct",
			"bypass.test:443",
			restricted,
		)
		if result.proxyErr != ErrPlatformForbidden || result.session != nil {
			t.Fatalf("prepare result = %+v, want ErrPlatformForbidden without a session", result)
		}
	})
}

func TestRouterResolvePlatformID(t *testing.T) {
	env := newProxyE2EEnv(t)
	// An empty name means "the default platform", so that platform has to exist
	// for the case to mean anything. Production always has it; the shared
	// fixture does not register it, so it is added here rather than there.
	env.pool.RegisterPlatform(platform.NewPlatform(platform.DefaultPlatformID, platform.DefaultPlatformName, nil, nil))

	cases := []struct {
		name     string
		platName string
		want     string
	}{
		{"named platform", "plat", "plat-id"},
		{"empty name resolves the default platform", "", platform.DefaultPlatformID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := env.router.ResolvePlatformID(tc.platName)
			if err != nil {
				t.Fatalf("ResolvePlatformID: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ResolvePlatformID(%q) = %q, want %q", tc.platName, got, tc.want)
			}
		})
	}

	if _, err := env.router.ResolvePlatformID("no-such-platform"); err == nil {
		t.Fatal("an unknown platform name must be reported")
	}
}
