package service_test

import (
	"context"
	"errors"
	"testing"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

func TestReadonlyOperationRejectsInvalidProtocolInputsBeforeStorage(t *testing.T) {
	runs := &service.AgentReadonlyService{}
	actor := agentexecution.Actor{UserID: uuid.New(), Mode: agentprotocol.ExecutionModeForeground}
	for _, test := range []struct {
		name     string
		kind     string
		id       string
		payload  []byte
		reserved int
	}{
		{name: "kind", kind: "write", id: "id", payload: []byte("{}")},
		{name: "id", kind: "calendar_read", id: " bad ", payload: []byte("{}")},
		{name: "payload", kind: "calendar_read", id: "id", payload: []byte("{\"broken\":")},
		{name: "reservation", kind: "provider_turn", id: "id", payload: []byte("{}"), reserved: -1},
		{name: "calendar reservation", kind: "calendar_read", id: "id", payload: []byte("{}"), reserved: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := runs.BeginOperation(context.Background(), actor, uuid.New(), test.kind, test.id, test.payload, test.reserved)
			var protocolError *agentexecution.Error
			if !errors.As(err, &protocolError) || protocolError.Agent.Code != agentprotocol.ErrorCodeValidationFailed {
				t.Fatalf("BeginOperation() error = %v, want validation_failed", err)
			}
		})
	}
}

func TestReadonlyOperationRejectsInvalidUsageBeforeStorage(t *testing.T) {
	runs := &service.AgentReadonlyService{}
	actor := agentexecution.Actor{UserID: uuid.New(), Mode: agentprotocol.ExecutionModeForeground}
	operation := agentexecution.Operation{UserID: actor.UserID, RunID: uuid.New(), Kind: "provider_turn", ID: "turn-1", Attempts: 1}
	for _, usage := range []agentprotocol.Usage{
		{InputTokens: -1, TotalTokens: -1},
		{InputTokens: 2, OutputTokens: 3, TotalTokens: 4},
	} {
		err := runs.EndOperation(context.Background(), actor, operation, usage, true, "")
		var protocolError *agentexecution.Error
		if !errors.As(err, &protocolError) || protocolError.Agent.Code != agentprotocol.ErrorCodeValidationFailed {
			t.Fatalf("EndOperation(%#v) error = %v, want validation_failed", usage, err)
		}
	}
}
