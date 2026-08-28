package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	dayauth "dayorder.local/api/internal/auth"
	"dayorder.local/api/internal/store"
)

const (
	maxStateBytes   = 16 << 20
	maxAuthBytes    = 1 << 20
	sessionDuration = 30 * 24 * time.Hour
	sessionCookie   = "dayorder_session"
)

var (
	emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	emptyAppData = json.RawMessage(`{"version":1,"goals":[],"tasks":[],"events":[],"records":[],"notes":[],"reviews":[],"agentRuns":[],"audit":[],"settings":{"energy":3,"aiEnabled":true,"remindersEnabled":false,"onboardingCompleted":false,"focusAreas":[],"dataMode":"local","localOnly":true,"permissions":{"goals":true,"calendar":true,"records":true,"privateNotes":false}}}`)
)

type Storage interface {
	CreateAccount(context.Context, store.CreateAccountParams) (store.User, store.Session, store.State, error)
	UserByEmail(context.Context, string) (store.User, error)
	UserByID(context.Context, string) (store.User, error)
	CreateSession(context.Context, string, string, []byte, string, time.Time, time.Time) (store.Session, error)
	SessionUser(context.Context, []byte, time.Time) (store.Session, store.User, error)
	DeleteSession(context.Context, string) error
	UpdatePasswordHash(context.Context, string, string, time.Time) error
	UpdateDisplayName(context.Context, string, string, time.Time) (store.User, error)
	UpdateEmail(context.Context, string, string, time.Time) (store.User, error)
	RotatePasswordSession(context.Context, string, string, string, []byte, string, time.Time, time.Time) (store.Session, error)
	LoadUserState(context.Context, string) (store.State, error)
	SaveUserState(context.Context, string, json.RawMessage, int64) (store.State, error)
}

type Options struct {
	AllowedOrigins []string
	Logger         *slog.Logger
}
type API struct {
	store          Storage
	allowedOrigins map[string]struct{}
	logger         *slog.Logger
	limiter        *loginLimiter
	dummyHash      string
}
type errorResponse struct {
	Code            string `json:"code"`
	Message         string `json:"message"`
	CurrentRevision int64  `json:"currentRevision,omitempty"`
}
type authResult struct {
	session store.Session
	user    store.User
}

type loginAttempt struct {
	failures int
	started  time.Time
}
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{attempts: make(map[string]loginAttempt)} }
func (l *loginLimiter) blocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.attempts[key]
	if !ok {
		return false
	}
	if now.Sub(a.started) >= 15*time.Minute {
		delete(l.attempts, key)
		return false
	}
	return a.failures >= 5
}
func (l *loginLimiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.attempts[key]
	if !ok || now.Sub(a.started) >= 15*time.Minute {
		a = loginAttempt{started: now}
	}
	a.failures++
	l.attempts[key] = a
}
func (l *loginLimiter) clear(key string) { l.mu.Lock(); delete(l.attempts, key); l.mu.Unlock() }

func New(storage Storage, options Options) http.Handler {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	dummyHash, _ := dayauth.HashPassword("invalid-password-placeholder")
	a := &API{store: storage, allowedOrigins: make(map[string]struct{}), logger: logger, limiter: newLoginLimiter(), dummyHash: dummyHash}
	for _, origin := range options.AllowedOrigins {
		if origin = strings.TrimSpace(origin); origin != "" {
			a.allowedOrigins[origin] = struct{}{}
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", a.health)
	mux.HandleFunc("/api/v1/auth/register", a.register)
	mux.HandleFunc("/api/v1/auth/login", a.login)
	mux.HandleFunc("/api/v1/auth/logout", a.logout)
	mux.HandleFunc("/api/v1/auth/session", a.currentSession)
	mux.HandleFunc("/api/v1/users/me", a.profile)
	mux.HandleFunc("/api/v1/users/me/email", a.email)
	mux.HandleFunc("/api/v1/users/me/password", a.password)
	mux.HandleFunc("/api/v1/state", a.state)
	return a.middleware(mux)
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		a.methodNotAllowed(w)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "storage": "sqlite", "time": time.Now().UTC()})
}

