package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type fakeAgentStore struct {
	created model.AgentRun
	apply   model.AgentApplyResult
	changes []model.AgentChange
}

func (store *fakeAgentStore) CreateRun(_ context.Context, _ database.Tx, _ uuid.UUID, run model.AgentRun) (model.AgentRun, error) {
	run.Version = 1
	run.CreatedAt = time.Now().UTC()
	run.UpdatedAt = run.CreatedAt
	store.created = run
	return run, nil
}

func (store *fakeAgentStore) GetRun(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.AgentRun, error) {
	return store.created, nil
}

func (*fakeAgentStore) ListRuns(context.Context, database.Tx, uuid.UUID, *model.ResourcePosition, int) ([]model.AgentRun, error) {
	return nil, nil
}

func (store *fakeAgentStore) ApplyChange(_ context.Context, _ database.Tx, _ uuid.UUID, changeID uuid.UUID, expectedVersion int64, _ time.Time) (model.AgentApplyResult, error) {
	if store.apply.Change.ID != changeID || store.apply.Change.Version != expectedVersion+1 {
		return model.AgentApplyResult{}, model.ErrConflict
	}
	return store.apply, nil
}

func (*fakeAgentStore) RejectChange(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64, time.Time) (model.AgentApplyResult, error) {
	return model.AgentApplyResult{}, nil
}

func (*fakeAgentStore) StopRun(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64, time.Time) (model.AgentRun, error) {
	return model.AgentRun{}, nil
}

func TestAgentServiceCreatesRunWithTransactionalSyncAuditAndOutbox(t *testing.T) {
	store := &fakeAgentStore{}
	syncWriter := &recordingSyncWriter{}
	auditWriter := &recordingAuditWriter{}
	outboxWriter := &recordingOutboxWriter{}
	idempotency, _ := NewIdempotencyService(&memoryIdempotencyStore{})
	commands, _ := NewCommandService(immediateUserTransactor{tx: &testTransaction{}}, idempotency, syncWriter, auditWriter, outboxWriter)
	cursors, _ := NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	agents, err := NewAgentService(store, immediateUserTransactor{tx: &testTransaction{}}, commands, cursors)
	if err != nil {
		t.Fatal(err)
	}
	created, err := agents.Create(context.Background(), testMutation(), StartAgentInput{
		Intent:     "安排本周尚未完成的高优先级任务",
		ActionMode: "confirm",
		Scope:      model.AgentScope{Domains: []string{"goals", "tasks"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == uuid.Nil || created.Status != "ready" || created.Version != 1 {
		t.Fatalf("created run = %#v", created)
	}
	if len(syncWriter.changes) != 1 || syncWriter.changes[0].EntityType != "agent_run" || syncWriter.changes[0].Operation != "create" {
		t.Fatalf("sync changes = %#v", syncWriter.changes)
	}
	if len(auditWriter.audits) != 1 || auditWriter.audits[0].Action != "agent.run.create" {
		t.Fatalf("audits = %#v", auditWriter.audits)
	}
	if len(outboxWriter.events) != 1 || outboxWriter.events[0].EventType != "agent.run.requested" || outboxWriter.events[0].AggregateID != created.ID {
		t.Fatalf("outbox = %#v", outboxWriter.events)
	}
}

func TestAgentChangePatchWhitelistRejectsArbitraryFieldsAndSQLLikePaths(t *testing.T) {
	taskID := uuid.New()
	base := int64(4)
	for _, test := range []struct {
		name   string
		change model.AgentChangeDraft
	}{
		{name: "unknown field", change: model.AgentChangeDraft{ChangeType: "reschedule-task", TargetType: "task", TargetID: &taskID, BaseVersion: &base, Patch: json.RawMessage(`[{"op":"replace","path":"/title","value":"owned"}]`)}},
		{name: "SQL-looking path", change: model.AgentChangeDraft{ChangeType: "reschedule-task", TargetType: "task", TargetID: &taskID, BaseVersion: &base, Patch: json.RawMessage(`[{"op":"replace","path":"/scheduledStart;DROP TABLE tasks","value":"2026-08-29T09:00:00Z"}]`)}},
		{name: "wrong target", change: model.AgentChangeDraft{ChangeType: "archive-record", TargetType: "note", TargetID: &taskID, BaseVersion: &base, Patch: json.RawMessage(`[{"op":"replace","path":"/archivedAt","value":"2026-08-29T09:00:00Z"}]`)}},
		{name: "missing version", change: model.AgentChangeDraft{ChangeType: "reschedule-task", TargetType: "task", TargetID: &taskID, Patch: json.RawMessage(`[{"op":"replace","path":"/scheduledStart","value":"2026-08-29T09:00:00Z"}]`)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateAgentChange(test.change); !errors.Is(err, ErrValidation) {
				t.Fatalf("validateAgentChange() error = %v", err)
			}
		})
	}
	valid := model.AgentChangeDraft{
		ChangeType: "reschedule-task", TargetType: "task", TargetID: &taskID, BaseVersion: &base,
		Patch:  json.RawMessage(`[{"op":"replace","path":"/scheduledStart","value":"2026-08-29T09:00:00Z"},{"op":"replace","path":"/scheduledEnd","value":"2026-08-29T10:00:00Z"}]`),
		Reason: "为高优先级目标留出连续时间",
	}
	if err := validateAgentChange(valid); err != nil {
		t.Fatalf("valid change rejected: %v", err)
	}
}

func TestAgentAcceptEmitsTargetAndAgentChangesInOneCommand(t *testing.T) {
	changeID, runID, taskID := uuid.New(), uuid.New(), uuid.New()
	store := &fakeAgentStore{apply: model.AgentApplyResult{
		Change: model.AgentChange{ID: changeID, RunID: runID, ChangeType: "reschedule-task", TargetType: "task", Status: "applied", Version: 2},
		Run:    model.AgentRun{ID: runID, Status: "completed", Version: 4}, RunUpdated: true,
		TargetType: "task", TargetID: taskID, TargetOperation: "update", TargetVersion: 8,
		BeforeData: json.RawMessage(`{"id":"` + taskID.String() + `","version":7}`),
		AfterData:  json.RawMessage(`{"id":"` + taskID.String() + `","version":8}`),
	}}
	syncWriter := &recordingSyncWriter{}
	auditWriter := &recordingAuditWriter{}
	commands := testCommandService(t, syncWriter, auditWriter)
	cursors, _ := NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	agents, _ := NewAgentService(store, immediateUserTransactor{tx: &testTransaction{}}, commands, cursors)

	result, err := agents.Accept(context.Background(), testMutation(), changeID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Change.Status != "applied" || len(syncWriter.changes) != 3 {
		t.Fatalf("result=%#v sync=%#v", result, syncWriter.changes)
	}
	if syncWriter.changes[0].EntityType != "task" || syncWriter.changes[1].EntityType != "agent_change" || syncWriter.changes[2].EntityType != "agent_run" {
		t.Fatalf("sync changes = %#v", syncWriter.changes)
	}
	if len(auditWriter.audits) != 1 || auditWriter.audits[0].ActorType != "agent" || auditWriter.audits[0].ActorID == nil || *auditWriter.audits[0].ActorID != runID {
		t.Fatalf("audit = %#v", auditWriter.audits)
	}
}
