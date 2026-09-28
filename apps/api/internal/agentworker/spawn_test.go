package agentworker

import (
	"strings"
	"testing"

	"dayorder.local/api/internal/agentprotocol"
)

func testBudget() agentprotocol.Budget {
	return agentprotocol.Budget{MaxSteps: 8, MaxTokens: 2000, MaxDurationMs: 60000, MaxWorkers: 3, MaxConcurrency: 2, MaxRepeatedToolCalls: 2}
}

func testParent() ParentContext {
	budget := testBudget()
	return ParentContext{
		RunID: "parent-1", Depth: 0, ExecutionMode: agentprotocol.ExecutionModeForeground,
		Capabilities: agentprotocol.CapabilitySnapshot{
			RuntimeVersion: "2.0.0", ExecutionMode: agentprotocol.ExecutionModeForeground,
			ToolIds: []string{"dayorder.notes.write", "dayorder.notes.search"},
			Skills: []agentprotocol.SkillRef{
				{Name: "notes", Version: "1.2.0", Digest: "sha256:notes"},
				{Name: "calendar", Version: "2.0.0", Digest: "sha256:calendar"},
			},
			Scope: agentprotocol.AgentScope{Domains: []string{"notes"}, EntityIds: []string{"note-2", "note-1"}},
		},
		Budget:          budget,
		RemainingBudget: agentprotocol.Budget{MaxSteps: 8, MaxTokens: 1200, MaxDurationMs: 60000, MaxWorkers: 2, MaxConcurrency: 1, MaxRepeatedToolCalls: 2},
		LoadedSkillInstructions: []agentprotocol.WorkerSkillInstructions{
			{Skill: agentprotocol.SkillRef{Name: "notes", Version: "1.2.0", Digest: "sha256:notes"}, Instructions: "Use the notes API carefully."},
			{Skill: agentprotocol.SkillRef{Name: "calendar", Version: "2.0.0", Digest: "sha256:calendar"}, Instructions: "Use the calendar API carefully."},
		},
	}
}

func testRequest() SpawnRequest {
	return SpawnRequest{
		WorkerID: "worker-1", Depth: 1, ExecutionMode: agentprotocol.ExecutionModeForeground,
		Task:           agentprotocol.WorkerTask{Goal: "Find the relevant note", SuccessCriteria: []string{"Return one match"}, Scope: []string{"notes"}, Constraints: []string{"Read only"}, Hints: []string{"Search titles first"}},
		ToolIDs:        []string{"dayorder.notes.search"},
		Skills:         []agentprotocol.SkillRef{{Name: "notes", Version: "1.2.0", Digest: "sha256:notes"}},
		Budget:         agentprotocol.Budget{MaxSteps: 4, MaxTokens: 1200, MaxDurationMs: 30000, MaxWorkers: 1, MaxConcurrency: 1, MaxRepeatedToolCalls: 1},
		OutputContract: expectedOutputContract(),
	}
}

func expectedOutputContract() agentprotocol.WorkerOutputContract {
	return agentprotocol.WorkerOutputContract{Sections: [4]agentprotocol.WorkerOutputContractSectionsElem{
		agentprotocol.WorkerOutputContractSectionsElemSummary,
		agentprotocol.WorkerOutputContractSectionsElemFindings,
		agentprotocol.WorkerOutputContractSectionsElemRisks,
		agentprotocol.WorkerOutputContractSectionsElemNextSteps,
	}}
}

func TestFreezeSpawnSpecCopiesAndSortsNestedData(t *testing.T) {
	parent := testParent()
	request := testRequest()
	request.ToolIDs = []string{"dayorder.notes.write", "dayorder.notes.search"}
	request.Skills = []agentprotocol.SkillRef{
		{Name: "notes", Version: "1.2.0", Digest: "sha256:notes"},
		{Name: "calendar", Version: "2.0.0", Digest: "sha256:calendar"},
	}

	spawn, err := FreezeSpawnSpec(parent, request)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := spawn.Capabilities.ToolIds[0], "dayorder.notes.search"; got != want {
		t.Fatalf("first tool = %q, want %q", got, want)
	}
	if got, want := spawn.Capabilities.Skills[0].Name, "calendar"; got != want {
		t.Fatalf("first skill = %q, want %q", got, want)
	}
	if got, want := spawn.Capabilities.Scope.EntityIds[0], "note-1"; got != want {
		t.Fatalf("first entity = %q, want %q", got, want)
	}
	if got, want := spawn.SkillInstructions[0].Skill.Name, "calendar"; got != want {
		t.Fatalf("first instruction = %q, want %q", got, want)
	}

	request.ToolIDs[0] = "mutated.tool"
	request.Task.SuccessCriteria[0] = "mutated"
	request.Skills[0].Digest = "mutated"
	parent.Capabilities.Scope.EntityIds[0] = "mutated"
	parent.LoadedSkillInstructions[0].Instructions = "mutated"
	if spawn.Capabilities.ToolIds[0] != "dayorder.notes.search" || spawn.Task.SuccessCriteria[0] != "Return one match" || spawn.Capabilities.Skills[1].Digest != "sha256:notes" || spawn.Capabilities.Scope.EntityIds[0] != "note-1" || spawn.SkillInstructions[1].Instructions != "Use the notes API carefully." {
		t.Fatal("spawn aliases caller-owned nested data")
	}
}

