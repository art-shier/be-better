package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/auth"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type fakeSessionStore struct {
	login             model.LoginAccount
	loginErr          error
	created           *model.NewSession
	authHash          []byte
	authenticated     model.AuthenticatedSession
	authErr           error
	revokedSessionID  uuid.UUID
	rotation          *model.PasswordSessionRotation
	passwordHashValue string
	currentHash       string
}

func (store *fakeSessionStore) LookupLoginAccount(context.Context, string) (model.LoginAccount, error) {
	return store.login, store.loginErr
}
func (store *fakeSessionStore) CreateSession(_ context.Context, session model.NewSession) (model.Session, error) {
	store.created = &session
	return session.Session, nil
}
func (store *fakeSessionStore) AuthenticateSession(_ context.Context, tokenHash []byte, _ time.Duration) (model.AuthenticatedSession, error) {
	store.authHash = append([]byte(nil), tokenHash...)
	return store.authenticated, store.authErr
}
func (store *fakeSessionStore) RevokeSession(_ context.Context, _ uuid.UUID, sessionID uuid.UUID) error {
	store.revokedSessionID = sessionID
	return nil
}
func (store *fakeSessionStore) RotatePasswordSession(_ context.Context, rotation model.PasswordSessionRotation) (model.Session, error) {
	store.rotation = &rotation
	return rotation.Session.Session, nil
}
func (store *fakeSessionStore) UpdatePasswordHash(_ context.Context, _ uuid.UUID, passwordHash string) error {
	store.passwordHashValue = passwordHash
	return nil
}
func (store *fakeSessionStore) PasswordHashByUserID(context.Context, uuid.UUID) (string, error) {
	return store.currentHash, nil
}

type fakeThrottleStore struct {
	blocked       bool
	retryAt       time.Time
	recorded      []string
	cleared       []string
	statusQueries []string
}

func (store *fakeThrottleStore) LoginThrottleStatus(_ context.Context, dimension string, _ []byte) (model.LoginThrottle, error) {
	store.statusQueries = append(store.statusQueries, dimension)
	if store.blocked {
		return model.LoginThrottle{Failures: 5, BlockedUntil: &store.retryAt}, nil
	}
	return model.LoginThrottle{}, model.ErrNotFound
}
func (store *fakeThrottleStore) RecordLoginFailure(_ context.Context, dimension string, _ []byte) (model.LoginThrottle, error) {
	store.recorded = append(store.recorded, dimension)
	return model.LoginThrottle{}, nil
}
func (store *fakeThrottleStore) ClearLoginThrottle(_ context.Context, dimension string, _ []byte) error {
	store.cleared = append(store.cleared, dimension)
	return nil
}

func newSessionServiceForTest(t *testing.T, sessions *fakeSessionStore, throttles *fakeThrottleStore) *SessionService {
	t.Helper()
	service, err := NewSessionService(sessions, throttles, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC) }
	return service
}

func TestLoginReturnsSameCredentialErrorForUnknownAndWrongPassword(t *testing.T) {
	validHash, err := auth.HashPassword("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		store *fakeSessionStore
	}{
		{name: "unknown", store: &fakeSessionStore{loginErr: model.ErrNotFound}},
		{name: "wrong password", store: &fakeSessionStore{login: model.LoginAccount{PasswordHash: validHash, Account: model.Account{Status: model.AccountActive}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			throttles := &fakeThrottleStore{}
			service := newSessionServiceForTest(t, test.store, throttles)
			_, err := service.Login(context.Background(), LoginInput{
				Email: "user@example.com", Password: "wrong-value", IP: "203.0.113.10",
			})
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("Login() error = %v, want ErrInvalidCredentials", err)
			}
			if len(throttles.recorded) != 2 {
				t.Fatalf("recorded throttle dimensions = %v", throttles.recorded)
			}
		})
	}
}

func TestLoginStoresOnlySessionTokenHashAndClearsBothThrottleDimensions(t *testing.T) {
	passwordHash, err := auth.HashPassword("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	accountID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	sessions := &fakeSessionStore{login: model.LoginAccount{
		Account:      model.Account{ID: accountID, Email: "user@example.com", Status: model.AccountActive},
		PasswordHash: passwordHash,
	}}
	throttles := &fakeThrottleStore{}
	service := newSessionServiceForTest(t, sessions, throttles)
	result, err := service.Login(context.Background(), LoginInput{
		Email: "USER@example.com", Password: "correct-password", IP: "203.0.113.10", UserAgent: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Token == "" || sessions.created == nil || len(sessions.created.TokenHash) != 32 {
		t.Fatalf("login result/session = %#v / %#v", result, sessions.created)
	}
	if string(sessions.created.TokenHash) == result.Token {
		t.Fatal("session repository received a plaintext token")
	}
	if len(throttles.cleared) != 2 {
		t.Fatalf("cleared throttle dimensions = %v", throttles.cleared)
	}
}

func TestLoginRejectsBlockedAndInactiveAccounts(t *testing.T) {
	retryAt := time.Date(2026, 8, 28, 9, 15, 0, 0, time.UTC)
	blocked := &fakeThrottleStore{blocked: true, retryAt: retryAt}
	service := newSessionServiceForTest(t, &fakeSessionStore{}, blocked)
	_, err := service.Login(context.Background(), LoginInput{Email: "user@example.com", Password: "password-value", IP: "203.0.113.10"})
	var rateLimit *RateLimitError
	if !errors.As(err, &rateLimit) || !rateLimit.RetryAt.Equal(retryAt) {
		t.Fatalf("blocked Login() error = %#v", err)
	}

	passwordHash, hashErr := auth.HashPassword("correct-password")
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	service = newSessionServiceForTest(t, &fakeSessionStore{login: model.LoginAccount{
		Account: model.Account{Status: model.AccountPendingVerification}, PasswordHash: passwordHash,
	}}, &fakeThrottleStore{})
	_, err = service.Login(context.Background(), LoginInput{Email: "user@example.com", Password: "correct-password", IP: "203.0.113.10"})
	if !errors.Is(err, ErrAccountNotActive) {
		t.Fatalf("inactive Login() error = %v", err)
	}
}

func TestAuthenticateHashesTokenAndPasswordRotationCreatesNewSession(t *testing.T) {
	accountID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	sessionID := uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	sessions := &fakeSessionStore{authenticated: model.AuthenticatedSession{
		Account: model.Account{ID: accountID, Status: model.AccountActive},
		Session: model.Session{ID: sessionID, UserID: accountID},
	}, currentHash: mustPasswordHash(t, "old-password-value")}
	service := newSessionServiceForTest(t, sessions, &fakeThrottleStore{})
	if _, err := service.Authenticate(context.Background(), "raw-session-token"); err != nil {
		t.Fatal(err)
	}
	if string(sessions.authHash) == "raw-session-token" || len(sessions.authHash) != 32 {
		t.Fatal("Authenticate did not hash the session token")
	}

	result, err := service.ChangePassword(context.Background(), ChangePasswordInput{
		Account:         sessions.authenticated.Account,
		CurrentPassword: "old-password-value",
		NewPassword:     "new-password-value",
		UserAgent:       "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Token == "" || sessions.rotation == nil || sessions.rotation.UserID != accountID {
		t.Fatalf("password rotation = %#v, result = %#v", sessions.rotation, result)
	}
	if sessions.rotation.PasswordHash == "new-password-value" || len(sessions.rotation.Session.TokenHash) != 32 {
		t.Fatal("password rotation persisted plaintext credentials")
	}
}

func mustPasswordHash(t testing.TB, password string) string {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
