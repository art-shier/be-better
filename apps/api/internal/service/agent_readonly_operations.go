package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/canonicaljson"
	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

const (
	readonlyProviderTurnLimit     = 9
	readonlyCalendarReadLimit     = 8
	readonlyCalendarAttemptLimit  = 32
	readonlyOperationAttemptLimit = 2
)

func (service *AgentReadonlyService) Authorize(ctx context.Context, actor agentexecution.Actor, runID uuid.UUID) (agentexecution.Record, error) {
	if service == nil || runID == uuid.Nil {
		return agentexecution.Record{}, fmt.Errorf("%w: readonly run ID is required", ErrValidation)
	}
	if err := validateReadonlyActor(actor); err != nil {
		return agentexecution.Record{}, err
	}
	var record agentexecution.Record
	var authorizationErr error
	var terminal *agentexecution.Record
	err := service.transactor.WithUser(ctx, actor.UserID, func(ctx context.Context, tx database.Tx) error {
		var err error
		record, err = service.store.Get(ctx, tx, actor.UserID, runID, true)
		if err != nil {
			return err
		}
		now := service.now().UTC().Truncate(time.Microsecond)
		if err = authorizeReadonlyActor(record, actor); err != nil {
			return err
		}
		if isReadonlyTerminal(record.Run.Status) {
			return model.ErrConflict
		}
		if deadlineExpired(record, now) {
			expectedVersion := record.Run.Version
			timeoutReadonlyRecord(&record, now)
			if err = service.saveReadonlyTransition(ctx, tx, &record, expectedVersion, actor.Token, "agent.readonly.timeout", "system"); err != nil {
				return err
			}
			terminal = &record
			authorizationErr = model.ErrConflict
		}
		return nil
	})
	if err != nil {
		return agentexecution.Record{}, err
	}
	if terminal != nil {
		service.observeRunTerminal(*terminal)
	}
	return record, authorizationErr
}

