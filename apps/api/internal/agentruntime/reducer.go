// Package agentruntime implements the deterministic runtime state machine.
package agentruntime

import (
	"encoding/json"
	"fmt"

	"dayorder.local/api/internal/agentprotocol"
)

// Config supplies the immutable values for a new runtime state.
type Config struct {
	RunID              string
	ExecutionMode      agentprotocol.ExecutionMode
	CapabilitySnapshot agentprotocol.CapabilitySnapshot
	Budget             agentprotocol.Budget
}

// NewState creates an independent, zeroed runtime state.
func NewState(config Config) agentprotocol.RuntimeState {
	state := agentprotocol.RuntimeState{
		ProtocolVersion: "2.0", RunID: config.RunID, ExecutionMode: config.ExecutionMode,
		Phase: agentprotocol.RuntimePhaseIdle, Sequence: 0, StepCount: 0,
		Messages: []agentprotocol.Message{}, CapabilitySnapshot: config.CapabilitySnapshot,
		Budget: config.Budget, Usage: agentprotocol.Usage{}, RepeatedToolCalls: 0,
	}
	cloned, err := clone(state)
	if err != nil {
		panic(fmt.Sprintf("clone runtime state: %v", err))
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionRuntimeState, cloned); err != nil {
		panic(fmt.Sprintf("invalid runtime config: %v", err))
	}
	return cloned
}

// CanonicalToolFingerprint returns a stable name-plus-input identity for a tool call.
func CanonicalToolFingerprint(call agentprotocol.ToolCall) (string, error) {
	raw, err := canonicalJSON(call.Input)
	if err != nil {
		return "", err
	}
	return call.Name + ":" + string(raw), nil
}

// Advance computes one transition without mutating or aliasing its inputs.
func Advance(state agentprotocol.RuntimeState, input agentprotocol.RuntimeInput) (agentprotocol.RuntimeTransition, error) {
	if !knownRuntimePhase(state.Phase) {
		next, err := clone(state)
		if err != nil {
			return agentprotocol.RuntimeTransition{}, err
		}
		return protocolFailure(next, fmt.Sprintf("unsupported RuntimeState phase %s", state.Phase))
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionRuntimeState, state); err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	next, err := clone(state)
	if err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	if message := taggedPayloadError(input); message != "" {
		return protocolFailure(next, message)
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionRuntimeInput, input); err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	if terminal(state.Phase) {
		return protocolFailure(next, "terminal_state")
	}
	if state.ExecutionMode != state.CapabilitySnapshot.ExecutionMode {
		return protocolFailure(next, "execution mode mismatch")
	}
	return dispatch(next, input)
}

func clone[T any](value T) (T, error) {
	var result T
	raw, err := json.Marshal(value)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(raw, &result)
	return result, err
}

func nextState(state agentprotocol.RuntimeState) (agentprotocol.RuntimeState, error) {
	next, err := clone(state)
	if err == nil {
		next.Sequence++
	}
	return next, err
}

func finish(state agentprotocol.RuntimeState, effects []agentprotocol.RuntimeEffect) (agentprotocol.RuntimeTransition, error) {
	stateCopy, err := clone(state)
	if err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	effectsCopy, err := clone(effects)
	if err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	transition := agentprotocol.RuntimeTransition{State: stateCopy, Effects: effectsCopy}
	if err := agentprotocol.Validate(agentprotocol.DefinitionRuntimeTransition, transition); err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	return transition, nil
}

func incompatible(message string) agentprotocol.AgentError {
	return agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProtocolIncompatible, Message: message, Retryable: false}
}

func validationFailure(message string) agentprotocol.AgentError {
	return agentprotocol.AgentError{Code: agentprotocol.ErrorCodeValidationFailed, Message: message, Retryable: false}
}

func clearPending(state *agentprotocol.RuntimeState) {
	state.PendingToolCall = nil
	state.PendingApprovalID = nil
}

func failed(state agentprotocol.RuntimeState, failure agentprotocol.AgentError) (agentprotocol.RuntimeTransition, error) {
	next, err := nextState(state)
	if err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	next.Phase = agentprotocol.RuntimePhaseFailed
	clearPending(&next)
	errorCopy, err := clone(failure)
	if err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	next.Error = &errorCopy
	effectError, err := clone(failure)
	if err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeFailRun, Error: &effectError}})
}

