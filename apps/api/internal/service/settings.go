package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type SettingsStore interface {
	Get(context.Context, database.Tx, uuid.UUID) (model.UserSettings, error)
	Update(context.Context, database.Tx, uuid.UUID, int, []byte, int64) (model.UserSettings, error)
}
type SettingsService struct {
	store      SettingsStore
	transactor UserTransactor
	commands   *CommandService
}

func NewSettingsService(store SettingsStore, transactor UserTransactor, commands *CommandService) (*SettingsService, error) {
	if store == nil || transactor == nil || commands == nil {
		return nil, errors.New("settings dependencies are required")
	}
	return &SettingsService{store: store, transactor: transactor, commands: commands}, nil
}
func (service *SettingsService) Get(ctx context.Context, userID uuid.UUID) (model.UserSettings, error) {
	var value model.UserSettings
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var e error
		value, e = service.store.Get(ctx, tx, userID)
		return e
	})
	return value, err
}
func (service *SettingsService) Patch(ctx context.Context, mutation MutationContext, expected int64, patch json.RawMessage) (model.UserSettings, error) {
	if expected < 1 {
		return model.UserSettings{}, fmt.Errorf("%w: expected settings version is required", ErrValidation)
	}
	var patchObject map[string]any
	if err := json.Unmarshal(patch, &patchObject); err != nil || patchObject == nil {
		return model.UserSettings{}, fmt.Errorf("%w: settings patch must be an object", ErrValidation)
	}
	if err := validateSettingsObject(patchObject, true, 0); err != nil {
		return model.UserSettings{}, err
	}
	payload, _ := json.Marshal(map[string]any{"expectedVersion": expected, "patch": patchObject})
	response, err := service.commands.Execute(ctx, resourceCommand(mutation, "settings.update", payload), func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		before, e := service.store.Get(ctx, tx, mutation.UserID)
		if e != nil {
			return CommandResult{}, e
		}
		var current map[string]any
		if e = json.Unmarshal(before.Settings, &current); e != nil {
			return CommandResult{}, fmt.Errorf("decode current settings: %w", e)
		}
		if current == nil {
			current = map[string]any{}
		}
		merged := mergeJSONObject(current, patchObject)
		if e = validateSettingsObject(merged, false, 0); e != nil {
			return CommandResult{}, e
		}
		encoded, _ := json.Marshal(merged)
		updated, e := service.store.Update(ctx, tx, mutation.UserID, 1, encoded, expected)
		if e != nil {
			return CommandResult{}, e
		}
		return CommandResult{Status: 200, Body: resourceJSON(updated), Changes: []model.SyncChangeDraft{{EntityType: "settings", EntityID: mutation.UserID, Operation: "update", EntityVersion: updated.Version}}, Audits: []model.AuditDraft{{Action: "settings.update", BeforeData: before.Settings, AfterData: updated.Settings}}}, nil
	})
	if err != nil {
		return model.UserSettings{}, err
	}
	var value model.UserSettings
	if err = json.Unmarshal(response.Body, &value); err != nil {
		return model.UserSettings{}, err
	}
	return value, nil
}

func mergeJSONObject(current, patch map[string]any) map[string]any {
	merged := make(map[string]any, len(current))
	for key, value := range current {
		merged[key] = value
	}
	for key, value := range patch {
		if value == nil {
			delete(merged, key)
			continue
		}
		patchMap, patchOK := value.(map[string]any)
		currentMap, currentOK := merged[key].(map[string]any)
		if patchOK && currentOK {
			merged[key] = mergeJSONObject(currentMap, patchMap)
		} else {
			merged[key] = value
		}
	}
	return merged
}
func validateSettingsObject(value map[string]any, patch bool, depth int) error {
	if depth > 3 {
		return fmt.Errorf("%w: settings nesting is too deep", ErrValidation)
	}
	allowed := map[string]bool{"energy": true, "aiEnabled": true, "remindersEnabled": true, "onboardingCompleted": true, "focusAreas": true, "dataMode": true, "localOnly": true, "permissions": true}
	for key, raw := range value {
		if !allowed[key] {
			return fmt.Errorf("%w: unknown settings key %q", ErrValidation, key)
		}
		if patch && raw == nil {
			continue
		}
		switch key {
		case "energy":
			number, ok := raw.(float64)
			if !ok || number < 1 || number > 5 || number != float64(int(number)) {
				return fmt.Errorf("%w: energy must be an integer from 1 to 5", ErrValidation)
			}
		case "aiEnabled", "remindersEnabled", "onboardingCompleted", "localOnly":
			if _, ok := raw.(bool); !ok {
				return fmt.Errorf("%w: %s must be boolean", ErrValidation, key)
			}
		case "dataMode":
			text, ok := raw.(string)
			if !ok || (text != "local" && text != "selected") {
				return fmt.Errorf("%w: invalid data mode", ErrValidation)
			}
		case "focusAreas":
			items, ok := raw.([]any)
			if !ok || len(items) > 20 {
				return fmt.Errorf("%w: invalid focus areas", ErrValidation)
			}
			for _, item := range items {
				text, ok := item.(string)
				if !ok || utf8.RuneCountInString(text) < 1 || utf8.RuneCountInString(text) > 40 {
					return fmt.Errorf("%w: invalid focus area", ErrValidation)
				}
			}
		case "permissions":
			object, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("%w: permissions must be an object", ErrValidation)
			}
			for permission, setting := range object {
				if !map[string]bool{"goals": true, "calendar": true, "records": true, "privateNotes": true}[permission] {
					return fmt.Errorf("%w: unknown permission", ErrValidation)
				}
				if setting != nil || !patch {
					if _, ok := setting.(bool); !ok {
						return fmt.Errorf("%w: permission must be boolean", ErrValidation)
					}
				}
			}
			if err := validateDepth(object, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
func validateDepth(value any, depth int) error {
	if depth > 3 {
		return fmt.Errorf("%w: settings nesting is too deep", ErrValidation)
	}
	switch typed := value.(type) {
	case map[string]any:
		for _, nested := range typed {
			if err := validateDepth(nested, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, nested := range typed {
			if err := validateDepth(nested, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
