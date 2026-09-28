package agentexecution_test

import (
	"encoding/json"
	"testing"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"

	"github.com/google/uuid"
)

func TestReadonlyForegroundDoesNotEnqueue(t *testing.T) {
	id := uuid.New()
	if got := agentexecution.RunOutbox(agentprotocol.ExecutionModeForeground, id); len(got) != 0 {
		t.Fatalf("foreground outbox = %#v", got)
	}
	got := agentexecution.RunOutbox(agentprotocol.ExecutionModeBackground, id)
	if len(got) != 1 || got[0].EventType != "agent.readonly.run.requested" {
		t.Fatalf("background outbox = %#v", got)
	}
	if got[0].AggregateType != "agent_run" || got[0].AggregateID != id {
		t.Fatalf("background aggregate = %#v", got[0])
	}
	var payload struct {
		FormatVersion int    `json:"formatVersion"`
		RunID         string `json:"runId"`
	}
	if err := json.Unmarshal(got[0].Payload, &payload); err != nil {
		t.Fatalf("decode background payload: %v", err)
	}
	if payload.FormatVersion != 1 || payload.RunID != id.String() {
		t.Fatalf("background payload = %#v", payload)
	}
}

func TestReadonlyScopePolicy(t *testing.T) {
	from := "2026-09-01T00:00:00Z"
	to := "2026-09-02T00:00:00Z"
	explicitEmpty := []string{}
	tests := []struct {
		name  string
		scope agentprotocol.AgentScope
	}{
		{name: "missing from", scope: agentprotocol.AgentScope{Domains: []string{"calendar"}, To: &to}},
		{name: "missing to", scope: agentprotocol.AgentScope{Domains: []string{"calendar"}, From: &from}},
		{name: "equal instants in different offsets", scope: agentprotocol.AgentScope{Domains: []string{"calendar"}, From: ptr("2026-09-01T08:00:00+08:00"), To: ptr("2026-09-01T00:00:00Z")}},
		{name: "reverse window", scope: agentprotocol.AgentScope{Domains: []string{"calendar"}, From: &to, To: &from}},
		{name: "over 31 days", scope: agentprotocol.AgentScope{Domains: []string{"calendar"}, From: &from, To: ptr("2026-10-02T00:00:01Z")}},
		{name: "empty domains", scope: agentprotocol.AgentScope{Domains: []string{}, From: &from, To: &to}},
		{name: "duplicate domains", scope: agentprotocol.AgentScope{Domains: []string{"calendar", "calendar"}, From: &from, To: &to}},
		{name: "other domain", scope: agentprotocol.AgentScope{Domains: []string{"tasks"}, From: &from, To: &to}},
		{name: "explicit empty entity ids", scope: agentprotocol.AgentScope{Domains: []string{"calendar"}, EntityIds: explicitEmpty, From: &from, To: &to}},
		{name: "specified entity id", scope: agentprotocol.AgentScope{Domains: []string{"calendar"}, EntityIds: []string{uuid.NewString()}, From: &from, To: &to}},
		{name: "malformed from", scope: agentprotocol.AgentScope{Domains: []string{"calendar"}, From: ptr("not-a-time"), To: &to}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := agentexecution.ValidateScope(test.scope); err == nil {
				t.Fatalf("ValidateScope(%#v) succeeded", test.scope)
			}
		})
	}

	if err := agentexecution.ValidateScope(agentprotocol.AgentScope{
		Domains: []string{"calendar"}, From: &from, To: &to,
	}); err != nil {
		t.Fatalf("ValidateScope(valid) error = %v", err)
	}
}

func ptr(value string) *string { return &value }
