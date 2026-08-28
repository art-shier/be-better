package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type OutboxEvent struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	EventType     string
	AggregateType string
	AggregateID   uuid.UUID
	Payload       json.RawMessage
	Attempts      int
	LockToken     uuid.UUID
}

type OutboxRetry struct {
	EventID     uuid.UUID
	LockToken   uuid.UUID
	AvailableAt time.Time
	LastError   string
	Terminal    bool
}
