package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

type stubAccountApplication struct {
	registerResult model.Account
	registerErr    error
	registerCalls  int
	verifyResult   model.Account
	verifyErr      error
}

func (application *stubAccountApplication) Register(context.Context, service.RegisterInput) (model.Account, error) {
	application.registerCalls++
	return application.registerResult, application.registerErr
}
func (application *stubAccountApplication) VerifyEmail(context.Context, string) (model.Account, error) {
	return application.verifyResult, application.verifyErr
}
func (application *stubAccountApplication) ResendVerification(context.Context, string) error {
	return nil
}
func (application *stubAccountApplication) RequestPasswordReset(context.Context, string) error {
	return nil
}
func (application *stubAccountApplication) ResetPassword(context.Context, string, string) (model.Account, error) {
	return model.Account{}, nil
}
func (application *stubAccountApplication) UpdateDisplayName(context.Context, uuid.UUID, string) (model.Account, error) {
	return model.Account{}, nil
}
func (application *stubAccountApplication) UpdateEmail(context.Context, model.Account, string) (model.Account, error) {
	return model.Account{}, nil
}

type stubSessionApplication struct {
	loginResult         service.SessionResult
	loginErr            error
	verifiedResult      service.SessionResult
	verifiedErr         error
	verifiedAccount     model.Account
	verifiedUserAgent   string
	authenticated       model.AuthenticatedSession
	authenticateErr     error
	changeResult        service.SessionResult
	changeErr           error
	lastLogin           service.LoginInput
	logoutAuthenticated model.AuthenticatedSession
}

func (application *stubSessionApplication) Login(_ context.Context, input service.LoginInput) (service.SessionResult, error) {
	application.lastLogin = input
	return application.loginResult, application.loginErr
}
func (application *stubSessionApplication) CreateVerifiedSession(_ context.Context, account model.Account, userAgent string) (service.SessionResult, error) {
	application.verifiedAccount = account
	application.verifiedUserAgent = userAgent
	return application.verifiedResult, application.verifiedErr
}
func (application *stubSessionApplication) Authenticate(context.Context, string) (model.AuthenticatedSession, error) {
	return application.authenticated, application.authenticateErr
}
func (application *stubSessionApplication) Logout(_ context.Context, authenticated model.AuthenticatedSession) error {
	application.logoutAuthenticated = authenticated
	return nil
}
func (application *stubSessionApplication) ChangePassword(context.Context, service.ChangePasswordInput) (service.SessionResult, error) {
	return application.changeResult, application.changeErr
}
func (application *stubSessionApplication) VerifyPassword(context.Context, uuid.UUID, string) error {
	return nil
}

func TestPostgresRouterLoginSetsHardenedHTTPSCookieAndRequestID(t *testing.T) {
	expires := time.Now().UTC().Add(24 * time.Hour)
	sessions := &stubSessionApplication{loginResult: service.SessionResult{
		Account: model.Account{ID: uuid.New(), Email: "user@example.com", Status: model.AccountActive},
		Session: model.Session{ExpiresAt: expires}, Token: "raw-session-token",
	}}
	handler := newTestRouter(t, &stubAccountApplication{}, sessions, nil)
	request := httptest.NewRequest(http.MethodPost, "https://dayorder.example/api/v1/auth/login", bytes.NewBufferString(`{"email":"user@example.com","password":"valid-password"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-For", "203.0.113.5")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if _, err := uuid.Parse(response.Header().Get("X-Request-ID")); err != nil {
		t.Fatalf("request ID = %q", response.Header().Get("X-Request-ID"))
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookies = %#v", cookies)
	}
	if sessions.lastLogin.IP != "203.0.113.5" {
		t.Fatalf("login client IP = %q", sessions.lastLogin.IP)
	}
}

func TestPostgresRouterEmailVerificationCreatesSession(t *testing.T) {
	account := model.Account{ID: uuid.New(), Email: "user@example.com", Status: model.AccountActive}
	expires := time.Now().UTC().Add(24 * time.Hour)
	sessions := &stubSessionApplication{verifiedResult: service.SessionResult{
		Account: account, Session: model.Session{ExpiresAt: expires}, Token: "verified-session-token",
	}}
	handler := newTestRouter(t, &stubAccountApplication{verifyResult: account}, sessions, nil)
	request := httptest.NewRequest(http.MethodPost, "https://dayorder.example/api/v1/auth/verify-email", bytes.NewBufferString(`{"token":"verification-token"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "DayOrder Test Browser")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if sessions.verifiedAccount.ID != account.ID || sessions.verifiedUserAgent != "DayOrder Test Browser" {
		t.Fatalf("verified session input = account %s user-agent %q", sessions.verifiedAccount.ID, sessions.verifiedUserAgent)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value != "verified-session-token" || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatalf("session cookies = %#v", cookies)
	}
	var body struct {
		User      model.Account `json:"user"`
		ExpiresAt time.Time     `json:"expiresAt"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.User.ID != account.ID || !body.ExpiresAt.Equal(expires) {
		t.Fatalf("verification response = %#v", body)
	}
}

func TestPostgresRouterRejectsOriginBeforeApplicationAndReturnsErrorEnvelope(t *testing.T) {
	accounts := &stubAccountApplication{}
	handler := newTestRouter(t, accounts, &stubSessionApplication{}, nil)
	request := httptest.NewRequest(http.MethodPost, "https://dayorder.example/api/v1/auth/register", bytes.NewBufferString(`{}`))
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || accounts.registerCalls != 0 {
		t.Fatalf("status=%d registerCalls=%d", response.Code, accounts.registerCalls)
	}
	var envelope apiErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "ORIGIN_NOT_ALLOWED" || envelope.Error.RequestID == "" {
		t.Fatalf("error envelope = %#v", envelope)
	}
}

func TestPostgresRouterMapsServiceErrorsWithoutLeakingInternals(t *testing.T) {
	accounts := &stubAccountApplication{registerErr: service.ErrEmailInUse}
	handler := newTestRouter(t, accounts, &stubSessionApplication{}, nil)
	request := httptest.NewRequest(http.MethodPost, "http://dayorder.example/api/v1/auth/register", bytes.NewBufferString(`{"email":"used@example.com","displayName":"User","password":"valid-password"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var envelope apiErrorEnvelope
	_ = json.Unmarshal(response.Body.Bytes(), &envelope)
	if envelope.Error.Code != "EMAIL_ALREADY_REGISTERED" || envelope.Error.Retryable {
		t.Fatalf("error envelope = %#v", envelope)
	}
}

func TestLivenessDoesNotDependOnDatabaseButReadinessDoes(t *testing.T) {
	handler := newTestRouter(t, &stubAccountApplication{}, &stubSessionApplication{}, func(context.Context) error {
		return errors.New("database unavailable")
	})
	for _, test := range []struct {
		path string
		want int
	}{{path: "/health/live", want: http.StatusOK}, {path: "/health/ready", want: http.StatusServiceUnavailable}} {
		request := httptest.NewRequest(http.MethodGet, "http://dayorder.example"+test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Errorf("%s status = %d, want %d", test.path, response.Code, test.want)
		}
	}
}

func newTestRouter(t testing.TB, accounts AccountApplication, sessions SessionApplication, ready func(context.Context) error) http.Handler {
	t.Helper()
	handler, err := NewRouter(RouterOptions{
		Accounts: accounts, Sessions: sessions, Ready: ready,
		AllowedOrigins: []string{"https://dayorder.example"},
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}
