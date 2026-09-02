package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type fakeAccountStore struct {
	registration  *model.AccountRegistration
	createErr     error
	consumeHash   []byte
	consumeResult model.Account
	consumeErr    error
	updatedEmail  string
}

func (store *fakeAccountStore) CreateRegistration(_ context.Context, registration model.AccountRegistration) (model.Account, model.Session, error) {
	store.registration = &registration
	return registration.Account, registration.Session.Session, store.createErr
}

func (store *fakeAccountStore) ConsumeAccountToken(_ context.Context, tokenHash []byte, _ model.AccountTokenPurpose, _ time.Time) (model.Account, error) {
	store.consumeHash = append([]byte(nil), tokenHash...)
	return store.consumeResult, store.consumeErr
}
func (store *fakeAccountStore) LookupLoginAccount(context.Context, string) (model.LoginAccount, error) {
	return model.LoginAccount{}, model.ErrNotFound
}
func (store *fakeAccountStore) CreateAccountTokenDelivery(context.Context, model.Account, model.AccountTokenDelivery) error {
	return nil
}
func (store *fakeAccountStore) ResetPasswordWithToken(context.Context, []byte, string, time.Time) (model.Account, error) {
	return model.Account{}, model.ErrNotFound
}
func (store *fakeAccountStore) UpdateDisplayName(context.Context, uuid.UUID, string) (model.Account, error) {
	return model.Account{}, nil
}
func (store *fakeAccountStore) UpdateEmail(_ context.Context, userID uuid.UUID, email string) (model.Account, error) {
	store.updatedEmail = email
	return model.Account{ID: userID, Email: email, NormalizedEmail: email, Status: model.AccountActive}, nil
}

func TestNormalizeEmailIsCaseInsensitiveAndStrict(t *testing.T) {
	normalized, err := NormalizeEmail("  Person.Name@Example.COM ")
	if err != nil {
		t.Fatal(err)
	}
	if normalized != "person.name@example.com" {
		t.Fatalf("normalized email = %q", normalized)
	}
	for _, invalid := range []string{"", "missing-at.example", "a@b", "two words@example.com"} {
		if _, err = NormalizeEmail(invalid); !errors.Is(err, ErrValidation) {
			t.Errorf("NormalizeEmail(%q) error = %v, want ErrValidation", invalid, err)
		}
	}
}

func TestRegisterCreatesActiveAccountWithoutVerificationDelivery(t *testing.T) {
	store := &fakeAccountStore{}
	service, err := NewAccountService(store)
	if err != nil {
		t.Fatal(err)
	}
	fixedNow := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixedNow }
	userID, sessionID := uuid.New(), uuid.New()
	uuidIndex := 0
	service.newUUID = func() (uuid.UUID, error) {
		uuidIndex++
		if uuidIndex == 1 {
			return userID, nil
		}
		return sessionID, nil
	}
	service.newToken = func() (string, []byte, error) {
		return "registration-session-token", []byte("01234567890123456789012345678901"), nil
	}

	result, err := service.Register(context.Background(), RegisterInput{
		Email: " User@Example.COM ", DisplayName: "  日序用户  ", Password: "long-enough-password", UserAgent: "DayOrder Test Browser",
	})
	if err != nil {
		t.Fatal(err)
	}
	account := result.Account
	if account.Status != model.AccountActive || account.NormalizedEmail != "user@example.com" || account.EmailVerifiedAt != nil {
		t.Fatalf("registered account = %#v", account)
	}
	if store.registration == nil {
		t.Fatal("registration was not persisted")
	}
	registration := store.registration
	if registration.PasswordHash == "long-enough-password" || registration.PasswordHash == "" {
		t.Fatal("registration did not persist a password hash")
	}
	if !account.CreatedAt.Equal(fixedNow) || !account.UpdatedAt.Equal(fixedNow) {
		t.Fatalf("registration timestamps = %s %s", account.CreatedAt, account.UpdatedAt)
	}
	if result.Token != "registration-session-token" || result.Session.ID != sessionID || result.Session.UserID != userID {
		t.Fatalf("registration session result = %#v token %q", result.Session, result.Token)
	}
	if registration.Session.UserAgent != "DayOrder Test Browser" || !registration.Session.ExpiresAt.Equal(fixedNow.Add(sessionDuration)) {
		t.Fatalf("persisted registration session = %#v", registration.Session)
	}
	if string(registration.Session.TokenHash) == result.Token || len(registration.Session.TokenHash) != 32 {
		t.Fatal("registration persisted a plaintext or invalid session token hash")
	}
}

func TestRegisterMapsNormalizedEmailConflict(t *testing.T) {
	store := &fakeAccountStore{createErr: model.ErrConflict}
	service, err := NewAccountService(store)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Register(context.Background(), RegisterInput{
		Email: "used@example.com", DisplayName: "User", Password: "long-enough-password",
	})
	if !errors.Is(err, ErrEmailInUse) {
		t.Fatalf("Register() error = %v, want ErrEmailInUse", err)
	}
}

func TestUpdateEmailKeepsAccountActiveWithoutVerificationDelivery(t *testing.T) {
	store := &fakeAccountStore{}
	accountService, err := NewAccountService(store)
	if err != nil {
		t.Fatal(err)
	}
	current := model.Account{ID: uuid.New(), Email: "old@example.com", Status: model.AccountActive}

	updated, err := accountService.UpdateEmail(context.Background(), current, " New@Example.COM ")
	if err != nil {
		t.Fatal(err)
	}
	if store.updatedEmail != "new@example.com" || updated.Status != model.AccountActive || updated.EmailVerifiedAt != nil {
		t.Fatalf("updated account = %#v stored email = %q", updated, store.updatedEmail)
	}
}

func TestVerifyEmailHashesTokenAndMapsInvalidToken(t *testing.T) {
	store := &fakeAccountStore{consumeErr: model.ErrNotFound}
	service, err := NewAccountService(store)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.VerifyEmail(context.Background(), "verification-token")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("VerifyEmail() error = %v, want ErrInvalidToken", err)
	}
	if string(store.consumeHash) == "verification-token" || len(store.consumeHash) != 32 {
		t.Fatal("verification token was not hashed before persistence lookup")
	}
}
