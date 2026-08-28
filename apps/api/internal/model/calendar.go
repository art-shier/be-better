package model

import (
	"time"

	"github.com/google/uuid"
)

type CalendarEvent struct {
	ID             uuid.UUID  `json:"id"`
	Title          string     `json:"title"`
	StartAt        time.Time  `json:"startAt"`
	EndAt          time.Time  `json:"endAt"`
	Timezone       string     `json:"timezone"`
	Location       *string    `json:"location,omitempty"`
	Kind           string     `json:"kind"`
	SourceCalendar *string    `json:"sourceCalendar,omitempty"`
	GoalID         *uuid.UUID `json:"goalId,omitempty"`
	Version        int64      `json:"version"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	DeletedAt      *time.Time `json:"deletedAt,omitempty"`
}

type CalendarReminder struct {
	ID            uuid.UUID  `json:"id"`
	EventID       uuid.UUID  `json:"eventId"`
	OffsetMinutes int        `json:"offsetMinutes"`
	Channel       string     `json:"channel"`
	ScheduledAt   time.Time  `json:"scheduledAt"`
	Status        string     `json:"status"`
	DeliveredAt   *time.Time `json:"deliveredAt,omitempty"`
	Attempts      int        `json:"attempts"`
	Version       int64      `json:"version"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	DeletedAt     *time.Time `json:"deletedAt,omitempty"`
}
