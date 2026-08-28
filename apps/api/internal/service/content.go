package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type ContentStore interface {
	CreateRecord(context.Context, database.Tx, uuid.UUID, model.Record) (model.Record, error)
	GetRecord(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.Record, error)
	ListRecords(context.Context, database.Tx, uuid.UUID, *model.ResourcePosition, int) ([]model.Record, error)
	UpdateRecord(context.Context, database.Tx, uuid.UUID, model.Record, int64) (model.Record, error)
	DeleteRecord(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64) (model.Record, error)
	CreateNote(context.Context, database.Tx, uuid.UUID, model.Note) (model.Note, error)
	GetNote(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.Note, error)
	ListNotes(context.Context, database.Tx, uuid.UUID, *model.ResourcePosition, int) ([]model.Note, error)
	SearchNotes(context.Context, database.Tx, uuid.UUID, string, int) ([]model.Note, error)
	UpdateNote(context.Context, database.Tx, uuid.UUID, model.Note, int64) (model.Note, error)
	DeleteNote(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64) (model.Note, error)
	CreateReview(context.Context, database.Tx, uuid.UUID, model.DailyReview) (model.DailyReview, error)
	GetReview(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.DailyReview, error)
	ListReviews(context.Context, database.Tx, uuid.UUID, *model.ResourcePosition, int) ([]model.DailyReview, error)
	UpdateReview(context.Context, database.Tx, uuid.UUID, model.DailyReview, int64) (model.DailyReview, error)
	DeleteReview(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64) (model.DailyReview, error)
	EnsureTag(context.Context, database.Tx, uuid.UUID, uuid.UUID, string, string) (model.Tag, bool, error)
	ListTags(context.Context, database.Tx, uuid.UUID, int) ([]model.Tag, error)
	ReplaceRecordTags(context.Context, database.Tx, uuid.UUID, uuid.UUID, []model.Tag) error
	ReplaceNoteTags(context.Context, database.Tx, uuid.UUID, uuid.UUID, []model.Tag) error
	ListRecordTags(context.Context, database.Tx, uuid.UUID, uuid.UUID) ([]model.Tag, error)
	ListNoteTags(context.Context, database.Tx, uuid.UUID, uuid.UUID) ([]model.Tag, error)
	ResolveEntityType(context.Context, database.Tx, uuid.UUID, uuid.UUID) (string, error)
	ReplaceNoteLinks(context.Context, database.Tx, uuid.UUID, uuid.UUID, []model.EntityLink) error
	ListNoteLinks(context.Context, database.Tx, uuid.UUID, uuid.UUID) ([]model.EntityLink, error)
	CleanupTags(context.Context, database.Tx, uuid.UUID) ([]model.Tag, error)
}

type ContentService struct {
	store      ContentStore
	transactor UserTransactor
	commands   *CommandService
	cursors    *ResourceCursorCodec
	newUUID    func() uuid.UUID
}
type RecordInput struct {
	ID         *uuid.UUID `json:"id,omitempty"`
	RawText    string     `json:"rawText"`
	Kind       string     `json:"kind"`
	OccurredAt time.Time  `json:"occurredAt"`
	Mood       *int       `json:"mood"`
	Energy     *int       `json:"energy"`
	ArchivedAt *time.Time `json:"archivedAt"`
	Tags       []string   `json:"tags"`
}
type NoteInput struct {
	ID              *uuid.UUID  `json:"id,omitempty"`
	Title           string      `json:"title"`
	BodyMarkdown    string      `json:"bodyMarkdown"`
	Category        string      `json:"category"`
	ArchivedAt      *time.Time  `json:"archivedAt"`
	Tags            []string    `json:"tags"`
	LinkedEntityIDs []uuid.UUID `json:"linkedEntityIds"`
}
type ReviewInput struct {
	ID            *uuid.UUID `json:"id,omitempty"`
	ReviewDate    string     `json:"reviewDate"`
	Wins          string     `json:"wins"`
	Blockers      string     `json:"blockers"`
	Mood          *int       `json:"mood"`
	Energy        *int       `json:"energy"`
	TomorrowFocus string     `json:"tomorrowFocus"`
	AISummary     *string    `json:"aiSummary"`
}
type RecordPage struct {
	Records    []model.Record `json:"records"`
	NextCursor string         `json:"nextCursor,omitempty"`
	HasMore    bool           `json:"hasMore"`
}
type NotePage struct {
	Notes      []model.Note `json:"notes"`
	NextCursor string       `json:"nextCursor,omitempty"`
	HasMore    bool         `json:"hasMore"`
}
type ReviewPage struct {
	Reviews    []model.DailyReview `json:"reviews"`
	NextCursor string              `json:"nextCursor,omitempty"`
	HasMore    bool                `json:"hasMore"`
}

