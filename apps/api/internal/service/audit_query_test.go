package service

import (
	"context"
	"testing"
	"time"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type fakeAuditReadStore struct{ events []model.AuditEvent }

func (store *fakeAuditReadStore) Get(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.AuditEvent, error) {
	return store.events[0], nil
}

func (store *fakeAuditReadStore) List(context.Context, database.Tx, uuid.UUID, *model.ResourcePosition, int) ([]model.AuditEvent, error) {
	return store.events, nil
}

func TestAuditQueryServiceMarksOnlySupportedAppendOnlyEventsUndoable(t *testing.T) {
	userID, eventID, taskID := uuid.New(), uuid.New(), uuid.New()
	createdAt := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	store := &fakeAuditReadStore{events: []model.AuditEvent{{
		ID: eventID, ActorType: "agent", Action: "agent.change.apply", CreatedAt: createdAt,
		BeforeData: []byte(`{"id":"` + taskID.String() + `","version":1}`),
		AfterData:  []byte(`{"id":"` + taskID.String() + `","version":2}`),
		Entities:   []model.AuditEntity{{EntityType: "task", EntityID: taskID}, {EntityType: "agent_run", EntityID: uuid.New()}},
	}}}
	cursors, _ := NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	queries, err := NewAuditQueryService(store, immediateUserTransactor{tx: &testTransaction{}}, cursors)
	if err != nil {
		t.Fatal(err)
	}
	page, err := queries.List(context.Background(), userID, "", 20)
	if err != nil || len(page.Events) != 1 || !page.Events[0].Undoable {
		t.Fatalf("audit page = %#v, %v", page, err)
	}
	store.events[0].Action = "task.update"
	event, err := queries.Get(context.Background(), userID, eventID)
	if err != nil || event.Undoable {
		t.Fatalf("ordinary audit event = %#v, %v", event, err)
	}
}

type fakeUndoStore struct{ result model.UndoResult }

func (store *fakeUndoStore) ApplyUndo(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64, time.Time) (model.UndoResult, error) {
	return store.result, nil
}

func TestUndoServiceCreatesNewVersionedSyncAndAuditCommand(t *testing.T) {
	auditID, taskID := uuid.New(), uuid.New()
	store := &fakeUndoStore{result: model.UndoResult{
		OriginalAuditID: auditID, EntityType: "task", EntityID: taskID, EntityOperation: "update", EntityVersion: 4,
		Data:       []byte(`{"id":"` + taskID.String() + `","version":4}`),
		BeforeData: []byte(`{"id":"` + taskID.String() + `","version":3}`),
		AfterData:  []byte(`{"id":"` + taskID.String() + `","version":4}`),
	}}
	syncWriter := &recordingSyncWriter{}
	auditWriter := &recordingAuditWriter{}
	commands := testCommandService(t, syncWriter, auditWriter)
	undos, err := NewUndoService(store, commands)
	if err != nil {
		t.Fatal(err)
	}
	result, err := undos.Undo(context.Background(), testMutation(), auditID, 3)
	if err != nil || result.EntityVersion != 4 {
		t.Fatalf("Undo() = %#v, %v", result, err)
	}
	if len(syncWriter.changes) != 1 || syncWriter.changes[0].EntityType != "task" || syncWriter.changes[0].EntityVersion != 4 {
		t.Fatalf("sync changes = %#v", syncWriter.changes)
	}
	if len(auditWriter.audits) != 1 || auditWriter.audits[0].Action != "audit.undo" {
		t.Fatalf("audits = %#v", auditWriter.audits)
	}
}
