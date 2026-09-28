package agentruntime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"dayorder.local/api/internal/agentprotocol"
)

func ptr(value string) *string { return &value }

func testConfig() Config {
	return Config{
		RunID:         "run-1",
		ExecutionMode: agentprotocol.ExecutionModeForeground,
		CapabilitySnapshot: agentprotocol.CapabilitySnapshot{
			RuntimeVersion: "2.0.0", ExecutionMode: agentprotocol.ExecutionModeForeground,
			ToolIds: []string{"test.clock.read"}, Skills: []agentprotocol.SkillRef{},
			Scope: agentprotocol.AgentScope{Domains: []string{"test"}},
		},
		Budget: agentprotocol.Budget{MaxSteps: 4, MaxTokens: 100, MaxDurationMs: 30000, MaxWorkers: 1, MaxConcurrency: 1, MaxRepeatedToolCalls: 2},
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func startedState(t *testing.T) agentprotocol.RuntimeState {
	t.Helper()
	transition, err := Advance(NewState(testConfig()), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("plan today")})
	if err != nil {
		t.Fatal(err)
	}
	return transition.State
}

func pendingToolState(t *testing.T) agentprotocol.RuntimeState {
	t.Helper()
	called, err := Advance(startedState(t), agentprotocol.RuntimeInput{
		Type:          agentprotocol.RuntimeInputTypeProviderEvent,
		ProviderEvent: &agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: agentprotocol.ToolCallInput{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	transition, err := Advance(called.State, agentprotocol.RuntimeInput{
		Type: agentprotocol.RuntimeInputTypeProviderEvent,
		ProviderEvent: &agentprotocol.ProviderEvent{
			Type:       agentprotocol.ProviderEventTypeCompleted,
			StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonToolUse),
			Usage:      &agentprotocol.Usage{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return transition.State
}

func TestAdvanceStartsModelTurnWithoutMutatingInput(t *testing.T) {
	state := NewState(testConfig())
	before := mustJSON(t, state)
	transition, err := Advance(state, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("plan today")})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustJSON(t, state); got != before {
		t.Fatalf("state mutated\nbefore=%s\nafter=%s", before, got)
	}
	if transition.State.Phase != agentprotocol.RuntimePhaseModelPending || len(transition.Effects) != 1 || transition.Effects[0].Type != agentprotocol.RuntimeEffectTypeRequestModelTurn {
		t.Fatalf("transition = %#v", transition)
	}
	transition.State.Messages[0].Content[0].Text = ptr("changed")
	if len(state.Messages) != 0 {
		t.Fatalf("state messages aliased: %#v", state.Messages)
	}
}

func TestAdvanceAcceptsUserMessageWhenBackgroundExecutionModesMatch(t *testing.T) {
	config := testConfig()
	config.ExecutionMode = agentprotocol.ExecutionModeBackground
	config.CapabilitySnapshot.ExecutionMode = agentprotocol.ExecutionModeBackground
	transition, err := Advance(NewState(config), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("plan today")})
	if err != nil {
		t.Fatal(err)
	}
	if transition.State.ExecutionMode != agentprotocol.ExecutionModeBackground || transition.State.Phase != agentprotocol.RuntimePhaseModelPending || transition.State.Sequence != 1 {
		t.Fatalf("state = %#v", transition.State)
	}
	if len(transition.Effects) != 1 || transition.Effects[0].Type != agentprotocol.RuntimeEffectTypeRequestModelTurn || transition.Effects[0].TurnID == nil || *transition.Effects[0].TurnID != "turn-1" {
		t.Fatalf("effects = %#v", transition.Effects)
	}
}

func TestAdvanceRejectsMismatchedExecutionModes(t *testing.T) {
	tests := []struct {
		name         string
		stateMode    agentprotocol.ExecutionMode
		snapshotMode agentprotocol.ExecutionMode
	}{
		{name: "foreground state", stateMode: agentprotocol.ExecutionModeForeground, snapshotMode: agentprotocol.ExecutionModeBackground},
		{name: "background state", stateMode: agentprotocol.ExecutionModeBackground, snapshotMode: agentprotocol.ExecutionModeForeground},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := testConfig()
			config.ExecutionMode = test.stateMode
			config.CapabilitySnapshot.ExecutionMode = test.snapshotMode
			transition, err := Advance(NewState(config), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("plan today")})
			if err != nil {
				t.Fatal(err)
			}
			if transition.State.Phase != agentprotocol.RuntimePhaseFailed || transition.State.Sequence != 1 || transition.State.Error == nil || transition.State.Error.Code != agentprotocol.ErrorCodeProtocolIncompatible || transition.State.Error.Message != "execution mode mismatch" || transition.State.Error.Retryable {
				t.Fatalf("state = %#v", transition.State)
			}
			if len(transition.Effects) != 1 || transition.Effects[0].Type != agentprotocol.RuntimeEffectTypeFailRun || transition.Effects[0].Error == nil || !reflect.DeepEqual(transition.Effects[0].Error, transition.State.Error) {
				t.Fatalf("effects = %#v", transition.Effects)
			}
		})
	}
}

func TestCanonicalToolFingerprintSortsNestedObjectKeysAndPreservesArrays(t *testing.T) {
	first := agentprotocol.ToolCall{ID: "one", Name: "test.clock.read", Input: agentprotocol.ToolCallInput{"zone": "UTC", "nested": map[string]any{"z": 2, "a": []any{map[string]any{"y": 1, "x": 2}, 3}}, "format": "iso"}}
	second := agentprotocol.ToolCall{ID: "two", Name: "test.clock.read", Input: agentprotocol.ToolCallInput{"format": "iso", "nested": map[string]any{"a": []any{map[string]any{"x": 2, "y": 1}, 3}, "z": 2}, "zone": "UTC"}}
	got, err := CanonicalToolFingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	if want := `test.clock.read:{"format":"iso","nested":{"a":[{"x":2,"y":1},3],"z":2},"zone":"UTC"}`; got != want {
		t.Fatalf("fingerprint = %q, want %q", got, want)
	}
	other, err := CanonicalToolFingerprint(second)
	if err != nil || other != got {
		t.Fatalf("reordered fingerprint = %q, %v", other, err)
	}
}

func TestCanonicalToolFingerprintMatchesSharedCanonicalJSONFixture(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "..", "contracts", "agent", "fixtures", "runtime", "canonical-json.json")
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Input     agentprotocol.ToolCallInput `json:"input"`
		Canonical string                      `json:"canonical"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}

	got, err := CanonicalToolFingerprint(agentprotocol.ToolCall{ID: "call-canonical", Name: "test.clock.read", Input: fixture.Input})
	if err != nil {
		t.Fatal(err)
	}
	want := "test.clock.read:" + fixture.Canonical
	if got != want {
		t.Fatalf("fingerprint = %q, want %q", got, want)
	}
}

func TestAdvanceFlushesDraftIntoToolHistoryAndAcceptsOnlyExecutedResult(t *testing.T) {
	streaming, err := Advance(startedState(t), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeProviderEvent, ProviderEvent: &agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeTextDelta, Text: ptr("Checking.")}})
	if err != nil {
		t.Fatal(err)
	}
	staged, err := Advance(streaming.State, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeProviderEvent, ProviderEvent: &agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: agentprotocol.ToolCallInput{}}}})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := Advance(staged.State, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeProviderEvent, ProviderEvent: &agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonToolUse), Usage: &agentprotocol.Usage{}}})
	if err != nil {
		t.Fatal(err)
	}
	if pending.State.AssistantDraft != nil || mustJSON(t, pending.State.Messages[1].Content) != `[{"text":"Checking.","type":"text"},{"toolCall":{"id":"call-1","input":{},"name":"test.clock.read"},"type":"tool_call"}]` {
		t.Fatalf("tool history = %s", mustJSON(t, pending.State.Messages))
	}
	before, err := Advance(pending.State, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResult, ToolResult: &agentprotocol.RuntimeInputToolResult{CallID: "call-1", Result: agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{"time": "10:00"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "tool_result is not allowed before tool execution"; before.State.Error == nil || before.State.Error.Message != want {
		t.Fatalf("error = %#v", before.State.Error)
	}
	executing, err := Advance(pending.State, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResolution, ToolResolution: &agentprotocol.ToolResolution{CallID: "call-1", Decision: agentprotocol.ToolResolutionDecisionExecute}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := Advance(executing.State, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResult, ToolResult: &agentprotocol.RuntimeInputToolResult{CallID: "call-1", Result: agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{"time": "10:00"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if after.State.Phase != agentprotocol.RuntimePhaseModelPending || len(after.State.Messages) != 3 || after.Effects[0].Type != agentprotocol.RuntimeEffectTypeRequestModelTurn {
		t.Fatalf("result transition = %#v", after)
	}
}

func TestAdvanceSeparatesPendingHistoryAndEffectCopies(t *testing.T) {
	staged, err := Advance(startedState(t), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeProviderEvent, ProviderEvent: &agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{ID: "call-1", Name: "test.clock.read", Input: agentprotocol.ToolCallInput{"options": map[string]any{"format": "iso"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	transition, err := Advance(staged.State, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeProviderEvent, ProviderEvent: &agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonToolUse), Usage: &agentprotocol.Usage{}}})
	if err != nil {
		t.Fatal(err)
	}
	transition.State.PendingToolCall.Input["options"].(map[string]any)["format"] = "changed"
	if got := transition.State.Messages[1].Content[0].ToolCall.Input["options"].(map[string]any)["format"]; got != "iso" {
		t.Fatalf("history aliased pending call: %v", got)
	}
	transition.Effects[0].ToolCall.Input["options"].(map[string]any)["format"] = "effect changed"
	if got := transition.State.Messages[1].Content[0].ToolCall.Input["options"].(map[string]any)["format"]; got != "iso" {
		t.Fatalf("history aliased effect: %v", got)
	}
}

func TestAdvanceRejectsDuplicateResolutionAndMismatchedResultIDs(t *testing.T) {
	executing, err := Advance(pendingToolState(t), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResolution, ToolResolution: &agentprotocol.ToolResolution{CallID: "call-1", Decision: agentprotocol.ToolResolutionDecisionExecute}})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := Advance(executing.State, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResolution, ToolResolution: &agentprotocol.ToolResolution{CallID: "call-1", Decision: agentprotocol.ToolResolutionDecisionExecute}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "tool_resolution callId does not match pending tool call"; duplicate.State.Error == nil || duplicate.State.Error.Message != want || duplicate.State.StepCount != 1 {
		t.Fatalf("duplicate resolution = %#v", duplicate)
	}
	mismatch, err := Advance(executing.State, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResult, ToolResult: &agentprotocol.RuntimeInputToolResult{CallID: "other", Result: agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{"time": "10:00"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "tool_result callId does not match in-flight tool call"; mismatch.State.Error == nil || mismatch.State.Error.Message != want {
		t.Fatalf("mismatched result = %#v", mismatch)
	}
}

func TestAdvanceNormalizesContradictoryTrafficAndUnsupportedPhases(t *testing.T) {
	transition, err := Advance(startedState(t), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeCancel, Text: ptr("contradictory")})
	if err != nil {
		t.Fatal(err)
	}
	if transition.State.Error == nil || transition.State.Error.Code != agentprotocol.ErrorCodeProtocolIncompatible || transition.State.Error.Message != "invalid cancel payload" {
		t.Fatalf("contradictory input = %#v", transition)
	}
	state := NewState(testConfig())
	state.Phase = agentprotocol.RuntimePhase("future_phase")
	transition, err = Advance(state, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("plan today")})
	if err != nil {
		t.Fatal(err)
	}
	if transition.State.Error == nil || transition.State.Error.Code != agentprotocol.ErrorCodeProtocolIncompatible || transition.State.Error.Message != "unsupported RuntimeState phase future_phase" {
		t.Fatalf("unsupported phase = %#v", transition)
	}
}

func TestAdvancePreservesGenericValidationFailures(t *testing.T) {
	state := NewState(testConfig())
	state.Sequence = -1
	_, err := Advance(state, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("plan today")})
	var validation *agentprotocol.ValidationError
	if !errors.As(err, &validation) || validation.Code() != "validation_failed" {
		t.Fatalf("invalid state error = %v", err)
	}

	_, err = Advance(startedState(t), agentprotocol.RuntimeInput{
		Type: agentprotocol.RuntimeInputTypeProviderEvent,
		ProviderEvent: &agentprotocol.ProviderEvent{
			Type:       agentprotocol.ProviderEventTypeCompleted,
			StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonEndTurn),
			Usage:      &agentprotocol.Usage{InputTokens: -1, OutputTokens: 0, TotalTokens: 0},
		},
	})
	if !errors.As(err, &validation) || validation.Code() != "validation_failed" {
		t.Fatalf("invalid nested input error = %v", err)
	}
}

func TestAdvanceStagesToolCallUntilCompletedToolUseReportsUsage(t *testing.T) {
	config := Config{
		RunID:         "r-v2",
		ExecutionMode: agentprotocol.ExecutionModeForeground,
		CapabilitySnapshot: agentprotocol.CapabilitySnapshot{
			RuntimeVersion: "2.0.0", ExecutionMode: agentprotocol.ExecutionModeForeground,
			ToolIds: []string{"dayorder.calendar.read"}, Skills: []agentprotocol.SkillRef{},
			Scope: agentprotocol.AgentScope{Domains: []string{"calendar"}},
		},
		Budget: agentprotocol.Budget{MaxSteps: 8, MaxTokens: 16000, MaxDurationMs: 120000, MaxWorkers: 1, MaxConcurrency: 1, MaxRepeatedToolCalls: 2},
	}
	started, err := Advance(NewState(config), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: ptr("查询日程")})
	if err != nil {
		t.Fatal(err)
	}
	called, err := Advance(started.State, agentprotocol.RuntimeInput{
		Type: agentprotocol.RuntimeInputTypeProviderEvent,
		ProviderEvent: &agentprotocol.ProviderEvent{
			Type: agentprotocol.ProviderEventTypeToolCall,
			Call: &agentprotocol.ToolCall{ID: "c1", Name: "dayorder.calendar.read", Input: agentprotocol.ToolCallInput{}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(called.Effects) != 0 || called.State.Phase != agentprotocol.RuntimePhaseModelStreaming {
		t.Fatalf("incomplete tool call transition = %#v", called)
	}
	ended, err := Advance(called.State, agentprotocol.RuntimeInput{
		Type: agentprotocol.RuntimeInputTypeProviderEvent,
		ProviderEvent: &agentprotocol.ProviderEvent{
			Type:       agentprotocol.ProviderEventTypeCompleted,
			StopReason: driverStopReasonPtr(agentprotocol.ProviderEventStopReasonToolUse),
			Usage:      &agentprotocol.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ended.State.Usage.TotalTokens != 15 || len(ended.Effects) != 1 || ended.Effects[0].Type != agentprotocol.RuntimeEffectTypeResolveTool {
		t.Fatalf("completed tool call transition = %#v", ended)
	}
}

func TestAdvanceHandlesCompletedProviderStopReasonsExplicitly(t *testing.T) {
	tests := []struct {
		name       string
		stopReason agentprotocol.ProviderEventStopReason
		phase      agentprotocol.RuntimePhase
		error      *agentprotocol.AgentError
		effect     agentprotocol.RuntimeEffectType
	}{
		{name: "end turn", stopReason: agentprotocol.ProviderEventStopReasonEndTurn, phase: agentprotocol.RuntimePhaseCompleted, effect: agentprotocol.RuntimeEffectTypeCompleteRun},
		{
			name: "max tokens", stopReason: agentprotocol.ProviderEventStopReasonMaxTokens, phase: agentprotocol.RuntimePhaseFailed,
			error:  &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeValidationFailed, Message: "provider stopped at max_tokens", Retryable: false},
			effect: agentprotocol.RuntimeEffectTypeFailRun,
		},
		{
			name: "cancelled", stopReason: agentprotocol.ProviderEventStopReasonCancelled, phase: agentprotocol.RuntimePhaseCancelled,
			error:  &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeCancelled, Message: "provider cancelled", Retryable: false},
			effect: agentprotocol.RuntimeEffectTypeCancelRun,
		},
		{
			name: "orphan tool use", stopReason: agentprotocol.ProviderEventStopReasonToolUse, phase: agentprotocol.RuntimePhaseFailed,
			error:  &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProtocolIncompatible, Message: "provider completed with orphan tool_use", Retryable: false},
			effect: agentprotocol.RuntimeEffectTypeFailRun,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transition, err := Advance(startedState(t), agentprotocol.RuntimeInput{
				Type: agentprotocol.RuntimeInputTypeProviderEvent,
				ProviderEvent: &agentprotocol.ProviderEvent{
					Type:       agentprotocol.ProviderEventTypeCompleted,
					StopReason: driverStopReasonPtr(test.stopReason),
					Usage:      &agentprotocol.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if transition.State.Phase != test.phase || !reflect.DeepEqual(transition.State.Error, test.error) || transition.State.Usage != (agentprotocol.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}) {
				t.Fatalf("state = %#v", transition.State)
			}
			if len(transition.Effects) != 1 || transition.Effects[0].Type != test.effect {
				t.Fatalf("effects = %#v", transition.Effects)
			}
			if test.effect == agentprotocol.RuntimeEffectTypeFailRun && !reflect.DeepEqual(transition.Effects[0].Error, test.error) {
				t.Fatalf("effect error = %#v", transition.Effects[0].Error)
			}
		})
	}
}

func TestAdvanceNamesUnsupportedNestedTaggedVariantsInProtocolFailures(t *testing.T) {
	transition, err := Advance(startedState(t), agentprotocol.RuntimeInput{
		Type:          agentprotocol.RuntimeInputTypeProviderEvent,
		ProviderEvent: &agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventType("future_event")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if transition.State.Error == nil || transition.State.Error.Message != "invalid future_event provider event payload" {
		t.Fatalf("unsupported provider event = %#v", transition)
	}

	transition, err = Advance(startedState(t), agentprotocol.RuntimeInput{
		Type: agentprotocol.RuntimeInputTypeApprovalResponse,
		ApprovalResponse: &agentprotocol.RuntimeInputApprovalResponse{
			ApprovalID: "approval-1", Decision: agentprotocol.RuntimeInputApprovalResponseDecision("later"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if transition.State.Error == nil || transition.State.Error.Message != "invalid later approval response payload" {
		t.Fatalf("unsupported approval response = %#v", transition)
	}
}
