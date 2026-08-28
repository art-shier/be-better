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

type fakeGoalStore struct {
	goal       model.Goal
	created    int
	deleteGoal model.Goal
	milestones []model.GoalMilestone
	tasks      []model.Task
	deleteErr  error
}

func (store *fakeGoalStore) CreateGoal(_ context.Context, _ database.Tx, _ uuid.UUID, goal model.Goal) (model.Goal, error) {
	store.created++
	goal.Version = 1
	goal.CreatedAt = time.Now().UTC()
	goal.UpdatedAt = goal.CreatedAt
	store.goal = goal
	return goal, nil
}
func (store *fakeGoalStore) GetGoal(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.Goal, error) {
	return store.goal, nil
}
func (*fakeGoalStore) ListGoals(context.Context, database.Tx, uuid.UUID, *model.ResourcePosition, int) ([]model.Goal, error) {
	return nil, nil
}
func (*fakeGoalStore) UpdateGoal(context.Context, database.Tx, uuid.UUID, model.Goal, int64) (model.Goal, error) {
	return model.Goal{}, nil
}
func (store *fakeGoalStore) DeleteGoal(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64) (model.Goal, []model.GoalMilestone, []model.Task, error) {
	return store.deleteGoal, store.milestones, store.tasks, store.deleteErr
}
func (*fakeGoalStore) CreateMilestone(context.Context, database.Tx, uuid.UUID, model.GoalMilestone) (model.GoalMilestone, error) {
	return model.GoalMilestone{}, nil
}
func (*fakeGoalStore) GetMilestone(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.GoalMilestone, error) {
	return model.GoalMilestone{}, nil
}
func (*fakeGoalStore) ListMilestones(context.Context, database.Tx, uuid.UUID, uuid.UUID) ([]model.GoalMilestone, error) {
	return nil, nil
}
func (*fakeGoalStore) UpdateMilestone(context.Context, database.Tx, uuid.UUID, model.GoalMilestone, int64) (model.GoalMilestone, error) {
	return model.GoalMilestone{}, nil
}
func (*fakeGoalStore) DeleteMilestone(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64) (model.GoalMilestone, error) {
	return model.GoalMilestone{}, nil
}

func TestGoalServiceValidatesBeforeWritingAndCreatesTransactionalRecords(t *testing.T) {
	store := &fakeGoalStore{}
	syncWriter := &recordingSyncWriter{}
	auditWriter := &recordingAuditWriter{}
	commands := testCommandService(t, syncWriter, auditWriter)
	cursors, _ := NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	service, _ := NewGoalService(store, immediateUserTransactor{tx: &testTransaction{}}, commands, cursors)
	mutation := testMutation()
	if _, err := service.Create(context.Background(), mutation, CreateGoalInput{}); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid Create() error = %v", err)
	}
	input := CreateGoalInput{Title: "Ship", Area: "Work", MetricType: "project", TargetValue: 1, StartDate: "2026-08-28"}
	created, err := service.Create(context.Background(), mutation, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || store.created != 1 || len(syncWriter.changes) != 1 || len(auditWriter.audits) != 1 {
		t.Fatalf("created=%#v store=%d sync=%d audit=%d", created, store.created, len(syncWriter.changes), len(auditWriter.audits))
	}
}

func TestGoalDeleteEmitsChangesForMilestonesAndDetachedTasks(t *testing.T) {
	goalID, milestoneID, taskID := uuid.New(), uuid.New(), uuid.New()
	store := &fakeGoalStore{
		goal: model.Goal{ID: goalID, Version: 1}, deleteGoal: model.Goal{ID: goalID, Version: 2},
		milestones: []model.GoalMilestone{{ID: milestoneID, Version: 2}},
		tasks:      []model.Task{{ID: taskID, Version: 4}},
	}
	syncWriter := &recordingSyncWriter{}
	auditWriter := &recordingAuditWriter{}
	commands := testCommandService(t, syncWriter, auditWriter)
	cursors, _ := NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	service, _ := NewGoalService(store, immediateUserTransactor{tx: &testTransaction{}}, commands, cursors)
	if err := service.Delete(context.Background(), testMutation(), goalID, 1); err != nil {
		t.Fatal(err)
	}
	if len(syncWriter.changes) != 3 || syncWriter.changes[0].Operation != "delete" || syncWriter.changes[2].Operation != "update" {
		t.Fatalf("sync changes = %#v", syncWriter.changes)
	}
	if len(auditWriter.audits) != 1 || len(auditWriter.audits[0].Entities) != 3 {
		t.Fatalf("audit = %#v", auditWriter.audits)
	}
}

func testCommandService(t testing.TB, syncWriter CommandSyncWriter, auditWriter CommandAuditWriter) *CommandService {
	t.Helper()
	idempotency, _ := NewIdempotencyService(&memoryIdempotencyStore{})
	commands, err := NewCommandService(immediateUserTransactor{tx: &testTransaction{}}, idempotency, syncWriter, auditWriter, &recordingOutboxWriter{})
	if err != nil {
		t.Fatal(err)
	}
	return commands
}

func testMutation() MutationContext {
	return MutationContext{UserID: uuid.New(), DeviceID: uuid.New(), MutationID: uuid.New(), RequestID: uuid.New()}
}
