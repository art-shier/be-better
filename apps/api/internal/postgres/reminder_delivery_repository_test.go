package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/database"
	dbmigrations "dayorder.local/api/internal/migrations"
	"dayorder.local/api/internal/model"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/testdb"
	"dayorder.local/api/internal/worker"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReminderDeliveryRepositoryUsesRLSAndRecordsAtomicResult(t *testing.T) {
	databaseFixture := testdb.StartForTest(t)
	if err := dbmigrations.Up(databaseFixture.MigrationURL); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	workerPool, err := database.Open(ctx, testDatabaseConfig(databaseFixture.WorkerURL))
	if err != nil {
		t.Fatal(err)
	}
	defer workerPool.Close()
	migrationPool, err := pgxpool.New(ctx, databaseFixture.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer migrationPool.Close()

	userID := uuid.New()
	otherUserID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	scheduledAt := time.Date(2026, 8, 29, 8, 30, 0, 0, time.UTC)
	startAt := scheduledAt.Add(time.Hour)
	for _, user := range []struct {
		id    uuid.UUID
		email string
	}{{userID, "owner@example.com"}, {otherUserID, "other@example.com"}} {
		if _, err = migrationPool.Exec(ctx, `
INSERT INTO dayorder.users (id, email, normalized_email, display_name, password_hash, status, email_verified_at)
VALUES ($1, $2, $2, '日序用户', 'test-password-hash', 'active', now())
`, user.id, user.email); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = migrationPool.Exec(ctx, `
INSERT INTO dayorder.calendar_events (id, user_id, title, start_at, end_at, timezone, kind)
VALUES ($1, $2, '复盘', $3, $3 + interval '1 hour', 'Asia/Shanghai', 'personal')
`, eventID, userID, startAt); err != nil {
		t.Fatal(err)
	}
	if _, err = migrationPool.Exec(ctx, `
INSERT INTO dayorder.calendar_event_reminders (id, user_id, event_id, offset_minutes, channel, scheduled_at)
VALUES ($1, $2, $3, 60, 'email', $4)
`, reminderID, userID, eventID, scheduledAt); err != nil {
		t.Fatal(err)
	}

	repository, err := postgresstore.NewReminderDeliveryRepository(workerPool)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := repository.Load(ctx, userID, reminderID)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Email != "owner@example.com" || delivery.EventTitle != "复盘" || delivery.Status != "pending" {
		t.Fatalf("delivery = %#v", delivery)
	}
	if _, err = repository.Load(ctx, otherUserID, reminderID); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("cross-user Load() error = %v, want not found", err)
	}

	outboxID := uuid.New()
	if err = repository.RecordResult(ctx, worker.ReminderDeliveryResult{
		UserID: userID, ReminderID: reminderID, EventID: eventID, OutboxEventID: outboxID,
		Channel: "email", ScheduledAt: scheduledAt, Outcome: worker.ReminderDelivered,
	}); err != nil {
		t.Fatal(err)
	}
	var status string
	var attempts int
	var version int64
	if err = migrationPool.QueryRow(ctx, `
SELECT status, attempts, version FROM dayorder.calendar_event_reminders WHERE id = $1
`, reminderID).Scan(&status, &attempts, &version); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" || attempts != 1 || version != 2 {
		t.Fatalf("reminder state = %s/%d/%d", status, attempts, version)
	}
	var syncCount, auditCount int
	if err = migrationPool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM dayorder.sync_changes WHERE user_id = $1 AND entity_type = 'reminder' AND entity_id = $2 AND entity_version = 2),
  (SELECT count(*) FROM dayorder.audit_events AS event
     JOIN dayorder.audit_event_entities AS entity
       ON entity.user_id = event.user_id AND entity.audit_event_id = event.id
    WHERE event.user_id = $1 AND event.request_id = $3 AND event.actor_type = 'system'
      AND entity.entity_type = 'reminder' AND entity.entity_id = $2)
`, userID, reminderID, outboxID).Scan(&syncCount, &auditCount); err != nil {
		t.Fatal(err)
	}
	if syncCount != 1 || auditCount != 1 {
		t.Fatalf("sync/audit counts = %d/%d", syncCount, auditCount)
	}

	if err = repository.RecordResult(ctx, worker.ReminderDeliveryResult{
		UserID: userID, ReminderID: reminderID, EventID: eventID, OutboxEventID: uuid.New(),
		Channel: "email", ScheduledAt: scheduledAt.Add(time.Hour), Outcome: worker.ReminderFailed,
		FailureReason: "email delivery failed",
	}); err != nil {
		t.Fatal(err)
	}
	if err = migrationPool.QueryRow(ctx, `
SELECT status, attempts, version FROM dayorder.calendar_event_reminders WHERE id = $1
`, reminderID).Scan(&status, &attempts, &version); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" || attempts != 1 || version != 2 {
		t.Fatalf("stale result changed reminder state = %s/%d/%d", status, attempts, version)
	}
}
