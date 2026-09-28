package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"dayorder.local/api/internal/agentassets"
	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

const defaultAgentCalendarReadLimit = 20

// AgentCalendarReadService projects account calendar events through a Run's
// frozen read scope and records the source versions used by the Run.
type AgentCalendarReadService struct {
	runs     *AgentReadonlyService
	calendar *CalendarService
	spec     agentprotocol.ToolSpec
	observer agentexecution.Observer
}

func NewAgentCalendarReadService(runs *AgentReadonlyService, calendar *CalendarService) (*AgentCalendarReadService, error) {
	if runs == nil || calendar == nil {
		return nil, errors.New("readonly run and calendar services are required")
	}
	spec, err := agentassets.CalendarReadSpec()
	if err != nil {
		return nil, fmt.Errorf("load calendar read tool: %w", err)
	}
	return &AgentCalendarReadService{runs: runs, calendar: calendar, spec: spec, observer: runs.observer}, nil
}

// SetObserver is construction-time assembly for the Calendar Tool observer.
// Callers must not mutate it after the service starts handling reads.
func (service *AgentCalendarReadService) SetObserver(observer agentexecution.Observer) {
	if service != nil {
		service.observer = observer
	}
}

func (service *AgentCalendarReadService) Read(ctx context.Context, actor agentexecution.Actor, runID uuid.UUID, callID string, input agentprotocol.CalendarReadInput) (agentprotocol.ToolResult, error) {
	if service == nil {
		return agentprotocol.ToolResult{}, agentCalendarError(agentprotocol.ErrorCodeValidationFailed, "calendar read service is required")
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionCalendarReadInput, input); err != nil {
		return agentprotocol.ToolResult{}, agentCalendarError(agentprotocol.ErrorCodeValidationFailed, "invalid calendar read input")
	}
	effective, start, end, err := normalizeAgentCalendarInput(input)
	if err != nil {
		return agentprotocol.ToolResult{}, err
	}
	record, err := service.runs.Authorize(ctx, actor, runID)
	if err != nil {
		return agentprotocol.ToolResult{}, normalizeAgentCalendarError(err)
	}
	if err = authorizeAgentCalendarWindow(record, start, end); err != nil {
		return agentprotocol.ToolResult{}, err
	}
	payload, err := json.Marshal(effective)
	if err != nil {
		return agentprotocol.ToolResult{}, fmt.Errorf("encode calendar read input: %w", err)
	}
	operation, err := service.runs.BeginOperation(ctx, actor, runID, "calendar_read", callID, payload, 0)
	if err != nil {
		return agentprotocol.ToolResult{}, normalizeAgentCalendarError(err)
	}
	startedAt := time.Now()

	cursor := ""
	if effective.Cursor != nil {
		cursor = string(*effective.Cursor)
	}
	page, err := service.calendar.List(ctx, actor.UserID, &start, &end, cursor, effective.Limit)
	dependencyExitedAt := time.Now()
	if err != nil {
		return service.failRead(ctx, actor, operation, record.Execution.ModelProfile, startedAt, dependencyExitedAt, err)
	}
	data := agentprotocol.CalendarReadData{
		Events:  make([]agentprotocol.CalendarReadDataEventsElem, 0, len(page.Events)),
		Window:  agentprotocol.CalendarReadDataWindow{Start: effective.Start, End: effective.End},
		HasMore: page.HasMore, NextCursor: nil,
	}
	if page.NextCursor != "" {
		next := page.NextCursor
		data.NextCursor = &next
	}
	refs := make([]model.AgentSourceRefDraft, 0, len(page.Events))
	for _, event := range page.Events {
		data.Events = append(data.Events, agentprotocol.CalendarReadDataEventsElem{
			ID: agentprotocol.UUID(event.ID.String()), Title: event.Title,
			StartAt:  agentprotocol.DateTime(event.StartAt.UTC().Format(time.RFC3339Nano)),
			EndAt:    agentprotocol.DateTime(event.EndAt.UTC().Format(time.RFC3339Nano)),
			Timezone: event.Timezone, Kind: event.Kind, Version: int(event.Version),
		})
		refs = append(refs, model.AgentSourceRefDraft{
			EntityType: "calendar_event", EntityID: event.ID, EntityVersion: event.Version, LabelSnapshot: event.Title,
		})
	}
	if err = agentprotocol.Validate(agentprotocol.DefinitionCalendarReadData, data); err != nil {
		return service.failRead(ctx, actor, operation, record.Execution.ModelProfile, startedAt, dependencyExitedAt, agentCalendarError(agentprotocol.ErrorCodeValidationFailed, "invalid calendar read output"))
	}
	result, err := calendarToolResult(data)
	if err != nil {
		return service.failRead(ctx, actor, operation, record.Execution.ModelProfile, startedAt, dependencyExitedAt, err)
	}
	resultBytes, err := json.Marshal(result)
	if err != nil {
		return service.failRead(ctx, actor, operation, record.Execution.ModelProfile, startedAt, dependencyExitedAt, fmt.Errorf("encode calendar read result: %w", err))
	}
	if len(resultBytes) > service.spec.ResultMaxBytes {
		return service.failRead(ctx, actor, operation, record.Execution.ModelProfile, startedAt, dependencyExitedAt, agentCalendarError(agentprotocol.ErrorCodeValidationFailed, "calendar read result exceeds resultMaxBytes"))
	}
	if err = service.runs.RecordCalendarRefs(ctx, actor, runID, refs); err != nil {
		return service.failRead(ctx, actor, operation, record.Execution.ModelProfile, startedAt, dependencyExitedAt, err)
	}
	settlementCode := ""
	if contextErr := ctx.Err(); contextErr != nil {
		settlementCode = string(agentCalendarErrorCode(contextErr))
	}
	if err = service.settleReadOperation(ctx, actor, operation, record.Execution.ModelProfile, startedAt, dependencyExitedAt, settlementCode); err != nil {
		return agentprotocol.ToolResult{}, joinAgentCalendarErrors(ctx.Err(), nil, err)
	}
	if err = ctx.Err(); err != nil {
		return agentprotocol.ToolResult{}, err
	}
	if _, err = service.runs.Authorize(ctx, actor, runID); err != nil {
		return agentprotocol.ToolResult{}, normalizeAgentCalendarError(err)
	}
	if err = ctx.Err(); err != nil {
		return agentprotocol.ToolResult{}, err
	}
	return result, nil
}

