package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type OutboxRepository struct{ pool *pgxpool.Pool }

func NewOutboxRepository(pool *pgxpool.Pool) (*OutboxRepository, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL worker pool is required")
	}
	return &OutboxRepository{pool: pool}, nil
}

func (repository *OutboxRepository) Claim(ctx context.Context, limit int, lockToken uuid.UUID, staleAfter time.Duration) ([]model.OutboxEvent, error) {
	rows, err := repository.pool.Query(ctx, `
SELECT id, user_id, event_type, aggregate_type, aggregate_id, payload, attempts, lock_token
FROM dayorder.claim_outbox_events($1, $2, $3::interval)
`, limit, pgUUID(lockToken), pgtype.Interval{Microseconds: staleAfter.Microseconds(), Valid: true})
	if err != nil {
		return nil, fmt.Errorf("claim outbox events: %w", err)
	}
	defer rows.Close()
	events := make([]model.OutboxEvent, 0)
	for rows.Next() {
		var event model.OutboxEvent
		var id, userID, aggregateID, claimedLockToken pgtype.UUID
		if err = rows.Scan(
			&id, &userID, &event.EventType, &event.AggregateType, &aggregateID,
			&event.Payload, &event.Attempts, &claimedLockToken,
		); err != nil {
			return nil, fmt.Errorf("scan claimed outbox event: %w", err)
		}
		event.ID = uuid.UUID(id.Bytes)
		event.UserID = uuid.UUID(userID.Bytes)
		event.AggregateID = uuid.UUID(aggregateID.Bytes)
		event.LockToken = uuid.UUID(claimedLockToken.Bytes)
		events = append(events, event)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claimed outbox events: %w", err)
	}
	return events, nil
}

func (repository *OutboxRepository) Complete(ctx context.Context, eventID, lockToken uuid.UUID) error {
	var completed bool
	if err := repository.pool.QueryRow(
		ctx, "SELECT dayorder.complete_outbox_event($1, $2)", pgUUID(eventID), pgUUID(lockToken),
	).Scan(&completed); err != nil {
		return fmt.Errorf("complete outbox event: %w", err)
	}
	if !completed {
		return model.ErrConflict
	}
	return nil
}

func (repository *OutboxRepository) Retry(ctx context.Context, retry model.OutboxRetry) error {
	var retried bool
	if err := repository.pool.QueryRow(ctx, `
SELECT dayorder.retry_outbox_event($1, $2, $3, $4, $5)
`, pgUUID(retry.EventID), pgUUID(retry.LockToken), retry.AvailableAt.UTC(), retry.LastError, retry.Terminal).Scan(&retried); err != nil {
		return fmt.Errorf("retry outbox event: %w", err)
	}
	if !retried {
		return model.ErrConflict
	}
	return nil
}
