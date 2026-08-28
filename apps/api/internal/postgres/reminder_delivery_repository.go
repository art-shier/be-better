package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"dayorder.local/api/internal/database"
	db "dayorder.local/api/internal/db/gen"
	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/worker"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ReminderDeliveryRepository struct {
	transactor *database.Transactor
	sync       *SyncRepository
	audit      *AuditRepository
}

func NewReminderDeliveryRepository(pool *pgxpool.Pool) (*ReminderDeliveryRepository, error) {
	transactor, err := database.NewPoolTransactor(pool)
	if err != nil {
		return nil, err
	}
	return &ReminderDeliveryRepository{
		transactor: transactor,
		sync:       NewSyncRepository(),
		audit:      NewAuditRepository(),
	}, nil
}

func (repository *ReminderDeliveryRepository) Load(
	ctx context.Context,
	userID uuid.UUID,
	reminderID uuid.UUID,
) (worker.ReminderDelivery, error) {
	if userID == uuid.Nil || reminderID == uuid.Nil {
		return worker.ReminderDelivery{}, model.ErrNotFound
	}
	var delivery worker.ReminderDelivery
	err := repository.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		row, readErr := db.New(tx).GetReminderDelivery(ctx, pgUUID(userID), pgUUID(reminderID))
		if readErr != nil {
			return mapDatabaseError("get reminder delivery", readErr)
		}
		delivery = reminderDeliveryFromRow(row)
		return nil
	})
	return delivery, err
}

func (repository *ReminderDeliveryRepository) RecordResult(
	ctx context.Context,
	result worker.ReminderDeliveryResult,
) error {
	result.FailureReason = strings.TrimSpace(result.FailureReason)
	if err := validateReminderDeliveryResult(result); err != nil {
		return err
	}
	return repository.transactor.WithUser(ctx, result.UserID, func(ctx context.Context, tx database.Tx) error {
		queries := db.New(tx)
		beforeRow, err := queries.GetReminderDeliveryForUpdate(ctx, pgUUID(result.UserID), pgUUID(result.ReminderID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock reminder delivery: %w", err)
		}
		before := reminderDeliveryFromLockedRow(beforeRow)
		if !reminderResultMatches(before, result) {
			return nil
		}

		updatedRow, err := queries.RecordReminderDeliveryResult(ctx, db.RecordReminderDeliveryResultParams{
			Status: result.Outcome, UserID: pgUUID(result.UserID), ReminderID: pgUUID(result.ReminderID),
			EventID: pgUUID(result.EventID), Channel: result.Channel, ScheduledAt: pgTime(result.ScheduledAt),
			ExpectedVersion: before.Version,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("record reminder delivery result: %w", err)
		}
		updated := calendarReminderFromRow(updatedRow)
		if _, err = repository.sync.Append(ctx, tx, result.UserID, model.SyncChangeDraft{
			EntityType: "reminder", EntityID: result.ReminderID,
			Operation: "update", EntityVersion: updated.Version,
		}); err != nil {
			return err
		}
		beforeJSON, _ := json.Marshal(map[string]any{
			"id": before.ReminderID, "eventId": before.EventID, "channel": before.Channel,
			"scheduledAt": before.ScheduledAt, "status": before.Status, "version": before.Version,
		})
		afterJSON, _ := json.Marshal(updated)
		metadataJSON, _ := json.Marshal(map[string]any{
			"channel": result.Channel, "failureReason": result.FailureReason,
		})
		action := "reminder.delivered"
		if result.Outcome == worker.ReminderFailed {
			action = "reminder.delivery_failed"
		}
		return repository.audit.Append(ctx, tx, result.UserID, model.AuditDraft{
			ID: uuid.New(), ActorType: "system", Action: action, RequestID: result.OutboxEventID,
			BeforeData: beforeJSON, AfterData: afterJSON, Metadata: metadataJSON,
			Entities: []model.AuditEntity{{EntityType: "reminder", EntityID: result.ReminderID}},
		})
	})
}

func validateReminderDeliveryResult(result worker.ReminderDeliveryResult) error {
	if result.UserID == uuid.Nil || result.ReminderID == uuid.Nil || result.EventID == uuid.Nil ||
		result.OutboxEventID == uuid.Nil || result.ScheduledAt.IsZero() ||
		(result.Channel != "email" && result.Channel != "in_app") ||
		(result.Outcome != worker.ReminderDelivered && result.Outcome != worker.ReminderFailed) {
		return errors.New("invalid reminder delivery result")
	}
	if result.Outcome == worker.ReminderFailed && (result.FailureReason == "" || utf8.RuneCountInString(result.FailureReason) > 240) {
		return errors.New("invalid reminder delivery failure reason")
	}
	return nil
}

func reminderResultMatches(delivery worker.ReminderDelivery, result worker.ReminderDeliveryResult) bool {
	return delivery.UserID == result.UserID && delivery.ReminderID == result.ReminderID &&
		delivery.EventID == result.EventID && delivery.Channel == result.Channel &&
		delivery.ScheduledAt.UTC().Equal(result.ScheduledAt.UTC()) && delivery.DeletedAt == nil &&
		(delivery.Status == "pending" || delivery.Status == "processing" || delivery.Status == "failed")
}

func reminderDeliveryFromRow(row *db.GetReminderDeliveryRow) worker.ReminderDelivery {
	return worker.ReminderDelivery{
		UserID: uuid.UUID(row.UserID.Bytes), ReminderID: uuid.UUID(row.ReminderID.Bytes),
		EventID: uuid.UUID(row.EventID.Bytes), Channel: row.Channel, ScheduledAt: row.ScheduledAt.Time.UTC(),
		Status: row.Status, Version: row.ReminderVersion, DeletedAt: optionalTime(row.ReminderDeletedAt),
		AccountStatus: row.AccountStatus, AccountDeletedAt: optionalTime(row.AccountDeletedAt),
		Email: row.Email, DisplayName: row.DisplayName, EventTitle: row.EventTitle,
		EventStartAt: row.EventStartAt.Time.UTC(), Timezone: row.Timezone, EventDeletedAt: optionalTime(row.EventDeletedAt),
	}
}

func reminderDeliveryFromLockedRow(row *db.GetReminderDeliveryForUpdateRow) worker.ReminderDelivery {
	return worker.ReminderDelivery{
		UserID: uuid.UUID(row.UserID.Bytes), ReminderID: uuid.UUID(row.ReminderID.Bytes),
		EventID: uuid.UUID(row.EventID.Bytes), Channel: row.Channel, ScheduledAt: row.ScheduledAt.Time.UTC(),
		Status: row.Status, Version: row.ReminderVersion, DeletedAt: optionalTime(row.ReminderDeletedAt),
		AccountStatus: row.AccountStatus, AccountDeletedAt: optionalTime(row.AccountDeletedAt),
		Email: row.Email, DisplayName: row.DisplayName, EventTitle: row.EventTitle,
		EventStartAt: row.EventStartAt.Time.UTC(), Timezone: row.Timezone, EventDeletedAt: optionalTime(row.EventDeletedAt),
	}
}
