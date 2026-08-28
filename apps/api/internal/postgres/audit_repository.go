package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"dayorder.local/api/internal/database"
	db "dayorder.local/api/internal/db/gen"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type AuditRepository struct {
	tasks    *TaskRepository
	calendar *CalendarRepository
	content  *ContentRepository
	newUUID  func() uuid.UUID
}

func NewAuditRepository() *AuditRepository {
	return &AuditRepository{
		tasks: NewTaskRepository(), calendar: NewCalendarRepository(), content: NewContentRepository(), newUUID: uuid.New,
	}
}

func (*AuditRepository) Append(
	ctx context.Context,
	tx database.Tx,
	userID uuid.UUID,
	draft model.AuditDraft,
) error {
	if tx == nil {
		return errors.New("audit transaction is required")
	}
	queries := db.New(tx)
	actorID := pgtype.UUID{}
	if draft.ActorID != nil {
		actorID = pgUUID(*draft.ActorID)
	}
	if _, err := queries.CreateAuditEvent(ctx, db.CreateAuditEventParams{
		ID: pgUUID(draft.ID), UserID: pgUUID(userID), ActorType: draft.ActorType,
		ActorID: actorID, Action: draft.Action, RequestID: pgUUID(draft.RequestID),
		BeforeData: bytes.Clone(draft.BeforeData), AfterData: bytes.Clone(draft.AfterData),
		Metadata: bytes.Clone(draft.Metadata),
	}); err != nil {
		return fmt.Errorf("create audit event: %w", err)
	}
	for _, entity := range draft.Entities {
		if err := queries.CreateAuditEventEntity(
			ctx, pgUUID(draft.ID), pgUUID(userID), entity.EntityType, pgUUID(entity.EntityID),
		); err != nil {
			return fmt.Errorf("create audit event entity: %w", err)
		}
	}
	return nil
}

func (repository *AuditRepository) Get(ctx context.Context, tx database.Tx, userID, eventID uuid.UUID) (model.AuditEvent, error) {
	queries := db.New(tx)
	row, err := queries.GetAuditEvent(ctx, pgUUID(userID), pgUUID(eventID))
	if err != nil {
		return model.AuditEvent{}, mapDatabaseError("get audit event", err)
	}
	return repository.hydrateAuditEvent(ctx, queries, userID, auditEventFromRow(row))
}

