package agentexecution

import (
	"context"
	"time"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type Store interface {
	LockAccount(context.Context, database.Tx, uuid.UUID) error
	Create(context.Context, database.Tx, Record) error
	Get(context.Context, database.Tx, uuid.UUID, uuid.UUID, bool) (Record, error)
	Active(context.Context, database.Tx, uuid.UUID) ([]Record, error)
	CountCreatedSince(context.Context, database.Tx, uuid.UUID, time.Time) (int, error)
	Save(context.Context, database.Tx, Record, int64, uuid.UUID) error
	Operations(context.Context, database.Tx, uuid.UUID, uuid.UUID) ([]Operation, error)
	PutOperation(context.Context, database.Tx, Operation, string) error
	AddRefs(context.Context, database.Tx, uuid.UUID, uuid.UUID, []model.AgentSourceRefDraft) error
	AddSteps(context.Context, database.Tx, uuid.UUID, uuid.UUID, []model.AgentStepDraft) error
}