func NewContentService(store ContentStore, transactor UserTransactor, commands *CommandService, cursors *ResourceCursorCodec) (*ContentService, error) {
	if store == nil || transactor == nil || commands == nil || cursors == nil {
		return nil, errors.New("content dependencies are required")
	}
	return &ContentService{store: store, transactor: transactor, commands: commands, cursors: cursors, newUUID: uuid.New}, nil
}

func (service *ContentService) CreateRecord(ctx context.Context, mutation MutationContext, input RecordInput) (model.Record, error) {
	identifier := service.newUUID()
	if input.ID != nil {
		identifier = *input.ID
	}
	value := recordFromInput(identifier, input)
	if err := validateRecord(value); err != nil {
		return model.Record{}, err
	}
	names, err := normalizeTagNames(input.Tags)
	if err != nil {
		return model.Record{}, err
	}
	payload, _ := json.Marshal(input)
	response, err := executeResourceCommand(ctx, service.commands, mutation, "record.create", payload, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		created, e := service.store.CreateRecord(ctx, tx, mutation.UserID, value)
		if e != nil {
			return CommandResult{}, e
		}
		tags, tagChanges, e := service.ensureTags(ctx, tx, mutation.UserID, names)
		if e != nil {
			return CommandResult{}, e
		}
		if e = service.store.ReplaceRecordTags(ctx, tx, mutation.UserID, created.ID, tags); e != nil {
			return CommandResult{}, e
		}
		created.Tags = tags
		changes := append([]model.SyncChangeDraft{{EntityType: "record", EntityID: created.ID, Operation: "create", EntityVersion: created.Version}}, tagChanges...)
		return CommandResult{Status: 201, Body: resourceJSON(created), Changes: changes, Audits: []model.AuditDraft{{Action: "record.create", AfterData: resourceJSON(created), Entities: []model.AuditEntity{{EntityType: "record", EntityID: created.ID}}}}}, nil
	})
	return decodeRecord(response, err)
}

func (service *ContentService) GetRecord(ctx context.Context, userID, id uuid.UUID) (model.Record, error) {
	var value model.Record
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var e error
		value, e = service.store.GetRecord(ctx, tx, userID, id)
		if e != nil {
			return e
		}
		value.Tags, e = service.store.ListRecordTags(ctx, tx, userID, id)
		return e
	})
	return value, err
}
func (service *ContentService) ListRecords(ctx context.Context, userID uuid.UUID, cursor string, limit int) (RecordPage, error) {
	if limit < 1 || limit > maxResourcePageSize {
		return RecordPage{}, fmt.Errorf("%w: invalid page size", ErrValidation)
	}
	after, err := service.decodePosition(userID, "records", cursor)
	if err != nil {
		return RecordPage{}, err
	}
	var values []model.Record
	err = service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var e error
		values, e = service.store.ListRecords(ctx, tx, userID, after, limit+1)
		if e != nil {
			return e
		}
		for index := range values {
			values[index].Tags, e = service.store.ListRecordTags(ctx, tx, userID, values[index].ID)
			if e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return RecordPage{}, err
	}
	hasMore := len(values) > limit
	if hasMore {
		values = values[:limit]
	}
	next := ""
	if hasMore {
		last := values[len(values)-1]
		next, err = service.cursors.Encode(userID, "records", model.ResourcePosition{UpdatedAt: last.OccurredAt, ID: last.ID})
	}
	return RecordPage{Records: values, NextCursor: next, HasMore: hasMore}, err
}