func protocolFailure(state agentprotocol.RuntimeState, message string) (agentprotocol.RuntimeTransition, error) {
	return failed(state, incompatible(message))
}

func cancelled(state agentprotocol.RuntimeState, reason string) (agentprotocol.RuntimeTransition, error) {
	next, err := nextState(state)
	if err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	next.Phase = agentprotocol.RuntimePhaseCancelled
	clearPending(&next)
	next.Error = &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeCancelled, Message: reason, Retryable: false}
	return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeCancelRun}})
}

func requirePhase(state agentprotocol.RuntimeState, input string, phases ...agentprotocol.RuntimePhase) (agentprotocol.RuntimeTransition, error) {
	for _, phase := range phases {
		if state.Phase == phase {
			return agentprotocol.RuntimeTransition{}, nil
		}
	}
	return protocolFailure(state, fmt.Sprintf("%s is not allowed in %s", input, state.Phase))
}

func phaseAllowed(state agentprotocol.RuntimeState, input string, phases ...agentprotocol.RuntimePhase) (bool, agentprotocol.RuntimeTransition, error) {
	transition, err := requirePhase(state, input, phases...)
	if err != nil {
		return false, agentprotocol.RuntimeTransition{}, err
	}
	if transition.State.ProtocolVersion != "" {
		return false, transition, nil
	}
	return true, agentprotocol.RuntimeTransition{}, nil
}

func dispatch(state agentprotocol.RuntimeState, input agentprotocol.RuntimeInput) (agentprotocol.RuntimeTransition, error) {
	switch input.Type {
	case agentprotocol.RuntimeInputTypeUserMessage:
		return onUserMessage(state, *input.Text)
	case agentprotocol.RuntimeInputTypeProviderEvent:
		return onProviderEvent(state, *input.ProviderEvent)
	case agentprotocol.RuntimeInputTypeToolResolution:
		return onToolResolution(state, *input.ToolResolution)
	case agentprotocol.RuntimeInputTypeToolResult:
		return onToolResult(state, *input.ToolResult)
	case agentprotocol.RuntimeInputTypeApprovalResponse:
		return onApprovalResponse(state, *input.ApprovalResponse)
	case agentprotocol.RuntimeInputTypeRuntimeError:
		return failed(state, *input.Error)
	case agentprotocol.RuntimeInputTypeCancel:
		reason := "cancelled"
		if input.Reason != nil {
			reason = *input.Reason
		}
		return cancelled(state, reason)
	default:
		return protocolFailure(state, "invalid RuntimeInput")
	}
}

func onUserMessage(state agentprotocol.RuntimeState, text string) (agentprotocol.RuntimeTransition, error) {
	ok, transition, err := phaseAllowed(state, "user_message", agentprotocol.RuntimePhaseIdle)
	if !ok || err != nil {
		return transition, err
	}
	next, err := nextState(state)
	if err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	next.Phase = agentprotocol.RuntimePhaseModelPending
	next.Messages = append(next.Messages, agentprotocol.Message{Role: agentprotocol.MessageRoleUser, Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeText, Text: &text}}})
	turnID := fmt.Sprintf("turn-%d", next.Sequence)
	return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeRequestModelTurn, TurnID: &turnID}})
}

