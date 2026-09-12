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

type tagContentStore struct {
	ContentStore
	tag               model.Tag
	created           bool
	ensureName        string
	ensureNormalized  string
	updatedName       string
	updatedNormalized string
	deletedVersion    int64
	record            model.Record
	cleanupCalls      int
}

func (store *tagContentStore) EnsureTag(_ context.Context, _ database.Tx, _ uuid.UUID, id uuid.UUID, name, normalized string) (model.Tag, bool, error) {
	store.ensureName = name
	store.ensureNormalized = normalized
	store.tag = model.Tag{ID: id, Name: name, Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	return store.tag, store.created, nil
}

func (store *tagContentStore) GetTag(_ context.Context, _ database.Tx, _ uuid.UUID, _ uuid.UUID) (model.Tag, error) {
	return store.tag, nil
}

func (store *tagContentStore) UpdateTag(_ context.Context, _ database.Tx, _ uuid.UUID, value model.Tag, expected int64, normalized string) (model.Tag, error) {
	store.updatedName = value.Name
	store.updatedNormalized = normalized
	value.Version = expected + 1
	store.tag = value
	return value, nil
}

func (store *tagContentStore) DeleteTag(_ context.Context, _ database.Tx, _ uuid.UUID, id uuid.UUID, expected int64) (model.Tag, error) {
	store.deletedVersion = expected
	store.tag.ID = id
	store.tag.Version = expected + 1
	return store.tag, nil
}

func (store *tagContentStore) GetRecord(_ context.Context, _ database.Tx, _ uuid.UUID, _ uuid.UUID) (model.Record, error) {
	return store.record, nil
}

func (store *tagContentStore) UpdateRecord(_ context.Context, _ database.Tx, _ uuid.UUID, value model.Record, expected int64) (model.Record, error) {
	value.Version = expected + 1
	store.record = value
	return value, nil
}

func (*tagContentStore) ListRecordTags(context.Context, database.Tx, uuid.UUID, uuid.UUID) ([]model.Tag, error) {
	return nil, nil
}

func (*tagContentStore) ReplaceRecordTags(context.Context, database.Tx, uuid.UUID, uuid.UUID, []model.Tag) error {
	return nil
}

func (store *tagContentStore) CleanupTags(context.Context, database.Tx, uuid.UUID) ([]model.Tag, error) {
	store.cleanupCalls++
	return nil, nil
}

func TestRemovingTheLastReferenceDoesNotDeleteGlobalTag(t *testing.T) {
	id := uuid.New()
	store := &tagContentStore{record: model.Record{ID: id, RawText: "记录", Kind: "idea", OccurredAt: time.Now(), Version: 2}}
	cursors, _ := NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	content, _ := NewContentService(store, immediateUserTransactor{tx: &testTransaction{}}, testCommandService(t, &recordingSyncWriter{}, &recordingAuditWriter{}), cursors)
	mutation := MutationContext{UserID: uuid.New(), DeviceID: uuid.New(), MutationID: uuid.New()}

	_, err := content.UpdateRecord(context.Background(), mutation, id, 2, RecordInput{RawText: "记录", Kind: "idea", OccurredAt: store.record.OccurredAt, Tags: []string{}})

	if err != nil {
		t.Fatal(err)
	}
	if store.cleanupCalls != 0 {
		t.Fatalf("global tag cleanup calls=%d", store.cleanupCalls)
	}
}

func TestGetGlobalTagReturnsStoredResource(t *testing.T) {
	id := uuid.New()
	want := model.Tag{ID: id, Name: "产品", Version: 2}
	store := &tagContentStore{tag: want}
	cursors, _ := NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	content, _ := NewContentService(store, immediateUserTransactor{tx: &testTransaction{}}, testCommandService(t, &recordingSyncWriter{}, &recordingAuditWriter{}), cursors)

	got, err := content.GetTag(context.Background(), uuid.New(), id)

	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("tag=%#v want=%#v", got, want)
	}
}