func (service *AgentReadonlyService) BeginOperation(ctx context.Context, actor agentexecution.Actor, runID uuid.UUID, kind, id string, canonicalPayload []byte, reservedTokens int) (agentexecution.Operation, error) {
	if service == nil || runID == uuid.Nil {
		return agentexecution.Operation{}, fmt.Errorf("%w: readonly run ID is required", ErrValidation)
	}
	if err := validateReadonlyActor(actor); err != nil {
		return agentexecution.Operation{}, err
	}
	if kind != "provider_turn" && kind != "calendar_read" {
		return agentexecution.Operation{}, readonlyOperationValidation("readonly operation kind is invalid")
	}
	if id == "" || strings.TrimSpace(id) != id || len(id) > 240 {
		return agentexecution.Operation{}, readonlyOperationValidation("readonly operation ID is invalid")
	}
	if reservedTokens < 0 {
		return agentexecution.Operation{}, readonlyOperationValidation("readonly operation token reservation is invalid")
	}
	if kind == "calendar_read" && reservedTokens != 0 {
		return agentexecution.Operation{}, readonlyOperationValidation("calendar reads cannot reserve model tokens")
	}
	payload, err := canonicalReadonlyOperationJSON(canonicalPayload)
	if err != nil {
		return agentexecution.Operation{}, readonlyOperationValidation("readonly operation payload is invalid")
	}
	hash := sha256.Sum256(payload)
	var operation agentexecution.Operation
	var beginErr error
	var terminal *agentexecution.Record
	err = service.transactor.WithUser(ctx, actor.UserID, func(ctx context.Context, tx database.Tx) error {
		record, getErr := service.store.Get(ctx, tx, actor.UserID, runID, true)
		if getErr != nil {
			return getErr
		}
		now := service.now().UTC().Truncate(time.Microsecond)
		if getErr = authorizeReadonlyActor(record, actor); getErr != nil {
			return getErr
		}
		if isReadonlyTerminal(record.Run.Status) {
			return model.ErrConflict
		}
		if deadlineExpired(record, now) {
			expectedVersion := record.Run.Version
			timeoutReadonlyRecord(&record, now)
			if getErr = service.saveReadonlyTransition(ctx, tx, &record, expectedVersion, actor.Token, "agent.readonly.timeout", "system"); getErr != nil {
				return getErr
			}
			terminal = &record
			beginErr = model.ErrConflict
			return nil
		}
		operations, getErr := service.store.Operations(ctx, tx, actor.UserID, runID)
		if getErr != nil {
			return getErr
		}
		providerTurns, calendarReads, calendarAttempts := 0, 0, 0
		matchingIndex := -1
		for index, existing := range operations {
			switch existing.Kind {
			case "provider_turn":
				providerTurns++
				if kind == "provider_turn" && existing.State == "running" {
					return model.ErrConflict
				}
			case "calendar_read":
				calendarReads++
				calendarAttempts += existing.Attempts
			}
			if existing.Kind == kind && existing.ID == id {
				matchingIndex = index
			}
		}
		if matchingIndex >= 0 {
			existing := operations[matchingIndex]
			if kind != "calendar_read" || existing.Hash != hash || existing.State == "running" {
				return model.ErrConflict
			}
			if calendarAttempts >= readonlyCalendarAttemptLimit {
				return readonlyOperationValidation("readonly calendar attempt limit exceeded")
			}
			operation = existing
			operation.State = "running"
			operation.Attempts++
			operation.UsageComplete = false
			operation.ErrorCode = ""
			operation.FinishedAt = time.Time{}
			if getErr = service.store.PutOperation(ctx, tx, operation, existing.State); getErr != nil {
				return getErr
			}
			return service.saveReadonlyTransition(ctx, tx, &record, record.Run.Version, actor.Token, "agent.readonly.operation.begin", "agent")
		}
		if kind == "provider_turn" && providerTurns >= readonlyProviderTurnLimit {
			return readonlyOperationValidation("readonly provider turn limit exceeded")
		}
		if kind == "calendar_read" && calendarAttempts >= readonlyCalendarAttemptLimit {
			return readonlyOperationValidation("readonly calendar attempt limit exceeded")
		}
		if kind == "calendar_read" && calendarReads >= readonlyCalendarReadLimit {
			return readonlyOperationValidation("readonly calendar read limit exceeded")
		}
		if readonlyTokenBudgetExceeded(record, reservedTokens) {
			return readonlyOperationValidation("run token budget exceeded")
		}
		operation = agentexecution.Operation{
			UserID: actor.UserID, RunID: runID, Kind: kind, ID: id, State: "running", Hash: hash,
			Attempts: 1, ReservedTokens: reservedTokens, UsageComplete: false, StartedAt: now,
		}
		if getErr = service.store.PutOperation(ctx, tx, operation, ""); getErr != nil {
			return getErr
		}
		if kind == "provider_turn" {
			record.Execution.ReservedTokens += reservedTokens
			record.Execution.UsageComplete = false
		}
		return service.saveReadonlyTransition(ctx, tx, &record, record.Run.Version, actor.Token, "agent.readonly.operation.begin", "agent")
	})
	if err != nil {
		return agentexecution.Operation{}, err
	}
	if terminal != nil {
		service.observeRunTerminal(*terminal)
	}
	return operation, beginErr
}

