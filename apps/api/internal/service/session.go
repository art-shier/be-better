package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"dayorder.local/api/internal/auth"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

const (
	sessionDuration  = 30 * 24 * time.Hour
	sessionTouchRate = 5 * time.Minute
)

type SessionStore interface {
	LookupLoginAccount(context.Context, string) (model.LoginAccount, error)
	CreateSession(context.Context, model.NewSession) (model.Session, error)
	AuthenticateSession(context.Context, []byte, time.Duration) (model.AuthenticatedSession, error)
	RevokeSession(context.Context, uuid.UUID, uuid.UUID) error
	RotatePasswordSession(context.Context, model.PasswordSessionRotation) (model.Session, error)
	UpdatePasswordHash(context.Context, uuid.UUID, string) error
	PasswordHashByUserID(context.Context, uuid.UUID) (string, error)
}

type ThrottleStore interface {
	LoginThrottleStatus(context.Context, string, []byte) (model.LoginThrottle, error)
	RecordLoginFailure(context.Context, string, []byte) (model.LoginThrottle, error)
	ClearLoginThrottle(context.Context, string, []byte) error
}

type SessionService struct {
	sessions  SessionStore
	throttles ThrottleStore
	hmacKey   []byte
	dummyHash string
	now       func() time.Time
	newUUID   func() (uuid.UUID, error)
	newToken  func() (string, []byte, error)
}

type LoginInput struct {
	Email     string
	Password  string
	IP        string
	UserAgent string
}

type SessionResult struct {
	Account model.Account
	Session model.Session
	Token   string
}

type RateLimitError struct{ RetryAt time.Time }

func (err *RateLimitError) Error() string { return "login rate limited" }

type ChangePasswordInput struct {
	Account         model.Account
	CurrentPassword string
	NewPassword     string
	UserAgent       string
}

func NewSessionService(sessions SessionStore, throttles ThrottleStore, hmacKey []byte) (*SessionService, error) {
	if sessions == nil || throttles == nil {
		return nil, errors.New("session and throttle stores are required")
	}
	if len(hmacKey) < 32 {
		return nil, errors.New("login throttle HMAC key must contain at least 32 bytes")
	}
	dummyHash, err := auth.HashPassword("dayorder-invalid-password-placeholder")
	if err != nil {
		return nil, fmt.Errorf("create login comparison hash: %w", err)
	}
	return &SessionService{
		sessions: sessions, throttles: throttles, hmacKey: append([]byte(nil), hmacKey...),
		dummyHash: dummyHash,
		now:       func() time.Time { return time.Now().UTC() },
		newUUID:   uuid.NewRandom,
		newToken:  auth.NewToken,
	}, nil
}

func (service *SessionService) Login(ctx context.Context, input LoginInput) (SessionResult, error) {
	normalizedEmail, normalizeErr := NormalizeEmail(input.Email)
	if normalizeErr != nil {
		normalizedEmail = strings.ToLower(strings.TrimSpace(input.Email))
	}
	dimensions := []throttleDimension{
		{name: "email", hash: service.dimensionHash("email", normalizedEmail)},
		{name: "ip", hash: service.dimensionHash("ip", canonicalIP(input.IP))},
	}
	now := service.now().UTC()
	var retryAt time.Time
	for _, dimension := range dimensions {
		status, err := service.throttles.LoginThrottleStatus(ctx, dimension.name, dimension.hash)
		if err != nil && !errors.Is(err, model.ErrNotFound) {
			return SessionResult{}, fmt.Errorf("read login throttle: %w", err)
		}
		if status.BlockedUntil != nil && status.BlockedUntil.After(now) && status.BlockedUntil.After(retryAt) {
			retryAt = status.BlockedUntil.UTC()
		}
	}
	if !retryAt.IsZero() {
		return SessionResult{}, &RateLimitError{RetryAt: retryAt}
	}

	loginAccount, lookupErr := service.sessions.LookupLoginAccount(ctx, normalizedEmail)
	passwordHash := service.dummyHash
	if lookupErr == nil {
		passwordHash = loginAccount.PasswordHash
	}
	passwordValid, verifyErr := auth.VerifyPassword(passwordHash, input.Password)
	if normalizeErr != nil || lookupErr != nil || verifyErr != nil || !passwordValid {
		for _, dimension := range dimensions {
			if _, err := service.throttles.RecordLoginFailure(ctx, dimension.name, dimension.hash); err != nil {
				return SessionResult{}, fmt.Errorf("record login throttle: %w", err)
			}
		}
		return SessionResult{}, ErrInvalidCredentials
	}
	if loginAccount.Status != model.AccountActive {
		return SessionResult{}, ErrAccountNotActive
	}
	for _, dimension := range dimensions {
		if err := service.throttles.ClearLoginThrottle(ctx, dimension.name, dimension.hash); err != nil {
			return SessionResult{}, fmt.Errorf("clear login throttle: %w", err)
		}
	}
	if auth.PasswordHashNeedsUpgrade(loginAccount.PasswordHash) {
		if upgraded, err := auth.HashPassword(input.Password); err == nil {
			if err = service.sessions.UpdatePasswordHash(ctx, loginAccount.ID, upgraded); err != nil {
				return SessionResult{}, fmt.Errorf("upgrade password hash: %w", err)
			}
		}
	}
	return service.createSession(ctx, loginAccount.Account, input.UserAgent, now)
}

