package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

const agentTerminalAttempt = 10

type AgentRunProcessor interface {
	Process(context.Context, uuid.UUID, uuid.UUID) error
	Fail(context.Context, uuid.UUID, uuid.UUID, string, string) error
}

type AgentHandler struct{ processor AgentRunProcessor }

func NewAgentHandler(processor AgentRunProcessor) (*AgentHandler, error) {
	if processor == nil {
		return nil, errors.New("agent run processor is required")
	}
	return &AgentHandler{processor: processor}, nil
}

func (handler *AgentHandler) Handle(ctx context.Context, event model.OutboxEvent) error {
	if event.UserID == uuid.Nil || event.AggregateType != "agent_run" || event.AggregateID == uuid.Nil {
		return errors.New("invalid agent run event")
	}
	var payload struct {
		RunID uuid.UUID `json:"runId"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.RunID == uuid.Nil || payload.RunID != event.AggregateID {
		return errors.New("invalid agent run event payload")
	}
	err := handler.processor.Process(ctx, event.UserID, payload.RunID)
	if err == nil || event.Attempts < agentTerminalAttempt {
		return err
	}
	if failErr := handler.processor.Fail(
		ctx, event.UserID, payload.RunID, "AGENT_ANALYSIS_FAILED",
		"Agent 分析在多次安全重试后仍未完成，请稍后重新发起。",
	); failErr != nil {
		return errors.Join(err, fmt.Errorf("mark terminal agent failure: %w", failErr))
	}
	return err
}
