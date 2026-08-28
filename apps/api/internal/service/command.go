package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type CommandRequest struct {
	UserID      uuid.UUID
	DeviceID    uuid.UUID
	MutationID  uuid.UUID
	RequestID   uuid.UUID
	CommandName string
	RequestBody []byte
}

type MutationContext struct {
	UserID     uuid.UUID
	DeviceID   uuid.UUID
	MutationID uuid.UUID
	RequestID  uuid.UUID
	// Duplicate is optional and lets batch callers distinguish an idempotent
	// replay from the first successful application without changing every
	// resource service return type.
	Duplicate *bool
}

type CommandResult struct {
	Status  int
	Body    []byte
	Changes []model.SyncChangeDraft
	Audits  []model.AuditDraft
	Outbox  []model.OutboxDraft
}

type CommandResponse struct {
	Status    int
	Body      []byte
	Duplicate bool
}

type CommandOperation func(context.Context, database.Tx) (CommandResult, error)

func executeResourceCommand(
	ctx context.Context,
	commands *CommandService,
	mutation MutationContext,
	commandName string,
	requestBody []byte,
	operation CommandOperation,
) (CommandResponse, error) {
	response, err := commands.Execute(ctx, resourceCommand(mutation, commandName, requestBody), operation)
	if err == nil && mutation.Duplicate != nil {
		*mutation.Duplicate = response.Duplicate
	}
	return response, err
}

type CommandSyncWriter interface {
	Record(context.Context, database.Tx, uuid.UUID, []model.SyncChangeDraft) error
}

type CommandAuditWriter interface {
	Record(context.Context, database.Tx, uuid.UUID, []model.AuditDraft) error
}

type CommandOutboxWriter interface {
	Record(context.Context, database.Tx, uuid.UUID, []model.OutboxDraft) error
}

type CommandService struct {
	transactor   UserTransactor
	idempotency  *IdempotencyService
	syncWriter   CommandSyncWriter
	auditWriter  CommandAuditWriter
	outboxWriter CommandOutboxWriter
	now          func() time.Time
	newUUID      func() uuid.UUID
}

func NewCommandService(
	transactor UserTransactor,
	idempotency *IdempotencyService,
	syncWriter CommandSyncWriter,
	auditWriter CommandAuditWriter,
	outboxWriter CommandOutboxWriter,
) (*CommandService, error) {
	if transactor == nil {
		return nil, errors.New("command transactor is required")
	}
	if idempotency == nil {
		return nil, errors.New("command idempotency service is required")
	}
	if syncWriter == nil || auditWriter == nil || outboxWriter == nil {
		return nil, errors.New("command sync, audit, and outbox writers are required")
	}
	return &CommandService{
		transactor: transactor, idempotency: idempotency, syncWriter: syncWriter,
		auditWriter: auditWriter, outboxWriter: outboxWriter,
		now: time.Now, newUUID: uuid.New,
	}, nil
}

