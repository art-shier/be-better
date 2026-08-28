package model

import (
	"time"

	"github.com/google/uuid"
)

type Goal struct {
	ID           uuid.UUID  `json:"id"`
	Title        string     `json:"title"`
	Why          string     `json:"why"`
	Area         string     `json:"area"`
	MetricType   string     `json:"metricType"`
	TargetValue  float64    `json:"targetValue"`
	CurrentValue float64    `json:"currentValue"`
	Unit         string     `json:"unit"`
	StartDate    string     `json:"startDate"`
	DueDate      *string    `json:"dueDate,omitempty"`
	Status       string     `json:"status"`
	Health       string     `json:"health"`
	Version      int64      `json:"version"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	DeletedAt    *time.Time `json:"deletedAt,omitempty"`
}

type GoalMilestone struct {
	ID          uuid.UUID  `json:"id"`
	GoalID      uuid.UUID  `json:"goalId"`
	Title       string     `json:"title"`
	DueAt       *time.Time `json:"dueAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	SortOrder   int        `json:"sortOrder"`
	Version     int64      `json:"version"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	DeletedAt   *time.Time `json:"deletedAt,omitempty"`
}
