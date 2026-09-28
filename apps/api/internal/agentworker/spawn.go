// Package agentworker shapes and validates frozen worker wire contracts. It
// intentionally contains no worker execution or scheduling implementation.
package agentworker

import (
	"fmt"
	"sort"

	"dayorder.local/api/internal/agentprotocol"
)

type ParentContext struct {
	RunID                   string
	Depth                   int
	ExecutionMode           agentprotocol.ExecutionMode
	Capabilities            agentprotocol.CapabilitySnapshot
	Budget                  agentprotocol.Budget
	RemainingBudget         agentprotocol.Budget
	LoadedSkillInstructions []agentprotocol.WorkerSkillInstructions
}

type SpawnRequest struct {
	WorkerID       string
	Depth          int
	ExecutionMode  agentprotocol.ExecutionMode
	Task           agentprotocol.WorkerTask
	ToolIDs        []string
	Skills         []agentprotocol.SkillRef
	Budget         agentprotocol.Budget
	OutputContract agentprotocol.WorkerOutputContract
}

// ResultProtocolError classifies a malformed worker result at the runtime
// boundary without changing the shared DTO validator's validation_failed code.
type ResultProtocolError struct {
	cause error
}

func (e *ResultProtocolError) Error() string {
	return "protocol_incompatible: " + e.cause.Error()
}

func (e *ResultProtocolError) Unwrap() error { return e.cause }
func (e *ResultProtocolError) Code() string  { return "protocol_incompatible" }

var forbiddenMetaTools = map[string]struct{}{
	"skill_list": {}, "skill_load": {}, "permission_grant": {}, "permission.grant": {},
}

func validationError(format string, args ...any) error {
	return fmt.Errorf("validation_failed: "+format, args...)
}

func skillKey(skill agentprotocol.SkillRef) string {
	return skill.Name + "\x00" + string(skill.Version) + "\x00" + skill.Digest
}

func budgetValues(b agentprotocol.Budget) [6]int {
	return [6]int{b.MaxSteps, b.MaxTokens, b.MaxDurationMs, b.MaxWorkers, b.MaxConcurrency, b.MaxRepeatedToolCalls}
}

func validateBudgetCapacity(parent ParentContext, request SpawnRequest) error {
	if parent.RemainingBudget.MaxWorkers < 1 || parent.RemainingBudget.MaxConcurrency < 1 {
		return validationError("parent worker capacity is exhausted")
	}
	total := budgetValues(parent.Budget)
	remaining := budgetValues(parent.RemainingBudget)
	requested := budgetValues(request.Budget)
	names := [6]string{"maxSteps", "maxTokens", "maxDurationMs", "maxWorkers", "maxConcurrency", "maxRepeatedToolCalls"}
	for i, name := range names {
		if remaining[i] < 0 || remaining[i] > total[i] {
			return validationError("parent remaining budget %s is invalid", name)
		}
		if requested[i] > remaining[i] {
			return validationError("requested %s exceeds parent remaining budget", name)
		}
	}
	return nil
}

func validateOutputContract(contract agentprotocol.WorkerOutputContract) error {
	want := [4]agentprotocol.WorkerOutputContractSectionsElem{
		agentprotocol.WorkerOutputContractSectionsElemSummary,
		agentprotocol.WorkerOutputContractSectionsElemFindings,
		agentprotocol.WorkerOutputContractSectionsElemRisks,
		agentprotocol.WorkerOutputContractSectionsElemNextSteps,
	}
	if contract.Sections != want {
		return validationError("output contract must use summary, findings, risks, next_steps in order")
	}
	return nil
}

func cloneStrings(source []string) []string {
	if source == nil {
		return nil
	}
	return append([]string(nil), source...)
}

func cloneSkillRefs(source []agentprotocol.SkillRef) []agentprotocol.SkillRef {
	if source == nil {
		return nil
	}
	return append([]agentprotocol.SkillRef(nil), source...)
}

func cloneScope(source agentprotocol.AgentScope) agentprotocol.AgentScope {
	cloned := agentprotocol.AgentScope{
		Domains:   cloneStrings(source.Domains),
		EntityIds: cloneStrings(source.EntityIds),
	}
	if source.From != nil {
		value := *source.From
		cloned.From = &value
	}
	if source.To != nil {
		value := *source.To
		cloned.To = &value
	}
	return cloned
}

func cloneTask(source agentprotocol.WorkerTask) agentprotocol.WorkerTask {
	return agentprotocol.WorkerTask{
		Goal:            source.Goal,
		SuccessCriteria: cloneStrings(source.SuccessCriteria),
		Scope:           cloneStrings(source.Scope),
		Constraints:     cloneStrings(source.Constraints),
		Hints:           cloneStrings(source.Hints),
	}
}

func cloneInstructions(source []agentprotocol.WorkerSkillInstructions) []agentprotocol.WorkerSkillInstructions {
	if source == nil {
		return nil
	}
	return append([]agentprotocol.WorkerSkillInstructions(nil), source...)
}

