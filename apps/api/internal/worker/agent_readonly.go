package worker

import (
	"context"
	"errors"

	"dayorder.local/api/internal/model"
)

type ReadonlyAgentHandler struct {
	processor interface {
		Process(context.Context, model.OutboxEvent) error
	}
}

func NewReadonlyAgentHandler(processor interface {
	Process(context.Context, model.OutboxEvent) error
}) (*ReadonlyAgentHandler, error) {
	if processor == nil {
		return nil, errors.New("readonly agent processor is required")
	}
	return &ReadonlyAgentHandler{processor: processor}, nil
}

func (handler *ReadonlyAgentHandler) Handle(ctx context.Context, event model.OutboxEvent) error {
	return handler.processor.Process(ctx, event)
}
