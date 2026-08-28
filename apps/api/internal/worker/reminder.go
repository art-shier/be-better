package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/mail"
	"strings"
	"time"

	daymail "dayorder.local/api/internal/mail"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

const (
	ReminderDelivered = "delivered"
	ReminderFailed    = "failed"
)

type ReminderDelivery struct {
	UserID           uuid.UUID
	ReminderID       uuid.UUID
	EventID          uuid.UUID
	Channel          string
	ScheduledAt      time.Time
	Status           string
	Version          int64
	DeletedAt        *time.Time
	AccountStatus    string
	AccountDeletedAt *time.Time
	Email            string
	DisplayName      string
	EventTitle       string
	EventStartAt     time.Time
	Timezone         string
	EventDeletedAt   *time.Time
}

type ReminderDeliveryResult struct {
	UserID        uuid.UUID
	ReminderID    uuid.UUID
	EventID       uuid.UUID
	OutboxEventID uuid.UUID
	Channel       string
	ScheduledAt   time.Time
	Outcome       string
	FailureReason string
}

type ReminderDeliveryStore interface {
	Load(context.Context, uuid.UUID, uuid.UUID) (ReminderDelivery, error)
	RecordResult(context.Context, ReminderDeliveryResult) error
}

type ReminderHandler struct {
	store  ReminderDeliveryStore
	sender daymail.Sender
}

type reminderPayload struct {
	ReminderID  uuid.UUID `json:"reminderId"`
	EventID     uuid.UUID `json:"eventId"`
	Channel     string    `json:"channel"`
	ScheduledAt time.Time `json:"scheduledAt"`
}

func NewReminderHandler(store ReminderDeliveryStore, sender daymail.Sender) (*ReminderHandler, error) {
	if store == nil {
		return nil, errors.New("reminder delivery store is required")
	}
	if sender == nil {
		return nil, errors.New("email sender is required")
	}
	return &ReminderHandler{store: store, sender: sender}, nil
}

func (handler *ReminderHandler) Handle(ctx context.Context, event model.OutboxEvent) error {
	payload, err := parseReminderPayload(event)
	if err != nil {
		return err
	}
	delivery, err := handler.store.Load(ctx, event.UserID, payload.ReminderID)
	if errors.Is(err, model.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load reminder delivery: %w", err)
	}
	if !currentReminderDelivery(delivery, payload) {
		return nil
	}

	result := ReminderDeliveryResult{
		UserID: event.UserID, ReminderID: delivery.ReminderID, EventID: delivery.EventID,
		OutboxEventID: event.ID, Channel: delivery.Channel, ScheduledAt: delivery.ScheduledAt,
		Outcome: ReminderDelivered,
	}
	if delivery.Channel == "email" {
		message, messageErr := reminderMessage(delivery)
		if messageErr != nil {
			return messageErr
		}
		if sendErr := handler.sender.Send(ctx, message); sendErr != nil {
			result.Outcome = ReminderFailed
			result.FailureReason = "email delivery failed"
			if recordErr := handler.store.RecordResult(ctx, result); recordErr != nil {
				return errors.Join(fmt.Errorf("send reminder email: %w", sendErr), fmt.Errorf("record reminder failure: %w", recordErr))
			}
			return fmt.Errorf("send reminder email: %w", sendErr)
		}
	}
	if err = handler.store.RecordResult(ctx, result); err != nil {
		return fmt.Errorf("record reminder delivery: %w", err)
	}
	return nil
}

func parseReminderPayload(event model.OutboxEvent) (reminderPayload, error) {
	var payload reminderPayload
	if event.UserID == uuid.Nil || event.ID == uuid.Nil || event.AggregateType != "reminder" || event.AggregateID == uuid.Nil {
		return reminderPayload{}, errors.New("invalid reminder delivery event")
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return reminderPayload{}, errors.New("invalid reminder delivery payload")
	}
	payload.Channel = strings.TrimSpace(payload.Channel)
	if payload.ReminderID == uuid.Nil || payload.EventID == uuid.Nil || payload.ReminderID != event.AggregateID ||
		(payload.Channel != "email" && payload.Channel != "in_app") || payload.ScheduledAt.IsZero() {
		return reminderPayload{}, errors.New("invalid reminder delivery payload")
	}
	payload.ScheduledAt = payload.ScheduledAt.UTC()
	return payload, nil
}

func currentReminderDelivery(delivery ReminderDelivery, payload reminderPayload) bool {
	if delivery.UserID == uuid.Nil || delivery.ReminderID != payload.ReminderID || delivery.EventID != payload.EventID ||
		delivery.Channel != payload.Channel || !delivery.ScheduledAt.UTC().Equal(payload.ScheduledAt) ||
		delivery.DeletedAt != nil || delivery.EventDeletedAt != nil || delivery.AccountDeletedAt != nil ||
		delivery.AccountStatus != "active" {
		return false
	}
	return delivery.Status == "pending" || delivery.Status == "failed" || delivery.Status == "processing"
}

func reminderMessage(delivery ReminderDelivery) (daymail.Message, error) {
	email := strings.TrimSpace(delivery.Email)
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		return daymail.Message{}, errors.New("reminder recipient is invalid")
	}
	location, err := time.LoadLocation(strings.TrimSpace(delivery.Timezone))
	if err != nil {
		return daymail.Message{}, errors.New("reminder timezone is invalid")
	}
	name := strings.TrimSpace(delivery.DisplayName)
	if name == "" {
		name = "日序用户"
	}
	title := strings.TrimSpace(delivery.EventTitle)
	if title == "" {
		return daymail.Message{}, errors.New("reminder event title is invalid")
	}
	localStart := delivery.EventStartAt.In(location).Format("2006-01-02 15:04")
	return daymail.Message{
		To:      email,
		Subject: "提醒：" + title,
		Text:    fmt.Sprintf("%s，你好：\n\n你的日程“%s”将在 %s（%s）开始。\n", name, title, localStart, delivery.Timezone),
		HTML: fmt.Sprintf(
			"<p>%s，你好：</p><p>你的日程“%s”将在 <strong>%s</strong>（%s）开始。</p>",
			html.EscapeString(name), html.EscapeString(title), html.EscapeString(localStart), html.EscapeString(delivery.Timezone),
		),
	}, nil
}
