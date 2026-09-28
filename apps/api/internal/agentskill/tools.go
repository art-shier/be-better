package agentskill

import (
	"context"
	"encoding/json"
	"fmt"

	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agenttool"
)

const resultLimit = 256 * 1024

const (
	skillNamePattern = "^[a-z0-9][a-z0-9-]{0,63}$"
	semVerPattern    = "^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\\+[0-9A-Za-z-]+(?:\\.[0-9A-Za-z-]+)*)?$"
	toolIDPattern    = "^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$"
)

type metaBinding struct {
	spec   agentprotocol.ToolSpec
	invoke func(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error)
}

func (b metaBinding) Spec() agentprotocol.ToolSpec {
	raw, _ := json.Marshal(b.spec)
	var copied agentprotocol.ToolSpec
	_ = json.Unmarshal(raw, &copied)
	return copied
}

func (b metaBinding) Invoke(ctx context.Context, input map[string]any, toolContext agenttool.Context) (agentprotocol.ToolResult, error) {
	return b.invoke(ctx, input, toolContext)
}

// MetaBindings returns the model-facing progressive-disclosure bindings in
// stable skill_list, skill_load order.
func MetaBindings(registry *Registry, snapshot agentprotocol.CapabilitySnapshot, tools *agenttool.Registry, policy agenttool.Policy) []agenttool.Binding {
	list := metaBinding{spec: skillListSpec(), invoke: func(_ context.Context, input map[string]any, _ agenttool.Context) (agentprotocol.ToolResult, error) {
		if input == nil || len(input) != 0 {
			return failure(agentprotocol.ErrorCodeValidationFailed, "skill_list input must be an empty object")
		}
		return success(agentprotocol.ToolResultData{"skills": registry.ListForModel()})
	}}
	load := metaBinding{spec: skillLoadSpec(), invoke: func(_ context.Context, input map[string]any, _ agenttool.Context) (agentprotocol.ToolResult, error) {
		name, ok := input["name"].(string)
		if input == nil || len(input) != 1 || !ok || name == "" {
			return failure(agentprotocol.ErrorCodeValidationFailed, "skill_load input must contain only a non-empty string name")
		}
		if _, visible := registry.LoadForModel(name); !visible {
			return failure(agentprotocol.ErrorCodeCapabilityUnavailable, "Skill is not available to the model: "+name)
		}
		activation, err := registry.Activate(name, snapshot, tools, policy)
		if err != nil {
			return failure(agentprotocol.ErrorCodeCapabilityUnavailable, err.Error())
		}
		return success(agentprotocol.ToolResultData{"activation": activation})
	}}
	return []agenttool.Binding{list, load}
}

func success(data agentprotocol.ToolResultData) (agentprotocol.ToolResult, error) {
	result := agentprotocol.ToolResult{Ok: true, Data: copyResultData(data)}
	if err := agentprotocol.Validate(agentprotocol.DefinitionToolResult, result); err != nil {
		return agentprotocol.ToolResult{}, fmt.Errorf("validate Skill Tool result: %w", err)
	}
	return result, nil
}

func failure(code agentprotocol.ErrorCode, message string) (agentprotocol.ToolResult, error) {
	result := agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: code, Message: message, Retryable: false}}
	if err := agentprotocol.Validate(agentprotocol.DefinitionToolResult, result); err != nil {
		return agentprotocol.ToolResult{}, fmt.Errorf("validate Skill Tool result: %w", err)
	}
	return result, nil
}

func copyResultData(data agentprotocol.ToolResultData) agentprotocol.ToolResultData {
	raw, _ := json.Marshal(data)
	var copied agentprotocol.ToolResultData
	_ = json.Unmarshal(raw, &copied)
	// Preserve the concrete protocol types expected by local callers while the
	// JSON round trip above proves/copies their wire representation.
	for key, value := range data {
		switch typed := value.(type) {
		case []agentprotocol.SkillDescriptor:
			descriptors := make([]agentprotocol.SkillDescriptor, len(typed))
			copy(descriptors, typed)
			copied[key] = descriptors
		case agentprotocol.SkillActivation:
			activation := typed
			activation.RequestedToolIds = make([]string, len(typed.RequestedToolIds))
			copy(activation.RequestedToolIds, typed.RequestedToolIds)
			activation.ActiveToolIds = make([]string, len(typed.ActiveToolIds))
			copy(activation.ActiveToolIds, typed.ActiveToolIds)
			activation.SupportingFiles = make([]agentprotocol.SupportingFileDescriptor, len(typed.SupportingFiles))
			copy(activation.SupportingFiles, typed.SupportingFiles)
			copied[key] = activation
		}
	}
	return copied
}

