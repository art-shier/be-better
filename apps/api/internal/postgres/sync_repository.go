package postgres

import (
	"context"
	"errors"
	"fmt"

	"dayorder.local/api/internal/database"
	db "dayorder.local/api/internal/db/gen"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type SyncRepository struct{}

func NewSyncRepository() *SyncRepository { return &SyncRepository{} }

func (*SyncRepository) Append(
	ctx context.Context,
	tx database.Tx,
	userID uuid.UUID,
	draft model.SyncChangeDraft,
) (model.SyncChange, error) {
	if tx == nil {
		return model.SyncChange{}, errors.New("sync transaction is required")
	}
	row, err := db.New(tx).AppendSyncChange(ctx, db.AppendSyncChangeParams{
		UserID: pgUUID(userID), EntityType: draft.EntityType, EntityID: pgUUID(draft.EntityID),
		Operation: draft.Operation, EntityVersion: draft.EntityVersion,
	})
	if err != nil {
		return model.SyncChange{}, fmt.Errorf("append sync change: %w", err)
	}
	return syncChangeFromRow(row), nil
}

func (*SyncRepository) CurrentCursor(
	ctx context.Context,
	tx database.Tx,
	userID uuid.UUID,
) (int64, error) {
	if tx == nil {
		return 0, errors.New("sync transaction is required")
	}
	sequence, err := db.New(tx).CurrentSyncCursor(ctx, pgUUID(userID))
	if err != nil {
		return 0, fmt.Errorf("read current sync cursor: %w", err)
	}
	return sequence, nil
}

func (*SyncRepository) List(
	ctx context.Context,
	tx database.Tx,
	userID uuid.UUID,
	after int64,
	limit int,
) ([]model.SyncChange, error) {
	if tx == nil {
		return nil, errors.New("sync transaction is required")
	}
	rows, err := db.New(tx).ListSyncChanges(ctx, pgUUID(userID), after, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("list sync changes: %w", err)
	}
	changes := make([]model.SyncChange, 0, len(rows))
	for _, row := range rows {
		changes = append(changes, syncChangeFromRow(row))
	}
	return changes, nil
}

func syncChangeFromRow(row *db.DayorderSyncChange) model.SyncChange {
	return model.SyncChange{
		Sequence: row.Sequence, EntityType: row.EntityType, EntityID: uuid.UUID(row.EntityID.Bytes),
		Operation: row.Operation, EntityVersion: row.EntityVersion, ChangedAt: row.ChangedAt.Time.UTC(),
	}
}
