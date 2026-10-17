package api

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"prism/internal/auth"
	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/service"
)

// The role gate is what keeps the two management surfaces apart: the routes a
// signed-in user owns (/api/v1/auth and /api/v1/me) and everything else, which
// belongs to the administrator. The tests below drive it through the identity
// middleware, because the gate only means anything behind one.

// userRoleIdentity authenticates a signed-in account that is not an
// administrator, and gives it an access key as well.
func userRoleIdentity() *Identity {
	user := &model.User{
		ID:       "u-reader",
		Username: "reader",
		Role:     model.UserRoleUser,
		Status:   model.UserStatusActive,
	}
	return &Identity{
		Sessions: &fakeSessions{
			user:    user,
			session: &model.Session{IDHash: "user-session", UserID: user.ID},
		},
		AdminKeys: fakeAdminKeys{
			credential: "pk_test_user",
			identity:   AdminKeyIdentity{KeyID: "user-key", UserID: user.ID, Role: model.UserRoleUser},
		},
	}
}

func TestIsUserReachablePath(t *testing.T) {
	cases := map[string]bool{
		"/api/v1/auth":         true,
		"/api/v1/auth/login":   true,
		"/api/v1/auth/session": true,
		"/api/v1/me":           true,
		"/api/v1/me/keys":      true,
		"/api/v1/me/usage":     true,
		// Close to a reachable path, but not under it: a prefix match on the
		// raw string would let these through.
		"/api/v1/authz":       false,
		"/api/v1/meta":        false,
		"/api/v1/metoo":       false,
		"/api/v1/me2":         false,
		"/api/v1/platforms":   false,
		"/api/v1/admin/plans": false,
		"/api/v1":             false,
		"":                    false,
	}
	for path, want := range cases {
		if got := isUserReachablePath(path); got != want {
			t.Errorf("isUserReachablePath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestRequireAdmin(t *testing.T) {
	csrfToken, err := auth.NewCSRFToken()
	if err != nil {
		t.Fatalf("NewCSRFToken: %v", err)
	}
	admin := testIdentityWithLimiter(testAdminCredential, nil)

	cases := []struct {
		name string
		// identity resolves the request; nil means "use a user-role identity".
		identity *Identity
		// session is the cookie value, key an automation credential; either may
		// be empty to leave that credential out.
		session string
		key     string
		method  string
		path    string
		// csrf sends a matching double-submit pair for a write.
		csrf        bool
		wantStatus  int
		wantCode    string
		wantReached bool
	}{
		{
			// A refusal must be indistinguishable from a path that does not
			// exist, or the management surface becomes something to enumerate.
			name:       "a user is refused on the control plane as not-found",
			session:    "user-session",
			method:     http.MethodGet,
			path:       "/api/v1/platforms",
			wantStatus: http.StatusNotFound,
			wantCode:   "NOT_FOUND",
		},
		{
			name:        "a user reaches their own panel",
			session:     "user-session",
			method:      http.MethodGet,
			path:        "/api/v1/me/keys",
			wantStatus:  http.StatusOK,
			wantReached: true,
		},
		{
			name:        "a user reaches the authentication routes",
			session:     "user-session",
			method:      http.MethodGet,
			path:        "/api/v1/auth/session",
			wantStatus:  http.StatusOK,
			wantReached: true,
		},
		{
			// The prefix rule must not leak onto a neighbour that merely starts
			// with the same letters.
			name:       "a user is refused where the path only shares a prefix",
			session:    "user-session",
			method:     http.MethodGet,
			path:       "/api/v1/metoo",
			wantStatus: http.StatusNotFound,
			wantCode:   "NOT_FOUND",
		},
		{
			// The CSRF pair matches, so the refusal below is the role and not
			// the missing token.
			name:       "a write from a user is refused on the control plane",
			session:    "user-session",
			method:     http.MethodPost,
			path:       "/api/v1/admin/plans",
			csrf:       true,
			wantStatus: http.StatusNotFound,
			wantCode:   "NOT_FOUND",
		},
		{
			// A key inherits the role of whoever holds it: minting one for a
			// user must not smuggle that user past the gate.
			name:       "a key held by a user does not open the control plane",
			key:        "pk_test_user",
			method:     http.MethodGet,
			path:       "/api/v1/platforms",
			wantStatus: http.StatusNotFound,
			wantCode:   "NOT_FOUND",
		},
		{
			name:       "no credential is refused as unauthenticated",
			method:     http.MethodGet,
			path:       "/api/v1/me/keys",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "UNAUTHORIZED",
		},
		{
			name:        "an administrator reaches the control plane",
			identity:    admin,
			session:     "admin-session",
			method:      http.MethodGet,
			path:        "/api/v1/platforms",
			wantStatus:  http.StatusOK,
			wantReached: true,
		},
		{
			name:        "an administrator also reaches the user panel",
			identity:    admin,
			session:     "admin-session",
			method:      http.MethodGet,
			path:        "/api/v1/me/keys",
			wantStatus:  http.StatusOK,
			wantReached: true,
		},
		{
			// Sign-in and sign-up are how a caller obtains a credential, so
			// they arrive without an actor on purpose. The role gate must not
			// be the middleware that refuses them: answering 401 to the request
			// that creates the session would lock every console out.
			name:        "an unauthenticated caller reaches the sign-in route",
			method:      http.MethodPost,
			path:        "/api/v1/auth/login",
			wantStatus:  http.StatusOK,
			wantReached: true,
		},
		{
			name:        "an unauthenticated caller reaches the sign-up route",
			method:      http.MethodPost,
			path:        "/api/v1/auth/register",
			wantStatus:  http.StatusOK,
			wantReached: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})

			identity := tc.identity
			if identity == nil {
				identity = userRoleIdentity()
			}

			req := sessionRequest(tc.method, tc.path, tc.session)
			if tc.key != "" {
				req.Header.Set("Authorization", "Bearer "+tc.key)
			}
			if tc.csrf {
				req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrfToken})
				req.Header.Set(auth.CSRFHeaderName, csrfToken)
			}

			rec := httptest.NewRecorder()
			identity.Middleware(requireAdmin(next)).ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantCode != "" {
				assertErrorCode(t, rec, tc.wantCode)
			}
			if reached != tc.wantReached {
				t.Errorf("handler reached = %v, want %v", reached, tc.wantReached)
			}
		})
	}
}