// FreezeSpawnSpec validates delegation against the parent's frozen capacity
// and returns a deterministic deep copy suitable for the worker wire boundary.
func FreezeSpawnSpec(parent ParentContext, request SpawnRequest) (agentprotocol.WorkerSpawnSpec, error) {
	if err := agentprotocol.Validate(agentprotocol.DefinitionCapabilitySnapshot, parent.Capabilities); err != nil {
		return agentprotocol.WorkerSpawnSpec{}, err
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionBudget, parent.Budget); err != nil {
		return agentprotocol.WorkerSpawnSpec{}, err
	}
	if parent.ExecutionMode != parent.Capabilities.ExecutionMode {
		return agentprotocol.WorkerSpawnSpec{}, validationError("parent execution mode does not match its capability snapshot")
	}
	if parent.Depth != 0 || request.Depth != 1 {
		return agentprotocol.WorkerSpawnSpec{}, validationError("worker depth exceeds the maximum nesting depth of one")
	}
	if request.ExecutionMode != parent.ExecutionMode {
		return agentprotocol.WorkerSpawnSpec{}, validationError("worker execution mode must be inherited from parent")
	}
	if err := validateOutputContract(request.OutputContract); err != nil {
		return agentprotocol.WorkerSpawnSpec{}, err
	}
	if err := validateBudgetCapacity(parent, request); err != nil {
		return agentprotocol.WorkerSpawnSpec{}, err
	}

	availableTools := make(map[string]struct{}, len(parent.Capabilities.ToolIds))
	for _, toolID := range parent.Capabilities.ToolIds {
		availableTools[toolID] = struct{}{}
	}
	seenTools := make(map[string]struct{}, len(request.ToolIDs))
	for _, toolID := range request.ToolIDs {
		if _, exists := seenTools[toolID]; exists {
			return agentprotocol.WorkerSpawnSpec{}, validationError("duplicate tool %s", toolID)
		}
		seenTools[toolID] = struct{}{}
		if _, forbidden := forbiddenMetaTools[toolID]; forbidden {
			return agentprotocol.WorkerSpawnSpec{}, validationError("forbidden meta-tool %s cannot be delegated", toolID)
		}
		if _, exists := availableTools[toolID]; !exists {
			return agentprotocol.WorkerSpawnSpec{}, fmt.Errorf("capability_unavailable: tool %s is not available from the parent", toolID)
		}
	}

	availableSkills := make(map[string]struct{}, len(parent.Capabilities.Skills))
	for _, skill := range parent.Capabilities.Skills {
		availableSkills[skillKey(skill)] = struct{}{}
	}
	loadedSkills := make(map[string]agentprotocol.WorkerSkillInstructions, len(parent.LoadedSkillInstructions))
	for _, instructions := range parent.LoadedSkillInstructions {
		loadedSkills[skillKey(instructions.Skill)] = instructions
	}
	selectedInstructions := make([]agentprotocol.WorkerSkillInstructions, 0, len(request.Skills))
	seenSkills := make(map[string]struct{}, len(request.Skills))
	for _, skill := range request.Skills {
		key := skillKey(skill)
		if _, exists := seenSkills[key]; exists {
			return agentprotocol.WorkerSpawnSpec{}, validationError("duplicate skill %s", skill.Name)
		}
		seenSkills[key] = struct{}{}
		if _, exists := availableSkills[key]; !exists {
			return agentprotocol.WorkerSpawnSpec{}, fmt.Errorf("capability_unavailable: skill %s@%s with digest %s is unavailable", skill.Name, skill.Version, skill.Digest)
		}
		instructions, exists := loadedSkills[key]
		if !exists {
			return agentprotocol.WorkerSpawnSpec{}, fmt.Errorf("capability_unavailable: loaded instructions for skill %s@%s are unavailable", skill.Name, skill.Version)
		}
		selectedInstructions = append(selectedInstructions, instructions)
	}

	tools := cloneStrings(request.ToolIDs)
	sort.Strings(tools)
	skills := cloneSkillRefs(request.Skills)
	sort.Slice(skills, func(i, j int) bool { return skillKey(skills[i]) < skillKey(skills[j]) })
	instructions := cloneInstructions(selectedInstructions)
	sort.Slice(instructions, func(i, j int) bool { return skillKey(instructions[i].Skill) < skillKey(instructions[j].Skill) })
	scope := cloneScope(parent.Capabilities.Scope)
	sort.Strings(scope.Domains)
	sort.Strings(scope.EntityIds)

	spawn := agentprotocol.WorkerSpawnSpec{
		ProtocolVersion: "2.0", ParentRunID: parent.RunID, WorkerID: request.WorkerID, Depth: 1,
		ExecutionMode: parent.ExecutionMode,
		Task:          cloneTask(request.Task),
		Capabilities: agentprotocol.CapabilitySnapshot{
			RuntimeVersion: parent.Capabilities.RuntimeVersion,
			ExecutionMode:  parent.ExecutionMode,
			ToolIds:        tools,
			Skills:         skills,
			Scope:          scope,
		},
		Budget:            request.Budget,
		SkillInstructions: instructions,
		OutputContract:    request.OutputContract,
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionWorkerSpawnSpec, spawn); err != nil {
		return agentprotocol.WorkerSpawnSpec{}, err
	}
	return spawn, nil
}

// ValidateResult enforces both the shared result schema and the exact output
// contract supplied with the frozen spawn.
func ValidateResult(result agentprotocol.WorkerResult, contract agentprotocol.WorkerOutputContract) error {
	if err := validateOutputContract(contract); err != nil {
		return err
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionWorkerResult, result); err != nil {
		return &ResultProtocolError{cause: err}
	}
	return nil
}
