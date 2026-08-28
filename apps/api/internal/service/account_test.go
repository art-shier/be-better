package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type fakeAccountStore struct {
	registration  *model.PendingAccountRegistration
	createErr     error
	consumeHash   []byte
	consumeResult model.Account
	consumeErr    error
}

func (store *fakeAccountStore) CreatePendingAccount(_ context.Context, registration model.PendingAccountRegistration) (model.Account, error) {
	store.registration = &registration
	return registration.Account, store.createErr
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
func (store *fakeAccountStore) UpdateEmail(context.Context, uuid.UUID, string, model.AccountTokenDelivery) (model.Account, error) {
	return model.Account{}, nil
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

func TestRegisterCreatesPendingAccountTokenAndVerificationOutbox(t *testing.T) {
	store := &fakeAccountStore{}
	service, err := NewAccountService(store)
	if err != nil {
		t.Fatal(err)
	}
	fixedNow := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixedNow }

	account, err := service.Register(context.Background(), RegisterInput{
		Email: " User@Example.COM ", DisplayName: "  日序用户  ", Password: "long-enough-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	if account.Status != model.AccountPendingVerification || account.NormalizedEmail != "user@example.com" {
		t.Fatalf("registered account = %#v", account)
	}
	if store.registration == nil {
		t.Fatal("pending registration was not persisted")
	}
	registration := store.registration
	if registration.PasswordHash == "long-enough-password" || registration.PasswordHash == "" {
		t.Fatal("registration did not persist a password hash")
	}
	if len(registration.VerificationToken.TokenHash) != 32 || registration.VerificationToken.Purpose != model.TokenVerifyEmail {
		t.Fatalf("verification token = %#v", registration.VerificationToken)
	}
	if registration.VerificationToken.ExpiresAt != fixedNow.Add(24*time.Hour) {
		t.Fatalf("verification token expires at %s", registration.VerificationToken.ExpiresAt)
	}
	var payload struct {
		Token string `json:"token"`
		Email string `json:"email"`
	}
	if err = json.Unmarshal(registration.OutboxPayload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Token == "" || payload.Email != "user@example.com" {
		t.Fatalf("verification outbox payload = %#v", payload)
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
