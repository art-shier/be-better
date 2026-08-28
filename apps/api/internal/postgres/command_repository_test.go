package postgres_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dayorder.local/api/internal/database"
	dbmigrations "dayorder.local/api/internal/migrations"
	"dayorder.local/api/internal/model"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/service"
	"dayorder.local/api/internal/testdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCommandRepositoriesAreConcurrentIdempotentAndAtomic(t *testing.T) {
	databaseFixture := testdb.StartForTest(t)
	if err := dbmigrations.Up(databaseFixture.MigrationURL); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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

	userID := uuid.New()
	deviceID := uuid.New()
	if _, err = migrationPool.Exec(ctx, `
INSERT INTO dayorder.users (
    id, email, normalized_email, display_name, password_hash, status, email_verified_at
) VALUES ($1, $2, $2, 'Command Test', 'unused', 'active', now())
`, userID, "command-"+userID.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err = migrationPool.Exec(ctx, `
INSERT INTO dayorder.user_settings (user_id) VALUES ($1)
`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err = migrationPool.Exec(ctx, `
INSERT INTO dayorder.user_devices (id, user_id, device_name, platform)
VALUES ($1, $2, 'integration-test', 'test')
`, deviceID, userID); err != nil {
		t.Fatal(err)
	}

	transactor, err := database.NewPoolTransactor(apiPool)
	if err != nil {
		t.Fatal(err)
	}
	idempotency, _ := service.NewIdempotencyService(postgresstore.NewIdempotencyRepository())
	syncService, _ := service.NewSyncService(
		postgresstore.NewSyncRepository(), transactor,
		[]byte("0123456789abcdef0123456789abcdef"),
	)
	auditService, _ := service.NewAuditService(postgresstore.NewAuditRepository())
	commands, err := service.NewCommandService(
		transactor, idempotency, syncService, auditService, postgresstore.NewOutboxWriter(),
	)
	if err != nil {
		t.Fatal(err)
	}

	entityID := uuid.New()
	requestID := uuid.New()
	request := service.CommandRequest{
		UserID: userID, DeviceID: deviceID, MutationID: uuid.New(), RequestID: requestID,
		CommandName: "task.create", RequestBody: []byte(`{"operation":"create"}`),
	}
	var callbackCalls atomic.Int32
	operation := func(context.Context, database.Tx) (service.CommandResult, error) {
		callbackCalls.Add(1)
		return commandResult(entityID, requestID, uuid.New(), uuid.Nil), nil
	}
	start := make(chan struct{})
	responses := make(chan service.CommandResponse, 2)
	errorsChannel := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			response, executeErr := commands.Execute(ctx, request, operation)
			responses <- response
			errorsChannel <- executeErr
		}()
	}
	close(start)
	group.Wait()
	close(responses)
	close(errorsChannel)
	for executeErr := range errorsChannel {
		if executeErr != nil {
			t.Fatal(executeErr)
		}
	}
	duplicates := 0
	for response := range responses {
		if response.Duplicate {
			duplicates++
		}
	}
	if callbackCalls.Load() != 1 || duplicates != 1 {
		t.Fatalf("callback calls = %d, duplicate responses = %d", callbackCalls.Load(), duplicates)
	}
	assertCommandCounts(t, ctx, migrationPool, request.MutationID, entityID, requestID, 1)

	rollbackEntityID := uuid.New()
	rollbackRequestID := uuid.New()
	duplicateOutboxID := uuid.New()
	rollbackRequest := service.CommandRequest{
		UserID: userID, DeviceID: deviceID, MutationID: uuid.New(), RequestID: rollbackRequestID,
		CommandName: "task.create", RequestBody: []byte(`{"operation":"rollback"}`),
	}
	_, err = commands.Execute(ctx, rollbackRequest, func(ctx context.Context, tx database.Tx) (service.CommandResult, error) {
		if _, updateErr := tx.Exec(ctx, `
UPDATE dayorder.user_settings SET version = version + 1 WHERE user_id = $1
`, userID); updateErr != nil {
			return service.CommandResult{}, updateErr
		}
		return commandResult(rollbackEntityID, rollbackRequestID, duplicateOutboxID, duplicateOutboxID), nil
	})
	if err == nil {
		t.Fatal("duplicate outbox ID should fail the command")
	}
	assertCommandCounts(t, ctx, migrationPool, rollbackRequest.MutationID, rollbackEntityID, rollbackRequestID, 0)
	var settingsVersion int64
	if err = migrationPool.QueryRow(ctx, `
SELECT version FROM dayorder.user_settings WHERE user_id = $1
`, userID).Scan(&settingsVersion); err != nil {
		t.Fatal(err)
	}
	if settingsVersion != 1 {
		t.Fatalf("rolled-back settings version = %d, want 1", settingsVersion)
	}
}

func commandResult(entityID, requestID, firstOutboxID, secondOutboxID uuid.UUID) service.CommandResult {
	result := service.CommandResult{
		Status: 201, Body: []byte(`{"ok":true}`),
		Changes: []model.SyncChangeDraft{{
			EntityType: "task", EntityID: entityID, Operation: "create", EntityVersion: 1,
		}},
		Audits: []model.AuditDraft{{
			Action: "task.create", RequestID: requestID,
			Entities: []model.AuditEntity{{EntityType: "task", EntityID: entityID}},
		}},
		Outbox: []model.OutboxDraft{{
			ID: firstOutboxID, EventType: "task.created", AggregateType: "task",
			AggregateID: entityID, Payload: []byte(`{"ok":true}`),
		}},
	}
	if secondOutboxID != uuid.Nil {
		result.Outbox = append(result.Outbox, model.OutboxDraft{
			ID: secondOutboxID, EventType: "task.index.requested", AggregateType: "task",
			AggregateID: entityID, Payload: []byte(`{"ok":true}`),
		})
	}
	return result
}

func assertCommandCounts(
	t testing.TB,
	ctx context.Context,
	pool *pgxpool.Pool,
	mutationID, entityID, requestID uuid.UUID,
	want int,
) {
	t.Helper()
	queries := []struct {
		name string
		sql  string
		arg  uuid.UUID
	}{
		{"mutation", "SELECT count(*) FROM dayorder.client_mutations WHERE mutation_id = $1", mutationID},
		{"sync", "SELECT count(*) FROM dayorder.sync_changes WHERE entity_id = $1", entityID},
		{"audit", "SELECT count(*) FROM dayorder.audit_events WHERE request_id = $1", requestID},
		{"outbox", "SELECT count(*) FROM dayorder.outbox_events WHERE aggregate_id = $1", entityID},
	}
	for _, query := range queries {
		var count int
		if err := pool.QueryRow(ctx, query.sql, query.arg).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Errorf("%s count = %d, want %d", query.name, count, want)
		}
	}
}
