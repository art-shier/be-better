package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"dayorder.local/api/internal/database"
	db "dayorder.local/api/internal/db/gen"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type OutboxWriter struct{}

func NewOutboxWriter() *OutboxWriter { return &OutboxWriter{} }

func (*OutboxWriter) Record(
	ctx context.Context,
	tx database.Tx,
	userID uuid.UUID,
	events []model.OutboxDraft,
) error {
	if tx == nil {
		return errors.New("outbox transaction is required")
	}
	queries := db.New(tx)
	for _, event := range events {
		if _, err := queries.CreateOutboxEvent(ctx, db.CreateOutboxEventParams{
			ID: pgUUID(event.ID), UserID: pgUUID(userID), EventType: event.EventType,
			AggregateType: event.AggregateType, AggregateID: pgUUID(event.AggregateID),
			Payload: bytes.Clone(event.Payload), AvailableAt: pgTime(event.AvailableAt),
		}); err != nil {
			return fmt.Errorf("create outbox event: %w", err)
		}
	}
	return nil
}
