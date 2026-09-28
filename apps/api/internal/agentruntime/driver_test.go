package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"dayorder.local/api/internal/agentassets"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentprovider"
	"dayorder.local/api/internal/agenttool"
)

type driverTestBinding struct {
	spec   agentprotocol.ToolSpec
	invoke func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error)
}

func (b driverTestBinding) Spec() agentprotocol.ToolSpec { return b.spec }

func (b driverTestBinding) Invoke(ctx context.Context, input map[string]any, toolContext agenttool.Context) (agentprotocol.ToolResult, error) {
	return b.invoke(ctx, input, toolContext)
}

type driverApprovalFunc func(context.Context, string, agentprotocol.ToolCall) (Decision, error)

func (f driverApprovalFunc) Request(ctx context.Context, approvalID string, call agentprotocol.ToolCall) (Decision, error) {
	return f(ctx, approvalID, call)
}

type driverProviderFunc func(context.Context, agentprotocol.ModelTurnRequest) iter.Seq2[agentprotocol.ProviderEvent, error]

func (f driverProviderFunc) Stream(ctx context.Context, request agentprotocol.ModelTurnRequest) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return f(ctx, request)
}

func driverTestState() agentprotocol.RuntimeState {
	return NewState(Config{
		RunID:         "run-1",
		ExecutionMode: agentprotocol.ExecutionModeBackground,
		CapabilitySnapshot: agentprotocol.CapabilitySnapshot{
			RuntimeVersion: "2.0.0",
			ExecutionMode:  agentprotocol.ExecutionModeBackground,
			ToolIds:        []string{"test.clock.read"},
			Skills:         []agentprotocol.SkillRef{},
			Scope:          agentprotocol.AgentScope{Domains: []string{"test"}},
		},
		Budget: agentprotocol.Budget{
			MaxSteps:             4,
			MaxTokens:            100,
			MaxDurationMs:        30_000,
			MaxWorkers:           1,
			MaxConcurrency:       1,
			MaxRepeatedToolCalls: 2,
		},
	})
}

func driverClockBinding(result map[string]any) driverTestBinding {
	return driverTestBinding{
		spec: agentprotocol.ToolSpec{
			ID:              "test.clock.read",
			Description:     "Read a deterministic test clock",
			InputSchema:     map[string]any{"type": "object", "additionalProperties": false},
			OutputSchema:    map[string]any{"type": "object", "properties": map[string]any{"now": map[string]any{"type": "string"}}, "required": []any{"now"}, "additionalProperties": false},
			SideEffect:      agentprotocol.SideEffectRead,
			RequiredDomains: []string{"test"},
			ExecutionTargets: []agentprotocol.ToolSpecExecutionTargetsElem{
				agentprotocol.ToolSpecExecutionTargetsElemServer,
			},
			ApprovalPolicy: agentprotocol.ToolSpecApprovalPolicyNever,
			Idempotent:     true,
			TimeoutMs:      1_000,
			ResultMaxBytes: 1_024,
		},
		invoke: func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
			return agentprotocol.ToolResult{Ok: true, Data: result}, nil
		},
	}
}

func calendarDriverState() agentprotocol.RuntimeState {
	return NewState(Config{
		RunID:         "run-calendar",
		ExecutionMode: agentprotocol.ExecutionModeBackground,
		CapabilitySnapshot: agentprotocol.CapabilitySnapshot{
			RuntimeVersion: "2.0.0", ExecutionMode: agentprotocol.ExecutionModeBackground,
			ToolIds: []string{"dayorder.calendar.read"}, Skills: []agentprotocol.SkillRef{},
			Scope: agentprotocol.AgentScope{Domains: []string{"calendar"}, From: ptr("2026-09-05T00:00:00Z"), To: ptr("2026-09-06T00:00:00Z")},
		},
		Budget: agentprotocol.Budget{
			MaxSteps: 4, MaxTokens: 100, MaxDurationMs: 30_000, MaxWorkers: 1,
			MaxConcurrency: 1, MaxRepeatedToolCalls: 2,
		},
	})
}

func calendarDriverProvider(input map[string]any) *agentprovider.ScriptedProvider {
	return agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{
			ID: "call-calendar", Name: "dayorder.calendar.read", Input: input,
		}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}},
	})
}

func calendarDriverData() map[string]any {
	return map[string]any{
		"events": []any{map[string]any{
			"id": "550e8400-e29b-41d4-a716-446655440000", "title": "Planning",
			"startAt": "2026-09-05T09:00:00Z", "endAt": "2026-09-05T09:30:00Z",
			"timezone": "Asia/Shanghai", "kind": "meeting", "version": 1,
		}},
		"window":  map[string]any{"start": "2026-09-05T00:00:00Z", "end": "2026-09-06T00:00:00Z"},
		"hasMore": false, "nextCursor": nil,
	}
}

func allowDriverApproval() ApprovalBroker {
	return driverApprovalFunc(func(context.Context, string, agentprotocol.ToolCall) (Decision, error) {
		return DecisionAllow, nil
	})
}

func effectTypes(effects []agentprotocol.RuntimeEffect) []string {
	types := make([]string, len(effects))
	for index, effect := range effects {
		types[index] = string(effect.Type)
	}
	return types
}

func newDriverForTest(t *testing.T, provider agentprovider.StreamProvider, tools *agenttool.Registry, approvals ApprovalBroker, policy agenttool.Policy) *Driver {
	t.Helper()
	driver, err := NewDriver(DriverConfig{Provider: provider, Tools: tools, Approvals: approvals, ModelProfile: "server/default", Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	return driver
}

func newDriverWithDeadlineForTest(t *testing.T, provider agentprovider.StreamProvider, tools *agenttool.Registry, deadline time.Time) *Driver {
	t.Helper()
	driver, err := NewDriver(DriverConfig{
		Provider: provider, Tools: tools, Approvals: allowDriverApproval(),
		ModelProfile: "server/default", Policy: agenttool.Policy{Allow: []string{"*"}}, Deadline: deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	return driver
}

func driverStopReasonPtr(value agentprotocol.ProviderEventStopReason) *agentprotocol.ProviderEventStopReason {
	return &value
}

func completedToolUseEvent() agentprotocol.ProviderEvent {
	return agentprotocol.ProviderEvent{
		Type:       agentprotocol.ProviderEventTypeCompleted,
		StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonToolUse),
		Usage:      &agentprotocol.Usage{},
	}
}

func TestDriverAcceptsMaximumCrossHostRunTimer(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{{
		{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}},
	}})
	driver := newDriverForTest(t, provider, &agenttool.Registry{}, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})
	state := driverTestState()
	state.Budget.MaxDurationMs = 2_147_483_647

	trace, err := driver.Run(context.Background(), state, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("bounded Run")})
	if err != nil {
		t.Fatal(err)
	}
	if trace.State.Phase != agentprotocol.RuntimePhaseCompleted || trace.State.Error != nil {
		t.Fatalf("trace = %#v", trace)
	}
}

func TestDriverAcceptsMaximumCrossHostToolTimer(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-max-timer", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}},
	})
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	binding.spec.TimeoutMs = 2_147_483_647
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver := newDriverForTest(t, provider, tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("bounded Tool")})
	if err != nil {
		t.Fatal(err)
	}
	wantResult := agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{"now": "09:00"}}
	if trace.State.Phase != agentprotocol.RuntimePhaseCompleted || len(trace.Inputs) < 5 || trace.Inputs[4].ToolResult == nil || !reflect.DeepEqual(trace.Inputs[4].ToolResult.Result, wantResult) {
		t.Fatalf("trace = %#v", trace)
	}
}

func TestDriverRejectsForegroundModeBeforeStartingBackgroundHostEffects(t *testing.T) {
	provider := agentprovider.NewScriptedProvider(nil)
	driver := newDriverForTest(t, provider, &agenttool.Registry{}, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})
	state := driverTestState()
	state.ExecutionMode = agentprotocol.ExecutionModeForeground
	state.CapabilitySnapshot.ExecutionMode = agentprotocol.ExecutionModeForeground

	trace, err := driver.Run(context.Background(), state, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("do not start")})
	if err != nil {
		t.Fatal(err)
	}
	want := agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProtocolIncompatible, Message: "background Driver requires background execution mode", Retryable: false}
	wantInputs := []agentprotocol.RuntimeInput{{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &want}}
	if provider.RequestCount() != 0 || trace.ModelTurns != 0 || trace.State.Phase != agentprotocol.RuntimePhaseFailed || !reflect.DeepEqual(trace.State.Error, &want) {
		t.Fatalf("requests = %d, trace = %#v", provider.RequestCount(), trace)
	}
	if !reflect.DeepEqual(trace.Inputs, wantInputs) || !slices.Equal(effectTypes(trace.Effects), []string{"fail_run"}) || trace.Effects[0].Error == nil || !reflect.DeepEqual(*trace.Effects[0].Error, want) {
		t.Fatalf("inputs = %#v, effects = %#v", trace.Inputs, trace.Effects)
	}
}