func (service *SessionService) Authenticate(ctx context.Context, token string) (model.AuthenticatedSession, error) {
	if strings.TrimSpace(token) == "" {
		return model.AuthenticatedSession{}, ErrInvalidSession
	}
	authenticated, err := service.sessions.AuthenticateSession(ctx, auth.HashToken(token), sessionTouchRate)
	if errors.Is(err, model.ErrNotFound) {
		return model.AuthenticatedSession{}, ErrInvalidSession
	}
	if err != nil {
		return model.AuthenticatedSession{}, fmt.Errorf("authenticate session: %w", err)
	}
	if authenticated.Account.Status != model.AccountActive {
		return model.AuthenticatedSession{}, ErrAccountNotActive
	}
	return authenticated, nil
}

func (service *SessionService) Logout(ctx context.Context, authenticated model.AuthenticatedSession) error {
	if err := service.sessions.RevokeSession(ctx, authenticated.Account.ID, authenticated.Session.ID); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func (service *SessionService) VerifyPassword(ctx context.Context, userID uuid.UUID, password string) error {
	passwordHash, err := service.sessions.PasswordHashByUserID(ctx, userID)
	if err != nil {
		return fmt.Errorf("read current password: %w", err)
	}
	valid, verifyErr := auth.VerifyPassword(passwordHash, password)
	if verifyErr != nil || !valid {
		return ErrInvalidCredentials
	}
	return nil
}

func (service *SessionService) ChangePassword(ctx context.Context, input ChangePasswordInput) (SessionResult, error) {
	if err := ValidatePassword(input.NewPassword); err != nil {
		return SessionResult{}, err
	}
	currentHash, err := service.sessions.PasswordHashByUserID(ctx, input.Account.ID)
	if err != nil {
		return SessionResult{}, fmt.Errorf("read current password: %w", err)
	}
	valid, verifyErr := auth.VerifyPassword(currentHash, input.CurrentPassword)
	if verifyErr != nil || !valid {
		return SessionResult{}, ErrInvalidCredentials
	}
	newHash, err := auth.HashPassword(input.NewPassword)
	if err != nil {
		return SessionResult{}, fmt.Errorf("hash new password: %w", err)
	}
	now := service.now().UTC()
	newSession, token, err := service.newSession(input.Account.ID, input.UserAgent, now)
	if err != nil {
		return SessionResult{}, err
	}
	persisted, err := service.sessions.RotatePasswordSession(ctx, model.PasswordSessionRotation{
		UserID: input.Account.ID, PasswordHash: newHash, Session: newSession,
	})
	if err != nil {
		return SessionResult{}, fmt.Errorf("rotate password session: %w", err)
	}
	return SessionResult{Account: input.Account, Session: persisted, Token: token}, nil
}

func (service *SessionService) createSession(ctx context.Context, account model.Account, userAgent string, now time.Time) (SessionResult, error) {
	newSession, token, err := service.newSession(account.ID, userAgent, now)
	if err != nil {
		return SessionResult{}, err
	}
	persisted, err := service.sessions.CreateSession(ctx, newSession)
	if err != nil {
		return SessionResult{}, fmt.Errorf("create session: %w", err)
	}
	return SessionResult{Account: account, Session: persisted, Token: token}, nil
}

func (service *SessionService) newSession(userID uuid.UUID, userAgent string, now time.Time) (model.NewSession, string, error) {
	sessionID, err := service.newUUID()
	if err != nil {
		return model.NewSession{}, "", fmt.Errorf("generate session ID: %w", err)
	}
	token, tokenHash, err := service.newToken()
	if err != nil {
		return model.NewSession{}, "", fmt.Errorf("generate session token: %w", err)
	}
	session := model.Session{
		ID: sessionID, UserID: userID, UserAgent: userAgent,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(sessionDuration),
	}
	return model.NewSession{Session: session, TokenHash: tokenHash}, token, nil
}

type throttleDimension struct {
	name string
	hash []byte
}

func (service *SessionService) dimensionHash(dimension, value string) []byte {
	mac := hmac.New(sha256.New, service.hmacKey)
	_, _ = mac.Write([]byte(dimension))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func canonicalIP(value string) string {
	value = strings.TrimSpace(value)
	if address, err := netip.ParseAddr(value); err == nil {
		return address.Unmap().String()
	}
	return "unknown"
}
