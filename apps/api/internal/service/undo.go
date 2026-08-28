package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type UndoStore interface {
	ApplyUndo(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64, time.Time) (model.UndoResult, error)
}

type UndoService struct {
	store    UndoStore
	commands *CommandService
	now      func() time.Time
}

func NewUndoService(store UndoStore, commands *CommandService) (*UndoService, error) {
	if store == nil || commands == nil {
		return nil, errors.New("undo store and command service are required")
	}
	return &UndoService{store: store, commands: commands, now: time.Now}, nil
}

func (service *UndoService) Undo(ctx context.Context, mutation MutationContext, auditID uuid.UUID, expectedVersion int64) (model.UndoResult, error) {
	if auditID == uuid.Nil || expectedVersion < 1 {
		return model.UndoResult{}, fmt.Errorf("%w: audit ID and current entity version are required", ErrValidation)
	}
	payload, _ := json.Marshal(map[string]any{"auditId": auditID, "expectedVersion": expectedVersion})
	response, err := executeResourceCommand(ctx, service.commands, mutation, "audit.undo", payload, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		result, undoErr := service.store.ApplyUndo(ctx, tx, mutation.UserID, auditID, expectedVersion, service.now().UTC())
		if undoErr != nil {
			return CommandResult{}, undoErr
		}
		return CommandResult{
			Status: 200, Body: resourceJSON(result),
			Changes: []model.SyncChangeDraft{{
				EntityType: result.EntityType, EntityID: result.EntityID,
				Operation: result.EntityOperation, EntityVersion: result.EntityVersion,
			}},
			Audits: []model.AuditDraft{{
				Action: "audit.undo", BeforeData: result.BeforeData, AfterData: result.AfterData,
				Metadata: resourceJSON(map[string]any{"originalAuditId": result.OriginalAuditID}),
				Entities: []model.AuditEntity{{EntityType: result.EntityType, EntityID: result.EntityID}},
			}},
		}, nil
	})
	if err != nil {
		return model.UndoResult{}, err
	}
	var result model.UndoResult
	if err = json.Unmarshal(response.Body, &result); err != nil {
		return model.UndoResult{}, fmt.Errorf("decode undo response: %w", err)
	}
	return result, nil
}
