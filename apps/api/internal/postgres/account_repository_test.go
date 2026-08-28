package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/database"
	dbmigrations "dayorder.local/api/internal/migrations"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/service"
	"dayorder.local/api/internal/testdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAccountRepositoryRegistrationVerificationSessionsAndThrottles(t *testing.T) {
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

	account, err := accounts.Register(ctx, service.RegisterInput{
		Email: "User@Example.COM", DisplayName: "User", Password: "initial-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = accounts.Register(ctx, service.RegisterInput{
		Email: " user@example.com ", DisplayName: "Duplicate", Password: "initial-password",
	}); !errors.Is(err, service.ErrEmailInUse) {
		t.Fatalf("case-insensitive duplicate registration error = %v", err)
	}
	if _, err = sessions.Login(ctx, service.LoginInput{
		Email: account.Email, Password: "initial-password", IP: "203.0.113.10",
	}); !errors.Is(err, service.ErrAccountNotActive) {
		t.Fatalf("pending-account login error = %v", err)
	}

	verificationToken := outboxToken(t, ctx, migrationPool, account.ID.String())
	workerPool, err := database.Open(ctx, testDatabaseConfig(databaseFixture.WorkerURL))
	if err != nil {
		t.Fatal(err)
	}
	defer workerPool.Close()
	outboxRepository, err := postgresstore.NewOutboxRepository(workerPool)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := outboxRepository.Claim(ctx, 10, uuid.New(), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].EventType != "email.verification.requested" {
		t.Fatalf("claimed verification events = %#v", claimed)
	}
	if err = outboxRepository.Complete(ctx, claimed[0].ID, claimed[0].LockToken); err != nil {
		t.Fatal(err)
	}
	var scrubbedPayload string
	if err = migrationPool.QueryRow(ctx, "SELECT payload::text FROM dayorder.outbox_events WHERE id = $1", claimed[0].ID).Scan(&scrubbedPayload); err != nil {
		t.Fatal(err)
	}
	if scrubbedPayload != "{}" {
		t.Fatalf("processed verification payload = %q, want scrubbed object", scrubbedPayload)
	}
	verified, err := accounts.VerifyEmail(ctx, verificationToken)
	if err != nil {
		t.Fatal(err)
	}
	if verified.EmailVerifiedAt == nil {
		t.Fatal("verified account has no verification timestamp")
	}
	if _, err = accounts.VerifyEmail(ctx, verificationToken); !errors.Is(err, service.ErrInvalidToken) {
		t.Fatalf("reused verification token error = %v", err)
	}

	login, err := sessions.Login(ctx, service.LoginInput{
		Email: account.Email, Password: "initial-password", IP: "203.0.113.10", UserAgent: "integration-test",
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
		Account: verified, CurrentPassword: "initial-password", NewPassword: "rotated-password", UserAgent: "integration-test",
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

func outboxToken(t testing.TB, ctx context.Context, pool *pgxpool.Pool, userID string) string {
	t.Helper()
	var token string
	if err := pool.QueryRow(ctx, `
SELECT payload ->> 'token'
FROM dayorder.outbox_events
WHERE user_id = $1::uuid AND event_type = 'email.verification.requested'
ORDER BY created_at DESC
LIMIT 1
`, userID).Scan(&token); err != nil {
		t.Fatal(err)
	}
	return token
}

func testDatabaseConfig(databaseURL string) config.DatabaseConfig {
	return config.DatabaseConfig{
		URL: databaseURL, MaxConns: 4, MinConns: 0,
		MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute,
		StatementTimeout: 10 * time.Second, LockTimeout: 3 * time.Second,
		IdleTransactionTimeout: 10 * time.Second, HealthTimeout: 10 * time.Second,
	}
}