func TestDriverRejectsCallerOriginatedContinuationInputsBeforeTrustedHostWork(t *testing.T) {
	modelPending, err := Advance(driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("start")})
	if err != nil {
		t.Fatal(err)
	}
	toolCalled, err := Advance(modelPending.State, agentprotocol.RuntimeInput{
		Type: agentprotocol.RuntimeInputTypeProviderEvent,
		ProviderEvent: &agentprotocol.ProviderEvent{
			Type: agentprotocol.ProviderEventTypeToolCall,
			Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	completion := completedToolUseEvent()
	toolPending, err := Advance(toolCalled.State, agentprotocol.RuntimeInput{
		Type:          agentprotocol.RuntimeInputTypeProviderEvent,
		ProviderEvent: &completion,
	})
	if err != nil {
		t.Fatal(err)
	}
	executing, err := Advance(toolPending.State, agentprotocol.RuntimeInput{
		Type: agentprotocol.RuntimeInputTypeToolResolution,
		ToolResolution: &agentprotocol.ToolResolution{
			CallID: "call-1", Decision: agentprotocol.ToolResolutionDecisionExecute,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	approvalID := "approval-call-1"
	approvalPending, err := Advance(toolPending.State, agentprotocol.RuntimeInput{
		Type: agentprotocol.RuntimeInputTypeToolResolution,
		ToolResolution: &agentprotocol.ToolResolution{
			CallID: "call-1", Decision: agentprotocol.ToolResolutionDecisionApproval, ApprovalID: &approvalID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		state agentprotocol.RuntimeState
		input agentprotocol.RuntimeInput
	}{
		{
			name:  "provider_event",
			state: modelPending.State,
			input: agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeProviderEvent, ProviderEvent: &agentprotocol.ProviderEvent{
				Type: agentprotocol.ProviderEventTypeTextDelta, Text: ptr("caller event"),
			}},
		},
		{
			name:  "tool_resolution execute for a policy-denied Tool",
			state: toolPending.State,
			input: agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResolution, ToolResolution: &agentprotocol.ToolResolution{
				CallID: "call-1", Decision: agentprotocol.ToolResolutionDecisionExecute,
			}},
		},
		{
			name:  "tool_resolution with an arbitrary approval ID",
			state: toolPending.State,
			input: agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResolution, ToolResolution: &agentprotocol.ToolResolution{
				CallID: "call-1", Decision: agentprotocol.ToolResolutionDecisionApproval, ApprovalID: ptr("caller-controlled"),
			}},
		},
		{
			name:  "tool_result",
			state: executing.State,
			input: agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResult, ToolResult: &agentprotocol.RuntimeInputToolResult{
				CallID: "call-1", Result: agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{"now": "caller result"}},
			}},
		},
		{
			name:  "approval_response allow",
			state: approvalPending.State,
			input: agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeApprovalResponse, ApprovalResponse: &agentprotocol.RuntimeInputApprovalResponse{
				ApprovalID: approvalID, Decision: agentprotocol.RuntimeInputApprovalResponseDecisionAllow,
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := agentprovider.NewScriptedProvider(nil)
			toolInvocations := 0
			approvalRequests := 0
			binding := driverClockBinding(map[string]any{"now": "09:00"})
			binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
				toolInvocations++
				return agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{"now": "09:00"}}, nil
			}
			tools := &agenttool.Registry{}
			if err := tools.Register(binding); err != nil {
				t.Fatal(err)
			}
			approvals := driverApprovalFunc(func(context.Context, string, agentprotocol.ToolCall) (Decision, error) {
				approvalRequests++
				return DecisionAllow, nil
			})
			driver := newDriverForTest(t, provider, tools, approvals, agenttool.Policy{Allow: []string{"*"}, Deny: []string{"test.clock.read"}})

			trace, err := driver.Run(context.Background(), test.state, test.input)
			if err != nil {
				t.Fatal(err)
			}
			want := agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProtocolIncompatible, Message: "Driver initial input must be user_message, cancel, or runtime_error", Retryable: false}
			wantInputs := []agentprotocol.RuntimeInput{{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &want}}
			wantEffects := []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeFailRun, Error: &want}}
			if provider.RequestCount() != 0 || toolInvocations != 0 || approvalRequests != 0 || trace.ModelTurns != 0 || trace.State.Phase != agentprotocol.RuntimePhaseFailed || !reflect.DeepEqual(trace.State.Error, &want) {
				t.Fatalf("provider = %d, Tool = %d, approval = %d, trace = %#v", provider.RequestCount(), toolInvocations, approvalRequests, trace)
			}
			if !reflect.DeepEqual(trace.Inputs, wantInputs) || !reflect.DeepEqual(trace.Effects, wantEffects) {
				t.Fatalf("inputs = %#v, effects = %#v", trace.Inputs, trace.Effects)
			}
		})
	}
}

func TestDriverNormalizesMalformedAllowedInitialInputsIntoTerminalTraces(t *testing.T) {
	tests := []struct {
		name  string
		input agentprotocol.RuntimeInput
		want  agentprotocol.AgentError
	}{
		{
			name:  "missing user_message payload",
			input: agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage},
			want:  agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProtocolIncompatible, Message: "invalid user_message payload", Retryable: false},
		},
		{
			name: "generic invalid runtime_error payload",
			input: agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &agentprotocol.AgentError{
				Code: agentprotocol.ErrorCode("future_error"), Message: "invalid", Retryable: false,
			}},
			want: agentprotocol.AgentError{Code: agentprotocol.ErrorCodeValidationFailed, Message: "invalid RuntimeInput", Retryable: false},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := agentprovider.NewScriptedProvider(nil)
			driver := newDriverForTest(t, provider, &agenttool.Registry{}, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

			trace, err := driver.Run(context.Background(), driverTestState(), test.input)
			if err != nil {
				t.Fatal(err)
			}

			wantInputs := []agentprotocol.RuntimeInput{{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &test.want}}
			wantEffects := []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeFailRun, Error: &test.want}}
			if provider.RequestCount() != 0 || trace.ModelTurns != 0 || trace.State.Phase != agentprotocol.RuntimePhaseFailed || trace.State.Sequence != 1 || !reflect.DeepEqual(trace.State.Error, &test.want) {
				t.Fatalf("trace = %#v", trace)
			}
			if !reflect.DeepEqual(trace.Inputs, wantInputs) || !reflect.DeepEqual(trace.Effects, wantEffects) {
				t.Fatalf("inputs = %#v, effects = %#v", trace.Inputs, trace.Effects)
			}
		})
	}
}

func TestDriverDrivesModelToToolAndBackWithoutHTTP(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{
			{Type: agentprotocol.ProviderEventTypeTextDelta, Text: ptr("done")},
			{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 8, OutputTokens: 2, TotalTokens: 10}},
		},
	})
	tools := &agenttool.Registry{}
	if err := tools.Register(driverClockBinding(map[string]any{"now": "09:00"})); err != nil {
		t.Fatal(err)
	}
	driver, err := NewDriver(DriverConfig{
		Provider: provider, Tools: tools, Approvals: allowDriverApproval(),
		ModelProfile: "server/default", Policy: agenttool.Policy{Allow: []string{"*"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("time?")})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"request_model_turn", "resolve_tool", "execute_tool", "request_model_turn", "emit_text", "complete_run"}
	if trace.ModelTurns != 2 || trace.State.Phase != agentprotocol.RuntimePhaseCompleted || !slices.Equal(effectTypes(trace.Effects), want) {
		t.Fatalf("trace = %#v", trace)
	}
}

type cancelAfterFirstProvider struct {
	inner   agentprovider.StreamProvider
	cancel  context.CancelFunc
	yielded int
}

func (p *cancelAfterFirstProvider) Stream(ctx context.Context, request agentprotocol.ModelTurnRequest) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		p.inner.Stream(ctx, request)(func(event agentprotocol.ProviderEvent, err error) bool {
			p.yielded++
			keepGoing := yield(event, err)
			if p.yielded == 1 {
				p.cancel()
			}
			return keepGoing
		})
	}
}