func (service *AgentReadonlyService) RetryOperation(ctx context.Context, actor agentexecution.Actor, handle agentexecution.Operation, reservedTokens int) (agentexecution.Operation, error) {
	if service == nil || handle.RunID == uuid.Nil {
		return agentexecution.Operation{}, fmt.Errorf("%w: readonly operation run ID is required", ErrValidation)
	}
	if err := validateReadonlyActor(actor); err != nil {
		return agentexecution.Operation{}, err
	}
	if handle.UserID != actor.UserID || handle.Kind != "provider_turn" || handle.ID == "" || handle.Attempts < 1 || reservedTokens < 0 {
		return agentexecution.Operation{}, readonlyOperationValidation("readonly operation retry is invalid")
	}
	var retried agentexecution.Operation
	var retryErr error
	var terminal *agentexecution.Record
	err := service.transactor.WithUser(ctx, actor.UserID, func(ctx context.Context, tx database.Tx) error {
		record, err := service.store.Get(ctx, tx, actor.UserID, handle.RunID, true)
		if err != nil {
			return err
		}
		now := service.now().UTC().Truncate(time.Microsecond)
		if err = authorizeReadonlyActor(record, actor); err != nil {
			return err
		}
		if isReadonlyTerminal(record.Run.Status) {
			return model.ErrConflict
		}
		if deadlineExpired(record, now) {
			expectedVersion := record.Run.Version
			timeoutReadonlyRecord(&record, now)
			if err = service.saveReadonlyTransition(ctx, tx, &record, expectedVersion, actor.Token, "agent.readonly.timeout", "system"); err != nil {
				return err
			}
			terminal = &record
			retryErr = model.ErrConflict
			return nil
		}
		operations, err := service.store.Operations(ctx, tx, actor.UserID, handle.RunID)
		if err != nil {
			return err
		}
		index, persisted := readonlyPersistedOperation(operations, handle.Kind, handle.ID)
		if index < 0 || !readonlyOperationHandleMatches(persisted, handle) {
			return model.ErrConflict
		}
		for otherIndex, operation := range operations {
			if otherIndex != index && operation.Kind == "provider_turn" && operation.State == "running" {
				return model.ErrConflict
			}
		}
		if persisted.Attempts >= readonlyOperationAttemptLimit {
			return readonlyOperationValidation("readonly operation retry limit exceeded")
		}
		if persisted.State != "unknown" || persisted.UsageComplete || persisted.ReservedTokens%persisted.Attempts != 0 {
			return model.ErrConflict
		}
		perAttemptReservation := persisted.ReservedTokens / persisted.Attempts
		if reservedTokens != perAttemptReservation {
			return model.ErrConflict
		}
		if readonlyTokenBudgetExceeded(record, reservedTokens) {
			return readonlyOperationValidation("run token budget exceeded")
		}
		retried = persisted
		retried.State = "running"
		retried.Attempts++
		retried.ReservedTokens += reservedTokens
		retried.ErrorCode = ""
		retried.FinishedAt = time.Time{}
		if err = service.store.PutOperation(ctx, tx, retried, persisted.State); err != nil {
			return err
		}
		record.Execution.ReservedTokens += reservedTokens
		record.Execution.UsageComplete = false
		return service.saveReadonlyTransition(ctx, tx, &record, record.Run.Version, actor.Token, "agent.readonly.operation.retry", "agent")
	})
	if err != nil {
		return agentexecution.Operation{}, err
	}
	if terminal != nil {
		service.observeRunTerminal(*terminal)
	}
	return retried, retryErr
}

func (service *AgentReadonlyService) EndOperation(ctx context.Context, actor agentexecution.Actor, handle agentexecution.Operation, usage agentprotocol.Usage, usageComplete bool, errorCode string) error {
	if service == nil || handle.RunID == uuid.Nil {
		return fmt.Errorf("%w: readonly operation run ID is required", ErrValidation)
	}
	if err := validateReadonlyActor(actor); err != nil {
		return err
	}
	if handle.UserID != actor.UserID || (handle.Kind != "provider_turn" && handle.Kind != "calendar_read") || handle.ID == "" || handle.Attempts < 1 {
		return readonlyOperationValidation("readonly operation settlement is invalid")
	}
	if !readonlyOperationUsageValid(usage) {
		return readonlyOperationValidation("readonly operation usage is invalid")
	}
	if handle.Kind == "calendar_read" && usage != (agentprotocol.Usage{}) {
		return readonlyOperationValidation("calendar reads cannot report model token usage")
	}
	if !readonlyOperationErrorCodeValid(errorCode) {
		return readonlyOperationValidation("readonly operation error code is invalid")
	}
	return service.transactor.WithUser(ctx, actor.UserID, func(ctx context.Context, tx database.Tx) error {
		record, err := service.store.Get(ctx, tx, actor.UserID, handle.RunID, true)
		if err != nil {
			return err
		}
		now := service.now().UTC().Truncate(time.Microsecond)
		if err = authorizeReadonlyActor(record, actor); err != nil {
			return err
		}
		operations, err := service.store.Operations(ctx, tx, actor.UserID, handle.RunID)
		if err != nil {
			return err
		}
		index, persisted := readonlyPersistedOperation(operations, handle.Kind, handle.ID)
		if index < 0 || !readonlyOperationHandleMatches(persisted, handle) {
			return model.ErrConflict
		}
		if persisted.State != "running" {
			return nil
		}

		release := 0
		if handle.Kind == "provider_turn" && usageComplete {
			if persisted.ReservedTokens%persisted.Attempts != 0 {
				return model.ErrConflict
			}
			release = persisted.ReservedTokens / persisted.Attempts
		}
		settled := persisted
		settled.Usage = addReadonlyUsage(settled.Usage, usage)
		settled.ErrorCode = errorCode
		settled.FinishedAt = now
		if !usageComplete {
			settled.State = "unknown"
		} else if errorCode == "" {
			settled.State = "completed"
		} else {
			settled.State = "failed"
		}
		settled.ReservedTokens -= release
		priorAttemptUncertain := handle.Kind == "provider_turn" && persisted.Attempts > 1 && !persisted.UsageComplete
		settled.UsageComplete = usageComplete && !priorAttemptUncertain
		if handle.Kind == "provider_turn" && settled.ReservedTokens != 0 {
			settled.UsageComplete = false
		}
		operations[index] = settled

		if err = service.store.PutOperation(ctx, tx, settled, persisted.State); err != nil {
			return err
		}
		if handle.Kind == "provider_turn" {
			record.Execution.KnownUsage = addReadonlyUsage(record.Execution.KnownUsage, usage)
			record.Execution.ReservedTokens -= release
			record.Execution.UsageComplete = readonlyProviderUsageComplete(operations)
		}
		return service.saveReadonlyTransition(ctx, tx, &record, record.Run.Version, actor.Token, "agent.readonly.operation.end", "agent")
	})
}

