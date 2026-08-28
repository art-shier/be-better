package service

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type memoryIdempotencyStore struct {
	mu       sync.Mutex
	mutation *model.ClientMutation
	complete int
}

func (store *memoryIdempotencyStore) Claim(_ context.Context, _ database.Tx, draft model.ClientMutationDraft) (model.ClientMutation, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.mutation != nil {
		return *store.mutation, false, nil
	}
	mutation := model.ClientMutation{
		ID: draft.ID, UserID: draft.UserID, DeviceID: draft.DeviceID,
		MutationID: draft.MutationID, RequestHash: bytes.Clone(draft.RequestHash),
		CreatedAt: time.Now(), ExpiresAt: draft.ExpiresAt,
	}
	store.mutation = &mutation
	return mutation, true, nil
}

func (store *memoryIdempotencyStore) Complete(_ context.Context, _ database.Tx, userID, mutationID uuid.UUID, status int, body []byte) (model.ClientMutation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.mutation == nil || store.mutation.UserID != userID || store.mutation.ID != mutationID {
		return model.ClientMutation{}, model.ErrNotFound
	}
	store.complete++
	store.mutation.ResponseStatus = &status
	store.mutation.ResponseBody = bytes.Clone(body)
	return *store.mutation, nil
}

func TestIdempotencyServiceClaimsCompletesAndReplaysMatchingRequest(t *testing.T) {
	store := &memoryIdempotencyStore{}
	service, err := NewIdempotencyService(store)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC) }
	key := MutationKey{UserID: uuid.New(), DeviceID: uuid.New(), MutationID: uuid.New()}

	first, err := service.Begin(context.Background(), &testTransaction{}, key, []byte(`{"title":"first"}`))
	if err != nil || first.Replay {
		t.Fatalf("first Begin() = %#v, %v", first, err)
	}
	if _, err = service.Complete(context.Background(), &testTransaction{}, first.Mutation, 201, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	second, err := service.Begin(context.Background(), &testTransaction{}, key, []byte(`{"title":"first"}`))
	if err != nil || !second.Replay || second.Mutation.ResponseStatus == nil || *second.Mutation.ResponseStatus != 201 {
		t.Fatalf("second Begin() = %#v, %v", second, err)
	}
	if store.complete != 1 {
		t.Fatalf("completion count = %d, want 1", store.complete)
	}
}

func TestIdempotencyServiceRejectsMutationIDReuseWithDifferentRequest(t *testing.T) {
	store := &memoryIdempotencyStore{}
	service, _ := NewIdempotencyService(store)
	key := MutationKey{UserID: uuid.New(), DeviceID: uuid.New(), MutationID: uuid.New()}
	if _, err := service.Begin(context.Background(), &testTransaction{}, key, []byte(`{"value":1}`)); err != nil {
		t.Fatal(err)
	}
	_, err := service.Begin(context.Background(), &testTransaction{}, key, []byte(`{"value":2}`))
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("Begin() error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestIdempotencyServiceRejectsCommittedPlaceholderWithoutResult(t *testing.T) {
	store := &memoryIdempotencyStore{}
	service, _ := NewIdempotencyService(store)
	key := MutationKey{UserID: uuid.New(), DeviceID: uuid.New(), MutationID: uuid.New()}
	if _, err := service.Begin(context.Background(), &testTransaction{}, key, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	_, err := service.Begin(context.Background(), &testTransaction{}, key, []byte(`{}`))
	if !errors.Is(err, ErrIdempotencyIncomplete) {
		t.Fatalf("Begin() error = %v, want ErrIdempotencyIncomplete", err)
	}
}