func TestDriverCancellationStopsFirstProviderStreamBeforeLaterScriptedEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inner := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{{
		{Type: agentprotocol.ProviderEventTypeTextDelta, Text: ptr("first")},
		{Type: agentprotocol.ProviderEventTypeTextDelta, Text: ptr("must not be consumed")},
		{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}},
	}})
	provider := &cancelAfterFirstProvider{inner: inner, cancel: cancel}
	driver, err := NewDriver(DriverConfig{
		Provider: provider, Tools: &agenttool.Registry{}, Approvals: allowDriverApproval(),
		ModelProfile: "server/default", Policy: agenttool.Policy{Allow: []string{"*"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	trace, err := driver.Run(ctx, driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("cancel stream")})
	if err != nil {
		t.Fatal(err)
	}

	if trace.State.Phase != agentprotocol.RuntimePhaseCancelled || provider.yielded != 1 || inner.RequestCount() != 1 {
		t.Fatalf("trace = %#v, yielded = %d, requests = %d", trace, provider.yielded, inner.RequestCount())
	}
	if got := effectTypes(trace.Effects); !slices.Equal(got, []string{"request_model_turn", "emit_text", "cancel_run"}) {
		t.Fatalf("effect types = %v", got)
	}
}

func TestDriverFeedsOutputSchemaFailureBackAsValidatedToolResult(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	tools := &agenttool.Registry{}
	if err := tools.Register(driverClockBinding(map[string]any{"now": "09:00", "extra": "invalid"})); err != nil {
		t.Fatal(err)
	}
	driver, err := NewDriver(DriverConfig{Provider: provider, Tools: tools, Approvals: allowDriverApproval(), ModelProfile: "server/default", Policy: agenttool.Policy{Allow: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("time?")})
	if err != nil {
		t.Fatal(err)
	}

	want := agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeValidationFailed, Message: "tool output schema validation failed", Retryable: false}}
	if got := trace.Inputs[4].ToolResult.Result; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool result = %#v, want %#v", got, want)
	}
}

func TestDriverExecutesBuiltinCalendarToolOnlyForValidInput(t *testing.T) {
	valid := map[string]any{
		"start": "2026-09-05T00:00:00Z", "end": "2026-09-06T00:00:00Z",
		"cursor": strings.Repeat("日", 1_365),
	}
	tests := []struct {
		name        string
		input       map[string]any
		invocations int
	}{
		{name: "valid", input: valid, invocations: 1},
		{name: "invalid time", input: copyMapWithAny(valid, "start", "not-a-time"), invocations: 0},
		{name: "cursor bytes", input: copyMapWithAny(valid, "cursor", strings.Repeat("日", 1_366)), invocations: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec, err := agentassets.CalendarReadSpec()
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			binding := driverTestBinding{spec: spec, invoke: func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
				calls++
				return agentprotocol.ToolResult{Ok: true, Data: calendarDriverData()}, nil
			}}
			tools := &agenttool.Registry{}
			if err := tools.Register(binding); err != nil {
				t.Fatal(err)
			}
			driver := newDriverForTest(t, calendarDriverProvider(test.input), tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"dayorder.calendar.read"}})
			trace, err := driver.Run(context.Background(), calendarDriverState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("calendar")})
			if err != nil {
				t.Fatal(err)
			}
			if calls != test.invocations || trace.Inputs[4].ToolResult.Result.Ok != (test.invocations == 1) {
				t.Fatalf("calls = %d, result = %#v", calls, trace.Inputs[4].ToolResult.Result)
			}
		})
	}
}

func TestDriverNormalizesInvalidBuiltinCalendarOutput(t *testing.T) {
	spec, err := agentassets.CalendarReadSpec()
	if err != nil {
		t.Fatal(err)
	}
	data := calendarDriverData()
	data["events"].([]any)[0].(map[string]any)["id"] = "not-a-uuid"
	binding := driverTestBinding{spec: spec, invoke: func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
		return agentprotocol.ToolResult{Ok: true, Data: data}, nil
	}}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	provider := calendarDriverProvider(map[string]any{"start": "2026-09-05T00:00:00Z", "end": "2026-09-06T00:00:00Z"})
	driver := newDriverForTest(t, provider, tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"dayorder.calendar.read"}})
	trace, err := driver.Run(context.Background(), calendarDriverState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("calendar")})
	if err != nil {
		t.Fatal(err)
	}
	want := agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeValidationFailed, Message: "tool output schema validation failed", Retryable: false}}
	if !reflect.DeepEqual(trace.Inputs[4].ToolResult.Result, want) {
		t.Fatalf("result = %#v, want %#v", trace.Inputs[4].ToolResult.Result, want)
	}
}

func copyMapWithAny(source map[string]any, key string, value any) map[string]any {
	copied := make(map[string]any, len(source)+1)
	for sourceKey, sourceValue := range source {
		copied[sourceKey] = sourceValue
	}
	copied[key] = value
	return copied
}

func TestDriverRejectsInvalidToolInputBeforeInvokingBinding(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{"unexpected": true}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	invocations := 0
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
		invocations++
		return agentprotocol.ToolResult{Ok: true, Data: map[string]any{"now": "09:00"}}, nil
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver, err := NewDriver(DriverConfig{Provider: provider, Tools: tools, Approvals: allowDriverApproval(), ModelProfile: "server/default", Policy: agenttool.Policy{Allow: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("bad input")})
	if err != nil {
		t.Fatal(err)
	}

	want := agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeValidationFailed, Message: "tool input schema validation failed", Retryable: false}}
	if invocations != 0 || !reflect.DeepEqual(trace.Inputs[4].ToolResult.Result, want) {
		t.Fatalf("invocations = %d, tool result = %#v", invocations, trace.Inputs[4].ToolResult.Result)
	}
}

func TestDriverNormalizesProtocolInvalidBindingResult(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	// Keep the cross-host normalization order stable: a protocol-invalid
	// Binding result is not reclassified by the byte limit.
	binding.spec.ResultMaxBytes = 1
	binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
		return agentprotocol.ToolResult{Ok: true}, nil
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver, err := NewDriver(DriverConfig{Provider: provider, Tools: tools, Approvals: allowDriverApproval(), ModelProfile: "server/default", Policy: agenttool.Policy{Allow: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("invalid result")})
	if err != nil {
		t.Fatal(err)
	}

	want := agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeValidationFailed, Message: "invalid ToolResult", Retryable: false}}
	if got := trace.Inputs[4].ToolResult.Result; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool result = %#v, want %#v", got, want)
	}
}

func TestDriverEnforcesToolResultByteLimit(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	binding := driverClockBinding(map[string]any{"now": strings.Repeat("时", 80)})
	binding.spec.ResultMaxBytes = 100
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver, err := NewDriver(DriverConfig{Provider: provider, Tools: tools, Approvals: allowDriverApproval(), ModelProfile: "server/default", Policy: agenttool.Policy{Allow: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("large result")})
	if err != nil {
		t.Fatal(err)
	}

	want := agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeValidationFailed, Message: "tool result exceeds resultMaxBytes", Retryable: false}}
	if got := trace.Inputs[4].ToolResult.Result; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool result = %#v, want %#v", got, want)
	}
}

func TestDriverExecutesExactlyOnceAfterAllowedDeterministicApproval(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	invocations := 0
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	binding.spec.ApprovalPolicy = agentprotocol.ToolSpecApprovalPolicyIfNeeded
	binding.spec.SideEffect = agentprotocol.SideEffectReversibleWrite
	binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
		invocations++
		return agentprotocol.ToolResult{Ok: true, Data: map[string]any{"now": "09:00"}}, nil
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	var gotApprovalID string
	var gotCall agentprotocol.ToolCall
	approvals := driverApprovalFunc(func(_ context.Context, approvalID string, call agentprotocol.ToolCall) (Decision, error) {
		gotApprovalID, gotCall = approvalID, call
		return DecisionAllow, nil
	})
	driver, err := NewDriver(DriverConfig{
		Provider: provider, Tools: tools, Approvals: approvals, ModelProfile: "server/default",
		Policy: agenttool.Policy{Allow: []string{"*"}, ApprovalFor: []agentprotocol.SideEffect{agentprotocol.SideEffectReversibleWrite}},
	})
	if err != nil {
		t.Fatal(err)
	}

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("approve?")})
	if err != nil {
		t.Fatal(err)
	}

	wantEffects := []string{"request_model_turn", "resolve_tool", "request_approval", "execute_tool", "request_model_turn", "complete_run"}
	if gotApprovalID != "approval-call-1" || gotCall.ID != "call-1" || invocations != 1 || !slices.Equal(effectTypes(trace.Effects), wantEffects) {
		t.Fatalf("approval = %q %#v, invocations = %d, effects = %v", gotApprovalID, gotCall, invocations, effectTypes(trace.Effects))
	}
}

func TestDriverNormalizesInvalidApprovalDecisionWithoutExecuting(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{{
		{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}},
		completedToolUseEvent(),
	}})
	invocations := 0
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	binding.spec.ApprovalPolicy = agentprotocol.ToolSpecApprovalPolicyAlways
	binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
		invocations++
		return agentprotocol.ToolResult{Ok: true, Data: map[string]any{"now": "09:00"}}, nil
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver, err := NewDriver(DriverConfig{
		Provider: provider, Tools: tools,
		Approvals:    driverApprovalFunc(func(context.Context, string, agentprotocol.ToolCall) (Decision, error) { return Decision("later"), nil }),
		ModelProfile: "server/default", Policy: agenttool.Policy{Allow: []string{"*"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("invalid approval")})
	if err != nil {
		t.Fatal(err)
	}

	wantError := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProtocolIncompatible, Message: "invalid ApprovalBroker decision", Retryable: false}
	if invocations != 0 || trace.State.Phase != agentprotocol.RuntimePhaseFailed || !reflect.DeepEqual(trace.State.Error, wantError) {
		t.Fatalf("invocations = %d, trace = %#v", invocations, trace)
	}
}

