package agentbinding_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"dayorder.local/api/internal/agentbinding"
	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agenttool"
	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

type calendarReaderFunc func(context.Context, agentexecution.Actor, uuid.UUID, string, agentprotocol.CalendarReadInput) (agentprotocol.ToolResult, error)

func (read calendarReaderFunc) Read(ctx context.Context, actor agentexecution.Actor, runID uuid.UUID, callID string, input agentprotocol.CalendarReadInput) (agentprotocol.ToolResult, error) {
	return read(ctx, actor, runID, callID, input)
}

func TestCalendarBindingBindsIdentityAndPreservesStrictInput(t *testing.T) {
	actor := agentexecution.Actor{UserID: uuid.New(), Token: uuid.New(), Mode: agentprotocol.ExecutionModeBackground}
	runID := uuid.New()
	wantInput := agentprotocol.CalendarReadInput{Start: "2026-09-05T08:00:00+08:00", End: "2026-09-06T08:00:00+08:00", Limit: 7}
	wantResult := agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{"hasMore": false}}
	calls := 0
	binding, err := agentbinding.NewCalendar(actor, runID, calendarReaderFunc(func(_ context.Context, gotActor agentexecution.Actor, gotRunID uuid.UUID, callID string, input agentprotocol.CalendarReadInput) (agentprotocol.ToolResult, error) {
		calls++
		if gotActor != actor || gotRunID != runID || callID != "call-1" || !reflect.DeepEqual(input, wantInput) {
			t.Fatalf("Read() got actor=%#v run=%s call=%q input=%#v", gotActor, gotRunID, callID, input)
		}
		return wantResult, nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	got, err := binding.Invoke(context.Background(), map[string]any{
		"start": string(wantInput.Start), "end": string(wantInput.End), "limit": 7,
	}, agenttool.Context{RunID: runID.String(), CallID: "call-1"})
	if err != nil || !reflect.DeepEqual(got, wantResult) || calls != 1 {
		t.Fatalf("Invoke() result=%#v error=%v calls=%d", got, err, calls)
	}

	got, err = binding.Invoke(context.Background(), map[string]any{
		"start": string(wantInput.Start), "end": string(wantInput.End), "limit": 7, "unknown": true,
	}, agenttool.Context{RunID: runID.String(), CallID: "call-2"})
	assertBindingFailure(t, got, err, agentprotocol.ErrorCodeValidationFailed)
	if calls != 1 {
		t.Fatalf("strict-invalid input reached reader %d times", calls)
	}

	got, err = binding.Invoke(context.Background(), map[string]any{
		"start": string(wantInput.Start), "end": string(wantInput.End), "limit": 7,
	}, agenttool.Context{RunID: uuid.NewString(), CallID: "call-3"})
	assertBindingFailure(t, got, err, agentprotocol.ErrorCodePermissionDenied)
	if calls != 1 {
		t.Fatalf("mismatched run reached reader %d times", calls)
	}
}

func TestCalendarBindingReturnsExpectedReaderErrorsAsToolResults(t *testing.T) {
	actor := agentexecution.Actor{UserID: uuid.New(), Mode: agentprotocol.ExecutionModeForeground}
	runID := uuid.New()
	input := map[string]any{"start": "2026-09-05T00:00:00Z", "end": "2026-09-06T00:00:00Z"}
	tests := []struct {
		name string
		err  error
		code agentprotocol.ErrorCode
	}{
		{"protocol pointer", &agentexecution.Error{Agent: agentprotocol.AgentError{Code: agentprotocol.ErrorCodePermissionDenied, Message: "outside scope"}}, agentprotocol.ErrorCodePermissionDenied},
		{"protocol value", agentexecution.Error{Agent: agentprotocol.AgentError{Code: agentprotocol.ErrorCodeValidationFailed, Message: "invalid request"}}, agentprotocol.ErrorCodeValidationFailed},
		{"wrapped protocol pointer", fmt.Errorf("adapter detail: %w", &agentexecution.Error{Agent: agentprotocol.AgentError{Code: agentprotocol.ErrorCodeVersionConflict, Message: "state changed"}}), agentprotocol.ErrorCodeVersionConflict},
		{"wrapped protocol value", fmt.Errorf("adapter detail: %w", agentexecution.Error{Agent: agentprotocol.AgentError{Code: agentprotocol.ErrorCodePermissionDenied, Message: "outside scope"}}), agentprotocol.ErrorCodePermissionDenied},
		{"service validation", service.ErrValidation, agentprotocol.ErrorCodeValidationFailed},
		{"invalid cursor", service.ErrInvalidCursor, agentprotocol.ErrorCodeValidationFailed},
		{"version conflict", model.ErrConflict, agentprotocol.ErrorCodeVersionConflict},
		{"hidden run", model.ErrNotFound, agentprotocol.ErrorCodePermissionDenied},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binding, err := agentbinding.NewCalendar(actor, runID, calendarReaderFunc(func(context.Context, agentexecution.Actor, uuid.UUID, string, agentprotocol.CalendarReadInput) (agentprotocol.ToolResult, error) {
				return agentprotocol.ToolResult{}, test.err
			}))
			if err != nil {
				t.Fatal(err)
			}
			got, invokeErr := binding.Invoke(context.Background(), input, agenttool.Context{RunID: runID.String(), CallID: "call"})
			assertBindingFailure(t, got, invokeErr, test.code)
		})
	}

	cancelled := context.Canceled
	binding, err := agentbinding.NewCalendar(actor, runID, calendarReaderFunc(func(context.Context, agentexecution.Actor, uuid.UUID, string, agentprotocol.CalendarReadInput) (agentprotocol.ToolResult, error) {
		return agentprotocol.ToolResult{}, cancelled
	}))
	if err != nil {
		t.Fatal(err)
	}
	got, invokeErr := binding.Invoke(context.Background(), input, agenttool.Context{RunID: runID.String(), CallID: "cancelled"})
	if !reflect.DeepEqual(got, agentprotocol.ToolResult{}) || !errors.Is(invokeErr, context.Canceled) {
		t.Fatalf("cancelled Invoke() result=%#v error=%v", got, invokeErr)
	}
}

