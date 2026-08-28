package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type ClientMutation struct {
	ID             uuid.UUID
	UserID         uuid.UUID
	DeviceID       uuid.UUID
	MutationID     uuid.UUID
	RequestHash    []byte
	ResponseStatus *int
	ResponseBody   json.RawMessage
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

type ClientMutationDraft struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	DeviceID    uuid.UUID
	MutationID  uuid.UUID
	RequestHash []byte
	ExpiresAt   time.Time
}

type SyncChange struct {
	Sequence      int64     `json:"sequence"`
	EntityType    string    `json:"entityType"`
	EntityID      uuid.UUID `json:"entityId"`
	Operation     string    `json:"operation"`
	EntityVersion int64     `json:"entityVersion"`
	ChangedAt     time.Time `json:"changedAt"`
}

type ResourcePosition struct {
	UpdatedAt time.Time
	ID        uuid.UUID
}

type SyncChangeDraft struct {
	EntityType    string
	EntityID      uuid.UUID
	Operation     string
	EntityVersion int64
}

type AuditEntity struct {
	EntityType string
	EntityID   uuid.UUID
}

type AuditDraft struct {
	ID         uuid.UUID
	ActorType  string
	ActorID    *uuid.UUID
	Action     string
	RequestID  uuid.UUID
	BeforeData json.RawMessage
	AfterData  json.RawMessage
	Metadata   json.RawMessage
	Entities   []AuditEntity
}

type OutboxDraft struct {
	ID            uuid.UUID
	EventType     string
	AggregateType string
	AggregateID   uuid.UUID
	Payload       json.RawMessage
	AvailableAt   time.Time
}