// RecordCalendarRefs publishes source references only while the same actor is
// still authorized, using a short transaction separate from the domain read.
func (service *AgentReadonlyService) RecordCalendarRefs(ctx context.Context, actor agentexecution.Actor, runID uuid.UUID, refs []model.AgentSourceRefDraft) error {
	if service == nil || runID == uuid.Nil {
		return fmt.Errorf("%w: readonly run ID is required", ErrValidation)
	}
	if err := validateReadonlyActor(actor); err != nil {
		return err
	}
	var authorizationErr error
	var terminal *agentexecution.Record
	err := service.transactor.WithUser(ctx, actor.UserID, func(ctx context.Context, tx database.Tx) error {
		record, err := service.store.Get(ctx, tx, actor.UserID, runID, true)
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
			return nil
		}
		return service.store.AddRefs(ctx, tx, actor.UserID, runID, refs)
	})
	if err != nil {
		return err
	}
	if terminal != nil {
		service.observeRunTerminal(*terminal)
	}
	return authorizationErr
}

func normalizeAgentCalendarInput(input agentprotocol.CalendarReadInput) (agentprotocol.CalendarReadInput, time.Time, time.Time, error) {
	start, err := time.Parse(time.RFC3339, string(input.Start))
	if err != nil {
		return agentprotocol.CalendarReadInput{}, time.Time{}, time.Time{}, agentCalendarError(agentprotocol.ErrorCodeValidationFailed, "invalid calendar read input")
	}
	end, err := time.Parse(time.RFC3339, string(input.End))
	if err != nil || !start.Before(end) || end.Sub(start) > 31*24*time.Hour {
		return agentprotocol.CalendarReadInput{}, time.Time{}, time.Time{}, agentCalendarError(agentprotocol.ErrorCodeValidationFailed, "invalid calendar read window")
	}
	start, end = start.UTC(), end.UTC()
	effective := input
	effective.Start = agentprotocol.DateTime(start.Format(time.RFC3339Nano))
	effective.End = agentprotocol.DateTime(end.Format(time.RFC3339Nano))
	if effective.Limit == 0 {
		effective.Limit = defaultAgentCalendarReadLimit
	}
	if err = agentprotocol.Validate(agentprotocol.DefinitionCalendarReadInput, effective); err != nil {
		return agentprotocol.CalendarReadInput{}, time.Time{}, time.Time{}, agentCalendarError(agentprotocol.ErrorCodeValidationFailed, "invalid calendar read input")
	}
	return effective, start, end, nil
}