func TestFreezeSpawnSpecRejectsUnavailableCapabilitiesModeAndDepth(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SpawnRequest)
		want   string
	}{
		{"unknown tool", func(r *SpawnRequest) { r.ToolIDs = []string{"missing.tool"} }, "capability_unavailable"},
		{"unknown skill digest", func(r *SpawnRequest) { r.Skills[0].Digest = "sha256:unknown" }, "capability_unavailable"},
		{"wrong mode", func(r *SpawnRequest) { r.ExecutionMode = agentprotocol.ExecutionModeBackground }, "execution mode"},
		{"nested worker", func(r *SpawnRequest) { r.Depth = 2 }, "depth"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testRequest()
			tt.mutate(&r)
			_, err := FreezeSpawnSpec(testParent(), r)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestFreezeSpawnSpecRejectsForbiddenMetaToolsEvenWhenParentListsThem(t *testing.T) {
	for _, toolID := range []string{"skill_list", "skill_load", "permission_grant", "permission.grant"} {
		t.Run(toolID, func(t *testing.T) {
			p := testParent()
			p.Capabilities.ToolIds = []string{toolID}
			r := testRequest()
			r.ToolIDs = []string{toolID}
			_, err := FreezeSpawnSpec(p, r)
			if err == nil || !strings.Contains(err.Error(), "forbidden meta-tool") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestFreezeSpawnSpecEnforcesEveryRemainingBudgetBoundary(t *testing.T) {
	tests := []struct {
		name  string
		raise func(*agentprotocol.Budget)
	}{
		{"steps", func(b *agentprotocol.Budget) { b.MaxSteps++ }},
		{"tokens", func(b *agentprotocol.Budget) { b.MaxTokens++ }},
		{"duration", func(b *agentprotocol.Budget) { b.MaxDurationMs++ }},
		{"workers", func(b *agentprotocol.Budget) { b.MaxWorkers++ }},
		{"concurrency", func(b *agentprotocol.Budget) { b.MaxConcurrency++ }},
		{"repeated calls", func(b *agentprotocol.Budget) { b.MaxRepeatedToolCalls++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testParent()
			r := testRequest()
			r.Budget = p.RemainingBudget
			tt.raise(&r.Budget)
			_, err := FreezeSpawnSpec(p, r)
			if err == nil || !strings.Contains(err.Error(), "remaining budget") {
				t.Fatalf("error = %v", err)
			}
		})
	}
	p := testParent()
	r := testRequest()
	r.Budget = p.RemainingBudget
	if _, err := FreezeSpawnSpec(p, r); err != nil {
		t.Fatalf("boundary rejected: %v", err)
	}
}

func TestFreezeSpawnSpecRejectsInconsistentAndExhaustedParentCapacity(t *testing.T) {
	p := testParent()
	p.RemainingBudget.MaxTokens = p.Budget.MaxTokens + 1
	if _, err := FreezeSpawnSpec(p, testRequest()); err == nil || !strings.Contains(err.Error(), "remaining budget") {
		t.Fatalf("error = %v", err)
	}
	p = testParent()
	p.RemainingBudget.MaxWorkers = 0
	if _, err := FreezeSpawnSpec(p, testRequest()); err == nil || !strings.Contains(err.Error(), "worker capacity") {
		t.Fatalf("error = %v", err)
	}
}

func validResult() agentprotocol.WorkerResult {
	return agentprotocol.WorkerResult{
		ProtocolVersion: "2.0", WorkerID: "worker-1", State: agentprotocol.WorkerResultStateSucceeded,
		Sections: &agentprotocol.WorkerResultSections{Summary: "done", Findings: "one", Risks: "none", NextSteps: "ship"},
		Usage:    agentprotocol.Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
	}
}

func TestValidateResultAcceptsExactResultAndContract(t *testing.T) {
	if err := ValidateResult(validResult(), expectedOutputContract()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateResultRejectsMalformedResults(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*agentprotocol.WorkerResult)
	}{
		{"missing sections", func(r *agentprotocol.WorkerResult) { r.Sections = nil }},
		{"wrong protocol", func(r *agentprotocol.WorkerResult) { r.ProtocolVersion = "1.0" }},
		{"failed with sections", func(r *agentprotocol.WorkerResult) { r.State = agentprotocol.WorkerResultStateFailed }},
		{"invalid usage", func(r *agentprotocol.WorkerResult) { r.Usage.TotalTokens = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validResult()
			tt.mutate(&r)
			err := ValidateResult(r, expectedOutputContract())
			if err == nil || !strings.Contains(err.Error(), "protocol_incompatible") {
				t.Fatalf("error = %v", err)
			}
			coded, ok := err.(interface{ Code() string })
			if !ok || coded.Code() != "protocol_incompatible" {
				t.Fatalf("error code = %v, want protocol_incompatible", coded)
			}
		})
	}
}

func TestValidateResultRejectsMissingExtraDuplicateOrReorderedContractSections(t *testing.T) {
	tests := []struct {
		name     string
		sections [4]agentprotocol.WorkerOutputContractSectionsElem
	}{
		{"missing", [4]agentprotocol.WorkerOutputContractSectionsElem{"summary", "findings", "risks", ""}},
		{"extra name", [4]agentprotocol.WorkerOutputContractSectionsElem{"summary", "findings", "risks", "appendix"}},
		{"duplicate", [4]agentprotocol.WorkerOutputContractSectionsElem{"summary", "findings", "findings", "next_steps"}},
		{"reordered", [4]agentprotocol.WorkerOutputContractSectionsElem{"findings", "summary", "risks", "next_steps"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateResult(validResult(), agentprotocol.WorkerOutputContract{Sections: tt.sections})
			if err == nil {
				t.Fatal("expected invalid output contract")
			}
		})
	}
}
