import { readFileSync } from "node:fs";

import type { AnySchema } from "ajv";

import type { ToolResult } from "../../src/agent/generated/protocol";
import { createSchemaValidator } from "../../src/agent/protocol/schema";

const schemaID = "https://dayorder.local/schemas/agent/v2/protocol.schema.json";
const schemaURL = new URL(
  "../../src/agent/generated/protocol.schema.json",
  import.meta.url,
);

function loadToolResultValidator() {
  let schema: AnySchema;
  try {
    schema = JSON.parse(readFileSync(schemaURL, "utf8"));
  } catch {
    throw new Error(
      "shared protocol schema could not be loaded for Playwright",
    );
  }

  const ajv = createSchemaValidator();
  ajv.addSchema(schema, schemaID);
  const validator = ajv.getSchema(`${schemaID}#/$defs/ToolResult`);
  if (!validator)
    throw new Error("shared protocol schema is missing ToolResult");
  return validator;
}

const validateSharedToolResult = loadToolResultValidator();

export function validateToolResult(value: unknown): ToolResult {
  if (!validateSharedToolResult(value)) {
    throw new Error(
      "validation_failed: ToolResult does not match the shared protocol schema",
    );
  }
  return value as ToolResult;
}