type registerRequest struct {
	DisplayName string          `json:"displayName"`
	Email       string          `json:"email"`
	Password    string          `json:"password"`
	InitialData json.RawMessage `json:"initialData"`
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		a.methodNotAllowed(w)
		return
	}
	var request registerRequest
	if !a.decodeJSON(w, r, &request, maxStateBytes) {
		return
	}
	name, nameErr := validateDisplayName(request.DisplayName)
	email, emailErr := validateEmail(request.Email)
	passwordErr := validatePassword(request.Password)
	if nameErr != nil || emailErr != nil || passwordErr != nil {
		message := firstError(nameErr, emailErr, passwordErr).Error()
		a.writeError(w, http.StatusUnprocessableEntity, "VALIDATION_FAILED", message)
		return
	}
	data := request.InitialData
	if len(data) == 0 || string(data) == "null" {
		data = emptyAppData
	}
	if err := validateAppData(data); err != nil {
		a.writeError(w, http.StatusUnprocessableEntity, "INVALID_STATE", err.Error())
		return
	}
	passwordHash, err := dayauth.HashPassword(request.Password)
	if err != nil {
		a.internal(w, "hash password", err)
		return
	}
	userID, err := dayauth.NewID("user")
	if err != nil {
		a.internal(w, "generate user id", err)
		return
	}
	sessionID, err := dayauth.NewID("session")
	if err != nil {
		a.internal(w, "generate session id", err)
		return
	}
	token, tokenHash, err := dayauth.NewToken()
	if err != nil {
		a.internal(w, "generate session token", err)
		return
	}
	now := time.Now().UTC()
	user, session, state, err := a.store.CreateAccount(r.Context(), store.CreateAccountParams{UserID: userID, Email: email, DisplayName: name, PasswordHash: passwordHash, SessionID: sessionID, TokenHash: tokenHash, UserAgent: r.UserAgent(), StateData: data, Now: now, ExpiresAt: now.Add(sessionDuration)})
	if errors.Is(err, store.ErrDuplicateEmail) {
		a.writeError(w, http.StatusConflict, "EMAIL_ALREADY_REGISTERED", "该邮箱已注册，请直接登录")
		return
	}
	if err != nil {
		a.internal(w, "register account", err)
		return
	}
	a.setSessionCookie(w, r, token, session.ExpiresAt)
	a.writeJSON(w, http.StatusCreated, map[string]any{"user": user, "expiresAt": session.ExpiresAt, "state": state})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		a.methodNotAllowed(w)
		return
	}
	var request loginRequest
	if !a.decodeJSON(w, r, &request, maxAuthBytes) {
		return
	}
	email, _ := validateEmail(request.Email)
	if email == "" {
		email = strings.ToLower(strings.TrimSpace(request.Email))
	}
	key := clientIP(r) + "|" + email
	now := time.Now().UTC()
	if a.limiter.blocked(key, now) {
		w.Header().Set("Retry-After", "900")
		a.writeError(w, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "尝试次数过多，请稍后再试")
		return
	}
	user, err := a.store.UserByEmail(r.Context(), email)
	hash := a.dummyHash
	if err == nil {
		hash = user.PasswordHash
	}
	valid, verifyErr := dayauth.VerifyPassword(hash, request.Password)
	if verifyErr != nil || err != nil || !valid {
		a.limiter.fail(key, now)
		a.writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "邮箱或密码不正确")
		return
	}
	a.limiter.clear(key)
	if dayauth.PasswordHashNeedsUpgrade(user.PasswordHash) {
		if upgraded, hashErr := dayauth.HashPassword(request.Password); hashErr == nil {
			_ = a.store.UpdatePasswordHash(r.Context(), user.ID, upgraded, now)
		}
	}
	sessionID, idErr := dayauth.NewID("session")
	token, tokenHash, tokenErr := dayauth.NewToken()
	if idErr != nil || tokenErr != nil {
		a.internal(w, "generate session", firstError(idErr, tokenErr))
		return
	}
	session, err := a.store.CreateSession(r.Context(), sessionID, user.ID, tokenHash, r.UserAgent(), now, now.Add(sessionDuration))
	if err != nil {
		a.internal(w, "create session", err)
		return
	}
	a.setSessionCookie(w, r, token, session.ExpiresAt)
	a.writeJSON(w, http.StatusOK, map[string]any{"user": user, "expiresAt": session.ExpiresAt})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		a.methodNotAllowed(w)
		return
	}
	result, ok := a.authenticate(r)
	if !ok {
		a.expireSessionCookie(w, r)
		a.authRequired(w)
		return
	}
	if err := a.store.DeleteSession(r.Context(), result.session.ID); err != nil {
		a.internal(w, "delete session", err)
		return
	}
	a.expireSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}
