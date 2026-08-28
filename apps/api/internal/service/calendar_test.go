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

type fakeCalendarStore struct {
	event     model.CalendarEvent
	reminders []model.CalendarReminder
	events    []model.CalendarEvent
	listAfter *model.ResourcePosition
	listLimit int
	deleted   []model.CalendarReminder
}

func (store *fakeCalendarStore) CreateEvent(_ context.Context, _ database.Tx, _ uuid.UUID, event model.CalendarEvent) (model.CalendarEvent, error) {
	event.Version = 1
	event.CreatedAt = time.Now().UTC()
	event.UpdatedAt = event.CreatedAt
	store.event = event
	return event, nil
}
func (store *fakeCalendarStore) GetEvent(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.CalendarEvent, error) {
	return store.event, nil
}
func (store *fakeCalendarStore) ListEvents(_ context.Context, _ database.Tx, _ uuid.UUID, _, _ *time.Time, after *model.ResourcePosition, limit int) ([]model.CalendarEvent, error) {
	store.listAfter = after
	store.listLimit = limit
	return store.events, nil
}
func (store *fakeCalendarStore) UpdateEvent(_ context.Context, _ database.Tx, _ uuid.UUID, event model.CalendarEvent, expected int64) (model.CalendarEvent, []model.CalendarReminder, error) {
	event.Version = expected + 1
	store.event = event
	for index := range store.reminders {
		store.reminders[index].ScheduledAt = event.StartAt.Add(-time.Duration(store.reminders[index].OffsetMinutes) * time.Minute)
		store.reminders[index].Version++
	}
	return event, append([]model.CalendarReminder(nil), store.reminders...), nil
}
func (*fakeCalendarStore) DeleteEvent(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64) (model.CalendarEvent, []model.CalendarReminder, error) {
	return model.CalendarEvent{}, nil, nil
}
func (store *fakeCalendarStore) CreateReminder(_ context.Context, _ database.Tx, _ uuid.UUID, reminder model.CalendarReminder) (model.CalendarReminder, error) {
	reminder.Version = 1
	reminder.CreatedAt = time.Now().UTC()
	reminder.UpdatedAt = reminder.CreatedAt
	store.reminders = append(store.reminders, reminder)
	return reminder, nil
}
func (store *fakeCalendarStore) ListReminders(context.Context, database.Tx, uuid.UUID, uuid.UUID) ([]model.CalendarReminder, error) {
	return store.reminders, nil
}
func (store *fakeCalendarStore) DeleteReminder(_ context.Context, _ database.Tx, _ uuid.UUID, _, reminderID uuid.UUID) (model.CalendarReminder, error) {
	for index, reminder := range store.reminders {
		if reminder.ID == reminderID {
			reminder.Version++
			deletedAt := time.Now().UTC()
			reminder.DeletedAt = &deletedAt
			store.deleted = append(store.deleted, reminder)
			store.reminders = append(store.reminders[:index], store.reminders[index+1:]...)
			return reminder, nil
		}
	}
	return model.CalendarReminder{}, model.ErrNotFound
}

func TestCalendarServiceValidatesIANAZoneAndSchedulesReminderOutbox(t *testing.T) {
	store := &fakeCalendarStore{}
	outbox := &recordingOutboxWriter{}
	idempotency, _ := NewIdempotencyService(&memoryIdempotencyStore{})
	commands, _ := NewCommandService(immediateUserTransactor{tx: &testTransaction{}}, idempotency, &recordingSyncWriter{}, &recordingAuditWriter{}, outbox)
	cursors, _ := NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	service, _ := NewCalendarService(store, immediateUserTransactor{tx: &testTransaction{}}, commands, cursors)
	start := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	input := CalendarEventInput{Title: "Focus", StartAt: start, EndAt: start.Add(time.Hour), Timezone: "not/a-zone", Kind: "focus"}
	if _, err := service.Create(context.Background(), testMutation(), input); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid timezone error=%v", err)
	}
	input.Timezone = "Asia/Shanghai"
	input.Reminders = []ReminderInput{{OffsetMinutes: 30, Channel: "in_app"}}
	result, err := service.Create(context.Background(), testMutation(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Reminders) != 1 || !result.Reminders[0].ScheduledAt.Equal(start.Add(-30*time.Minute)) {
		t.Fatalf("reminders=%#v", result.Reminders)
	}
	if len(outbox.events) != 1 || outbox.events[0].EventType != "reminder.delivery.requested" || !outbox.events[0].AvailableAt.Equal(start.Add(-30*time.Minute)) {
		t.Fatalf("outbox=%#v", outbox.events)
	}
	var reminderPayload struct {
		ScheduledAt time.Time `json:"scheduledAt"`
	}
	if err = json.Unmarshal(outbox.events[0].Payload, &reminderPayload); err != nil || !reminderPayload.ScheduledAt.Equal(start.Add(-30*time.Minute)) {
		t.Fatalf("outbox payload=%s err=%v", outbox.events[0].Payload, err)
	}
}

