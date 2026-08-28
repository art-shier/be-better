package postgres

import (
	"bytes"
	"context"
	"errors"

	"dayorder.local/api/internal/database"
	db "dayorder.local/api/internal/db/gen"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type IdempotencyRepository struct{}

func NewIdempotencyRepository() *IdempotencyRepository { return &IdempotencyRepository{} }

func (*IdempotencyRepository) Claim(
	ctx context.Context,
	tx database.Tx,
	draft model.ClientMutationDraft,
) (model.ClientMutation, bool, error) {
	if tx == nil {
		return model.ClientMutation{}, false, errors.New("idempotency transaction is required")
	}
	queries := db.New(tx)
	if _, err := queries.GetActiveUserDevice(ctx, pgUUID(draft.UserID), pgUUID(draft.DeviceID)); errors.Is(err, pgx.ErrNoRows) {
		return model.ClientMutation{}, false, model.ErrDeviceNotActive
	} else if err != nil {
		return model.ClientMutation{}, false, mapDatabaseError("validate mutation device", err)
	}
	row, err := queries.CreateClientMutation(ctx, db.CreateClientMutationParams{
		ID: pgUUID(draft.ID), UserID: pgUUID(draft.UserID), DeviceID: pgUUID(draft.DeviceID),
		MutationID: pgUUID(draft.MutationID), RequestHash: bytes.Clone(draft.RequestHash),
		ExpiresAt: pgTime(draft.ExpiresAt),
	})
	if err == nil {
		return clientMutationFromRow(row), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.ClientMutation{}, false, mapDatabaseError("claim client mutation", err)
	}
	row, err = queries.GetClientMutation(
		ctx, pgUUID(draft.UserID), pgUUID(draft.DeviceID), pgUUID(draft.MutationID),
	)
	if err != nil {
		return model.ClientMutation{}, false, mapDatabaseError("read existing client mutation", err)
	}
	return clientMutationFromRow(row), false, nil
}

func (*IdempotencyRepository) Complete(
	ctx context.Context,
	tx database.Tx,
	userID uuid.UUID,
	mutationID uuid.UUID,
	status int,
	body []byte,
) (model.ClientMutation, error) {
	if tx == nil {
		return model.ClientMutation{}, errors.New("idempotency transaction is required")
	}
	row, err := db.New(tx).CompleteClientMutation(
		ctx, pgtype.Int4{Int32: int32(status), Valid: true}, bytes.Clone(body),
		pgUUID(userID), pgUUID(mutationID),
	)
	if err != nil {
		return model.ClientMutation{}, mapDatabaseError("complete client mutation", err)
	}
	return clientMutationFromRow(row), nil
}

func clientMutationFromRow(row *db.DayorderClientMutation) model.ClientMutation {
	mutation := model.ClientMutation{
		ID: uuid.UUID(row.ID.Bytes), UserID: uuid.UUID(row.UserID.Bytes),
		DeviceID: uuid.UUID(row.DeviceID.Bytes), MutationID: uuid.UUID(row.MutationID.Bytes),
		RequestHash: bytes.Clone(row.RequestHash), ResponseBody: bytes.Clone(row.ResponseBody),
		CreatedAt: row.CreatedAt.Time.UTC(), ExpiresAt: row.ExpiresAt.Time.UTC(),
	}
	if row.ResponseStatus.Valid {
		status := int(row.ResponseStatus.Int32)
		mutation.ResponseStatus = &status
	}
	return mutation
}
