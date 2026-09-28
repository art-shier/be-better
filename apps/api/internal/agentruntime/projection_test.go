package agentruntime

import (
	"errors"
	"testing"

	"dayorder.local/api/internal/agentprotocol"
)

func projectionState() agentprotocol.RuntimeState {
	return NewState(Config{
		RunID:         "run-projection",
		ExecutionMode: agentprotocol.ExecutionModeForeground,
		CapabilitySnapshot: agentprotocol.CapabilitySnapshot{
			RuntimeVersion: "2.0.0", ExecutionMode: agentprotocol.ExecutionModeForeground,
			ToolIds: []string{}, Skills: []agentprotocol.SkillRef{},
			Scope: agentprotocol.AgentScope{Domains: []string{"test"}},
		},
		Budget: agentprotocol.Budget{MaxSteps: 4, MaxTokens: 100, MaxDurationMs: 30000, MaxWorkers: 1, MaxConcurrency: 1, MaxRepeatedToolCalls: 2},
	})
}

func TestProjectRunMapsRuntimePhases(t *testing.T) {
	tests := []struct {
		phase  agentprotocol.RuntimePhase
		status string
	}{
		{agentprotocol.RuntimePhaseIdle, "ready"},
		{agentprotocol.RuntimePhaseModelPending, "analyzing"},
		{agentprotocol.RuntimePhaseModelStreaming, "analyzing"},
		{agentprotocol.RuntimePhaseToolPending, "analyzing"},
		{agentprotocol.RuntimePhaseApprovalPending, "waiting"},
		{agentprotocol.RuntimePhaseCompleted, "completed"},
		{agentprotocol.RuntimePhaseFailed, "failed"},
		{agentprotocol.RuntimePhaseCancelled, "stopped"},
	}

	for _, test := range tests {
		t.Run(string(test.phase), func(t *testing.T) {
			state := projectionState()
			state.Phase = test.phase
			if test.phase == agentprotocol.RuntimePhaseFailed {
				state.Error = &agentprotocol.AgentError{
					Code: agentprotocol.ErrorCodeProviderUnavailable, Message: "provider unavailable", Retryable: true,
				}
			}

			projection, err := ProjectRun(state)
			if err != nil {
				t.Fatal(err)
			}
			if projection.Status != test.status {
				t.Fatalf("status = %q, want %q", projection.Status, test.status)
			}
			if test.phase == agentprotocol.RuntimePhaseFailed {
				if projection.ErrorCode == nil || *projection.ErrorCode != "provider_unavailable" || projection.ErrorMessage == nil || *projection.ErrorMessage != "provider unavailable" {
					t.Fatalf("failed projection = %#v", projection)
				}
				return
			}
			if projection.ErrorCode != nil || projection.ErrorMessage != nil {
				t.Fatalf("non-failed projection includes error fields: %#v", projection)
			}
		})
	}
}

func TestProjectRunPreservesGenericRuntimeStateValidationFailure(t *testing.T) {
	state := projectionState()
	state.Sequence = -1

	_, err := ProjectRun(state)
	var validation *agentprotocol.ValidationError
	if !errors.As(err, &validation) || validation.Code() != "validation_failed" {
		t.Fatalf("error = %v, want validation_failed", err)
	}
}

func TestProjectRunClassifiesUnknownPhaseAsProtocolIncompatible(t *testing.T) {
	state := projectionState()
	state.Phase = agentprotocol.RuntimePhase("future_phase")

	_, err := ProjectRun(state)
	if err == nil {
		t.Fatal("expected protocol incompatible error")
	}
	coded, ok := err.(interface{ Code() string })
	if !ok || coded.Code() != "protocol_incompatible" {
		t.Fatalf("error code = %v, want protocol_incompatible", err)
	}
}