func authorizeAgentCalendarWindow(record agentexecution.Record, start, end time.Time) error {
	scope := record.Execution.Capabilities.Scope
	if scope.From == nil || scope.To == nil {
		return fmt.Errorf("readonly calendar scope is missing a window")
	}
	from, fromErr := time.Parse(time.RFC3339, *scope.From)
	to, toErr := time.Parse(time.RFC3339, *scope.To)
	if fromErr != nil || toErr != nil || !from.Before(to) {
		return fmt.Errorf("readonly calendar scope has an invalid window")
	}
	if start.Before(from.UTC()) || end.After(to.UTC()) {
		return agentCalendarError(agentprotocol.ErrorCodePermissionDenied, "calendar read window is outside the Run scope")
	}
	return nil
}

func calendarToolResult(data agentprotocol.CalendarReadData) (agentprotocol.ToolResult, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return agentprotocol.ToolResult{}, fmt.Errorf("encode calendar read data: %w", err)
	}
	var resultData agentprotocol.ToolResultData
	if err = json.Unmarshal(raw, &resultData); err != nil {
		return agentprotocol.ToolResult{}, fmt.Errorf("decode calendar read data: %w", err)
	}
	result := agentprotocol.ToolResult{Ok: true, Data: resultData}
	if err = agentprotocol.Validate(agentprotocol.DefinitionToolResult, result); err != nil {
		return agentprotocol.ToolResult{}, agentCalendarError(agentprotocol.ErrorCodeValidationFailed, "invalid calendar read result")
	}
	return result, nil
}

func (service *AgentCalendarReadService) failRead(ctx context.Context, actor agentexecution.Actor, operation agentexecution.Operation, profile string, startedAt, dependencyExitedAt time.Time, readErr error) (agentprotocol.ToolResult, error) {
	code := agentCalendarErrorCode(readErr)
	if err := service.settleReadOperation(ctx, actor, operation, profile, startedAt, dependencyExitedAt, string(code)); err != nil {
		return agentprotocol.ToolResult{}, joinAgentCalendarErrors(ctx.Err(), readErr, err)
	}
	return agentprotocol.ToolResult{}, joinAgentCalendarErrors(ctx.Err(), readErr, nil)
}

func (service *AgentCalendarReadService) settleReadOperation(ctx context.Context, actor agentexecution.Actor, operation agentexecution.Operation, profile string, startedAt, dependencyExitedAt time.Time, errorCode string) error {
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := service.runs.EndOperation(settleCtx, actor, operation, agentprotocol.Usage{}, true, errorCode); err != nil {
		observedCode := errorCode
		if observedCode == "" {
			observedCode = string(agentCalendarErrorCode(err))
		}
		service.observeCalendarTool(actor, operation, profile, startedAt, observedCode, 0, false)
		return err
	}
	cancelLatency := time.Duration(0)
	if errorCode == string(agentprotocol.ErrorCodeCancelled) && settleCtx.Err() == nil {
		if latency, present, _ := service.runs.CancellationLatency(settleCtx, actor, operation.RunID, dependencyExitedAt); present {
			cancelLatency = latency
		}
	} else if errorCode == string(agentprotocol.ErrorCodeTimeout) {
		if deadline, ok := ctx.Deadline(); ok && !deadline.After(dependencyExitedAt) {
			cancelLatency = dependencyExitedAt.Sub(deadline)
		}
	}
	service.observeCalendarTool(actor, operation, profile, startedAt, errorCode, cancelLatency, true)
	return nil
}

