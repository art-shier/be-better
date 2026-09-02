package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"dayorder.local/api/internal/auth"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

var (
	ErrValidation         = errors.New("validation failed")
	ErrEmailInUse         = errors.New("email is already registered")
	ErrInvalidToken       = errors.New("account token is invalid or expired")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAccountNotActive   = errors.New("account is not active")
	ErrInvalidSession     = errors.New("session is invalid or expired")
)

var emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

type AccountStore interface {
	CreateRegistration(context.Context, model.AccountRegistration) (model.Account, model.Session, error)
	ConsumeAccountToken(context.Context, []byte, model.AccountTokenPurpose, time.Time) (model.Account, error)
	LookupLoginAccount(context.Context, string) (model.LoginAccount, error)
	CreateAccountTokenDelivery(context.Context, model.Account, model.AccountTokenDelivery) error
	ResetPasswordWithToken(context.Context, []byte, string, time.Time) (model.Account, error)
	UpdateDisplayName(context.Context, uuid.UUID, string) (model.Account, error)
	UpdateEmail(context.Context, uuid.UUID, string) (model.Account, error)
}

type AccountService struct {
	store    AccountStore
	now      func() time.Time
	newUUID  func() (uuid.UUID, error)
	newToken func() (string, []byte, error)
}

type RegisterInput struct {
	Email       string
	DisplayName string
	Password    string
	UserAgent   string
}

type RegistrationResult struct {
	Account model.Account
	Session model.Session
	Token   string
}

func NewAccountService(store AccountStore) (*AccountService, error) {
	if store == nil {
		return nil, errors.New("account store is required")
	}
	return &AccountService{
		store:    store,
		now:      func() time.Time { return time.Now().UTC() },
		newUUID:  uuid.NewRandom,
		newToken: auth.NewToken,
	}, nil
}

func (service *AccountService) Register(ctx context.Context, input RegisterInput) (RegistrationResult, error) {
	normalizedEmail, err := NormalizeEmail(input.Email)
	if err != nil {
		return RegistrationResult{}, err
	}
	displayName, err := ValidateDisplayName(input.DisplayName)
	if err != nil {
		return RegistrationResult{}, err
	}
	if err = ValidatePassword(input.Password); err != nil {
		return RegistrationResult{}, err
	}
	passwordHash, err := auth.HashPassword(input.Password)
	if err != nil {
		return RegistrationResult{}, fmt.Errorf("hash account password: %w", err)
	}
	userID, err := service.newUUID()
	if err != nil {
		return RegistrationResult{}, fmt.Errorf("generate account ID: %w", err)
	}
	sessionID, err := service.newUUID()
	if err != nil {
		return RegistrationResult{}, fmt.Errorf("generate registration session ID: %w", err)
	}
	token, tokenHash, err := service.newToken()
	if err != nil {
		return RegistrationResult{}, fmt.Errorf("generate registration session token: %w", err)
	}
	now := service.now().UTC()
	registration := model.AccountRegistration{
		Account: model.Account{
			ID:              userID,
			Email:           normalizedEmail,
			NormalizedEmail: normalizedEmail,
			DisplayName:     displayName,
			Status:          model.AccountActive,
			CreatedAt:       now,
			UpdatedAt:       now,
		},
		PasswordHash: passwordHash,
		Session: model.NewSession{
			Session: model.Session{
				ID: sessionID, UserID: userID, UserAgent: input.UserAgent,
				CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(sessionDuration),
			},
			TokenHash: tokenHash,
		},
	}
	account, session, err := service.store.CreateRegistration(ctx, registration)
	if errors.Is(err, model.ErrConflict) {
		return RegistrationResult{}, ErrEmailInUse
	}
	if err != nil {
		return RegistrationResult{}, fmt.Errorf("create account registration: %w", err)
	}
	return RegistrationResult{Account: account, Session: session, Token: token}, nil
}

func (service *AccountService) VerifyEmail(ctx context.Context, token string) (model.Account, error) {
	if strings.TrimSpace(token) == "" {
		return model.Account{}, ErrInvalidToken
	}
	account, err := service.store.ConsumeAccountToken(
		ctx, auth.HashToken(token), model.TokenVerifyEmail, service.now().UTC(),
	)
	if errors.Is(err, model.ErrNotFound) {
		return model.Account{}, ErrInvalidToken
	}
	if err != nil {
		return model.Account{}, fmt.Errorf("consume verification token: %w", err)
	}
	return account, nil
}

func (service *AccountService) ResendVerification(ctx context.Context, email string) error {
	return service.requestAccountToken(ctx, email, model.TokenVerifyEmail, "email.verification.requested", 24*time.Hour, model.AccountPendingVerification)
}

