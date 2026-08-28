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

type CalendarStore interface {
	CreateEvent(context.Context, database.Tx, uuid.UUID, model.CalendarEvent) (model.CalendarEvent, error)
	GetEvent(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.CalendarEvent, error)
	ListEvents(context.Context, database.Tx, uuid.UUID, *time.Time, *time.Time, int) ([]model.CalendarEvent, error)
	UpdateEvent(context.Context, database.Tx, uuid.UUID, model.CalendarEvent, int64) (model.CalendarEvent, []model.CalendarReminder, error)
	DeleteEvent(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64) (model.CalendarEvent, []model.CalendarReminder, error)
	CreateReminder(context.Context, database.Tx, uuid.UUID, model.CalendarReminder) (model.CalendarReminder, error)
	ListReminders(context.Context, database.Tx, uuid.UUID, uuid.UUID) ([]model.CalendarReminder, error)
}

type CalendarService struct {
	store      CalendarStore
	transactor UserTransactor
	commands   *CommandService
	newUUID    func() uuid.UUID
}

type ReminderInput struct {
	OffsetMinutes int    `json:"offsetMinutes"`
	Channel       string `json:"channel"`
}
type CalendarEventInput struct {
	Title          string          `json:"title"`
	StartAt        time.Time       `json:"startAt"`
	EndAt          time.Time       `json:"endAt"`
	Timezone       string          `json:"timezone"`
	Location       *string         `json:"location"`
	Kind           string          `json:"kind"`
	SourceCalendar *string         `json:"sourceCalendar"`
	GoalID         *uuid.UUID      `json:"goalId"`
	Reminders      []ReminderInput `json:"reminders,omitempty"`
}
type CalendarEventResult struct {
	Event     model.CalendarEvent      `json:"event"`
	Reminders []model.CalendarReminder `json:"reminders"`
}

func NewCalendarService(store CalendarStore, transactor UserTransactor, commands *CommandService) (*CalendarService, error) {
	if store == nil || transactor == nil || commands == nil {
		return nil, errors.New("calendar store, transactor, and commands are required")
	}
	return &CalendarService{store: store, transactor: transactor, commands: commands, newUUID: uuid.New}, nil
}

func (service *CalendarService) Create(ctx context.Context, mutation MutationContext, input CalendarEventInput) (CalendarEventResult, error) {
	event := eventFromInput(service.newUUID(), input)
	if err := validateCalendarEvent(event); err != nil {
		return CalendarEventResult{}, err
	}
	if err := validateReminderInputs(input.Reminders); err != nil {
		return CalendarEventResult{}, err
	}
	payload, _ := json.Marshal(input)
	response, err := service.commands.Execute(ctx, resourceCommand(mutation, "calendar_event.create", payload), func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		created, createErr := service.store.CreateEvent(ctx, tx, mutation.UserID, event)
		if createErr != nil {
			return CommandResult{}, createErr
		}
		result := CalendarEventResult{Event: created, Reminders: make([]model.CalendarReminder, 0, len(input.Reminders))}
		changes := []model.SyncChangeDraft{{EntityType: "calendar_event", EntityID: created.ID, Operation: "create", EntityVersion: created.Version}}
		entities := []model.AuditEntity{{EntityType: "calendar_event", EntityID: created.ID}}
		outbox := make([]model.OutboxDraft, 0, len(input.Reminders))
		for _, reminderInput := range input.Reminders {
			reminder := model.CalendarReminder{ID: service.newUUID(), EventID: created.ID, OffsetMinutes: reminderInput.OffsetMinutes, Channel: reminderInput.Channel,
				ScheduledAt: created.StartAt.Add(-time.Duration(reminderInput.OffsetMinutes) * time.Minute)}
			stored, reminderErr := service.store.CreateReminder(ctx, tx, mutation.UserID, reminder)
			if reminderErr != nil {
				return CommandResult{}, reminderErr
			}
			result.Reminders = append(result.Reminders, stored)
			changes = append(changes, model.SyncChangeDraft{EntityType: "reminder", EntityID: stored.ID, Operation: "create", EntityVersion: stored.Version})
			outbox = append(outbox, reminderOutbox(stored))
		}
		return CommandResult{Status: 201, Body: resourceJSON(result), Changes: changes, Outbox: outbox,
			Audits: []model.AuditDraft{{Action: "calendar_event.create", AfterData: resourceJSON(created), Entities: entities}}}, nil
	})
	return decodeCalendarResult(response, err)
}

