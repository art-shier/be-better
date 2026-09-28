package agentexecution_test

import (
	"errors"
	"reflect"
	"testing"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
)

func TestErrorExposesProtocolFailure(t *testing.T) {
	want := agentprotocol.AgentError{
		Code: agentprotocol.ErrorCodeValidationFailed, Message: "run token budget exceeded",
	}
	var err error = &agentexecution.Error{Agent: want}
	if err.Error() != want.Message {
		t.Fatalf("Error() = %q, want %q", err.Error(), want.Message)
	}
	var protocolError *agentexecution.Error
	if !errors.As(err, &protocolError) || !reflect.DeepEqual(protocolError.Agent, want) {
		t.Fatalf("protocol error = %#v", protocolError)
	}
}

func TestErrorValueImplementsError(t *testing.T) {
	var err error = agentexecution.Error{Agent: agentprotocol.AgentError{Code: agentprotocol.ErrorCodeInternalError}}
	if err.Error() != string(agentprotocol.ErrorCodeInternalError) {
		t.Fatalf("Error() = %q", err.Error())
	}
}