func onProviderEvent(state agentprotocol.RuntimeState, event agentprotocol.ProviderEvent) (agentprotocol.RuntimeTransition, error) {
	ok, transition, err := phaseAllowed(state, "provider_event", agentprotocol.RuntimePhaseModelPending, agentprotocol.RuntimePhaseModelStreaming)
	if !ok || err != nil {
		return transition, err
	}
	switch event.Type {
	case agentprotocol.ProviderEventTypeTextDelta:
		next, err := nextState(state)
		if err != nil {
			return agentprotocol.RuntimeTransition{}, err
		}
		next.Phase = agentprotocol.RuntimePhaseModelStreaming
		draft := *event.Text
		if state.AssistantDraft != nil {
			draft = *state.AssistantDraft + draft
		}
		next.AssistantDraft = &draft
		text, err := clone(*event.Text)
		if err != nil {
			return agentprotocol.RuntimeTransition{}, err
		}
		return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeEmitText, Text: &text}})
	case agentprotocol.ProviderEventTypeToolCall:
		return onToolCall(state, *event.Call)
	case agentprotocol.ProviderEventTypeCompleted:
		next, err := nextState(state)
		if err != nil {
			return agentprotocol.RuntimeTransition{}, err
		}
		next.Usage = agentprotocol.Usage{InputTokens: state.Usage.InputTokens + event.Usage.InputTokens, OutputTokens: state.Usage.OutputTokens + event.Usage.OutputTokens, TotalTokens: state.Usage.TotalTokens + event.Usage.TotalTokens}
		if next.Usage.TotalTokens > state.Budget.MaxTokens {
			next.Phase = agentprotocol.RuntimePhaseFailed
			clearPending(&next)
			next.AssistantDraft = nil
			next.Error = ptrError(validationFailure("maxTokens exceeded"))
			failure := validationFailure("maxTokens exceeded")
			return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeFailRun, Error: &failure}})
		}
		switch *event.StopReason {
		case agentprotocol.ProviderEventStopReasonEndTurn:
			if state.PendingToolCall != nil {
				failure := incompatible("provider completed end_turn with pending tool call")
				next.Phase = agentprotocol.RuntimePhaseFailed
				clearPending(&next)
				next.Error = ptrError(failure)
				return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeFailRun, Error: &failure}})
			}
			next.Phase = agentprotocol.RuntimePhaseCompleted
			if state.AssistantDraft != nil && *state.AssistantDraft != "" {
				draft, err := clone(*state.AssistantDraft)
				if err != nil {
					return agentprotocol.RuntimeTransition{}, err
				}
				next.Messages = append(next.Messages, agentprotocol.Message{Role: agentprotocol.MessageRoleAssistant, Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeText, Text: &draft}}})
			}
			next.AssistantDraft, next.Error = nil, nil
			return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeCompleteRun}})
		case agentprotocol.ProviderEventStopReasonMaxTokens:
			failure := validationFailure("provider stopped at max_tokens")
			next.Phase = agentprotocol.RuntimePhaseFailed
			clearPending(&next)
			next.Error = ptrError(failure)
			return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeFailRun, Error: &failure}})
		case agentprotocol.ProviderEventStopReasonCancelled:
			next.Phase = agentprotocol.RuntimePhaseCancelled
			clearPending(&next)
			next.Error = ptrError(agentprotocol.AgentError{Code: agentprotocol.ErrorCodeCancelled, Message: "provider cancelled", Retryable: false})
			return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeCancelRun}})
		case agentprotocol.ProviderEventStopReasonToolUse:
			if state.PendingToolCall == nil {
				failure := incompatible("provider completed with orphan tool_use")
				next.Phase = agentprotocol.RuntimePhaseFailed
				clearPending(&next)
				next.Error = ptrError(failure)
				return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeFailRun, Error: &failure}})
			}
			call, err := clone(*state.PendingToolCall)
			if err != nil {
				return agentprotocol.RuntimeTransition{}, err
			}
			fingerprint, err := CanonicalToolFingerprint(call)
			if err != nil {
				return agentprotocol.RuntimeTransition{}, err
			}
			repeated := 1
			if state.LastToolFingerprint != nil && *state.LastToolFingerprint == fingerprint {
				repeated = state.RepeatedToolCalls + 1
			}
			content := []agentprotocol.ContentBlock{}
			if state.AssistantDraft != nil {
				draft, err := clone(*state.AssistantDraft)
				if err != nil {
					return agentprotocol.RuntimeTransition{}, err
				}
				content = append(content, agentprotocol.ContentBlock{Type: agentprotocol.ContentBlockTypeText, Text: &draft})
			}
			historyCall, err := clone(call)
			if err != nil {
				return agentprotocol.RuntimeTransition{}, err
			}
			content = append(content, agentprotocol.ContentBlock{Type: agentprotocol.ContentBlockTypeToolCall, ToolCall: &historyCall})
			next.Messages = append(next.Messages, agentprotocol.Message{Role: agentprotocol.MessageRoleAssistant, Content: content})
			next.AssistantDraft, next.LastToolFingerprint, next.RepeatedToolCalls = nil, &fingerprint, repeated
			if repeated > state.Budget.MaxRepeatedToolCalls {
				failure := validationFailure("maxRepeatedToolCalls exceeded")
				next.Phase = agentprotocol.RuntimePhaseFailed
				clearPending(&next)
				next.Error = ptrError(failure)
				return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeFailRun, Error: &failure}})
			}
			next.Phase = agentprotocol.RuntimePhaseToolPending
			next.PendingToolCall = &call
			effectCall, err := clone(call)
			if err != nil {
				return agentprotocol.RuntimeTransition{}, err
			}
			return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeResolveTool, ToolCall: &effectCall}})
		default:
			return protocolFailure(state, "invalid completed provider event payload")
		}
	case agentprotocol.ProviderEventTypeError:
		return failed(state, *event.Error)
	default:
		return protocolFailure(state, "invalid RuntimeInput")
	}
}

