package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/database"
	dbmigrations "dayorder.local/api/internal/migrations"
	"dayorder.local/api/internal/model"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/service"
	"dayorder.local/api/internal/testdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAccountRepositoryDirectRegistrationSessionsAndThrottles(t *testing.T) {
	databaseFixture := testdb.StartForTest(t)
	if err := dbmigrations.Up(databaseFixture.MigrationURL); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	apiPool, err := database.Open(ctx, testDatabaseConfig(databaseFixture.APIURL))
	if err != nil {
		t.Fatal(err)
	}
	defer apiPool.Close()
	migrationPool, err := pgxpool.New(ctx, databaseFixture.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer migrationPool.Close()

	repository, err := postgresstore.NewAccountRepository(apiPool)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := service.NewAccountService(repository)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := service.NewSessionService(
		repository, repository, []byte("0123456789abcdef0123456789abcdef"),
	)
	if err != nil {
		t.Fatal(err)
	}

	registration, err := accounts.Register(ctx, service.RegisterInput{
		Email: "User@Example.COM", DisplayName: "User", Password: "initial-password", UserAgent: "integration-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	account := registration.Account
	if account.Status != model.AccountActive || account.EmailVerifiedAt != nil {
		t.Fatalf("directly registered account = %#v", account)
	}
	if _, err = sessions.Authenticate(ctx, registration.Token); err != nil {
		t.Fatalf("authenticate registration session: %v", err)
	}
	if _, err = accounts.Register(ctx, service.RegisterInput{
		Email: " user@example.com ", DisplayName: "Duplicate", Password: "initial-password",
	}); !errors.Is(err, service.ErrEmailInUse) {
		t.Fatalf("case-insensitive duplicate registration error = %v", err)
	}
	var verificationArtifacts int
	if err = migrationPool.QueryRow(ctx, `
SELECT
    (SELECT count(*) FROM dayorder.account_tokens WHERE user_id = $1 AND purpose = 'verify_email')
  + (SELECT count(*) FROM dayorder.outbox_events WHERE user_id = $1 AND event_type = 'email.verification.requested')
`, account.ID).Scan(&verificationArtifacts); err != nil {
		t.Fatal(err)
	}
	if verificationArtifacts != 0 {
		t.Fatalf("direct registration created %d verification artifacts", verificationArtifacts)
	}
	failedUserID := uuid.New()
	_, _, err = repository.CreateRegistration(ctx, model.AccountRegistration{
		Account: model.Account{
			ID: failedUserID, Email: "rollback@example.com", NormalizedEmail: "rollback@example.com",
			DisplayName: "Rollback", Status: model.AccountActive,
		},
		PasswordHash: "password-hash",
		Session: model.NewSession{Session: model.Session{
			ID: uuid.New(), UserID: failedUserID, ExpiresAt: time.Unix(0, 0).UTC(),
		}, TokenHash: []byte("01234567890123456789012345678901")},
	})
	if err == nil {
		t.Fatal("registration with an invalid session unexpectedly succeeded")
	}
	var failedUserCount int
	if err = migrationPool.QueryRow(ctx, `SELECT count(*) FROM dayorder.users WHERE id = $1`, failedUserID).Scan(&failedUserCount); err != nil {
		t.Fatal(err)
	}
	if failedUserCount != 0 {
		t.Fatalf("failed registration left %d user rows", failedUserCount)
	}
	updated, err := accounts.UpdateEmail(ctx, account, " Updated@Example.COM ")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Email != "updated@example.com" || updated.Status != model.AccountActive || updated.EmailVerifiedAt != nil {
		t.Fatalf("directly updated email account = %#v", updated)
	}

	if _, err = sessions.Login(ctx, service.LoginInput{
		Email: account.Email, Password: "initial-password", IP: "203.0.113.10", UserAgent: "integration-test",
	}); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("old email login error = %v, want ErrInvalidCredentials", err)
	}
	login, err := sessions.Login(ctx, service.LoginInput{
		Email: updated.Email, Password: "initial-password", IP: "203.0.113.10", UserAgent: "integration-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	var storedHash []byte
	if err = migrationPool.QueryRow(ctx, `
SELECT token_hash FROM dayorder.sessions WHERE id = $1
`, login.Session.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if string(storedHash) == login.Token || len(storedHash) != 32 {
		t.Fatal("session stored a plaintext or invalid token hash")
	}
	if _, err = sessions.Authenticate(ctx, login.Token); err != nil {
		t.Fatal(err)
	}

	rotated, err := sessions.ChangePassword(ctx, service.ChangePasswordInput{
		Account: account, CurrentPassword: "initial-password", NewPassword: "rotated-password", UserAgent: "integration-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.Authenticate(ctx, login.Token); !errors.Is(err, service.ErrInvalidSession) {
		t.Fatalf("old session after password rotation error = %v", err)
	}
	if _, err = sessions.Authenticate(ctx, rotated.Token); err != nil {
		t.Fatalf("rotated session authentication: %v", err)
	}

	for attempt := 0; attempt < 5; attempt++ {
		_, err = sessions.Login(ctx, service.LoginInput{
			Email: "missing@example.com", Password: "wrong-password", IP: "198.51.100.20",
		})
		if !errors.Is(err, service.ErrInvalidCredentials) {
			t.Fatalf("failed login %d error = %v", attempt+1, err)
		}
	}
	_, err = sessions.Login(ctx, service.LoginInput{
		Email: "missing@example.com", Password: "wrong-password", IP: "198.51.100.20",
	})
	var rateLimit *service.RateLimitError
	if !errors.As(err, &rateLimit) {
		t.Fatalf("sixth failed login error = %v, want rate limit", err)
	}
}

func testDatabaseConfig(databaseURL string) config.DatabaseConfig {
	return config.DatabaseConfig{
		URL: databaseURL, MaxConns: 4, MinConns: 0,
		MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute,
		StatementTimeout: 10 * time.Second, LockTimeout: 3 * time.Second,
		IdleTransactionTimeout: 10 * time.Second, HealthTimeout: 10 * time.Second,
	}
}
