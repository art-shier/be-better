package migrations

import (
	"context"
	"testing"
	"time"

	"dayorder.local/api/internal/testdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUpgradeFromPreviousMigrationVersion(t *testing.T) {
	database := testdb.StartForTest(t)
	runner, err := open(database.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Steps(int(LatestVersion - 1)); err != nil {
		_, _ = runner.Close()
		t.Fatalf("apply previous migration versions: %v", err)
	}
	if sourceErr, databaseErr := runner.Close(); sourceErr != nil || databaseErr != nil {
		t.Fatalf("close previous-version migrator: source=%v database=%v", sourceErr, databaseErr)
	}

	version, dirty, exists, err := CurrentVersion(database.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || dirty || version != LatestVersion-1 {
		t.Fatalf("previous schema state = version %d, dirty %t, exists %t", version, dirty, exists)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, database.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID, tokenID := uuid.New(), uuid.New()
	verificationOutboxID, resetOutboxID, agentOutboxID, lockToken := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err = pool.Exec(ctx, `
INSERT INTO dayorder.users (id, email, normalized_email, display_name, password_hash, status)
VALUES ($1, 'legacy@example.com', 'legacy@example.com', 'Legacy', 'password-hash', 'pending_verification');
INSERT INTO dayorder.user_settings (user_id) VALUES ($1);
INSERT INTO dayorder.account_tokens (id, user_id, purpose, token_hash, expires_at)
VALUES ($2, $1, 'verify_email', decode(repeat('11', 32), 'hex'), now() + interval '1 hour');
INSERT INTO dayorder.outbox_events (
    id, user_id, event_type, aggregate_type, aggregate_id, payload, status, locked_at, lock_token
) VALUES
    ($3, $1, 'email.verification.requested', 'user', $1, '{"token":"legacy-verification-secret"}'::jsonb, 'processing', now(), $6),
    ($4, $1, 'email.password_reset.requested', 'user', $1, '{"token":"legacy-reset-secret"}'::jsonb, 'dead', NULL, NULL),
    ($5, $1, 'agent.run.requested', 'agent_run', $1, '{"intent":"legacy-agent-request"}'::jsonb, 'pending', NULL, NULL);
`, userID, tokenID, verificationOutboxID, resetOutboxID, agentOutboxID, lockToken); err != nil {
		t.Fatal(err)
	}
	if err = Up(database.MigrationURL); err != nil {
		t.Fatalf("upgrade previous schema to latest: %v", err)
	}
	if err = RequireCurrent(database.MigrationURL); err != nil {
		t.Fatal(err)
	}
	var status string
	var verifiedAt *time.Time
	var tokenConsumed bool
	var sanitizedOutboxCount int
	if err = pool.QueryRow(ctx, `
SELECT account.status, account.email_verified_at,
       token.consumed_at IS NOT NULL,
       (SELECT count(*)
          FROM dayorder.outbox_events AS event
         WHERE event.id IN ($3, $4, $5)
           AND event.status = 'processed'
           AND event.payload = '{}'::jsonb
           AND event.locked_at IS NULL
           AND event.lock_token IS NULL
           AND event.processed_at IS NOT NULL)
FROM dayorder.users AS account
JOIN dayorder.account_tokens AS token ON token.user_id = account.id AND token.id = $2
WHERE account.id = $1
`, userID, tokenID, verificationOutboxID, resetOutboxID, agentOutboxID).Scan(&status, &verifiedAt, &tokenConsumed, &sanitizedOutboxCount); err != nil {
		t.Fatal(err)
	}
	if status != "active" || verifiedAt != nil || !tokenConsumed || sanitizedOutboxCount != 3 {
		t.Fatalf("upgraded account state = status %q verified %v tokenConsumed %t sanitizedOutboxCount %d", status, verifiedAt, tokenConsumed, sanitizedOutboxCount)
	}

	rollbackUserID, rollbackTokenID, rollbackOutboxID := uuid.New(), uuid.New(), uuid.New()
	if _, err = pool.Exec(ctx, `
INSERT INTO dayorder.users (id, email, normalized_email, display_name, password_hash, status)
VALUES ($1, 'rollback@example.com', 'rollback@example.com', 'Rollback', 'password-hash', 'pending_verification');
INSERT INTO dayorder.user_settings (user_id) VALUES ($1);
INSERT INTO dayorder.account_tokens (id, user_id, purpose, token_hash, expires_at)
VALUES ($2, $1, 'verify_email', decode(repeat('22', 32), 'hex'), now() + interval '1 hour');
INSERT INTO dayorder.outbox_events (id, user_id, event_type, aggregate_type, aggregate_id, payload)
VALUES ($3, $1, 'email.verification.requested', 'user', $1, '{"token":"rollback-secret"}'::jsonb);
`, rollbackUserID, rollbackTokenID, rollbackOutboxID); err != nil {
		t.Fatal(err)
	}
	if err = Up(database.MigrationURL); err != nil {
		t.Fatalf("rerun migration reconciliation after rollback writes: %v", err)
	}
	var rollbackStatus, rollbackPayload, rollbackOutboxStatus string
	var rollbackTokenConsumed bool
	if err = pool.QueryRow(ctx, `
SELECT account.status, token.consumed_at IS NOT NULL, event.status, event.payload::text
FROM dayorder.users AS account
JOIN dayorder.account_tokens AS token ON token.user_id = account.id AND token.id = $2
JOIN dayorder.outbox_events AS event ON event.user_id = account.id AND event.id = $3
WHERE account.id = $1
`, rollbackUserID, rollbackTokenID, rollbackOutboxID).Scan(
		&rollbackStatus, &rollbackTokenConsumed, &rollbackOutboxStatus, &rollbackPayload,
	); err != nil {
		t.Fatal(err)
	}
	if rollbackStatus != "active" || !rollbackTokenConsumed || rollbackOutboxStatus != "processed" || rollbackPayload != "{}" {
		t.Fatalf("reconciled rollback state = account %q tokenConsumed %t outbox %q payload %q", rollbackStatus, rollbackTokenConsumed, rollbackOutboxStatus, rollbackPayload)
	}
}
