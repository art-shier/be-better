package agentskill

import (
	"context"
	"slices"
	"strings"
	"testing"

	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agenttool"
)

func profileWith(t *testing.T, replacements ...[2]string) SkillProfile {
	t.Helper()
	bundle := validBundle(t)
	for _, replacement := range replacements {
		bundle.SkillMarkdown = bytesReplace(bundle.SkillMarkdown, replacement[0], replacement[1])
	}
	profile, err := ParseBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func bytesReplace(source []byte, from, to string) []byte {
	return []byte(strings.Replace(string(source), from, to, 1))
}

func capabilitySnapshot(overrides ...func(*agentprotocol.CapabilitySnapshot)) agentprotocol.CapabilitySnapshot {
	snapshot := agentprotocol.CapabilitySnapshot{
		RuntimeVersion: "2.0.0",
		ExecutionMode:  agentprotocol.ExecutionModeForeground,
		ToolIds:        []string{},
		Skills:         []agentprotocol.SkillRef{},
		Scope:          agentprotocol.AgentScope{Domains: []string{}},
	}
	for _, override := range overrides {
		override(&snapshot)
	}
	return snapshot
}

type testBinding struct{ spec agentprotocol.ToolSpec }

func (b testBinding) Spec() agentprotocol.ToolSpec { return b.spec }
func (b testBinding) Invoke(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
	return agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{}}, nil
}

