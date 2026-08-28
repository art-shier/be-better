package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type AuditReadStore interface {
	Get(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.AuditEvent, error)
	List(context.Context, database.Tx, uuid.UUID, *model.ResourcePosition, int) ([]model.AuditEvent, error)
}

type AuditQueryService struct {
	store      AuditReadStore
	transactor UserTransactor
	cursors    *ResourceCursorCodec
}

type AuditPage struct {
	Events     []model.AuditEvent `json:"events"`
	NextCursor string             `json:"nextCursor,omitempty"`
	HasMore    bool               `json:"hasMore"`
}

func NewAuditQueryService(store AuditReadStore, transactor UserTransactor, cursors *ResourceCursorCodec) (*AuditQueryService, error) {
	if store == nil || transactor == nil || cursors == nil {
		return nil, errors.New("audit read store, transactor, and cursors are required")
	}
	return &AuditQueryService{store: store, transactor: transactor, cursors: cursors}, nil
}

func (service *AuditQueryService) Get(ctx context.Context, userID, eventID uuid.UUID) (model.AuditEvent, error) {
	if userID == uuid.Nil || eventID == uuid.Nil {
		return model.AuditEvent{}, fmt.Errorf("%w: audit user and event IDs are required", ErrValidation)
	}
	var event model.AuditEvent
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var readErr error
		event, readErr = service.store.Get(ctx, tx, userID, eventID)
		return readErr
	})
	if err != nil {
		return model.AuditEvent{}, err
	}
	event.Undoable = auditUndoable(event)
	return event, nil
}

func (service *AuditQueryService) List(ctx context.Context, userID uuid.UUID, cursor string, limit int) (AuditPage, error) {
	if userID == uuid.Nil || limit < 1 || limit > maxResourcePageSize {
		return AuditPage{}, fmt.Errorf("%w: invalid audit list request", ErrValidation)
	}
	var after *model.ResourcePosition
	if cursor != "" {
		decoded, err := service.cursors.Decode(userID, "audit-events", cursor)
		if err != nil {
			return AuditPage{}, err
		}
		after = &decoded
	}
	var events []model.AuditEvent
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var readErr error
		events, readErr = service.store.List(ctx, tx, userID, after, limit+1)
		return readErr
	})
	if err != nil {
		return AuditPage{}, err
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	for index := range events {
		events[index].Undoable = auditUndoable(events[index])
	}
	next := ""
	if hasMore {
		last := events[len(events)-1]
		next, err = service.cursors.Encode(userID, "audit-events", model.ResourcePosition{UpdatedAt: last.CreatedAt, ID: last.ID})
	}
	return AuditPage{Events: events, NextCursor: next, HasMore: hasMore}, err
}

func auditUndoable(event model.AuditEvent) bool {
	if event.Action != "agent.change.apply" || len(event.AfterData) == 0 {
		return false
	}
	targets := 0
	for _, entity := range event.Entities {
		if entity.EntityType == "agent_run" {
			continue
		}
		if !map[string]bool{"task": true, "calendar_event": true, "record": true, "note": true}[entity.EntityType] {
			return false
		}
		targets++
	}
	if targets != 1 {
		return false
	}
	var after struct {
		Version int64 `json:"version"`
	}
	return json.Unmarshal(event.AfterData, &after) == nil && after.Version > 0
}