func TestDriverCancellationPropagatesToCooperativeToolBinding(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{{
		{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}},
		completedToolUseEvent(),
	}})
	started := make(chan struct{})
	observed := make(chan error, 1)
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	binding.invoke = func(ctx context.Context, _ map[string]any, _ agenttool.Context) (agentprotocol.ToolResult, error) {
		close(started)
		<-ctx.Done()
		observed <- ctx.Err()
		return agentprotocol.ToolResult{Ok: true, Data: map[string]any{"now": "09:00"}}, nil
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver, err := NewDriver(DriverConfig{Provider: provider, Tools: tools, Approvals: allowDriverApproval(), ModelProfile: "server/default", Policy: agenttool.Policy{Allow: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		trace Trace
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		trace, runErr := driver.Run(ctx, driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("cancel tool")})
		done <- outcome{trace: trace, err: runErr}
	}()
	<-started
	cancel()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run remained blocked after the Tool Binding observed cancellation")
	}
	if got.err != nil || got.trace.State.Phase != agentprotocol.RuntimePhaseCancelled {
		t.Fatalf("Run() trace = %#v, error = %v", got.trace, got.err)
	}
	if err := <-observed; !errors.Is(err, context.Canceled) {
		t.Fatalf("Tool Binding context error = %v, want context.Canceled", err)
	}
}

func TestDriverCancellationInterruptsNonCooperativeApprovalBroker(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{{
		{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}},
		completedToolUseEvent(),
	}})
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	binding.spec.ApprovalPolicy = agentprotocol.ToolSpecApprovalPolicyAlways
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	approvals := driverApprovalFunc(func(context.Context, string, agentprotocol.ToolCall) (Decision, error) {
		close(started)
		<-release
		close(finished)
		return DecisionAllow, nil
	})
	driver, err := NewDriver(DriverConfig{Provider: provider, Tools: tools, Approvals: approvals, ModelProfile: "server/default", Policy: agenttool.Policy{Allow: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		trace Trace
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		trace, runErr := driver.Run(ctx, driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("cancel approval")})
		done <- outcome{trace: trace, err: runErr}
	}()
	<-started
	cancel()

	var got outcome
	timedOut := false
	select {
	case got = <-done:
	case <-time.After(200 * time.Millisecond):
		timedOut = true
	}
	close(release)
	<-finished
	if timedOut {
		<-done
		t.Fatal("Run remained blocked in a non-cooperative ApprovalBroker")
	}
	if got.err != nil || got.trace.State.Phase != agentprotocol.RuntimePhaseCancelled {
		t.Fatalf("Run() trace = %#v, error = %v", got.trace, got.err)
	}
}

func TestNewDriverRejectsMissingOrURLLikeModelProfile(t *testing.T) {
	for _, profile := range []string{"", "   ", "https://provider.example/model", "HTTP://provider.example/model", "https:provider.example/model", "//provider.example/model"} {
		t.Run(profile, func(t *testing.T) {
			_, err := NewDriver(DriverConfig{
				Provider: agentprovider.NewScriptedProvider(nil), Tools: &agenttool.Registry{},
				Approvals: allowDriverApproval(), ModelProfile: profile, Policy: agenttool.Policy{Allow: []string{"*"}},
			})
			if err == nil {
				t.Fatalf("NewDriver() accepted model profile %q", profile)
			}
		})
	}
}

func TestNewDriverRejectsMissingRuntimeDependencies(t *testing.T) {
	valid := DriverConfig{
		Provider: agentprovider.NewScriptedProvider(nil), Tools: &agenttool.Registry{},
		Approvals: allowDriverApproval(), ModelProfile: "server/default", Policy: agenttool.Policy{Allow: []string{"*"}},
	}
	tests := []struct {
		name   string
		mutate func(*DriverConfig)
	}{
		{name: "Provider", mutate: func(config *DriverConfig) { config.Provider = nil }},
		{name: "typed nil Provider", mutate: func(config *DriverConfig) { var provider *agentprovider.ScriptedProvider; config.Provider = provider }},
		{name: "Tools", mutate: func(config *DriverConfig) { config.Tools = nil }},
		{name: "Approvals", mutate: func(config *DriverConfig) { config.Approvals = nil }},
		{name: "typed nil Approvals", mutate: func(config *DriverConfig) { var approvals driverApprovalFunc; config.Approvals = approvals }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if _, err := NewDriver(config); err == nil {
				t.Fatalf("NewDriver() accepted a nil %s", test.name)
			}
		})
	}
}

func TestNewDriverCopiesPolicyConfiguration(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	tools := &agenttool.Registry{}
	if err := tools.Register(driverClockBinding(map[string]any{"now": "09:00"})); err != nil {
		t.Fatal(err)
	}
	policy := agenttool.Policy{Allow: []string{"*"}, Deny: []string{}, ApprovalFor: []agentprotocol.SideEffect{}}
	driver, err := NewDriver(DriverConfig{Provider: provider, Tools: tools, Approvals: allowDriverApproval(), ModelProfile: "server/default", Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	policy.Allow[0] = "different.tool"

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("policy copy")})
	if err != nil {
		t.Fatal(err)
	}
	if trace.State.Phase != agentprotocol.RuntimePhaseCompleted {
		t.Fatalf("trace = %#v", trace)
	}
}

func TestDriverNormalizesMissingTaggedProviderEventPayload(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{{
		{Type: agentprotocol.ProviderEventTypeTextDelta},
	}})
	driver, err := NewDriver(DriverConfig{Provider: provider, Tools: &agenttool.Registry{}, Approvals: allowDriverApproval(), ModelProfile: "server/default", Policy: agenttool.Policy{Allow: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("invalid event")})
	if err != nil {
		t.Fatal(err)
	}

	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProtocolIncompatible, Message: "invalid text_delta provider event payload", Retryable: false}
	if trace.State.Phase != agentprotocol.RuntimePhaseFailed || !reflect.DeepEqual(trace.State.Error, want) {
		t.Fatalf("trace = %#v", trace)
	}
}

func TestDriverNormalizesGenericMalformedProviderEventAsValidationFailed(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{{
		{
			Type:       agentprotocol.ProviderEventTypeCompleted,
			StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn),
			Usage:      &agentprotocol.Usage{InputTokens: -1, OutputTokens: 0, TotalTokens: 0},
		},
	}})
	driver := newDriverForTest(t, provider, &agenttool.Registry{}, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("invalid event")})
	if err != nil {
		t.Fatal(err)
	}

	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeValidationFailed, Message: "invalid ProviderEvent", Retryable: false}
	if trace.State.Phase != agentprotocol.RuntimePhaseFailed || !reflect.DeepEqual(trace.State.Error, want) {
		t.Fatalf("trace = %#v", trace)
	}
}

func TestDriverPreservesProviderStreamOrderInTraceAndAssistantText(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{{
		{Type: agentprotocol.ProviderEventTypeTextDelta, Text: ptr("first ")},
		{Type: agentprotocol.ProviderEventTypeTextDelta, Text: ptr("second")},
		{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}},
	}})
	driver := newDriverForTest(t, provider, &agenttool.Registry{}, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("order?")})
	if err != nil {
		t.Fatal(err)
	}

	wantInputs := []agentprotocol.RuntimeInput{
		{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("order?")},
		{Type: agentprotocol.RuntimeInputTypeProviderEvent, ProviderEvent: &agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeTextDelta, Text: ptr("first ")}},
		{Type: agentprotocol.RuntimeInputTypeProviderEvent, ProviderEvent: &agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeTextDelta, Text: ptr("second")}},
		{Type: agentprotocol.RuntimeInputTypeProviderEvent, ProviderEvent: &agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}},
	}
	wantEffects := []agentprotocol.RuntimeEffect{
		{Type: agentprotocol.RuntimeEffectTypeRequestModelTurn, TurnID: ptr("turn-1")},
		{Type: agentprotocol.RuntimeEffectTypeEmitText, Text: ptr("first ")},
		{Type: agentprotocol.RuntimeEffectTypeEmitText, Text: ptr("second")},
		{Type: agentprotocol.RuntimeEffectTypeCompleteRun},
	}
	wantMessage := agentprotocol.Message{Role: agentprotocol.MessageRoleAssistant, Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeText, Text: ptr("first second")}}}
	if !reflect.DeepEqual(trace.Inputs, wantInputs) || !reflect.DeepEqual(trace.Effects, wantEffects) || !reflect.DeepEqual(trace.State.Messages[len(trace.State.Messages)-1], wantMessage) {
		t.Fatalf("trace = %#v", trace)
	}
}

