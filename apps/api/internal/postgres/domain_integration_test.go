package postgres_test

import (
	"context"
	"encoding/json"
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

func TestDomainServicesEnforceIsolationVersionsRelationshipsSearchAndSync(t *testing.T) {
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

	userA, userB := uuid.New(), uuid.New()
	for index, userID := range []uuid.UUID{userA, userB} {
		email := "domain-" + userID.String() + "@example.com"
		if _, err = migrationPool.Exec(ctx, `
INSERT INTO dayorder.users (id, email, normalized_email, display_name, password_hash, status, email_verified_at)
VALUES ($1, $2, $2, $3, 'test-password-hash', 'active', now());
INSERT INTO dayorder.user_settings (user_id) VALUES ($1);
`, userID, email, "Domain User "+string(rune('A'+index))); err != nil {
			t.Fatal(err)
		}
	}

	transactor, _ := database.NewPoolTransactor(apiPool)
	idempotency, _ := service.NewIdempotencyService(postgresstore.NewIdempotencyRepository())
	syncService, _ := service.NewSyncService(postgresstore.NewSyncRepository(), transactor, []byte("0123456789abcdef0123456789abcdef"))
	auditService, _ := service.NewAuditService(postgresstore.NewAuditRepository())
	commands, _ := service.NewCommandService(transactor, idempotency, syncService, auditService, postgresstore.NewOutboxWriter())
	cursors, _ := service.NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	goals, _ := service.NewGoalService(postgresstore.NewGoalRepository(), transactor, commands, cursors)
	tasks, _ := service.NewTaskService(postgresstore.NewTaskRepository(), transactor, commands, cursors)
	content, _ := service.NewContentService(postgresstore.NewContentRepository(), transactor, commands, cursors)
	settings, _ := service.NewSettingsService(postgresstore.NewSettingsRepository(), transactor, commands)
	devices, _ := service.NewDeviceService(postgresstore.NewDeviceRepository(), transactor, auditService)

	deviceA, deviceB := uuid.New(), uuid.New()
	if _, err = devices.Register(ctx, userA, deviceA, service.RegisterDeviceInput{DeviceName: "A", Platform: "web"}); err != nil {
		t.Fatal(err)
	}
	if _, err = devices.Register(ctx, userB, deviceB, service.RegisterDeviceInput{DeviceName: "B", Platform: "web"}); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := syncService.Bootstrap(ctx, userA, deviceA)
	if err != nil {
		t.Fatal(err)
	}

	goal, err := goals.Create(ctx, integrationMutation(userA, deviceA), service.CreateGoalInput{
		Title: "Ship", Area: "Work", MetricType: "project", TargetValue: 1, StartDate: "2026-08-28",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = goals.Get(ctx, userB, goal.ID); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("cross-user goal read error = %v", err)
	}

	task, err := tasks.Create(ctx, integrationMutation(userA, deviceA), service.TaskInput{
		Title: "Task", Status: "todo", Priority: "normal", GoalID: &goal.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	updatedTask, err := tasks.Update(ctx, integrationMutation(userA, deviceA), task.ID, 1, service.TaskInput{
		Title: "Task", Status: "doing", Priority: "normal", GoalID: &goal.ID,
	})
	if err != nil || updatedTask.Version != 2 {
		t.Fatalf("task update = %#v, %v", updatedTask, err)
	}
	if _, err = tasks.Update(ctx, integrationMutation(userA, deviceA), task.ID, 1, service.TaskInput{
		Title: "Stale", Status: "doing", Priority: "normal", GoalID: &goal.ID,
	}); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("stale task update error = %v", err)
	}
	if err = goals.Delete(ctx, integrationMutation(userA, deviceA), goal.ID, goal.Version); err != nil {
		t.Fatal(err)
	}
	detachedTask, err := tasks.Get(ctx, userA, task.ID)
	if err != nil || detachedTask.GoalID != nil || detachedTask.Version != 3 {
		t.Fatalf("detached task = %#v, %v", detachedTask, err)
	}

	noteA, err := content.CreateNote(ctx, integrationMutation(userA, deviceA), service.NoteInput{
		Title: "Private", BodyMarkdown: "alphaunique body", Category: "Work", Tags: []string{"Focus"}, LinkedEntityIDs: []uuid.UUID{task.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = content.CreateNote(ctx, integrationMutation(userB, deviceB), service.NoteInput{
		Title: "Other", BodyMarkdown: "betaunique body", Category: "Work",
	}); err != nil {
		t.Fatal(err)
	}
	searchA, err := content.ListNotes(ctx, userA, "alphaunique", "", 20)
	if err != nil || len(searchA.Notes) != 1 || searchA.Notes[0].ID != noteA.ID || len(searchA.Notes[0].Tags) != 1 || len(searchA.Notes[0].LinkedEntityIDs) != 1 || searchA.Notes[0].LinkedEntityIDs[0] != task.ID {
		t.Fatalf("own note search = %#v, %v", searchA, err)
	}
	if _, err = content.CreateNote(ctx, integrationMutation(userB, deviceB), service.NoteInput{
		Title: "Invalid link", BodyMarkdown: "cross-user link", Category: "Work", LinkedEntityIDs: []uuid.UUID{task.ID},
	}); !errors.Is(err, service.ErrValidation) {
		t.Fatalf("cross-user note link error = %v", err)
	}
	searchOther, err := content.ListNotes(ctx, userA, "betaunique", "", 20)
	if err != nil || len(searchOther.Notes) != 0 {
		t.Fatalf("cross-user note search = %#v, %v", searchOther, err)
	}

	reviewInput := service.ReviewInput{ReviewDate: "2026-08-28", Wins: "Done", TomorrowFocus: "Next"}
	if _, err = content.CreateReview(ctx, integrationMutation(userA, deviceA), reviewInput); err != nil {
		t.Fatal(err)
	}
	if _, err = content.CreateReview(ctx, integrationMutation(userA, deviceA), reviewInput); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("duplicate review error = %v", err)
	}

	updatedSettings, err := settings.Patch(ctx, integrationMutation(userA, deviceA), 1, json.RawMessage(`{"energy":4}`))
	if err != nil || updatedSettings.Version != 2 {
		t.Fatalf("settings update = %#v, %v", updatedSettings, err)
	}
	if _, err = settings.Patch(ctx, integrationMutation(userA, deviceA), 1, json.RawMessage(`{"energy":3}`)); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("stale settings error = %v", err)
	}

	page, err := syncService.DeviceChanges(ctx, userA, deviceA, bootstrap.Cursor, 500)
	if err != nil {
		t.Fatal(err)
	}
	var sawGoalDelete, sawTaskData, sawTaggedNote bool
	for _, change := range page.Changes {
		switch {
		case change.EntityType == "goal" && change.EntityID == goal.ID && change.Operation == "delete" && len(change.Data) == 0:
			sawGoalDelete = true
		case change.EntityType == "task" && change.EntityID == task.ID && len(change.Data) > 0:
			sawTaskData = true
		case change.EntityType == "note" && change.EntityID == noteA.ID && len(change.Data) > 0:
			var note model.Note
			if json.Unmarshal(change.Data, &note) == nil && len(note.Tags) == 1 && len(note.LinkedEntityIDs) == 1 {
				sawTaggedNote = true
			}
		}
	}
	if !sawGoalDelete || !sawTaskData || !sawTaggedNote {
		t.Fatalf("sync evidence goalDelete=%t taskData=%t taggedNote=%t changes=%#v", sawGoalDelete, sawTaskData, sawTaggedNote, page.Changes)
	}
}

func integrationMutation(userID, deviceID uuid.UUID) service.MutationContext {
	return service.MutationContext{
		UserID: userID, DeviceID: deviceID, MutationID: uuid.New(), RequestID: uuid.New(),
	}
}
