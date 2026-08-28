package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

const (
	maxAuditSnapshotBytes = 32 * 1024
	maxAuditMetadataBytes = 8 * 1024
)

var auditEntityTypes = map[string]struct{}{
	"goal": {}, "milestone": {}, "task": {}, "calendar_event": {},
	"record": {}, "note": {}, "daily_review": {}, "agent_run": {},
}

type AuditStore interface {
	Append(context.Context, database.Tx, uuid.UUID, model.AuditDraft) error
}

type AuditService struct {
	store   AuditStore
	newUUID func() uuid.UUID
}

func NewAuditService(store AuditStore) (*AuditService, error) {
	if store == nil {
		return nil, errors.New("audit store is required")
	}
	return &AuditService{store: store, newUUID: uuid.New}, nil
}

func (service *AuditService) Record(
	ctx context.Context,
	tx database.Tx,
	userID uuid.UUID,
	audits []model.AuditDraft,
) error {
	if service == nil || service.store == nil {
		return errors.New("audit service is required")
	}
	if tx == nil || userID == uuid.Nil {
		return fmt.Errorf("%w: audit transaction and user are required", ErrValidation)
	}
	prepared := make([]model.AuditDraft, len(audits))
	for index, draft := range audits {
		validated, err := service.prepare(userID, draft)
		if err != nil {
			return fmt.Errorf("prepare audit %d: %w", index, err)
		}
		prepared[index] = validated
	}
	for _, draft := range prepared {
		if err := service.store.Append(ctx, tx, userID, draft); err != nil {
			return fmt.Errorf("append audit event: %w", err)
		}
	}
	return nil
}

func (service *AuditService) prepare(userID uuid.UUID, draft model.AuditDraft) (model.AuditDraft, error) {
	draft.Action = strings.TrimSpace(draft.Action)
	if utf8.RuneCountInString(draft.Action) < 1 || utf8.RuneCountInString(draft.Action) > 120 {
		return model.AuditDraft{}, fmt.Errorf("%w: audit action must contain 1 to 120 characters", ErrValidation)
	}
	if draft.ID == uuid.Nil {
		draft.ID = service.newUUID()
	}
	if draft.RequestID == uuid.Nil {
		draft.RequestID = service.newUUID()
	}
	if draft.ActorType == "" {
		draft.ActorType = "user"
	}
	if draft.ActorType != "user" && draft.ActorType != "agent" && draft.ActorType != "system" {
		return model.AuditDraft{}, fmt.Errorf("%w: invalid audit actor type", ErrValidation)
	}
	if draft.ActorType == "user" {
		if draft.ActorID != nil && *draft.ActorID != userID {
			return model.AuditDraft{}, fmt.Errorf("%w: audit user actor must match transaction user", ErrValidation)
		}
		if draft.ActorID == nil {
			actorID := userID
			draft.ActorID = &actorID
		}
	}
	entities, containsNote, err := prepareAuditEntities(draft.Entities)
	if err != nil {
		return model.AuditDraft{}, err
	}
	draft.Entities = entities
	if draft.BeforeData, err = sanitizeAuditObject(draft.BeforeData, containsNote, maxAuditSnapshotBytes, true); err != nil {
		return model.AuditDraft{}, fmt.Errorf("%w: invalid audit before snapshot", ErrValidation)
	}
	if draft.AfterData, err = sanitizeAuditObject(draft.AfterData, containsNote, maxAuditSnapshotBytes, true); err != nil {
		return model.AuditDraft{}, fmt.Errorf("%w: invalid audit after snapshot", ErrValidation)
	}
	if len(draft.Metadata) == 0 {
		draft.Metadata = json.RawMessage(`{}`)
	}
	if draft.Metadata, err = sanitizeAuditObject(draft.Metadata, false, maxAuditMetadataBytes, false); err != nil {
		return model.AuditDraft{}, fmt.Errorf("%w: invalid audit metadata", ErrValidation)
	}
	return draft, nil
}

func prepareAuditEntities(entities []model.AuditEntity) ([]model.AuditEntity, bool, error) {
	prepared := make([]model.AuditEntity, 0, len(entities))
	seen := make(map[string]struct{}, len(entities))
	containsNote := false
	for _, entity := range entities {
		if _, ok := auditEntityTypes[entity.EntityType]; !ok || entity.EntityID == uuid.Nil {
			return nil, false, fmt.Errorf("%w: invalid audit entity", ErrValidation)
		}
		key := entity.EntityType + ":" + entity.EntityID.String()
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		prepared = append(prepared, entity)
		containsNote = containsNote || entity.EntityType == "note"
	}
	return prepared, containsNote, nil
}

func sanitizeAuditObject(raw json.RawMessage, containsNote bool, limit int, nullable bool) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		if nullable {
			return nil, nil
		}
		return json.RawMessage(`{}`), nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, errors.New("audit value must be a JSON object")
	}
	filtered := filterAuditMap(object, containsNote)
	encoded, err := json.Marshal(filtered)
	if err != nil {
		return nil, fmt.Errorf("encode filtered audit value: %w", err)
	}
	if len(encoded) > limit {
		encoded, err = json.Marshal(map[string]any{"_truncated": true, "_originalBytes": len(encoded)})
		if err != nil {
			return nil, fmt.Errorf("encode bounded audit value: %w", err)
		}
	}
	return encoded, nil
}

func filterAuditMap(object map[string]any, containsNote bool) map[string]any {
	filtered := make(map[string]any, len(object))
	for key, value := range object {
		normalized := normalizeAuditKey(key)
		if isSensitiveAuditKey(normalized) || (containsNote && isNoteBodyKey(normalized)) {
			continue
		}
		filtered[key] = filterAuditValue(value, containsNote)
	}
	return filtered
}

func filterAuditValue(value any, containsNote bool) any {
	switch typed := value.(type) {
	case map[string]any:
		return filterAuditMap(typed, containsNote)
	case []any:
		filtered := make([]any, len(typed))
		for index, item := range typed {
			filtered[index] = filterAuditValue(item, containsNote)
		}
		return filtered
	default:
		return value
	}
}

func normalizeAuditKey(key string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			return unicode.ToLower(character)
		}
		return -1
	}, key)
}

func isSensitiveAuditKey(key string) bool {
	for _, fragment := range []string{
		"password", "passphrase", "token", "secret", "apikey",
		"authorization", "cookie", "session",
	} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}

func isNoteBodyKey(key string) bool {
	return key == "body" || key == "bodymarkdown" || key == "markdown" || key == "content"
}