func (service *ContentService) UpdateRecord(ctx context.Context, mutation MutationContext, id uuid.UUID, expected int64, input RecordInput) (model.Record, error) {
	value := recordFromInput(id, input)
	if expected < 1 {
		return model.Record{}, fmt.Errorf("%w: expected version is required", ErrValidation)
	}
	if err := validateRecord(value); err != nil {
		return model.Record{}, err
	}
	names, err := normalizeTagNames(input.Tags)
	if err != nil {
		return model.Record{}, err
	}
	payload, _ := json.Marshal(map[string]any{"id": id, "expectedVersion": expected, "input": input})
	response, err := executeResourceCommand(ctx, service.commands, mutation, "record.update", payload, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		before, e := service.store.GetRecord(ctx, tx, mutation.UserID, id)
		if e != nil {
			return CommandResult{}, e
		}
		before.Tags, e = service.store.ListRecordTags(ctx, tx, mutation.UserID, id)
		if e != nil {
			return CommandResult{}, e
		}
		updated, e := service.store.UpdateRecord(ctx, tx, mutation.UserID, value, expected)
		if e != nil {
			return CommandResult{}, e
		}
		tags, tagChanges, e := service.ensureTags(ctx, tx, mutation.UserID, names)
		if e != nil {
			return CommandResult{}, e
		}
		if e = service.store.ReplaceRecordTags(ctx, tx, mutation.UserID, id, tags); e != nil {
			return CommandResult{}, e
		}
		removed, e := service.store.CleanupTags(ctx, tx, mutation.UserID)
		if e != nil {
			return CommandResult{}, e
		}
		for _, tag := range removed {
			tagChanges = append(tagChanges, model.SyncChangeDraft{EntityType: "tag", EntityID: tag.ID, Operation: "delete", EntityVersion: tag.Version})
		}
		updated.Tags = tags
		return CommandResult{Status: 200, Body: resourceJSON(updated), Changes: append([]model.SyncChangeDraft{{EntityType: "record", EntityID: id, Operation: "update", EntityVersion: updated.Version}}, tagChanges...), Audits: []model.AuditDraft{{Action: "record.update", BeforeData: resourceJSON(before), AfterData: resourceJSON(updated), Entities: []model.AuditEntity{{EntityType: "record", EntityID: id}}}}}, nil
	})
	return decodeRecord(response, err)
}
func (service *ContentService) DeleteRecord(ctx context.Context, mutation MutationContext, id uuid.UUID, expected int64) error {
	return service.deleteContent(ctx, mutation, "record", id, expected, func(ctx context.Context, tx database.Tx) (any, any, int64, error) {
		before, e := service.store.GetRecord(ctx, tx, mutation.UserID, id)
		if e != nil {
			return nil, nil, 0, e
		}
		before.Tags, e = service.store.ListRecordTags(ctx, tx, mutation.UserID, id)
		if e != nil {
			return nil, nil, 0, e
		}
		deleted, e := service.store.DeleteRecord(ctx, tx, mutation.UserID, id, expected)
		if e == nil {
			e = service.store.ReplaceRecordTags(ctx, tx, mutation.UserID, id, nil)
		}
		return before, deleted, deleted.Version, e
	})
}

