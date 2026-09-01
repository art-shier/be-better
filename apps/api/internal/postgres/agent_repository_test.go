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
	"dayorder.local/api/internal/service"
	"dayorder.local/api/internal/testdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAgentRepositoryPersistsNestedRunAndEnforcesOwnership(t *testing.T) {
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

	userA, userB := uuid.New(), uuid.New()
	for _, userID := range []uuid.UUID{userA, userB} {
		email := "agent-" + userID.String() + "@example.com"
		if _, err = migrationPool.Exec(ctx, `
INSERT INTO dayorder.users (id, email, normalized_email, display_name, password_hash, status, email_verified_at)
VALUES ($1, $2, $2, 'Agent User', 'test-password-hash', 'active', now())
`, userID, email); err != nil {
			t.Fatal(err)
		}
		if _, err = migrationPool.Exec(ctx, `INSERT INTO dayorder.user_settings (user_id) VALUES ($1)`, userID); err != nil {
			t.Fatal(err)
		}
	}

	transactor, _ := database.NewPoolTransactor(apiPool)
	idempotency, _ := service.NewIdempotencyService(postgresstore.NewIdempotencyRepository())
	syncService, _ := service.NewSyncService(postgresstore.NewSyncRepository(), transactor, []byte("0123456789abcdef0123456789abcdef"))
	auditService, _ := service.NewAuditService(postgresstore.NewAuditRepository())
	commands, _ := service.NewCommandService(transactor, idempotency, syncService, auditService, postgresstore.NewOutboxWriter())
	cursors, _ := service.NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	repository := postgresstore.NewAgentRepository()
	agents, err := service.NewAgentService(repository, transactor, commands, cursors)
	if err != nil {
		t.Fatal(err)
	}

	deviceID := uuid.New()
	if _, err = migrationPool.Exec(ctx, `INSERT INTO dayorder.user_devices (id, user_id, device_name, platform) VALUES ($1, $2, 'Browser', 'web')`, deviceID, userA); err != nil {
		t.Fatal(err)
	}
	goals, _ := service.NewGoalService(postgresstore.NewGoalRepository(), transactor, commands, cursors)
	tasks, _ := service.NewTaskService(postgresstore.NewTaskRepository(), transactor, commands, cursors)
	goal, err := goals.Create(ctx, integrationMutation(userA, deviceID), service.CreateGoalInput{
		Title: "Ship", Area: "Work", MetricType: "project", TargetValue: 1, StartDate: "2026-08-28",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Create(ctx, integrationMutation(userA, deviceID), service.TaskInput{
		Title: "Critical task", Status: "todo", Priority: "important", EstimateMinutes: 45, GoalID: &goal.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := agents.Create(ctx, integrationMutation(userA, deviceID), service.StartAgentInput{
		Intent: "检查尚未安排的任务", ActionMode: "confirm",
		Scope: model.AgentScope{Domains: []string{"tasks"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := agents.Get(ctx, userA, run.ID)
	if err != nil || loaded.ID != run.ID || loaded.Steps == nil || loaded.Changes == nil || loaded.SourceRefs == nil {
		t.Fatalf("loaded run = %#v, %v", loaded, err)
	}
	if _, err = agents.Get(ctx, userB, run.ID); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("cross-user read error = %v", err)
	}
	page, err := agents.List(ctx, userA, "", 20)
	if err != nil || len(page.Runs) != 1 || page.Runs[0].ID != run.ID {
		t.Fatalf("agent page = %#v, %v", page, err)
	}

	workerTransactor, _ := database.NewPoolTransactor(workerPool)
	workerSync, _ := service.NewSyncService(postgresstore.NewSyncRepository(), workerTransactor, []byte("0123456789abcdef0123456789abcdef"))
	workerAudit, _ := service.NewAuditService(postgresstore.NewAuditRepository())
	processor, err := service.NewAgentProcessor(
		postgresstore.NewAgentRepository(), workerTransactor, workerSync, workerAudit,
		service.NewDeterministicAgentProvider(func() time.Time { return time.Date(2026, 8, 29, 8, 15, 0, 0, time.UTC) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = processor.Process(ctx, userA, run.ID); err != nil {
		t.Fatal(err)
	}
	analyzed, err := agents.Get(ctx, userA, run.ID)
	if err != nil || analyzed.Status != "waiting" || len(analyzed.Steps) != 3 || len(analyzed.Changes) != 1 || len(analyzed.SourceRefs) != 1 {
		t.Fatalf("analyzed run = %#v, %v", analyzed, err)
	}
	change := analyzed.Changes[0]
	if change.TargetID == nil || *change.TargetID != task.ID || change.BaseVersion == nil || *change.BaseVersion != task.Version {
		t.Fatalf("version-bound change = %#v", change)
	}
	applied, err := agents.Accept(ctx, integrationMutation(userA, deviceID), change.ID, change.Version)
	if err != nil || applied.Change.Status != "applied" || applied.Run.Status != "completed" {
		t.Fatalf("applied change = %#v, %v", applied, err)
	}
	updatedTask, err := tasks.Get(ctx, userA, task.ID)
	if err != nil || updatedTask.Version != task.Version+1 || updatedTask.ScheduledStart == nil || updatedTask.ScheduledEnd == nil {
		t.Fatalf("updated task = %#v, %v", updatedTask, err)
	}
	if _, err = agents.Accept(ctx, integrationMutation(userA, deviceID), change.ID, change.Version); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("repeated agent accept error = %v", err)
	}
	auditRepository := postgresstore.NewAuditRepository()
	auditQueries, _ := service.NewAuditQueryService(auditRepository, transactor, cursors)
	auditPage, err := auditQueries.List(ctx, userA, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var appliedAudit model.AuditEvent
	for _, event := range auditPage.Events {
		if event.Action == "agent.change.apply" {
			appliedAudit = event
			break
		}
	}
	if appliedAudit.ID == uuid.Nil || !appliedAudit.Undoable {
		t.Fatalf("applied audit event = %#v", appliedAudit)
	}
	undoService, _ := service.NewUndoService(auditRepository, commands)
	undone, err := undoService.Undo(ctx, integrationMutation(userA, deviceID), appliedAudit.ID, updatedTask.Version)
	if err != nil || undone.EntityVersion != updatedTask.Version+1 {
		t.Fatalf("undo result = %#v, %v", undone, err)
	}
	restoredTask, err := tasks.Get(ctx, userA, task.ID)
	if err != nil || restoredTask.ScheduledStart != nil || restoredTask.ScheduledEnd != nil {
		t.Fatalf("restored task = %#v, %v", restoredTask, err)
	}
	var syncCount, auditCount int
	if err = migrationPool.QueryRow(ctx, `SELECT count(*) FROM dayorder.sync_changes WHERE user_id = $1 AND entity_type IN ('agent_run', 'agent_change')`, userA).Scan(&syncCount); err != nil {
		t.Fatal(err)
	}
	if err = migrationPool.QueryRow(ctx, `SELECT count(*) FROM dayorder.audit_events WHERE user_id = $1 AND action LIKE 'agent.%'`, userA).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if syncCount < 5 || auditCount < 4 {
		t.Fatalf("agent transaction evidence sync=%d audit=%d", syncCount, auditCount)
	}
}
