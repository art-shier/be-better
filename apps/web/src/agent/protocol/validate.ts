import type { ValidateFunction } from "ajv/dist/2020.js";

import schema from "../generated/protocol.schema.json";
import { createSchemaValidator } from "./schema";

import type {
  AgentError,
  AgentScope,
  Budget,
  CalendarReadData,
  CalendarReadInput,
  CalendarReadRequest,
  CapabilitySnapshot,
  ConformanceCase,
  Message,
  ModelTurnRequest,
  ProviderEnvelope,
  ProviderEvent,
  ReadonlyRunFinish,
  ReadonlyRunStart,
  ReadonlyRunView,
  RuntimeEffect,
  RuntimeInput,
  RuntimeState,
  RuntimeTransition,
  SkillActivation,
  SkillDescriptor,
  SkillManifest,
  SkillRef,
  ToolCall,
  ToolResult,
  ToolSpec,
  Usage,
  WorkerResult,
  WorkerSpawnSpec,
} from "../generated/protocol";

export type ProtocolDefinition =
  | "AgentError"
  | "AgentScope"
  | "Budget"
  | "CalendarReadInput"
  | "CalendarReadData"
  | "ReadonlyRunStart"
  | "ReadonlyRunView"
  | "ReadonlyRunFinish"
  | "CalendarReadRequest"
  | "Usage"
  | "Message"
  | "ToolSpec"
  | "ToolCall"
  | "ToolResult"
  | "SkillManifest"
  | "SkillDescriptor"
  | "SkillRef"
  | "SkillActivation"
  | "CapabilitySnapshot"
  | "ProviderEvent"
  | "ProviderEnvelope"
  | "ModelTurnRequest"
  | "RuntimeState"
  | "RuntimeInput"
  | "RuntimeEffect"
  | "RuntimeTransition"
  | "ConformanceCase"
  | "WorkerSpawnSpec"
  | "WorkerResult";

export type ProtocolValue =
  | AgentError
  | AgentScope
  | Budget
  | CalendarReadInput
  | CalendarReadData
  | ReadonlyRunStart
  | ReadonlyRunView
  | ReadonlyRunFinish
  | CalendarReadRequest
  | Usage
  | Message
  | ToolSpec
  | ToolCall
  | ToolResult
  | SkillManifest
  | SkillDescriptor
  | SkillRef
  | SkillActivation
  | CapabilitySnapshot
  | ProviderEvent
  | ProviderEnvelope
  | ModelTurnRequest
  | RuntimeState
  | RuntimeInput
  | RuntimeEffect
  | RuntimeTransition
  | ConformanceCase
  | WorkerSpawnSpec
  | WorkerResult;

const schemaID = "https://dayorder.local/schemas/agent/v2/protocol.schema.json";
const ajv = createSchemaValidator();
ajv.addSchema(schema, schemaID);
const cache = new Map<string, ValidateFunction>();

export class ProtocolValidationError extends Error {
  readonly code = "validation_failed";
}

export function validateProtocol<T>(definition: ProtocolDefinition, value: unknown): T {
  const validator = cache.get(definition) ?? ajv.getSchema(`${schemaID}#/$defs/${definition}`);
  if (!validator) throw new Error(`missing protocol definition: ${definition}`);
  cache.set(definition, validator);

  if (!validator(value)) {
    throw new ProtocolValidationError(
      `validation_failed: ${ajv.errorsText(validator.errors, { separator: "; " })}`,
    );
  }

  if (definition === "ConformanceCase") {
    const conformanceCase = value as ConformanceCase;
    if (conformanceCase.inputs.length !== conformanceCase.expectedTransitions.length) {
      throw new ProtocolValidationError(
        "validation_failed: ConformanceCase inputs and expectedTransitions must have equal lengths",
      );
    }
  }

  return value as T;
}
