package worker

import (
	"context"
	"errors"
	"testing"

	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type fakeAgentRunProcessor struct {
	processErr error
	userID     uuid.UUID
	runID      uuid.UUID
	failCalls  int
}

func (processor *fakeAgentRunProcessor) Process(_ context.Context, userID, runID uuid.UUID) error {
	processor.userID, processor.runID = userID, runID
	return processor.processErr
}

func (processor *fakeAgentRunProcessor) Fail(context.Context, uuid.UUID, uuid.UUID, string, string) error {
	processor.failCalls++
	return nil
}

func TestAgentHandlerProcessesRunAndMarksTerminalFailure(t *testing.T) {
	userID, runID := uuid.New(), uuid.New()
	processor := &fakeAgentRunProcessor{}
	handler, err := NewAgentHandler(processor)
	if err != nil {
		t.Fatal(err)
	}
	event := model.OutboxEvent{
		ID: uuid.New(), UserID: userID, EventType: "agent.run.requested", AggregateType: "agent_run", AggregateID: runID,
		Payload: []byte(`{"runId":"` + runID.String() + `"}`), Attempts: 1,
	}
	if err = handler.Handle(context.Background(), event); err != nil || processor.userID != userID || processor.runID != runID {
		t.Fatalf("Handle() error=%v user=%s run=%s", err, processor.userID, processor.runID)
	}

	providerErr := errors.New("provider unavailable")
	processor.processErr = providerErr
	event.Attempts = 9
	if err = handler.Handle(context.Background(), event); !errors.Is(err, providerErr) || processor.failCalls != 0 {
		t.Fatalf("retryable Handle() error=%v failCalls=%d", err, processor.failCalls)
	}
	event.Attempts = 10
	if err = handler.Handle(context.Background(), event); !errors.Is(err, providerErr) || processor.failCalls != 1 {
		t.Fatalf("terminal Handle() error=%v failCalls=%d", err, processor.failCalls)
	}
}
