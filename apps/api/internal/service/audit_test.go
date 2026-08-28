package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type recordingAuditStore struct {
	drafts []model.AuditDraft
}

func (store *recordingAuditStore) Append(_ context.Context, _ database.Tx, _ uuid.UUID, draft model.AuditDraft) error {
	store.drafts = append(store.drafts, draft)
	return nil
}

func TestAuditServiceFiltersSecretsAndNoteBodiesRecursively(t *testing.T) {
	store := &recordingAuditStore{}
	service, err := NewAuditService(store)
	if err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	noteID := uuid.New()
	err = service.Record(context.Background(), &testTransaction{}, userID, []model.AuditDraft{{
		Action: "note.update",
		BeforeData: json.RawMessage(`{
			"title":"safe","body":"private note","password":"p",
			"nested":{"apiKey":"key","sessionToken":"token","visible":true}
		}`),
		AfterData: json.RawMessage(`{"title":"changed","markdown":"restricted"}`),
		Metadata:  json.RawMessage(`{"cookie":"secret","source":"web"}`),
		Entities:  []model.AuditEntity{{EntityType: "note", EntityID: noteID}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.drafts) != 1 {
		t.Fatalf("draft count = %d", len(store.drafts))
	}
	encoded, _ := json.Marshal(store.drafts[0])
	text := string(encoded)
	for _, forbidden := range []string{"private note", "restricted", `"p"`, "key", "token", "secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("audit retained restricted value %q: %s", forbidden, text)
		}
	}
	for _, expected := range []string{"safe", "changed", "visible", "source", "web"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("audit lost allowed value %q: %s", expected, text)
		}
	}
	if store.drafts[0].ActorType != "user" || store.drafts[0].ActorID == nil || *store.drafts[0].ActorID != userID {
		t.Fatalf("default actor = %q/%v", store.drafts[0].ActorType, store.drafts[0].ActorID)
	}
}

func TestAuditServiceBoundsLargeSnapshotsAndDeduplicatesEntities(t *testing.T) {
	store := &recordingAuditStore{}
	service, _ := NewAuditService(store)
	entityID := uuid.New()
	err := service.Record(context.Background(), &testTransaction{}, uuid.New(), []model.AuditDraft{{
		Action:     "task.update",
		BeforeData: json.RawMessage(`{"description":"` + strings.Repeat("x", maxAuditSnapshotBytes*2) + `"}`),
		Entities: []model.AuditEntity{
			{EntityType: "task", EntityID: entityID},
			{EntityType: "task", EntityID: entityID},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	draft := store.drafts[0]
	if len(draft.BeforeData) > maxAuditSnapshotBytes || !strings.Contains(string(draft.BeforeData), `"_truncated":true`) {
		t.Fatalf("bounded snapshot length/content = %d %s", len(draft.BeforeData), draft.BeforeData)
	}
	if len(draft.Entities) != 1 {
		t.Fatalf("entity count = %d, want 1", len(draft.Entities))
	}
}

func TestAuditServiceRejectsNonObjectSnapshot(t *testing.T) {
	store := &recordingAuditStore{}
	service, _ := NewAuditService(store)
	err := service.Record(context.Background(), &testTransaction{}, uuid.New(), []model.AuditDraft{{
		Action: "task.update", BeforeData: json.RawMessage(`["not","an","object"]`),
	}})
	if !errors.Is(err, ErrValidation) || len(store.drafts) != 0 {
		t.Fatalf("Record() error = %v, drafts = %d", err, len(store.drafts))
	}
}

func TestAuditServiceRejectsSpoofedUserActor(t *testing.T) {
	store := &recordingAuditStore{}
	service, _ := NewAuditService(store)
	actorID := uuid.New()
	err := service.Record(context.Background(), &testTransaction{}, uuid.New(), []model.AuditDraft{{
		Action: "task.update", ActorType: "user", ActorID: &actorID,
	}})
	if !errors.Is(err, ErrValidation) || len(store.drafts) != 0 {
		t.Fatalf("Record() error = %v, drafts = %d", err, len(store.drafts))
	}
}