func ptrError(value agentprotocol.AgentError) *agentprotocol.AgentError { return &value }

func onToolCall(state agentprotocol.RuntimeState, call agentprotocol.ToolCall) (agentprotocol.RuntimeTransition, error) {
	if state.PendingToolCall != nil {
		return protocolFailure(state, "multiple tool calls in one turn")
	}
	next, err := nextState(state)
	if err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	next.Phase = agentprotocol.RuntimePhaseModelStreaming
	pending, err := clone(call)
	if err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	next.PendingToolCall = &pending
	return finish(next, []agentprotocol.RuntimeEffect{})
}

func onToolResolution(state agentprotocol.RuntimeState, resolution agentprotocol.ToolResolution) (agentprotocol.RuntimeTransition, error) {
	ok, transition, err := phaseAllowed(state, "tool_resolution", agentprotocol.RuntimePhaseToolPending)
	if !ok || err != nil {
		return transition, err
	}
	if state.PendingToolCall == nil || resolution.CallID != state.PendingToolCall.ID {
		return protocolFailure(state, "tool_resolution callId does not match pending tool call")
	}
	switch resolution.Decision {
	case agentprotocol.ToolResolutionDecisionExecute:
		return executePendingTool(state)
	case agentprotocol.ToolResolutionDecisionApproval:
		next, err := nextState(state)
		if err != nil {
			return agentprotocol.RuntimeTransition{}, err
		}
		next.Phase = agentprotocol.RuntimePhaseApprovalPending
		next.PendingApprovalID, err = clone(resolution.ApprovalID)
		if err != nil {
			return agentprotocol.RuntimeTransition{}, err
		}
		toolCall, err := clone(*state.PendingToolCall)
		if err != nil {
			return agentprotocol.RuntimeTransition{}, err
		}
		approvalID, err := clone(*resolution.ApprovalID)
		if err != nil {
			return agentprotocol.RuntimeTransition{}, err
		}
		return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeRequestApproval, ToolCall: &toolCall, ApprovalID: &approvalID}})
	case agentprotocol.ToolResolutionDecisionUnavailable, agentprotocol.ToolResolutionDecisionDenied:
		return failed(state, *resolution.Error)
	default:
		return protocolFailure(state, "invalid RuntimeInput")
	}
}

func executePendingTool(state agentprotocol.RuntimeState) (agentprotocol.RuntimeTransition, error) {
	if state.StepCount >= state.Budget.MaxSteps {
		return failed(state, validationFailure("maxSteps exceeded"))
	}
	next, err := nextState(state)
	if err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	next.Phase = agentprotocol.RuntimePhaseToolPending
	next.StepCount++
	clearPending(&next)
	toolCall, err := clone(*state.PendingToolCall)
	if err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeExecuteTool, ToolCall: &toolCall}})
}

