// Package agentbinding adapts trusted application services to local Agent Tool
// bindings without importing repositories or transports.
package agentbinding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"dayorder.local/api/internal/agentassets"
	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agenttool"
	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

type CalendarReader interface {
	Read(context.Context, agentexecution.Actor, uuid.UUID, string, agentprotocol.CalendarReadInput) (agentprotocol.ToolResult, error)
}

type calendarBinding struct {
	actor         agentexecution.Actor
	runID         uuid.UUID
	reader        CalendarReader
	spec          agentprotocol.ToolSpec
	validateInput func(any) error
}

func NewCalendar(actor agentexecution.Actor, runID uuid.UUID, reader CalendarReader) (agenttool.Binding, error) {
	if err := validateCalendarIdentity(actor, runID); err != nil {
		return nil, err
	}
	if reader == nil || isNilCalendarReader(reader) {
		return nil, errors.New("calendar reader is required")
	}
	spec, err := agentassets.CalendarReadSpec()
	if err != nil {
		return nil, fmt.Errorf("load calendar read tool: %w", err)
	}
	validateInput, err := compileCalendarInput(spec.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("compile calendar read input schema: %w", err)
	}
	return &calendarBinding{actor: actor, runID: runID, reader: reader, spec: spec, validateInput: validateInput}, nil
}

func (binding *calendarBinding) Spec() agentprotocol.ToolSpec {
	raw, _ := json.Marshal(binding.spec)
	var copied agentprotocol.ToolSpec
	_ = json.Unmarshal(raw, &copied)
	return copied
}

func (binding *calendarBinding) Invoke(ctx context.Context, input map[string]any, toolContext agenttool.Context) (agentprotocol.ToolResult, error) {
	if toolContext.RunID != binding.runID.String() {
		return calendarFailure(agentprotocol.ErrorCodePermissionDenied, "calendar read is not permitted"), nil
	}
	if toolContext.CallID == "" || strings.TrimSpace(toolContext.CallID) != toolContext.CallID || len(toolContext.CallID) > 240 {
		return calendarFailure(agentprotocol.ErrorCodeValidationFailed, "calendar read call ID is invalid"), nil
	}
	if err := binding.validateInput(input); err != nil {
		return calendarFailure(agentprotocol.ErrorCodeValidationFailed, "calendar read input is invalid"), nil
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return calendarFailure(agentprotocol.ErrorCodeValidationFailed, "calendar read input is invalid"), nil
	}
	var decoded agentprotocol.CalendarReadInput
	if err = json.Unmarshal(raw, &decoded); err != nil {
		return calendarFailure(agentprotocol.ErrorCodeValidationFailed, "calendar read input is invalid"), nil
	}
	result, err := binding.reader.Read(ctx, binding.actor, binding.runID, toolContext.CallID, decoded)
	if err == nil {
		return result, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return agentprotocol.ToolResult{}, err
	}
	if code, message, ok := expectedCalendarError(err); ok {
		return calendarFailure(code, message), nil
	}
	return agentprotocol.ToolResult{}, err
}

func validateCalendarIdentity(actor agentexecution.Actor, runID uuid.UUID) error {
	if actor.UserID == uuid.Nil || runID == uuid.Nil {
		return errors.New("calendar actor user and run are required")
	}
	switch actor.Mode {
	case agentprotocol.ExecutionModeForeground:
		if actor.Token != uuid.Nil {
			return errors.New("foreground calendar actor token must be empty")
		}
	case agentprotocol.ExecutionModeBackground:
		if actor.Token == uuid.Nil {
			return errors.New("background calendar actor token is required")
		}
	default:
		return errors.New("calendar actor mode is invalid")
	}
	return nil
}

func isNilCalendarReader(reader CalendarReader) bool {
	value := reflect.ValueOf(reader)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func compileCalendarInput(schema agentprotocol.ToolSpecInputSchema) (func(any) error, error) {
	compiler, err := agentprotocol.NewSchemaCompiler()
	if err != nil {
		return nil, err
	}
	const schemaID = "urn:dayorder:agent:calendar-read-input"
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var document any
	if err = json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	if err = compiler.AddResource(schemaID, document); err != nil {
		return nil, err
	}
	compiled, err := compiler.Compile(schemaID)
	if err != nil {
		return nil, err
	}
	return compiled.Validate, nil
}

func expectedCalendarError(err error) (agentprotocol.ErrorCode, string, bool) {
	if protocolError, found := calendarProtocolError(err); found {
		switch protocolError.Code {
		case agentprotocol.ErrorCodePermissionDenied, agentprotocol.ErrorCodeValidationFailed, agentprotocol.ErrorCodeVersionConflict:
			message := protocolError.Message
			if message == "" {
				message = string(protocolError.Code)
			}
			return protocolError.Code, message, true
		}
	}
	switch {
	case errors.Is(err, service.ErrValidation), errors.Is(err, service.ErrInvalidCursor):
		return agentprotocol.ErrorCodeValidationFailed, "invalid calendar read request", true
	case errors.Is(err, model.ErrConflict):
		return agentprotocol.ErrorCodeVersionConflict, "calendar read state changed", true
	case errors.Is(err, model.ErrNotFound):
		return agentprotocol.ErrorCodePermissionDenied, "calendar read is not permitted", true
	default:
		return "", "", false
	}
}

func calendarProtocolError(err error) (agentprotocol.AgentError, bool) {
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

func calendarFailure(code agentprotocol.ErrorCode, message string) agentprotocol.ToolResult {
	return agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: code, Message: message, Retryable: false}}
}
