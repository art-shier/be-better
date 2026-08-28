package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type AuditEvent struct {
	ID         uuid.UUID       `json:"id"`
	ActorType  string          `json:"actorType"`
	ActorID    *uuid.UUID      `json:"actorId,omitempty"`
	Action     string          `json:"action"`
	RequestID  uuid.UUID       `json:"requestId"`
	BeforeData json.RawMessage `json:"beforeData,omitempty"`
	AfterData  json.RawMessage `json:"afterData,omitempty"`
	Metadata   json.RawMessage `json:"metadata"`
	Entities   []AuditEntity   `json:"entities"`
	CreatedAt  time.Time       `json:"createdAt"`
	Undoable   bool            `json:"undoable"`
}

type UndoResult struct {
	OriginalAuditID uuid.UUID       `json:"originalAuditId"`
	EntityType      string          `json:"entityType"`
	EntityID        uuid.UUID       `json:"entityId"`
	EntityOperation string          `json:"entityOperation"`
	EntityVersion   int64           `json:"entityVersion"`
	Data            json.RawMessage `json:"data,omitempty"`
	BeforeData      json.RawMessage `json:"-"`
	AfterData       json.RawMessage `json:"-"`
}