func onToolResult(state agentprotocol.RuntimeState, result agentprotocol.RuntimeInputToolResult) (agentprotocol.RuntimeTransition, error) {
	ok, transition, err := phaseAllowed(state, "tool_result", agentprotocol.RuntimePhaseToolPending)
	if !ok || err != nil {
		return transition, err
	}
	if state.PendingToolCall != nil {
		return protocolFailure(state, "tool_result is not allowed before tool execution")
	}
	inFlight := mostRecentUnresolvedToolCall(state)
	if inFlight == nil || result.CallID != inFlight.ID {
		return protocolFailure(state, "tool_result callId does not match in-flight tool call")
	}
	next, err := nextState(state)
	if err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	next.Phase = agentprotocol.RuntimePhaseModelPending
	toolResult, err := clone(result.Result)
	if err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	next.Messages = append(next.Messages, agentprotocol.Message{Role: agentprotocol.MessageRoleTool, Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeToolResult, ToolResult: &toolResult}}})
	next.PendingToolCall = nil
	turnID := fmt.Sprintf("turn-%d", next.Sequence)
	return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeRequestModelTurn, TurnID: &turnID}})
}

func mostRecentUnresolvedToolCall(state agentprotocol.RuntimeState) *agentprotocol.ToolCall {
	for messageIndex := len(state.Messages) - 1; messageIndex >= 0; messageIndex-- {
		content := state.Messages[messageIndex].Content
		for contentIndex := len(content) - 1; contentIndex >= 0; contentIndex-- {
			block := content[contentIndex]
			if block.Type == agentprotocol.ContentBlockTypeToolResult {
				return nil
			}
			if block.Type == agentprotocol.ContentBlockTypeToolCall {
				return block.ToolCall
			}
		}
	}
	return nil
}

func onApprovalResponse(state agentprotocol.RuntimeState, response agentprotocol.RuntimeInputApprovalResponse) (agentprotocol.RuntimeTransition, error) {
	ok, transition, err := phaseAllowed(state, "approval_response", agentprotocol.RuntimePhaseApprovalPending)
	if !ok || err != nil {
		return transition, err
	}
	if state.PendingToolCall == nil || state.PendingApprovalID == nil || response.ApprovalID != *state.PendingApprovalID {
		return protocolFailure(state, "approval_response approvalId does not match pending approval")
	}
	if response.Decision == agentprotocol.RuntimeInputApprovalResponseDecisionAllow {
		return executePendingTool(state)
	}
	denied := agentprotocol.AgentError{Code: agentprotocol.ErrorCodeApprovalDenied, Message: "approval denied", Retryable: false}
	next, err := nextState(state)
	if err != nil {
		return agentprotocol.RuntimeTransition{}, err
	}
	next.Phase = agentprotocol.RuntimePhaseModelPending
	next.Messages = append(next.Messages, agentprotocol.Message{Role: agentprotocol.MessageRoleTool, Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeToolResult, ToolResult: &agentprotocol.ToolResult{Ok: false, Error: &denied}}}})
	clearPending(&next)
	turnID := fmt.Sprintf("turn-%d", next.Sequence)
	return finish(next, []agentprotocol.RuntimeEffect{{Type: agentprotocol.RuntimeEffectTypeRequestModelTurn, TurnID: &turnID}})
}

func terminal(phase agentprotocol.RuntimePhase) bool {
	return phase == agentprotocol.RuntimePhaseCompleted || phase == agentprotocol.RuntimePhaseFailed || phase == agentprotocol.RuntimePhaseCancelled
}

