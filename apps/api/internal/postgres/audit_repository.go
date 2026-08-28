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
	"github.com/jackc/pgx/v5/pgtype"
)

type AuditRepository struct{}

func NewAuditRepository() *AuditRepository { return &AuditRepository{} }

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
