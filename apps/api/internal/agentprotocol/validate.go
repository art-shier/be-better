package agentprotocol

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const protocolSchemaID = "https://dayorder.local/schemas/agent/v2/protocol.schema.json"

// Definition identifies one of the protocol definitions in the canonical schema.
type Definition string

const (
	DefinitionAgentError          Definition = "AgentError"
	DefinitionAgentScope          Definition = "AgentScope"
	DefinitionBudget              Definition = "Budget"
	DefinitionCalendarReadInput   Definition = "CalendarReadInput"
	DefinitionCalendarReadData    Definition = "CalendarReadData"
	DefinitionReadonlyRunStart    Definition = "ReadonlyRunStart"
	DefinitionReadonlyRunView     Definition = "ReadonlyRunView"
	DefinitionReadonlyRunFinish   Definition = "ReadonlyRunFinish"
	DefinitionCalendarReadRequest Definition = "CalendarReadRequest"
	DefinitionUsage               Definition = "Usage"
	DefinitionMessage             Definition = "Message"
	DefinitionToolSpec            Definition = "ToolSpec"
	DefinitionToolCall            Definition = "ToolCall"
	DefinitionToolResult          Definition = "ToolResult"
	DefinitionSkillManifest       Definition = "SkillManifest"
	DefinitionSkillDescriptor     Definition = "SkillDescriptor"
	DefinitionSkillRef            Definition = "SkillRef"
	DefinitionSkillActivation     Definition = "SkillActivation"
	DefinitionCapabilitySnapshot  Definition = "CapabilitySnapshot"
	DefinitionProviderEvent       Definition = "ProviderEvent"
	DefinitionProviderEnvelope    Definition = "ProviderEnvelope"
	DefinitionModelTurnRequest    Definition = "ModelTurnRequest"
	DefinitionRuntimeState        Definition = "RuntimeState"
	DefinitionRuntimeInput        Definition = "RuntimeInput"
	DefinitionRuntimeEffect       Definition = "RuntimeEffect"
	DefinitionRuntimeTransition   Definition = "RuntimeTransition"
	DefinitionConformanceCase     Definition = "ConformanceCase"
	DefinitionWorkerSpawnSpec     Definition = "WorkerSpawnSpec"
	DefinitionWorkerResult        Definition = "WorkerResult"
)

// ValidationError reports a value that does not satisfy a protocol definition.
type ValidationError struct {
	message string
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "validation_failed"
	}
	if e.message == "" {
		return "validation_failed"
	}
	return "validation_failed: " + e.message
}

// Code identifies this error as a protocol validation failure.
func (e *ValidationError) Code() string { return "validation_failed" }

//go:embed protocol.schema.json
var protocolSchema []byte

var (
	validatorsOnce sync.Once
	validators     map[Definition]*jsonschema.Schema
	validatorsErr  error
)

func compiledValidators() (map[Definition]*jsonschema.Schema, error) {
	validatorsOnce.Do(func() {
		var document any
		if err := json.Unmarshal(protocolSchema, &document); err != nil {
			validatorsErr = err
			return
		}

		compiler, err := NewSchemaCompiler()
		if err != nil {
			validatorsErr = err
			return
		}
		if err := compiler.AddResource(protocolSchemaID, document); err != nil {
			validatorsErr = err
			return
		}

		validators = make(map[Definition]*jsonschema.Schema, len(protocolDefinitions))
		for _, definition := range protocolDefinitions {
			validator, err := compiler.Compile(protocolSchemaID + "#/$defs/" + string(definition))
			if err != nil {
				validatorsErr = err
				return
			}
			validators[definition] = validator
		}
	})
	return validators, validatorsErr
}

var protocolDefinitions = []Definition{
	DefinitionAgentError,
	DefinitionAgentScope,
	DefinitionBudget,
	DefinitionCalendarReadInput,
	DefinitionCalendarReadData,
	DefinitionReadonlyRunStart,
	DefinitionReadonlyRunView,
	DefinitionReadonlyRunFinish,
	DefinitionCalendarReadRequest,
	DefinitionUsage,
	DefinitionMessage,
	DefinitionToolSpec,
	DefinitionToolCall,
	DefinitionToolResult,
	DefinitionSkillManifest,
	DefinitionSkillDescriptor,
	DefinitionSkillRef,
	DefinitionSkillActivation,
	DefinitionCapabilitySnapshot,
	DefinitionProviderEvent,
	DefinitionProviderEnvelope,
	DefinitionModelTurnRequest,
	DefinitionRuntimeState,
	DefinitionRuntimeInput,
	DefinitionRuntimeEffect,
	DefinitionRuntimeTransition,
	DefinitionConformanceCase,
	DefinitionWorkerSpawnSpec,
	DefinitionWorkerResult,
}

// Validate checks value against the canonical schema definition.
func Validate(definition Definition, value any) error {
	validators, err := compiledValidators()
	if err != nil {
		return fmt.Errorf("compile agent protocol: %w", err)
	}
	validator, ok := validators[definition]
	if !ok {
		return fmt.Errorf("unknown protocol definition %q", definition)
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return &ValidationError{message: err.Error()}
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return &ValidationError{message: err.Error()}
	}
	if err := validator.Validate(document); err != nil {
		return &ValidationError{message: err.Error()}
	}

	if definition == DefinitionConformanceCase {
		conformanceCase, ok := document.(map[string]any)
		if ok {
			inputs, inputsOK := conformanceCase["inputs"].([]any)
			expected, expectedOK := conformanceCase["expectedTransitions"].([]any)
			if inputsOK && expectedOK && len(inputs) != len(expected) {
				return &ValidationError{message: "ConformanceCase inputs and expectedTransitions must have equal lengths"}
			}
		}
	}

	return nil
}
