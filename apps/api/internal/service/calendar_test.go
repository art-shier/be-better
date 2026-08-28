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
func (*fakeCalendarStore) ListEvents(context.Context, database.Tx, uuid.UUID, *time.Time, *time.Time, int) ([]model.CalendarEvent, error) {
	return nil, nil
}
func (*fakeCalendarStore) UpdateEvent(context.Context, database.Tx, uuid.UUID, model.CalendarEvent, int64) (model.CalendarEvent, []model.CalendarReminder, error) {
	return model.CalendarEvent{}, nil, nil
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

func TestCalendarServiceValidatesIANAZoneAndSchedulesReminderOutbox(t *testing.T) {
	store := &fakeCalendarStore{}
	outbox := &recordingOutboxWriter{}
	idempotency, _ := NewIdempotencyService(&memoryIdempotencyStore{})
	commands, _ := NewCommandService(immediateUserTransactor{tx: &testTransaction{}}, idempotency, &recordingSyncWriter{}, &recordingAuditWriter{}, outbox)
	service, _ := NewCalendarService(store, immediateUserTransactor{tx: &testTransaction{}}, commands)
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
