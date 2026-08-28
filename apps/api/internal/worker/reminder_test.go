package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	daymail "dayorder.local/api/internal/mail"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type fakeReminderDeliveryStore struct {
	delivery  ReminderDelivery
	loadErr   error
	results   []ReminderDeliveryResult
	resultErr error
}

func (store *fakeReminderDeliveryStore) Load(
	_ context.Context,
	userID uuid.UUID,
	reminderID uuid.UUID,
) (ReminderDelivery, error) {
	if userID != store.delivery.UserID || reminderID != store.delivery.ReminderID {
		return ReminderDelivery{}, errors.New("unexpected reminder lookup")
	}
	return store.delivery, store.loadErr
}

func (store *fakeReminderDeliveryStore) RecordResult(
	_ context.Context,
	result ReminderDeliveryResult,
) error {
	store.results = append(store.results, result)
	return store.resultErr
}

type failingSender struct{ err error }

func (sender *failingSender) Send(context.Context, daymail.Message) error { return sender.err }

func TestReminderHandlerDeliversInAppWithoutEmail(t *testing.T) {
	delivery := testReminderDelivery()
	delivery.Channel = "in_app"
	store := &fakeReminderDeliveryStore{delivery: delivery}
	sender := &fakeSender{}
	handler, err := NewReminderHandler(store, sender)
	if err != nil {
		t.Fatal(err)
	}

	event := reminderEvent(delivery)
	if err = handler.Handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 0 {
		t.Fatalf("in-app reminder sent %d emails", len(sender.messages))
	}
	if len(store.results) != 1 || store.results[0].Outcome != ReminderDelivered {
		t.Fatalf("delivery results = %#v", store.results)
	}
	if store.results[0].OutboxEventID != event.ID {
		t.Fatalf("outbox event ID = %s, want %s", store.results[0].OutboxEventID, event.ID)
	}
}

func TestReminderHandlerSendsLocalizedEscapedEmail(t *testing.T) {
	delivery := testReminderDelivery()
	store := &fakeReminderDeliveryStore{delivery: delivery}
	sender := &fakeSender{}
	handler, err := NewReminderHandler(store, sender)
	if err != nil {
		t.Fatal(err)
	}

	if err = handler.Handle(context.Background(), reminderEvent(delivery)); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(sender.messages))
	}
	message := sender.messages[0]
	if message.To != delivery.Email || !strings.Contains(message.Text, "2026-08-29 17:30") {
		t.Fatalf("message recipient/time = %#v", message)
	}
	if strings.Contains(message.HTML, "<script>") || !strings.Contains(message.HTML, "&lt;script&gt;") {
		t.Fatalf("HTML title was not escaped: %s", message.HTML)
	}
	if len(store.results) != 1 || store.results[0].Outcome != ReminderDelivered {
		t.Fatalf("delivery results = %#v", store.results)
	}
}

func TestReminderHandlerRecordsFailedEmailForOutboxRetry(t *testing.T) {
	delivery := testReminderDelivery()
	store := &fakeReminderDeliveryStore{delivery: delivery}
	sendErr := errors.New("SMTP unavailable")
	handler, err := NewReminderHandler(store, &failingSender{err: sendErr})
	if err != nil {
		t.Fatal(err)
	}

	err = handler.Handle(context.Background(), reminderEvent(delivery))
	if !errors.Is(err, sendErr) {
		t.Fatalf("Handle() error = %v, want wrapped send error", err)
	}
	if len(store.results) != 1 || store.results[0].Outcome != ReminderFailed {
		t.Fatalf("delivery results = %#v", store.results)
	}
	if store.results[0].FailureReason != "email delivery failed" {
		t.Fatalf("failure reason = %q", store.results[0].FailureReason)
	}
}

func TestReminderHandlerSkipsStaleDeletedAndAlreadyDeliveredEvents(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ReminderDelivery)
	}{
		{name: "stale schedule", mutate: func(value *ReminderDelivery) { value.ScheduledAt = value.ScheduledAt.Add(time.Hour) }},
		{name: "deleted", mutate: func(value *ReminderDelivery) { deleted := time.Now().UTC(); value.DeletedAt = &deleted }},
		{name: "delivered", mutate: func(value *ReminderDelivery) { value.Status = "delivered" }},
		{name: "cancelled", mutate: func(value *ReminderDelivery) { value.Status = "cancelled" }},
		{name: "inactive account", mutate: func(value *ReminderDelivery) { value.AccountStatus = "disabled" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			delivery := testReminderDelivery()
			event := reminderEvent(delivery)
			test.mutate(&delivery)
			store := &fakeReminderDeliveryStore{delivery: delivery}
			sender := &fakeSender{}
			handler, err := NewReminderHandler(store, sender)
			if err != nil {
				t.Fatal(err)
			}
			if err = handler.Handle(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if len(sender.messages) != 0 || len(store.results) != 0 {
				t.Fatalf("stale reminder sent=%d results=%d", len(sender.messages), len(store.results))
			}
		})
	}
}

func TestReminderHandlerRejectsMalformedOrMismatchedPayload(t *testing.T) {
	delivery := testReminderDelivery()
	store := &fakeReminderDeliveryStore{delivery: delivery}
	handler, err := NewReminderHandler(store, &fakeSender{})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []model.OutboxEvent{
		{AggregateType: "reminder", AggregateID: delivery.ReminderID, Payload: []byte("not-json")},
		{AggregateType: "task", AggregateID: delivery.ReminderID, Payload: []byte(`{"reminderId":"` + delivery.ReminderID.String() + `"}`)},
		{AggregateType: "reminder", AggregateID: uuid.New(), Payload: []byte(`{"reminderId":"` + delivery.ReminderID.String() + `"}`)},
	} {
		if err = handler.Handle(context.Background(), event); err == nil {
			t.Fatalf("event %#v unexpectedly accepted", event)
		}
	}
}

func testReminderDelivery() ReminderDelivery {
	return ReminderDelivery{
		UserID:     uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		ReminderID: uuid.MustParse("22222222-2222-4222-8222-222222222222"),
		EventID:    uuid.MustParse("33333333-3333-4333-8333-333333333333"),
		Channel:    "email", ScheduledAt: time.Date(2026, 8, 29, 8, 30, 0, 0, time.UTC),
		Status: "pending", Email: "user@example.com", DisplayName: "日序用户",
		AccountStatus: "active",
		EventTitle:    "复盘 <script>", EventStartAt: time.Date(2026, 8, 29, 9, 30, 0, 0, time.UTC),
		Timezone: "Asia/Shanghai",
	}
}

func reminderEvent(delivery ReminderDelivery) model.OutboxEvent {
	payload, _ := json.Marshal(map[string]any{
		"reminderId":  delivery.ReminderID,
		"eventId":     delivery.EventID,
		"channel":     delivery.Channel,
		"scheduledAt": delivery.ScheduledAt,
	})
	return model.OutboxEvent{
		ID:            uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		UserID:        delivery.UserID,
		EventType:     "reminder.delivery.requested",
		AggregateType: "reminder",
		AggregateID:   delivery.ReminderID,
		Payload:       payload,
	}
}
