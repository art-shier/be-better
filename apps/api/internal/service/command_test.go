package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type testTransaction struct{}

func (*testTransaction) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("OK"), nil
}
func (*testTransaction) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (*testTransaction) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (*testTransaction) Commit(context.Context) error                            { return nil }
func (*testTransaction) Rollback(context.Context) error                          { return nil }

type immediateUserTransactor struct{ tx database.Tx }

func (transactor immediateUserTransactor) WithUser(ctx context.Context, _ uuid.UUID, operation func(context.Context, database.Tx) error) error {
	return operation(ctx, transactor.tx)
}

type recordingSyncWriter struct {
	tx      database.Tx
	changes []model.SyncChangeDraft
	err     error
}

func (writer *recordingSyncWriter) Record(_ context.Context, tx database.Tx, _ uuid.UUID, changes []model.SyncChangeDraft) error {
	writer.tx = tx
	writer.changes = append(writer.changes, changes...)
	return writer.err
}

type recordingAuditWriter struct {
	tx     database.Tx
	audits []model.AuditDraft
	err    error
}

func (writer *recordingAuditWriter) Record(_ context.Context, tx database.Tx, _ uuid.UUID, audits []model.AuditDraft) error {
	writer.tx = tx
	writer.audits = append(writer.audits, audits...)
	return writer.err
}

type recordingOutboxWriter struct {
	tx     database.Tx
	events []model.OutboxDraft
}

func (writer *recordingOutboxWriter) Record(_ context.Context, tx database.Tx, _ uuid.UUID, events []model.OutboxDraft) error {
	writer.tx = tx
	writer.events = append(writer.events, events...)
	return nil
}

func TestCommandServiceWritesAllPrimitivesInOneTransactionAndReplays(t *testing.T) {
	tx := &testTransaction{}
	idempotency, _ := NewIdempotencyService(&memoryIdempotencyStore{})
	syncWriter := &recordingSyncWriter{}
	auditWriter := &recordingAuditWriter{}
	outboxWriter := &recordingOutboxWriter{}
	commands, err := NewCommandService(immediateUserTransactor{tx: tx}, idempotency, syncWriter, auditWriter, outboxWriter)
	if err != nil {
		t.Fatal(err)
	}
	entityID := uuid.New()
	request := CommandRequest{
		UserID: uuid.New(), DeviceID: uuid.New(), MutationID: uuid.New(), RequestID: uuid.New(),
		CommandName: "task.create", RequestBody: []byte(`{"title":"ship"}`),
	}
	callbackCalls := 0
	operation := func(context.Context, database.Tx) (CommandResult, error) {
		callbackCalls++
		return CommandResult{
			Status: 201, Body: []byte(`{"id":"created"}`),
			Changes: []model.SyncChangeDraft{{EntityType: "task", EntityID: entityID, Operation: "create", EntityVersion: 1}},
			Audits:  []model.AuditDraft{{Action: "task.create", Entities: []model.AuditEntity{{EntityType: "task", EntityID: entityID}}}},
			Outbox:  []model.OutboxDraft{{EventType: "task.created", AggregateType: "task", AggregateID: entityID, Payload: []byte(`{"id":"created"}`)}},
		}, nil
	}

	first, err := commands.Execute(context.Background(), request, operation)
	if err != nil || first.Duplicate || first.Status != 201 {
		t.Fatalf("first Execute() = %#v, %v", first, err)
	}
	second, err := commands.Execute(context.Background(), request, operation)
	if err != nil || !second.Duplicate || second.Status != first.Status || string(second.Body) != string(first.Body) {
		t.Fatalf("second Execute() = %#v, %v", second, err)
	}
	if callbackCalls != 1 {
		t.Fatalf("callback calls = %d, want 1", callbackCalls)
	}
	if syncWriter.tx != tx || auditWriter.tx != tx || outboxWriter.tx != tx {
		t.Fatal("command primitives did not receive the business transaction")
	}
	if len(syncWriter.changes) != 1 || len(auditWriter.audits) != 1 || len(outboxWriter.events) != 1 {
		t.Fatalf("writes = sync %d audit %d outbox %d", len(syncWriter.changes), len(auditWriter.audits), len(outboxWriter.events))
	}
	if auditWriter.audits[0].RequestID != request.RequestID {
		t.Fatalf("audit request ID = %s, want %s", auditWriter.audits[0].RequestID, request.RequestID)
	}
	conflictingRequest := request
	conflictingRequest.CommandName = "goal.create"
	if _, err = commands.Execute(context.Background(), conflictingRequest, operation); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("cross-command mutation reuse error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestCommandServiceStopsBeforeIdempotencyCompletionWhenPrimitiveFails(t *testing.T) {
	store := &memoryIdempotencyStore{}
	idempotency, _ := NewIdempotencyService(store)
	writeError := errors.New("audit unavailable")
	commands, _ := NewCommandService(
		immediateUserTransactor{tx: &testTransaction{}}, idempotency,
		&recordingSyncWriter{}, &recordingAuditWriter{err: writeError}, &recordingOutboxWriter{},
	)
	request := CommandRequest{
		UserID: uuid.New(), DeviceID: uuid.New(), MutationID: uuid.New(), RequestID: uuid.New(),
		CommandName: "task.update", RequestBody: []byte(`{}`),
	}
	_, err := commands.Execute(context.Background(), request, func(context.Context, database.Tx) (CommandResult, error) {
		return CommandResult{
			Status: 200, Body: []byte(`{"ok":true}`),
			Changes: []model.SyncChangeDraft{{EntityType: "task", EntityID: uuid.New(), Operation: "update", EntityVersion: 2}},
			Audits:  []model.AuditDraft{{Action: "task.update"}},
		}, nil
	})
	if !errors.Is(err, writeError) {
		t.Fatalf("Execute() error = %v, want audit failure", err)
	}
	if store.complete != 0 {
		t.Fatalf("idempotency completion count = %d, want 0", store.complete)
	}
}

func TestCommandServiceRejectsInvalidIdentityBeforeStartingTransaction(t *testing.T) {
	store := &memoryIdempotencyStore{}
	idempotency, _ := NewIdempotencyService(store)
	commands, _ := NewCommandService(
		immediateUserTransactor{tx: &testTransaction{}}, idempotency,
		&recordingSyncWriter{}, &recordingAuditWriter{}, &recordingOutboxWriter{},
	)
	_, err := commands.Execute(context.Background(), CommandRequest{}, func(context.Context, database.Tx) (CommandResult, error) {
		return CommandResult{}, nil
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Execute() error = %v, want validation", err)
	}
	if !reflect.DeepEqual(store, &memoryIdempotencyStore{}) {
		t.Fatal("invalid command reached idempotency storage")
	}
}