func (service *ContentService) CreateNote(ctx context.Context, mutation MutationContext, input NoteInput) (model.Note, error) {
	identifier := service.newUUID()
	if input.ID != nil {
		identifier = *input.ID
	}
	value := noteFromInput(identifier, input)
	if err := validateNote(value); err != nil {
		return model.Note{}, err
	}
	names, err := normalizeTagNames(input.Tags)
	if err != nil {
		return model.Note{}, err
	}
	linkedIDs, err := normalizeLinkedEntityIDs(identifier, input.LinkedEntityIDs)
	if err != nil {
		return model.Note{}, err
	}
	payload, _ := json.Marshal(input)
	response, err := executeResourceCommand(ctx, service.commands, mutation, "note.create", payload, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		created, e := service.store.CreateNote(ctx, tx, mutation.UserID, value)
		if e != nil {
			return CommandResult{}, e
		}
		tags, tagChanges, e := service.ensureTags(ctx, tx, mutation.UserID, names)
		if e != nil {
			return CommandResult{}, e
		}
		if e = service.store.ReplaceNoteTags(ctx, tx, mutation.UserID, created.ID, tags); e != nil {
			return CommandResult{}, e
		}
		created.Tags = tags
		created.LinkedEntityIDs, e = service.replaceNoteLinks(ctx, tx, mutation.UserID, created.ID, linkedIDs)
		if e != nil {
			return CommandResult{}, e
		}
		return CommandResult{Status: 201, Body: resourceJSON(created), Changes: append([]model.SyncChangeDraft{{EntityType: "note", EntityID: created.ID, Operation: "create", EntityVersion: created.Version}}, tagChanges...), Audits: []model.AuditDraft{{Action: "note.create", AfterData: resourceJSON(created), Entities: []model.AuditEntity{{EntityType: "note", EntityID: created.ID}}}}}, nil
	})
	return decodeNote(response, err)
}
func (service *ContentService) GetNote(ctx context.Context, userID, id uuid.UUID) (model.Note, error) {
	var value model.Note
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var e error
		value, e = service.store.GetNote(ctx, tx, userID, id)
		if e != nil {
			return e
		}
		value.Tags, e = service.store.ListNoteTags(ctx, tx, userID, id)
		if e != nil {
			return e
		}
		value.LinkedEntityIDs, e = service.listNoteLinkedEntityIDs(ctx, tx, userID, id)
		return e
	})
	return value, err
}
func (service *ContentService) ListNotes(ctx context.Context, userID uuid.UUID, query, cursor string, limit int) (NotePage, error) {
	if limit < 1 || limit > maxResourcePageSize {
		return NotePage{}, fmt.Errorf("%w: invalid page size", ErrValidation)
	}
	if query != "" && cursor != "" {
		return NotePage{}, fmt.Errorf("%w: search does not accept a list cursor", ErrValidation)
	}
	after, err := service.decodePosition(userID, "notes", cursor)
	if err != nil {
		return NotePage{}, err
	}
	var values []model.Note
	err = service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var e error
		if query != "" {
			values, e = service.store.SearchNotes(ctx, tx, userID, query, limit)
		} else {
			values, e = service.store.ListNotes(ctx, tx, userID, after, limit+1)
		}
		if e != nil {
			return e
		}
		for index := range values {
			values[index].Tags, e = service.store.ListNoteTags(ctx, tx, userID, values[index].ID)
			if e != nil {
				return e
			}
			values[index].LinkedEntityIDs, e = service.listNoteLinkedEntityIDs(ctx, tx, userID, values[index].ID)
			if e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return NotePage{}, err
	}
	hasMore := query == "" && len(values) > limit
	if hasMore {
		values = values[:limit]
	}
	next := ""
	if hasMore {
		last := values[len(values)-1]
		next, err = service.cursors.Encode(userID, "notes", model.ResourcePosition{UpdatedAt: last.UpdatedAt, ID: last.ID})
	}
	return NotePage{Notes: values, NextCursor: next, HasMore: hasMore}, err
}
func (service *ContentService) UpdateNote(ctx context.Context, mutation MutationContext, id uuid.UUID, expected int64, input NoteInput) (model.Note, error) {
	value := noteFromInput(id, input)
	if expected < 1 {
		return model.Note{}, fmt.Errorf("%w: expected version is required", ErrValidation)
	}
	if err := validateNote(value); err != nil {
		return model.Note{}, err
	}
	names, err := normalizeTagNames(input.Tags)
	if err != nil {
		return model.Note{}, err
	}
	linkedIDs, err := normalizeLinkedEntityIDs(id, input.LinkedEntityIDs)
	if err != nil {
		return model.Note{}, err
	}
	payload, _ := json.Marshal(map[string]any{"id": id, "expectedVersion": expected, "input": input})
	response, err := executeResourceCommand(ctx, service.commands, mutation, "note.update", payload, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		before, e := service.store.GetNote(ctx, tx, mutation.UserID, id)
		if e != nil {
			return CommandResult{}, e
		}
		before.Tags, e = service.store.ListNoteTags(ctx, tx, mutation.UserID, id)
		if e != nil {
			return CommandResult{}, e
		}
		before.LinkedEntityIDs, e = service.listNoteLinkedEntityIDs(ctx, tx, mutation.UserID, id)
		if e != nil {
			return CommandResult{}, e
		}
		updated, e := service.store.UpdateNote(ctx, tx, mutation.UserID, value, expected)
		if e != nil {
			return CommandResult{}, e
		}
		tags, tagChanges, e := service.ensureTags(ctx, tx, mutation.UserID, names)
		if e != nil {
			return CommandResult{}, e
		}
		if e = service.store.ReplaceNoteTags(ctx, tx, mutation.UserID, id, tags); e != nil {
			return CommandResult{}, e
		}
		removed, e := service.store.CleanupTags(ctx, tx, mutation.UserID)
		if e != nil {
			return CommandResult{}, e
		}
		for _, tag := range removed {
			tagChanges = append(tagChanges, model.SyncChangeDraft{EntityType: "tag", EntityID: tag.ID, Operation: "delete", EntityVersion: tag.Version})
		}
		updated.Tags = tags
		updated.LinkedEntityIDs, e = service.replaceNoteLinks(ctx, tx, mutation.UserID, id, linkedIDs)
		if e != nil {
			return CommandResult{}, e
		}
		return CommandResult{Status: 200, Body: resourceJSON(updated), Changes: append([]model.SyncChangeDraft{{EntityType: "note", EntityID: id, Operation: "update", EntityVersion: updated.Version}}, tagChanges...), Audits: []model.AuditDraft{{Action: "note.update", BeforeData: resourceJSON(before), AfterData: resourceJSON(updated), Entities: []model.AuditEntity{{EntityType: "note", EntityID: id}}}}}, nil
	})
	return decodeNote(response, err)
}
func (service *ContentService) DeleteNote(ctx context.Context, mutation MutationContext, id uuid.UUID, expected int64) error {
	return service.deleteContent(ctx, mutation, "note", id, expected, func(ctx context.Context, tx database.Tx) (any, any, int64, error) {
		before, e := service.store.GetNote(ctx, tx, mutation.UserID, id)
		if e != nil {
			return nil, nil, 0, e
		}
		before.Tags, e = service.store.ListNoteTags(ctx, tx, mutation.UserID, id)
		if e != nil {
			return nil, nil, 0, e
		}
		before.LinkedEntityIDs, e = service.listNoteLinkedEntityIDs(ctx, tx, mutation.UserID, id)
		if e != nil {
			return nil, nil, 0, e
		}
		deleted, e := service.store.DeleteNote(ctx, tx, mutation.UserID, id, expected)
		if e == nil {
			e = service.store.ReplaceNoteTags(ctx, tx, mutation.UserID, id, nil)
		}
		if e == nil {
			e = service.store.ReplaceNoteLinks(ctx, tx, mutation.UserID, id, nil)
		}
		return before, deleted, deleted.Version, e
	})
}

