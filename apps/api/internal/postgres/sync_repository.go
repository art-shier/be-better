package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"dayorder.local/api/internal/database"
	db "dayorder.local/api/internal/db/gen"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

func (*SyncRepository) Resolve(
	ctx context.Context,
	tx database.Tx,
	userID uuid.UUID,
	change model.SyncChange,
) ([]byte, error) {
	if tx == nil {
		return nil, errors.New("sync transaction is required")
	}
	var value any
	var err error
	switch change.EntityType {
	case "goal":
		value, err = NewGoalRepository().GetGoal(ctx, tx, userID, change.EntityID)
	case "milestone":
		value, err = NewGoalRepository().GetMilestone(ctx, tx, userID, change.EntityID)
	case "task":
		value, err = NewTaskRepository().GetTask(ctx, tx, userID, change.EntityID)
	case "calendar_event":
		value, err = NewCalendarRepository().GetEvent(ctx, tx, userID, change.EntityID)
	case "reminder":
		var row *db.DayorderCalendarEventReminder
		row, err = db.New(tx).GetCalendarReminder(ctx, pgUUID(userID), pgUUID(change.EntityID))
		if err == nil {
			value = calendarReminderFromRow(row)
		} else {
			err = mapDatabaseError("get sync reminder", err)
		}
	case "record":
		repository := NewContentRepository()
		var record model.Record
		record, err = repository.GetRecord(ctx, tx, userID, change.EntityID)
		if err == nil {
			record.Tags, err = repository.ListRecordTags(ctx, tx, userID, change.EntityID)
			value = record
		}
	case "note":
		repository := NewContentRepository()
		var note model.Note
		note, err = repository.GetNote(ctx, tx, userID, change.EntityID)
		if err == nil {
			note.Tags, err = repository.ListNoteTags(ctx, tx, userID, change.EntityID)
			value = note
		}
	case "daily_review":
		value, err = NewContentRepository().GetReview(ctx, tx, userID, change.EntityID)
	case "tag":
		var row *db.DayorderTag
		row, err = db.New(tx).GetTag(ctx, pgUUID(userID), pgUUID(change.EntityID))
		if err == nil {
			value = tagFromRow(row)
		} else {
			err = mapDatabaseError("get sync tag", err)
		}
	case "settings":
		if change.EntityID != userID {
			return nil, model.ErrNotFound
		}
		value, err = NewSettingsRepository().Get(ctx, tx, userID)
	default:
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode sync entity: %w", err)
	}
	return encoded, nil
}

func (*SyncRepository) RequireActiveDevice(
	ctx context.Context,
	tx database.Tx,
	userID uuid.UUID,
	deviceID uuid.UUID,
) error {
	if tx == nil {
		return errors.New("sync transaction is required")
	}
	_, err := db.New(tx).GetActiveUserDevice(ctx, pgUUID(userID), pgUUID(deviceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrDeviceNotActive
	}
	if err != nil {
		return fmt.Errorf("validate sync device: %w", err)
	}
	return nil
}

func (*SyncRepository) AdvanceDeviceCursor(
	ctx context.Context,
	tx database.Tx,
	userID uuid.UUID,
	deviceID uuid.UUID,
	sequence int64,
) error {
	if tx == nil {
		return errors.New("sync transaction is required")
	}
	_, err := db.New(tx).AdvanceUserDeviceSyncCursor(ctx, sequence, pgUUID(userID), pgUUID(deviceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrDeviceNotActive
	}
	if err != nil {
		return fmt.Errorf("advance device sync cursor: %w", err)
	}
	return nil
}

func syncChangeFromRow(row *db.DayorderSyncChange) model.SyncChange {
	return model.SyncChange{
		Sequence: row.Sequence, EntityType: row.EntityType, EntityID: uuid.UUID(row.EntityID.Bytes),
		Operation: row.Operation, EntityVersion: row.EntityVersion, ChangedAt: row.ChangedAt.Time.UTC(),
	}
}