func (service *CalendarService) Get(ctx context.Context, userID, eventID uuid.UUID) (CalendarEventResult, error) {
	var result CalendarEventResult
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var readErr error
		result.Event, readErr = service.store.GetEvent(ctx, tx, userID, eventID)
		if readErr != nil {
			return readErr
		}
		result.Reminders, readErr = service.store.ListReminders(ctx, tx, userID, eventID)
		return readErr
	})
	return result, err
}

func (service *CalendarService) List(ctx context.Context, userID uuid.UUID, start, end *time.Time, limit int) ([]model.CalendarEvent, error) {
	if limit < 1 || limit > 500 || (start != nil && end != nil && end.Before(*start)) {
		return nil, fmt.Errorf("%w: invalid calendar window or limit", ErrValidation)
	}
	var events []model.CalendarEvent
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var readErr error
		events, readErr = service.store.ListEvents(ctx, tx, userID, utcTime(start), utcTime(end), limit)
		return readErr
	})
	return events, err
}

func (service *CalendarService) Update(ctx context.Context, mutation MutationContext, eventID uuid.UUID, expectedVersion int64, input CalendarEventInput) (CalendarEventResult, error) {
	if eventID == uuid.Nil || expectedVersion < 1 {
		return CalendarEventResult{}, fmt.Errorf("%w: event ID and expected version are required", ErrValidation)
	}
	event := eventFromInput(eventID, input)
	if err := validateCalendarEvent(event); err != nil {
		return CalendarEventResult{}, err
	}
	payload, _ := json.Marshal(struct {
		ID       uuid.UUID          `json:"id"`
		Expected int64              `json:"expectedVersion"`
		Input    CalendarEventInput `json:"input"`
	}{eventID, expectedVersion, input})
	response, err := service.commands.Execute(ctx, resourceCommand(mutation, "calendar_event.update", payload), func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		before, readErr := service.store.GetEvent(ctx, tx, mutation.UserID, eventID)
		if readErr != nil {
			return CommandResult{}, readErr
		}
		updated, reminders, updateErr := service.store.UpdateEvent(ctx, tx, mutation.UserID, event, expectedVersion)
		if updateErr != nil {
			return CommandResult{}, updateErr
		}
		changes := []model.SyncChangeDraft{{EntityType: "calendar_event", EntityID: updated.ID, Operation: "update", EntityVersion: updated.Version}}
		outbox := make([]model.OutboxDraft, 0, len(reminders))
		for _, reminder := range reminders {
			changes = append(changes, model.SyncChangeDraft{EntityType: "reminder", EntityID: reminder.ID, Operation: "update", EntityVersion: reminder.Version})
			outbox = append(outbox, reminderOutbox(reminder))
		}
		return CommandResult{Status: 200, Body: resourceJSON(CalendarEventResult{Event: updated, Reminders: reminders}), Changes: changes, Outbox: outbox,
			Audits: []model.AuditDraft{{Action: "calendar_event.update", BeforeData: resourceJSON(before), AfterData: resourceJSON(updated), Entities: []model.AuditEntity{{EntityType: "calendar_event", EntityID: updated.ID}}}}}, nil
	})
	return decodeCalendarResult(response, err)
}