func TestDriverBuildsModelRequestFromProfileMessagesAndEffectiveToolSpecsOnly(t *testing.T) {
	var requests []agentprotocol.ModelTurnRequest
	provider := driverProviderFunc(func(_ context.Context, request agentprotocol.ModelTurnRequest) iter.Seq2[agentprotocol.ProviderEvent, error] {
		requests = append(requests, request)
		return func(yield func(agentprotocol.ProviderEvent, error) bool) {
			yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil)
		}
	})
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver, err := NewDriver(DriverConfig{Provider: provider, Tools: tools, Approvals: allowDriverApproval(), ModelProfile: "server/approved-profile", Policy: agenttool.Policy{Allow: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("request?")})
	if err != nil {
		t.Fatal(err)
	}
	if trace.State.Phase != agentprotocol.RuntimePhaseCompleted || len(requests) != 1 {
		t.Fatalf("trace = %#v, requests = %#v", trace, requests)
	}
	want := agentprotocol.ModelTurnRequest{
		ProtocolVersion: "2.0", RunID: "run-1", TurnID: "turn-1", ModelProfile: "server/approved-profile",
		Messages: []agentprotocol.Message{{Role: agentprotocol.MessageRoleUser, Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeText, Text: ptr("request?")}}}},
		Tools:    []agentprotocol.ToolSpec{binding.spec},
	}
	if !reflect.DeepEqual(requests[0], want) {
		t.Fatalf("request = %#v, want %#v", requests[0], want)
	}
	raw, err := json.Marshal(requests[0])
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"messages", "modelProfile", "protocolVersion", "runId", "tools", "turnId"}) {
		t.Fatalf("request keys = %v", keys)
	}
}

func TestDriverFailsMissingBindingAsCapabilityUnavailable(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{{
		{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}},
		completedToolUseEvent(),
	}})
	driver := newDriverForTest(t, provider, &agenttool.Registry{}, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("missing")})
	if err != nil {
		t.Fatal(err)
	}

	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeCapabilityUnavailable, Message: "test.clock.read is unavailable", Retryable: false}
	if trace.State.Phase != agentprotocol.RuntimePhaseFailed || !reflect.DeepEqual(trace.State.Error, want) || !slices.Equal(effectTypes(trace.Effects), []string{"request_model_turn", "resolve_tool", "fail_run"}) {
		t.Fatalf("trace = %#v", trace)
	}
}

func TestDriverDeniesIneffectiveToolBeforeBindingInvocation(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{{
		{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}},
		completedToolUseEvent(),
	}})
	invocations := 0
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
		invocations++
		return agentprotocol.ToolResult{Ok: true, Data: map[string]any{"now": "09:00"}}, nil
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver := newDriverForTest(t, provider, tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}, Deny: []string{"test.clock.read"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("denied")})
	if err != nil {
		t.Fatal(err)
	}

	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodePermissionDenied, Message: "tool denied: test.clock.read", Retryable: false}
	if invocations != 0 || !reflect.DeepEqual(trace.State.Error, want) {
		t.Fatalf("invocations = %d, trace = %#v", invocations, trace)
	}
}

func TestDriverReturnsApprovalDenialToModelWithoutExecutingTool(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	invocations := 0
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	binding.spec.ApprovalPolicy = agentprotocol.ToolSpecApprovalPolicyAlways
	binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
		invocations++
		return agentprotocol.ToolResult{Ok: true, Data: map[string]any{"now": "09:00"}}, nil
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	approvals := driverApprovalFunc(func(context.Context, string, agentprotocol.ToolCall) (Decision, error) { return DecisionDeny, nil })
	driver := newDriverForTest(t, provider, tools, approvals, agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("deny")})
	if err != nil {
		t.Fatal(err)
	}

	wantEffects := []string{"request_model_turn", "resolve_tool", "request_approval", "request_model_turn", "complete_run"}
	wantResult := &agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeApprovalDenied, Message: "approval denied", Retryable: false}}
	if invocations != 0 || trace.State.Phase != agentprotocol.RuntimePhaseCompleted || !slices.Equal(effectTypes(trace.Effects), wantEffects) || !reflect.DeepEqual(trace.State.Messages[2].Content[0].ToolResult, wantResult) {
		t.Fatalf("invocations = %d, trace = %#v", invocations, trace)
	}
}

func TestDriverNormalizesApprovalBrokerFailureWithoutLeakingDetails(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{{
		{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}},
		completedToolUseEvent(),
	}})
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	binding.spec.ApprovalPolicy = agentprotocol.ToolSpecApprovalPolicyAlways
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	approvals := driverApprovalFunc(func(context.Context, string, agentprotocol.ToolCall) (Decision, error) {
		return "", errors.New("private broker detail")
	})
	driver := newDriverForTest(t, provider, tools, approvals, agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("broker failure")})
	if err != nil {
		t.Fatal(err)
	}

	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeInternalError, Message: "approval request failed", Retryable: false}
	raw, _ := json.Marshal(trace)
	if !reflect.DeepEqual(trace.State.Error, want) || strings.Contains(string(raw), "private broker detail") {
		t.Fatalf("trace = %s", raw)
	}
}

func TestDriverNormalizesBindingFailureWithoutLeakingDetails(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
		return agentprotocol.ToolResult{}, errors.New("private Tool detail")
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver := newDriverForTest(t, provider, tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("tool failure")})
	if err != nil {
		t.Fatal(err)
	}

	want := agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeToolFailed, Message: "tool invocation failed", Retryable: false}}
	raw, _ := json.Marshal(trace)
	if !reflect.DeepEqual(trace.Inputs[4].ToolResult.Result, want) || strings.Contains(string(raw), "private Tool detail") {
		t.Fatalf("trace = %s", raw)
	}
}

func TestDriverNormalizesBindingPanicWithoutLeakingDetails(t *testing.T) {
	// Mutation caught: removing synchronous panic recovery from the trusted
	// Binding boundary would crash the Run or expose the private panic value.
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-panic", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
		panic("private Tool panic detail")
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver := newDriverForTest(t, provider, tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("panicking tool")})
	if err != nil {
		t.Fatal(err)
	}

	want := agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeToolFailed, Message: "tool invocation failed", Retryable: false}}
	raw, _ := json.Marshal(trace)
	if trace.State.Phase != agentprotocol.RuntimePhaseCompleted || !reflect.DeepEqual(trace.Inputs[4].ToolResult.Result, want) || strings.Contains(string(raw), "private Tool panic detail") {
		t.Fatalf("trace = %s", raw)
	}
}

func TestDriverKeepsBindingFailureAsToolFailedUnderSmallResultLimit(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	binding.spec.ResultMaxBytes = 1
	binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
		return agentprotocol.ToolResult{}, errors.New("private Tool detail")
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver := newDriverForTest(t, provider, tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("tool failure")})
	if err != nil {
		t.Fatal(err)
	}

	want := agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeToolFailed, Message: "tool invocation failed", Retryable: false}}
	if got := trace.Inputs[4].ToolResult.Result; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool result = %#v, want %#v", got, want)
	}
}

func TestDriverPreservesValidatedProviderErrorEvent(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{{
		{Type: agentprotocol.ProviderEventTypeError, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProviderRateLimited, Message: "provider rate limited", Retryable: true}},
	}})
	driver := newDriverForTest(t, provider, &agenttool.Registry{}, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("provider event failure")})
	if err != nil {
		t.Fatal(err)
	}

	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProviderRateLimited, Message: "provider rate limited", Retryable: true}
	if !reflect.DeepEqual(trace.State.Error, want) || !reflect.DeepEqual(trace.Effects[len(trace.Effects)-1].Error, want) {
		t.Fatalf("trace = %#v", trace)
	}
}

func TestDriverNormalizesProviderStreamFailureWithoutLeakingDetails(t *testing.T) {
	provider := driverProviderFunc(func(context.Context, agentprotocol.ModelTurnRequest) iter.Seq2[agentprotocol.ProviderEvent, error] {
		return func(yield func(agentprotocol.ProviderEvent, error) bool) {
			yield(agentprotocol.ProviderEvent{}, errors.New("private Provider detail"))
		}
	})
	driver := newDriverForTest(t, provider, &agenttool.Registry{}, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("provider error")})
	if err != nil {
		t.Fatal(err)
	}

	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProviderUnavailable, Message: "provider stream failed", Retryable: false}
	raw, _ := json.Marshal(trace)
	if !reflect.DeepEqual(trace.State.Error, want) || strings.Contains(string(raw), "private Provider detail") {
		t.Fatalf("trace = %s", raw)
	}
}

func TestDriverNormalizesProviderStreamConstructionPanic(t *testing.T) {
	provider := driverProviderFunc(func(context.Context, agentprotocol.ModelTurnRequest) iter.Seq2[agentprotocol.ProviderEvent, error] {
		panic("private Provider construction detail")
	})
	driver := newDriverForTest(t, provider, &agenttool.Registry{}, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("provider panic")})
	if err != nil {
		t.Fatal(err)
	}

	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProviderUnavailable, Message: "provider stream failed", Retryable: false}
	raw, _ := json.Marshal(trace)
	if !reflect.DeepEqual(trace.State.Error, want) || strings.Contains(string(raw), "private Provider construction detail") {
		t.Fatalf("trace = %s", raw)
	}
}

