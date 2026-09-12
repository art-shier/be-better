package agenttool

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"dayorder.local/api/internal/agentprotocol"
)

type fakeBinding struct {
	spec agentprotocol.ToolSpec
}

func (b *fakeBinding) Spec() agentprotocol.ToolSpec { return b.spec }

func (b *fakeBinding) Invoke(context.Context, map[string]any, Context) (agentprotocol.ToolResult, error) {
	return agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{}}, nil
}

func toolSpec(id string, domains []string, targets []agentprotocol.ToolSpecExecutionTargetsElem) agentprotocol.ToolSpec {
	if domains == nil {
		domains = []string{}
	}
	return agentprotocol.ToolSpec{
		ID:               id,
		Description:      id + " description",
		InputSchema:      agentprotocol.ToolSpecInputSchema{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
		OutputSchema:     agentprotocol.ToolSpecOutputSchema{"type": "object"},
		SideEffect:       agentprotocol.SideEffectRead,
		RequiredDomains:  domains,
		ExecutionTargets: targets,
		ApprovalPolicy:   agentprotocol.ToolSpecApprovalPolicyNever,
		Idempotent:       true,
		TimeoutMs:        1000,
		ResultMaxBytes:   1024,
	}
}

func snapshot(domains []string, mode agentprotocol.ExecutionMode) agentprotocol.CapabilitySnapshot {
	return agentprotocol.CapabilitySnapshot{
		RuntimeVersion: "2.0.0",
		ExecutionMode:  mode,
		ToolIds:        []string{},
		Skills:         []agentprotocol.SkillRef{},
		Scope:          agentprotocol.AgentScope{Domains: domains},
	}
}

func withGrantedTools(value agentprotocol.CapabilitySnapshot, ids ...string) agentprotocol.CapabilitySnapshot {
	value.ToolIds = slices.Clone(ids)
	return value
}

func mustRegister(t *testing.T, registry *Registry, spec agentprotocol.ToolSpec) {
	t.Helper()
	if err := registry.Register(&fakeBinding{spec: spec}); err != nil {
		t.Fatal(err)
	}
}

func TestEffectiveToolIDsIntersectsRequestedBindingsRuntimeScopeAndPolicy(t *testing.T) {
	tests := []struct {
		name      string
		requested []string
		bindings  []agentprotocol.ToolSpec
		snapshot  agentprotocol.CapabilitySnapshot
		policy    Policy
		want      []string
	}{
		{
			name:      "filters missing bindings and scope mismatches",
			requested: []string{"missing.tool", "device.calendar.read", "dayorder.notes.search"},
			bindings: []agentprotocol.ToolSpec{
				toolSpec("dayorder.notes.search", []string{"notes"}, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemClient, agentprotocol.ToolSpecExecutionTargetsElemServer}),
				toolSpec("device.calendar.read", []string{"calendar"}, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemServer}),
			},
			snapshot: withGrantedTools(snapshot([]string{"notes"}, agentprotocol.ExecutionModeBackground), "device.calendar.read", "dayorder.notes.search"),
			policy:   Policy{Allow: []string{"*"}},
			want:     []string{"dayorder.notes.search"},
		},
		{
			name:      "maps foreground to client targets",
			requested: []string{"dayorder.server.only", "dayorder.client.only"},
			bindings: []agentprotocol.ToolSpec{
				toolSpec("dayorder.server.only", nil, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemServer}),
				toolSpec("dayorder.client.only", nil, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemClient}),
			},
			snapshot: withGrantedTools(snapshot(nil, agentprotocol.ExecutionModeForeground), "dayorder.client.only", "dayorder.server.only"),
			policy:   Policy{Allow: []string{"*"}},
			want:     []string{"dayorder.client.only"},
		},
		{
			name:      "maps background to server targets",
			requested: []string{"dayorder.client.only", "dayorder.server.only"},
			bindings: []agentprotocol.ToolSpec{
				toolSpec("dayorder.client.only", nil, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemClient}),
				toolSpec("dayorder.server.only", nil, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemServer}),
			},
			snapshot: withGrantedTools(snapshot(nil, agentprotocol.ExecutionModeBackground), "dayorder.client.only", "dayorder.server.only"),
			policy:   Policy{Allow: []string{"*"}},
			want:     []string{"dayorder.server.only"},
		},
		{
			name:      "gives explicit deny precedence over wildcard allow",
			requested: []string{"dayorder.denied", "dayorder.allowed"},
			bindings: []agentprotocol.ToolSpec{
				toolSpec("dayorder.denied", nil, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemServer}),
				toolSpec("dayorder.allowed", nil, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemServer}),
			},
			snapshot: withGrantedTools(snapshot(nil, agentprotocol.ExecutionModeBackground), "dayorder.allowed", "dayorder.denied"),
			policy:   Policy{Allow: []string{"*"}, Deny: []string{"dayorder.denied"}, ApprovalFor: []agentprotocol.SideEffect{agentprotocol.SideEffectIrreversibleWrite}},
			want:     []string{"dayorder.allowed"},
		},
		{
			name:      "deduplicates and sorts requested tools without granting unrequested bindings",
			requested: []string{"dayorder.zeta", "dayorder.alpha", "dayorder.zeta"},
			bindings: []agentprotocol.ToolSpec{
				toolSpec("dayorder.zeta", nil, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemServer}),
				toolSpec("dayorder.alpha", nil, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemServer}),
				toolSpec("dayorder.not-requested", nil, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemServer}),
			},
			snapshot: withGrantedTools(snapshot(nil, agentprotocol.ExecutionModeBackground), "dayorder.alpha", "dayorder.zeta", "dayorder.not-requested"),
			policy:   Policy{Allow: []string{"*"}},
			want:     []string{"dayorder.alpha", "dayorder.zeta"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := &Registry{}
			for _, spec := range tt.bindings {
				mustRegister(t, registry, spec)
			}

			got := EffectiveToolIDs(tt.requested, registry, tt.snapshot, tt.policy)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("EffectiveToolIDs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEffectiveToolIDsNeverWidensFrozenRunGrant(t *testing.T) {
	registry := &Registry{}
	mustRegister(t, registry, toolSpec("dayorder.calendar.read", []string{"calendar"}, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemServer}))
	mustRegister(t, registry, toolSpec("dayorder.calendar.write", []string{"calendar"}, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemServer}))

	got := EffectiveToolIDs(
		[]string{"dayorder.calendar.write", "dayorder.calendar.read"},
		registry,
		withGrantedTools(snapshot([]string{"calendar"}, agentprotocol.ExecutionModeBackground), "dayorder.calendar.read"),
		Policy{Allow: []string{"*"}},
	)
	if !slices.Equal(got, []string{"dayorder.calendar.read"}) {
		t.Fatalf("EffectiveToolIDs() = %v, want frozen Run grant only", got)
	}
}