func (service *CalendarService) Delete(ctx context.Context, mutation MutationContext, eventID uuid.UUID, expectedVersion int64) error {
	if eventID == uuid.Nil || expectedVersion < 1 {
		return fmt.Errorf("%w: event ID and expected version are required", ErrValidation)
	}
	payload, _ := json.Marshal(map[string]any{"id": eventID, "expectedVersion": expectedVersion})
	_, err := service.commands.Execute(ctx, resourceCommand(mutation, "calendar_event.delete", payload), func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		before, readErr := service.store.GetEvent(ctx, tx, mutation.UserID, eventID)
		if readErr != nil {
			return CommandResult{}, readErr
		}
		deleted, reminders, deleteErr := service.store.DeleteEvent(ctx, tx, mutation.UserID, eventID, expectedVersion)
		if deleteErr != nil {
			return CommandResult{}, deleteErr
		}
		changes := []model.SyncChangeDraft{{EntityType: "calendar_event", EntityID: deleted.ID, Operation: "delete", EntityVersion: deleted.Version}}
		for _, reminder := range reminders {
			changes = append(changes, model.SyncChangeDraft{EntityType: "reminder", EntityID: reminder.ID, Operation: "delete", EntityVersion: reminder.Version})
		}
		return CommandResult{Status: 200, Body: resourceJSON(map[string]any{"id": deleted.ID, "version": deleted.Version}), Changes: changes,
			Audits: []model.AuditDraft{{Action: "calendar_event.delete", BeforeData: resourceJSON(before), AfterData: resourceJSON(deleted), Entities: []model.AuditEntity{{EntityType: "calendar_event", EntityID: deleted.ID}}}}}, nil
	})
	return err
}

func eventFromInput(id uuid.UUID, input CalendarEventInput) model.CalendarEvent {
	return model.CalendarEvent{ID: id, Title: strings.TrimSpace(input.Title), StartAt: input.StartAt.UTC(), EndAt: input.EndAt.UTC(), Timezone: strings.TrimSpace(input.Timezone),
		Location: trimmedOptional(input.Location), Kind: input.Kind, SourceCalendar: trimmedOptional(input.SourceCalendar), GoalID: input.GoalID}
}

func validateCalendarEvent(event model.CalendarEvent) error {
	if utf8.RuneCountInString(event.Title) < 1 || utf8.RuneCountInString(event.Title) > 240 || event.StartAt.IsZero() || event.EndAt.Before(event.StartAt) {
		return fmt.Errorf("%w: invalid calendar title or time range", ErrValidation)
	}
	if _, err := time.LoadLocation(event.Timezone); err != nil {
		return fmt.Errorf("%w: timezone must be a valid IANA name", ErrValidation)
	}
	if !map[string]bool{"fixed": true, "focus": true, "health": true, "personal": true}[event.Kind] {
		return fmt.Errorf("%w: invalid calendar event kind", ErrValidation)
	}
	if event.Location != nil && utf8.RuneCountInString(*event.Location) > 240 {
		return fmt.Errorf("%w: calendar location is too long", ErrValidation)
	}
	if event.SourceCalendar != nil && utf8.RuneCountInString(*event.SourceCalendar) > 120 {
		return fmt.Errorf("%w: source calendar is too long", ErrValidation)
	}
	return nil
}

func validateReminderInputs(inputs []ReminderInput) error {
	seen := map[string]bool{}
	for _, input := range inputs {
		if input.OffsetMinutes < 0 || input.OffsetMinutes > 525600 || (input.Channel != "in_app" && input.Channel != "email") {
			return fmt.Errorf("%w: invalid reminder", ErrValidation)
		}
		key := fmt.Sprintf("%d:%s", input.OffsetMinutes, input.Channel)
		if seen[key] {
			return fmt.Errorf("%w: duplicate reminder", ErrValidation)
		}
		seen[key] = true
	}
	return nil
}

func reminderOutbox(reminder model.CalendarReminder) model.OutboxDraft {
	return model.OutboxDraft{EventType: "reminder.delivery.requested", AggregateType: "reminder", AggregateID: reminder.ID,
		Payload: resourceJSON(map[string]any{
			"reminderId": reminder.ID, "eventId": reminder.EventID,
			"channel": reminder.Channel, "scheduledAt": reminder.ScheduledAt,
		}), AvailableAt: reminder.ScheduledAt}
}

func trimmedOptional(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func decodeCalendarResult(response CommandResponse, err error) (CalendarEventResult, error) {
	if err != nil {
		return CalendarEventResult{}, err
	}
	var result CalendarEventResult
	if decodeErr := json.Unmarshal(response.Body, &result); decodeErr != nil {
		return CalendarEventResult{}, fmt.Errorf("decode calendar response: %w", decodeErr)
	}
	return result, nil
}
