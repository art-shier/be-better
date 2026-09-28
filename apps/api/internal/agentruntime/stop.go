package agentruntime

import (
	"context"
	"errors"

	"dayorder.local/api/internal/agentprotocol"
)

// StopCause is a host-supplied reason for stopping a Run.
type StopCause struct {
	Kind string
}

func (cause StopCause) Error() string {
	return cause.Kind
}

// StopInput normalizes a host stop cause into the reducer input contract.
func StopInput(cause error) agentprotocol.RuntimeInput {
	switch stopCauseKind(cause) {
	case "timeout":
		failure := agentError(agentprotocol.ErrorCodeTimeout, "runtime deadline exceeded")
		return agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure}
	case "interrupted":
		failure := agentError(agentprotocol.ErrorCodeInternalError, "runtime interrupted")
		return agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure}
	default:
		reason := "cancelled"
		return agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeCancel, Reason: &reason}
	}
}

func stopCauseKind(cause error) string {
	var value StopCause
	if errors.As(cause, &value) {
		return explicitStopKind(value.Kind)
	}
	var pointer *StopCause
	if errors.As(cause, &pointer) && pointer != nil {
		return explicitStopKind(pointer.Kind)
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return "timeout"
	}
	if cause == nil || errors.Is(cause, context.Canceled) {
		return "user"
	}
	return "interrupted"
}

func explicitStopKind(kind string) string {
	if kind == "user" || kind == "timeout" || kind == "interrupted" {
		return kind
	}
	return "interrupted"
}