func TestRegistryRejectsDuplicateAndInvalidToolSpecs(t *testing.T) {
	registry := &Registry{}
	mustRegister(t, registry, toolSpec("dayorder.duplicate", nil, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemServer}))

	err := registry.Register(&fakeBinding{spec: toolSpec("dayorder.duplicate", nil, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemServer})})
	if err == nil || !strings.Contains(err.Error(), "duplicate tool ID: dayorder.duplicate") {
		t.Fatalf("duplicate registration error = %v", err)
	}

	invalid := toolSpec("dayorder.invalid", nil, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemServer})
	invalid.TimeoutMs = 0
	err = registry.Register(&fakeBinding{spec: invalid})
	var validationErr *agentprotocol.ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("invalid registration error = %v, want protocol validation error", err)
	}
	if got := registry.Specs(); len(got) != 1 || got[0].ID != "dayorder.duplicate" {
		t.Fatalf("Specs() = %v, want only dayorder.duplicate", got)
	}
}

func TestRegistryCopiesSpecsAndResolvedBindings(t *testing.T) {
	registry := &Registry{}
	binding := &fakeBinding{spec: toolSpec("dayorder.alpha", []string{"notes"}, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemServer})}
	mustRegister(t, registry, binding.spec)

	binding.spec.ID = "dayorder.mutated"
	binding.spec.RequiredDomains[0] = "calendar"
	binding.spec.InputSchema["type"] = "array"
	binding.spec.InputSchema["properties"].(map[string]any)["query"].(map[string]any)["type"] = "number"

	specs := registry.Specs()
	specs[0].ID = "dayorder.changed"
	specs[0].RequiredDomains[0] = "calendar"
	specs[0].ExecutionTargets[0] = agentprotocol.ToolSpecExecutionTargetsElemClient
	specs[0].InputSchema["type"] = "array"
	specs[0].InputSchema["properties"].(map[string]any)["query"].(map[string]any)["type"] = "number"

	resolved, ok := registry.Resolve("dayorder.alpha")
	if !ok {
		t.Fatal("Resolve(dayorder.alpha) returned no binding")
	}
	resolvedSpec := resolved.Spec()
	resolvedSpec.RequiredDomains[0] = "calendar"
	resolvedSpec.InputSchema["type"] = "array"

	want := toolSpec("dayorder.alpha", []string{"notes"}, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemServer})
	if got := registry.Specs(); len(got) != 1 || !equalToolSpec(got[0], want) {
		t.Fatalf("Specs() after caller mutation = %#v, want %#v", got, want)
	}
	if _, ok := registry.Resolve("dayorder.mutated"); ok {
		t.Fatal("Resolve(dayorder.mutated) unexpectedly found caller-mutated binding")
	}
}

func equalToolSpec(got, want agentprotocol.ToolSpec) bool {
	return got.ID == want.ID && got.Description == want.Description &&
		slices.Equal(got.RequiredDomains, want.RequiredDomains) &&
		slices.Equal(got.ExecutionTargets, want.ExecutionTargets) &&
		got.InputSchema["type"] == "object" &&
		got.InputSchema["properties"].(map[string]any)["query"].(map[string]any)["type"] == "string"
}