func (service *CommandService) Execute(
	ctx context.Context,
	request CommandRequest,
	operation CommandOperation,
) (CommandResponse, error) {
	if service == nil || service.transactor == nil {
		return CommandResponse{}, errors.New("command service is required")
	}
	if request.UserID == uuid.Nil || request.DeviceID == uuid.Nil || request.MutationID == uuid.Nil {
		return CommandResponse{}, fmt.Errorf("%w: command user, device, and mutation IDs are required", ErrValidation)
	}
	request.CommandName = strings.TrimSpace(request.CommandName)
	if utf8.RuneCountInString(request.CommandName) < 1 || utf8.RuneCountInString(request.CommandName) > 120 {
		return CommandResponse{}, fmt.Errorf("%w: command name must contain 1 to 120 characters", ErrValidation)
	}
	if operation == nil {
		return CommandResponse{}, fmt.Errorf("%w: command operation is required", ErrValidation)
	}
	if request.RequestID == uuid.Nil {
		request.RequestID = service.newUUID()
	}
	requestFingerprint := make([]byte, 0, len(request.CommandName)+1+len(request.RequestBody))
	requestFingerprint = append(requestFingerprint, request.CommandName...)
	requestFingerprint = append(requestFingerprint, 0)
	requestFingerprint = append(requestFingerprint, request.RequestBody...)
	var response CommandResponse
	err := service.transactor.WithUser(ctx, request.UserID, func(ctx context.Context, tx database.Tx) error {
		decision, err := service.idempotency.Begin(ctx, tx, MutationKey{
			UserID: request.UserID, DeviceID: request.DeviceID, MutationID: request.MutationID,
		}, requestFingerprint)
		if err != nil {
			return err
		}
		if decision.Replay {
			response = CommandResponse{
				Status: *decision.Mutation.ResponseStatus,
				Body:   bytes.Clone(decision.Mutation.ResponseBody), Duplicate: true,
			}
			return nil
		}

		result, err := operation(ctx, tx)
		if err != nil {
			return err
		}
		if len(result.Changes) == 0 || len(result.Audits) == 0 {
			return fmt.Errorf("%w: mutating command requires sync and audit records", ErrValidation)
		}
		for index := range result.Audits {
			if result.Audits[index].RequestID == uuid.Nil {
				result.Audits[index].RequestID = request.RequestID
			}
		}
		outbox, err := service.prepareOutbox(result.Outbox)
		if err != nil {
			return err
		}
		if err = service.syncWriter.Record(ctx, tx, request.UserID, result.Changes); err != nil {
			return err
		}
		if err = service.auditWriter.Record(ctx, tx, request.UserID, result.Audits); err != nil {
			return err
		}
		if err = service.outboxWriter.Record(ctx, tx, request.UserID, outbox); err != nil {
			return err
		}
		completed, err := service.idempotency.Complete(ctx, tx, decision.Mutation, result.Status, result.Body)
		if err != nil {
			return err
		}
		response = CommandResponse{
			Status: *completed.ResponseStatus, Body: bytes.Clone(completed.ResponseBody),
		}
		return nil
	})
	if err != nil {
		return CommandResponse{}, err
	}
	return response, nil
}

func (service *CommandService) prepareOutbox(drafts []model.OutboxDraft) ([]model.OutboxDraft, error) {
	prepared := make([]model.OutboxDraft, len(drafts))
	for index, draft := range drafts {
		draft.EventType = strings.TrimSpace(draft.EventType)
		draft.AggregateType = strings.TrimSpace(draft.AggregateType)
		if utf8.RuneCountInString(draft.EventType) < 1 || utf8.RuneCountInString(draft.EventType) > 120 {
			return nil, fmt.Errorf("%w: outbox event type must contain 1 to 120 characters", ErrValidation)
		}
		if utf8.RuneCountInString(draft.AggregateType) < 1 || utf8.RuneCountInString(draft.AggregateType) > 32 {
			return nil, fmt.Errorf("%w: outbox aggregate type must contain 1 to 32 characters", ErrValidation)
		}
		if draft.AggregateID == uuid.Nil {
			return nil, fmt.Errorf("%w: outbox aggregate ID is required", ErrValidation)
		}
		if draft.ID == uuid.Nil {
			draft.ID = service.newUUID()
		}
		if draft.AvailableAt.IsZero() {
			draft.AvailableAt = service.now().UTC()
		} else {
			draft.AvailableAt = draft.AvailableAt.UTC()
		}
		if len(draft.Payload) == 0 {
			draft.Payload = json.RawMessage(`{}`)
		}
		var payload map[string]any
		if err := json.Unmarshal(draft.Payload, &payload); err != nil || payload == nil {
			return nil, fmt.Errorf("%w: outbox payload must be a JSON object", ErrValidation)
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode outbox payload: %w", err)
		}
		draft.Payload = encoded
		prepared[index] = draft
	}
	return prepared, nil
}