func readonlyPersistedOperation(operations []agentexecution.Operation, kind, id string) (int, agentexecution.Operation) {
	for index, operation := range operations {
		if operation.Kind == kind && operation.ID == id {
			return index, operation
		}
	}
	return -1, agentexecution.Operation{}
}

func readonlyOperationHandleMatches(persisted, handle agentexecution.Operation) bool {
	return persisted.UserID == handle.UserID && persisted.RunID == handle.RunID &&
		persisted.Kind == handle.Kind && persisted.ID == handle.ID && persisted.Hash == handle.Hash &&
		persisted.Attempts == handle.Attempts && persisted.StartedAt.Equal(handle.StartedAt)
}

func readonlyOperationUsageValid(usage agentprotocol.Usage) bool {
	return usage.InputTokens >= 0 && usage.OutputTokens >= 0 && usage.TotalTokens >= 0 &&
		usage.TotalTokens == usage.InputTokens+usage.OutputTokens
}

func readonlyTokenBudgetExceeded(record agentexecution.Record, reserve int) bool {
	maximum := record.Execution.Budget.MaxTokens
	known, reserved := record.Execution.KnownUsage.TotalTokens, record.Execution.ReservedTokens
	if known > maximum || reserved > maximum-known {
		return true
	}
	return reserve > maximum-known-reserved
}

func addReadonlyUsage(first, second agentprotocol.Usage) agentprotocol.Usage {
	return agentprotocol.Usage{
		InputTokens: first.InputTokens + second.InputTokens, OutputTokens: first.OutputTokens + second.OutputTokens,
		TotalTokens: first.TotalTokens + second.TotalTokens,
	}
}

func readonlyProviderUsageComplete(operations []agentexecution.Operation) bool {
	for _, operation := range operations {
		if operation.Kind == "provider_turn" && (operation.State == "running" || !operation.UsageComplete) {
			return false
		}
	}
	return true
}

func readonlyOperationErrorCodeValid(code string) bool {
	switch agentprotocol.ErrorCode(code) {
	case "",
		agentprotocol.ErrorCodeApprovalDenied,
		agentprotocol.ErrorCodeCancelled,
		agentprotocol.ErrorCodeCapabilityUnavailable,
		agentprotocol.ErrorCodeInternalError,
		agentprotocol.ErrorCodePermissionDenied,
		agentprotocol.ErrorCodeProtocolIncompatible,
		agentprotocol.ErrorCodeProviderRateLimited,
		agentprotocol.ErrorCodeProviderUnavailable,
		agentprotocol.ErrorCodeTimeout,
		agentprotocol.ErrorCodeToolFailed,
		agentprotocol.ErrorCodeValidationFailed,
		agentprotocol.ErrorCodeVersionConflict:
		return true
	default:
		return false
	}
}

func readonlyOperationValidation(message string) error {
	return &agentexecution.Error{Agent: agentprotocol.AgentError{
		Code: agentprotocol.ErrorCodeValidationFailed, Message: message, Retryable: false,
	}}
}

func canonicalReadonlyOperationJSON(raw []byte) ([]byte, error) {
	return canonicaljson.Bytes(raw)
}
