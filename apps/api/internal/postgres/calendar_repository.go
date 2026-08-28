package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"dayorder.local/api/internal/database"
	db "dayorder.local/api/internal/db/gen"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type CalendarRepository struct{}

func NewCalendarRepository() *CalendarRepository { return &CalendarRepository{} }

func (*CalendarRepository) CreateEvent(ctx context.Context, tx database.Tx, userID uuid.UUID, event model.CalendarEvent) (model.CalendarEvent, error) {
	row, err := db.New(tx).CreateCalendarEvent(ctx, db.CreateCalendarEventParams{
		ID: pgUUID(event.ID), UserID: pgUUID(userID), Title: event.Title,
		StartAt: pgTime(event.StartAt), EndAt: pgTime(event.EndAt), Timezone: event.Timezone,
		Location: pgOptionalText(event.Location), Kind: event.Kind,
		SourceCalendar: pgOptionalText(event.SourceCalendar), GoalID: pgOptionalUUID(event.GoalID),
	})
	if err != nil {
		return model.CalendarEvent{}, mapDatabaseError("create calendar event", err)
	}
	return calendarEventFromRow(row), nil
}

func (*CalendarRepository) GetEvent(ctx context.Context, tx database.Tx, userID, eventID uuid.UUID) (model.CalendarEvent, error) {
	row, err := db.New(tx).GetCalendarEvent(ctx, pgUUID(userID), pgUUID(eventID))
	if err != nil {
		return model.CalendarEvent{}, mapDatabaseError("get calendar event", err)
	}
	return calendarEventFromRow(row), nil
}

func (*CalendarRepository) ListEvents(ctx context.Context, tx database.Tx, userID uuid.UUID, start, end *time.Time, limit int) ([]model.CalendarEvent, error) {
	rows, err := db.New(tx).ListCalendarEvents(
		ctx, pgUUID(userID), pgOptionalTime(start), pgOptionalTime(end), int32(limit),
	)
	if err != nil {
		return nil, fmt.Errorf("list calendar events: %w", err)
	}
	events := make([]model.CalendarEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, calendarEventFromRow(row))
	}
	return events, nil
}

func (*CalendarRepository) UpdateEvent(ctx context.Context, tx database.Tx, userID uuid.UUID, event model.CalendarEvent, expectedVersion int64) (model.CalendarEvent, []model.CalendarReminder, error) {
	queries := db.New(tx)
	row, err := queries.UpdateCalendarEvent(ctx, db.UpdateCalendarEventParams{
		Title: event.Title, StartAt: pgTime(event.StartAt), EndAt: pgTime(event.EndAt), Timezone: event.Timezone,
		Location: pgOptionalText(event.Location), Kind: event.Kind, SourceCalendar: pgOptionalText(event.SourceCalendar),
		GoalID: pgOptionalUUID(event.GoalID), UserID: pgUUID(userID), ID: pgUUID(event.ID), ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return model.CalendarEvent{}, nil, calendarWriteError(ctx, queries, userID, event.ID, "update calendar event", err)
	}
	reminderRows, err := queries.RescheduleCalendarReminders(ctx, pgTime(event.StartAt), pgUUID(userID), pgUUID(event.ID))
	if err != nil {
		return model.CalendarEvent{}, nil, fmt.Errorf("reschedule calendar reminders: %w", err)
	}
	return calendarEventFromRow(row), calendarRemindersFromRows(reminderRows), nil
}

func (*CalendarRepository) DeleteEvent(ctx context.Context, tx database.Tx, userID, eventID uuid.UUID, expectedVersion int64) (model.CalendarEvent, []model.CalendarReminder, error) {
	queries := db.New(tx)
	row, err := queries.SoftDeleteCalendarEvent(ctx, pgUUID(userID), pgUUID(eventID), expectedVersion)
	if err != nil {
		return model.CalendarEvent{}, nil, calendarWriteError(ctx, queries, userID, eventID, "delete calendar event", err)
	}
	reminderRows, err := queries.SoftDeleteCalendarReminders(ctx, pgUUID(userID), pgUUID(eventID))
	if err != nil {
		return model.CalendarEvent{}, nil, fmt.Errorf("delete calendar reminders: %w", err)
	}
	return calendarEventFromRow(row), calendarRemindersFromRows(reminderRows), nil
}

func (*CalendarRepository) CreateReminder(ctx context.Context, tx database.Tx, userID uuid.UUID, reminder model.CalendarReminder) (model.CalendarReminder, error) {
	row, err := db.New(tx).CreateCalendarReminder(ctx, db.CreateCalendarReminderParams{
		ID: pgUUID(reminder.ID), UserID: pgUUID(userID), EventID: pgUUID(reminder.EventID),
		OffsetMinutes: int32(reminder.OffsetMinutes), Channel: reminder.Channel, ScheduledAt: pgTime(reminder.ScheduledAt),
	})
	if err != nil {
		return model.CalendarReminder{}, mapDatabaseError("create calendar reminder", err)
	}
	return calendarReminderFromRow(row), nil
}

func (*CalendarRepository) ListReminders(ctx context.Context, tx database.Tx, userID, eventID uuid.UUID) ([]model.CalendarReminder, error) {
	rows, err := db.New(tx).ListCalendarReminders(ctx, pgUUID(userID), pgUUID(eventID))
	if err != nil {
		return nil, fmt.Errorf("list calendar reminders: %w", err)
	}
	return calendarRemindersFromRows(rows), nil
}

func calendarWriteError(ctx context.Context, queries *db.Queries, userID, eventID uuid.UUID, operation string, err error) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return mapDatabaseError(operation, err)
	}
	if _, readErr := queries.GetCalendarEvent(ctx, pgUUID(userID), pgUUID(eventID)); errors.Is(readErr, pgx.ErrNoRows) {
		return model.ErrNotFound
	} else if readErr != nil {
		return fmt.Errorf("check calendar event after failed write: %w", readErr)
	}
	return model.ErrConflict
}

func calendarEventFromRow(row *db.DayorderCalendarEvent) model.CalendarEvent {
	return model.CalendarEvent{ID: uuid.UUID(row.ID.Bytes), Title: row.Title, StartAt: row.StartAt.Time.UTC(), EndAt: row.EndAt.Time.UTC(), Timezone: row.Timezone,
		Location: optionalText(row.Location), Kind: row.Kind, SourceCalendar: optionalText(row.SourceCalendar), GoalID: optionalUUID(row.GoalID), Version: row.Version,
		CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), DeletedAt: optionalTime(row.DeletedAt)}
}

func calendarReminderFromRow(row *db.DayorderCalendarEventReminder) model.CalendarReminder {
	return model.CalendarReminder{ID: uuid.UUID(row.ID.Bytes), EventID: uuid.UUID(row.EventID.Bytes), OffsetMinutes: int(row.OffsetMinutes), Channel: row.Channel,
		ScheduledAt: row.ScheduledAt.Time.UTC(), Status: row.Status, DeliveredAt: optionalTime(row.DeliveredAt), Attempts: int(row.Attempts), Version: row.Version,
		CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), DeletedAt: optionalTime(row.DeletedAt)}
}

func calendarRemindersFromRows(rows []*db.DayorderCalendarEventReminder) []model.CalendarReminder {
	reminders := make([]model.CalendarReminder, 0, len(rows))
	for _, row := range rows {
		reminders = append(reminders, calendarReminderFromRow(row))
	}
	return reminders
}