func TestDriverFailsWhenProviderStreamEndsBeforeTurnCompletion(t *testing.T) {
	provider := driverProviderFunc(func(context.Context, agentprotocol.ModelTurnRequest) iter.Seq2[agentprotocol.ProviderEvent, error] {
		return func(func(agentprotocol.ProviderEvent, error) bool) {}
	})
	driver := newDriverForTest(t, provider, &agenttool.Registry{}, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("empty stream")})
	if err != nil {
		t.Fatal(err)
	}

	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProviderUnavailable, Message: "provider stream ended before turn completion", Retryable: false}
	if !reflect.DeepEqual(trace.State.Error, want) {
		t.Fatalf("trace = %#v", trace)
	}
}

func TestDriverFailsDeterministicallyWhenScriptedProviderIsExhausted(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{{
		{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}},
		completedToolUseEvent(),
	}})
	tools := &agenttool.Registry{}
	if err := tools.Register(driverClockBinding(map[string]any{"now": "09:00"})); err != nil {
		t.Fatal(err)
	}
	driver := newDriverForTest(t, provider, tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("exhaust")})
	if err != nil {
		t.Fatal(err)
	}

	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProviderUnavailable, Message: "scripted provider exhausted", Retryable: false}
	if provider.RequestCount() != 2 || !reflect.DeepEqual(trace.State.Error, want) || !slices.Equal(effectTypes(trace.Effects), []string{"request_model_turn", "resolve_tool", "execute_tool", "request_model_turn", "fail_run"}) {
		t.Fatalf("requests = %d, trace = %#v", provider.RequestCount(), trace)
	}
}

func TestDriverCancelsBeforeStartingProviderWhenCallerAlreadyCancelled(t *testing.T) {
	provider := agentprovider.NewScriptedProvider(nil)
	driver := newDriverForTest(t, provider, &agenttool.Registry{}, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	trace, err := driver.Run(ctx, driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("do not start")})
	if err != nil {
		t.Fatal(err)
	}

	if provider.RequestCount() != 0 || trace.ModelTurns != 0 || trace.State.Phase != agentprotocol.RuntimePhaseCancelled || !slices.Equal(effectTypes(trace.Effects), []string{"cancel_run"}) {
		t.Fatalf("requests = %d, trace = %#v", provider.RequestCount(), trace)
	}
}

func TestDriverClassifiesNativeCallerDeadlineBeforeStartingProvider(t *testing.T) {
	provider := agentprovider.NewScriptedProvider(nil)
	driver := newDriverForTest(t, provider, &agenttool.Registry{}, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	trace, err := driver.Run(ctx, driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("do not start")})
	if err != nil {
		t.Fatal(err)
	}

	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeTimeout, Message: "runtime deadline exceeded", Retryable: false}
	if provider.RequestCount() != 0 || trace.ModelTurns != 0 || trace.State.Phase != agentprotocol.RuntimePhaseFailed || !reflect.DeepEqual(trace.State.Error, want) {
		t.Fatalf("requests = %d, trace = %#v", provider.RequestCount(), trace)
	}
}

type blockingDriverProvider struct {
	started   chan struct{}
	release   chan struct{}
	finished  chan struct{}
	delivered int
}

func newBlockingDriverProvider() *blockingDriverProvider {
	return &blockingDriverProvider{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
}

func (p *blockingDriverProvider) Stream(context.Context, agentprotocol.ModelTurnRequest) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		close(p.started)
		<-p.release
		if yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil) {
			p.delivered++
		}
		close(p.finished)
	}
}

func TestDriverCancellationInterruptsNonCooperativeProviderWithoutLateSend(t *testing.T) {
	provider := newBlockingDriverProvider()
	driver := newDriverForTest(t, provider, &agenttool.Registry{}, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		trace Trace
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		trace, runErr := driver.Run(ctx, driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("cancel provider")})
		done <- outcome{trace: trace, err: runErr}
	}()
	<-provider.started
	cancel()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(200 * time.Millisecond):
		close(provider.release)
		<-provider.finished
		t.Fatal("Run remained blocked in a non-cooperative StreamProvider")
	}
	close(provider.release)
	<-provider.finished
	if got.err != nil || got.trace.State.Phase != agentprotocol.RuntimePhaseCancelled || provider.delivered != 0 {
		t.Fatalf("Run() trace = %#v, error = %v, delivered = %d", got.trace, got.err, provider.delivered)
	}
}

func TestDriverClassifiesArbitraryInterruptionWhileProviderWorkIsActive(t *testing.T) {
	provider := newBlockingDriverProvider()
	driver := newDriverForTest(t, provider, &agenttool.Registry{}, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})
	ctx, cancel := context.WithCancelCause(context.Background())
	type outcome struct {
		trace Trace
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		trace, runErr := driver.Run(ctx, driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("interrupt provider")})
		done <- outcome{trace: trace, err: runErr}
	}()
	<-provider.started
	cancel(errors.New("private Provider interruption"))

	var got outcome
	select {
	case got = <-done:
	case <-time.After(200 * time.Millisecond):
		close(provider.release)
		<-provider.finished
		t.Fatal("Run remained blocked after interruption")
	}
	close(provider.release)
	<-provider.finished
	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeInternalError, Message: "runtime interrupted", Retryable: false}
	if got.err != nil || !reflect.DeepEqual(got.trace.State.Error, want) || provider.delivered != 0 {
		t.Fatalf("Run() trace = %#v, error = %v, delivered = %d", got.trace, got.err, provider.delivered)
	}
}

func TestDriverDoesNotStartProviderAfterAbsoluteDeadline(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{{
		{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}},
	}})
	driver := newDriverWithDeadlineForTest(t, provider, &agenttool.Registry{}, time.Now().Add(-time.Millisecond))
	state := driverTestState()
	state.Budget.MaxDurationMs = 120_000

	trace, err := driver.Run(context.Background(), state, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("already expired")})
	if err != nil {
		t.Fatal(err)
	}
	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeTimeout, Message: "runtime deadline exceeded", Retryable: false}
	if provider.RequestCount() != 0 || trace.ModelTurns != 0 || trace.State.Phase != agentprotocol.RuntimePhaseFailed || !reflect.DeepEqual(trace.State.Error, want) {
		t.Fatalf("requests = %d, trace = %#v", provider.RequestCount(), trace)
	}
}

func TestDriverUsesTwentyMillisecondsRemainingDeadlineInsteadOfBudget(t *testing.T) {
	provider := newBlockingDriverProvider()
	driver := newDriverWithDeadlineForTest(t, provider, &agenttool.Registry{}, time.Now().Add(20*time.Millisecond))
	state := driverTestState()
	state.Budget.MaxDurationMs = 120_000
	type outcome struct {
		trace Trace
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		trace, runErr := driver.Run(context.Background(), state, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("remaining deadline")})
		done <- outcome{trace: trace, err: runErr}
	}()
	select {
	case <-provider.started:
	case early := <-done:
		t.Fatalf("Provider did not start with time remaining: trace = %#v, error = %v", early.trace, early.err)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Provider did not start with time remaining")
	}

	var got outcome
	select {
	case got = <-done:
	case <-time.After(300 * time.Millisecond):
		close(provider.release)
		<-provider.finished
		t.Fatal("Run replaced the absolute deadline with budget.maxDurationMs")
	}
	close(provider.release)
	<-provider.finished
	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeTimeout, Message: "runtime deadline exceeded", Retryable: false}
	if got.err != nil || !reflect.DeepEqual(got.trace.State.Error, want) || provider.delivered != 0 {
		t.Fatalf("Run() trace = %#v, error = %v, delivered = %d", got.trace, got.err, provider.delivered)
	}
}

func TestDriverBudgetDeadlineInterruptsNonCooperativeProviderAsTimeout(t *testing.T) {
	provider := newBlockingDriverProvider()
	driver := newDriverForTest(t, provider, &agenttool.Registry{}, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})
	state := driverTestState()
	state.Budget.MaxDurationMs = 10
	type outcome struct {
		trace Trace
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		trace, runErr := driver.Run(context.Background(), state, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("deadline")})
		done <- outcome{trace: trace, err: runErr}
	}()
	<-provider.started

	var got outcome
	select {
	case got = <-done:
	case <-time.After(300 * time.Millisecond):
		close(provider.release)
		<-provider.finished
		t.Fatal("Run ignored budget.maxDurationMs")
	}
	close(provider.release)
	<-provider.finished
	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeTimeout, Message: "runtime deadline exceeded", Retryable: false}
	if got.err != nil || !reflect.DeepEqual(got.trace.State.Error, want) || provider.delivered != 0 {
		t.Fatalf("Run() trace = %#v, error = %v, delivered = %d", got.trace, got.err, provider.delivered)
	}
}

