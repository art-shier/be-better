package model

import (
	"time"

	"github.com/google/uuid"
)

type Task struct {
	ID              uuid.UUID  `json:"id"`
	Title           string     `json:"title"`
	Status          string     `json:"status"`
	Priority        string     `json:"priority"`
	EstimateMinutes int        `json:"estimateMinutes"`
	DueAt           *time.Time `json:"dueAt,omitempty"`
	ScheduledStart  *time.Time `json:"scheduledStart,omitempty"`
	ScheduledEnd    *time.Time `json:"scheduledEnd,omitempty"`
	GoalID          *uuid.UUID `json:"goalId,omitempty"`
	SourceRecordID  *uuid.UUID `json:"sourceRecordId,omitempty"`
	CompletedAt     *time.Time `json:"completedAt,omitempty"`
	Version         int64      `json:"version"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	DeletedAt       *time.Time `json:"deletedAt,omitempty"`
}
