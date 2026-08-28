package model

import (
	"time"

	"github.com/google/uuid"
)

type Record struct {
	ID         uuid.UUID  `json:"id"`
	RawText    string     `json:"rawText"`
	Kind       string     `json:"kind"`
	OccurredAt time.Time  `json:"occurredAt"`
	Mood       *int       `json:"mood,omitempty"`
	Energy     *int       `json:"energy,omitempty"`
	ArchivedAt *time.Time `json:"archivedAt,omitempty"`
	Version    int64      `json:"version"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	DeletedAt  *time.Time `json:"deletedAt,omitempty"`
	Tags       []Tag      `json:"tags,omitempty"`
}

type Note struct {
	ID           uuid.UUID  `json:"id"`
	Title        string     `json:"title"`
	BodyMarkdown string     `json:"bodyMarkdown"`
	Category     string     `json:"category"`
	ArchivedAt   *time.Time `json:"archivedAt,omitempty"`
	Version      int64      `json:"version"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	DeletedAt    *time.Time `json:"deletedAt,omitempty"`
	Tags         []Tag      `json:"tags,omitempty"`
}

type DailyReview struct {
	ID            uuid.UUID  `json:"id"`
	ReviewDate    string     `json:"reviewDate"`
	Wins          string     `json:"wins"`
	Blockers      string     `json:"blockers"`
	Mood          *int       `json:"mood,omitempty"`
	Energy        *int       `json:"energy,omitempty"`
	TomorrowFocus string     `json:"tomorrowFocus"`
	AISummary     *string    `json:"aiSummary,omitempty"`
	Version       int64      `json:"version"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	DeletedAt     *time.Time `json:"deletedAt,omitempty"`
}

type Tag struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	Version   int64      `json:"version"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	DeletedAt *time.Time `json:"deletedAt,omitempty"`
}

type EntityLink struct {
	ID           uuid.UUID `json:"id"`
	SourceType   string    `json:"sourceType"`
	SourceID     uuid.UUID `json:"sourceId"`
	TargetType   string    `json:"targetType"`
	TargetID     uuid.UUID `json:"targetId"`
	RelationType string    `json:"relationType"`
	CreatedAt    time.Time `json:"createdAt"`
}
