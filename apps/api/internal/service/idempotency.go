package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

const defaultMutationRetention = 30 * 24 * time.Hour

var (
	ErrIdempotencyConflict   = errors.New("IDEMPOTENCY_CONFLICT")
	ErrIdempotencyIncomplete = errors.New("IDEMPOTENCY_RESULT_INCOMPLETE")
)

type MutationKey struct {
	UserID     uuid.UUID
	DeviceID   uuid.UUID
	MutationID uuid.UUID
}

type IdempotencyDecision struct {
	Mutation model.ClientMutation
	Replay   bool
}

type IdempotencyStore interface {
	Claim(context.Context, database.Tx, model.ClientMutationDraft) (model.ClientMutation, bool, error)
	Complete(context.Context, database.Tx, uuid.UUID, uuid.UUID, int, []byte) (model.ClientMutation, error)
}

type IdempotencyService struct {
	store   IdempotencyStore
	now     func() time.Time
	newUUID func() uuid.UUID
	ttl     time.Duration
}

func NewIdempotencyService(store IdempotencyStore) (*IdempotencyService, error) {
	if store == nil {
		return nil, errors.New("idempotency store is required")
	}
	return &IdempotencyService{
		store: store, now: time.Now, newUUID: uuid.New, ttl: defaultMutationRetention,
	}, nil
}

func (service *IdempotencyService) Begin(
	ctx context.Context,
	tx database.Tx,
	key MutationKey,
	requestBody []byte,
) (IdempotencyDecision, error) {
	if service == nil || service.store == nil {
		return IdempotencyDecision{}, errors.New("idempotency service is required")
	}
	if tx == nil || key.UserID == uuid.Nil || key.DeviceID == uuid.Nil || key.MutationID == uuid.Nil {
		return IdempotencyDecision{}, fmt.Errorf("%w: mutation identity is required", ErrValidation)
	}
	hash := sha256.Sum256(requestBody)
	now := service.now().UTC()
	mutation, claimed, err := service.store.Claim(ctx, tx, model.ClientMutationDraft{
		ID: service.newUUID(), UserID: key.UserID, DeviceID: key.DeviceID,
		MutationID: key.MutationID, RequestHash: hash[:], ExpiresAt: now.Add(service.ttl),
	})
	if err != nil {
		return IdempotencyDecision{}, fmt.Errorf("claim client mutation: %w", err)
	}
	if claimed {
		return IdempotencyDecision{Mutation: cloneClientMutation(mutation)}, nil
	}
	if !bytes.Equal(mutation.RequestHash, hash[:]) {
		return IdempotencyDecision{}, ErrIdempotencyConflict
	}
	if mutation.ResponseStatus == nil {
		return IdempotencyDecision{}, ErrIdempotencyIncomplete
	}
	return IdempotencyDecision{Mutation: cloneClientMutation(mutation), Replay: true}, nil
}

func (service *IdempotencyService) Complete(
	ctx context.Context,
	tx database.Tx,
	mutation model.ClientMutation,
	status int,
	body []byte,
) (model.ClientMutation, error) {
	if service == nil || service.store == nil {
		return model.ClientMutation{}, errors.New("idempotency service is required")
	}
	if tx == nil || mutation.ID == uuid.Nil || mutation.UserID == uuid.Nil {
		return model.ClientMutation{}, fmt.Errorf("%w: claimed mutation is required", ErrValidation)
	}
	if status < 100 || status > 599 {
		return model.ClientMutation{}, fmt.Errorf("%w: response status must be between 100 and 599", ErrValidation)
	}
	validatedBody, err := idempotencyResponseBody(body)
	if err != nil {
		return model.ClientMutation{}, err
	}
	completed, err := service.store.Complete(ctx, tx, mutation.UserID, mutation.ID, status, validatedBody)
	if err != nil {
		return model.ClientMutation{}, fmt.Errorf("complete client mutation: %w", err)
	}
	return cloneClientMutation(completed), nil
}

func idempotencyResponseBody(body []byte) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("%w: response body must be valid JSON", ErrValidation)
	}
	switch decoded.(type) {
	case map[string]any, []any:
	default:
		return nil, fmt.Errorf("%w: response body must be a JSON object or array", ErrValidation)
	}
	compact := bytes.Buffer{}
	if err := json.Compact(&compact, body); err != nil {
		return nil, fmt.Errorf("compact response body: %w", err)
	}
	return compact.Bytes(), nil
}

func cloneClientMutation(mutation model.ClientMutation) model.ClientMutation {
	mutation.RequestHash = bytes.Clone(mutation.RequestHash)
	mutation.ResponseBody = bytes.Clone(mutation.ResponseBody)
	if mutation.ResponseStatus != nil {
		status := *mutation.ResponseStatus
		mutation.ResponseStatus = &status
	}
	return mutation
}