func (service *AccountService) RequestPasswordReset(ctx context.Context, email string) error {
	return service.requestAccountToken(ctx, email, model.TokenResetPassword, "email.password_reset.requested", time.Hour, model.AccountActive)
}

func (service *AccountService) ResetPassword(ctx context.Context, token, password string) (model.Account, error) {
	if strings.TrimSpace(token) == "" {
		return model.Account{}, ErrInvalidToken
	}
	if err := ValidatePassword(password); err != nil {
		return model.Account{}, err
	}
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		return model.Account{}, fmt.Errorf("hash reset password: %w", err)
	}
	account, err := service.store.ResetPasswordWithToken(ctx, auth.HashToken(token), passwordHash, service.now().UTC())
	if errors.Is(err, model.ErrNotFound) {
		return model.Account{}, ErrInvalidToken
	}
	if err != nil {
		return model.Account{}, fmt.Errorf("reset account password: %w", err)
	}
	return account, nil
}

func (service *AccountService) UpdateDisplayName(ctx context.Context, userID uuid.UUID, displayName string) (model.Account, error) {
	validated, err := ValidateDisplayName(displayName)
	if err != nil {
		return model.Account{}, err
	}
	account, err := service.store.UpdateDisplayName(ctx, userID, validated)
	if err != nil {
		return model.Account{}, fmt.Errorf("update display name: %w", err)
	}
	return account, nil
}

func (service *AccountService) UpdateEmail(ctx context.Context, current model.Account, email string) (model.Account, error) {
	normalized, err := NormalizeEmail(email)
	if err != nil {
		return model.Account{}, err
	}
	account, err := service.store.UpdateEmail(ctx, current.ID, normalized)
	if errors.Is(err, model.ErrConflict) {
		return model.Account{}, ErrEmailInUse
	}
	if err != nil {
		return model.Account{}, fmt.Errorf("update email: %w", err)
	}
	return account, nil
}

func (service *AccountService) requestAccountToken(
	ctx context.Context,
	email string,
	purpose model.AccountTokenPurpose,
	eventType string,
	duration time.Duration,
	requiredStatus model.AccountStatus,
) error {
	normalized, err := NormalizeEmail(email)
	if err != nil {
		return nil
	}
	loginAccount, err := service.store.LookupLoginAccount(ctx, normalized)
	if errors.Is(err, model.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup account token recipient: %w", err)
	}
	if loginAccount.Status != requiredStatus {
		return nil
	}
	delivery, err := service.newTokenDelivery(
		loginAccount.ID, loginAccount.Email, loginAccount.DisplayName, purpose, eventType, duration,
	)
	if err != nil {
		return err
	}
	if err = service.store.CreateAccountTokenDelivery(ctx, loginAccount.Account, delivery); err != nil {
		return fmt.Errorf("create account token delivery: %w", err)
	}
	return nil
}

func (service *AccountService) newTokenDelivery(
	userID uuid.UUID,
	email string,
	displayName string,
	purpose model.AccountTokenPurpose,
	eventType string,
	duration time.Duration,
) (model.AccountTokenDelivery, error) {
	tokenID, err := service.newUUID()
	if err != nil {
		return model.AccountTokenDelivery{}, fmt.Errorf("generate account token ID: %w", err)
	}
	outboxID, err := service.newUUID()
	if err != nil {
		return model.AccountTokenDelivery{}, fmt.Errorf("generate account token outbox ID: %w", err)
	}
	plainToken, tokenHash, err := service.newToken()
	if err != nil {
		return model.AccountTokenDelivery{}, fmt.Errorf("generate account token: %w", err)
	}
	now := service.now().UTC()
	payload, err := json.Marshal(map[string]string{
		"email": email, "displayName": displayName, "token": plainToken,
	})
	if err != nil {
		return model.AccountTokenDelivery{}, fmt.Errorf("encode account token outbox: %w", err)
	}
	return model.AccountTokenDelivery{
		Token: model.AccountToken{
			ID: tokenID, UserID: userID, Purpose: purpose, TokenHash: tokenHash,
			ExpiresAt: now.Add(duration), CreatedAt: now,
		},
		OutboxID: outboxID, EventType: eventType, OutboxPayload: payload,
	}, nil
}

func NormalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 3 || len(value) > 254 || !emailPattern.MatchString(value) {
		return "", fmt.Errorf("%w: invalid email", ErrValidation)
	}
	return value, nil
}

func ValidateDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if length := utf8.RuneCountInString(value); length < 1 || length > 80 {
		return "", fmt.Errorf("%w: display name must contain 1 to 80 characters", ErrValidation)
	}
	return value, nil
}

func ValidatePassword(value string) error {
	if length := utf8.RuneCountInString(value); length < 10 || length > 128 {
		return fmt.Errorf("%w: password must contain 10 to 128 characters", ErrValidation)
	}
	return nil
}