func (service *AgentCalendarReadService) observeCalendarTool(actor agentexecution.Actor, operation agentexecution.Operation, profile string, startedAt time.Time, errorCode string, cancelLatency time.Duration, settlementComplete bool) {
	duration := time.Since(startedAt)
	outcome := "completed"
	if errorCode == string(agentprotocol.ErrorCodeCancelled) {
		outcome = "cancelled"
	} else if errorCode != "" {
		outcome = "failed"
	}
	observation := agentexecution.Observation{
		Kind: "tool", Mode: string(actor.Mode), ToolID: "dayorder.calendar.read", ModelProfile: profile,
		Outcome: outcome, ErrorCode: errorCode, Duration: duration, CancelLatency: cancelLatency,
		UsageComplete: settlementComplete, Attempts: operation.Attempts,
	}
	if service.observer != nil {
		service.observer.ObserveAgent(observation)
	}
	if service.runs.logger != nil {
		service.runs.logger.Info("agent tool completed",
			"runId", operation.RunID.String(), "callId", operation.ID, "mode", actor.Mode,
			"profile", profile, "tool", "dayorder.calendar.read", "outcome", outcome,
			"errorCode", errorCode, "durationMs", duration.Milliseconds(), "cancelLatencyMs", cancelLatency.Milliseconds(),
			"attempt", operation.Attempts, "settlementComplete", settlementComplete,
		)
	}
}

func joinAgentCalendarErrors(contextErr, primary, settlement error) error {
	parts := make([]error, 0, 3)
	contextErr = canonicalAgentCalendarContextError(contextErr)
	if contextErr == nil {
		contextErr = canonicalAgentCalendarContextError(primary)
	}
	if contextErr != nil {
		parts = append(parts, contextErr)
	}
	if primary != nil {
		if protocolError, found := agentCalendarProtocolError(primary); found {
			parts = append(parts, &agentexecution.Error{Agent: protocolError})
		} else if canonicalAgentCalendarContextError(primary) == nil {
			parts = append(parts, normalizeAgentCalendarPrimaryFailure(primary))
		}
	}
	if settlement != nil {
		parts = append(parts, normalizeAgentCalendarSettlementError(settlement))
	}
	switch len(parts) {
	case 0:
		return nil
	case 1:
		return parts[0]
	default:
		return errors.Join(parts...)
	}
}

func canonicalAgentCalendarContextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

func normalizeAgentCalendarPrimaryFailure(err error) error {
	normalized := normalizeAgentCalendarError(err)
	if _, found := agentCalendarProtocolError(normalized); found {
		return normalized
	}
	return agentCalendarError(agentprotocol.ErrorCodeToolFailed, "calendar read failed")
}

func normalizeAgentCalendarSettlementError(err error) error {
	if err == nil {
		return nil
	}
	normalized := normalizeAgentCalendarError(err)
	if protocolError, found := agentCalendarProtocolError(normalized); found {
		return &agentexecution.Error{Agent: protocolError}
	}
	return agentCalendarError(agentprotocol.ErrorCodeToolFailed, "calendar read settlement failed")
}

func normalizeAgentCalendarError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if protocolError, found := agentCalendarProtocolError(err); found {
		return &agentexecution.Error{Agent: protocolError}
	}
	switch {
	case errors.Is(err, ErrValidation), errors.Is(err, ErrInvalidCursor):
		return agentCalendarError(agentprotocol.ErrorCodeValidationFailed, "invalid calendar read request")
	case errors.Is(err, model.ErrConflict):
		return agentCalendarError(agentprotocol.ErrorCodeVersionConflict, "calendar read state changed")
	case errors.Is(err, model.ErrNotFound):
		return agentCalendarError(agentprotocol.ErrorCodePermissionDenied, "calendar read is not permitted")
	default:
		return err
	}
}

func agentCalendarErrorCode(err error) agentprotocol.ErrorCode {
	if errors.Is(err, context.Canceled) {
		return agentprotocol.ErrorCodeCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return agentprotocol.ErrorCodeTimeout
	}
	if normalized := normalizeAgentCalendarError(err); normalized != nil {
		if protocolError, found := agentCalendarProtocolError(normalized); found {
			return protocolError.Code
		}
	}
	return agentprotocol.ErrorCodeToolFailed
}

func agentCalendarProtocolError(err error) (agentprotocol.AgentError, bool) {
	var pointer *agentexecution.Error
	if errors.As(err, &pointer) && pointer != nil {
		return pointer.Agent, true
	}
	var value agentexecution.Error
	if errors.As(err, &value) {
		return value.Agent, true
	}
	return agentprotocol.AgentError{}, false
}

func agentCalendarError(code agentprotocol.ErrorCode, message string) error {
	return &agentexecution.Error{Agent: agentprotocol.AgentError{Code: code, Message: message, Retryable: false}}
}