func TestDeleteGlobalTagRecordsOneDeleteChange(t *testing.T) {
	id := uuid.New()
	store := &tagContentStore{tag: model.Tag{ID: id, Name: "产品", Version: 2}}
	syncWriter := &recordingSyncWriter{}
	cursors, _ := NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	content, _ := NewContentService(store, immediateUserTransactor{tx: &testTransaction{}}, testCommandService(t, syncWriter, &recordingAuditWriter{}), cursors)
	mutation := MutationContext{UserID: uuid.New(), DeviceID: uuid.New(), MutationID: uuid.New()}

	err := content.DeleteTag(context.Background(), mutation, id, 2)

	if err != nil {
		t.Fatal(err)
	}
	if store.deletedVersion != 2 {
		t.Fatalf("deleted version=%d", store.deletedVersion)
	}
	if len(syncWriter.changes) != 1 || syncWriter.changes[0].EntityType != "tag" || syncWriter.changes[0].Operation != "delete" || syncWriter.changes[0].EntityVersion != 3 {
		t.Fatalf("sync changes=%#v", syncWriter.changes)
	}
}

func TestRenameGlobalTagUpdatesTheTagResource(t *testing.T) {
	id := uuid.New()
	store := &tagContentStore{tag: model.Tag{ID: id, Name: "产品", Version: 2}}
	syncWriter := &recordingSyncWriter{}
	cursors, _ := NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	content, _ := NewContentService(store, immediateUserTransactor{tx: &testTransaction{}}, testCommandService(t, syncWriter, &recordingAuditWriter{}), cursors)
	mutation := MutationContext{UserID: uuid.New(), DeviceID: uuid.New(), MutationID: uuid.New()}

	tag, err := content.UpdateTag(context.Background(), mutation, id, 2, "  产品设计  ")

	if err != nil {
		t.Fatal(err)
	}
	if tag.Name != "产品设计" || tag.Version != 3 || store.updatedName != "产品设计" || store.updatedNormalized != "产品设计" {
		t.Fatalf("tag=%#v updatedName=%q normalized=%q", tag, store.updatedName, store.updatedNormalized)
	}
	if len(syncWriter.changes) != 1 || syncWriter.changes[0].EntityType != "tag" || syncWriter.changes[0].Operation != "update" {
		t.Fatalf("sync changes=%#v", syncWriter.changes)
	}
}

func TestCreateGlobalTagNormalizesNameAndRecordsStandaloneResource(t *testing.T) {
	store := &tagContentStore{created: true}
	syncWriter := &recordingSyncWriter{}
	cursors, _ := NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	content, err := NewContentService(store, immediateUserTransactor{tx: &testTransaction{}}, testCommandService(t, syncWriter, &recordingAuditWriter{}), cursors)
	if err != nil {
		t.Fatal(err)
	}
	mutation := MutationContext{UserID: uuid.New(), DeviceID: uuid.New(), MutationID: uuid.New()}
	id := uuid.New()

	tag, err := content.CreateTag(context.Background(), mutation, id, "  灵感  ")

	if err != nil {
		t.Fatal(err)
	}
	if tag.ID != id || store.ensureName != "灵感" || store.ensureNormalized != "灵感" {
		t.Fatalf("tag=%#v name=%q normalized=%q", tag, store.ensureName, store.ensureNormalized)
	}
	if len(syncWriter.changes) != 1 || syncWriter.changes[0].EntityType != "tag" || syncWriter.changes[0].Operation != "create" {
		t.Fatalf("sync changes=%#v", syncWriter.changes)
	}
}

func TestContentValidationNormalizesTagsAndBoundsScores(t *testing.T) {
	tags, err := normalizeTagNames([]string{" Work ", "work", "健康"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0] != "Work" || tags[1] != "健康" {
		t.Fatalf("tags=%#v", tags)
	}
	invalid := 6
	err = validateRecord(model.Record{ID: uuid.New(), RawText: "entry", Kind: "status", OccurredAt: time.Now(), Mood: &invalid})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("record validation error=%v", err)
	}
}

func TestNormalizeLinkedEntityIDsDeduplicatesAndRejectsInvalidLinks(t *testing.T) {
	source, first, second := uuid.New(), uuid.New(), uuid.New()
	values, err := normalizeLinkedEntityIDs(source, []uuid.UUID{first, first, second})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0] != first || values[1] != second {
		t.Fatalf("linked ids=%#v", values)
	}
	for _, invalid := range [][]uuid.UUID{{uuid.Nil}, {source}} {
		if _, err = normalizeLinkedEntityIDs(source, invalid); !errors.Is(err, ErrValidation) {
			t.Fatalf("invalid links %#v error=%v", invalid, err)
		}
	}
}
