package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type AgentScope struct {
	Domains   []string    `json:"domains"`
	EntityIDs []uuid.UUID `json:"entityIds,omitempty"`
	From      *time.Time  `json:"from,omitempty"`
	To        *time.Time  `json:"to,omitempty"`
}

type AgentRun struct {
	ID           uuid.UUID        `json:"id"`
	Intent       string           `json:"intent"`
	Status       string           `json:"status"`
	ActionMode   string           `json:"actionMode"`
	Scope        json.RawMessage  `json:"scope"`
	Provider     *string          `json:"provider,omitempty"`
	Model        *string          `json:"model,omitempty"`
	StartedAt    *time.Time       `json:"startedAt,omitempty"`
	FinishedAt   *time.Time       `json:"finishedAt,omitempty"`
	Summary      *string          `json:"summary,omitempty"`
	ErrorCode    *string          `json:"errorCode,omitempty"`
	ErrorMessage *string          `json:"errorMessage,omitempty"`
	Version      int64            `json:"version"`
	CreatedAt    time.Time        `json:"createdAt"`
	UpdatedAt    time.Time        `json:"updatedAt"`
	Steps        []AgentStep      `json:"steps"`
	Changes      []AgentChange    `json:"changes"`
	SourceRefs   []AgentSourceRef `json:"sourceRefs"`
}

type AgentStep struct {
	ID         uuid.UUID       `json:"id"`
	RunID      uuid.UUID       `json:"runId"`
	SequenceNo int             `json:"sequenceNo"`
	Title      string          `json:"title"`
	Detail     string          `json:"detail"`
	Status     string          `json:"status"`
	Metadata   json.RawMessage `json:"metadata"`
	StartedAt  *time.Time      `json:"startedAt,omitempty"`
	FinishedAt *time.Time      `json:"finishedAt,omitempty"`
	Version    int64           `json:"version"`
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
}

type AgentChange struct {
	ID            uuid.UUID       `json:"id"`
	RunID         uuid.UUID       `json:"runId"`
	ChangeType    string          `json:"changeType"`
	TargetType    string          `json:"targetType"`
	TargetID      *uuid.UUID      `json:"targetId,omitempty"`
	BaseVersion   *int64          `json:"baseVersion,omitempty"`
	Patch         json.RawMessage `json:"patch"`
	PreviewBefore json.RawMessage `json:"previewBefore,omitempty"`
	PreviewAfter  json.RawMessage `json:"previewAfter,omitempty"`
	Reason        string          `json:"reason"`
	Status        string          `json:"status"`
	AcceptedAt    *time.Time      `json:"acceptedAt,omitempty"`
	AppliedAt     *time.Time      `json:"appliedAt,omitempty"`
	Version       int64           `json:"version"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

type AgentSourceRef struct {
	ID            uuid.UUID `json:"id"`
	RunID         uuid.UUID `json:"runId"`
	EntityType    string    `json:"entityType"`
	EntityID      uuid.UUID `json:"entityId"`
	EntityVersion int64     `json:"entityVersion"`
	LabelSnapshot string    `json:"labelSnapshot"`
	CreatedAt     time.Time `json:"createdAt"`
}

type AgentStepDraft struct {
	Title    string          `json:"title"`
	Detail   string          `json:"detail"`
	Metadata json.RawMessage `json:"metadata"`
}

type AgentChangeDraft struct {
	ChangeType    string          `json:"changeType"`
	TargetType    string          `json:"targetType"`
	TargetID      *uuid.UUID      `json:"targetId,omitempty"`
	BaseVersion   *int64          `json:"baseVersion,omitempty"`
	Patch         json.RawMessage `json:"patch"`
	PreviewBefore json.RawMessage `json:"previewBefore,omitempty"`
	PreviewAfter  json.RawMessage `json:"previewAfter,omitempty"`
	Reason        string          `json:"reason"`
}

type AgentSourceRefDraft struct {
	EntityType    string    `json:"entityType"`
	EntityID      uuid.UUID `json:"entityId"`
	EntityVersion int64     `json:"entityVersion"`
	LabelSnapshot string    `json:"labelSnapshot"`
}

type AgentPlan struct {
	Summary    string                `json:"summary"`
	Steps      []AgentStepDraft      `json:"steps"`
	Changes    []AgentChangeDraft    `json:"changes"`
	SourceRefs []AgentSourceRefDraft `json:"sourceRefs"`
}

type AgentSnapshot struct {
	Run     AgentRun
	Goals   []Goal
	Tasks   []Task
	Events  []CalendarEvent
	Records []Record
	Notes   []Note
}

type AgentApplyResult struct {
	Change          AgentChange     `json:"change"`
	Run             AgentRun        `json:"run"`
	RunUpdated      bool            `json:"-"`
	TargetType      string          `json:"-"`
	TargetID        uuid.UUID       `json:"-"`
	TargetOperation string          `json:"-"`
	TargetVersion   int64           `json:"-"`
	BeforeData      json.RawMessage `json:"-"`
	AfterData       json.RawMessage `json:"-"`
}
