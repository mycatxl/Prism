package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"prism/internal/auth"
	"prism/internal/model"
	"prism/internal/service"
)

func TestLoginAccountLockHasIdenticalPublicResponse(t *testing.T) {
	var baseline *httptest.ResponseRecorder
	for _, err := range []error{auth.ErrInvalidCredentials, errors.Join(auth.ErrAccountLocked, auth.ErrTooManyAttempts)} {
		handler := newAuthHandlers(&fakeSessions{loginErr: err}, &fakeAccounts{}).HandleLogin()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"candidate","password":"wrong-password"}`))
		req.Host = "127.0.0.1"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if baseline == nil {
			baseline = rec
			continue
		}
		if rec.Code != baseline.Code || rec.Body.String() != baseline.Body.String() || !reflect.DeepEqual(rec.Header(), baseline.Header()) {
			t.Fatal("locked and unknown accounts have distinguishable public responses")
		}
	}
}

func TestPasswordRechecksShareAnAccountFailureBudget(t *testing.T) {
	accounts := &fakeAccounts{changePwErr: service.ErrCurrentPasswordWrong, verifyPwErr: service.ErrCurrentPasswordWrong}
	handlers := newAuthHandlers(&fakeSessions{}, accounts)
	handlers.FailureLimiter = NewAuthFailureLimiter(2, time.Minute, time.Minute, nil)
	for i, handler := range []http.Handler{handlers.HandleChangePassword(), handlers.HandleDisableTOTP(), handlers.HandleChangePassword(), handlers.HandleDisableTOTP()} {
		body := `{"current_password":"wrong"}`
		if i%2 == 0 {
			body = `{"current_password":"wrong","new_password":"new-long-password"}`
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", strings.NewReader(body))
		req = req.WithContext(contextWithActor(req.Context(), Actor{UserID: "u-admin", Role: model.UserRoleAdmin, ViaSession: true}))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		want := http.StatusForbidden
		if i >= 2 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("attempt %d = %d, want %d: %s", i, rec.Code, want, rec.Body.String())
		}
	}
}

func TestAuthLoggingDoesNotExposeInternalErrors(t *testing.T) {
	const secret = "SELECT password_hash FROM users /private/database.db pk_secret"
	handlers := newAuthHandlers(&fakeSessions{loginErr: errors.New(secret)}, &fakeAccounts{})
	var log strings.Builder
	handlers.Logf = func(format string, args ...any) { _, _ = fmt.Fprintf(&log, format, args...) }
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"alice","password":"wrong-password"}`))
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	handlers.HandleLogin().ServeHTTP(rec, req)
	if strings.Contains(log.String(), secret) || strings.Contains(rec.Body.String(), secret) {
		t.Fatal("internal error exposed to client")
	}
}