func (a *API) currentSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		a.methodNotAllowed(w)
		return
	}
	result, ok := a.authenticate(r)
	if !ok {
		a.authRequired(w)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"user": result.user, "expiresAt": result.session.ExpiresAt})
}

func (a *API) profile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		a.methodNotAllowed(w)
		return
	}
	result, ok := a.authenticate(r)
	if !ok {
		a.authRequired(w)
		return
	}
	var request struct {
		DisplayName string `json:"displayName"`
	}
	if !a.decodeJSON(w, r, &request, maxAuthBytes) {
		return
	}
	name, err := validateDisplayName(request.DisplayName)
	if err != nil {
		a.writeError(w, http.StatusUnprocessableEntity, "VALIDATION_FAILED", err.Error())
		return
	}
	user, err := a.store.UpdateDisplayName(r.Context(), result.user.ID, name, time.Now().UTC())
	if err != nil {
		a.internal(w, "update profile", err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (a *API) email(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		a.methodNotAllowed(w)
		return
	}
	result, ok := a.authenticate(r)
	if !ok {
		a.authRequired(w)
		return
	}
	var request struct {
		CurrentPassword string `json:"currentPassword"`
		Email           string `json:"email"`
	}
	if !a.decodeJSON(w, r, &request, maxAuthBytes) {
		return
	}
	email, err := validateEmail(request.Email)
	if err != nil {
		a.writeError(w, http.StatusUnprocessableEntity, "VALIDATION_FAILED", err.Error())
		return
	}
	if !passwordMatches(result.user.PasswordHash, request.CurrentPassword) {
		a.writeError(w, http.StatusUnauthorized, "CURRENT_PASSWORD_INVALID", "当前密码不正确")
		return
	}
	user, err := a.store.UpdateEmail(r.Context(), result.user.ID, email, time.Now().UTC())
	if errors.Is(err, store.ErrDuplicateEmail) {
		a.writeError(w, http.StatusConflict, "EMAIL_ALREADY_REGISTERED", "该邮箱已注册")
		return
	}
	if err != nil {
		a.internal(w, "update email", err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (a *API) password(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		a.methodNotAllowed(w)
		return
	}
	result, ok := a.authenticate(r)
	if !ok {
		a.authRequired(w)
		return
	}
	var request struct {
		CurrentPassword string `json:"currentPassword"`
		Password        string `json:"password"`
	}
	if !a.decodeJSON(w, r, &request, maxAuthBytes) {
		return
	}
	if err := validatePassword(request.Password); err != nil {
		a.writeError(w, http.StatusUnprocessableEntity, "VALIDATION_FAILED", err.Error())
		return
	}
	if !passwordMatches(result.user.PasswordHash, request.CurrentPassword) {
		a.writeError(w, http.StatusUnauthorized, "CURRENT_PASSWORD_INVALID", "当前密码不正确")
		return
	}
	hash, err := dayauth.HashPassword(request.Password)
	if err != nil {
		a.internal(w, "hash password", err)
		return
	}
	sessionID, idErr := dayauth.NewID("session")
	token, tokenHash, tokenErr := dayauth.NewToken()
	if idErr != nil || tokenErr != nil {
		a.internal(w, "generate rotated session", firstError(idErr, tokenErr))
		return
	}
	now := time.Now().UTC()
	session, err := a.store.RotatePasswordSession(r.Context(), result.user.ID, hash, sessionID, tokenHash, r.UserAgent(), now, now.Add(sessionDuration))
	if err != nil {
		a.internal(w, "rotate password session", err)
		return
	}
	a.setSessionCookie(w, r, token, session.ExpiresAt)
	w.WriteHeader(http.StatusNoContent)
}

type putStateRequest struct {
	ExpectedRevision int64           `json:"expectedRevision"`
	Data             json.RawMessage `json:"data"`
}

func (a *API) state(w http.ResponseWriter, r *http.Request) {
	result, ok := a.authenticate(r)
	if !ok {
		a.authRequired(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		state, err := a.store.LoadUserState(r.Context(), result.user.ID)
		if errors.Is(err, store.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "STATE_NOT_FOUND", "no account state has been created")
			return
		}
		if err != nil {
			a.internal(w, "load state", err)
			return
		}
		a.writeJSON(w, http.StatusOK, state)
	case http.MethodPut:
		var request putStateRequest
		if !a.decodeJSON(w, r, &request, maxStateBytes) {
			return
		}
		if err := validateAppData(request.Data); err != nil {
			a.writeError(w, http.StatusUnprocessableEntity, "INVALID_STATE", err.Error())
			return
		}
		state, err := a.store.SaveUserState(r.Context(), result.user.ID, request.Data, request.ExpectedRevision)
		if err != nil {
			var conflict *store.ConflictError
			if errors.As(err, &conflict) {
				a.writeJSON(w, http.StatusConflict, errorResponse{Code: "REVISION_CONFLICT", Message: "server state changed; fetch the latest revision before retrying", CurrentRevision: conflict.CurrentRevision})
				return
			}
			if errors.Is(err, store.ErrNotFound) {
				a.writeError(w, http.StatusNotFound, "STATE_NOT_FOUND", "no account state has been created")
				return
			}
			a.internal(w, "save state", err)
			return
		}
		a.writeJSON(w, http.StatusOK, state)
	default:
		a.methodNotAllowed(w)
	}
}

func validateAppData(data json.RawMessage) error {
	if len(data) == 0 || !json.Valid(data) {
		return errors.New("data must be valid JSON")
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("data must be a JSON object")
	}
	var version int
	if err := json.Unmarshal(value["version"], &version); err != nil || version != 1 {
		return fmt.Errorf("unsupported data version %d", version)
	}
	for _, key := range []string{"goals", "tasks", "events", "records", "notes", "reviews", "agentRuns", "audit"} {
		var items []json.RawMessage
		if err := json.Unmarshal(value[key], &items); err != nil {
			return fmt.Errorf("%s must be an array", key)
		}
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(value["settings"], &settings); err != nil || settings == nil {
		return errors.New("settings must be an object")
	}
	return nil
}

func (a *API) authenticate(r *http.Request) (authResult, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return authResult{}, false
	}
	session, user, err := a.store.SessionUser(r.Context(), dayauth.HashToken(cookie.Value), time.Now().UTC())
	if err != nil {
		return authResult{}, false
	}
	return authResult{session: session, user: user}, true
}
func passwordMatches(hash, password string) bool {
	ok, err := dayauth.VerifyPassword(hash, password)
	return err == nil && ok
}

func validateEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 254 || !emailPattern.MatchString(value) {
		return "", errors.New("请输入有效邮箱")
	}
	return value, nil
}
func validateDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	length := utf8.RuneCountInString(value)
	if length < 1 || length > 40 {
		return "", errors.New("称呼需要 1–40 个字符")
	}
	return value, nil
}
func validatePassword(value string) error {
	length := utf8.RuneCountInString(value)
	if length < 10 || length > 128 {
		return errors.New("密码需要 10–128 个字符")
	}
	return nil
}
func firstError(values ...error) error {
	for _, err := range values {
		if err != nil {
			return err
		}
	}
	return errors.New("unknown error")
}
func clientIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
		return forwarded
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (a *API) decodeJSON(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		a.writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "请求内容不完整或格式不正确")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		a.writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "请求只能包含一个 JSON 对象")
		return false
	}
	return true
}
func (a *API) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(sessionDuration.Seconds())})
}
func (a *API) expireSessionCookie(w http.ResponseWriter, r *http.Request) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, Expires: time.Unix(1, 0), MaxAge: -1})
}
func (a *API) authRequired(w http.ResponseWriter) {
	a.writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "需要登录后继续")
}
func (a *API) methodNotAllowed(w http.ResponseWriter) {
	a.writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
}
func (a *API) internal(w http.ResponseWriter, operation string, err error) {
	a.logger.Error(operation, "error", err)
	a.writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "服务暂时无法完成请求")
}

func (a *API) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			_, allowed := a.allowedOrigins[origin]
			if !allowed && !sameHostOrigin(origin, r.Host) {
				a.writeError(w, http.StatusForbidden, "ORIGIN_NOT_ALLOWED", "origin is not allowed")
				return
			}
			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, PATCH, POST, DELETE, OPTIONS")
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		started := time.Now()
		next.ServeHTTP(w, r)
		a.logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}
func sameHostOrigin(origin, requestHost string) bool {
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Scheme != "" && parsed.Host != "" && strings.EqualFold(parsed.Host, requestHost)
}
func (a *API) writeError(w http.ResponseWriter, status int, code, message string) {
	a.writeJSON(w, status, errorResponse{Code: code, Message: message})
}
func (a *API) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
