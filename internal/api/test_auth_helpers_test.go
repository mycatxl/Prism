package api

import (
	"net/http"

	"prism/internal/model"
)

const testAdminCredential = "pk_test_admin"

func testAuthDepsForServer() *AuthDeps {
	return &AuthDeps{
		Identity:  testIdentityWithLimiter(testAdminCredential, nil),
		Handlers:  &AuthHandlers{Sessions: &fakeSessions{}},
		Issuer:    "Prism",
		Blocklist: NewLoginBlocklist(nil),
	}
}

func testIdentityWithLimiter(credential string, limiter *AuthFailureLimiter) *Identity {
	return &Identity{
		Sessions: &fakeSessions{
			user:    adminUser(),
			session: &model.Session{IDHash: "test-session", UserID: "u-admin"},
		},
		AdminKeys: fakeAdminKeys{
			credential: credential,
			identity:   AdminKeyIdentity{KeyID: "test-key", UserID: "u-admin", Role: model.UserRoleAdmin},
		},
		FailureLimiter: limiter,
	}
}

func testIdentityMiddleware(credential string, limiter *AuthFailureLimiter, next http.Handler) http.Handler {
	return testIdentityWithLimiter(credential, limiter).Middleware(next)
}

func testIdentityWithHeader(credential string, limiter *AuthFailureLimiter, next http.Handler) http.Handler {
	identity := testIdentityWithLimiter(credential, limiter)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := r.Clone(r.Context())
		if req.Header.Get("Authorization") == "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		identity.Middleware(next).ServeHTTP(w, req)
	})
}