func skillListSpec() agentprotocol.ToolSpec {
	return agentprotocol.ToolSpec{
		ID: "skill_list", Description: "List Skills available to the model without loading their instructions.",
		InputSchema: agentprotocol.ToolSpecInputSchema{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		OutputSchema: agentprotocol.ToolSpecOutputSchema{
			"type": "object", "properties": map[string]any{"skills": map[string]any{"type": "array", "items": skillDescriptorSchema()}},
			"required": []any{"skills"}, "additionalProperties": false,
		},
		SideEffect: agentprotocol.SideEffectRead, RequiredDomains: []string{},
		ExecutionTargets: []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemClient, agentprotocol.ToolSpecExecutionTargetsElemServer},
		ApprovalPolicy:   agentprotocol.ToolSpecApprovalPolicyNever, Idempotent: true, TimeoutMs: 5000, ResultMaxBytes: resultLimit,
	}
}

func skillLoadSpec() agentprotocol.ToolSpec {
	return agentprotocol.ToolSpec{
		ID: "skill_load", Description: "Load one model-visible Skill and calculate its effective Tool access.",
		InputSchema: agentprotocol.ToolSpecInputSchema{
			"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}},
			"required": []any{"name"}, "additionalProperties": false,
		},
		OutputSchema: agentprotocol.ToolSpecOutputSchema{
			"type": "object", "properties": map[string]any{"activation": skillActivationSchema()},
			"required": []any{"activation"}, "additionalProperties": false,
		},
		SideEffect: agentprotocol.SideEffectRead, RequiredDomains: []string{},
		ExecutionTargets: []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemClient, agentprotocol.ToolSpecExecutionTargetsElemServer},
		ApprovalPolicy:   agentprotocol.ToolSpecApprovalPolicyNever, Idempotent: true, TimeoutMs: 5000, ResultMaxBytes: resultLimit,
	}
}

func skillRefSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"name":    map[string]any{"type": "string", "pattern": skillNamePattern},
			"version": map[string]any{"type": "string", "pattern": semVerPattern},
			"digest":  map[string]any{"type": "string"},
		},
		"required": []any{"name", "version", "digest"},
	}
}

func supportingFileDescriptorSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
			"kind": map[string]any{"enum": []any{"reference", "asset", "schema"}},
			"size": map[string]any{"type": "integer", "minimum": 0},
		},
		"required": []any{"path", "kind", "size"},
	}
}

func skillDescriptorSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"name":                   map[string]any{"type": "string", "pattern": skillNamePattern},
			"version":                map[string]any{"type": "string", "pattern": semVerPattern},
			"digest":                 map[string]any{"type": "string"},
			"description":            map[string]any{"type": "string"},
			"scope":                  map[string]any{"enum": []any{"system", "user", "device"}},
			"executionTarget":        map[string]any{"enum": []any{"client", "server", "either"}},
			"backgroundAllowed":      map[string]any{"type": "boolean"},
			"userInvocable":          map[string]any{"type": "boolean"},
			"disableModelInvocation": map[string]any{"type": "boolean"},
			"riskLevel":              map[string]any{"enum": []any{"low", "medium", "high", "critical"}},
		},
		"required": []any{"name", "version", "digest", "description", "scope", "executionTarget", "backgroundAllowed", "userInvocable", "disableModelInvocation", "riskLevel"},
	}
}

func skillActivationSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"skill": skillRefSchema(), "instructions": map[string]any{"type": "string"},
			"supportingFiles":  map[string]any{"type": "array", "items": supportingFileDescriptorSchema()},
			"requestedToolIds": map[string]any{"type": "array", "items": map[string]any{"type": "string", "pattern": toolIDPattern}},
			"activeToolIds":    map[string]any{"type": "array", "items": map[string]any{"type": "string", "pattern": toolIDPattern}},
		},
		"required": []any{"skill", "instructions", "supportingFiles", "requestedToolIds", "activeToolIds"},
	}
}
