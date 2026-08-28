package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"dayorder.local/api/internal/store"
)

func newTestAPI(t *testing.T) http.Handler {
	t.Helper()
	storage, err := store.Open(filepath.Join(t.TempDir(), "dayorder.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	return New(storage, Options{AllowedOrigins: []string{"http://127.0.0.1:5173"}, Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))})
}

func call(t *testing.T, handler http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func register(t *testing.T, handler http.Handler, email string, marker string) (*http.Cookie, map[string]any) {
	t.Helper()
	payload := `{"displayName":"测试用户","email":"` + email + `","password":"correct-horse-123","initialData":{"version":1,"goals":[],"tasks":[],"events":[],"records":[],"notes":[{"marker":"` + marker + `"}],"reviews":[],"agentRuns":[],"audit":[],"settings":{}}}`
	response := call(t, handler, http.MethodPost, "/api/v1/auth/register", payload, nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("register %s: %d %s", email, response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected session cookie, got %d", len(cookies))
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return cookies[0], body
}

func TestRegistrationSessionCookieAndMigration(t *testing.T) {
	handler := newTestAPI(t)
	unauthenticated := call(t, handler, http.MethodGet, "/api/v1/state", "", nil)
	if unauthenticated.Code != http.StatusUnauthorized || !strings.Contains(unauthenticated.Body.String(), "AUTH_REQUIRED") {
		t.Fatalf("anonymous state must be rejected: %d %s", unauthenticated.Code, unauthenticated.Body.String())
	}

	cookie, body := register(t, handler, "Person@Example.com", "migrated")
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge <= 0 || cookie.Path != "/" {
		t.Fatalf("unsafe cookie: %#v", cookie)
	}
	user := body["user"].(map[string]any)
	if user["email"] != "person@example.com" {
		t.Fatalf("email was not normalized: %#v", user)
	}

	state := call(t, handler, http.MethodGet, "/api/v1/state", "", cookie)
	if state.Code != http.StatusOK || !strings.Contains(state.Body.String(), "migrated") {
		t.Fatalf("migrated state missing: %d %s", state.Code, state.Body.String())
	}
	duplicate := call(t, handler, http.MethodPost, "/api/v1/auth/register", `{"displayName":"重复","email":"PERSON@example.com","password":"correct-horse-123"}`, nil)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), "EMAIL_ALREADY_REGISTERED") {
		t.Fatalf("duplicate email response: %d %s", duplicate.Code, duplicate.Body.String())
	}
}

func TestStateIsolationAndRevision(t *testing.T) {
	handler := newTestAPI(t)
	one, _ := register(t, handler, "one@example.com", "one-only")
	two, _ := register(t, handler, "two@example.com", "two-only")

	oneState := call(t, handler, http.MethodGet, "/api/v1/state", "", one)
	twoState := call(t, handler, http.MethodGet, "/api/v1/state", "", two)
	if !strings.Contains(oneState.Body.String(), "one-only") || strings.Contains(oneState.Body.String(), "two-only") {
		t.Fatalf("user one isolation failed: %s", oneState.Body.String())
	}
	if !strings.Contains(twoState.Body.String(), "two-only") || strings.Contains(twoState.Body.String(), "one-only") {
		t.Fatalf("user two isolation failed: %s", twoState.Body.String())
	}

	updated := `{"expectedRevision":1,"data":{"version":1,"goals":[],"tasks":[],"events":[],"records":[],"notes":[],"reviews":[],"agentRuns":[],"audit":[],"settings":{}}}`
	response := call(t, handler, http.MethodPut, "/api/v1/state", updated, one)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"revision":2`) {
		t.Fatalf("state update failed: %d %s", response.Code, response.Body.String())
	}
	conflict := call(t, handler, http.MethodPut, "/api/v1/state", updated, one)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), `"currentRevision":2`) {
		t.Fatalf("expected conflict: %d %s", conflict.Code, conflict.Body.String())
	}
	twoState = call(t, handler, http.MethodGet, "/api/v1/state", "", two)
	if !strings.Contains(twoState.Body.String(), `"revision":1`) {
		t.Fatalf("user two revision changed: %s", twoState.Body.String())
	}
}

func TestLoginProfileSecurityAndLogout(t *testing.T) {
	handler := newTestAPI(t)
	firstCookie, _ := register(t, handler, "account@example.com", "account")
	login := call(t, handler, http.MethodPost, "/api/v1/auth/login", `{"email":"account@example.com","password":"correct-horse-123"}`, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("login: %d %s", login.Code, login.Body.String())
	}
	secondCookie := login.Result().Cookies()[0]
	invalid := call(t, handler, http.MethodPost, "/api/v1/auth/login", `{"email":"missing@example.com","password":"wrong-password"}`, nil)
	if invalid.Code != http.StatusUnauthorized || !strings.Contains(invalid.Body.String(), "INVALID_CREDENTIALS") {
		t.Fatalf("login enumeration response: %d %s", invalid.Code, invalid.Body.String())
	}

	profile := call(t, handler, http.MethodPatch, "/api/v1/users/me", `{"displayName":"新的称呼"}`, secondCookie)
	if profile.Code != http.StatusOK || !strings.Contains(profile.Body.String(), "新的称呼") {
		t.Fatalf("profile: %d %s", profile.Code, profile.Body.String())
	}
	email := call(t, handler, http.MethodPut, "/api/v1/users/me/email", `{"currentPassword":"correct-horse-123","email":"new@example.com"}`, secondCookie)
	if email.Code != http.StatusOK || !strings.Contains(email.Body.String(), "new@example.com") {
		t.Fatalf("email: %d %s", email.Code, email.Body.String())
	}
	password := call(t, handler, http.MethodPut, "/api/v1/users/me/password", `{"currentPassword":"correct-horse-123","password":"new-password-456"}`, secondCookie)
	if password.Code != http.StatusNoContent {
		t.Fatalf("password: %d %s", password.Code, password.Body.String())
	}
	rotatedCookie := password.Result().Cookies()[0]
	if response := call(t, handler, http.MethodGet, "/api/v1/auth/session", "", firstCookie); response.Code != http.StatusUnauthorized {
		t.Fatalf("other session was not revoked: %d", response.Code)
	}
	if response := call(t, handler, http.MethodGet, "/api/v1/auth/session", "", rotatedCookie); response.Code != http.StatusOK {
		t.Fatalf("rotated session invalid: %d %s", response.Code, response.Body.String())
	}
	logout := call(t, handler, http.MethodPost, "/api/v1/auth/logout", `{}`, rotatedCookie)
	if logout.Code != http.StatusNoContent || logout.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("logout: %d %#v", logout.Code, logout.Result().Cookies())
	}
}

func TestCORSAndSecurityHeaders(t *testing.T) {
	handler := newTestAPI(t)
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/auth/login", nil)
	request.Header.Set("Origin", "http://127.0.0.1:5173")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("credentialed CORS missing: %d %#v", response.Code, response.Header())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{}`))
	request.Header.Set("Origin", "https://untrusted.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || response.Header().Get("X-Content-Type-Options") != "" { // rejection happens before regular security headers
		if response.Code != http.StatusForbidden {
			t.Fatalf("untrusted origin accepted: %d", response.Code)
		}
	}
	request = httptest.NewRequest(http.MethodGet, "http://dayorder.local/api/v1/health", nil)
	request.Header.Set("Origin", "http://dayorder.local")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("same-origin or headers failed: %d %#v", response.Code, response.Header())
	}
}