func (service *ContentService) CreateReview(ctx context.Context, mutation MutationContext, input ReviewInput) (model.DailyReview, error) {
	identifier := service.newUUID()
	if input.ID != nil {
		identifier = *input.ID
	}
	value := reviewFromInput(identifier, input)
	if err := validateReview(value); err != nil {
		return model.DailyReview{}, err
	}
	payload, _ := json.Marshal(input)
	response, err := executeResourceCommand(ctx, service.commands, mutation, "daily_review.create", payload, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		created, e := service.store.CreateReview(ctx, tx, mutation.UserID, value)
		if e != nil {
			return CommandResult{}, e
		}
		return CommandResult{Status: 201, Body: resourceJSON(created), Changes: []model.SyncChangeDraft{{EntityType: "daily_review", EntityID: created.ID, Operation: "create", EntityVersion: created.Version}}, Audits: []model.AuditDraft{{Action: "daily_review.create", AfterData: resourceJSON(created), Entities: []model.AuditEntity{{EntityType: "daily_review", EntityID: created.ID}}}}}, nil
	})
	return decodeReview(response, err)
}

func (service *ContentService) GetReview(ctx context.Context, userID, id uuid.UUID) (model.DailyReview, error) {
	var value model.DailyReview
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var readErr error
		value, readErr = service.store.GetReview(ctx, tx, userID, id)
		return readErr
	})
	return value, err
}

