import addFormats from "ajv-formats";

const encoder = new TextEncoder();

export function applyAgentSchemaPolicy(validator) {
  addFormats(validator, ["date-time", "uuid"]);
  validator.addKeyword({
    keyword: "maxUtf8Bytes",
    type: "string",
    schemaType: "number",
    metaSchema: { type: "integer", minimum: 0 },
    validate: (limit, value) => encoder.encode(value).byteLength <= limit,
  });
  return validator;
}
