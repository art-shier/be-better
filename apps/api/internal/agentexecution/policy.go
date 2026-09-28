package agentexecution

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

const maxReadonlyScopeWindow = 31 * 24 * time.Hour

// Actor is an execution authority constructed by the trusted host.
type Actor struct {
	UserID uuid.UUID
	Token  uuid.UUID
	Mode   agentprotocol.ExecutionMode
}

func ValidateScope(scope agentprotocol.AgentScope) error {
	if len(scope.Domains) != 1 || scope.Domains[0] != "calendar" {
		return errors.New("readonly scope must contain only the calendar domain")
	}
	if scope.EntityIds != nil {
		return errors.New("readonly scope does not accept entity IDs")
	}
	if scope.From == nil || scope.To == nil {
		return errors.New("readonly scope requires from and to")
	}
	from, err := time.Parse(time.RFC3339, *scope.From)
	if err != nil {
		return fmt.Errorf("parse readonly scope from: %w", err)
	}
	to, err := time.Parse(time.RFC3339, *scope.To)
	if err != nil {
		return fmt.Errorf("parse readonly scope to: %w", err)
	}
	if !from.Before(to) {
		return errors.New("readonly scope from must precede to")
	}
	if to.Sub(from) > maxReadonlyScopeWindow {
		return errors.New("readonly scope cannot exceed 31 days")
	}
	return nil
}

func RunOutbox(mode agentprotocol.ExecutionMode, runID uuid.UUID) []model.OutboxDraft {
	if mode != agentprotocol.ExecutionModeBackground {
		return nil
	}
	payload, _ := json.Marshal(struct {
		FormatVersion int    `json:"formatVersion"`
		RunID         string `json:"runId"`
	}{FormatVersion: 1, RunID: runID.String()})
	return []model.OutboxDraft{{
		EventType: "agent.readonly.run.requested", AggregateType: "agent_run",
		AggregateID: runID, Payload: payload,
	}}
}