func registerTool(t *testing.T, registry *agenttool.Registry, id string, domains []string) {
	t.Helper()
	err := registry.Register(testBinding{spec: agentprotocol.ToolSpec{
		ID: id, Description: id + " description",
		InputSchema: agentprotocol.ToolSpecInputSchema{"type": "object"}, OutputSchema: agentprotocol.ToolSpecOutputSchema{"type": "object"},
		SideEffect: agentprotocol.SideEffectRead, RequiredDomains: domains,
		ExecutionTargets: []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemClient, agentprotocol.ToolSpecExecutionTargetsElemServer},
		ApprovalPolicy:   agentprotocol.ToolSpecApprovalPolicyNever, Idempotent: true, TimeoutMs: 1000, ResultMaxBytes: 1024,
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegistryProgressiveDisclosureAndModelVisibility(t *testing.T) {
	visible := profileWith(t)
	hidden := profileWith(t, [2]string{"name: calendar-management", "name: hidden-calendar"}, [2]string{"disable-model-invocation: false", "disable-model-invocation: true"})
	modelOnly := profileWith(t, [2]string{"name: calendar-management", "name: model-only-calendar"}, [2]string{"user-invocable: true", "user-invocable: false"})
	registry, err := NewRegistry([]SkillProfile{modelOnly, hidden, visible})
	if err != nil {
		t.Fatal(err)
	}
	listed := registry.List()
	names := []string{listed[0].Name, listed[1].Name, listed[2].Name}
	if !slices.Equal(names, []string{"calendar-management", "hidden-calendar", "model-only-calendar"}) {
		t.Fatalf("List names = %v", names)
	}
	if strings.Contains(toJSON(t, listed), "Read only the requested date range") {
		t.Fatal("List leaked Skill instructions")
	}
	model := registry.ListForModel()
	if got := []string{model[0].Name, model[1].Name}; !slices.Equal(got, []string{"calendar-management", "model-only-calendar"}) {
		t.Fatalf("ListForModel names = %v", got)
	}
	if _, ok := registry.Load("hidden-calendar"); !ok {
		t.Fatal("ordinary load hid model-disabled Skill")
	}
	if _, ok := registry.LoadForModel("hidden-calendar"); ok {
		t.Fatal("model load exposed model-disabled Skill")
	}
}

func TestRegistryActivationIntersectsPermission(t *testing.T) {
	registry, err := NewRegistry([]SkillProfile{profileWith(t)})
	if err != nil {
		t.Fatal(err)
	}
	tools := &agenttool.Registry{}
	registerTool(t, tools, "dayorder.calendar.read", []string{"calendar"})
	registerTool(t, tools, "dayorder.calendar.propose-change", []string{"calendar"})
	allowAll := agenttool.Policy{Allow: []string{"*"}, Deny: []string{}, ApprovalFor: []agentprotocol.SideEffect{}}
	calendarScopeWithoutGrant := capabilitySnapshot(func(snapshot *agentprotocol.CapabilitySnapshot) {
		snapshot.Scope.Domains = []string{"calendar"}
	})
	activation, err := registry.Activate("calendar-management", calendarScopeWithoutGrant, tools, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(activation.ActiveToolIds) != 0 || !slices.Contains(activation.RequestedToolIds, "dayorder.calendar.read") {
		t.Fatalf("activation = %#v", activation)
	}

	withCalendar := capabilitySnapshot(func(snapshot *agentprotocol.CapabilitySnapshot) {
		snapshot.Scope.Domains = []string{"calendar"}
		snapshot.ToolIds = []string{"dayorder.calendar.read", "dayorder.calendar.propose-change"}
	})
	activation, err = registry.Activate("calendar-management", withCalendar, tools, agenttool.Policy{Allow: []string{"*"}, Deny: []string{"dayorder.calendar.propose-change"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(activation.ActiveToolIds, []string{"dayorder.calendar.read"}) {
		t.Fatalf("active tools = %v", activation.ActiveToolIds)
	}
}

func TestRegistryRejectsIncompatibleActivation(t *testing.T) {
	cases := []struct {
		name         string
		replacements [][2]string
		snapshot     agentprotocol.CapabilitySnapshot
	}{
		{"minimum runtime", [][2]string{{"min-runtime-version: 1.0.0", "min-runtime-version: 2.1.0"}}, capabilitySnapshot()},
		{"foreground target", [][2]string{{"execution-target: either", "execution-target: server"}}, capabilitySnapshot()},
		{"background target", [][2]string{{"execution-target: either", "execution-target: client"}}, capabilitySnapshot(func(s *agentprotocol.CapabilitySnapshot) { s.ExecutionMode = agentprotocol.ExecutionModeBackground })},
		{"background disallowed", [][2]string{{"background-allowed: true", "background-allowed: false"}}, capabilitySnapshot(func(s *agentprotocol.CapabilitySnapshot) { s.ExecutionMode = agentprotocol.ExecutionModeBackground })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry, err := NewRegistry([]SkillProfile{profileWith(t, tc.replacements...)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Activate("calendar-management", tc.snapshot, &agenttool.Registry{}, agenttool.Policy{Allow: []string{"*"}}); err == nil {
				t.Fatal("Activate() succeeded")
			}
		})
	}
}

func TestRegistryRejectsDuplicatesAndMismatchedProfiles(t *testing.T) {
	profile := profileWith(t)
	if _, err := NewRegistry([]SkillProfile{profile, profile}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error = %v", err)
	}
	profile.Descriptor.Description = "mismatch"
	if _, err := NewRegistry([]SkillProfile{profile}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestRegistryReturnsIsolatedCopies(t *testing.T) {
	alpha := profileWith(t, [2]string{"calendar-management", "alpha-calendar"})
	zeta := profileWith(t, [2]string{"calendar-management", "zeta-calendar"})
	registry, err := NewRegistry([]SkillProfile{zeta, alpha})
	if err != nil {
		t.Fatal(err)
	}
	alpha.Body = "caller mutation"
	alpha.Manifest.Description = "caller mutation"
	clear(alpha.SupportingFiles[0].Content)

	listed := registry.List()
	listed[0].Name = "return mutation"
	loaded, ok := registry.Load("alpha-calendar")
	if !ok {
		t.Fatal("Load() not found")
	}
	loaded.Body = "return mutation"
	loaded.Manifest.Description = "return mutation"
	clear(loaded.SupportingFiles[0].Content)

	again, _ := registry.Load("alpha-calendar")
	if got := registry.List(); got[0].Name != "alpha-calendar" || got[1].Name != "zeta-calendar" {
		t.Fatalf("List after mutation = %#v", got)
	}
	if !strings.Contains(again.Body, "Read only the requested date range") || again.Manifest.Description != "Read and propose changes to DayOrder calendar data." || !strings.Contains(string(again.SupportingFiles[0].Content), "# Usage") {
		t.Fatalf("Load after mutation = %#v", again)
	}

	first, err := registry.Activate("alpha-calendar", capabilitySnapshot(), &agenttool.Registry{}, agenttool.Policy{Allow: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	first.Skill.Name = "mutated"
	first.RequestedToolIds = first.RequestedToolIds[:0]
	first.SupportingFiles[0].Path = "mutated"
	second, err := registry.Activate("alpha-calendar", capabilitySnapshot(), &agenttool.Registry{}, agenttool.Policy{Allow: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Skill.Name != "alpha-calendar" || len(second.RequestedToolIds) != 2 || second.SupportingFiles[0].Path != "references/usage.md" {
		t.Fatalf("second activation = %#v", second)
	}
}
