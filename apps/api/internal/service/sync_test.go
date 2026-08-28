package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type memorySyncStore struct {
	changes []model.SyncChange
}

func (store *memorySyncStore) Append(_ context.Context, _ database.Tx, _ uuid.UUID, draft model.SyncChangeDraft) (model.SyncChange, error) {
	change := model.SyncChange{
		Sequence: int64(len(store.changes) + 1), EntityType: draft.EntityType,
		EntityID: draft.EntityID, Operation: draft.Operation, EntityVersion: draft.EntityVersion,
		ChangedAt: time.Now().UTC(),
	}
	store.changes = append(store.changes, change)
	return change, nil
}

func (store *memorySyncStore) CurrentCursor(_ context.Context, _ database.Tx, _ uuid.UUID) (int64, error) {
	if len(store.changes) == 0 {
		return 0, nil
	}
	return store.changes[len(store.changes)-1].Sequence, nil
}

func (store *memorySyncStore) List(_ context.Context, _ database.Tx, _ uuid.UUID, after int64, limit int) ([]model.SyncChange, error) {
	result := make([]model.SyncChange, 0, limit)
	for _, change := range store.changes {
		if change.Sequence > after {
			result = append(result, change)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func TestSyncServiceRecordsDeleteTombstoneAndPaginatesOpaqueCursor(t *testing.T) {
	store := &memorySyncStore{}
	service, err := NewSyncService(
		store, immediateUserTransactor{tx: &testTransaction{}},
		[]byte("0123456789abcdef0123456789abcdef"),
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	userID := uuid.New()
	entityID := uuid.New()
	if err = service.Record(context.Background(), &testTransaction{}, userID, []model.SyncChangeDraft{
		{EntityType: "task", EntityID: uuid.New(), Operation: "create", EntityVersion: 1},
		{EntityType: "task", EntityID: entityID, Operation: "delete", EntityVersion: 4},
	}); err != nil {
		t.Fatal(err)
	}
	cursor, err := service.CurrentCursor(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if cursor == "" || cursor == "2" {
		t.Fatalf("cursor is not opaque: %q", cursor)
	}
	start, err := service.encodeCursor(userID, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Changes(context.Background(), userID, start, 1)
	if err != nil || len(first.Changes) != 1 || !first.HasMore {
		t.Fatalf("first Changes() = %#v, %v", first, err)
	}
	second, err := service.Changes(context.Background(), userID, first.NextCursor, 10)
	if err != nil || len(second.Changes) != 1 || second.HasMore {
		t.Fatalf("second Changes() = %#v, %v", second, err)
	}
	if second.Changes[0].Operation != "delete" || second.Changes[0].EntityID != entityID {
		t.Fatalf("delete tombstone = %#v", second.Changes[0])
	}
}

func TestSyncCursorIsTamperEvidentUserBoundAndExpires(t *testing.T) {
	service, _ := NewSyncService(
		&memorySyncStore{}, immediateUserTransactor{tx: &testTransaction{}},
		[]byte("0123456789abcdef0123456789abcdef"),
	)
	issuedAt := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return issuedAt }
	userID := uuid.New()
	cursor, err := service.encodeCursor(userID, 42, issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	replacement := "A"
	if cursor[len(cursor)-1:] == replacement {
		replacement = "Q"
	}
	tampered := cursor[:len(cursor)-1] + replacement
	if _, err = service.Changes(context.Background(), userID, tampered, 100); !errors.Is(err, ErrInvalidSyncCursor) {
		t.Fatalf("tampered cursor error = %v", err)
	}
	if _, err = service.Changes(context.Background(), uuid.New(), cursor, 100); !errors.Is(err, ErrInvalidSyncCursor) {
		t.Fatalf("cross-user cursor error = %v", err)
	}
	service.now = func() time.Time { return issuedAt.Add(90*24*time.Hour + time.Second) }
	if _, err = service.Changes(context.Background(), userID, cursor, 100); !errors.Is(err, ErrSyncResetRequired) {
		t.Fatalf("expired cursor error = %v, want ErrSyncResetRequired", err)
	}
}

func TestSyncServiceRejectsInvalidChangeBeforeWriting(t *testing.T) {
	store := &memorySyncStore{}
	service, _ := NewSyncService(
		store, immediateUserTransactor{tx: &testTransaction{}},
		[]byte("0123456789abcdef0123456789abcdef"),
	)
	err := service.Record(context.Background(), &testTransaction{}, uuid.New(), []model.SyncChangeDraft{
		{EntityType: "task", EntityID: uuid.New(), Operation: "overwrite", EntityVersion: 1},
	})
	if !errors.Is(err, ErrValidation) || len(store.changes) != 0 {
		t.Fatalf("Record() error = %v, stored = %d", err, len(store.changes))
	}
}
