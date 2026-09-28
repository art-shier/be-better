package agentruntime

import (
	"fmt"

	"dayorder.local/api/internal/agentprotocol"
)

// RunProjection is the persistence-neutral status representation of a RuntimeState.
type RunProjection struct {
	Status       string
	ErrorCode    *string
	ErrorMessage *string
}

// ProjectionProtocolError marks an unsupported runtime phase without changing
// the shared DTO validator's validation_failed classification.
type ProjectionProtocolError struct {
	cause error
}

func (e *ProjectionProtocolError) Error() string {
	return "protocol_incompatible: " + e.cause.Error()
}

func (e *ProjectionProtocolError) Unwrap() error { return e.cause }
func (e *ProjectionProtocolError) Code() string  { return "protocol_incompatible" }

func knownRuntimePhase(phase agentprotocol.RuntimePhase) bool {
	switch phase {
	case agentprotocol.RuntimePhaseIdle,
		agentprotocol.RuntimePhaseModelPending,
		agentprotocol.RuntimePhaseModelStreaming,
		agentprotocol.RuntimePhaseToolPending,
		agentprotocol.RuntimePhaseApprovalPending,
		agentprotocol.RuntimePhaseCompleted,
		agentprotocol.RuntimePhaseFailed,
		agentprotocol.RuntimePhaseCancelled:
		return true
	default:
		return false
	}
}

func unsupportedRuntimePhase(phase agentprotocol.RuntimePhase, cause error) error {
	if cause == nil {
		cause = fmt.Errorf("unsupported RuntimeState phase %q", phase)
	}
	return &ProjectionProtocolError{cause: cause}
}

// ProjectRun maps an already-owned RuntimeState to the existing AgentRun status.
// It is deliberately side-effect free: AgentStep, AgentChange, SourceRef, and
// database persistence are Phase 2 adapters, not projection effects.
func ProjectRun(state agentprotocol.RuntimeState) (RunProjection, error) {
	if err := agentprotocol.Validate(agentprotocol.DefinitionRuntimeState, state); err != nil {
		if !knownRuntimePhase(state.Phase) {
			return RunProjection{}, unsupportedRuntimePhase(state.Phase, err)
		}
		return RunProjection{}, err
	}

	switch state.Phase {
	case agentprotocol.RuntimePhaseIdle:
		return RunProjection{Status: "ready"}, nil
	case agentprotocol.RuntimePhaseModelPending, agentprotocol.RuntimePhaseModelStreaming, agentprotocol.RuntimePhaseToolPending:
		return RunProjection{Status: "analyzing"}, nil
	case agentprotocol.RuntimePhaseApprovalPending:
		return RunProjection{Status: "waiting"}, nil
	case agentprotocol.RuntimePhaseCompleted:
		return RunProjection{Status: "completed"}, nil
	case agentprotocol.RuntimePhaseFailed:
		if state.Error == nil {
			return RunProjection{Status: "failed"}, nil
		}
		errorCode := string(state.Error.Code)
		errorMessage := state.Error.Message
		return RunProjection{Status: "failed", ErrorCode: &errorCode, ErrorMessage: &errorMessage}, nil
	case agentprotocol.RuntimePhaseCancelled:
		return RunProjection{Status: "stopped"}, nil
	default:
		return RunProjection{}, unsupportedRuntimePhase(state.Phase, nil)
	}
}