func taggedPayloadError(input agentprotocol.RuntimeInput) string {
	none := func(values ...bool) bool {
		for _, value := range values {
			if value {
				return false
			}
		}
		return true
	}
	switch input.Type {
	case agentprotocol.RuntimeInputTypeUserMessage:
		if input.Text == nil || !none(input.ProviderEvent != nil, input.ToolResolution != nil, input.ToolResult != nil, input.ApprovalResponse != nil, input.Error != nil, input.Reason != nil) {
			return "invalid user_message payload"
		}
	case agentprotocol.RuntimeInputTypeProviderEvent:
		if input.ProviderEvent == nil || !none(input.Text != nil, input.ToolResolution != nil, input.ToolResult != nil, input.ApprovalResponse != nil, input.Error != nil, input.Reason != nil) {
			return "invalid provider_event payload"
		}
		return providerEventPayloadError(*input.ProviderEvent)
	case agentprotocol.RuntimeInputTypeToolResolution:
		if input.ToolResolution == nil || !none(input.Text != nil, input.ProviderEvent != nil, input.ToolResult != nil, input.ApprovalResponse != nil, input.Error != nil, input.Reason != nil) {
			return "invalid tool_resolution payload"
		}
		return toolResolutionPayloadError(*input.ToolResolution)
	case agentprotocol.RuntimeInputTypeToolResult:
		if input.ToolResult == nil || !none(input.Text != nil, input.ProviderEvent != nil, input.ToolResolution != nil, input.ApprovalResponse != nil, input.Error != nil, input.Reason != nil) {
			return "invalid tool_result payload"
		}
	case agentprotocol.RuntimeInputTypeApprovalResponse:
		if input.ApprovalResponse == nil || !none(input.Text != nil, input.ProviderEvent != nil, input.ToolResolution != nil, input.ToolResult != nil, input.Error != nil, input.Reason != nil) {
			return "invalid approval_response payload"
		}
		return approvalResponsePayloadError(*input.ApprovalResponse)
	case agentprotocol.RuntimeInputTypeRuntimeError:
		if input.Error == nil || !none(input.Text != nil, input.ProviderEvent != nil, input.ToolResolution != nil, input.ToolResult != nil, input.ApprovalResponse != nil, input.Reason != nil) {
			return "invalid runtime_error payload"
		}
	case agentprotocol.RuntimeInputTypeCancel:
		if !none(input.Text != nil, input.ProviderEvent != nil, input.ToolResolution != nil, input.ToolResult != nil, input.ApprovalResponse != nil, input.Error != nil) {
			return "invalid cancel payload"
		}
	default:
		return "invalid RuntimeInput"
	}
	return ""
}

func approvalResponsePayloadError(response agentprotocol.RuntimeInputApprovalResponse) string {
	switch response.Decision {
	case agentprotocol.RuntimeInputApprovalResponseDecisionAllow, agentprotocol.RuntimeInputApprovalResponseDecisionDeny:
		return ""
	default:
		return fmt.Sprintf("invalid %s approval response payload", response.Decision)
	}
}

func providerEventPayloadError(event agentprotocol.ProviderEvent) string {
	none := func(values ...bool) bool {
		for _, value := range values {
			if value {
				return false
			}
		}
		return true
	}
	switch event.Type {
	case agentprotocol.ProviderEventTypeTextDelta:
		if event.Text == nil || !none(event.Call != nil, event.StopReason != nil, event.Usage != nil, event.Error != nil) {
			return "invalid text_delta provider event payload"
		}
	case agentprotocol.ProviderEventTypeToolCall:
		if event.Call == nil || !none(event.Text != nil, event.StopReason != nil, event.Usage != nil, event.Error != nil) {
			return "invalid tool_call provider event payload"
		}
	case agentprotocol.ProviderEventTypeCompleted:
		if event.StopReason == nil || event.Usage == nil || !none(event.Text != nil, event.Call != nil, event.Error != nil) {
			return "invalid completed provider event payload"
		}
	case agentprotocol.ProviderEventTypeError:
		if event.Error == nil || !none(event.Text != nil, event.Call != nil, event.StopReason != nil, event.Usage != nil) {
			return "invalid error provider event payload"
		}
	default:
		return fmt.Sprintf("invalid %s provider event payload", event.Type)
	}
	return ""
}

func toolResolutionPayloadError(resolution agentprotocol.ToolResolution) string {
	switch resolution.Decision {
	case agentprotocol.ToolResolutionDecisionExecute:
		if resolution.ApprovalID != nil || resolution.Error != nil {
			return "invalid execute tool resolution payload"
		}
	case agentprotocol.ToolResolutionDecisionApproval:
		if resolution.ApprovalID == nil || resolution.Error != nil {
			return "invalid approval tool resolution payload"
		}
	case agentprotocol.ToolResolutionDecisionUnavailable, agentprotocol.ToolResolutionDecisionDenied:
		if resolution.ApprovalID != nil || resolution.Error == nil {
			return fmt.Sprintf("invalid %s tool resolution payload", resolution.Decision)
		}
	default:
		return fmt.Sprintf("invalid %s tool resolution payload", resolution.Decision)
	}
	return ""
}