func (repository *AuditRepository) List(ctx context.Context, tx database.Tx, userID uuid.UUID, after *model.ResourcePosition, limit int) ([]model.AuditEvent, error) {
	afterTime, afterID := pgtype.Timestamptz{}, pgtype.UUID{}
	if after != nil {
		afterTime, afterID = pgTime(after.UpdatedAt), pgUUID(after.ID)
	}
	queries := db.New(tx)
	rows, err := queries.ListAuditEvents(ctx, pgUUID(userID), afterTime, afterID, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	events := make([]model.AuditEvent, 0, len(rows))
	for _, row := range rows {
		event, hydrateErr := repository.hydrateAuditEvent(ctx, queries, userID, auditEventFromRow(row))
		if hydrateErr != nil {
			return nil, hydrateErr
		}
		events = append(events, event)
	}
	return events, nil
}

func (repository *AuditRepository) ApplyUndo(ctx context.Context, tx database.Tx, userID, eventID uuid.UUID, expectedVersion int64, undoneAt time.Time) (model.UndoResult, error) {
	event, err := repository.Get(ctx, tx, userID, eventID)
	if err != nil {
		return model.UndoResult{}, err
	}
	if event.Action != "agent.change.apply" {
		return model.UndoResult{}, model.ErrConflict
	}
	target, err := auditUndoTarget(event.Entities)
	if err != nil {
		return model.UndoResult{}, err
	}
	var afterSnapshot struct {
		Version int64 `json:"version"`
	}
	if json.Unmarshal(event.AfterData, &afterSnapshot) != nil || afterSnapshot.Version != expectedVersion {
		return model.UndoResult{}, model.ErrConflict
	}
	var applied appliedAgentTarget
	if len(event.BeforeData) == 0 {
		applied, err = repository.undoCreatedTarget(ctx, tx, userID, target, expectedVersion)
	} else {
		applied, err = repository.restoreUpdatedTarget(ctx, tx, userID, target, event.BeforeData, expectedVersion, undoneAt)
	}
	if err != nil {
		return model.UndoResult{}, err
	}
	result := model.UndoResult{
		OriginalAuditID: event.ID, EntityType: target.EntityType, EntityID: applied.id,
		EntityOperation: applied.operation, EntityVersion: applied.version,
		BeforeData: applied.before, AfterData: applied.after,
	}
	if applied.operation != "delete" {
		result.Data = bytes.Clone(applied.after)
	}
	return result, nil
}

func (repository *AuditRepository) undoCreatedTarget(ctx context.Context, tx database.Tx, userID uuid.UUID, target model.AuditEntity, expectedVersion int64) (appliedAgentTarget, error) {
	switch target.EntityType {
	case "task":
		before, err := repository.tasks.GetTask(ctx, tx, userID, target.EntityID)
		if err != nil {
			return appliedAgentTarget{}, err
		}
		deleted, err := repository.tasks.DeleteTask(ctx, tx, userID, target.EntityID, expectedVersion)
		if err != nil {
			return appliedAgentTarget{}, err
		}
		return targetResult("delete", deleted.ID, deleted.Version, before, deleted)
	case "calendar_event":
		before, err := repository.calendar.GetEvent(ctx, tx, userID, target.EntityID)
		if err != nil {
			return appliedAgentTarget{}, err
		}
		deleted, reminders, err := repository.calendar.DeleteEvent(ctx, tx, userID, target.EntityID, expectedVersion)
		if err != nil {
			return appliedAgentTarget{}, err
		}
		if len(reminders) != 0 {
			return appliedAgentTarget{}, model.ErrConflict
		}
		return targetResult("delete", deleted.ID, deleted.Version, before, deleted)
	default:
		return appliedAgentTarget{}, model.ErrConflict
	}
}

func (repository *AuditRepository) restoreUpdatedTarget(ctx context.Context, tx database.Tx, userID uuid.UUID, target model.AuditEntity, snapshot json.RawMessage, expectedVersion int64, undoneAt time.Time) (appliedAgentTarget, error) {
	switch target.EntityType {
	case "task":
		current, err := repository.tasks.GetTask(ctx, tx, userID, target.EntityID)
		if err != nil {
			return appliedAgentTarget{}, err
		}
		var restored model.Task
		if err = json.Unmarshal(snapshot, &restored); err != nil || restored.ID != target.EntityID {
			return appliedAgentTarget{}, model.ErrConflict
		}
		updated, err := repository.tasks.UpdateTask(ctx, tx, userID, restored, expectedVersion)
		if err != nil {
			return appliedAgentTarget{}, err
		}
		return targetResult("update", updated.ID, updated.Version, current, updated)
	case "calendar_event":
		current, err := repository.calendar.GetEvent(ctx, tx, userID, target.EntityID)
		if err != nil {
			return appliedAgentTarget{}, err
		}
		var restored model.CalendarEvent
		if err = json.Unmarshal(snapshot, &restored); err != nil || restored.ID != target.EntityID {
			return appliedAgentTarget{}, model.ErrConflict
		}
		updated, reminders, err := repository.calendar.UpdateEvent(ctx, tx, userID, restored, expectedVersion)
		if err != nil {
			return appliedAgentTarget{}, err
		}
		if len(reminders) != 0 {
			return appliedAgentTarget{}, model.ErrConflict
		}
		return targetResult("update", updated.ID, updated.Version, current, updated)
	case "record":
		current, err := repository.content.GetRecord(ctx, tx, userID, target.EntityID)
		if err != nil {
			return appliedAgentTarget{}, err
		}
		var restored model.Record
		if err = json.Unmarshal(snapshot, &restored); err != nil || restored.ID != target.EntityID {
			return appliedAgentTarget{}, model.ErrConflict
		}
		updated, err := repository.content.UpdateRecord(ctx, tx, userID, restored, expectedVersion)
		if err != nil {
			return appliedAgentTarget{}, err
		}
		return targetResult("update", updated.ID, updated.Version, current, updated)
	case "note":
		return repository.restoreNoteLink(ctx, tx, userID, target.EntityID, snapshot, expectedVersion, undoneAt)
	default:
		return appliedAgentTarget{}, model.ErrConflict
	}
}

func (repository *AuditRepository) restoreNoteLink(ctx context.Context, tx database.Tx, userID, noteID uuid.UUID, snapshot json.RawMessage, expectedVersion int64, _ time.Time) (appliedAgentTarget, error) {
	current, err := repository.content.GetNote(ctx, tx, userID, noteID)
	if err != nil {
		return appliedAgentTarget{}, err
	}
	currentLinks, err := repository.content.ListNoteLinks(ctx, tx, userID, noteID)
	if err != nil {
		return appliedAgentTarget{}, err
	}
	for _, link := range currentLinks {
		current.LinkedEntityIDs = append(current.LinkedEntityIDs, link.TargetID)
	}
	var restored model.Note
	if err = json.Unmarshal(snapshot, &restored); err != nil || restored.ID != noteID {
		return appliedAgentTarget{}, model.ErrConflict
	}
	// Note 正文不会进入审计快照；撤销弱关联时保留当前正文。
	restored.BodyMarkdown = current.BodyMarkdown
	updated, err := repository.content.UpdateNote(ctx, tx, userID, restored, expectedVersion)
	if err != nil {
		return appliedAgentTarget{}, err
	}
	links := make([]model.EntityLink, 0, len(restored.LinkedEntityIDs))
	for _, targetID := range restored.LinkedEntityIDs {
		targetType, resolveErr := repository.content.ResolveEntityType(ctx, tx, userID, targetID)
		if resolveErr != nil {
			return appliedAgentTarget{}, resolveErr
		}
		links = append(links, model.EntityLink{
			ID: repository.newUUID(), SourceType: "note", SourceID: noteID,
			TargetType: targetType, TargetID: targetID, RelationType: "references",
		})
	}
	if err = repository.content.ReplaceNoteLinks(ctx, tx, userID, noteID, links); err != nil {
		return appliedAgentTarget{}, err
	}
	updated.LinkedEntityIDs = append([]uuid.UUID(nil), restored.LinkedEntityIDs...)
	return targetResult("update", updated.ID, updated.Version, current, updated)
}

func auditUndoTarget(entities []model.AuditEntity) (model.AuditEntity, error) {
	var target model.AuditEntity
	count := 0
	for _, entity := range entities {
		if entity.EntityType == "agent_run" {
			continue
		}
		if !map[string]bool{"task": true, "calendar_event": true, "record": true, "note": true}[entity.EntityType] {
			return model.AuditEntity{}, model.ErrConflict
		}
		target, count = entity, count+1
	}
	if count != 1 {
		return model.AuditEntity{}, model.ErrConflict
	}
	return target, nil
}

func (repository *AuditRepository) hydrateAuditEvent(ctx context.Context, queries *db.Queries, userID uuid.UUID, event model.AuditEvent) (model.AuditEvent, error) {
	rows, err := queries.ListAuditEventEntities(ctx, pgUUID(userID), pgUUID(event.ID))
	if err != nil {
		return model.AuditEvent{}, fmt.Errorf("list audit event entities: %w", err)
	}
	event.Entities = make([]model.AuditEntity, 0, len(rows))
	for _, row := range rows {
		event.Entities = append(event.Entities, model.AuditEntity{EntityType: row.EntityType, EntityID: uuid.UUID(row.EntityID.Bytes)})
	}
	return event, nil
}

func auditEventFromRow(row *db.DayorderAuditEvent) model.AuditEvent {
	return model.AuditEvent{
		ID: uuid.UUID(row.ID.Bytes), ActorType: row.ActorType, ActorID: optionalUUID(row.ActorID),
		Action: row.Action, RequestID: uuid.UUID(row.RequestID.Bytes), BeforeData: bytes.Clone(row.BeforeData),
		AfterData: bytes.Clone(row.AfterData), Metadata: bytes.Clone(row.Metadata), CreatedAt: row.CreatedAt.Time.UTC(),
		Entities: []model.AuditEntity{},
	}
}

func auditReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrNotFound
	}
	return err
}