func TestCalendarBindingKeepsContextAttributionForJoinedSettlementErrors(t *testing.T) {
	actor := agentexecution.Actor{UserID: uuid.New(), Mode: agentprotocol.ExecutionModeForeground}
	runID := uuid.New()
	input := map[string]any{"start": "2026-09-05T00:00:00Z", "end": "2026-09-06T00:00:00Z"}
	tests := []struct {
		name       string
		contextErr error
		cleanup    agentprotocol.AgentError
	}{
		{"cancelled conflict", context.Canceled, agentprotocol.AgentError{Code: agentprotocol.ErrorCodeVersionConflict, Message: "calendar read state changed"}},
		{"deadline settlement failure", context.DeadlineExceeded, agentprotocol.AgentError{Code: agentprotocol.ErrorCodeToolFailed, Message: "calendar read settlement failed"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binding, err := agentbinding.NewCalendar(actor, runID, calendarReaderFunc(func(context.Context, agentexecution.Actor, uuid.UUID, string, agentprotocol.CalendarReadInput) (agentprotocol.ToolResult, error) {
				return agentprotocol.ToolResult{}, errors.Join(test.contextErr, &agentexecution.Error{Agent: test.cleanup})
			}))
			if err != nil {
				t.Fatal(err)
			}
			result, invokeErr := binding.Invoke(context.Background(), input, agenttool.Context{RunID: runID.String(), CallID: "joined"})
			if !reflect.DeepEqual(result, agentprotocol.ToolResult{}) || !errors.Is(invokeErr, test.contextErr) {
				t.Fatalf("Invoke() result=%#v error=%v", result, invokeErr)
			}
			var cleanup *agentexecution.Error
			if !errors.As(invokeErr, &cleanup) || cleanup.Agent.Code != test.cleanup.Code {
				t.Fatalf("Invoke() error=%v, want cleanup code %s", invokeErr, test.cleanup.Code)
			}
		})
	}
}

func TestCalendarBindingValidatesConstructionAndReturnsSpecCopies(t *testing.T) {
	actor := agentexecution.Actor{UserID: uuid.New(), Mode: agentprotocol.ExecutionModeForeground}
	runID := uuid.New()
	reader := calendarReaderFunc(func(context.Context, agentexecution.Actor, uuid.UUID, string, agentprotocol.CalendarReadInput) (agentprotocol.ToolResult, error) {
		return agentprotocol.ToolResult{}, nil
	})
	for _, test := range []struct {
		name   string
		actor  agentexecution.Actor
		runID  uuid.UUID
		reader calendarReaderFunc
	}{
		{"missing user", agentexecution.Actor{Mode: agentprotocol.ExecutionModeForeground}, runID, reader},
		{"foreground token", agentexecution.Actor{UserID: uuid.New(), Token: uuid.New(), Mode: agentprotocol.ExecutionModeForeground}, runID, reader},
		{"background token", agentexecution.Actor{UserID: uuid.New(), Mode: agentprotocol.ExecutionModeBackground}, runID, reader},
		{"missing run", actor, uuid.Nil, reader},
		{"missing reader", actor, runID, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := agentbinding.NewCalendar(test.actor, test.runID, test.reader); err == nil {
				t.Fatal("NewCalendar() accepted invalid fixed identity or dependency")
			}
		})
	}

	binding, err := agentbinding.NewCalendar(actor, runID, reader)
	if err != nil {
		t.Fatal(err)
	}
	first := binding.Spec()
	if err = agentprotocol.Validate(agentprotocol.DefinitionToolSpec, first); err != nil || first.ID != "dayorder.calendar.read" {
		t.Fatalf("Spec() = %#v, error=%v", first, err)
	}
	first.InputSchema["type"] = "array"
	if second := binding.Spec(); second.InputSchema["type"] != "object" {
		t.Fatalf("Spec() shared mutable state: %#v", second.InputSchema)
	}
}

func assertBindingFailure(t testing.TB, result agentprotocol.ToolResult, err error, code agentprotocol.ErrorCode) {
	t.Helper()
	if err != nil || result.Ok || result.Error == nil || result.Error.Code != code || result.Error.Retryable || result.Error.Message == "" {
		t.Fatalf("Invoke() result=%#v error=%v, want structured %s", result, err, code)
	}
	if validateErr := agentprotocol.Validate(agentprotocol.DefinitionToolResult, result); validateErr != nil {
		t.Fatalf("failed ToolResult validation: %v", validateErr)
	}
}