// TestRequireAdminRefusalMatchesNotFound pins the property the refusal exists
// for: a tenant who reaches a management route gets the same answer as a tenant
// who invented the path. Any difference - status, code, body - tells a prober
// that the route is real, which is exactly what the gate must not leak.
func TestRequireAdminRefusalMatchesNotFound(t *testing.T) {
	identity := userRoleIdentity()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("handler reached for a path a user may not use")
	})

	refused := httptest.NewRecorder()
	identity.Middleware(requireAdmin(next)).ServeHTTP(
		refused, sessionRequest(http.MethodGet, "/api/v1/platforms", "user-session"),
	)

	// The same shape a caller gets for a path no mux ever registered.
	absent := httptest.NewRecorder()
	WriteError(absent, http.StatusNotFound, "NOT_FOUND", "not found")

	if refused.Code != http.StatusNotFound {
		t.Fatalf("refusal status = %d, want %d", refused.Code, http.StatusNotFound)
	}
	if refused.Body.String() != absent.Body.String() {
		t.Fatalf("refusal body = %q, want the not-found body %q", refused.Body.String(), absent.Body.String())
	}
}

// TestNoRouteLeaksThroughItsMethod pins the other half of the same property.
//
// A path that exists under one method answers 405 - with an Allow header
// naming the methods it serves - to a request using another. That confirms the
// route as surely as a 403 would. The catch-all route removes it: every
// request no route matched is the one not-found shape, whatever its method.
func TestNoRouteLeaksThroughItsMethod(t *testing.T) {
	// An administrator holding a bearer credential is the case where the role
	// gate lets the request through, so the mux itself decides - and the mux
	// is what used to answer 405 with an Allow header.
	srv := probeServerWithIdentity(testIdentityWithLimiter(testAdminCredential, nil))

	want := httptest.NewRecorder()
	WriteError(want, http.StatusNotFound, "NOT_FOUND", "not found")

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, bearerRequest(method, "/api/v1/system/info"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s on a GET-only route = %d, want %d", method, rec.Code, http.StatusNotFound)
			continue
		}
		if rec.Body.String() != want.Body.String() {
			t.Errorf("%s body = %q, want %q", method, rec.Body.String(), want.Body.String())
		}
		if allow := rec.Header().Get("Allow"); allow != "" {
			t.Errorf("%s leaked Allow: %q", method, allow)
		}
	}

	// The route itself still answers for the method it serves.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, bearerRequest(http.MethodGet, "/api/v1/system/info"))
	if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
		t.Errorf("GET on a GET route = %d, want the route to answer", rec.Code)
	}
}

// bearerRequest carries the administrator's access key, which is what an
// automation uses: no session cookie, so no CSRF token to match.
func bearerRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+testAdminCredential)
	return req
}

// probeServerWithIdentity builds the real server around a given resolver, so
// both the catch-all route and the role gate are in the chain.
func probeServerWithIdentity(identity *Identity) *Server {
	runtimeCfg := &atomic.Pointer[config.RuntimeConfig]{}
	runtimeCfg.Store(config.NewDefaultRuntimeConfig())
	envCfg := &config.EnvConfig{APIMaxBodyBytes: 1 << 20}
	deps := &AuthDeps{
		Identity:  identity,
		Handlers:  &AuthHandlers{Sessions: &fakeSessions{}},
		Issuer:    "Prism",
		Blocklist: NewLoginBlocklist(nil),
	}
	return NewServer(0, service.SystemInfo{Version: "test"}, runtimeCfg, envCfg, nil, 1<<20, nil, nil, nil, deps)
}