func TestDriverInvokesTrustedToolBindingOnRunGoroutine(t *testing.T) {
	// Mutation caught: wrapping an in-process ToolBinding in awaitCall or any
	// other goroutine severs Runtime ownership and can strand that goroutine.
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-stack", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	var invocationStack string
	binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
		buffer := make([]byte, 64<<10)
		invocationStack = string(buffer[:runtime.Stack(buffer, false)])
		return agentprotocol.ToolResult{Ok: true, Data: map[string]any{"now": "09:00"}}, nil
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver := newDriverForTest(t, provider, tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("owned tool")})
	if err != nil {
		t.Fatal(err)
	}
	if trace.State.Phase != agentprotocol.RuntimePhaseCompleted {
		t.Fatalf("Run phase = %q, want completed", trace.State.Phase)
	}
	if !strings.Contains(invocationStack, "(*runDriver).executeTool(") {
		t.Fatalf("Tool Binding ran outside the Run goroutine:\n%s", invocationStack)
	}
}

func TestDriverEnforcesPerToolTimeoutForCooperativeBinding(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-timeout", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	binding.spec.TimeoutMs = 5
	binding.invoke = func(ctx context.Context, _ map[string]any, _ agenttool.Context) (agentprotocol.ToolResult, error) {
		<-ctx.Done()
		return agentprotocol.ToolResult{Ok: true, Data: map[string]any{"now": "too late"}}, nil
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver := newDriverForTest(t, provider, tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})
	state := driverTestState()
	state.Budget.MaxDurationMs = 100

	trace, err := driver.Run(context.Background(), state, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("bounded tool")})
	if err != nil {
		t.Fatal(err)
	}
	wantResult := agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeTimeout, Message: "tool deadline exceeded", Retryable: false}}
	if trace.State.Phase != agentprotocol.RuntimePhaseCompleted || len(trace.Inputs) < 5 || trace.Inputs[4].ToolResult == nil || !reflect.DeepEqual(trace.Inputs[4].ToolResult.Result, wantResult) {
		t.Fatalf("trace = %#v", trace)
	}
	wantEffects := []string{"request_model_turn", "resolve_tool", "execute_tool", "request_model_turn", "complete_run"}
	if !slices.Equal(effectTypes(trace.Effects), wantEffects) {
		t.Fatalf("effects = %v, want %v", effectTypes(trace.Effects), wantEffects)
	}
}

func TestDriverRunDeadlineTakesPrecedenceDuringCooperativeToolCall(t *testing.T) {
	// Mutation caught: classifying the child context deadline before the Run
	// deadline would incorrectly return a ToolResult and continue the Run.
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-run-deadline", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
	})
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	binding.spec.TimeoutMs = 1_000
	bindingStarted := false
	binding.invoke = func(ctx context.Context, _ map[string]any, _ agenttool.Context) (agentprotocol.ToolResult, error) {
		bindingStarted = true
		<-ctx.Done()
		return agentprotocol.ToolResult{Ok: true, Data: map[string]any{"now": "too late"}}, nil
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver := newDriverWithDeadlineForTest(t, provider, tools, time.Now().Add(100*time.Millisecond))
	state := driverTestState()
	state.Budget.MaxDurationMs = 120_000

	trace, err := driver.Run(context.Background(), state, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("run deadline")})
	if err != nil {
		t.Fatal(err)
	}
	want := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeTimeout, Message: "runtime deadline exceeded", Retryable: false}
	if trace.State.Phase != agentprotocol.RuntimePhaseFailed || !reflect.DeepEqual(trace.State.Error, want) {
		t.Fatalf("trace = %#v", trace)
	}
	if !bindingStarted {
		t.Fatal("Run deadline expired before the Tool Binding started")
	}
	for _, input := range trace.Inputs {
		if input.Type == agentprotocol.RuntimeInputTypeToolResult {
			t.Fatalf("Run deadline produced a ToolResult: %#v", input)
		}
	}
}

func TestScriptedProviderCopiesEventsAndStopsAfterCancellation(t *testing.T) {
	originalText := "original"
	original := agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeTextDelta, Text: &originalText}
	scripts := [][]agentprotocol.ProviderEvent{{original, original, {Type: agentprotocol.ProviderEventTypeTextDelta, Text: ptr("later")}}}
	provider := agentprovider.NewScriptedProvider(scripts)
	originalText = "caller mutation"
	scripts[0][0].Text = ptr("script mutation")
	ctx, cancel := context.WithCancel(context.Background())
	request := agentprotocol.ModelTurnRequest{ProtocolVersion: "2.0", RunID: "run-1", TurnID: "turn-1", ModelProfile: "server/default", Messages: []agentprotocol.Message{}, Tools: []agentprotocol.ToolSpec{}}
	var got []agentprotocol.ProviderEvent
	provider.Stream(ctx, request)(func(event agentprotocol.ProviderEvent, err error) bool {
		if err != nil {
			t.Errorf("Stream() error = %v", err)
			return false
		}
		got = append(got, event)
		if len(got) == 1 {
			*event.Text = "returned mutation"
		}
		if len(got) == 2 {
			cancel()
		}
		return true
	})

	if provider.RequestCount() != 1 || len(got) != 2 || *got[0].Text != "returned mutation" || *got[1].Text != "original" {
		t.Fatalf("requests = %d, events = %#v", provider.RequestCount(), got)
	}
}

func TestScriptedProviderTreatsPresentNilScriptAsEmptyBeforeExhaustion(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{nil})
	request := agentprotocol.ModelTurnRequest{ProtocolVersion: "2.0", RunID: "run-1", TurnID: "turn-1", ModelProfile: "server/default", Messages: []agentprotocol.Message{}, Tools: []agentprotocol.ToolSpec{}}
	var first []agentprotocol.ProviderEvent
	provider.Stream(context.Background(), request)(func(event agentprotocol.ProviderEvent, err error) bool {
		if err != nil {
			t.Fatal(err)
		}
		first = append(first, event)
		return true
	})
	var second []agentprotocol.ProviderEvent
	provider.Stream(context.Background(), request)(func(event agentprotocol.ProviderEvent, err error) bool {
		if err != nil {
			t.Fatal(err)
		}
		second = append(second, event)
		return true
	})

	if len(first) != 0 || len(second) != 1 || second[0].Error == nil || second[0].Error.Message != "scripted provider exhausted" || provider.RequestCount() != 2 {
		t.Fatalf("first = %#v, second = %#v, requests = %d", first, second, provider.RequestCount())
	}
}

type statefulJSONMarshaler struct {
	calls int
}

func (value *statefulJSONMarshaler) MarshalJSON() ([]byte, error) {
	value.calls++
	if value.calls == 1 {
		return []byte(`"canonical"`), nil
	}
	return []byte(`"private changed value that exceeds the limit"`), nil
}

func TestDriverCanonicalizesStatefulToolResultOnceAndUsesCompleteEnvelopeBytes(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	value := &statefulJSONMarshaler{}
	binding := driverClockBinding(map[string]any{"now": "ignored"})
	binding.spec.OutputSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"const": "canonical"},
		},
		"required":             []any{"value"},
		"additionalProperties": false,
	}
	binding.spec.ResultMaxBytes = len(`{"data":{"value":"canonical"},"ok":true}`)
	binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
		return agentprotocol.ToolResult{Ok: true, Data: map[string]any{"value": value}}, nil
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver := newDriverForTest(t, provider, tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("stateful result")})
	if err != nil {
		t.Fatal(err)
	}

	want := agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{"value": "canonical"}}
	raw, _ := json.Marshal(trace)
	if value.calls != 1 || provider.RequestCount() != 2 || trace.State.Phase != agentprotocol.RuntimePhaseCompleted || !reflect.DeepEqual(trace.Inputs[4].ToolResult.Result, want) || !reflect.DeepEqual(trace.State.Messages[2].Content[0].ToolResult, &want) || strings.Contains(string(raw), "private changed value") {
		t.Fatalf("marshals = %d, requests = %d, trace = %s", value.calls, provider.RequestCount(), raw)
	}
}