func (service *ContentService) ListReviews(ctx context.Context, userID uuid.UUID, cursor string, limit int) (ReviewPage, error) {
	if limit < 1 || limit > maxResourcePageSize {
		return ReviewPage{}, fmt.Errorf("%w: invalid page size", ErrValidation)
	}
	after, err := service.decodePosition(userID, "daily-reviews", cursor)
	if err != nil {
		return ReviewPage{}, err
	}
	var values []model.DailyReview
	err = service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var e error
		values, e = service.store.ListReviews(ctx, tx, userID, after, limit+1)
		return e
	})
	if err != nil {
		return ReviewPage{}, err
	}
	hasMore := len(values) > limit
	if hasMore {
		values = values[:limit]
	}
	next := ""
	if hasMore {
		last := values[len(values)-1]
		position, parseErr := time.Parse(time.DateOnly, last.ReviewDate)
		if parseErr != nil {
			return ReviewPage{}, fmt.Errorf("decode daily review position: %w", parseErr)
		}
		next, err = service.cursors.Encode(userID, "daily-reviews", model.ResourcePosition{UpdatedAt: position, ID: last.ID})
	}
	return ReviewPage{Reviews: values, NextCursor: next, HasMore: hasMore}, err
}
func (service *ContentService) UpdateReview(ctx context.Context, mutation MutationContext, id uuid.UUID, expected int64, input ReviewInput) (model.DailyReview, error) {
	value := reviewFromInput(id, input)
	if expected < 1 {
		return model.DailyReview{}, fmt.Errorf("%w: expected version is required", ErrValidation)
	}
	if err := validateReview(value); err != nil {
		return model.DailyReview{}, err
	}
	payload, _ := json.Marshal(map[string]any{"id": id, "expectedVersion": expected, "input": input})
	response, err := executeResourceCommand(ctx, service.commands, mutation, "daily_review.update", payload, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		before, e := service.store.GetReview(ctx, tx, mutation.UserID, id)
		if e != nil {
			return CommandResult{}, e
		}
		updated, e := service.store.UpdateReview(ctx, tx, mutation.UserID, value, expected)
		if e != nil {
			return CommandResult{}, e
		}
		return CommandResult{Status: 200, Body: resourceJSON(updated), Changes: []model.SyncChangeDraft{{EntityType: "daily_review", EntityID: id, Operation: "update", EntityVersion: updated.Version}}, Audits: []model.AuditDraft{{Action: "daily_review.update", BeforeData: resourceJSON(before), AfterData: resourceJSON(updated), Entities: []model.AuditEntity{{EntityType: "daily_review", EntityID: id}}}}}, nil
	})
	return decodeReview(response, err)
}
func (service *ContentService) DeleteReview(ctx context.Context, mutation MutationContext, id uuid.UUID, expected int64) error {
	return service.deleteContent(ctx, mutation, "daily_review", id, expected, func(ctx context.Context, tx database.Tx) (any, any, int64, error) {
		before, e := service.store.GetReview(ctx, tx, mutation.UserID, id)
		if e != nil {
			return nil, nil, 0, e
		}
		deleted, e := service.store.DeleteReview(ctx, tx, mutation.UserID, id, expected)
		return before, deleted, deleted.Version, e
	})
}
func (service *ContentService) ListTags(ctx context.Context, userID uuid.UUID) ([]model.Tag, error) {
	var tags []model.Tag
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var e error
		tags, e = service.store.ListTags(ctx, tx, userID, 500)
		return e
	})
	return tags, err
}

