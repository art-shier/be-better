package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"dayorder.local/api/internal/agentprotocol"
)

func TestStopInputMapsStopCause(t *testing.T) {
	for _, test := range []struct {
		kind      string
		cause     error
		inputType agentprotocol.RuntimeInputType
		code      agentprotocol.ErrorCode
	}{
		{kind: "user", cause: StopCause{Kind: "user"}, inputType: agentprotocol.RuntimeInputTypeCancel},
		{kind: "timeout", cause: StopCause{Kind: "timeout"}, inputType: agentprotocol.RuntimeInputTypeRuntimeError, code: agentprotocol.ErrorCodeTimeout},
		{kind: "interrupted", cause: StopCause{Kind: "interrupted"}, inputType: agentprotocol.RuntimeInputTypeRuntimeError, code: agentprotocol.ErrorCodeInternalError},
		{kind: "nil", cause: nil, inputType: agentprotocol.RuntimeInputTypeCancel},
		{kind: "context cancelled", cause: context.Canceled, inputType: agentprotocol.RuntimeInputTypeCancel},
		{kind: "wrapped native deadline", cause: fmt.Errorf("private timeout detail: %w", context.DeadlineExceeded), inputType: agentprotocol.RuntimeInputTypeRuntimeError, code: agentprotocol.ErrorCodeTimeout},
		{kind: "arbitrary error", cause: errors.New("private interruption detail"), inputType: agentprotocol.RuntimeInputTypeRuntimeError, code: agentprotocol.ErrorCodeInternalError},
		{kind: "unknown structured kind", cause: StopCause{Kind: "shutdown"}, inputType: agentprotocol.RuntimeInputTypeRuntimeError, code: agentprotocol.ErrorCodeInternalError},
	} {
		t.Run(test.kind, func(t *testing.T) {
			input := StopInput(test.cause)
			if input.Type != test.inputType {
				t.Fatalf("Type = %q, want %q", input.Type, test.inputType)
			}
			var code agentprotocol.ErrorCode
			if input.Error != nil {
				code = input.Error.Code
			}
			if code != test.code {
				t.Fatalf("Error.Code = %q, want %q", code, test.code)
			}
			if serialized := fmt.Sprintf("%#v", input); strings.Contains(serialized, "private") {
				t.Fatalf("RuntimeInput exposes raw cause: %s", serialized)
			}
		})
	}
}