func TestDriverAppliesResultMaxBytesToCompleteToolResultEnvelopeAtExactBoundary(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "..", "contracts", "agent", "fixtures", "runtime", "tool-result-size.json")
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Result             agentprotocol.ToolResult `json:"result"`
		Canonical          string                   `json:"canonical"`
		CanonicalUTF8Bytes int                      `json:"canonicalUtf8Bytes"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	_, serialized, ok := canonicalizeToolResult(fixture.Result)
	if !ok || string(serialized) != fixture.Canonical || len(serialized) != fixture.CanonicalUTF8Bytes {
		t.Fatalf("fixture canonical = %q (%d bytes), want %q (%d bytes)", serialized, len(serialized), fixture.Canonical, fixture.CanonicalUTF8Bytes)
	}

	run := func(limit int) Trace {
		provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
			{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-size", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
			{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
		})
		binding := driverClockBinding(map[string]any{"now": "unused"})
		binding.spec.OutputSchema = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": "string"},
			},
			"required":             []any{"value"},
			"additionalProperties": false,
		}
		binding.spec.ResultMaxBytes = limit
		binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
			return fixture.Result, nil
		}
		tools := &agenttool.Registry{}
		if err := tools.Register(binding); err != nil {
			t.Fatal(err)
		}
		driver := newDriverForTest(t, provider, tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})
		trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("measure the result envelope")})
		if err != nil {
			t.Fatal(err)
		}
		return trace
	}

	exact := run(fixture.CanonicalUTF8Bytes)
	if len(exact.Inputs) < 5 || exact.Inputs[4].ToolResult == nil || !reflect.DeepEqual(exact.Inputs[4].ToolResult.Result, fixture.Result) {
		t.Fatalf("exact-boundary trace = %#v", exact)
	}
	below := run(fixture.CanonicalUTF8Bytes - 1)
	want := agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeValidationFailed, Message: "tool result exceeds resultMaxBytes", Retryable: false}}
	if len(below.Inputs) < 5 || below.Inputs[4].ToolResult == nil || !reflect.DeepEqual(below.Inputs[4].ToolResult.Result, want) {
		t.Fatalf("below-boundary trace = %#v", below)
	}
}

type panicJSONMarshaler struct {
	calls int
}

func (value *panicJSONMarshaler) MarshalJSON() ([]byte, error) {
	value.calls++
	panic("private serialization panic")
}

func TestDriverNormalizesPanickingToolResultSerialization(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	value := &panicJSONMarshaler{}
	binding := driverClockBinding(map[string]any{"now": "ignored"})
	binding.spec.OutputSchema = map[string]any{"type": "object", "additionalProperties": true}
	binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
		return agentprotocol.ToolResult{Ok: true, Data: map[string]any{"value": value}}, nil
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver := newDriverForTest(t, provider, tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("panicking result")})
	if err != nil {
		t.Fatal(err)
	}

	want := agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeValidationFailed, Message: "tool result is not JSON serializable", Retryable: false}}
	raw, _ := json.Marshal(trace)
	if value.calls != 1 || !reflect.DeepEqual(trace.Inputs[4].ToolResult.Result, want) || strings.Contains(string(raw), "private serialization panic") {
		t.Fatalf("marshals = %d, trace = %s", value.calls, raw)
	}
}

type marshalErrorJSONMarshaler struct {
	calls int
}

func (value *marshalErrorJSONMarshaler) MarshalJSON() ([]byte, error) {
	value.calls++
	return nil, errors.New("private serialization detail")
}

func TestDriverRejectsToolResultThatCannotCrossJSONOutputBoundary(t *testing.T) {
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	value := &marshalErrorJSONMarshaler{}
	binding := driverClockBinding(map[string]any{"now": "ignored"})
	binding.spec.OutputSchema = map[string]any{"type": "object", "additionalProperties": true}
	binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
		return agentprotocol.ToolResult{Ok: true, Data: map[string]any{"value": value}}, nil
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	driver := newDriverForTest(t, provider, tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("non-json result")})
	if err != nil {
		t.Fatal(err)
	}

	want := agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeValidationFailed, Message: "tool result is not JSON serializable", Retryable: false}}
	raw, _ := json.Marshal(trace)
	if value.calls != 1 || !reflect.DeepEqual(trace.Inputs[4].ToolResult.Result, want) || strings.Contains(string(raw), "private serialization detail") {
		t.Fatalf("marshals = %d, trace = %s", value.calls, raw)
	}
}

type rawJSONMarshaler struct {
	calls int
	raw   string
}

func (value *rawJSONMarshaler) MarshalJSON() ([]byte, error) {
	value.calls++
	return []byte(value.raw), nil
}

func TestDriverNormalizesInvalidAndUndecodableToolResultJSON(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "invalid JSON", raw: `not-json`},
		{name: "undecodable number", raw: `1e1000`},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
				{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}}, completedToolUseEvent()},
				{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
			})
			value := &rawJSONMarshaler{raw: test.raw}
			binding := driverClockBinding(map[string]any{"now": "ignored"})
			binding.spec.OutputSchema = map[string]any{"type": "object", "additionalProperties": true}
			binding.invoke = func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
				return agentprotocol.ToolResult{Ok: true, Data: map[string]any{"value": value}}, nil
			}
			tools := &agenttool.Registry{}
			if err := tools.Register(binding); err != nil {
				t.Fatal(err)
			}
			driver := newDriverForTest(t, provider, tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})

			trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("hostile result")})
			if err != nil {
				t.Fatal(err)
			}

			want := agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeValidationFailed, Message: "tool result is not JSON serializable", Retryable: false}}
			if value.calls != 1 || !reflect.DeepEqual(trace.Inputs[4].ToolResult.Result, want) {
				t.Fatalf("marshals = %d, result = %#v", value.calls, trace.Inputs[4].ToolResult.Result)
			}
		})
	}
}

func TestDriverPassesRunContextAndPreservesCallIdentifiersAcrossHostBoundaries(t *testing.T) {
	requestCount := 0
	var providerContexts []context.Context
	provider := driverProviderFunc(func(ctx context.Context, _ agentprotocol.ModelTurnRequest) iter.Seq2[agentprotocol.ProviderEvent, error] {
		providerContexts = append(providerContexts, ctx)
		requestCount++
		return func(yield func(agentprotocol.ProviderEvent, error) bool) {
			if requestCount == 1 {
				yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-ctx", Name: "test.clock.read", Input: map[string]any{}}}, nil)
				yield(completedToolUseEvent(), nil)
				return
			}
			yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil)
		}
	})
	var toolContext context.Context
	var toolIDs agenttool.Context
	binding := driverClockBinding(map[string]any{"now": "09:00"})
	binding.spec.ApprovalPolicy = agentprotocol.ToolSpecApprovalPolicyAlways
	binding.invoke = func(ctx context.Context, _ map[string]any, ids agenttool.Context) (agentprotocol.ToolResult, error) {
		toolContext, toolIDs = ctx, ids
		return agentprotocol.ToolResult{Ok: true, Data: map[string]any{"now": "09:00"}}, nil
	}
	tools := &agenttool.Registry{}
	if err := tools.Register(binding); err != nil {
		t.Fatal(err)
	}
	var brokerContext context.Context
	var approvalID string
	var approvalCall agentprotocol.ToolCall
	approvals := driverApprovalFunc(func(ctx context.Context, id string, call agentprotocol.ToolCall) (Decision, error) {
		brokerContext, approvalID, approvalCall = ctx, id, call
		return DecisionAllow, nil
	})
	driver := newDriverForTest(t, provider, tools, approvals, agenttool.Policy{Allow: []string{"*"}})

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("context")})
	if err != nil {
		t.Fatal(err)
	}

	if trace.State.Phase != agentprotocol.RuntimePhaseCompleted || len(providerContexts) != 2 || toolIDs.RunID != "run-1" || toolIDs.CallID != "call-ctx" || approvalID != "approval-call-ctx" || approvalCall.ID != "call-ctx" {
		t.Fatalf("trace = %#v, tool IDs = %#v, approval = %q %#v", trace, toolIDs, approvalID, approvalCall)
	}
	for name, hostContext := range map[string]context.Context{"Provider": providerContexts[0], "Tool": toolContext, "Approval": brokerContext} {
		if hostContext == nil {
			t.Fatalf("%s received nil Context", name)
		}
		if _, ok := hostContext.Deadline(); !ok {
			t.Fatalf("%s Context has no budget deadline", name)
		}
	}
}

func TestDriverTraceEntriesAndFinalStateAreIndependentCopies(t *testing.T) {
	scriptedCall := agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: map[string]any{}}
	provider := agentprovider.NewScriptedProvider([][]agentprotocol.ProviderEvent{
		{{Type: agentprotocol.ProviderEventTypeToolCall, Call: &scriptedCall}, completedToolUseEvent()},
		{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}},
	})
	scriptedCall.ID = "caller-mutated"
	resultData := map[string]any{"now": "09:00"}
	tools := &agenttool.Registry{}
	if err := tools.Register(driverClockBinding(resultData)); err != nil {
		t.Fatal(err)
	}
	driver := newDriverForTest(t, provider, tools, allowDriverApproval(), agenttool.Policy{Allow: []string{"*"}})
	inputText := "copy?"

	trace, err := driver.Run(context.Background(), driverTestState(), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: &inputText})
	if err != nil {
		t.Fatal(err)
	}
	inputText = "mutated after run"
	resultData["now"] = "mutated after run"
	trace.Effects[1].ToolCall.ID = "effect-mutated"
	trace.Inputs[3].ToolResolution.CallID = "input-mutated"

	if *trace.Inputs[0].Text != "copy?" || trace.Inputs[1].ProviderEvent.Call.ID != "call-1" || trace.Effects[2].ToolCall.ID != "call-1" || trace.State.Messages[1].Content[0].ToolCall.ID != "call-1" || trace.State.Messages[2].Content[0].ToolResult.Data["now"] != "09:00" {
		t.Fatalf("trace aliases caller or sibling values: %#v", trace)
	}
}

func TestLegacyHTTPProviderDoesNotImplementStreamProvider(t *testing.T) {
	var legacy any = (*agentprovider.HTTPProvider)(nil)
	if _, connected := legacy.(agentprovider.StreamProvider); connected {
		t.Fatal("legacy HTTPProvider unexpectedly implements the Phase 1 stream contract")
	}
}