func (service *ContentService) deleteContent(ctx context.Context, mutation MutationContext, entityType string, id uuid.UUID, expected int64, remove func(context.Context, database.Tx) (any, any, int64, error)) error {
	if id == uuid.Nil || expected < 1 {
		return fmt.Errorf("%w: resource ID and version are required", ErrValidation)
	}
	payload, _ := json.Marshal(map[string]any{"id": id, "expectedVersion": expected})
	_, err := executeResourceCommand(ctx, service.commands, mutation, entityType+".delete", payload, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		before, deleted, version, e := remove(ctx, tx)
		if e != nil {
			return CommandResult{}, e
		}
		removedTags := []model.Tag{}
		if entityType == "record" || entityType == "note" {
			removedTags, e = service.store.CleanupTags(ctx, tx, mutation.UserID)
			if e != nil {
				return CommandResult{}, e
			}
		}
		changes := []model.SyncChangeDraft{{EntityType: entityType, EntityID: id, Operation: "delete", EntityVersion: version}}
		for _, tag := range removedTags {
			changes = append(changes, model.SyncChangeDraft{EntityType: "tag", EntityID: tag.ID, Operation: "delete", EntityVersion: tag.Version})
		}
		return CommandResult{Status: 200, Body: resourceJSON(map[string]any{"id": id, "version": version}), Changes: changes, Audits: []model.AuditDraft{{Action: entityType + ".delete", BeforeData: resourceJSON(before), AfterData: resourceJSON(deleted), Entities: []model.AuditEntity{{EntityType: entityType, EntityID: id}}}}}, nil
	})
	return err
}
func (service *ContentService) ensureTags(ctx context.Context, tx database.Tx, userID uuid.UUID, names []string) ([]model.Tag, []model.SyncChangeDraft, error) {
	tags := make([]model.Tag, 0, len(names))
	changes := []model.SyncChangeDraft{}
	for _, name := range names {
		tag, created, err := service.store.EnsureTag(ctx, tx, userID, service.newUUID(), name, strings.ToLower(name))
		if err != nil {
			return nil, nil, err
		}
		tags = append(tags, tag)
		if created {
			changes = append(changes, model.SyncChangeDraft{EntityType: "tag", EntityID: tag.ID, Operation: "create", EntityVersion: tag.Version})
		}
	}
	return tags, changes, nil
}
func (service *ContentService) decodePosition(userID uuid.UUID, scope, cursor string) (*model.ResourcePosition, error) {
	if cursor == "" {
		return nil, nil
	}
	decoded, err := service.cursors.Decode(userID, scope, cursor)
	if err != nil {
		return nil, err
	}
	return &decoded, nil
}
func recordFromInput(id uuid.UUID, input RecordInput) model.Record {
	return model.Record{ID: id, RawText: strings.TrimSpace(input.RawText), Kind: input.Kind, OccurredAt: input.OccurredAt.UTC(), Mood: input.Mood, Energy: input.Energy, ArchivedAt: utcTime(input.ArchivedAt)}
}
func noteFromInput(id uuid.UUID, input NoteInput) model.Note {
	category := strings.TrimSpace(input.Category)
	if category == "" {
		category = "其他"
	}
	return model.Note{ID: id, Title: strings.TrimSpace(input.Title), BodyMarkdown: input.BodyMarkdown, Category: category, ArchivedAt: utcTime(input.ArchivedAt)}
}
func reviewFromInput(id uuid.UUID, input ReviewInput) model.DailyReview {
	return model.DailyReview{ID: id, ReviewDate: input.ReviewDate, Wins: input.Wins, Blockers: input.Blockers, Mood: input.Mood, Energy: input.Energy, TomorrowFocus: input.TomorrowFocus, AISummary: trimmedOptional(input.AISummary)}
}
func validateRecord(value model.Record) error {
	if value.ID == uuid.Nil || strings.TrimSpace(value.RawText) == "" || len(value.RawText) > 100000 || !map[string]bool{"status": true, "idea": true, "completion": true, "inbox": true}[value.Kind] || value.OccurredAt.IsZero() || !scoreValid(value.Mood) || !scoreValid(value.Energy) {
		return fmt.Errorf("%w: invalid record", ErrValidation)
	}
	return nil
}
func validateNote(value model.Note) error {
	if value.ID == uuid.Nil || utf8.RuneCountInString(value.Title) < 1 || utf8.RuneCountInString(value.Title) > 240 || utf8.RuneCountInString(value.Category) < 1 || utf8.RuneCountInString(value.Category) > 80 || len(value.BodyMarkdown) > 1000000 {
		return fmt.Errorf("%w: invalid note", ErrValidation)
	}
	return nil
}
func validateReview(value model.DailyReview) error {
	if value.ID == uuid.Nil {
		return fmt.Errorf("%w: review ID is required", ErrValidation)
	}
	if _, err := time.Parse(time.DateOnly, value.ReviewDate); err != nil || !scoreValid(value.Mood) || !scoreValid(value.Energy) || len(value.Wins)+len(value.Blockers)+len(value.TomorrowFocus) > 100000 {
		return fmt.Errorf("%w: invalid daily review", ErrValidation)
	}
	return nil
}
func scoreValid(value *int) bool { return value == nil || (*value >= 1 && *value <= 5) }
func normalizeTagNames(values []string) ([]string, error) {
	if len(values) > 50 {
		return nil, fmt.Errorf("%w: too many tags", ErrValidation)
	}
	seen := map[string]bool{}
	normalized := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 80 {
			return nil, fmt.Errorf("%w: invalid tag", ErrValidation)
		}
		if !seen[key] {
			seen[key] = true
			normalized = append(normalized, value)
		}
	}
	return normalized, nil
}
func normalizeLinkedEntityIDs(sourceID uuid.UUID, values []uuid.UUID) ([]uuid.UUID, error) {
	if len(values) > 50 {
		return nil, fmt.Errorf("%w: too many linked entities", ErrValidation)
	}
	seen := map[uuid.UUID]bool{}
	normalized := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if value == uuid.Nil || value == sourceID {
			return nil, fmt.Errorf("%w: invalid linked entity", ErrValidation)
		}
		if !seen[value] {
			seen[value] = true
			normalized = append(normalized, value)
		}
	}
	return normalized, nil
}
func (service *ContentService) listNoteLinkedEntityIDs(ctx context.Context, tx database.Tx, userID, noteID uuid.UUID) ([]uuid.UUID, error) {
	links, err := service.store.ListNoteLinks(ctx, tx, userID, noteID)
	if err != nil {
		return nil, err
	}
	values := make([]uuid.UUID, 0, len(links))
	for _, link := range links {
		values = append(values, link.TargetID)
	}
	return values, nil
}
func (service *ContentService) replaceNoteLinks(ctx context.Context, tx database.Tx, userID, noteID uuid.UUID, targetIDs []uuid.UUID) ([]uuid.UUID, error) {
	links := make([]model.EntityLink, 0, len(targetIDs))
	for _, targetID := range targetIDs {
		targetType, err := service.store.ResolveEntityType(ctx, tx, userID, targetID)
		if errors.Is(err, model.ErrNotFound) {
			return nil, fmt.Errorf("%w: linked entity does not exist", ErrValidation)
		}
		if err != nil {
			return nil, err
		}
		links = append(links, model.EntityLink{ID: service.newUUID(), SourceType: "note", SourceID: noteID, TargetType: targetType, TargetID: targetID, RelationType: "references"})
	}
	if err := service.store.ReplaceNoteLinks(ctx, tx, userID, noteID, links); err != nil {
		return nil, err
	}
	return targetIDs, nil
}
func decodeRecord(response CommandResponse, err error) (model.Record, error) {
	if err != nil {
		return model.Record{}, err
	}
	var value model.Record
	err = json.Unmarshal(response.Body, &value)
	return value, err
}
func decodeNote(response CommandResponse, err error) (model.Note, error) {
	if err != nil {
		return model.Note{}, err
	}
	var value model.Note
	err = json.Unmarshal(response.Body, &value)
	return value, err
}
func decodeReview(response CommandResponse, err error) (model.DailyReview, error) {
	if err != nil {
		return model.DailyReview{}, err
	}
	var value model.DailyReview
	err = json.Unmarshal(response.Body, &value)
	return value, err
}
