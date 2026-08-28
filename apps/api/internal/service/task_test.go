package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type fakeTaskStore struct {
	task      model.Task
	updated   model.Task
	updateErr error
}

func (*fakeTaskStore) CreateTask(context.Context, database.Tx, uuid.UUID, model.Task) (model.Task, error) {
	return model.Task{}, nil
}
func (store *fakeTaskStore) GetTask(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.Task, error) {
	return store.task, nil
}
func (*fakeTaskStore) ListTasks(context.Context, database.Tx, uuid.UUID, *string, *model.ResourcePosition, int) ([]model.Task, error) {
	return nil, nil
}
func (store *fakeTaskStore) UpdateTask(_ context.Context, _ database.Tx, _ uuid.UUID, task model.Task, _ int64) (model.Task, error) {
	store.updated = task
	if store.updateErr != nil {
		return model.Task{}, store.updateErr
	}
	task.Version = store.task.Version + 1
	task.CreatedAt = store.task.CreatedAt
	task.UpdatedAt = time.Now().UTC()
	return task, nil
}
func (*fakeTaskStore) DeleteTask(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64) (model.Task, error) {
	return model.Task{}, nil
}

func TestTaskUpdatePreservesCompletionTimeAndPropagatesVersionConflict(t *testing.T) {
	completedAt := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	taskID := uuid.New()
	store := &fakeTaskStore{task: model.Task{ID: taskID, Title: "Old", Status: "done", Priority: "normal", CompletedAt: &completedAt, Version: 3}}
	commands := testCommandService(t, &recordingSyncWriter{}, &recordingAuditWriter{})
	cursors, _ := NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	service, _ := NewTaskService(store, immediateUserTransactor{tx: &testTransaction{}}, commands, cursors)
	updated, err := service.Update(context.Background(), testMutation(), taskID, 3, TaskInput{Title: "New", Status: "done", Priority: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.CompletedAt == nil || !updated.CompletedAt.Equal(completedAt) {
		t.Fatalf("completedAt = %v", updated.CompletedAt)
	}

	store.updateErr = model.ErrConflict
	commands = testCommandService(t, &recordingSyncWriter{}, &recordingAuditWriter{})
	service, _ = NewTaskService(store, immediateUserTransactor{tx: &testTransaction{}}, commands, cursors)
	_, err = service.Update(context.Background(), testMutation(), taskID, 3, TaskInput{Title: "Again", Status: "done", Priority: "normal"})
	if !errors.Is(err, model.ErrConflict) {
		t.Fatalf("Update() error = %v", err)
	}
}