func TestCalendarServicePaginatesByStartTimeAndID(t *testing.T) {
	firstStart := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	secondStart := firstStart.Add(time.Hour)
	store := &fakeCalendarStore{events: []model.CalendarEvent{
		{ID: uuid.New(), StartAt: firstStart},
		{ID: uuid.New(), StartAt: secondStart},
		{ID: uuid.New(), StartAt: secondStart.Add(time.Hour)},
	}}
	cursors, _ := NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	calendar, err := NewCalendarService(store, immediateUserTransactor{tx: &testTransaction{}}, &CommandService{}, cursors)
	if err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	page, err := calendar.List(context.Background(), userID, nil, nil, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || !page.HasMore || page.NextCursor == "" || store.listLimit != 3 {
		t.Fatalf("page=%#v listLimit=%d", page, store.listLimit)
	}
	store.events = nil
	if _, err = calendar.List(context.Background(), userID, nil, nil, page.NextCursor, 2); err != nil {
		t.Fatal(err)
	}
	if store.listAfter == nil || store.listAfter.ID != page.Events[1].ID || !store.listAfter.UpdatedAt.Equal(secondStart) {
		t.Fatalf("decoded position=%#v", store.listAfter)
	}
}

func TestCalendarUpdateReconcilesReminderSet(t *testing.T) {
	eventID := uuid.New()
	start := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	keptID, removedID := uuid.New(), uuid.New()
	store := &fakeCalendarStore{
		event: model.CalendarEvent{ID: eventID, Title: "Focus", StartAt: start, EndAt: start.Add(time.Hour), Timezone: "Asia/Shanghai", Kind: "focus", Version: 1},
		reminders: []model.CalendarReminder{
			{ID: keptID, EventID: eventID, OffsetMinutes: 10, Channel: "in_app", ScheduledAt: start.Add(-10 * time.Minute), Version: 1},
			{ID: removedID, EventID: eventID, OffsetMinutes: 30, Channel: "email", ScheduledAt: start.Add(-30 * time.Minute), Version: 1},
		},
	}
	syncWriter := &recordingSyncWriter{}
	outbox := &recordingOutboxWriter{}
	idempotency, _ := NewIdempotencyService(&memoryIdempotencyStore{})
	commands, _ := NewCommandService(immediateUserTransactor{tx: &testTransaction{}}, idempotency, syncWriter, &recordingAuditWriter{}, outbox)
	cursors, _ := NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	calendar, _ := NewCalendarService(store, immediateUserTransactor{tx: &testTransaction{}}, commands, cursors)
	newStart := start.Add(24 * time.Hour)

	result, err := calendar.Update(context.Background(), testMutation(), eventID, 1, CalendarEventInput{
		Title: "Focus", StartAt: newStart, EndAt: newStart.Add(time.Hour), Timezone: "Asia/Shanghai", Kind: "focus",
		Reminders: []ReminderInput{{OffsetMinutes: 10, Channel: "in_app"}, {OffsetMinutes: 60, Channel: "email"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Reminders) != 2 || result.Reminders[0].ID != keptID || result.Reminders[0].Version != 2 || !result.Reminders[0].ScheduledAt.Equal(newStart.Add(-10*time.Minute)) {
		t.Fatalf("updated reminders=%#v", result.Reminders)
	}
	if result.Reminders[1].ID == uuid.Nil || result.Reminders[1].ID == removedID || result.Reminders[1].OffsetMinutes != 60 {
		t.Fatalf("created reminder=%#v", result.Reminders[1])
	}
	if len(store.deleted) != 1 || store.deleted[0].ID != removedID || store.deleted[0].DeletedAt == nil {
		t.Fatalf("deleted reminders=%#v", store.deleted)
	}
	operations := map[string]string{}
	for _, change := range syncWriter.changes {
		operations[change.EntityID.String()] = change.Operation
	}
	if operations[keptID.String()] != "update" || operations[removedID.String()] != "delete" || operations[result.Reminders[1].ID.String()] != "create" {
		t.Fatalf("sync changes=%#v", syncWriter.changes)
	}
	if len(outbox.events) != 2 {
		t.Fatalf("outbox events=%#v", outbox.events)
	}
}
