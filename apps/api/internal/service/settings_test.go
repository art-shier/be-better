package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type fakeSettingsStore struct{ value model.UserSettings }

func (store *fakeSettingsStore) Get(context.Context, database.Tx, uuid.UUID) (model.UserSettings, error) {
	return store.value, nil
}
func (store *fakeSettingsStore) Update(_ context.Context, _ database.Tx, _ uuid.UUID, schema int, settings []byte, expected int64) (model.UserSettings, error) {
	if expected != store.value.Version {
		return model.UserSettings{}, model.ErrConflict
	}
	store.value = model.UserSettings{SchemaVersion: schema, Version: expected + 1, Settings: append([]byte(nil), settings...), UpdatedAt: time.Now().UTC()}
	return store.value, nil
}

func TestSettingsServiceAppliesControlledMergePatch(t *testing.T) {
	store := &fakeSettingsStore{value: model.UserSettings{SchemaVersion: 1, Version: 1, Settings: json.RawMessage(`{"energy":3,"permissions":{"goals":true,"calendar":true}}`)}}
	commands := testCommandService(t, &recordingSyncWriter{}, &recordingAuditWriter{})
	service, _ := NewSettingsService(store, immediateUserTransactor{tx: &testTransaction{}}, commands)
	updated, err := service.Patch(context.Background(), testMutation(), 1, json.RawMessage(`{"energy":4,"permissions":{"calendar":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || string(updated.Settings) != `{"energy":4,"permissions":{"calendar":false,"goals":true}}` {
		t.Fatalf("updated=%#v", updated)
	}
	commands = testCommandService(t, &recordingSyncWriter{}, &recordingAuditWriter{})
	service, _ = NewSettingsService(store, immediateUserTransactor{tx: &testTransaction{}}, commands)
	if _, err = service.Patch(context.Background(), testMutation(), 2, json.RawMessage(`{"unknown":true}`)); !errors.Is(err, ErrValidation) {
		t.Fatalf("unknown key error=%v", err)
	}
}
